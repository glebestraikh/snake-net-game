package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	pb "snake-net-game/pkg/proto"
	"time"
)

func (m *Master) handleErrorMsg(addr *net.UDPAddr) {
	errorMsg := &pb.GameMessage{
		Type: &pb.GameMessage_Error{
			Error: &pb.GameMessage_ErrorMsg{
				ErrorMessage: proto.String("Cannot join: no available space"),
			},
		},
	}
	m.Node.SendMessage(errorMsg, addr)
}

func (m *Master) handleJoinMessage(msgSeq int64, joinMsg *pb.GameMessage_JoinMsg, addr *net.UDPAddr, coord *pb.GameState_Coord) {
	m.Node.Mu.Lock()
	newPlayerID := int32(len(m.players.Players) + 1)
	newPlayer := &pb.GamePlayer{
		Name:      proto.String(joinMsg.GetPlayerName()),
		Id:        proto.Int32(newPlayerID),
		IpAddress: proto.String(addr.IP.String()),
		Port:      proto.Int32(int32(addr.Port)),
		Role:      joinMsg.GetRequestedRole().Enum(),
		Type:      joinMsg.GetPlayerType().Enum(),
		Score:     proto.Int32(0),
	}
	m.players.Players = append(m.players.Players, newPlayer)
	m.Node.State.Players = m.players
	m.addSnakeForNewPlayer(newPlayerID, coord)
	m.Node.Mu.Unlock()

	// Вызываем checkAndAssignDeputy БЕЗ удержания мьютекса, так как внутри вызывается SendMessage
	m.checkAndAssignDeputy()

	ackMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(msgSeq),
		SenderId:   proto.Int32(1),
		ReceiverId: proto.Int32(newPlayerID),
		Type: &pb.GameMessage_Ack{
			Ack: &pb.GameMessage_AckMsg{},
		},
	}
	m.Node.SendMessage(ackMsg, addr)

	// Отправляем текущее состояние игры новому игроку
	m.Node.Mu.Lock()
	stateMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(m.Node.MsgSeq),
		SenderId:   proto.Int32(m.Node.PlayerInfo.GetId()),
		ReceiverId: proto.Int32(newPlayerID),
		Type: &pb.GameMessage_State{
			State: &pb.GameMessage_StateMsg{
				State: &pb.GameState{
					StateOrder: proto.Int32(m.Node.State.GetStateOrder()),
					Snakes:     m.Node.State.GetSnakes(),
					Foods:      m.Node.State.GetFoods(),
					Players:    m.Node.State.GetPlayers(),
				},
			},
		},
	}
	m.Node.Mu.Unlock()

	m.Node.SendMessage(stateMsg, addr)

	log.Printf("New player joined, ID: %v, sent initial state", newPlayer)
}

// назначение заместителя
func (m *Master) checkAndAssignDeputy() {
	m.Node.Mu.Lock()
	hasDeputy := m.hasDeputy()
	var playerToAssign *pb.GamePlayer
	if !hasDeputy {
		for _, player := range m.players.Players {
			if player.GetRole() == pb.NodeRole_NORMAL {
				playerToAssign = player
				break
			}
		}
	}
	m.Node.Mu.Unlock()

	// Вызываем assignDeputy БЕЗ мьютекса, так как внутри SendMessage
	if playerToAssign != nil {
		m.assignDeputy(playerToAssign)
	}
}

// проверка наличия Deputy
func (m *Master) hasDeputy() bool {
	for _, player := range m.players.Players {
		if player.GetRole() == pb.NodeRole_DEPUTY {
			return true
		}
	}
	return false
}

func (m *Master) addSnakeForNewPlayer(playerID int32, coord *pb.GameState_Coord) {
	newSnake := &pb.GameState_Snake{
		PlayerId: proto.Int32(playerID),
		Points: []*pb.GameState_Coord{
			{
				X: proto.Int32(coord.GetX()),
				Y: proto.Int32(coord.GetY()),
			},
		},
		State:         pb.GameState_Snake_ALIVE.Enum(),
		HeadDirection: pb.Direction_RIGHT.Enum(),
	}

	m.Node.State.Snakes = append(m.Node.State.Snakes, newSnake)
}

