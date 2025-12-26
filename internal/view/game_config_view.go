package view

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/protobuf/proto"
	"image/color"
	"snake-net-game/internal/controller"
	pb "snake-net-game/pkg/proto"
	"strconv"
)

type GameConfigView struct {
	window     fyne.Window
	controller *controller.GameController
}

func NewGameConfigView(window fyne.Window, controller *controller.GameController) *GameConfigView {
	return &GameConfigView{
		window:     window,
		controller: controller,
	}
}

func (gcv *GameConfigView) Show() {
	title := canvas.NewText("Настройки новой игры", color.White)
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter

	subtitle := canvas.NewText("Настройте параметры игрового поля", color.RGBA{R: 203, G: 213, B: 225, A: 255})
	subtitle.TextSize = 14
	subtitle.Alignment = fyne.TextAlignCenter

	gameNameEntry := widget.NewEntry()
	gameNameEntry.SetText("Моя игра")
	gameNameEntry.SetPlaceHolder("Введите название игры")

	widthEntry := widget.NewEntry()
	widthEntry.SetText("25")
	widthEntry.SetPlaceHolder("10-100")

	heightEntry := widget.NewEntry()
	heightEntry.SetText("25")
	heightEntry.SetPlaceHolder("10-100")

	foodEntry := widget.NewEntry()
	foodEntry.SetText("10")
	foodEntry.SetPlaceHolder("0-100")

	delayEntry := widget.NewEntry()
	delayEntry.SetText("180")
	delayEntry.SetPlaceHolder("100-3000")

	formCard := gcv.createFormCard(gameNameEntry, widthEntry, heightEntry, foodEntry, delayEntry)

	startButton := widget.NewButton("Начать игру", func() {
		gameName := gameNameEntry.Text
		if gameName == "" {
			gameName = "Игра без названия"
		}
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

		masterView := NewMasterGameView(gcv.window, gcv.controller, config, gameName)
		masterView.Show()
	})
	startButton.Importance = widget.HighImportance

	backButton := widget.NewButton("Назад", func() {
		mainView := NewMainView(gcv.window, gcv.controller)
		mainView.ShowMainMenu()
	})

	content := container.NewVBox(
		layout.NewSpacer(),
		container.NewCenter(title),
		container.NewCenter(subtitle),
		layout.NewSpacer(),
		formCard,
		layout.NewSpacer(),
		container.NewCenter(
			container.NewHBox(
				backButton,
				startButton,
			),
		),
		layout.NewSpacer(),
	)

	gcv.window.SetContent(container.NewPadded(content))
}

func (gcv *GameConfigView) createFormCard(gameNameEntry, widthEntry, heightEntry, foodEntry, delayEntry *widget.Entry) *fyne.Container {
	cardBg := canvas.NewRectangle(CardBackground)
	cardBg.CornerRadius = 10

	formItems := container.NewVBox()

	gameNameLabel := canvas.NewText("Название игры", color.White)
	gameNameLabel.TextStyle = fyne.TextStyle{Bold: true}
	gameNameDesc := widget.NewLabel("Название вашей игры для отображения в списке")
	gameNameDesc.TextStyle = fyne.TextStyle{Italic: true}
	formItems.Add(gameNameLabel)
	formItems.Add(gameNameEntry)
	formItems.Add(gameNameDesc)
	formItems.Add(widget.NewSeparator())

	widthLabel := canvas.NewText("Ширина поля", color.White)
	widthLabel.TextStyle = fyne.TextStyle{Bold: true}
	widthDesc := widget.NewLabel("Количество клеток по горизонтали")
	widthDesc.TextStyle = fyne.TextStyle{Italic: true}
	formItems.Add(widthLabel)
	formItems.Add(widthEntry)
	formItems.Add(widthDesc)
	formItems.Add(widget.NewSeparator())

	heightLabel := canvas.NewText("Высота поля", color.White)
	heightLabel.TextStyle = fyne.TextStyle{Bold: true}
	heightDesc := widget.NewLabel("Количество клеток по вертикали")
	heightDesc.TextStyle = fyne.TextStyle{Italic: true}
	formItems.Add(heightLabel)
	formItems.Add(heightEntry)
	formItems.Add(heightDesc)
	formItems.Add(widget.NewSeparator())

	foodLabel := canvas.NewText("Количество еды", color.White)
	foodLabel.TextStyle = fyne.TextStyle{Bold: true}
	foodDesc := widget.NewLabel("Статическое количество еды на поле")
	foodDesc.TextStyle = fyne.TextStyle{Italic: true}
	formItems.Add(foodLabel)
	formItems.Add(foodEntry)
	formItems.Add(foodDesc)
	formItems.Add(widget.NewSeparator())

	delayLabel := canvas.NewText("Задержка (мс)", color.White)
	delayLabel.TextStyle = fyne.TextStyle{Bold: true}
	delayDesc := widget.NewLabel("Задержка между ходами в миллисекундах")
	delayDesc.TextStyle = fyne.TextStyle{Italic: true}
	formItems.Add(delayLabel)
	formItems.Add(delayEntry)
	formItems.Add(delayDesc)

	cardContent := container.NewPadded(formItems)
	card := container.NewStack(cardBg, cardContent)

	return container.NewCenter(card)
}
