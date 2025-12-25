package player

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	"snake-net-game/internal/model/common"
	"snake-net-game/internal/model/master"
	pb "snake-net-game/pkg/proto"
	"sync"
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

	haveId                bool
	joinRequestSent       bool // флаг, что JoinRequest уже отправлен
	nodeGoroutinesStarted bool // флаг, что горутины Node уже запущены
	IsViewer              bool // true если игрок присоединяется как наблюдатель

	DiscoveredGames []DiscoveredGame

	stopChan chan struct{}  // канал для остановки горутин при переходе в MASTER
	wg       sync.WaitGroup // для отслеживания завершения горутин Player
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

		haveId:                false,
		joinRequestSent:       false,
		nodeGoroutinesStarted: false,

		DiscoveredGames: []DiscoveredGame{},
		stopChan:        make(chan struct{}),
	}
}

func (p *Player) Start() {
	p.discoverGames()
	p.wg.Add(2) // Для receiveMessages и checkTimeouts
	go p.receiveMessagesLoop()
	go p.checkTimeoutsLoop()

	// Запускаем горутины Node только если Config уже установлен
	p.Node.Mu.Lock()
	if p.Node.Config != nil && !p.nodeGoroutinesStarted {
		p.nodeGoroutinesStarted = true
		p.Node.Mu.Unlock()
		p.Node.StartResendUnconfirmedMessages(p.Node.Config.GetStateDelayMs())
		p.Node.StartSendPings(p.Node.Config.GetStateDelayMs())
		log.Printf("Started Node goroutines with stateDelayMs=%d", p.Node.Config.GetStateDelayMs())
	} else {
		p.Node.Mu.Unlock()
		log.Printf("Config is nil, will start Node goroutines after receiving Config")
	}

	// Если контроллер уже установил AnnouncementMsg и MasterAddr до вызова Start (например, при JoinGame),
	// отправляем JoinRequest сразу.
	p.Node.Mu.Lock()
	alreadyHaveAnnouncement := p.AnnouncementMsg != nil && p.MasterAddr != nil
	alreadySent := p.joinRequestSent
	p.Node.Mu.Unlock()

	if alreadyHaveAnnouncement && !alreadySent {
		log.Printf("Announcement already provided before Start; sending JoinRequest immediately")
		p.sendJoinRequest()
	}
}

func (p *Player) ReceiveMulticastMessages() {
	for {
		// Проверяем stopChan для корректного завершения
		select {
		case <-p.stopChan:
			log.Printf("Player ReceiveMulticastMessages stopped via stopChan")
			return
		default:
		}

		// Проверяем роль - если стали MASTER, выходим из горутины
		// так как Master запустит свою собственную receiveMulticastMessages
		p.Node.Mu.Lock()
		role := p.Node.PlayerInfo.GetRole()
		p.Node.Mu.Unlock()

		if role == pb.NodeRole_MASTER {
			log.Printf("Player became MASTER, stopping ReceiveMulticastMessages")
			return
		}

		// Устанавливаем короткий таймаут для возможности проверки stopChan
		_ = p.Node.MulticastConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		buf := make([]byte, 4096)
		n, addr, err := p.Node.MulticastConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Таймаут - нормальное поведение, просто проверяем stopChan снова
			}
			// Логируем только реальные ошибки, не таймауты
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
	// Проверяем, есть ли уже игра от этого мастера (по адресу)
	// Определяем мастер-адрес из объявления, если он там указан
	var masterAddr *net.UDPAddr
	if announcement != nil && announcement.GetPlayers() != nil && len(announcement.GetPlayers().GetPlayers()) > 0 {
		masterPlayer := announcement.GetPlayers().GetPlayers()[0]
		if masterPlayer != nil && masterPlayer.GetIpAddress() != "" && masterPlayer.GetPort() != 0 {
			addrStr := fmt.Sprintf("%s:%d", masterPlayer.GetIpAddress(), masterPlayer.GetPort())
			if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
				masterAddr = resolved
			}
		}
	}
	// если не получилось получить адрес из объявления — используем источник пакета
	if masterAddr == nil && addr != nil {
		masterAddr = addr
	}

	for i, game := range p.DiscoveredGames {
		if game.MasterAddr != nil && masterAddr != nil && game.MasterAddr.String() == masterAddr.String() {
			// Обновляем информацию об игре от этого мастера
			p.DiscoveredGames[i].Players = announcement.GetPlayers()
			p.DiscoveredGames[i].Config = announcement.GetConfig()
			p.DiscoveredGames[i].CanJoin = announcement.GetCanJoin()
			p.DiscoveredGames[i].GameName = announcement.GetGameName()
			p.DiscoveredGames[i].AnnouncementMsg = announcementMsg
			p.DiscoveredGames[i].MasterAddr = masterAddr
			return
		}
	}

	newGame := DiscoveredGame{
		Players:         announcement.GetPlayers(),
		Config:          announcement.GetConfig(),
		CanJoin:         announcement.GetCanJoin(),
		GameName:        announcement.GetGameName(),
		AnnouncementMsg: announcementMsg,
		MasterAddr:      masterAddr,
	}

	p.DiscoveredGames = append(p.DiscoveredGames, newGame)
	log.Printf("Discovered new game: '%s' from %s", announcement.GetGameName(), masterAddr.String())
}

