package player

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	"os"
	"snake-net-game/internal/model/common"
	pb "snake-net-game/pkg/proto"
	"time"
)

type DiscoveredGame struct {
	Players         *pb.GamePlayers
	Config          *pb.GameConfig
	CanJoin         bool
	GameName        string
	AnnouncementMsg *pb.GameMessage_AnnouncementMsg
	MasterAddr      *net.UDPAddr
}

type Player struct {
	Node *common.Node

	AnnouncementMsg *pb.GameMessage_AnnouncementMsg
	MasterAddr      *net.UDPAddr
	LastStateMsg    int32

	haveId bool

	DiscoveredGames []DiscoveredGame
}

func NewPlayer(multicastConn *net.UDPConn) *Player {
	// создаем сокет для остальных сообщений
	localAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		log.Fatalf("Error resolving local UDP address: %v", err)
	}
	unicastConn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Fatalf("Error creating unicast socket: %v", err)
	}

	playerIP, err := common.GetLocalIP()
	if err != nil {
		log.Fatalf("Error getting local IP: %v", err)
	}
	playerPort := unicastConn.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("Выделенный локальный адрес: %s:%v\n", playerIP, playerPort)

	playerInfo := &pb.GamePlayer{
		Name:      proto.String("Player"),
		Id:        proto.Int32(0),
		Role:      pb.NodeRole_NORMAL.Enum(),
		Type:      pb.PlayerType_HUMAN.Enum(),
		Score:     proto.Int32(0),
		IpAddress: proto.String(playerIP),
		Port:      proto.Int32(int32(playerPort)),
	}

	node := common.NewNode(nil, nil, multicastConn, unicastConn, playerInfo)

	return &Player{
		Node:            node,
		AnnouncementMsg: nil,
		MasterAddr:      nil,
		LastStateMsg:    0,

		haveId: false,

		DiscoveredGames: []DiscoveredGame{},
	}
}

func (p *Player) Start() {
	p.discoverGames()
	go p.receiveMessages()
	go p.Node.ResendUnconfirmedMessages(p.Node.Config.GetStateDelayMs())
	go p.Node.SendPings(p.Node.Config.GetStateDelayMs())
}

func (p *Player) ReceiveMulticastMessages() {
	for {
		buf := make([]byte, 4096)
		n, addr, err := p.Node.MulticastConn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("Error receiving multicast message: %v", err)
			continue
		}

		var msg pb.GameMessage
		err = proto.Unmarshal(buf[:n], &msg)
		if err != nil {
			log.Printf("Error unmarshaling multicast message: %v", err)
			continue
		}

		p.handleMulticastMessage(&msg, addr)
	}
}

func (p *Player) handleMulticastMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	switch t := msg.Type.(type) {
	case *pb.GameMessage_Announcement:

		for _, game := range t.Announcement.Games {
			p.addDiscoveredGame(game, addr, t.Announcement)
		}
	default:
	}
}

func (p *Player) addDiscoveredGame(announcement *pb.GameAnnouncement, addr *net.UDPAddr, announcementMsg *pb.GameMessage_AnnouncementMsg) {
	for _, game := range p.DiscoveredGames {
		if game.GameName == announcement.GetGameName() {
			return
		}
	}

	newGame := DiscoveredGame{
		Players:         announcement.GetPlayers(),
		Config:          announcement.GetConfig(),
		CanJoin:         announcement.GetCanJoin(),
		GameName:        announcement.GetGameName(),
		AnnouncementMsg: announcementMsg,
		MasterAddr:      addr,
	}

	p.DiscoveredGames = append(p.DiscoveredGames, newGame)
	log.Printf("Discovered new game: '%s'", announcement.GetGameName())
}

func (p *Player) receiveMessages() {
	for {
		buf := make([]byte, 4096)
		n, addr, err := p.Node.UnicastConn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("Error receiving message: %v", err)
			continue
		}

		var msg pb.GameMessage
		err = proto.Unmarshal(buf[:n], &msg)
		if err != nil {
			continue
		}

		p.handleMessage(&msg, addr)
	}
}

