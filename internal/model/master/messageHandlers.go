package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"math/rand"
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

	// Находим максимальный существующий ID и прибавляем 1
	maxID := int32(0)
	for _, player := range m.players.Players {
		if player.GetId() > maxID {
			maxID = player.GetId()
		}
	}
	newPlayerID := maxID + 1

	// Определяем роль игрока
	requestedRole := joinMsg.GetRequestedRole()
	// VIEWER не может быть MASTER или DEPUTY
	if requestedRole == pb.NodeRole_MASTER || requestedRole == pb.NodeRole_DEPUTY {
		requestedRole = pb.NodeRole_NORMAL
	}

	newPlayer := &pb.GamePlayer{
		Name:      proto.String(joinMsg.GetPlayerName()),
		Id:        proto.Int32(newPlayerID),
		IpAddress: proto.String(addr.IP.String()),
		Port:      proto.Int32(int32(addr.Port)),
		Role:      requestedRole.Enum(),
		Type:      joinMsg.GetPlayerType().Enum(),
		Score:     proto.Int32(0),
	}
	m.players.Players = append(m.players.Players, newPlayer)
	m.Node.State.Players = m.players

	// Создаем змейку только если игрок НЕ VIEWER
	if requestedRole != pb.NodeRole_VIEWER {
		m.addSnakeForNewPlayer(newPlayerID, coord)
	}
	m.Node.Mu.Unlock()

	// Вызываем checkAndAssignDeputy БЕЗ удержания мьютекса, так как внутри вызывается SendMessage
	// Назначаем Deputy только если новый игрок НЕ VIEWER
	if requestedRole != pb.NodeRole_VIEWER {
		m.checkAndAssignDeputy()
	}

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
	// Центр квадрата 5x5 с учетом тора
	centerX := (coord.GetX() + 2) % m.Node.Config.GetWidth()
	centerY := (coord.GetY() + 2) % m.Node.Config.GetHeight()

	// Пробуем все 4 направления для хвоста
	directions := []pb.Direction{pb.Direction_UP, pb.Direction_DOWN, pb.Direction_LEFT, pb.Direction_RIGHT}

	// Перемешиваем направления для случайности
	rand.Shuffle(len(directions), func(i, j int) {
		directions[i], directions[j] = directions[j], directions[i]
	})

	var tailX, tailY int32
	var headDirection pb.Direction
	var found bool

	// Пробуем каждое направление, пока не найдем свободное от еды
	for _, tailDirection := range directions {
		tailX = centerX
		tailY = centerY

		switch tailDirection {
		case pb.Direction_UP:
			tailY = (centerY - 1 + m.Node.Config.GetHeight()) % m.Node.Config.GetHeight()
			headDirection = pb.Direction_DOWN // Противоположное направление
		case pb.Direction_DOWN:
			tailY = (centerY + 1) % m.Node.Config.GetHeight()
			headDirection = pb.Direction_UP
		case pb.Direction_LEFT:
			tailX = (centerX - 1 + m.Node.Config.GetWidth()) % m.Node.Config.GetWidth()
			headDirection = pb.Direction_RIGHT
		case pb.Direction_RIGHT:
			tailX = (centerX + 1) % m.Node.Config.GetWidth()
			headDirection = pb.Direction_LEFT
		}

		// Проверяем, нет ли еды на клетках головы и хвоста
		headHasFood := false
		tailHasFood := false

		for _, food := range m.Node.State.Foods {
			if food.GetX() == centerX && food.GetY() == centerY {
				headHasFood = true
			}
			if food.GetX() == tailX && food.GetY() == tailY {
				tailHasFood = true
			}
		}

		if !headHasFood && !tailHasFood {
			found = true
			break
		}
	}

	// Если не нашли место без еды, используем первое направление (еда потом удалится)
	if !found {
		log.Printf("Warning: Could not find position without food for player %d", playerID)
		tailDirection := directions[0]
		tailX = centerX
		tailY = centerY

		switch tailDirection {
		case pb.Direction_UP:
			tailY = (centerY - 1 + m.Node.Config.GetHeight()) % m.Node.Config.GetHeight()
			headDirection = pb.Direction_DOWN
		case pb.Direction_DOWN:
			tailY = (centerY + 1) % m.Node.Config.GetHeight()
			headDirection = pb.Direction_UP
		case pb.Direction_LEFT:
			tailX = (centerX - 1 + m.Node.Config.GetWidth()) % m.Node.Config.GetWidth()
			headDirection = pb.Direction_RIGHT
		case pb.Direction_RIGHT:
			tailX = (centerX + 1) % m.Node.Config.GetWidth()
			headDirection = pb.Direction_LEFT
		}
	}

	// Создаем змейку длиной 2 клетки: голова в центре, хвост в соседней клетке
	newSnake := &pb.GameState_Snake{
		PlayerId: proto.Int32(playerID),
		Points: []*pb.GameState_Coord{
			{
				X: proto.Int32(centerX),
				Y: proto.Int32(centerY),
			},
			{
				X: proto.Int32(tailX),
				Y: proto.Int32(tailY),
			},
		},
		State:         pb.GameState_Snake_ALIVE.Enum(),
		HeadDirection: headDirection.Enum(),
	}

	m.Node.State.Snakes = append(m.Node.State.Snakes, newSnake)
	log.Printf("Created snake for player %d: head at (%d,%d), tail at (%d,%d), direction: %v",
		playerID, centerX, centerY, tailX, tailY, headDirection)
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

	// ZOMBIE-змейки не могут менять направление
	if snake.GetState() == pb.GameState_Snake_ZOMBIE {
		log.Printf("Cannot steer ZOMBIE snake for player ID: %d", playerId)
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
	defer m.wg.Done()
	ticker := time.NewTicker(time.Duration(0.8*float64(m.Node.Config.GetStateDelayMs())) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			log.Printf("Master checkTimeouts stopped")
			return
		case <-ticker.C:
		}

		now := time.Now()
		m.Node.Mu.Lock()
		// Проверяем, остановлен ли мастер
		if m.stopped {
			m.Node.Mu.Unlock()
			return
		}
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

	// Сохраняем информацию о игроке
	var wasDeputy bool

	for _, player := range m.players.Players {
		if player.GetId() == playerId {
			wasDeputy = player.GetRole() == pb.NodeRole_DEPUTY
			break
		}
	}

	// Удаляем змейку игрока, если она есть
	var snakeIndex = -1
	for i, snake := range m.Node.State.Snakes {
		if snake.GetPlayerId() == playerId {
			snakeIndex = i
			break
		}
	}
	if snakeIndex >= 0 {
		m.Node.State.Snakes = append(m.Node.State.Snakes[:snakeIndex], m.Node.State.Snakes[snakeIndex+1:]...)
		log.Printf("Removed snake for timed out player ID: %d", playerId)
	}

	// Удаляем из LastInteraction
	delete(m.Node.LastInteraction, playerId)

	// Проверяем роль игрока и наличие змейки
	var playerRole pb.NodeRole
	var playerIndex = -1
	var hasSnake = false

	for i, player := range m.players.Players {
		if player.GetId() == playerId {
			playerRole = player.GetRole()
			playerIndex = i
			break
		}
	}

	// Проверяем есть ли змейка у игрока
	for _, snake := range m.Node.State.Snakes {
		if snake.GetPlayerId() == playerId {
			hasSnake = true
			break
		}
	}

	// Удаляем игрока из списка если:
	// 1. Он VIEWER (изначально присоединился как наблюдатель)
	// 2. Он умер (нет змейки) - DEPUTY, NORMAL без змейки
	shouldRemove := (playerRole == pb.NodeRole_VIEWER || !hasSnake) && playerIndex >= 0

	if shouldRemove {
		// Получаем адрес игрока для удаления из LastSent
		disconnectedPlayer := m.players.Players[playerIndex]
		addrKey := fmt.Sprintf("%s:%d", disconnectedPlayer.GetIpAddress(), disconnectedPlayer.GetPort())
		playerAddr, err := net.ResolveUDPAddr("udp", addrKey)

		m.players.Players = append(m.players.Players[:playerIndex], m.players.Players[playerIndex+1:]...)
		m.Node.State.Players = m.players

		// Удаляем из LastSent чтобы прекратить отправку новых Ping
		delete(m.Node.LastSent, addrKey)

		// Удаляем все неподтвержденные сообщения для этого адреса
		if err == nil {
			m.Node.RemoveUnconfirmedMessagesForAddr(playerAddr)
		}

		log.Printf("Player ID: %d (role=%v, hasSnake=%v) has timed out and been removed from game list (addr=%s)",
			playerId, playerRole, hasSnake, addrKey)
	} else {
		log.Printf("Player ID: %d has timed out but remains in game list (has active snake)", playerId)
	}

	m.Node.Mu.Unlock()

	// Если игрок был DEPUTY, назначаем нового (БЕЗ мьютекса, так как внутри SendMessage)
	if wasDeputy {
		log.Printf("DEPUTY (player ID: %d) has timed out, selecting new DEPUTY", playerId)
		m.findNewDeputy()
	}
}