func (p *Player) receiveMessagesLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.stopChan:
			log.Printf("Player receiveMessages stopped")
			// Убираем дедлайн перед выходом
			_ = p.Node.UnicastConn.SetReadDeadline(time.Time{})
			return
		default:
		}

		buf := make([]byte, 4096)

		// Проверяем stopChan еще раз перед установкой дедлайна
		select {
		case <-p.stopChan:
			log.Printf("Player receiveMessages stopped (before SetReadDeadline)")
			_ = p.Node.UnicastConn.SetReadDeadline(time.Time{})
			return
		default:
			_ = p.Node.UnicastConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		}

		n, addr, err := p.Node.UnicastConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
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

	senderId := msg.GetSenderId()
	if senderId == 0 && addr != nil {
		senderId = p.Node.GetPlayerIdByAddress(addr)
	}

	p.Node.LastInteraction[senderId] = time.Now()

	// Запоминаем последний известный UDP-адрес отправителя
	if senderId > 0 && addr != nil {
		p.Node.KnownAddrs[senderId] = addr
	}

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
		// Проверяем - это AnnouncementMsg от нашего мастера или от другой игры
		// Если мы уже выбрали мастера (MasterAddr установлен), игнорируем сообщения от других
		if p.MasterAddr != nil && p.MasterAddr.String() != addr.String() {
			log.Printf("Received AnnouncementMsg from %v, but our master is %v - ignoring", addr, p.MasterAddr)
			p.Node.Mu.Unlock()
			return
		}

		// Устанавливаем MasterAddr из объявления, если там есть адрес мастера
		if len(t.Announcement.Games) > 0 {
			announcement := t.Announcement.Games[0]
			if announcement != nil && announcement.GetPlayers() != nil && len(announcement.GetPlayers().GetPlayers()) > 0 {
				masterPlayer := announcement.GetPlayers().GetPlayers()[0]
				if masterPlayer != nil && masterPlayer.GetIpAddress() != "" && masterPlayer.GetPort() != 0 {
					addrStr := fmt.Sprintf("%s:%d", masterPlayer.GetIpAddress(), masterPlayer.GetPort())
					if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
						p.MasterAddr = resolved
						p.Node.MasterAddr = resolved
					}
				}
			}
		}

		p.AnnouncementMsg = t.Announcement
		// Устанавливаем Config из AnnouncementMsg
		if len(t.Announcement.Games) > 0 {
			configWasNil := p.Node.Config == nil
			p.Node.Config = t.Announcement.Games[0].GetConfig()
			log.Printf("Set Config from AnnouncementMsg: stateDelayMs=%d", p.Node.Config.GetStateDelayMs())

			// Если Config был nil И горутины еще не запущены, запускаем их сейчас
			if configWasNil && !p.nodeGoroutinesStarted {
				p.nodeGoroutinesStarted = true
				p.Node.Mu.Unlock()
				p.Node.StartResendUnconfirmedMessages(p.Node.Config.GetStateDelayMs())
				p.Node.StartSendPings(p.Node.Config.GetStateDelayMs())
				log.Printf("Started Node goroutines after receiving Config")
				p.Node.Mu.Lock()
			}
		}

		// Отправляем JoinRequest только если ещё не отправляли
		alreadySent := p.joinRequestSent
		p.Node.Mu.Unlock()

		if !alreadySent {
			log.Printf("Received AnnouncementMsg from %v via unicast, sending JoinRequest", addr)
			p.sendJoinRequest()
		} else {
			log.Printf("Received AnnouncementMsg from %v via unicast, but JoinRequest already sent", addr)
		}
		return
	case *pb.GameMessage_State:
		stateOrder := t.State.GetState().GetStateOrder()
		if stateOrder <= p.LastStateMsg {
			p.Node.Mu.Unlock()
			return
		}
		p.LastStateMsg = stateOrder
		p.Node.State = t.State.GetState()

		// Always update master address from incoming states if we are a player
		if p.Node.Role != pb.NodeRole_MASTER {
			if p.MasterAddr == nil || p.MasterAddr.String() != addr.String() {
				log.Printf("Updating MasterAddr from incoming StateMsg: %v -> %v", p.MasterAddr, addr)
				p.MasterAddr = addr
				p.Node.MasterAddr = addr
			}
		}

		// Проверяем есть ли у игрока змейка
		hasSnake := false
		for _, snake := range p.Node.State.Snakes {
			if snake.GetPlayerId() == p.Node.PlayerInfo.GetId() {
				hasSnake = true
				break
			}
		}

		if !hasSnake {
			log.Printf("Player ID %d: Received StateMsg but has no snake (observer mode), stateOrder=%d",
				p.Node.PlayerInfo.GetId(), stateOrder)
		}

		// ВАЖНО: Синхронизируем роль из StateMsg в PlayerInfo
		// Это нужно на случай если роль изменилась на сервере
		for _, player := range p.Node.State.Players.Players {
			if player.GetId() == p.Node.PlayerInfo.GetId() {
				oldRole := p.Node.PlayerInfo.GetRole()
				newRole := player.GetRole()
				if oldRole != newRole {
					log.Printf("Player ID %d: Role changed in StateMsg from %v to %v",
						p.Node.PlayerInfo.GetId(), oldRole, newRole)
					p.Node.PlayerInfo.Role = player.Role
				}
				break
			}
		}

		p.Node.Cond.Broadcast()
		p.Node.Mu.Unlock()
		// SendAck вызываем БЕЗ мьютекса
		p.Node.SendAck(msg, addr)
		return
	case *pb.GameMessage_Error:
		errorMsg := t.Error.GetErrorMessage()
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		// Просто логируем ошибку, но не закрываем программу
		// Игрок может продолжить наблюдать за игрой
		log.Printf("Received error message: %s", errorMsg)
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

	// Определяем роль на основе IsViewer
	requestedRole := pb.NodeRole_NORMAL
	if p.IsViewer {
		requestedRole = pb.NodeRole_VIEWER
	}

	joinMsg := &pb.GameMessage{
		MsgSeq: proto.Int64(p.Node.MsgSeq),
		Type: &pb.GameMessage_Join{
			Join: &pb.GameMessage_JoinMsg{
				PlayerType:    pb.PlayerType_HUMAN.Enum(),
				PlayerName:    p.Node.PlayerInfo.Name,
				GameName:      proto.String(p.AnnouncementMsg.Games[0].GetGameName()),
				RequestedRole: requestedRole.Enum(),
			},
		},
	}

	p.Node.SendMessage(joinMsg, p.MasterAddr)

	// Устанавливаем флаг, что JoinRequest отправлен
	p.Node.Mu.Lock()
	p.joinRequestSent = true
	p.Node.Mu.Unlock()

	log.Printf("Player: Sent JoinMsg to master at %v (role: %v)", p.MasterAddr, requestedRole)
}

