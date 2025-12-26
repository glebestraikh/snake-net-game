package app

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"log"
	"net"
	"snake-net-game/internal/controller"
	"snake-net-game/internal/view"
)

// App представляет MVC приложение
type App struct {
	window     fyne.Window
	controller *controller.GameController
	view       *view.MainView
	multConn   *net.UDPConn
}

// NewApp создает новое MVC приложение
func NewApp() *App {
	myApp := app.New()

	// Применяем кастомную тему
	myApp.Settings().SetTheme(&view.CustomTheme{})

	myWindow := myApp.NewWindow("🐍 Snake Game - Multiplayer")
	myWindow.Resize(fyne.NewSize(900, 650))
	myWindow.CenterOnScreen()

	multicastUDPAddr, err := net.ResolveUDPAddr("udp4", "239.192.0.4:9192")
	if err != nil {
		log.Fatalf("Error resolving multicast address: %v", err)
	}

	// создаем сокет для multicast
	multConn, err := net.ListenMulticastUDP("udp4", nil, multicastUDPAddr)
	if err != nil {
		log.Fatalf("Error creating multicast socket: %v", err)
	}

	gameController := controller.NewGameController(myWindow, multConn)
	mainView := view.NewMainView(myWindow, gameController)

	return &App{
		window:     myWindow,
		controller: gameController,
		view:       mainView,
		multConn:   multConn,
	}
}

// Run запускает приложение
func (a *App) Run() {
	a.view.ShowMainMenu()
	a.window.ShowAndRun()
}
