package view

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"image/color"
	pb "snake-net-game/pkg/proto"
)

type InfoPanel struct {
	container          *fyne.Container
	scoreTable         *widget.Table
	foodCountLabel     *widget.Label
	tableData          [][]string
	becomeViewerButton *widget.Button
	isViewer           bool // Флаг, если пользователь присоединился как VIEWER
}

func NewInfoPanel(config *pb.GameConfig, onMainMenu func(), onExit func(), onBecomeViewer func(), scoreLabel *widget.Label, nameLabel *widget.Label, roleLabel *widget.Label, isViewer bool) *InfoPanel {
	panel := &InfoPanel{
		tableData: [][]string{
			{"Имя", "Счёт"},
		},
		isViewer: isViewer,
	}

	panel.scoreTable = widget.NewTable(
		func() (int, int) {
			if len(panel.tableData) == 0 {
				return 0, 0
			}
			return len(panel.tableData), len(panel.tableData[0])
		},
		func() fyne.CanvasObject {
			label := widget.NewLabel("")
			label.TextStyle = fyne.TextStyle{Bold: false}
			return label
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			if id.Row < len(panel.tableData) && id.Col < len(panel.tableData[0]) {
				label := cell.(*widget.Label)
				label.SetText(panel.tableData[id.Row][id.Col])
				// Заголовок таблицы жирным
				if id.Row == 0 {
					label.TextStyle = fyne.TextStyle{Bold: true}
				}
			}
		},
	)

	panel.scoreTable.SetColumnWidth(0, 130)
	panel.scoreTable.SetColumnWidth(1, 60)

	scrollableTable := container.NewScroll(panel.scoreTable)
	scrollableTable.SetMinSize(fyne.NewSize(200, 250))

	scoreLabel.TextStyle = fyne.TextStyle{Bold: true}
	nameLabel.TextStyle = fyne.TextStyle{Bold: true}
	roleLabel.TextStyle = fyne.TextStyle{Bold: true}

	gameInfoTitle := canvas.NewText("Информация об игре", color.White)
	gameInfoTitle.TextSize = 16
	gameInfoTitle.TextStyle = fyne.TextStyle{Bold: true}

	gameInfo := widget.NewLabel(fmt.Sprintf("Размер поля: %dx%d", config.GetWidth(), config.GetHeight()))
	panel.foodCountLabel = widget.NewLabel("Еда: 0")
	panel.foodCountLabel.TextStyle = fyne.TextStyle{Bold: true}

	playerCard := panel.createStatsCard("Игрок", container.NewVBox(scoreLabel, nameLabel, roleLabel))
	leaderboardCard := panel.createStatsCard("Таблица лидеров", scrollableTable)
	gameInfoCard := panel.createStatsCard("", container.NewVBox(gameInfoTitle, gameInfo, panel.foodCountLabel))

	mainMenuButton := widget.NewButton("Главное меню", onMainMenu)
	mainMenuButton.Importance = widget.HighImportance

	becomeViewerButton := widget.NewButton("Стать наблюдателем", onBecomeViewer)
	becomeViewerButton.Importance = widget.WarningImportance
	if isViewer {
		becomeViewerButton.Hide()
	} else {
		becomeViewerButton.Hide()
	}
	panel.becomeViewerButton = becomeViewerButton

	exitButton := widget.NewButton("Выйти", onExit)
	exitButton.Importance = widget.DangerImportance

	panel.container = container.NewVBox(
		playerCard,
		layout.NewSpacer(),
		leaderboardCard,
		layout.NewSpacer(),
		gameInfoCard,
		layout.NewSpacer(),
		mainMenuButton,
		becomeViewerButton,
		exitButton,
	)

	return panel
}

func (ip *InfoPanel) createStatsCard(title string, content fyne.CanvasObject) *fyne.Container {
	cardBg := canvas.NewRectangle(CardBackground)
	cardBg.CornerRadius = 10

	var cardContent fyne.CanvasObject
	if title != "" {
		titleText := canvas.NewText(title, color.White)
		titleText.TextSize = 16
		titleText.TextStyle = fyne.TextStyle{Bold: true}
		titleText.Alignment = fyne.TextAlignCenter

		cardContent = container.NewBorder(
			container.NewPadded(titleText),
			nil, nil, nil,
			container.NewPadded(content),
		)
	} else {
		cardContent = container.NewPadded(content)
	}

	return container.NewStack(cardBg, cardContent)
}

func (ip *InfoPanel) GetContainer() *fyne.Container {
	return ip.container
}

func (ip *InfoPanel) GetScoreTable() *widget.Table {
	return ip.scoreTable
}

func (ip *InfoPanel) GetFoodCountLabel() *widget.Label {
	return ip.foodCountLabel
}

func FormatRole(role pb.NodeRole) string {
	switch role {
	case pb.NodeRole_MASTER:
		return "Мастер"
	case pb.NodeRole_DEPUTY:
		return "Заместитель"
	case pb.NodeRole_VIEWER:
		return "Наблюдатель"
	case pb.NodeRole_NORMAL:
		return "Игрок"
	default:
		return fmt.Sprintf("%v", role)
	}
}

func (ip *InfoPanel) UpdateInfoPanel(state *pb.GameState, playerRole pb.NodeRole) {
	ip.tableData = [][]string{
		{"Имя", "Счёт"},
	}
	for _, gamePlayer := range state.GetPlayers().GetPlayers() {
		playerName := gamePlayer.GetName()
		ip.tableData = append(ip.tableData, []string{playerName, fmt.Sprintf("%d", gamePlayer.GetScore())})
	}

	ip.foodCountLabel.SetText(fmt.Sprintf("Еда: %d", len(state.Foods)))

	if playerRole == pb.NodeRole_NORMAL && !ip.isViewer {
		ip.becomeViewerButton.Show()
	} else {
		ip.becomeViewerButton.Hide()
	}

	ip.scoreTable.Refresh()
}