// обработка отвалившихся узлов
func (p *Player) checkTimeoutsLoop() {
	// НЕ используем defer p.wg.Done() здесь, потому что при вызове becomeMaster()
	// мы вызываем wg.Done() вручную ДО becomeMaster(), чтобы избежать deadlock

	// Ждем пока Config будет установлен
	for {
		p.Node.Mu.Lock()
		if p.Node.Config != nil {
			p.Node.Mu.Unlock()
			break
		}
		p.Node.Mu.Unlock()

		// Проверяем stopChan перед ожиданием
		select {
		case <-p.stopChan:
			log.Printf("Player checkTimeouts stopped before Config was set")
			p.wg.Done()
			return
		case <-time.After(100 * time.Millisecond):
			// Ждем и проверяем снова
		}
	}

	ticker := time.NewTicker(time.Duration(0.8*float64(p.Node.Config.GetStateDelayMs())) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			log.Printf("Player checkTimeouts stopped")
			p.wg.Done() // Вызываем вручную при нормальном завершении
			return
		case <-ticker.C:
		}

		now := time.Now()
		p.Node.Mu.Lock()

		if p.MasterAddr == nil {
			p.Node.Mu.Unlock()
			continue
		}

		// Проверяем таймаут MASTER
		masterId := p.getMasterPlayerId()
		lastInteraction, exists := p.Node.LastInteraction[masterId]

		// Таймаут происходит если:
		// 1. LastInteraction существует и прошло больше 0.8 * stateDelayMs
		// 2. LastInteraction не существует (мастер никогда не отвечал) - но только если мы уже в игре
		timeoutThreshold := time.Duration(0.8*float64(p.Node.Config.GetStateDelayMs())) * time.Millisecond
		isTimeout := false

		if exists {
			elapsed := now.Sub(lastInteraction)
			isTimeout = elapsed > timeoutThreshold
			if isTimeout {
				log.Printf("Timeout detected: elapsed=%v, threshold=%v, masterId=%d", elapsed, timeoutThreshold, masterId)
			}
		} else if p.haveId && masterId != 0 {
			// Если мы уже в игре (имеем ID) и знаем мастера, но никогда не получали от него сообщений
			// Это тоже таймаут
			isTimeout = true
			log.Printf("No LastInteraction record for master ID %d, haveId=%v", masterId, p.haveId)
		} else {
			// Отладка: почему не таймаут
			log.Printf("No timeout: exists=%v, haveId=%v, masterId=%d, role=%v", exists, p.haveId, masterId, p.Node.PlayerInfo.GetRole())
		}

		if isTimeout {
			log.Printf("MASTER timeout detected!")

			switch p.Node.PlayerInfo.GetRole() {
			// Обычный игрок заметил, что мастер отвалился и переключается к Deputy
			case pb.NodeRole_NORMAL:
				deputy := p.getDeputy()
				if deputy != nil {
					addrStr := fmt.Sprintf("%s:%d", deputy.GetIpAddress(), deputy.GetPort())
					addr, err := net.ResolveUDPAddr("udp", addrStr)
					if err != nil {
						log.Printf("Error resolving deputy address: %v", err)
						p.Node.Mu.Unlock()
						continue
					}

					oldMasterId := p.getMasterPlayerId()
					deputyId := deputy.GetId()

					// Обновляем адрес мастера
					oldMaster := p.MasterAddr
					p.MasterAddr = addr
					p.Node.MasterAddr = addr

					// Обновляем роли в локальном состоянии
					// Удаляем старого мастера из списка игроков
					var updatedPlayers []*pb.GamePlayer
					for _, player := range p.Node.State.Players.Players {
						if player.GetId() == oldMasterId {
							log.Printf("Removing old MASTER (player ID: %d) from local state", oldMasterId)
							continue
						}
						// DEPUTY становится новым MASTER
						if player.GetId() == deputyId {
							player.Role = pb.NodeRole_MASTER.Enum()
							log.Printf("Updated DEPUTY (player ID: %d) to MASTER in local state", deputyId)
						}
						updatedPlayers = append(updatedPlayers, player)
					}
					p.Node.State.Players.Players = updatedPlayers

					// Удаляем змейку старого мастера
					var updatedSnakes []*pb.GameState_Snake
					for _, snake := range p.Node.State.Snakes {
						if snake.GetPlayerId() != oldMasterId {
							updatedSnakes = append(updatedSnakes, snake)
						}
					}
					p.Node.State.Snakes = updatedSnakes

					// Обновляем LastInteraction - удаляем старого мастера, инициализируем нового
					delete(p.Node.LastInteraction, oldMasterId)
					p.Node.LastInteraction[deputyId] = time.Now()

					p.Node.Mu.Unlock()

					// Переадресуем неподтвержденные сообщения новому MASTER
					p.redirectUnconfirmedMessages(oldMaster, addr)

					log.Printf("Switched to DEPUTY (ID: %d) as new MASTER at %v", deputyId, p.MasterAddr)
				} else {
					log.Printf("No DEPUTY available to switch to")
					p.Node.Mu.Unlock()
				}
				continue

			// Deputy заметил, что отвалился мастер и заменяет его
			case pb.NodeRole_DEPUTY:
				p.Node.Mu.Unlock()
				// Декрементируем WaitGroup ДО вызова becomeMaster, чтобы избежать deadlock
				// (becomeMaster будет ждать завершения всех горутин, включая эту)
				p.wg.Done()
				p.becomeMaster()
				return // Выходим из цикла, так как стали MASTER (но defer wg.Done() уже не вызовется)
			}
		}
		p.Node.Mu.Unlock()
	}
}

