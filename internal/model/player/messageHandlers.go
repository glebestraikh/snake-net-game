package player

import (
	"google.golang.org/protobuf/proto"
	"log"
	pb "snake-net-game/pkg/proto"
)

func (p *Player) handleRoleChangeMessage(msg *pb.GameMessage) {
	roleChangeMsg := msg.GetRoleChange()
	switch {
	case roleChangeMsg.GetReceiverRole() == pb.NodeRole_DEPUTY:
		// DEPUTY
		p.Node.PlayerInfo.Role = pb.NodeRole_DEPUTY.Enum()
		log.Printf("Assigned as DEPUTY")
	case roleChangeMsg.GetReceiverRole() == pb.NodeRole_MASTER:
		// MASTER
		p.Node.PlayerInfo.Role = pb.NodeRole_MASTER.Enum()
		log.Printf("Assigned as MASTER")
		// TODO: Implement logic to take over as MASTER
	case roleChangeMsg.GetReceiverRole() == pb.NodeRole_VIEWER:
		// VIEWER
		p.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
		log.Printf("Assigned as VIEWER")
	default:
		log.Printf("Received unknown RoleChangeMsg")
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
