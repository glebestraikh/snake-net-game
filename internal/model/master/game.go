package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"math/rand"
	"net"
	pb "snake-net-game/pkg/proto"
)

// GenerateFood генерация еды
func (m *Master) GenerateFood() {
	// На поле в каждый момент времени присутствует еда в количестве,
	// вычисляемом по формуле (food_static + (число ALIVE-змеек)).
	requireFood := m.Node.Config.GetFoodStatic() + int32(len(m.Node.State.Snakes))
	currentFood := int32(len(m.Node.State.GetFoods()))

	if currentFood < requireFood {
		needNum := requireFood - currentFood
		for i := int32(0); i < needNum; i++ {
			coord := m.findEmptyCell()
			if coord != nil {
				m.Node.State.Foods = append(m.Node.State.Foods, coord)
			} else {
				log.Println("No empty cells available for new food.")
				break
			}
		}
	}
}

func (m *Master) findEmptyCell() *pb.GameState_Coord {
	numCells := m.Node.Config.GetWidth() * m.Node.Config.GetHeight()
	for attempts := int32(0); attempts < numCells; attempts++ {
		x := rand.Int31n(m.Node.Config.GetWidth())
		y := rand.Int31n(m.Node.Config.GetHeight())
		if m.isCellEmpty(x, y) {
			return &pb.GameState_Coord{X: proto.Int32(x), Y: proto.Int32(y)}
		}
	}
	return nil
}

func (m *Master) isCellEmpty(x, y int32) bool {
	for _, snake := range m.Node.State.Snakes {
		for _, point := range snake.Points {
			if point.GetX() == x && point.GetY() == y {
				return false
			}
		}
	}

	for _, food := range m.Node.State.Foods {
		if food.GetX() == x && food.GetY() == y {
			return false
		}
	}

	return true
}

// UpdateGameState обновление состояния игры
func (m *Master) UpdateGameState() {
	for _, snake := range m.Node.State.Snakes {
		m.moveSnake(snake)
	}

	m.checkCollisions()

	// Проверяем нужно ли передать роль MASTER (если сам MASTER был убит)
	if m.needTransferMaster {
		m.needTransferMaster = false
		log.Printf("MASTER was killed, transferring control to DEPUTY and becoming observer")
		// Обработаем это в отдельной горутине после освобождения мьютекса
		go m.transferMasterToDeputy()
	}

	// Проверяем нужно ли назначить нового DEPUTY после смерти предыдущего
	if m.needNewDeputy {
		m.needNewDeputy = false
		// Вызываем findNewDeputy БЕЗ мьютекса (он будет захвачен внутри)
		go m.findNewDeputy()
	}
}