func (p *Player) getMasterPlayerId() int32 {
	if p.Node.State == nil || p.Node.State.Players == nil {
		return 0
	}
	for _, player := range p.Node.State.Players.GetPlayers() {
		if player.GetRole() == pb.NodeRole_MASTER {
			return player.GetId()
		}
	}
	return 0
}

func (p *Player) getDeputy() *pb.GamePlayer {
	if p.Node.State == nil || p.Node.State.Players == nil {
		return nil
	}
	for _, player := range p.Node.State.Players.GetPlayers() {
		if player.GetRole() == pb.NodeRole_DEPUTY {
			return player
		}
	}
	return nil
}

func (p *Player) redirectUnconfirmedMessages(oldAddr, newAddr *net.UDPAddr) {
	p.Node.Mu.Lock()
	defer p.Node.Mu.Unlock()

	// Используем публичный метод Node для переадресации
	p.Node.RedirectUnconfirmedMessages(oldAddr, newAddr)
}

func (p *Player) becomeMaster() {
	log.Printf("DEPUTY becoming new MASTER")

	// СНАЧАЛА отправляем сигнал остановки
	close(p.stopChan)
	log.Printf("Sent stop signal to all Player goroutines")

	// ПОТОМ устанавливаем короткий дедлайн, чтобы ReadFromUDP быстро вернулся с timeout
	// и горутина могла проверить stopChan и завершиться
	_ = p.Node.UnicastConn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	_ = p.Node.MulticastConn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	log.Printf("Set short deadlines to unblock Read operations")

	// Останавливаем горутины Node (ResendUnconfirmedMessages и SendPings)
	p.Node.StopNodeGoroutines()
	log.Printf("Sent stop signal to all Node goroutines")

	// Ждем завершения всех горутин Player с таймаутом
	log.Printf("Waiting for Player goroutines to finish...")
	playerDone := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(playerDone)
	}()

	select {
	case <-playerDone:
		log.Printf("All Player goroutines finished successfully")
	case <-time.After(2 * time.Second):
		log.Printf("WARNING: Timeout waiting for Player goroutines to finish, proceeding anyway")
	}

	// Ждем завершения всех горутин Node с таймаутом
	log.Printf("Waiting for Node goroutines to finish...")
	nodeDone := make(chan struct{})
	go func() {
		p.Node.WaitForGoroutines()
		close(nodeDone)
	}()

	select {
	case <-nodeDone:
		log.Printf("All Node goroutines finished successfully")
	case <-time.After(2 * time.Second):
		log.Printf("WARNING: Timeout waiting for Node goroutines to finish, proceeding anyway")
	}

	// Очищаем очередь неподтвержденных сообщений
	p.Node.Mu.Lock()
	log.Printf("Clearing %d unconfirmed messages", p.Node.UnconfirmedMessages())
	p.Node.ClearUnconfirmedMessages()

	// Очищаем AckChan от всех оставшихся сообщений
	for len(p.Node.AckChan) > 0 {
		<-p.Node.AckChan
	}
	p.Node.Mu.Unlock()

	log.Printf("All old goroutines stopped and cleaned up")

	// Обновляем роль игрока
	p.Node.Mu.Lock()
	p.Node.PlayerInfo.Role = pb.NodeRole_MASTER.Enum()
	p.Node.Role = pb.NodeRole_MASTER // Также обновляем Node.Role!

	// Удаляем старого мастера из списка игроков и змеек
	var updatedPlayers []*pb.GamePlayer
	var updatedSnakes []*pb.GameState_Snake
	oldMasterId := int32(-1)

	for _, player := range p.Node.State.Players.Players {
		if player.GetRole() == pb.NodeRole_MASTER && player.GetId() != p.Node.PlayerInfo.GetId() {
			oldMasterId = player.GetId()
			// НЕ удаляем старого MASTER! Меняем его роль на VIEWER
			// чтобы он продолжал получать StateMsg и видеть игру
			player.Role = pb.NodeRole_VIEWER.Enum()
			// Инициализируем LastInteraction для старого MASTER-VIEWER
			// чтобы работала проверка таймаута, если он отключится
			p.Node.LastInteraction[oldMasterId] = time.Now()
			log.Printf("Changed old MASTER (player ID: %d) role to VIEWER in game state and initialized LastInteraction", oldMasterId)
		}
		if player.GetId() == p.Node.PlayerInfo.GetId() {
			player.Role = pb.NodeRole_MASTER.Enum()
		}
		updatedPlayers = append(updatedPlayers, player)
	}

	// Удаляем змейку старого мастера и убеждаемся что наша змейка ALIVE
	for _, snake := range p.Node.State.Snakes {
		if snake.GetPlayerId() == oldMasterId {
			continue // Пропускаем змейку старого мастера
		}
		// Убеждаемся что наша змейка остаётся ALIVE
		if snake.GetPlayerId() == p.Node.PlayerInfo.GetId() {
			snake.State = pb.GameState_Snake_ALIVE.Enum()
			log.Printf("Ensured our snake (player ID: %d) is ALIVE", p.Node.PlayerInfo.GetId())
		}
		updatedSnakes = append(updatedSnakes, snake)
	}

	p.Node.State.Players.Players = updatedPlayers
	p.Node.State.Snakes = updatedSnakes

	// Логируем текущее состояние для отладки
	log.Printf("After cleanup: %d players, %d snakes", len(updatedPlayers), len(updatedSnakes))
	for _, snake := range updatedSnakes {
		log.Printf("Snake for player %d: state=%v", snake.GetPlayerId(), snake.GetState())
	}

	// Удаляем старого MASTER из LastInteraction (если он был)
	if oldMasterId != -1 {
		delete(p.Node.LastInteraction, oldMasterId)
		log.Printf("Removed old MASTER (ID: %d) from LastInteraction", oldMasterId)

		// Инициализируем LastInteraction для старого MASTER-VIEWER с текущим временем
		// чтобы работала проверка таймаута
		p.Node.LastInteraction[oldMasterId] = time.Now()
		log.Printf("Initialized LastInteraction for old MASTER-VIEWER (ID: %d)", oldMasterId)
	}

	// Удаляем старого мастера из LastSent, если его адрес там есть
	if oldMasterId != -1 && p.MasterAddr != nil {
		oldMasterKey := p.MasterAddr.String()
		delete(p.Node.LastSent, oldMasterKey)
		log.Printf("Removed old MASTER address %s from LastSent", oldMasterKey)
	}

	// НЕ очищаем полностью LastInteraction и LastSent!
	// Они содержат информацию об остальных игроках, которая нужна для корректной работы
	log.Printf("Kept existing LastInteraction entries for %d players", len(p.Node.LastInteraction))
	log.Printf("Kept existing LastSent entries for %d addresses", len(p.Node.LastSent))

	// Очищаем MasterAddr - мы теперь сами мастер
	p.Node.MasterAddr = nil
	p.MasterAddr = nil
	log.Printf("Cleared MasterAddr")

	// Создаем новый канал остановки и AckChan для Master
	p.Node.ResetStopChan()
	p.Node.AckChan = make(chan int64, 100)

	// Извлекаем название игры из AnnouncementMsg
	gameName := "Game1" // дефолтное значение
	if p.AnnouncementMsg != nil && len(p.AnnouncementMsg.Games) > 0 {
		gameName = p.AnnouncementMsg.Games[0].GetGameName()
		log.Printf("Preserving original game name: '%s'", gameName)
	}

	// Создаем структуру Master из текущего Player
	newMaster := master.NewMasterFromPlayer(p.Node, p.Node.State.Players, p.LastStateMsg, gameName)
	p.Node.Mu.Unlock()

	log.Printf("Player %d is now MASTER, starting master functions", p.Node.PlayerInfo.GetId())

	// Отправляем RoleChangeMsg всем игрокам о смене MASTER
	p.notifyPlayersAboutNewMaster()

	// Запускаем все функции мастера
	newMaster.Start()

	log.Printf("Master started successfully")
}

