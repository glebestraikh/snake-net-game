package view

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"image/color"
	"log"
	"snake-net-game/internal/controller"
	"snake-net-game/internal/model/player"
	"time"
)

// JoinGameView представляет экран присоединения к игре
type JoinGameView struct {
	window     fyne.Window
	controller *controller.GameController
	playerNode *player.Player
}

// NewJoinGameView создает новое представление присоединения
func NewJoinGameView(window fyne.Window, controller *controller.GameController) *JoinGameView {
	return &JoinGameView{
		window:     window,
		controller: controller,
	}
}

// Show отображает экран присоединения к игре
func (jgv *JoinGameView) Show() {
	log.Printf("присоединение...")
	jgv.playerNode = jgv.controller.CreatePlayer()

	// Заголовок
	title := canvas.NewText("🌐 Присоединиться к игре", color.White)
	title.TextSize = 28
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignCenter

	discoveryLabel := widget.NewLabel("🔍 Поиск доступных игр...")
	discoveryLabel.Alignment = fyne.TextAlignCenter
	discoveryLabel.TextStyle = fyne.TextStyle{Bold: true}

	// Список игр с улучшенным дизайном
	gameList := widget.NewSelect([]string{}, func(value string) {
		log.Printf("Selected game: %s", value)
	})
	gameList.PlaceHolder = "Выберите игру из списка"

	// Поле ввода имени игрока
	playerNameEntry := widget.NewEntry()
	playerNameEntry.SetPlaceHolder("Введите ваше имя")

	// Выбор роли (Игрок или Наблюдатель)
	roleSelect := widget.NewSelect([]string{"🎮 Игрок", "👁 Наблюдатель"}, func(value string) {
		log.Printf("Selected role: %s", value)
	})
	roleSelect.SetSelected("🎮 Игрок")

	// Карточка с формой
	formCard := jgv.createJoinFormCard(gameList, playerNameEntry, roleSelect, discoveryLabel)

	joinButton := widget.NewButton("🎮 Присоединиться", func() {
		playerName := playerNameEntry.Text
		if playerName == "" {
			errorLabel := widget.NewLabel("⚠️ Имя игрока не может быть пустым!")
			errorLabel.Alignment = fyne.TextAlignCenter
			errorLabel.TextStyle = fyne.TextStyle{Bold: true}

			errorCard := jgv.createErrorCard(errorLabel)

			content := container.NewVBox(
				layout.NewSpacer(),
				container.NewCenter(title),
				errorCard,
				formCard,
				layout.NewSpacer(),
			)
			jgv.window.SetContent(container.NewPadded(content))
			return
		}
		selectedGame := jgv.getSelectedGame(gameList)
		if selectedGame != nil {
			// Определяем роль на основе выбора
			isViewer := roleSelect.Selected == "👁 Наблюдатель"
			playerView := NewPlayerGameView(jgv.window, jgv.controller, jgv.playerNode, playerName, selectedGame, isViewer)
			playerView.Show()
		}
	})
	joinButton.Importance = widget.HighImportance

	backButton := widget.NewButton("⬅️ Назад", func() {
		mainView := NewMainView(jgv.window, jgv.controller)
		mainView.ShowMainMenu()
	})

	content := container.NewVBox(
		layout.NewSpacer(),
		container.NewCenter(title),
		formCard,
		layout.NewSpacer(),
		container.NewCenter(
			container.NewHBox(
				backButton,
				joinButton,
			),
		),
		layout.NewSpacer(),
	)

	jgv.window.SetContent(container.NewPadded(content))

	// Реализуем обнаружение игр и обновление списка
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			games := jgv.playerNode.DiscoveredGames
			if len(games) > 0 {
				gameNames := jgv.getGameNames(games)
				fyne.Do(func() {
					gameList.Options = gameNames
					gameList.Refresh()
					discoveryLabel.SetText("✅ Игры найдены! Выберите из списка:")
				})
				return
			}
		}
	}()
}

// createJoinFormCard создает карточку с формой присоединения
func (jgv *JoinGameView) createJoinFormCard(gameList *widget.Select, nameEntry *widget.Entry, roleSelect *widget.Select, statusLabel *widget.Label) *fyne.Container {
	cardBg := canvas.NewRectangle(CardBackground)
	cardBg.CornerRadius = 10

	statusLabel.Alignment = fyne.TextAlignCenter

	gameListLabel := canvas.NewText("Доступные игры", color.White)
	gameListLabel.TextStyle = fyne.TextStyle{Bold: true}

	nameLabel := canvas.NewText("Имя игрока", color.White)
	nameLabel.TextStyle = fyne.TextStyle{Bold: true}

	roleLabel := canvas.NewText("Роль", color.White)
	roleLabel.TextStyle = fyne.TextStyle{Bold: true}

	formContent := container.NewVBox(
		statusLabel,
		widget.NewSeparator(),
		gameListLabel,
		gameList,
		widget.NewSeparator(),
		nameLabel,
		nameEntry,
		widget.NewSeparator(),
		roleLabel,
		roleSelect,
	)

	cardContent := container.NewPadded(formContent)
	card := container.NewStack(cardBg, cardContent)

	return container.NewCenter(card)
}

// createErrorCard создает карточку с сообщением об ошибке
func (jgv *JoinGameView) createErrorCard(errorLabel *widget.Label) *fyne.Container {
	cardBg := canvas.NewRectangle(color.RGBA{R: 239, G: 68, B: 68, A: 100})
	cardBg.CornerRadius = 10

	cardContent := container.NewPadded(errorLabel)
	card := container.NewStack(cardBg, cardContent)

	return container.NewCenter(card)
}

func (jgv *JoinGameView) getGameNames(games []player.DiscoveredGame) []string {
	names := make([]string, len(games))
	for i, game := range games {
		names[i] = game.GameName
	}
	return names
}

func (jgv *JoinGameView) getSelectedGame(gameList *widget.Select) *player.DiscoveredGame {
	for _, game := range jgv.playerNode.DiscoveredGames {
		if gameList.Selected == game.GameName {
			return &game
		}
	}
	log.Printf("Could't find selected game")
	return nil
}
