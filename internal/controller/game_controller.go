package controller

import (
	"fyne.io/fyne/v2"
	"google.golang.org/protobuf/proto"
	"net"
	"snake-net-game/internal/model/common"
	"snake-net-game/internal/model/master"
	"snake-net-game/internal/model/player"
	pb "snake-net-game/pkg/proto"
	"time"
)

// GameController управляет логикой игры
type GameController struct {
	window     fyne.Window
	multConn   *net.UDPConn
	gameTicker *time.Ticker
	isRunning  bool
}

// NewGameController создает новый контроллер игры
func NewGameController(window fyne.Window, multConn *net.UDPConn) *GameController {
	return &GameController{
		window:   window,
		multConn: multConn,
	}
}

// StartNewGame начинает новую игру как мастер
func (gc *GameController) StartNewGame(config *pb.GameConfig) *master.Master {
	masterNode := master.NewMaster(gc.multConn, config)
	go masterNode.Start()
	return masterNode
}

// JoinGame присоединяется к существующей игре
func (gc *GameController) JoinGame(playerNode *player.Player, playerName string, selectedGame *player.DiscoveredGame, isViewer bool) {
	playerNode.Node.PlayerInfo.Name = proto.String(playerName)
	playerNode.Node.Config = selectedGame.Config
	playerNode.MasterAddr = selectedGame.MasterAddr
	playerNode.AnnouncementMsg = selectedGame.AnnouncementMsg
	playerNode.IsViewer = isViewer
	playerNode.Start()
}

// CreatePlayer создает нового игрока
func (gc *GameController) CreatePlayer() *player.Player {
	playerNode := player.NewPlayer(gc.multConn)
	go playerNode.ReceiveMulticastMessages()
	return playerNode
}

// BecomeViewer отправляет RoleChangeMsg для перехода игрока в режим VIEWER
// Только NORMAL игроки могут становиться VIEWER (не MASTER и не DEPUTY)
func (gc *GameController) BecomeViewerForPlayer(playerNode *player.Player) {
	if playerNode.MasterAddr == nil {
		return
	}

	// Проверяем что игрок NORMAL (не MASTER и не DEPUTY)
	playerNode.Node.Mu.Lock()
	currentRole := playerNode.Node.PlayerInfo.GetRole()
	if currentRole != pb.NodeRole_NORMAL {
		playerNode.Node.Mu.Unlock()
		return // MASTER и DEPUTY не могут становиться VIEWER
	}

	// Обновляем локальную роль
	playerNode.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	playerNode.IsViewer = true
	playerNode.Node.Mu.Unlock()

	roleChangeMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(playerNode.Node.MsgSeq),
		SenderId:   proto.Int32(playerNode.Node.PlayerInfo.GetId()),
		ReceiverId: proto.Int32(1), // MASTER всегда ID 1
		Type: &pb.GameMessage_RoleChange{
			RoleChange: &pb.GameMessage_RoleChangeMsg{
				SenderRole:   pb.NodeRole_VIEWER.Enum(), // Новая роль отправителя
				ReceiverRole: nil,                       // Роль получателя не меняется
			},
		},
	}

	playerNode.Node.SendMessage(roleChangeMsg, playerNode.MasterAddr)
}

// StartGameLoop запускает игровой цикл для мастера
func (gc *GameController) StartGameLoop(node *common.Node, updateFunc func(*pb.GameState, *pb.GameConfig, int32, string, pb.NodeRole)) {
	gc.gameTicker = time.NewTicker(time.Millisecond * 60)
	gc.isRunning = true

	if node.State == nil {
		node.Mu.Lock()
		for node.State == nil {
			node.Cond.Wait()
		}
		node.Mu.Unlock()
	}

	go func() {
		for gc.isRunning {
			select {
			case <-gc.gameTicker.C:
				node.Mu.Lock()
				if node.State == nil {
					node.Mu.Unlock()
					continue
				}
				stateCopy := proto.Clone(node.State).(*pb.GameState)
				configCopy := proto.Clone(node.Config).(*pb.GameConfig)

				var playerScore int32
				for _, gamePlayer := range node.State.GetPlayers().GetPlayers() {
					if gamePlayer.GetId() == node.PlayerInfo.GetId() {
						playerScore = gamePlayer.GetScore()
						break
					}
				}
				playerName := node.PlayerInfo.GetName()
				playerRole := node.PlayerInfo.GetRole()
				node.Mu.Unlock()

				fyne.Do(func() {
					updateFunc(stateCopy, configCopy, playerScore, playerName, playerRole)
				})
			}
		}
	}()
}

