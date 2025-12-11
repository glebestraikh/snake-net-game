package view

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"math/rand"
	"snake-net-game/internal/controller"
	"snake-net-game/internal/model/master"
	pb "snake-net-game/pkg/proto"
	"time"
)

// MasterGameView представляет экран игры для мастера
type MasterGameView struct {
	window     fyne.Window
	controller *controller.GameController
	config     *pb.GameConfig
	gameName   string
	renderer   *GameRenderer
	infoPanel  *InfoPanel
	scoreLabel *widget.Label
	nameLabel  *widget.Label
	roleLabel  *widget.Label
}

// NewMasterGameView создает новое представление игры мастера
func NewMasterGameView(window fyne.Window, controller *controller.GameController, config *pb.GameConfig, gameName string) *MasterGameView {
	return &MasterGameView{
		window:     window,
		controller: controller,
		config:     config,
		gameName:   gameName,
		renderer:   NewGameRenderer(),
	}
}

// Show отображает экран игры мастера
func (mgv *MasterGameView) Show() {
	masterNode := mgv.controller.StartNewGame(mgv.config, mgv.gameName)

	gameContent := mgv.renderer.CreateGameContent(mgv.config)

	mgv.scoreLabel = widget.NewLabel("Счет: 0")
	mgv.nameLabel = widget.NewLabel("Имя: ")
	mgv.roleLabel = widget.NewLabel("Роль: ")

	mgv.infoPanel = NewInfoPanel(mgv.config,
		func() {
			// Главное меню
			mgv.controller.StopGameLoop()
			mainView := NewMainView(mgv.window, mgv.controller)
			mainView.ShowMainMenu()
		},
		func() {
			// Выход из приложения
			mgv.controller.StopGameLoop()
			mgv.window.Close()
		},
		func() {
			// Стать наблюдателем (для мастера пока недоступно)
			// TODO: реализовать переход мастера в VIEWER
		},
		mgv.scoreLabel, mgv.nameLabel, mgv.roleLabel, false)

	splitContent := container.NewHSplit(
		gameContent,
		mgv.infoPanel.GetContainer(),
	)
	splitContent.SetOffset(0.7)

	mgv.window.SetContent(splitContent)

	mgv.startGameLoop(masterNode, gameContent)
}

func (mgv *MasterGameView) startGameLoop(masterNode *master.Master, gameContent *fyne.Container) {
	rand.NewSource(time.Now().UnixNano())

	// Обработка клавиш
	mgv.window.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		mgv.controller.HandleKeyInputForMaster(e, masterNode.Node)
	})

	mgv.controller.StartGameLoop(masterNode.Node, func(state *pb.GameState, config *pb.GameConfig, score int32, name string, role pb.NodeRole) {
		mgv.scoreLabel.SetText(fmt.Sprintf("Счет: %d", score))
		mgv.nameLabel.SetText(fmt.Sprintf("Имя: %v", name))
		mgv.roleLabel.SetText(fmt.Sprintf("Роль: %s", FormatRole(role)))
		mgv.renderer.RenderGameState(gameContent, state, config)
		mgv.infoPanel.UpdateInfoPanel(state, role)
	})
}
