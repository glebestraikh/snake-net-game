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
	crashedPlayersToNotify []*net.UDPAddr // Адреса игроков, которым нужно отправить ErrorMsg после освобождения мьютекса
	needNewDeputy          bool           // флаг, что нужно назначить нового DEPUTY после checkCollisions
	needTransferMaster     bool           // флаг, что MASTER умер и нужно передать роль DEPUTY
	observerAddrs          []*net.UDPAddr // адреса убитых игроков, которые продолжают наблюдать за игрой
	stopChan               chan struct{}  // канал для остановки горутин Master
	wg                     sync.WaitGroup // для отслеживания завершения горутин
	stopped                bool           // флаг, что мастер остановлен
}

// NewMaster создает нового мастера
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

// NewMasterFromPlayer создает мастера из существующего игрока (когда DEPUTY становится MASTER)
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

// Start запуск мастера
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

// отправка AnnouncementMsg
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
			// Проверяем флаг stopped перед отправкой
			m.Node.Mu.Lock()
			if m.stopped {
				m.Node.Mu.Unlock()
				log.Printf("Master sendAnnouncementMessage stopped (stopped flag)")
				return
			}
			m.Node.Mu.Unlock()

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
}

// получение мультикаст сообщений
func (m *Master) receiveMulticastMessages() {
	defer m.wg.Done()
	for {
		select {
		case <-m.stopChan:
			log.Printf("Master receiveMulticastMessages stopped")
			return
		default:
		}

		// Устанавливаем короткий таймаут для возможности проверки stopChan
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
	defer m.wg.Done()
	becameViewer := false // Флаг что мы уже стали VIEWER и больше не проверяем stopChan

	for {
		// Проверяем stopChan только если еще не стали VIEWER
		if !becameViewer {
			select {
			case <-m.stopChan:
				// Проверяем роль - если стали VIEWER, продолжаем получать сообщения
				m.Node.Mu.Lock()
				role := m.Node.PlayerInfo.GetRole()
				m.Node.Mu.Unlock()

				if role == pb.NodeRole_VIEWER {
					log.Printf("Master receiveMessages: became VIEWER, continuing to receive messages")
					becameViewer = true // Больше не проверяем stopChan
					// НЕ останавливаемся, продолжаем получать StateMsg
				} else {
					log.Printf("Master receiveMessages stopped")
					return
				}
			default:
			}
		}

		// Устанавливаем короткий таймаут для возможности проверки stopChan
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

		// Логируем тип полученного сообщения
		m.Node.Mu.Lock()
		role := m.Node.PlayerInfo.GetRole()
		m.Node.Mu.Unlock()

		if role == pb.NodeRole_VIEWER {
			log.Printf("Master receiveMessages (VIEWER mode): Received message type %T from %v", msg.Type, addr)
		}

		m.handleMessage(&msg, addr)
	}
}

// обработка юникаст сообщения
func (m *Master) handleMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	if msg.GetSenderId() > 0 {
		m.Node.Mu.Lock()
		m.Node.LastInteraction[msg.GetSenderId()] = time.Now()
		// remember sender address for later (fallback if GamePlayer lacks ip/port)
		if addr != nil {
			m.Node.KnownAddrs[msg.GetSenderId()] = addr
		}
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

		// Если мы стали VIEWER, обновляем наше состояние из полученного StateMsg
		m.Node.Mu.Lock()
		if currentRole == pb.NodeRole_VIEWER {
			// Обновляем состояние игры чтобы UI мог его отображать
			m.Node.State = t.State.GetState()
			log.Printf("Old MASTER (now VIEWER) ID %d: Updated Node.State with %d snakes, %d foods, %d players",
				playerId, len(m.Node.State.Snakes), len(m.Node.State.Foods), len(m.Node.State.Players.Players))
			m.Node.Cond.Broadcast() // Уведомляем UI о новом состоянии
		}
		m.Node.Mu.Unlock()

		m.Node.SendAck(msg, addr)

	default:
		log.Printf("Received unknown message type from %v", addr)
	}
}

