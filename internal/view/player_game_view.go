package view

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"math/rand"
	"snake-net-game/internal/controller"
	"snake-net-game/internal/model/player"
	pb "snake-net-game/pkg/proto"
	"time"
)

// PlayerGameView представляет экран игры для игрока
type PlayerGameView struct {
	window       fyne.Window
	controller   *controller.GameController
	playerNode   *player.Player
	playerName   string
	selectedGame *player.DiscoveredGame
	isViewer     bool
	renderer     *GameRenderer
	infoPanel    *InfoPanel
	scoreLabel   *widget.Label
	nameLabel    *widget.Label
	roleLabel    *widget.Label
}

// NewPlayerGameView создает новое представление игры игрока
func NewPlayerGameView(window fyne.Window, controller *controller.GameController, playerNode *player.Player, playerName string, selectedGame *player.DiscoveredGame, isViewer bool) *PlayerGameView {
	return &PlayerGameView{
		window:       window,
		controller:   controller,
		playerNode:   playerNode,
		playerName:   playerName,
		selectedGame: selectedGame,
		isViewer:     isViewer,
		renderer:     NewGameRenderer(),
	}
}

// Show отображает экран игры игрока
func (pgv *PlayerGameView) Show() {
	pgv.controller.JoinGame(pgv.playerNode, pgv.playerName, pgv.selectedGame, pgv.isViewer)

	gameContent := pgv.renderer.CreateGameContent(pgv.playerNode.Node.Config)

	pgv.scoreLabel = widget.NewLabel("Счет: 0")
	pgv.nameLabel = widget.NewLabel("Имя: ")
	pgv.roleLabel = widget.NewLabel("Роль: ")

	pgv.infoPanel = NewInfoPanel(pgv.playerNode.Node.Config,
		func() {
			// Главное меню
			pgv.controller.StopGameLoop()
			mainView := NewMainView(pgv.window, pgv.controller)
			mainView.ShowMainMenu()
		},
		func() {
			// Выход из приложения
			pgv.controller.StopGameLoop()
			pgv.window.Close()
		},
		func() {
			// Стать наблюдателем
			pgv.controller.BecomeViewerForPlayer(pgv.playerNode)
		},
		pgv.scoreLabel, pgv.nameLabel, pgv.roleLabel)

	splitContent := container.NewHSplit(
		gameContent,
		pgv.infoPanel.GetContainer(),
	)
	splitContent.SetOffset(0.7)

	pgv.window.SetContent(splitContent)

	pgv.startGameLoop(gameContent)
}

func (pgv *PlayerGameView) startGameLoop(gameContent *fyne.Container) {
	rand.NewSource(time.Now().UnixNano())

	// Обработка клавиш
	pgv.window.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		pgv.controller.HandleKeyInputForPlayer(e, pgv.playerNode)
	})

	pgv.controller.StartGameLoopForPlayer(pgv.playerNode, func(state *pb.GameState, config *pb.GameConfig, score int32, name string, role pb.NodeRole) {
		pgv.scoreLabel.SetText(fmt.Sprintf("Счет: %d", score))
		pgv.nameLabel.SetText(fmt.Sprintf("Имя: %v", name))
		pgv.roleLabel.SetText(fmt.Sprintf("Роль: %v", role))
		pgv.renderer.RenderGameState(gameContent, state, config)
		pgv.infoPanel.UpdateInfoPanel(state)
	})
}
