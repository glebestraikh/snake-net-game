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
		for _, player := range m.players.Players {
			if player.GetId() == killer {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
	}

	if crashedPlayerId != m.Node.PlayerInfo.GetId() {
		// Сохраняем адрес игрока ДО удаления, чтобы отправить ему ErrorMsg позже
		var crashedPlayerAddr *net.UDPAddr
		for _, player := range m.players.Players {
			if player.GetId() == crashedPlayerId {
				addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				addr, err := net.ResolveUDPAddr("udp", addrStr)
				if err == nil {
					crashedPlayerAddr = addr
				}
				break
			}
		}

		m.removePlayerUnsafe(crashedPlayerId)
		log.Printf("Player ID: %d has crashed and been removed.", crashedPlayerId)

		// Сохраняем адрес для отправки ErrorMsg после освобождения мьютекса
		if crashedPlayerAddr != nil {
			m.crashedPlayersToNotify = append(m.crashedPlayersToNotify, crashedPlayerAddr)
		}
	}
}

func (m *Master) hasFreeSquare(state *pb.GameState, config *pb.GameConfig, squareSize int32) (bool, *pb.GameState_Coord) {
	occupied := make([][]bool, config.GetWidth())
	for i := range occupied {
		occupied[i] = make([]bool, config.GetHeight())
	}

	for _, snake := range state.Snakes {
		for _, point := range snake.Points {
			x, y := point.GetX(), point.GetY()
			if x >= 0 && x < config.GetWidth() && y >= 0 && y < config.GetHeight() {
				occupied[x][y] = true
			}
		}
	}

	for startX := int32(0); startX <= config.GetWidth()-squareSize; startX++ {
		for startY := int32(0); startY <= config.GetHeight()-squareSize; startY++ {
			if isSquareFree(occupied, startX, startY, squareSize) {
				return true, &pb.GameState_Coord{X: proto.Int32(startX), Y: proto.Int32(startY)}
			}
		}
	}

	return false, nil
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