// notifyPlayersAboutNewMaster уведомляет всех игроков о смене MASTER
func (p *Player) notifyPlayersAboutNewMaster() {
	p.Node.Mu.Lock()
	if p.Node.State == nil || p.Node.State.Players == nil {
		p.Node.Mu.Unlock()
		return
	}

	// Собираем адреса всех игроков кроме себя и старого мастера
	var playerAddrs []struct {
		addr     *net.UDPAddr
		playerId int32
	}

	for _, player := range p.Node.State.Players.Players {
		// Пропускаем себя
		if player.GetId() == p.Node.PlayerInfo.GetId() {
			continue
		}
		// Пропускаем старого мастера (у него была роль MASTER до смены)
		if player.GetRole() == pb.NodeRole_MASTER {
			log.Printf("Skipping old MASTER (player ID: %d) in notifications", player.GetId())
			continue
		}

		var addr *net.UDPAddr
		// try explicit player address first
		if player.GetIpAddress() != "" && player.GetPort() != 0 {
			addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
			if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
				addr = resolved
			}
		}
		// fallback to known last-seen address
		if addr == nil {
			if known, ok := p.Node.KnownAddrs[player.GetId()]; ok && known != nil {
				addr = known
			}
		}
		if addr == nil {
			log.Printf("No valid address for player %d, skipping notification", player.GetId())
			continue
		}

		playerAddrs = append(playerAddrs, struct {
			addr     *net.UDPAddr
			playerId int32
		}{addr, player.GetId()})
	}
	p.Node.Mu.Unlock()

	// Отправляем RoleChangeMsg каждому игроку
	// НЕ меняем их роль, просто уведомляем о новом мастере
	for _, info := range playerAddrs {
		roleChangeMsg := &pb.GameMessage{
			MsgSeq:     proto.Int64(p.Node.MsgSeq),
			SenderId:   proto.Int32(p.Node.PlayerInfo.GetId()),
			ReceiverId: proto.Int32(info.playerId),
			Type: &pb.GameMessage_RoleChange{
				RoleChange: &pb.GameMessage_RoleChangeMsg{
					SenderRole:   pb.NodeRole_MASTER.Enum(),
					ReceiverRole: pb.NodeRole_NORMAL.Enum(), // Оставляем их NORMAL (или DEPUTY, если это был DEPUTY)
				},
			},
		}
		p.Node.SendMessage(roleChangeMsg, info.addr)
		log.Printf("Notified player ID %d at %v about new MASTER", info.playerId, info.addr)
	}

	// Выбираем нового DEPUTY
	p.selectNewDeputy()
}

