package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	"snake-net-game/internal/model/common"
	pb "snake-net-game/pkg/proto"
	"sync"
	"time"
)

type Master struct {
	Node *common.Node

	announcement           *pb.GameAnnouncement
	players                *pb.GamePlayers
	lastStateMsg           int32
	crashedPlayersToNotify []*net.UDPAddr
	needNewDeputy          bool
	needTransferMaster     bool
	observerAddrs          []*net.UDPAddr
	stopChan               chan struct{}
	wg                     sync.WaitGroup
	stopped                bool
}

func NewMaster(multicastConn *net.UDPConn, config *pb.GameConfig, gameName string) *Master {
	localAddr, err := net.ResolveUDPAddr("udp4", ":0")
	if err != nil {
		log.Fatalf("Error resolving local UDP address: %v", err)
	}
	unicastConn, err := net.ListenUDP("udp4", localAddr)
	if err != nil {
		log.Fatalf("Error creating unicast socket: %v", err)
	}

	masterIP, err := common.GetLocalIP()
	if err != nil {
		log.Fatalf("Error getting local IP: %v", err)
	}
	masterPort := unicastConn.LocalAddr().(*net.UDPAddr).Port
	log.Printf("Выделенный локальный адрес: %s:%v\n", masterIP, masterPort)

	masterPlayer := &pb.GamePlayer{
		Name:      proto.String("Master"),
		Id:        proto.Int32(1),
		Role:      pb.NodeRole_MASTER.Enum(),
		Type:      pb.PlayerType_HUMAN.Enum(),
		Score:     proto.Int32(0),
		IpAddress: proto.String(masterIP),
		Port:      proto.Int32(int32(masterPort)),
	}

	players := &pb.GamePlayers{
		Players: []*pb.GamePlayer{masterPlayer},
	}

	state := &pb.GameState{
		StateOrder: proto.Int32(1),
		Snakes:     []*pb.GameState_Snake{},
		Foods:      []*pb.GameState_Coord{},
		Players:    players,
	}

	headX := config.GetWidth() / 2
	headY := config.GetHeight() / 2
	tailX := (headX - 1 + config.GetWidth()) % config.GetWidth()
	tailY := headY

	masterSnake := &pb.GameState_Snake{
		PlayerId: proto.Int32(masterPlayer.GetId()),
		Points: []*pb.GameState_Coord{
			{
				X: proto.Int32(headX),
				Y: proto.Int32(headY),
			},
			{
				X: proto.Int32(tailX),
				Y: proto.Int32(tailY),
			},
		},
		State:         pb.GameState_Snake_ALIVE.Enum(),
		HeadDirection: pb.Direction_RIGHT.Enum(),
	}

	state.Snakes = append(state.Snakes, masterSnake)

	announcement := &pb.GameAnnouncement{
		Players:  players,
		Config:   config,
		CanJoin:  proto.Bool(true),
		GameName: proto.String(gameName),
	}

	node := common.NewNode(state, config, multicastConn, unicastConn, masterPlayer)

	return &Master{
		Node:         node,
		announcement: announcement,
		players:      players,
		lastStateMsg: 0,
		stopChan:     make(chan struct{}),
		stopped:      false,
	}
}

func NewMasterFromPlayer(node *common.Node, players *pb.GamePlayers, lastStateMsg int32, gameName string) *Master {
	announcement := &pb.GameAnnouncement{
		Players:  players,
		Config:   node.Config,
		CanJoin:  proto.Bool(true),
		GameName: proto.String(gameName),
	}

	return &Master{
		Node:                   node,
		announcement:           announcement,
		players:                players,
		lastStateMsg:           lastStateMsg,
		crashedPlayersToNotify: nil,
		stopChan:               make(chan struct{}),
		stopped:                false,
	}
}

func (m *Master) Start() {
	m.wg.Add(5)                                                            // 5 горутин мастера
	go m.sendAnnouncementMessage()                                         // каждую секунду шлёт Announcement по multicast
	go m.receiveMessages()                                                 // принимает Unicast сообщения (Join, Steer, Ping…)
	go m.receiveMulticastMessages()                                        // принимает DiscoverMsg
	go m.checkTimeouts()                                                   // следит, что игроки не пропали
	go m.sendStateMessage()                                                // пересчитывает мир и шлёт StateMsg
	m.Node.StartResendUnconfirmedMessages(m.Node.Config.GetStateDelayMs()) // переотправка Msg без ACK
	m.Node.StartSendPings(m.Node.Config.GetStateDelayMs())                 // шлёт PING если долго не было unicast
}

