package player

import (
	"google.golang.org/protobuf/proto"
	"log"
	pb "snake-net-game/pkg/proto"
)

func (p *Player) handleRoleChangeMessage(msg *pb.GameMessage) {
	roleChangeMsg := msg.GetRoleChange()

	// Проверяем - это сообщение для нас (receiverId) или о ком-то другом
	if msg.GetReceiverId() != 0 && msg.GetReceiverId() != p.Node.PlayerInfo.GetId() {
		// Это сообщение не для нас, игнорируем
		log.Printf("Received RoleChange message not for us (receiverId=%d, our ID=%d)",
			msg.GetReceiverId(), p.Node.PlayerInfo.GetId())
		return
	}

	newRole := roleChangeMsg.GetReceiverRole()

	// Обновляем роль игрока
	p.Node.PlayerInfo.Role = newRole.Enum()

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
