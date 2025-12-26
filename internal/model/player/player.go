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
	LastSeen        time.Time // Время последнего получения объявления
}

type Player struct {
	Node *common.Node

	AnnouncementMsg *pb.GameMessage_AnnouncementMsg
	MasterAddr      *net.UDPAddr
	LastStateMsg    int32

	haveId                bool
	joinRequestSent       bool
	nodeGoroutinesStarted bool
	IsViewer              bool

	DiscoveredGames []DiscoveredGame

	stopChan chan struct{}
	wg       sync.WaitGroup

	becomingMaster sync.Once
}

func NewPlayer(multicastConn *net.UDPConn) *Player {
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
	p.wg.Add(2)
	go p.receiveMessagesLoop()
	go p.checkTimeoutsLoop()

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
		select {
		case <-p.stopChan:
			log.Printf("Player ReceiveMulticastMessages stopped via stopChan")
			return
		default:
		}

		p.Node.Mu.Lock()
		role := p.Node.PlayerInfo.GetRole()
		p.Node.Mu.Unlock()

		if role == pb.NodeRole_MASTER {
			log.Printf("Player became MASTER, stopping ReceiveMulticastMessages")
			return
		}

		_ = p.Node.MulticastConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		buf := make([]byte, 4096)
		n, addr, err := p.Node.MulticastConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
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
	var masterAddr *net.UDPAddr
	if announcement != nil && announcement.GetPlayers() != nil {
		for _, player := range announcement.GetPlayers().GetPlayers() {
			if player.GetRole() == pb.NodeRole_MASTER {
				if player.GetIpAddress() != "" && player.GetPort() != 0 {
					addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
					if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
						masterAddr = resolved
						break
					}
				}
			}
		}
		if masterAddr == nil && len(announcement.GetPlayers().GetPlayers()) > 0 {
			masterPlayer := announcement.GetPlayers().GetPlayers()[0]
			if masterPlayer != nil && masterPlayer.GetIpAddress() != "" && masterPlayer.GetPort() != 0 {
				addrStr := fmt.Sprintf("%s:%d", masterPlayer.GetIpAddress(), masterPlayer.GetPort())
				if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
					masterAddr = resolved
				}
			}
		}
	}
	if masterAddr == nil && addr != nil {
		masterAddr = addr
	}

	p.Node.Mu.Lock()
	defer p.Node.Mu.Unlock()
	for i, game := range p.DiscoveredGames {
		if game.MasterAddr != nil && masterAddr != nil && game.MasterAddr.String() == masterAddr.String() {
			p.DiscoveredGames[i].Players = announcement.GetPlayers()
			p.DiscoveredGames[i].Config = announcement.GetConfig()
			p.DiscoveredGames[i].CanJoin = announcement.GetCanJoin()
			p.DiscoveredGames[i].GameName = announcement.GetGameName()
			p.DiscoveredGames[i].AnnouncementMsg = announcementMsg
			p.DiscoveredGames[i].MasterAddr = masterAddr
			p.DiscoveredGames[i].LastSeen = time.Now()
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
		LastSeen:        time.Now(),
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
			_ = p.Node.UnicastConn.SetReadDeadline(time.Time{})
			return
		default:
		}

		buf := make([]byte, 4096)

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
		select {
		case p.Node.AckChan <- msg.GetMsgSeq():
		default:
			log.Printf("Warning: Player AckChan full, dropping ACK info for msg seq %d", msg.GetMsgSeq())
		}
		p.Node.Mu.Unlock()
		return
	case *pb.GameMessage_Announcement:
		if p.MasterAddr != nil && p.MasterAddr.String() != addr.String() {
			log.Printf("Received AnnouncementMsg from %v, but our master is %v - ignoring", addr, p.MasterAddr)
			p.Node.Mu.Unlock()
			return
		}

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
		if len(t.Announcement.Games) > 0 {
			configWasNil := p.Node.Config == nil
			p.Node.Config = t.Announcement.Games[0].GetConfig()
			log.Printf("Set Config from AnnouncementMsg: stateDelayMs=%d", p.Node.Config.GetStateDelayMs())

			if configWasNil && !p.nodeGoroutinesStarted {
				p.nodeGoroutinesStarted = true
				p.Node.Mu.Unlock()
				p.Node.StartResendUnconfirmedMessages(p.Node.Config.GetStateDelayMs())
				p.Node.StartSendPings(p.Node.Config.GetStateDelayMs())
				log.Printf("Started Node goroutines after receiving Config")
				p.Node.Mu.Lock()
			}
		}

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

		if p.Node.Role != pb.NodeRole_MASTER {
			if p.MasterAddr == nil || p.MasterAddr.String() != addr.String() {
				log.Printf("Updating MasterAddr from incoming StateMsg: %v -> %v", p.MasterAddr, addr)
				p.MasterAddr = addr
				p.Node.MasterAddr = addr
			}
		}

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
		p.Node.SendAck(msg, addr)
		return
	case *pb.GameMessage_Error:
		errorMsg := t.Error.GetErrorMessage()
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		log.Printf("Received error message: %s", errorMsg)
		return
	case *pb.GameMessage_RoleChange:
		p.handleRoleChangeMessage(msg, addr)
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		return
	case *pb.GameMessage_Ping, *pb.GameMessage_Steer, *pb.GameMessage_Join:
		p.Node.Mu.Unlock()
		p.Node.SendAck(msg, addr)
		return
	default:
		log.Printf("Received unknown message")
		p.Node.Mu.Unlock()
	}
}