// рассылаем всем игрокам состояние игры
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
		// Проверяем, остановлен ли мастер
		if m.stopped {
			m.Node.Mu.Unlock()
			return
		}
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

		// Копируем адреса всех игроков (включая умерших наблюдателей) внутри блока с мьютексом
		var allAddrs []*net.UDPAddr
		for _, player := range m.players.Players {
			if player.GetId() == m.Node.PlayerInfo.GetId() {
				continue
			}
			// prefer address from player record
			if player.GetIpAddress() != "" && player.GetPort() != 0 {
				addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				addr, err := net.ResolveUDPAddr("udp", addrStr)
				if err == nil {
					allAddrs = append(allAddrs, addr)
					continue
				}
			}
			// fallback to KnownAddrs map (last-seen UDP addr)
			if known, ok := m.Node.KnownAddrs[player.GetId()]; ok && known != nil {
				allAddrs = append(allAddrs, known)
			}
		}

		// observerAddrs больше не нужен, так как умершие игроки остаются в m.players.Players

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

		// Build compressed copy of snakes for sending without mutating master state
		var msgSnakes []*pb.GameState_Snake
		for _, snake := range snakesCopy {
			compressed := compressSnakePoints(snake, m.Node.Config.GetWidth(), m.Node.Config.GetHeight())
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

		// Debug: log and send to all players
		log.Printf("Sending StateMsg stateOrder=%d to %d addrs", newStateOrder, len(allAddrs))
		m.sendMessageToAllPlayers(stateMsg, allAddrs)
	}
}

// compressSnakePoints converts a snake represented as a list of absolute coordinates
// (head first, then subsequent body cells) into the protobuf "key points" format:
// first point is absolute head coordinate, each next point is a displacement (either x or y)
// relative to the previous key point. This matches Kotlin client's expectations.
func compressSnakePoints(s *pb.GameState_Snake, width, height int32) *pb.GameState_Snake {
	if s == nil || len(s.GetPoints()) == 0 {
		return s
	}
	pts := s.GetPoints()
	// copy head as absolute
	head := pts[0]
	newPoints := []*pb.GameState_Coord{{X: proto.Int32(head.GetX()), Y: proto.Int32(head.GetY())}}
	if len(pts) == 1 {
		// only head
		return &pb.GameState_Snake{
			PlayerId:      proto.Int32(s.GetPlayerId()),
			Points:        newPoints,
			State:         s.State,
			HeadDirection: s.HeadDirection,
		}
	}

	// helper to compute wrapped delta between prev and cur
	wrapDelta := func(prev, cur *pb.GameState_Coord) (int32, int32) {
		dx := cur.GetX() - prev.GetX()
		dy := cur.GetY() - prev.GetY()
		// normalize with torus (choose minimal displacement)
		if dx > width/2 {
			dx -= width
		} else if dx < -width/2 {
			dx += width
		}
		if dy > height/2 {
			dy -= height
		} else if dy < -height/2 {
			dy += height
		}
		return dx, dy
	}

	// accumulate runs of same axis
	prev := pts[0]
	var accum int32 = 0
	var axisIsX *bool = nil // nil = not initialized; true=x, false=y

	for i := 1; i < len(pts); i++ {
		cur := pts[i]
		dx, dy := wrapDelta(prev, cur)
		// determine this step's axis and value
		var stepIsX bool
		var val int32
		if dx != 0 {
			stepIsX = true
			val = dx
		} else {
			stepIsX = false
			val = dy
		}

		if axisIsX == nil {
			// start new run
			b := stepIsX
			axisIsX = &b
			accum = val
		} else if *axisIsX == stepIsX {
			// same axis, accumulate
			accum += val
		} else {
			// axis changed — flush previous run
			if *axisIsX {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(accum), Y: proto.Int32(0)})
			} else {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(0), Y: proto.Int32(accum)})
			}
			// start new run
			b := stepIsX
			axisIsX = &b
			accum = val
		}
		prev = cur
	}
	// flush final run
	if axisIsX != nil {
		if *axisIsX {
			newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(accum), Y: proto.Int32(0)})
		} else {
			newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(0), Y: proto.Int32(accum)})
		}
	}

	return &pb.GameState_Snake{
		PlayerId:      proto.Int32(s.GetPlayerId()),
		Points:        newPoints,
		State:         s.State,
		HeadDirection: s.HeadDirection,
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
		// prefer explicit player address
		if player.GetIpAddress() != "" && player.GetPort() != 0 {
			addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
			addr, err := net.ResolveUDPAddr("udp", addrStr)
			if err == nil {
				addrs = append(addrs, addr)
				continue
			}
		}
		// fallback to known last-seen address
		if known, ok := m.Node.KnownAddrs[player.GetId()]; ok && known != nil {
			addrs = append(addrs, known)
		}
	}
	return addrs
}

// отправка всем игрокам
func (m *Master) sendMessageToAllPlayers(msg *pb.GameMessage, addrs []*net.UDPAddr) {
	for _, addr := range addrs {
		m.Node.SendMessage(msg, addr)
	}
}

// stopMaster останавливает все master-горутины
func (m *Master) stopMaster() {
	m.Node.Mu.Lock()
	if m.stopped {
		m.Node.Mu.Unlock()
		log.Printf("Master already stopped")
		return
	}
	m.stopped = true
	m.Node.Mu.Unlock()

	log.Printf("Stopping Master goroutines...")
	close(m.stopChan)
	m.wg.Wait()
	log.Printf("All Master goroutines stopped successfully")
}
