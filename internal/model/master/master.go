package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	"snake-net-game/internal/model/common"
	pb "snake-net-game/pkg/proto"
	"time"
)

type Master struct {
	Node *common.Node

	announcement           *pb.GameAnnouncement
	players                *pb.GamePlayers
	lastStateMsg           int32
	crashedPlayersToNotify []*net.UDPAddr // Адреса игроков, которым нужно отправить ErrorMsg после освобождения мьютекса
}

// NewMaster создает нового мастера
func NewMaster(multicastConn *net.UDPConn, config *pb.GameConfig) *Master {
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

	// Создаем змейку мастера длиной 2 клетки
	headX := config.GetWidth() / 2
	headY := config.GetHeight() / 2
	// Хвост слева от головы
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
		HeadDirection: pb.Direction_RIGHT.Enum(), // Движется вправо (противоположно хвосту)
	}

	state.Snakes = append(state.Snakes, masterSnake)

	announcement := &pb.GameAnnouncement{
		Players:  players,
		Config:   config,
		CanJoin:  proto.Bool(true),
		GameName: proto.String("Game1"),
	}

	node := common.NewNode(state, config, multicastConn, unicastConn, masterPlayer)

	return &Master{
		Node:         node,
		announcement: announcement,
		players:      players,
		lastStateMsg: 0,
	}
}

// NewMasterFromPlayer создает мастера из существующего игрока (когда DEPUTY становится MASTER)
func NewMasterFromPlayer(node *common.Node, players *pb.GamePlayers, lastStateMsg int32) *Master {
	announcement := &pb.GameAnnouncement{
		Players:  players,
		Config:   node.Config,
		CanJoin:  proto.Bool(true),
		GameName: proto.String("Game1"),
	}

	return &Master{
		Node:                   node,
		announcement:           announcement,
		players:                players,
		lastStateMsg:           lastStateMsg,
		crashedPlayersToNotify: nil,
	}
}

// Start запуск мастера
func (m *Master) Start() {
	go m.sendAnnouncementMessage()                                         // каждую секунду шлёт Announcement по multicast
	go m.receiveMessages()                                                 // принимает Unicast сообщения (Join, Steer, Ping…)
	go m.receiveMulticastMessages()                                        // принимает DiscoverMsg
	go m.checkTimeouts()                                                   // следит, что игроки не пропали
	go m.sendStateMessage()                                                // пересчитывает мир и шлёт StateMsg
	m.Node.StartResendUnconfirmedMessages(m.Node.Config.GetStateDelayMs()) // переотправка Msg без ACK
	m.Node.StartSendPings(m.Node.Config.GetStateDelayMs())                 // шлёт PING если долго не было unicast
}

