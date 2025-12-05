package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"image/color"
	pb "snake-net-game/pkg/proto"
)

const CellSize = 20

// renderGameState выводит игру на экран
func renderGameState(content *fyne.Container, state *pb.GameState, config *pb.GameConfig) {
	content.Objects = nil

	// игровое поле
	for i := int32(0); i < config.GetWidth(); i++ {
		for j := int32(0); j < config.GetHeight(); j++ {
			cell := canvas.NewRectangle(color.RGBA{R: 50, G: 50, B: 50, A: 255})
			cell.StrokeColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
			cell.StrokeWidth = 1
			cell.Resize(fyne.NewSize(CellSize, CellSize))
			cell.Move(fyne.NewPos(float32(i)*CellSize, float32(j)*CellSize))
			content.Add(cell)
		}
	}

	// еда
	for _, food := range state.Foods {
		apple := canvas.NewCircle(color.RGBA{255, 128, 0, 255})
		apple.Resize(fyne.NewSize(CellSize, CellSize))
		x := float32(food.GetX()) * CellSize
		y := float32(food.GetY()) * CellSize
		apple.Move(fyne.NewPos(x, y))
		content.Add(apple)
	}

	// змеи
	for _, snake := range state.Snakes {
		for i, point := range snake.Points {
			var rect *canvas.Rectangle
			role := getUserById(snake.GetPlayerId(), state)
			if i == 0 {
				// голова
				switch role {
				case pb.NodeRole_MASTER:
					rect = canvas.NewRectangle(color.RGBA{255, 0, 0, 255})
				case pb.NodeRole_NORMAL:
					rect = canvas.NewRectangle(color.RGBA{0, 255, 0, 255})
				case pb.NodeRole_DEPUTY:
					rect = canvas.NewRectangle(color.RGBA{150, 90, 255, 255})
				}
			} else {
				// тело
				switch role {
				case pb.NodeRole_MASTER:
					rect = canvas.NewRectangle(color.RGBA{128, 0, 0, 255})
				case pb.NodeRole_NORMAL:
					rect = canvas.NewRectangle(color.RGBA{0, 128, 0, 255})
				case pb.NodeRole_DEPUTY:
					rect = canvas.NewRectangle(color.RGBA{120, 60, 200, 255})
				}
			}
			rect.Resize(fyne.NewSize(CellSize, CellSize))
			x := float32(point.GetX()) * CellSize
			y := float32(point.GetY()) * CellSize
			rect.Move(fyne.NewPos(x, y))
			content.Add(rect)
		}
	}

	// Не вызываем Refresh напрямую - это должно происходить в главном потоке
	// Canvas обновится автоматически при следующем цикле отрисовки
}

func getUserById(id int32, state *pb.GameState) pb.NodeRole {
	for _, player := range state.Players.Players {
		if player.GetId() == id {
			return player.GetRole()
		}
	}
	return 0
}
