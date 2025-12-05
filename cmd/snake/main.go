package main

import (
	"snake-net-game/internal/mvc"
)

func main() {
	application := mvc.NewApp()
	application.Run()
}
