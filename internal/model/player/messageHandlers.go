package player

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	pb "snake-net-game/pkg/proto"
)

func (p *Player) handleRoleChangeMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	roleChangeMsg := msg.GetRoleChange()

	log.Printf("Player ID %d: Received RoleChangeMsg - senderId=%d, receiverId=%d, senderRole=%v, receiverRole=%v",
		p.Node.PlayerInfo.GetId(), msg.GetSenderId(), msg.GetReceiverId(),
		roleChangeMsg.GetSenderRole(), roleChangeMsg.GetReceiverRole())

	// Проверяем - это сообщение для нас или уведомление о смене мастера
	isForUs := p.Node.PlayerInfo.GetId() != 0 && msg.GetReceiverId() != 0 && msg.GetReceiverId() == p.Node.PlayerInfo.GetId()
	isMasterHandover := roleChangeMsg.GetSenderRole() == pb.NodeRole_VIEWER && roleChangeMsg.GetReceiverRole() == pb.NodeRole_MASTER

	if !isForUs && !isMasterHandover {
		// Это сообщение не для нас и не критическое уведомление, игнорируем
		log.Printf("Player ID %d: Ignoring RoleChange not for us (receiverId=%d, our ID=%d)",
			p.Node.PlayerInfo.GetId(), msg.GetReceiverId(), p.Node.PlayerInfo.GetId())
		return
	}

	newRole := roleChangeMsg.GetReceiverRole()

	// Если это уведомление о смене мастера, обновляем MasterAddr даже если сообщение не нашему ID
	if isMasterHandover && !isForUs {
		newMasterId := msg.GetReceiverId()
		log.Printf("Player ID %d: Master handover detected (Sender ID %d -> Receiver ID %d)",
			p.Node.PlayerInfo.GetId(), msg.GetSenderId(), newMasterId)

		var newMasterAddr *net.UDPAddr
		// Пытаемся найти адрес нового мастера
		if p.Node.State != nil && p.Node.State.Players != nil {
			for _, player := range p.Node.State.Players.Players {
				if player.GetId() == newMasterId {
					if player.GetIpAddress() != "" && player.GetPort() != 0 {
						addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
						if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
							newMasterAddr = resolved
							log.Printf("Found new Master address in state: %v", newMasterAddr)
						}
					}
					break
				}
			}
		}

		// Если в состоянии нет, пробуем источник пакета (если это и есть новый мастер)
		if newMasterAddr == nil && addr != nil && msg.GetSenderId() == newMasterId {
			newMasterAddr = addr
			log.Printf("Using packet source as new Master address: %v", newMasterAddr)
		}

		if newMasterAddr != nil {
			p.MasterAddr = newMasterAddr
			p.Node.MasterAddr = newMasterAddr
			log.Printf("Player ID %d: Switched MasterAddr to %v due to handover", p.Node.PlayerInfo.GetId(), newMasterAddr)
		}
		return
	}

	// Обновляем роль игрока (если сообщение для нас)
	p.Node.PlayerInfo.Role = newRole.Enum()
	log.Printf("Player ID %d: Updated PlayerInfo.Role to %v", p.Node.PlayerInfo.GetId(), newRole)

	// Если переходим в режим VIEWER, обновляем флаг
	if newRole == pb.NodeRole_VIEWER {
		p.IsViewer = true
	}

	// Обновляем роль в состоянии игры, если оно доступно
	if p.Node.State != nil && p.Node.State.Players != nil {
		for _, player := range p.Node.State.Players.Players {
			if player.GetId() == p.Node.PlayerInfo.GetId() {
				player.Role = newRole.Enum()
				break
			}
		}
	}

	switch newRole {
	case pb.NodeRole_DEPUTY:
		log.Printf("Assigned as DEPUTY")
	case pb.NodeRole_MASTER:
		log.Printf("Received MASTER role! Taking over as MASTER...")
		// DEPUTY становится MASTER - нужно вызвать becomeMaster
		go p.becomeMaster()
	case pb.NodeRole_VIEWER:
		log.Printf("Now in VIEWER mode - will continue observing the game")
		p.IsViewer = true
	case pb.NodeRole_NORMAL:
		log.Printf("Assigned as NORMAL player")
	default:
		log.Printf("Received unknown role: %v", newRole)
	}
}

func (p *Player) sendRoleChangeRequest(newRole pb.NodeRole) {
	roleChangeMsg := &pb.GameMessage{
		MsgSeq:   proto.Int64(p.Node.MsgSeq),
		SenderId: proto.Int32(p.Node.PlayerInfo.GetId()),
		Type: &pb.GameMessage_RoleChange{
			RoleChange: &pb.GameMessage_RoleChangeMsg{
				SenderRole:   p.Node.PlayerInfo.GetRole().Enum(),
				ReceiverRole: newRole.Enum(),
			},
		},
	}

	p.Node.SendMessage(roleChangeMsg, p.MasterAddr)
	log.Printf("Player: Sent RoleChangeMsg to %v with new role: %v", p.MasterAddr, newRole)
}
