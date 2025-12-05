package ui

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/protobuf/proto"
	"math/rand"
	"net"
	"snake-net-game/internal/model/common"
	"snake-net-game/internal/model/master"
	pb "snake-net-game/pkg/proto"
	"strconv"
	"time"
)

// ShowGameConfig настройки игры
func ShowGameConfig(w fyne.Window, multConn *net.UDPConn) {
	widthEntry := widget.NewEntry()
	widthEntry.SetText("25")
	heightEntry := widget.NewEntry()
	heightEntry.SetText("25")
	foodEntry := widget.NewEntry()
	foodEntry.SetText("10")
	delayEntry := widget.NewEntry()
	delayEntry.SetText("180")

	startButton := widget.NewButton("Начать игру", func() {
		width, _ := strconv.Atoi(widthEntry.Text)
		height, _ := strconv.Atoi(heightEntry.Text)
		food, _ := strconv.Atoi(foodEntry.Text)
		delay, _ := strconv.Atoi(delayEntry.Text)

		config := &pb.GameConfig{
			Width:        proto.Int32(int32(width)),
			Height:       proto.Int32(int32(height)),
			FoodStatic:   proto.Int32(int32(food)),
			StateDelayMs: proto.Int32(int32(delay)),
		}

		ShowMasterGameScreen(w, config, multConn)
	})

	backButton := widget.NewButton("Назад", func() {
		ShowMainMenu(w, multConn)
	})

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Ширина поля", Widget: widthEntry},
			{Text: "Высота поля", Widget: heightEntry},
			{Text: "Количество еды", Widget: foodEntry},
			{Text: "Задержка (мс)", Widget: delayEntry},
		},
	}

	content := container.NewVBox(
		widget.NewLabelWithStyle("Настройки игры", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		form,
		startButton,
		backButton,
	)

	w.SetContent(container.NewCenter(content))
}

// ShowMasterGameScreen показывает экран игры
func ShowMasterGameScreen(w fyne.Window, config *pb.GameConfig, multConn *net.UDPConn) {
	masterNode := master.NewMaster(multConn, config)
	go masterNode.Start()

	gameContent := CreateGameContent(config)

	scoreLabel := widget.NewLabel("Счет: 0")
	nameLabel := widget.NewLabel("Имя: ")
	roleLabel := widget.NewLabel("Роль: ")
	infoPanel, scoreTable, foodCountLabel := createInfoPanel(config, func() {
		StopGameLoop()
		ShowMainMenu(w, multConn)
	}, scoreLabel, nameLabel, roleLabel)

	splitContent := container.NewHSplit(
		gameContent,
		infoPanel,
	)
	splitContent.SetOffset(0.7)

	w.SetContent(splitContent)

	StartGameLoopForMaster(w, masterNode.Node, gameContent, scoreTable, foodCountLabel,
		func(score int32) { scoreLabel.SetText(fmt.Sprintf("Счет: %d", score)) },
		func(name string) { nameLabel.SetText(fmt.Sprintf("Имя: %v", name)) },
		func(role pb.NodeRole) { roleLabel.SetText(fmt.Sprintf("Роль: %v", role)) },
	)
}

func StartGameLoopForMaster(w fyne.Window, node *common.Node, gameContent *fyne.Container,
	scoreTable *widget.Table, foodCountLabel *widget.Label, updateScore func(int32), updateName func(string), updateRole func(pb.NodeRole)) {
	rand.NewSource(time.Now().UnixNano())

	gameTicker = time.NewTicker(time.Millisecond * 60)

	isRunning = true

	// обработка клавиш
	w.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		handleKeyInputForMaster(e, node)
	})

	if node.State == nil {
		node.Mu.Lock()
		for node.State == nil {
			node.Cond.Wait()
		}
		node.Mu.Unlock()
	}

	go func() {
		for isRunning {
			select {
			case <-gameTicker.C:
				// Быстро копируем данные под мьютексом
				node.Mu.Lock()
				if node.State == nil {
					node.Mu.Unlock()
					continue
				}
				stateCopy := proto.Clone(node.State).(*pb.GameState)
				configCopy := proto.Clone(node.Config).(*pb.GameConfig)
				// Обновление счёта
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

				// Обновляем UI через fyne.Do для безопасного вызова из горутины
				fyne.Do(func() {
					updateScore(playerScore)
					updateName(playerName)
					updateRole(playerRole)
					renderGameState(gameContent, stateCopy, configCopy)
					updateInfoPanel(scoreTable, foodCountLabel, stateCopy)
				})
			}
		}
	}()
}

// handleKeyInput обработка клавиш
func handleKeyInputForMaster(e *fyne.KeyEvent, node *common.Node) {
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

	// Быстро захватываем и освобождаем мьютекс
	node.Mu.Lock()
	if node.State == nil || node.State.Snakes == nil {
		node.Mu.Unlock()
		return
	}

	for _, snake := range node.State.Snakes {
		if snake.GetPlayerId() == node.PlayerInfo.GetId() {
			currentDirection := snake.GetHeadDirection()

			// Проверка на противоположное направление
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
