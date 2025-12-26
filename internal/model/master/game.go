package master

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"math/rand"
	"net"
	pb "snake-net-game/pkg/proto"
)

func (m *Master) GenerateFood() {
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

func (m *Master) UpdateGameState() {
	for _, snake := range m.Node.State.Snakes {
		m.moveSnake(snake)
	}

	m.checkCollisions()

	if m.needTransferMaster {
		m.needTransferMaster = false
		log.Printf("MASTER was killed, transferring control to DEPUTY and becoming observer")
		go m.transferMasterToDeputy()
	}

	if m.needNewDeputy {
		m.needNewDeputy = false
		go m.findNewDeputy()
	}
}

func (m *Master) moveSnake(snake *pb.GameState_Snake) {
	head := snake.Points[0]
	newHead := &pb.GameState_Coord{
		X: proto.Int32(head.GetX()),
		Y: proto.Int32(head.GetY()),
	}

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

	snake.Points = append([]*pb.GameState_Coord{newHead}, snake.Points...)
	if !m.isFoodEaten(newHead) {
		snake.Points = snake.Points[:len(snake.Points)-1]
	} else {
		snakeId := snake.GetPlayerId()
		for _, player := range m.players.GetPlayers() {
			if player.GetId() == snakeId {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
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

func (m *Master) checkCollisions() {
	heads := make(map[string][]int32)

	for _, snake := range m.Node.State.Snakes {
		head := snake.Points[0]
		point := fmt.Sprintf("%d,%d", head.GetX(), head.GetY())
		heads[point] = append(heads[point], snake.GetPlayerId())
	}

	toKill := make(map[int32]int32)

	for point, pids := range heads {
		if len(pids) > 1 {
			log.Printf("Multiple heads on cell %s -> marking %d snakes for death", point, len(pids))
			for _, pid := range pids {
				toKill[pid] = pid
			}
		}
	}

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
					// Если уже помечено на смерть, оставляем существующего убийцу
					if _, exists := toKill[snake.GetPlayerId()]; !exists {
						toKill[snake.GetPlayerId()] = otherSnake.GetPlayerId()
					}
				}
			}
		}
	}

	for crashedPid, killer := range toKill {
		m.killSnake(crashedPid, killer)
	}
}

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
		for _, player := range m.Node.State.Players.GetPlayers() {
			if player.GetId() == killer {
				player.Score = proto.Int32(player.GetScore() + 1)
				break
			}
		}
	}

	if crashedPlayerId != m.Node.PlayerInfo.GetId() {
		var wasDeputy bool
		var playerAddr *net.UDPAddr

		for _, player := range m.players.Players {
			if player.GetId() == crashedPlayerId {
				wasDeputy = player.GetRole() == pb.NodeRole_DEPUTY
				player.Role = pb.NodeRole_VIEWER.Enum()

				for _, statePlayer := range m.Node.State.Players.GetPlayers() {
					if statePlayer.GetId() == crashedPlayerId {
						statePlayer.Role = pb.NodeRole_VIEWER.Enum()
						break
					}
				}

				addrStr := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				addr, err := net.ResolveUDPAddr("udp", addrStr)
				if err == nil {
					playerAddr = addr
				}
				break
			}
		}
		log.Printf("Player ID: %d has crashed but remains in game as observer.", crashedPlayerId)
		if playerAddr != nil {
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
		if wasDeputy {
			log.Printf("DEPUTY (player ID: %d) was killed, need to select new DEPUTY", crashedPlayerId)
			m.needNewDeputy = true
		}
	} else {
		log.Printf("MASTER (player ID: %d) has been killed!", crashedPlayerId)
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

	for _, snake := range state.Snakes {
		for _, point := range snake.Points {
			x := ((point.GetX() % width) + width) % width
			y := ((point.GetY() % height) + height) % height
			occupied[x][y] = true
		}
	}

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

	deputy.Role = pb.NodeRole_MASTER.Enum()

	m.announcement.CanJoin = proto.Bool(false)

	log.Printf("Old MASTER becoming VIEWER observer, stopping master duties\n")

	m.Node.MasterAddr = deputyAddr
	m.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	m.stopped = true

	log.Printf("Clearing %d unconfirmed Master messages", m.Node.UnconfirmedMessages())
	m.Node.ClearUnconfirmedMessages()

	m.Node.Cond.Broadcast()
	m.Node.Mu.Unlock()

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
			MsgSeq:     proto.Int64(m.Node.MsgSeq),
			SenderId:   proto.Int32(m.Node.PlayerInfo.GetId()),
			ReceiverId: proto.Int32(pInfo.id),
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

	close(m.stopChan)

	log.Printf("Old MASTER successfully transitioned to VIEWER mode")
	log.Printf("Old MASTER (now VIEWER) will continue receiving StateMsg from new MASTER")
}