func (p *Player) discoverGamesLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
			p.Node.Mu.Lock()
			var activeGames []DiscoveredGame
			for _, game := range p.DiscoveredGames {
				if time.Since(game.LastSeen) < 5*time.Second {
					activeGames = append(activeGames, game)
				} else {
					log.Printf("Removing stale game: '%s' from %v (last seen %v ago)",
						game.GameName, game.MasterAddr, time.Since(game.LastSeen))
				}
			}
			p.DiscoveredGames = activeGames
			p.Node.Mu.Unlock()
		}
	}
}

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

	p.Node.Mu.Lock()
	p.joinRequestSent = true
	p.Node.Mu.Unlock()

	log.Printf("Player: Sent JoinMsg to master at %v (role: %v)", p.MasterAddr, requestedRole)
}

func (p *Player) checkTimeoutsLoop() {
	for {
		p.Node.Mu.Lock()
		if p.Node.Config != nil {
			p.Node.Mu.Unlock()
			break
		}
		p.Node.Mu.Unlock()

		select {
		case <-p.stopChan:
			log.Printf("Player checkTimeouts stopped before Config was set")
			p.wg.Done()
			return
		case <-time.After(100 * time.Millisecond):
		}
	}

	ticker := time.NewTicker(time.Duration(0.8*float64(p.Node.Config.GetStateDelayMs())) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			log.Printf("Player checkTimeouts stopped")
			p.wg.Done()
			return
		case <-ticker.C:
		}

		now := time.Now()
		p.Node.Mu.Lock()

		if p.MasterAddr == nil {
			p.Node.Mu.Unlock()
			continue
		}

		masterId := p.getMasterPlayerId()
		lastInteraction, exists := p.Node.LastInteraction[masterId]

		timeoutThreshold := time.Duration(0.8*float64(p.Node.Config.GetStateDelayMs())) * time.Millisecond
		isTimeout := false

		if exists {
			elapsed := now.Sub(lastInteraction)
			isTimeout = elapsed > timeoutThreshold
			if isTimeout {
				log.Printf("Timeout detected: elapsed=%v, threshold=%v, masterId=%d", elapsed, timeoutThreshold, masterId)
			}
		} else if p.haveId && masterId != 0 {
			isTimeout = true
			log.Printf("No LastInteraction record for master ID %d, haveId=%v", masterId, p.haveId)
		} else {
			log.Printf("No timeout: exists=%v, haveId=%v, masterId=%d, role=%v", exists, p.haveId, masterId, p.Node.PlayerInfo.GetRole())
		}

		if isTimeout {
			log.Printf("MASTER timeout detected!")

			switch p.Node.PlayerInfo.GetRole() {
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

					oldMaster := p.MasterAddr
					p.MasterAddr = addr
					p.Node.MasterAddr = addr

					var updatedPlayers []*pb.GamePlayer
					for _, player := range p.Node.State.Players.Players {
						if player.GetId() == oldMasterId {
							log.Printf("Removing old MASTER (player ID: %d) from local state", oldMasterId)
							continue
						}
						if player.GetId() == deputyId {
							player.Role = pb.NodeRole_MASTER.Enum()
							log.Printf("Updated DEPUTY (player ID: %d) to MASTER in local state", deputyId)
						}
						updatedPlayers = append(updatedPlayers, player)
					}
					p.Node.State.Players.Players = updatedPlayers

					var updatedSnakes []*pb.GameState_Snake
					for _, snake := range p.Node.State.Snakes {
						if snake.GetPlayerId() != oldMasterId {
							updatedSnakes = append(updatedSnakes, snake)
						}
					}
					p.Node.State.Snakes = updatedSnakes

					delete(p.Node.LastInteraction, oldMasterId)
					p.Node.LastInteraction[deputyId] = time.Now()

					p.Node.Mu.Unlock()

					p.redirectUnconfirmedMessages(oldMaster, addr)

					log.Printf("Switched to DEPUTY (ID: %d) as new MASTER at %v", deputyId, p.MasterAddr)
				} else {
					log.Printf("No DEPUTY available to switch to")
					p.Node.Mu.Unlock()
				}
				continue

			case pb.NodeRole_DEPUTY:
				p.Node.Mu.Unlock()
				p.wg.Done()
				p.becomeMaster()
				return
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

	p.Node.RedirectUnconfirmedMessages(oldAddr, newAddr)
}

func (p *Player) becomeMaster() {
	p.becomingMaster.Do(func() {
		go p.doBecomeMaster()
	})
}

func (p *Player) doBecomeMaster() {
	log.Printf("DEPUTY becoming new MASTER (idempotent)")

	close(p.stopChan)
	log.Printf("Sent stop signal to all Player goroutines")

	p.Node.ClearUnconfirmedMessages()

	_ = p.Node.UnicastConn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	_ = p.Node.MulticastConn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	log.Printf("Set short deadlines to unblock Read operations")

	p.Node.StopNodeGoroutines()
	log.Printf("Sent stop signal to all Node goroutines")

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

	p.Node.ResetStopChan()

	p.Node.Mu.Lock()
	log.Printf("Clearing %d unconfirmed messages", p.Node.UnconfirmedMessages())
	p.Node.ClearUnconfirmedMessages()

	for len(p.Node.AckChan) > 0 {
		<-p.Node.AckChan
	}
	p.Node.Mu.Unlock()

	log.Printf("Internal Node state reset for MASTER duties")

	p.Node.Mu.Lock()
	p.Node.PlayerInfo.Role = pb.NodeRole_MASTER.Enum()
	p.Node.Role = pb.NodeRole_MASTER

	oldMasterId := int32(-1)
	if p.Node.State != nil && p.Node.State.Players != nil {
		for _, player := range p.Node.State.Players.Players {
			if player.GetRole() == pb.NodeRole_MASTER && player.GetId() != p.Node.PlayerInfo.GetId() {
				oldMasterId = player.GetId()
				// Переводим старого мастера в VIEWER
				player.Role = pb.NodeRole_VIEWER.Enum()
				log.Printf("Old MASTER (ID: %d) found in state, set to VIEWER", oldMasterId)
			}
			if player.GetId() == p.Node.PlayerInfo.GetId() {
				player.Role = pb.NodeRole_MASTER.Enum()
			}
		}

		var updatedSnakes []*pb.GameState_Snake
		for _, snake := range p.Node.State.Snakes {
			if snake.GetPlayerId() == oldMasterId {
				continue
			}
			if snake.GetPlayerId() == p.Node.PlayerInfo.GetId() {
				snake.State = pb.GameState_Snake_ALIVE.Enum()
			}
			updatedSnakes = append(updatedSnakes, snake)
		}
		p.Node.State.Snakes = updatedSnakes

		if oldMasterId != -1 {
			delete(p.Node.LastInteraction, oldMasterId)
		}
	}

	p.Node.MasterAddr = nil
	p.MasterAddr = nil

	p.Node.Cond.Broadcast()
	p.Node.Mu.Unlock()

	gameName := "Game1"
	if p.AnnouncementMsg != nil && len(p.AnnouncementMsg.Games) > 0 {
		gameName = p.AnnouncementMsg.Games[0].GetGameName()
	}

	newMaster := master.NewMasterFromPlayer(p.Node, p.Node.State.Players, p.LastStateMsg, gameName)
	log.Printf("Player %d transitioned to MASTER role. Game name: %s", p.Node.PlayerInfo.GetId(), gameName)

	p.notifyPlayersAboutNewMaster()

	newMaster.Start()
	log.Printf("Master duties started successfuly")
}

func (p *Player) notifyPlayersAboutNewMaster() {
	p.Node.Mu.Lock()
	if p.Node.State == nil || p.Node.State.Players == nil {
		p.Node.Mu.Unlock()
		return
	}

	var playerAddrs []struct {
		addr     *net.UDPAddr
		playerId int32
	}

	for _, player := range p.Node.State.Players.Players {
		if player.GetId() == p.Node.PlayerInfo.GetId() {
			continue
		}

		var addr *net.UDPAddr
		if player.GetIpAddress() != "" && player.GetPort() != 0 {
			addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
			if resolved, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
				addr = resolved
			}
		}
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

	for _, info := range playerAddrs {
		roleChangeMsg := &pb.GameMessage{
			MsgSeq:     proto.Int64(p.Node.MsgSeq),
			SenderId:   proto.Int32(p.Node.PlayerInfo.GetId()),
			ReceiverId: proto.Int32(info.playerId),
			Type: &pb.GameMessage_RoleChange{
				RoleChange: &pb.GameMessage_RoleChangeMsg{
					SenderRole:   pb.NodeRole_MASTER.Enum(),
					ReceiverRole: pb.NodeRole_NORMAL.Enum(),
				},
			},
		}
		p.Node.SendMessage(roleChangeMsg, info.addr)
		log.Printf("Notified player ID %d at %v about new MASTER", info.playerId, info.addr)
	}

	p.selectNewDeputy()
}

func (p *Player) selectNewDeputy() {
	p.Node.Mu.Lock()
	var deputyCandidate *pb.GamePlayer

	for _, player := range p.Node.State.Players.Players {
		if player.GetId() == p.Node.PlayerInfo.GetId() {
			continue
		}
		if player.GetRole() != pb.NodeRole_NORMAL {
			continue
		}

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

	deputyCandidate.Role = pb.NodeRole_DEPUTY.Enum()

	addrStr := fmt.Sprintf("%s:%d", deputyCandidate.GetIpAddress(), deputyCandidate.GetPort())
	deputyId := deputyCandidate.GetId()
	p.Node.Mu.Unlock()

	addr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		log.Printf("Error resolving address for new DEPUTY: %v", err)
		return
	}

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
