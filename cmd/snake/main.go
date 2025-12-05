package main

import (
	"snake-net-game/internal/mvc"
)

func main() {
	// Используем MVC архитектуру
	app := mvc.NewApp()
	app.Run()
}
