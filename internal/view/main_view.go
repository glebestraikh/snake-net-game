package view

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"image/color"
	"snake-net-game/internal/controller"
)

type MainView struct {
	window     fyne.Window
	controller *controller.GameController
}

func NewMainView(window fyne.Window, controller *controller.GameController) *MainView {
	return &MainView{
		window:     window,
		controller: controller,
	}
}

func (mv *MainView) ShowMainMenu() {
	title := canvas.NewText("SNAKE GAME", color.White)
	title.TextSize = 42
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter

	subtitle := canvas.NewText("Сетевая многопользовательская игра", color.RGBA{R: 203, G: 213, B: 225, A: 255})
	subtitle.TextSize = 16
	subtitle.Alignment = fyne.TextAlignCenter

	newGameButton := mv.createStyledButton("Новая игра", AccentGradient1, func() {
		configView := NewGameConfigView(mv.window, mv.controller)
		configView.Show()
	})

	joinGameButton := mv.createStyledButton("Присоединиться к игре", AccentGradient2, func() {
		joinView := NewJoinGameView(mv.window, mv.controller)
		joinView.Show()
	})

	exitButton := mv.createStyledButton("Выход", color.RGBA{R: 239, G: 68, B: 68, A: 255}, func() {
		mv.window.Close()
	})

	content := container.NewVBox(
		container.NewCenter(title),
		container.NewCenter(subtitle),
		widget.NewLabel(""),
		newGameButton,
		widget.NewLabel(""),
		joinGameButton,
		widget.NewLabel(""),
		exitButton,
	)

	centeredContent := container.NewCenter(content)

	mv.window.SetContent(centeredContent)
}

func (mv *MainView) createStyledButton(text string, bgColor color.Color, onTapped func()) *fyne.Container {
	btn := widget.NewButton(text, onTapped)
	btn.Importance = widget.HighImportance

	bg := canvas.NewRectangle(bgColor)
	bg.CornerRadius = 10

	btnContainer := container.NewStack(bg, btn)
	btnContainer.Resize(fyne.NewSize(300, 50))

	return container.NewCenter(btnContainer)
}