func (p *Player) handleMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	p.Node.Mu.Lock()
	p.Node.LastInteraction[msg.GetSenderId()] = time.Now()
	switch t := msg.Type.(type) {
	case *pb.GameMessage_Ack:
		if !p.haveId {
			p.Node.PlayerInfo.Id = proto.Int32(msg.GetReceiverId())
			log.Printf("Joined game with ID: %d", p.Node.PlayerInfo.GetId())
			p.haveId = true
		}
		p.Node.AckChan <- msg.GetMsgSeq()
		p.Node.Mu.Unlock()
		return
	case *pb.GameMessage_Announcement:
		p.MasterAddr = addr
		p.Node.MasterAddr = addr
		p.AnnouncementMsg = t.Announcement
		p.Node.Mu.Unlock()
		log.Printf("Received AnnouncementMsg from %v via unicast", addr)
		p.sendJoinRequest()
		return
	case *pb.GameMessage_State:
		stateOrder := t.State.GetState().GetStateOrder()
		if stateOrder <= p.LastStateMsg {
			p.Node.Mu.Unlock()
			return
		}
		p.LastStateMsg = stateOrder
		p.Node.State = t.State.GetState()
		p.Node.Cond.Broadcast()
		p.Node.Mu.Unlock()
		// SendAck вызываем БЕЗ мьютекса
		p.Node.SendAck(msg, addr)
		return
	case *pb.GameMessage_Error:
		errorMsg := t.Error.GetErrorMessage()
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		if errorMsg == "You have crashed and been removed from the game. Exiting..." {
			log.Printf("Received crash notification: %s", errorMsg)
			os.Exit(0)
		}
		return
	case *pb.GameMessage_RoleChange:
		p.handleRoleChangeMessage(msg)
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		return
	case *pb.GameMessage_Ping:
		// Отправляем AckMsg в ответ БЕЗ мьютекса
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		return
	default:
		log.Printf("Received unknown message")
		p.Node.Mu.Unlock()
	}
}

// DiscoverGames игрок ищет доступные игры
func (p *Player) discoverGames() {
	discoverMsg := &pb.GameMessage{
		MsgSeq: proto.Int64(p.Node.MsgSeq),
		Type: &pb.GameMessage_Discover{
			Discover: &pb.GameMessage_DiscoverMsg{},
		},
	}

	multicastAddr, err := net.ResolveUDPAddr("udp", p.Node.MulticastAddress)
	if err != nil {
		log.Fatalf("Error resolving multicast address: %v", err)
		return
	}

	p.Node.SendMessage(discoverMsg, multicastAddr)
	log.Printf("Player: Sent DiscoverMsg to multicast address %v", multicastAddr)
}

func (p *Player) sendJoinRequest() {
	if p.AnnouncementMsg == nil || len(p.AnnouncementMsg.Games) == 0 {
		log.Printf("Player: No available games to join")
		return
	}

	joinMsg := &pb.GameMessage{
		MsgSeq: proto.Int64(p.Node.MsgSeq),
		Type: &pb.GameMessage_Join{
			Join: &pb.GameMessage_JoinMsg{
				PlayerType:    pb.PlayerType_HUMAN.Enum(),
				PlayerName:    p.Node.PlayerInfo.Name,
				GameName:      proto.String(p.AnnouncementMsg.Games[0].GetGameName()),
				RequestedRole: pb.NodeRole_NORMAL.Enum(),
			},
		},
	}

	p.Node.SendMessage(joinMsg, p.MasterAddr)
	log.Printf("Player: Sent JoinMsg to master at %v", p.MasterAddr)
}

// обработка отвалившихся узлов
//func (p *Player) checkTimeouts() {
//	ticker := time.NewTicker(time.Duration(0.8*float64(p.node.Config.GetStateDelayMs())) * time.Millisecond)
//	defer ticker.Stop()
//
//	for range ticker.C {
//		now := time.Now()
//		p.node.Mu.Lock()
//		for _, lastInteraction := range p.node.LastInteraction {
//			// TODO: добавить проверку на то что мастер отвалился
//			if now.Sub(lastInteraction) > time.Duration(0.8*float64(p.node.Config.GetStateDelayMs()))*time.Millisecond {
//				switch p.node.PlayerInfo.GetRole() {
//				// игрок заметил, что мастер отвалился и переходит к Deputy
//				case pb.NodeRole_NORMAL:
//					deputy := p.getDeputy()
//					if deputy != nil {
//						addrStr := fmt.Sprintf("%s:%d", deputy.GetIpAddress(), deputy.GetPort())
//						addr, err := net.ResolveUDPAddr("udp", addrStr)
//						if err != nil {
//							log.Printf("Error resolving deputy address: %v", err)
//							p.node.Mu.Unlock()
//							continue
//						}
//						p.MasterAddr = addr
//						log.Printf("Switched to DEPUTY as new MASTER at %v", p.MasterAddr)
//					} else {
//						log.Printf("No DEPUTY available to switch to")
//					}
//
//				// Deputy заметил, что отвалился мастер и заменяет его
//				case pb.NodeRole_DEPUTY:
//					p.becomeMaster()
//				}
//			}
//		}
//		p.node.Mu.Unlock()
//	}
//}
//
//func (p *Player) getDeputy() *pb.GamePlayer {
//	for _, player := range p.node.State.Players.Players {
//		if player.GetRole() == pb.NodeRole_DEPUTY {
//			return player
//		}
//	}
//	return nil
//}
//
//func (p *Player) becomeMaster() {
//	log.Printf("DEPUTY becoming new MASTER")
//	// Обновляем роль игрока
//	p.node.PlayerInfo.Role = pb.NodeRole_MASTER.Enum()
//
//	// Создаем новый мастер
//	masterNode := master.NewDeputyMaster(p.node, p.node.PlayerInfo, p.LastStateMsg)
//	// Запускаем мастер
//	go masterNode.Start()
//	// Останавливаем функции игрока
//	p.stopPlayerFunctions()
//}