func (m *Master) sendAnnouncementMessage() {
	defer m.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			log.Printf("Master sendAnnouncementMessage stopped")
			return
		case <-ticker.C:
			m.Node.Mu.Lock()
			currentRole := m.Node.PlayerInfo.GetRole()
			if m.stopped || currentRole != pb.NodeRole_MASTER {
				m.Node.Mu.Unlock()
				log.Printf("Master sendAnnouncementMessage stopped (stopped flag or transition to %v)", currentRole)
				return
			}
			m.Node.Mu.Unlock()

			announcementMsg := &pb.GameMessage{
				MsgSeq: proto.Int64(0),
				Type: &pb.GameMessage_Announcement{
					Announcement: &pb.GameMessage_AnnouncementMsg{
						Games: []*pb.GameAnnouncement{m.announcement},
					},
				},
			}
			multicastAddr, err := net.ResolveUDPAddr("udp", m.Node.MulticastAddress)
			if err != nil {
				log.Fatalf("Error resolving multicast address: %v", err)
			}
			m.Node.SendMessage(announcementMsg, multicastAddr)
		}
	}
}

func (m *Master) receiveMulticastMessages() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stopChan:
			log.Printf("Master receiveMulticastMessages stopped")
			return
		default:
		}

		_ = m.Node.MulticastConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		buf := make([]byte, 4096)
		n, addr, err := m.Node.MulticastConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Таймаут - нормальное поведение
			}
			log.Printf("Error receiving multicast message: %v", err)
			continue
		}

		var msg pb.GameMessage
		err = proto.Unmarshal(buf[:n], &msg)
		if err != nil {
			log.Printf("Error unmarshalling multicast message: %v", err)
			continue
		}

		m.handleMulticastMessage(&msg, addr)
	}
}

func (m *Master) handleMulticastMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	switch msg.Type.(type) {
	case *pb.GameMessage_Discover:
		announcementMsg := &pb.GameMessage{
			MsgSeq: proto.Int64(m.Node.MsgSeq),
			Type: &pb.GameMessage_Announcement{
				Announcement: &pb.GameMessage_AnnouncementMsg{
					Games: []*pb.GameAnnouncement{m.announcement},
				},
			},
		}
		m.Node.SendMessage(announcementMsg, addr)
	default:
	}
}

func (m *Master) receiveMessages() {
	defer m.wg.Done()
	becameViewer := false

	for {
		if !becameViewer {
			select {
			case <-m.stopChan:
				m.Node.Mu.Lock()
				role := m.Node.PlayerInfo.GetRole()
				m.Node.Mu.Unlock()

				if role == pb.NodeRole_VIEWER {
					log.Printf("Master receiveMessages: became VIEWER, continuing to receive messages")
					becameViewer = true
				} else {
					log.Printf("Master receiveMessages stopped")
					return
				}
			default:
			}
		}

		_ = m.Node.UnicastConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		buf := make([]byte, 4096)
		n, addr, err := m.Node.UnicastConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Таймаут - нормальное поведение
			}
			log.Printf("Error receiving message: %v", err)
			continue
		}

		var msg pb.GameMessage
		err = proto.Unmarshal(buf[:n], &msg)
		if err != nil {
			log.Printf("Error unmarshalling message: %v", err)
			continue
		}

		if m.Node.PlayerInfo.GetIpAddress() == addr.IP.String() && m.Node.PlayerInfo.GetPort() == int32(addr.Port) {
			log.Printf("Get msg from itself")
			continue
		}

		m.Node.Mu.Lock()
		role := m.Node.PlayerInfo.GetRole()
		m.Node.Mu.Unlock()

		if role == pb.NodeRole_VIEWER {
			log.Printf("Master receiveMessages (VIEWER mode): Received message type %T from %v", msg.Type, addr)
		}

		m.handleMessage(&msg, addr)
	}
}