// removePlayerUnsafe удаляет игрока БЕЗ захвата мьютекса (для использования когда мьютекс уже захвачен)
// DEPRECATED: Этот метод больше не используется, так как игроки больше не удаляются из списка
// Оставлен для обратной совместимости, но может быть удален в будущем
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
		// Пропускаем себя
		if player.GetId() == m.Node.PlayerInfo.GetId() {
			continue
		}
		// Только NORMAL игроки могут стать DEPUTY (не VIEWER!)
		if player.GetRole() != pb.NodeRole_NORMAL {
			continue
		}
		// Проверяем что у игрока есть живая змейка
		hasAliveSnake := false
		for _, snake := range m.Node.State.Snakes {
			if snake.GetPlayerId() == player.GetId() && snake.GetState() == pb.GameState_Snake_ALIVE {
				hasAliveSnake = true
				break
			}
		}
		if hasAliveSnake {
			playerToAssign = player
			break
		}
	}
	m.Node.Mu.Unlock()

	// Вызываем assignDeputy БЕЗ мьютекса, так как внутри SendMessage
	if playerToAssign != nil {
		m.assignDeputy(playerToAssign)
	} else {
		log.Printf("No NORMAL players with alive snakes available to become DEPUTY")
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
		// Это сообщение отправляет старый MASTER новому MASTER (который был DEPUTY)
		// Но новый MASTER уже стал MASTER через becomeMaster() на стороне Player
		// Это сообщение получает старый MASTER (который еще не остановился)
		// Поэтому просто игнорируем его - старый MASTER остановится через transferMasterToDeputy()
		log.Printf("Received MASTER transfer notification, ignoring (already handled)")
		return

	case roleChangeMsg.GetSenderRole() == pb.NodeRole_NORMAL && roleChangeMsg.GetReceiverRole() == pb.NodeRole_VIEWER:
		// NORMAL Player -> VIEWER
		playerId := msg.GetSenderId()
		log.Printf("Player ID: %d requests to become VIEWER. Converting snake to ZOMBIE.", playerId)

		m.Node.Mu.Lock()

		// Проверяем был ли игрок DEPUTY - если да, то нужно найти нового
		wasDeputy := false
		var playerIP string
		var playerPort int32

		for _, player := range m.players.Players {
			if player.GetId() == playerId {
				if player.GetRole() == pb.NodeRole_DEPUTY {
					wasDeputy = true
				}
				player.Role = pb.NodeRole_VIEWER.Enum()
				// Сохраняем адрес игрока для отправки подтверждения
				playerIP = player.GetIpAddress()
				playerPort = player.GetPort()
				break
			}
		}

		m.makeSnakeZombie(playerId)

		// Проверяем, остались ли активные игроки (не VIEWER)
		gameEnding := m.checkGameEnd()

		m.Node.Mu.Unlock()

		// Отправляем подтверждение игроку о смене роли
		if playerIP != "" {
			playerAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", playerIP, playerPort))
			if err == nil {
				confirmRoleChangeMsg := &pb.GameMessage{
					MsgSeq:     proto.Int64(m.Node.MsgSeq),
					SenderId:   proto.Int32(m.Node.PlayerInfo.GetId()),
					ReceiverId: proto.Int32(playerId),
					Type: &pb.GameMessage_RoleChange{
						RoleChange: &pb.GameMessage_RoleChangeMsg{
							SenderRole:   pb.NodeRole_MASTER.Enum(),
							ReceiverRole: pb.NodeRole_VIEWER.Enum(),
						},
					},
				}
				m.Node.SendMessage(confirmRoleChangeMsg, playerAddr)
				log.Printf("Sent RoleChange confirmation to player ID: %d", playerId)
			}
		}

		if gameEnding {
			log.Printf("All active players have left. Game is ending.")
			m.endGame()
			return
		}

		// Если игрок был DEPUTY, назначаем нового
		if wasDeputy {
			log.Printf("DEPUTY (player ID: %d) became VIEWER, selecting new DEPUTY", playerId)
			m.findNewDeputy()
		}

	default:
		log.Printf("Received unknown RoleChangeMsg from player ID: %d (SenderRole: %v, ReceiverRole: %v)",
			msg.GetSenderId(), roleChangeMsg.GetSenderRole(), roleChangeMsg.GetReceiverRole())
	}
}

// checkGameEnd проверяет, остались ли активные игроки (вызывать с захваченным мьютексом)
func (m *Master) checkGameEnd() bool {
	activePlayersCount := 0
	for _, player := range m.players.Players {
		// Считаем только не-VIEWER игроков
		if player.GetRole() != pb.NodeRole_VIEWER {
			activePlayersCount++
		}
	}

	// Игра завершается, если не осталось активных игроков
	return activePlayersCount == 0
}

// endGame завершает игру
func (m *Master) endGame() {
	log.Println("=== GAME OVER ===")
	log.Println("All players have left the game. Game is ending.")

	// Помечаем игру как завершенную
	m.announcement.CanJoin = proto.Bool(false)

	// Опционально: можно закрыть соединения или выполнить cleanup
	// Но по заданию информация о завершенной игре не обязана нигде сохраняться

	log.Println("Game ended successfully.")
}