func (m *Master) handleDiscoverMessage(addr *net.UDPAddr) {
	log.Printf("Received DiscoverMsg from %v via unicast", addr)
	announcementMsg := &pb.GameMessage{
		MsgSeq: proto.Int64(m.Node.MsgSeq),
		Type: &pb.GameMessage_Announcement{
			Announcement: &pb.GameMessage_AnnouncementMsg{
				Games: []*pb.GameAnnouncement{m.announcement},
			},
		},
	}

	m.Node.SendMessage(announcementMsg, addr)
}

func (m *Master) handleSteerMessage(steerMsg *pb.GameMessage_SteerMsg, playerId int32) {
	m.Node.Mu.Lock()
	defer m.Node.Mu.Unlock()

	var snake *pb.GameState_Snake
	for _, s := range m.Node.State.Snakes {
		if s.GetPlayerId() == playerId {
			snake = s
			break
		}
	}

	if snake == nil {
		log.Printf("No snake found for player ID: %d", playerId)
		return
	}

	newDirection := steerMsg.GetDirection()
	currentDirection := snake.GetHeadDirection()

	isOppositeDirection := func(cur, new pb.Direction) bool {
		switch cur {
		case pb.Direction_UP:
			return new == pb.Direction_DOWN
		case pb.Direction_DOWN:
			return new == pb.Direction_UP
		case pb.Direction_LEFT:
			return new == pb.Direction_RIGHT
		case pb.Direction_RIGHT:
			return new == pb.Direction_LEFT
		}
		return false
	}(currentDirection, newDirection)

	if isOppositeDirection {
		log.Printf("Invalid direction change from player ID: %d", playerId)
		return
	}

	snake.HeadDirection = newDirection.Enum()
	log.Printf("Player ID: %d changed direction to: %v", playerId, newDirection)
}

// обработка отвалившихся узлов
func (m *Master) checkTimeouts() {
	ticker := time.NewTicker(time.Duration(0.8*float64(m.Node.Config.GetStateDelayMs())) * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		m.Node.Mu.Lock()
		// Копируем map для безопасной итерации
		interactionsCopy := make(map[int32]time.Time)
		for playerId, lastInteraction := range m.Node.LastInteraction {
			interactionsCopy[playerId] = lastInteraction
		}
		m.Node.Mu.Unlock()

		for playerId, lastInteraction := range interactionsCopy {
			if playerId == 0 {
				continue
			}
			if now.Sub(lastInteraction) > time.Duration(0.8*float64(m.Node.Config.GetStateDelayMs()))*time.Millisecond {
				log.Printf("player ID: %d has timeout", playerId)
				m.removePlayer(playerId)
			}
		}
	}
}

func (m *Master) removePlayer(playerId int32) {
	m.Node.Mu.Lock()
	m.removePlayerUnsafe(playerId)
	wasDeputy := m.wasPlayerDeputy(playerId)
	m.Node.Mu.Unlock()

	// Если игрок был DEPUTY, назначаем нового (БЕЗ мьютекса, так как внутри SendMessage)
	if wasDeputy {
		m.findNewDeputy()
	}
}

// removePlayerUnsafe удаляет игрока БЕЗ захвата мьютекса (для использования когда мьютекс уже захвачен)
func (m *Master) removePlayerUnsafe(playerId int32) {
	delete(m.Node.LastInteraction, playerId)

	var removedPlayer *pb.GamePlayer
	var index int
	for i, player := range m.players.Players {
		if player.GetId() == playerId {
			removedPlayer = player
			index = i
			break
		}
	}

	if removedPlayer == nil {
		log.Printf("Player ID: %d not found for removal", playerId)
		return
	}

	// Удаляем игрока
	m.players.Players = append(m.players.Players[:index], m.players.Players[index+1:]...)
	delete(m.Node.LastSent, fmt.Sprintf("%s:%d", removedPlayer.GetIpAddress(), removedPlayer.GetPort()))

	// Если игрок стал VIEWER, переводим его змею в ZOMBIE
	if removedPlayer.GetRole() == pb.NodeRole_VIEWER {
		m.makeSnakeZombie(playerId)
	}

	m.Node.State.Players = m.players
	log.Printf("Player ID: %d processed for removal", playerId)
}