// отправка AnnouncementMsg
func (m *Master) sendAnnouncementMessage() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		announcementMsg := &pb.GameMessage{
			MsgSeq: proto.Int64(1),
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

// получение мультикаст сообщений
func (m *Master) receiveMulticastMessages() {
	for {
		buf := make([]byte, 4096)
		n, addr, err := m.Node.MulticastConn.ReadFromUDP(buf)
		if err != nil {
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

// обработка мультикаст сообщений
func (m *Master) handleMulticastMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	switch msg.Type.(type) {
	case *pb.GameMessage_Discover:
		// пришел DiscoverMsg отправляем AnnouncementMsg
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

// получение юникаст сообщений
func (m *Master) receiveMessages() {
	for {
		buf := make([]byte, 4096)
		n, addr, err := m.Node.UnicastConn.ReadFromUDP(buf)
		if err != nil {
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
		m.handleMessage(&msg, addr)
	}
}

// обработка юникаст сообщения
func (m *Master) handleMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	if msg.GetSenderId() > 0 {
		m.Node.Mu.Lock()
		m.Node.LastInteraction[msg.GetSenderId()] = time.Now()
		m.Node.Mu.Unlock()
	}
	switch t := msg.Type.(type) {
	case *pb.GameMessage_Join:
		// проверяем есть ли место 5*5 для новой змеи
		m.Node.Mu.Lock()
		hasSquare, coord := m.hasFreeSquare(m.Node.State, m.Node.Config, 5)
		m.Node.Mu.Unlock()

		if !hasSquare {
			m.announcement.CanJoin = proto.Bool(false)
			m.handleErrorMsg(addr)
			log.Printf("Player cannot join: no available space")
			m.Node.SendAck(msg, addr)
		} else {
			// обрабатываем joinMsg
			joinMsg := t.Join
			m.handleJoinMessage(msg.GetMsgSeq(), joinMsg, addr, coord)
		}

	case *pb.GameMessage_Discover:
		m.handleDiscoverMessage(addr)

	case *pb.GameMessage_Steer:
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
		m.handleRoleChangeMessage(msg, addr)
		m.Node.SendAck(msg, addr)

	case *pb.GameMessage_Ping:
		m.Node.SendAck(msg, addr)

	case *pb.GameMessage_Ack:
		m.Node.AckChan <- msg.GetMsgSeq()

	case *pb.GameMessage_State:
		if t.State.GetState().GetStateOrder() <= m.lastStateMsg {
			return
		} else {
			m.lastStateMsg = t.State.GetState().GetStateOrder()
		}
		m.Node.SendAck(msg, addr)

	default:
		log.Printf("Received unknown message type from %v", addr)
	}
}

// рассылаем всем игрокам состояние игры
func (m *Master) sendStateMessage() {
	ticker := time.NewTicker(time.Duration(m.Node.Config.GetStateDelayMs()) * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		m.Node.Mu.Lock()
		m.GenerateFood()
		m.UpdateGameState()

		newStateOrder := m.Node.State.GetStateOrder() + 1
		m.Node.State.StateOrder = proto.Int32(newStateOrder)

		// ВАЖНО: Синхронизируем m.players с m.Node.State.Players
		// Это гарантирует, что все счета актуальны перед отправкой
		m.Node.State.Players = m.players

		// Копируем данные состояния и адреса игроков перед освобождением мьютекса
		snakesCopy := m.Node.State.GetSnakes()
		foodsCopy := m.Node.State.GetFoods()
		playersCopy := m.Node.State.GetPlayers()

		// Копируем адреса игроков внутри блока с мьютексом
		var allAddrs []*net.UDPAddr
		for _, player := range m.players.Players {
			if player.GetId() == m.Node.PlayerInfo.GetId() {
				continue
			}
			addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
			addr, err := net.ResolveUDPAddr("udp", addrStr)
			if err != nil {
				log.Printf("Error resolving UDP address for player ID %d: %v", player.GetId(), err)
				continue
			}
			allAddrs = append(allAddrs, addr)
		}

		// Копируем адреса упавших игроков для отправки ErrorMsg
		crashedPlayers := m.crashedPlayersToNotify
		m.crashedPlayersToNotify = nil
		m.Node.Mu.Unlock()

		// Отправляем ErrorMsg упавшим игрокам (после освобождения мьютекса)
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

		stateMsg := &pb.GameMessage{
			MsgSeq: proto.Int64(m.Node.MsgSeq),
			Type: &pb.GameMessage_State{
				State: &pb.GameMessage_StateMsg{
					State: &pb.GameState{
						StateOrder: proto.Int32(newStateOrder),
						Snakes:     snakesCopy,
						Foods:      foodsCopy,
						Players:    playersCopy,
					},
				},
			},
		}
		m.sendMessageToAllPlayers(stateMsg, allAddrs)
	}
}

// получение списка адресов всех игроков (кроме мастера)
func (m *Master) getAllPlayersUDPAddrs() []*net.UDPAddr {
	var addrs []*net.UDPAddr
	for _, player := range m.players.Players {
		// Исключаем самого мастера из списка получателей
		if player.GetId() == m.Node.PlayerInfo.GetId() {
			continue
		}
		addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
		addr, err := net.ResolveUDPAddr("udp", addrStr)
		if err != nil {
			log.Printf("Error resolving UDP address for player ID %d: %v", player.GetId(), err)
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// отправка всем игрокам
func (m *Master) sendMessageToAllPlayers(msg *pb.GameMessage, addrs []*net.UDPAddr) {
	for _, addr := range addrs {
		m.Node.SendMessage(msg, addr)
	}
}