func (m *Master) handleMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	if msg.GetSenderId() > 0 {
		m.Node.Mu.Lock()
		m.Node.LastInteraction[msg.GetSenderId()] = time.Now()
		if addr != nil {
			m.Node.KnownAddrs[msg.GetSenderId()] = addr
		}
		m.Node.Mu.Unlock()
	}
	switch t := msg.Type.(type) {
	case *pb.GameMessage_Join:
		joinMsg := t.Join
		m.handleJoinMessage(msg.GetMsgSeq(), joinMsg, addr)

	case *pb.GameMessage_Discover:
		m.Node.Mu.Lock()
		if m.stopped || m.Node.PlayerInfo.GetRole() != pb.NodeRole_MASTER {
			m.Node.Mu.Unlock()
			return
		}
		m.Node.Mu.Unlock()
		m.handleDiscoverMessage(addr)

	case *pb.GameMessage_Steer:
		m.Node.Mu.Lock()
		if m.stopped || m.Node.PlayerInfo.GetRole() != pb.NodeRole_MASTER {
			m.Node.Mu.Unlock()
			return
		}
		m.Node.Mu.Unlock()
		playerId := msg.GetSenderId()
		if playerId == 0 {
			playerId = m.Node.GetPlayerIdByAddress(addr)
		}
		if playerId != 0 {
			m.handleSteerMessage(t.Steer, playerId)
			m.Node.SendAck(msg, addr)
		} else {
			log.Printf("SteerMsg received from unknown address: %v", addr)
		}

	case *pb.GameMessage_RoleChange:
		m.handleRoleChangeMessage(msg)
		m.Node.SendAck(msg, addr)

	case *pb.GameMessage_Ping:
		m.Node.Mu.Lock()
		currentRole := m.Node.PlayerInfo.GetRole()
		m.Node.Mu.Unlock()
		if currentRole == pb.NodeRole_MASTER {
			m.Node.SendAck(msg, addr)
		} else {
			log.Printf("Ignoring PingMsg in %v mode", currentRole)
		}

	case *pb.GameMessage_Ack:
		select {
		case m.Node.AckChan <- msg.GetMsgSeq():
		default:
			log.Printf("Warning: Master AckChan full, dropping ACK info for msg seq %d", msg.GetMsgSeq())
		}

	case *pb.GameMessage_State:
		stateOrder := t.State.GetState().GetStateOrder()

		m.Node.Mu.Lock()
		currentRole := m.Node.PlayerInfo.GetRole()
		playerId := m.Node.PlayerInfo.GetId()
		m.Node.Mu.Unlock()

		log.Printf("Master ID %d (role=%v) received StateMsg stateOrder=%d (lastStateMsg=%d)",
			playerId, currentRole, stateOrder, m.lastStateMsg)

		if stateOrder <= m.lastStateMsg {
			log.Printf("Ignoring old StateMsg (stateOrder=%d <= lastStateMsg=%d)", stateOrder, m.lastStateMsg)
			return
		}
		m.lastStateMsg = stateOrder

		m.Node.Mu.Lock()
		if currentRole == pb.NodeRole_VIEWER {
			m.Node.State = t.State.GetState()
			log.Printf("Old MASTER (now VIEWER) ID %d: Updated state from Master %v. Snakes: %d, Foods: %d",
				playerId, addr, len(m.Node.State.Snakes), len(m.Node.State.Foods))
			m.Node.Cond.Broadcast()
		}
		m.Node.Mu.Unlock()

		m.Node.SendAck(msg, addr)

	default:
		log.Printf("Received unknown message type from %v", addr)
	}
}

func (m *Master) sendStateMessage() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Duration(m.Node.Config.GetStateDelayMs()) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			log.Printf("Master sendStateMessage stopped")
			return
		case <-ticker.C:
		}

		m.Node.Mu.Lock()
		if m.stopped {
			m.Node.Mu.Unlock()
			return
		}
		m.GenerateFood()
		m.UpdateGameState()

		newStateOrder := m.Node.State.GetStateOrder() + 1
		m.Node.State.StateOrder = proto.Int32(newStateOrder)

		m.Node.State.Players = m.players

		snakesCopy := m.Node.State.GetSnakes()
		foodsCopy := m.Node.State.GetFoods()
		playersCopy := m.Node.State.GetPlayers()

		var allAddrs []*net.UDPAddr
		for _, player := range m.players.Players {
			if player.GetId() == m.Node.PlayerInfo.GetId() {
				continue
			}
			if player.GetIpAddress() != "" && player.GetPort() != 0 {
				addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				addr, err := net.ResolveUDPAddr("udp", addrStr)
				if err == nil {
					allAddrs = append(allAddrs, addr)
					continue
				}
			}
			if known, ok := m.Node.KnownAddrs[player.GetId()]; ok && known != nil {
				allAddrs = append(allAddrs, known)
			}
		}

		crashedPlayers := m.crashedPlayersToNotify
		m.crashedPlayersToNotify = nil
		m.Node.Mu.Unlock()

		for _, crashedAddr := range crashedPlayers {
			errorMsg := &pb.GameMessage{
				MsgSeq: proto.Int64(m.Node.MsgSeq),
				Type: &pb.GameMessage_Error{
					Error: &pb.GameMessage_ErrorMsg{
						ErrorMessage: proto.String("You have crashed and been removed from the game. Exiting..."),
					},
				},
			}
			m.Node.SendMessage(errorMsg, crashedAddr)
		}

		var msgSnakes []*pb.GameState_Snake
		for _, snake := range snakesCopy {
			compressed := common.CompressSnake(snake, m.Node.Config.GetWidth(), m.Node.Config.GetHeight())
			msgSnakes = append(msgSnakes, compressed)
		}

		stateMsg := &pb.GameMessage{
			MsgSeq: proto.Int64(m.Node.MsgSeq),
			Type: &pb.GameMessage_State{
				State: &pb.GameMessage_StateMsg{
					State: &pb.GameState{
						StateOrder: proto.Int32(newStateOrder),
						Snakes:     msgSnakes,
						Foods:      foodsCopy,
						Players:    playersCopy,
					},
				},
			},
		}

		m.sendMessageToAllPlayers(stateMsg, allAddrs)
	}
}

func (m *Master) sendMessageToAllPlayers(msg *pb.GameMessage, addrs []*net.UDPAddr) {
	for _, addr := range addrs {
		m.Node.SendMessage(msg, addr)
	}
}