func (m *Master) moveSnake(snake *pb.GameState_Snake) {
	head := snake.Points[0]
	newHead := &pb.GameState_Coord{
		X: proto.Int32(head.GetX()),
		Y: proto.Int32(head.GetY()),
	}

	// изменение координат
	switch snake.GetHeadDirection() {
	case pb.Direction_UP:
		newHead.Y = proto.Int32(newHead.GetY() - 1)
	case pb.Direction_DOWN:
		newHead.Y = proto.Int32(newHead.GetY() + 1)
	case pb.Direction_LEFT:
		newHead.X = proto.Int32(newHead.GetX() - 1)
	case pb.Direction_RIGHT:
		newHead.X = proto.Int32(newHead.GetX() + 1)
	}

	// поведение при столкновении со стеной
	if newHead.GetX() < 0 {
		newHead.X = proto.Int32(m.Node.Config.GetWidth() - 1)
	} else if newHead.GetX() >= m.Node.Config.GetWidth() {
		newHead.X = proto.Int32(0)
	}
	if newHead.GetY() < 0 {
		newHead.Y = proto.Int32(m.Node.Config.GetHeight() - 1)
	} else if newHead.GetY() >= m.Node.Config.GetHeight() {
		newHead.Y = proto.Int32(0)
	}

	// добавляем новую голову
	snake.Points = append([]*pb.GameState_Coord{newHead}, snake.Points...)
	if !m.isFoodEaten(newHead) {
		snake.Points = snake.Points[:len(snake.Points)-1]
	} else {
		// игрок заработал +1 балл
		snakeId := snake.GetPlayerId()
		for _, player := range m.players.GetPlayers() {
			if player.GetId() == snakeId {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
		// Также обновляем в State.Players для синхронизации
		for _, player := range m.Node.State.Players.GetPlayers() {
			if player.GetId() == snakeId {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
	}
}

func (m *Master) isFoodEaten(head *pb.GameState_Coord) bool {
	for i, food := range m.Node.State.Foods {
		if head.GetX() == food.GetX() && head.GetY() == food.GetY() {
			m.Node.State.Foods = append(m.Node.State.Foods[:i], m.Node.State.Foods[i+1:]...)
			return true
		}
	}
	return false
}

// Проверяем столкновения с другими змеями
func (m *Master) checkCollisions() {
	heads := make(map[string]int32)

	for _, snake := range m.Node.State.Snakes {
		head := snake.Points[0]
		point := fmt.Sprintf("%d,%d", head.GetX(), head.GetY())
		heads[point] = snake.GetPlayerId()
	}

	// проверяем, есть ли клетки с более чем одной головой
	for key := range heads {
		count := 0
		var crashedPlayers []int32
		for k, pid := range heads {
			if k == key {
				count++
				crashedPlayers = append(crashedPlayers, pid)
			}
		}
		// несколько голов на одной клетке -- все погибают
		if count > 1 {
			for _, pid := range crashedPlayers {
				m.killSnake(pid, pid)
			}
		}
	}

	// проверяем столкновения головы змейки с телом других змей
	for _, snake := range m.Node.State.Snakes {
		head := snake.Points[0]
		headX, headY := head.GetX(), head.GetY()
		for _, otherSnake := range m.Node.State.Snakes {
			for i, point := range otherSnake.Points {
				// если это собственная змейка и это голова, пропускаем
				if otherSnake.GetPlayerId() == snake.GetPlayerId() && i == 0 {
					continue
				}
				if point.GetX() == headX && point.GetY() == headY {
					m.killSnake(snake.GetPlayerId(), otherSnake.GetPlayerId())
				}
			}
		}
	}
}

// убираем умершую змею
func (m *Master) killSnake(crashedPlayerId, killer int32) {

	var indexToRemove int
	var snakeToRemove *pb.GameState_Snake
	for index, snake := range m.Node.State.Snakes {
		if snake.GetPlayerId() == crashedPlayerId {
			indexToRemove = index
			snakeToRemove = snake
			break
		}
	}

	if snakeToRemove != nil {
		for _, point := range snakeToRemove.Points {
			if rand.Float32() < 0.5 {
				// заменяем на еду
				newFood := &pb.GameState_Coord{
					X: proto.Int32(point.GetX()),
					Y: proto.Int32(point.GetY()),
				}
				m.Node.State.Foods = append(m.Node.State.Foods, newFood)
			}
		}
		m.Node.State.Snakes = append(m.Node.State.Snakes[:indexToRemove], m.Node.State.Snakes[indexToRemove+1:]...)
	}

	if crashedPlayerId != killer {
		// Обновляем счет в m.players
		for _, player := range m.players.Players {
			if player.GetId() == killer {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
		// Также обновляем в State.Players для синхронизации
		for _, player := range m.Node.State.Players.GetPlayers() {
			if player.GetId() == killer {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
	}

	if crashedPlayerId != m.Node.PlayerInfo.GetId() {
		// Сохраняем информацию о игроке (НЕ удаляем его из списка!)
		var wasDeputy bool
		var playerAddr *net.UDPAddr

		for _, player := range m.players.Players {
			if player.GetId() == crashedPlayerId {
				wasDeputy = player.GetRole() == pb.NodeRole_DEPUTY
				// Устанавливаем роль VIEWER для умершего игрока
				player.Role = pb.NodeRole_VIEWER.Enum()

				// Также обновляем в State.Players для синхронизации
				for _, statePlayer := range m.Node.State.Players.GetPlayers() {
					if statePlayer.GetId() == crashedPlayerId {
						statePlayer.Role = pb.NodeRole_VIEWER.Enum()
						break
					}
				}

				// Сохраняем адрес для списка наблюдателей
				addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				addr, err := net.ResolveUDPAddr("udp", addrStr)
				if err == nil {
					playerAddr = addr
				}
				break
			}
		}

		// НЕ вызываем removePlayerUnsafe! Игрок остается в списке игроков
		// Это позволяет UI продолжать отображать его роль и счет
		log.Printf("Player ID: %d has crashed but remains in game as observer.", crashedPlayerId)

		// Добавляем адрес игрока в список наблюдателей
		// Игрок продолжит получать StateMsg и наблюдать за игрой
		if playerAddr != nil {
			// Проверяем, не добавлен ли уже
			alreadyAdded := false
			for _, addr := range m.observerAddrs {
				if addr.String() == playerAddr.String() {
					alreadyAdded = true
					break
				}
			}
			if !alreadyAdded {
				m.observerAddrs = append(m.observerAddrs, playerAddr)
				log.Printf("Player ID: %d added to observers, will continue receiving game updates", crashedPlayerId)
			}
		}

		// Если был DEPUTY, нужно назначить нового после освобождения мьютекса
		if wasDeputy {
			log.Printf("DEPUTY (player ID: %d) was killed, need to select new DEPUTY", crashedPlayerId)
			m.needNewDeputy = true
		}
	} else {
		// Сам MASTER был убит!
		log.Printf("MASTER (player ID: %d) has been killed!", crashedPlayerId)
		// MASTER должен передать управление DEPUTY и стать наблюдателем
		// Устанавливаем флаг что MASTER убит - обработаем это после освобождения мьютекса
		m.needTransferMaster = true
	}
}

func (m *Master) hasFreeSquare(state *pb.GameState, config *pb.GameConfig, squareSize int32) (bool, *pb.GameState_Coord) {
	width := config.GetWidth()
	height := config.GetHeight()

	occupied := make([][]bool, width)
	for i := range occupied {
		occupied[i] = make([]bool, height)
	}

	// Отмечаем клетки, занятые змейками
	for _, snake := range state.Snakes {
		for _, point := range snake.Points {
			x := ((point.GetX() % width) + width) % width
			y := ((point.GetY() % height) + height) % height
			occupied[x][y] = true
		}
	}

	// Проверяем все возможные начальные точки квадрата (с учётом тора)
	for startX := int32(0); startX < width; startX++ {
		for startY := int32(0); startY < height; startY++ {
			if isSquareFreeOnTorus(occupied, startX, startY, squareSize, width, height) {
				return true, &pb.GameState_Coord{X: proto.Int32(startX), Y: proto.Int32(startY)}
			}
		}
	}

	return false, nil
}

func isSquareFreeOnTorus(occupied [][]bool, startX, startY, squareSize, width, height int32) bool {
	for dx := int32(0); dx < squareSize; dx++ {
		for dy := int32(0); dy < squareSize; dy++ {
			x := (startX + dx) % width
			y := (startY + dy) % height
			if occupied[x][y] {
				return false
			}
		}
	}
	return true
}

func isSquareFree(occupied [][]bool, startX, startY, squareSize int32) bool {
	for x := startX; x < startX+squareSize; x++ {
		for y := startY; y < startY+squareSize; y++ {
			if occupied[x][y] {
				return false
			}
		}
	}
	return true
}

// transferMasterToDeputy передает роль MASTER к DEPUTY когда сам MASTER был убит
func (m *Master) transferMasterToDeputy() {
	m.Node.Mu.Lock()

	// Находим DEPUTY
	var deputy *pb.GamePlayer
	for _, player := range m.players.Players {
		if player.GetRole() == pb.NodeRole_DEPUTY {
			deputy = player
			break
		}
	}

	if deputy == nil {
		log.Printf("No DEPUTY found to transfer MASTER role to!")
		m.Node.Mu.Unlock()
		return
	}

	deputyId := deputy.GetId()
	deputyAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", deputy.GetIpAddress(), deputy.GetPort()))
	if err != nil {
		log.Printf("Error resolving DEPUTY address: %v", err)
		m.Node.Mu.Unlock()
		return
	}

	log.Printf("Transferring MASTER role to DEPUTY (player ID: %d)", deputyId)

	// ОБНОВЛЯЕМ РОЛИ АТОМАРНО
	// Это предотвращает отправку промежуточных некорректных состояний
	deputy.Role = pb.NodeRole_MASTER.Enum()

	// Метим игру как недоступную на этом узле (на всякий случай)
	m.announcement.CanJoin = proto.Bool(false)

	// Останавливаем работу этого MASTER (он теперь наблюдатель)
	log.Printf("Old MASTER becoming VIEWER observer, stopping master duties\n")

	// Устанавливаем новый адрес мастера (теперь это DEPUTY)
	m.Node.MasterAddr = deputyAddr
	// Меняем роль на VIEWER
	m.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	// Устанавливаем флаг stopped, чтобы управляющие горутины (sendStateMessage) немедленно остановились
	m.stopped = true

	// Очищаем очередь неподтвержденных сообщений, так как мы больше не мастер
	log.Printf("Clearing %d unconfirmed Master messages", m.Node.UnconfirmedMessages())
	m.Node.ClearUnconfirmedMessages()

	// ОЧЕНЬ ВАЖНО: уведомляем UI
	m.Node.Cond.Broadcast()
	m.Node.Mu.Unlock()

	// Собираем адреса всех игроков для уведомления
	// Мы уведомляем ВСЕХ игроков о том, что новый мастер теперь DEPUTY,
	// и что мы сами стали VIEWER. Это позволит игрокам сразу переключиться.
	var playersToNotify []struct {
		addr *net.UDPAddr
		id   int32
	}
	for _, p := range m.players.Players {
		if p.GetId() == m.Node.PlayerInfo.GetId() {
			continue
		}
		addrStr := fmt.Sprintf("%s:%d", p.GetIpAddress(), p.GetPort())
		if a, err := net.ResolveUDPAddr("udp", addrStr); err == nil {
			playersToNotify = append(playersToNotify, struct {
				addr *net.UDPAddr
				id   int32
			}{a, p.GetId()})
		}
	}

	for _, pInfo := range playersToNotify {
		roleChangeMsg := &pb.GameMessage{
			MsgSeq:   proto.Int64(m.Node.MsgSeq),
			SenderId: proto.Int32(m.Node.PlayerInfo.GetId()),
			// В сообщении о передаче мастерства ReceiverId содержит ID нового мастера (deputyId),
			// а не ID получателя пакета — иначе каждый получатель будет считать себя новым MASTER.
			ReceiverId: proto.Int32(deputyId),
			Type: &pb.GameMessage_RoleChange{
				RoleChange: &pb.GameMessage_RoleChangeMsg{
					SenderRole:   pb.NodeRole_VIEWER.Enum(),
					ReceiverRole: pb.NodeRole_MASTER.Enum(), // Для всех игроков Receiver (Deputy) теперь Master
				},
			},
		}

		m.Node.SendMessage(roleChangeMsg, pInfo.addr)
		log.Printf("Sent RoleChange (New Master ID: %d) to player ID: %d at %v", deputyId, pInfo.id, pInfo.addr)
	}

	// Закрываем stopChan чтобы остановить управляющие горутины окончательно
	close(m.stopChan)

	log.Printf("Old MASTER successfully transitioned to VIEWER mode")
	log.Printf("Old MASTER (now VIEWER) will continue receiving StateMsg from new MASTER")
}