// wasPlayerDeputy проверяет, был ли игрок заместителем (вызывать с захваченным мьютексом)
func (m *Master) wasPlayerDeputy(playerId int32) bool {
	for _, player := range m.players.Players {
		if player.GetId() == playerId {
			return player.GetRole() == pb.NodeRole_DEPUTY
		}
	}
	return false
}

func (m *Master) findNewDeputy() {
	m.Node.Mu.Lock()
	var playerToAssign *pb.GamePlayer
	for _, player := range m.players.Players {
		if player.GetRole() == pb.NodeRole_NORMAL {
			playerToAssign = player
			break
		}
	}
	m.Node.Mu.Unlock()

	// Вызываем assignDeputy БЕЗ мьютекса, так как внутри SendMessage
	if playerToAssign != nil {
		m.assignDeputy(playerToAssign)
	}
}

// назначение нового заместителя
func (m *Master) assignDeputy(player *pb.GamePlayer) {
	player.Role = pb.NodeRole_DEPUTY.Enum()

	roleChangeMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(m.Node.MsgSeq),
		SenderId:   proto.Int32(m.Node.PlayerInfo.GetId()),
		ReceiverId: proto.Int32(player.GetId()),
		Type: &pb.GameMessage_RoleChange{
			RoleChange: &pb.GameMessage_RoleChangeMsg{
				SenderRole:   pb.NodeRole_MASTER.Enum(),
				ReceiverRole: pb.NodeRole_DEPUTY.Enum(),
			},
		},
	}
	playerAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort()))
	if err != nil {
		log.Printf("Error resolving address for Deputy: %v", err)
		return
	}

	m.Node.SendMessage(roleChangeMsg, playerAddr)
	log.Printf("Player ID: %d is assigned as DEPUTY", player.GetId())
}

func (m *Master) makeSnakeZombie(playerId int32) {
	for _, snake := range m.Node.State.Snakes {
		if snake.GetPlayerId() == playerId {
			snake.State = pb.GameState_Snake_ZOMBIE.Enum()
			log.Printf("Snake for player ID: %d is now a ZOMBIE", playerId)
			return
		}
	}
	log.Printf("No snake found for player ID: %d to make ZOMBIE", playerId)
}

// обработка roleChangeMsg от Deputy
func (m *Master) handleRoleChangeMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	roleChangeMsg := msg.GetRoleChange()

	switch {
	case roleChangeMsg.GetSenderRole() == pb.NodeRole_DEPUTY && roleChangeMsg.GetReceiverRole() == pb.NodeRole_MASTER:
		// TODO: доделать
		// DEPUTY -> MASTER
		log.Printf("Deputy has taken over as MASTER. Stopping PlayerInfo.")
		m.stopMaster()

	case roleChangeMsg.GetReceiverRole() == pb.NodeRole_VIEWER:
		// Player -> VIEWER
		playerId := msg.GetSenderId()
		log.Printf("Player ID: %d is now a VIEWER. Converting snake to ZOMBIE.", playerId)

		m.Node.Mu.Lock()
		m.makeSnakeZombie(playerId)

		for _, player := range m.players.Players {
			if player.GetId() == playerId {
				player.Role = pb.NodeRole_VIEWER.Enum()
				break
			}
		}
		m.Node.Mu.Unlock()
	default:
		log.Printf("Received unknown RoleChangeMsg from player ID: %d", msg.GetSenderId())
	}
}

func (m *Master) stopMaster() {
	log.Println("Switching PlayerInfo role to VIEWER...")

	// Меняем роль мастера
	m.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	// Делаем змею мастера ZOMBIE
	m.makeSnakeZombie(m.Node.PlayerInfo.GetId())

	m.announcement.CanJoin = proto.Bool(false)
	// Останавливаем функции мастера
	log.Println("Master is now a VIEWER. Continuing as an observer.")
}
