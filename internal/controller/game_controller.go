package controller

import (
	"fyne.io/fyne/v2"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	"snake-net-game/internal/model/common"
	"snake-net-game/internal/model/master"
	"snake-net-game/internal/model/player"
	pb "snake-net-game/pkg/proto"
	"time"
)

type GameController struct {
	window     fyne.Window
	multConn   *net.UDPConn
	gameTicker *time.Ticker
	isRunning  bool
}

func NewGameController(window fyne.Window, multConn *net.UDPConn) *GameController {
	return &GameController{
		window:   window,
		multConn: multConn,
	}
}

func (gc *GameController) StartNewGame(config *pb.GameConfig, gameName string) *master.Master {
	masterNode := master.NewMaster(gc.multConn, config, gameName)
	go masterNode.Start()
	return masterNode
}

func (gc *GameController) JoinGame(playerNode *player.Player, playerName string, selectedGame *player.DiscoveredGame, isViewer bool) {
	playerNode.Node.PlayerInfo.Name = proto.String(playerName)
	playerNode.Node.Config = selectedGame.Config
	playerNode.MasterAddr = selectedGame.MasterAddr
	playerNode.AnnouncementMsg = selectedGame.AnnouncementMsg
	playerNode.IsViewer = isViewer

	if isViewer {
		playerNode.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	}

	if playerNode.MasterAddr != nil {
		log.Printf("JoinGame: master addr set to %s, announcement present=%v", playerNode.MasterAddr.String(), playerNode.AnnouncementMsg != nil)
	} else {
		log.Printf("JoinGame: master addr is nil, announcement present=%v", playerNode.AnnouncementMsg != nil)
	}

	playerNode.Start()
}

func (gc *GameController) CreatePlayer() *player.Player {
	playerNode := player.NewPlayer(gc.multConn)
	go playerNode.ReceiveMulticastMessages()
	return playerNode
}

func (gc *GameController) BecomeViewerForPlayer(playerNode *player.Player) {
	if playerNode.MasterAddr == nil {
		return
	}

	playerNode.Node.Mu.Lock()
	currentRole := playerNode.Node.PlayerInfo.GetRole()
	playerId := playerNode.Node.PlayerInfo.GetId()
	if currentRole != pb.NodeRole_NORMAL {
		playerNode.Node.Mu.Unlock()
		return // MASTER и DEPUTY не могут становиться VIEWER
	}

	playerNode.Node.PlayerInfo.Role = pb.NodeRole_VIEWER.Enum()
	playerNode.IsViewer = true
	playerNode.Node.Mu.Unlock()

	roleChangeMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(playerNode.Node.MsgSeq),
		SenderId:   proto.Int32(playerId),
		ReceiverId: proto.Int32(1), // MASTER всегда ID 1
		Type: &pb.GameMessage_RoleChange{
			RoleChange: &pb.GameMessage_RoleChangeMsg{
				SenderRole:   pb.NodeRole_NORMAL.Enum(), // Текущая роль отправителя
				ReceiverRole: pb.NodeRole_VIEWER.Enum(), // Желаемая новая роль
			},
		},
	}

	playerNode.Node.SendMessage(roleChangeMsg, playerNode.MasterAddr)
}

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
				var stateCopy *pb.GameState
				if node.PlayerInfo.GetRole() == pb.NodeRole_MASTER {
					stateCopy = common.CompressGameState(node.State, node.Config)
				} else {
					stateCopy = proto.Clone(node.State).(*pb.GameState)
				}
				configCopy := proto.Clone(node.Config).(*pb.GameConfig)

				var playerScore int32
				playerFound := false
				for _, gamePlayer := range node.State.GetPlayers().GetPlayers() {
					if gamePlayer.GetId() == node.PlayerInfo.GetId() {
						playerScore = gamePlayer.GetScore()
						playerFound = true
						break
					}
				}

				if !playerFound {
					playerScore = 0
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
				var stateCopy *pb.GameState
				if playerNode.Node.PlayerInfo.GetRole() == pb.NodeRole_MASTER {
					stateCopy = common.CompressGameState(playerNode.Node.State, playerNode.Node.Config)
				} else {
					stateCopy = proto.Clone(playerNode.Node.State).(*pb.GameState)
				}
				configCopy := proto.Clone(playerNode.Node.Config).(*pb.GameConfig)

				var playerScore int32
				playerFound := false
				for _, gamePlayer := range playerNode.Node.State.GetPlayers().GetPlayers() {
					if gamePlayer.GetId() == playerNode.Node.PlayerInfo.GetId() {
						playerScore = gamePlayer.GetScore()
						playerFound = true
						break
					}
				}

				if !playerFound {
					playerScore = 0
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

func (gc *GameController) StopGameLoop() {
	if gc.gameTicker != nil {
		gc.gameTicker.Stop()
	}
	gc.isRunning = false
}

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
				isOpposite = newDirection == pb.Direction_DOWN
			case pb.Direction_DOWN:
				isOpposite = newDirection == pb.Direction_UP
			case pb.Direction_LEFT:
				isOpposite = newDirection == pb.Direction_RIGHT
			case pb.Direction_RIGHT:
				isOpposite = newDirection == pb.Direction_LEFT
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

	playerNode.Node.Mu.Lock()
	if playerNode.Node.PlayerInfo.GetRole() == pb.NodeRole_MASTER {
		playerId := playerNode.Node.PlayerInfo.GetId()
		for _, snake := range playerNode.Node.State.Snakes {
			if snake.GetPlayerId() == playerId {
				currentDir := snake.GetHeadDirection()
				isOpposite := false
				switch currentDir {
				case pb.Direction_UP:
					isOpposite = newDirection == pb.Direction_DOWN
				case pb.Direction_DOWN:
					isOpposite = newDirection == pb.Direction_UP
				case pb.Direction_LEFT:
					isOpposite = newDirection == pb.Direction_RIGHT
				case pb.Direction_RIGHT:
					isOpposite = newDirection == pb.Direction_LEFT
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

	masterId := playerNode.Node.GetPlayerIdByAddress(playerNode.MasterAddr)
	playerId := playerNode.Node.PlayerInfo.GetId()

	steerMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(0),
		SenderId:   proto.Int32(playerId),
		ReceiverId: proto.Int32(masterId),
		Type: &pb.GameMessage_Steer{
			Steer: &pb.GameMessage_SteerMsg{
				Direction: newDirection.Enum(),
			},
		},
	}

	log.Printf("Player sending SteerMsg: sender_id=%d, receiver_id=%d, direction=%v", playerId, masterId, newDirection)
	playerNode.Node.SendMessage(steerMsg, playerNode.MasterAddr)
}