// selectNewDeputy выбирает нового DEPUTY среди NORMAL игроков
func (p *Player) selectNewDeputy() {
	p.Node.Mu.Lock()
	var deputyCandidate *pb.GamePlayer

	// Ищем живую змейку среди NORMAL игроков
	for _, player := range p.Node.State.Players.Players {
		// Пропускаем себя
		if player.GetId() == p.Node.PlayerInfo.GetId() {
			continue
		}
		// Проверяем что это NORMAL игрок
		if player.GetRole() != pb.NodeRole_NORMAL {
			continue
		}

		// Проверяем что у игрока есть живая змейка
		hasAliveSnake := false
		for _, snake := range p.Node.State.Snakes {
			if snake.GetPlayerId() == player.GetId() && snake.GetState() == pb.GameState_Snake_ALIVE {
				hasAliveSnake = true
				break
			}
		}

		if hasAliveSnake {
			deputyCandidate = player
			break
		}
	}

	if deputyCandidate == nil {
		p.Node.Mu.Unlock()
		log.Printf("No NORMAL players with alive snakes available to become DEPUTY")
		return
	}

	// Обновляем роль в состоянии
	deputyCandidate.Role = pb.NodeRole_DEPUTY.Enum()

	addrStr := fmt.Sprintf("%s:%d", deputyCandidate.GetIpAddress(), deputyCandidate.GetPort())
	deputyId := deputyCandidate.GetId()
	p.Node.Mu.Unlock()

	addr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		log.Printf("Error resolving address for new DEPUTY: %v", err)
		return
	}

	// Отправляем RoleChangeMsg новому DEPUTY
	roleChangeMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(p.Node.MsgSeq),
		SenderId:   proto.Int32(p.Node.PlayerInfo.GetId()),
		ReceiverId: proto.Int32(deputyId),
		Type: &pb.GameMessage_RoleChange{
			RoleChange: &pb.GameMessage_RoleChangeMsg{
				SenderRole:   pb.NodeRole_MASTER.Enum(),
				ReceiverRole: pb.NodeRole_DEPUTY.Enum(),
			},
		},
	}
	p.Node.SendMessage(roleChangeMsg, addr)
	log.Printf("Assigned player %d as new DEPUTY", deputyId)
}