// StartGameLoopForPlayer запускает игровой цикл для игрока
func (gc *GameController) StartGameLoopForPlayer(playerNode *player.Player, updateFunc func(*pb.GameState, *pb.GameConfig, int32, string, pb.NodeRole)) {
	gc.gameTicker = time.NewTicker(time.Millisecond * 60)
	gc.isRunning = true

	go func() {
		playerNode.Node.Mu.Lock()
		for playerNode.Node.State == nil {
			playerNode.Node.Cond.Wait()
		}
		playerNode.Node.Mu.Unlock()

		for gc.isRunning {
			select {
			case <-gc.gameTicker.C:
				playerNode.Node.Mu.Lock()
				if playerNode.Node.State == nil {
					playerNode.Node.Mu.Unlock()
					continue
				}
				stateCopy := proto.Clone(playerNode.Node.State).(*pb.GameState)
				configCopy := proto.Clone(playerNode.Node.Config).(*pb.GameConfig)

				var playerScore int32
				for _, gamePlayer := range playerNode.Node.State.GetPlayers().GetPlayers() {
					if gamePlayer.GetId() == playerNode.Node.PlayerInfo.GetId() {
						playerScore = gamePlayer.GetScore()
						break
					}
				}
				playerName := playerNode.Node.PlayerInfo.GetName()
				playerRole := playerNode.Node.PlayerInfo.GetRole()
				playerNode.Node.Mu.Unlock()

				fyne.Do(func() {
					updateFunc(stateCopy, configCopy, playerScore, playerName, playerRole)
				})
			}
		}
	}()
}

// StopGameLoop останавливает игровой цикл
func (gc *GameController) StopGameLoop() {
	if gc.gameTicker != nil {
		gc.gameTicker.Stop()
	}
	gc.isRunning = false
}

// HandleKeyInputForMaster обрабатывает ввод клавиш для мастера
func (gc *GameController) HandleKeyInputForMaster(e *fyne.KeyEvent, node *common.Node) {
	var newDirection pb.Direction

	switch e.Name {
	case fyne.KeyW, fyne.KeyUp:
		newDirection = pb.Direction_UP
	case fyne.KeyS, fyne.KeyDown:
		newDirection = pb.Direction_DOWN
	case fyne.KeyA, fyne.KeyLeft:
		newDirection = pb.Direction_LEFT
	case fyne.KeyD, fyne.KeyRight:
		newDirection = pb.Direction_RIGHT
	default:
		return
	}

	node.Mu.Lock()
	if node.State == nil || node.State.Snakes == nil {
		node.Mu.Unlock()
		return
	}

	for _, snake := range node.State.Snakes {
		if snake.GetPlayerId() == node.PlayerInfo.GetId() {
			currentDirection := snake.GetHeadDirection()

			isOpposite := false
			switch currentDirection {
			case pb.Direction_UP:
				isOpposite = (newDirection == pb.Direction_DOWN)
			case pb.Direction_DOWN:
				isOpposite = (newDirection == pb.Direction_UP)
			case pb.Direction_LEFT:
				isOpposite = (newDirection == pb.Direction_RIGHT)
			case pb.Direction_RIGHT:
				isOpposite = (newDirection == pb.Direction_LEFT)
			}

			if !isOpposite {
				snake.HeadDirection = newDirection.Enum()
			}
			break
		}
	}
	node.Mu.Unlock()
}

// HandleKeyInputForPlayer обрабатывает ввод клавиш для игрока
func (gc *GameController) HandleKeyInputForPlayer(e *fyne.KeyEvent, playerNode *player.Player) {
	var newDirection pb.Direction

	switch e.Name {
	case fyne.KeyW, fyne.KeyUp:
		newDirection = pb.Direction_UP
	case fyne.KeyS, fyne.KeyDown:
		newDirection = pb.Direction_DOWN
	case fyne.KeyA, fyne.KeyLeft:
		newDirection = pb.Direction_LEFT
	case fyne.KeyD, fyne.KeyRight:
		newDirection = pb.Direction_RIGHT
	default:
		return
	}

	// Если мы сами MASTER, меняем направление змейки напрямую
	playerNode.Node.Mu.Lock()
	if playerNode.Node.PlayerInfo.GetRole() == pb.NodeRole_MASTER {
		playerId := playerNode.Node.PlayerInfo.GetId()
		for _, snake := range playerNode.Node.State.Snakes {
			if snake.GetPlayerId() == playerId {
				// Проверяем, не пытаемся ли мы повернуть в противоположном направлении
				currentDir := snake.GetHeadDirection()
				isOpposite := false
				switch currentDir {
				case pb.Direction_UP:
					isOpposite = (newDirection == pb.Direction_DOWN)
				case pb.Direction_DOWN:
					isOpposite = (newDirection == pb.Direction_UP)
				case pb.Direction_LEFT:
					isOpposite = (newDirection == pb.Direction_RIGHT)
				case pb.Direction_RIGHT:
					isOpposite = (newDirection == pb.Direction_LEFT)
				}

				if !isOpposite {
					snake.HeadDirection = newDirection.Enum()
				}
				break
			}
		}
		playerNode.Node.Mu.Unlock()
		return
	}
	playerNode.Node.Mu.Unlock()

	// Если мы не MASTER, отправляем SteerMsg мастеру
	if playerNode.MasterAddr == nil {
		return // Нет адреса мастера
	}

	steerMsg := &pb.GameMessage{
		MsgSeq: proto.Int64(playerNode.Node.MsgSeq),
		Type: &pb.GameMessage_Steer{
			Steer: &pb.GameMessage_SteerMsg{
				Direction: newDirection.Enum(),
			},
		},
	}

	playerNode.Node.SendMessage(steerMsg, playerNode.MasterAddr)
}
