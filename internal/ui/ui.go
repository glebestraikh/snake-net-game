package ui

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"net"
	"snake-net-game/internal/connection"
	pb "snake-net-game/pkg/proto"
	"time"
)

var gameTicker *time.Ticker
var isRunning bool
var tableData [][]string

// ShowMainMenu выводит главное меню
func ShowMainMenu(w fyne.Window, multConn *net.UDPConn) {
	title := widget.NewLabel("Добро пожаловать в Snake Game!")
	title.Alignment = fyne.TextAlignCenter

	newGameButton := widget.NewButton("Новая игра", func() {
		ShowGameConfig(w, multConn)
	})

	joinGameButton := widget.NewButton("Присоединиться к игре", func() {
		ShowJoinGame(w, multConn)
	})

	exitButton := widget.NewButton("Выход", func() {
		w.Close()
	})

	content := container.NewVBox(
		title,
		newGameButton,
		joinGameButton,
		exitButton,
	)

	w.SetContent(container.NewCenter(content))
}

// CreateGameContent создает холст
func CreateGameContent(config *pb.GameConfig) *fyne.Container {
	gameContent := container.NewWithoutLayout()

	windowWidth := float32(config.GetWidth()) * CellSize
	windowHeight := float32(config.GetHeight()) * CellSize
	gameContent.Resize(fyne.NewSize(windowWidth, windowHeight))

	return gameContent
}

// StopGameLoop остановка игры
func StopGameLoop() {
	if gameTicker != nil {
		gameTicker.Stop()
	}
	isRunning = false
}

// createInfoPanel информационная панель
func createInfoPanel(config *pb.GameConfig, onExit func(), scoreLabel *widget.Label, nameLabel *widget.Label, roleLabel *widget.Label) (*fyne.Container, *widget.Table, *widget.Label) {
	// Инициализируем глобальную переменную
	tableData = [][]string{
		{"Name", "Score"},
	}

	scoreTable := widget.NewTable(
		func() (int, int) {
			if len(tableData) == 0 {
				return 0, 0
			}
			return len(tableData), len(tableData[0])
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			if id.Row < len(tableData) && id.Col < len(tableData[0]) {
				cell.(*widget.Label).SetText(tableData[id.Row][id.Col])
			}
		},
	)

	scoreTable.SetColumnWidth(0, 100)
	scoreTable.SetColumnWidth(1, 50)

	scrollableTable := container.NewScroll(scoreTable)
	scrollableTable.SetMinSize(fyne.NewSize(150, 300))

	gameInfo := widget.NewLabel(fmt.Sprintf("Текущая игра:\n\nРазмер: %dx%d\n", config.GetWidth(), config.GetHeight()))
	foodCountLabel := widget.NewLabel("Еда: 0")

	newGameButton := widget.NewButton("Новая игра", onExit)
	exitButton := widget.NewButton("Выйти", onExit)

	content := container.NewVBox(
		container.New(layout.NewPaddedLayout(), scoreLabel),
		container.New(layout.NewPaddedLayout(), nameLabel),
		container.New(layout.NewPaddedLayout(), roleLabel),
		container.New(layout.NewPaddedLayout(), scrollableTable),
		container.New(layout.NewPaddedLayout(), gameInfo),
		container.New(layout.NewPaddedLayout(), foodCountLabel),
		container.New(layout.NewPaddedLayout(), newGameButton),
		container.New(layout.NewPaddedLayout(), exitButton),
	)

	return content, scoreTable, foodCountLabel
}

// updateInfoPanel обновление инф панели
func updateInfoPanel(scoreTable *widget.Table, foodCountLabel *widget.Label, state *pb.GameState) {
	// Обновляем данные таблицы
	tableData = [][]string{
		{"Name", "Score"},
	}
	for _, gamePlayer := range state.GetPlayers().GetPlayers() {
		playerName := gamePlayer.GetName()
		if gamePlayer.GetRole() == pb.NodeRole_MASTER {
			playerName += " 👑"
		}
		if gamePlayer.GetRole() == pb.NodeRole_DEPUTY {
			playerName += " 🤡"
		}
		tableData = append(tableData, []string{playerName, fmt.Sprintf("%d", gamePlayer.GetScore())})
	}

	// Просто обновляем данные, Refresh вызовется автоматически при следующем рендере
	// SetText безопасен для вызова из горутины в Fyne v2+
	foodCountLabel.SetText(fmt.Sprintf("Еда: %d", len(state.Foods)))
}

// RunApp запуск (в main)
func RunApp() {
	myApp := app.New()
	myWindow := myApp.NewWindow("SnakeGame")
	myWindow.Resize(fyne.NewSize(800, 600))
	myWindow.CenterOnScreen()

	multConn := connection.Connection()

	ShowMainMenu(myWindow, multConn)

	myWindow.ShowAndRun()
}
