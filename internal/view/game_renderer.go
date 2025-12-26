package view

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"log"
	pb "snake-net-game/pkg/proto"
)

const CellSize = 24

type GameRenderer struct {
	gameContent *fyne.Container
}

func NewGameRenderer() *GameRenderer {
	return &GameRenderer{}
}

func (gr *GameRenderer) CreateGameContent(config *pb.GameConfig) *fyne.Container {
	gr.gameContent = container.NewWithoutLayout()

	windowWidth := float32(config.GetWidth()) * CellSize
	windowHeight := float32(config.GetHeight()) * CellSize
	gr.gameContent.Resize(fyne.NewSize(windowWidth, windowHeight))

	return gr.gameContent
}

func (gr *GameRenderer) RenderGameState(content *fyne.Container, state *pb.GameState, config *pb.GameConfig) {
	content.Objects = nil

	for i := int32(0); i < config.GetWidth(); i++ {
		for j := int32(0); j < config.GetHeight(); j++ {
			var cellColor = GameFieldDark
			if (i+j)%2 == 0 {
				cellColor = GameFieldLight
			}

			cell := canvas.NewRectangle(cellColor)
			cell.StrokeColor = GameFieldBorder
			cell.StrokeWidth = 0.5
			cell.CornerRadius = 2
			cell.Resize(fyne.NewSize(CellSize, CellSize))
			cell.Move(fyne.NewPos(float32(i)*CellSize, float32(j)*CellSize))
			content.Add(cell)
		}
	}

	for _, food := range state.Foods {
		x := float32(food.GetX()) * CellSize
		y := float32(food.GetY()) * CellSize

		glow := canvas.NewCircle(FoodGlow)
		glow.Resize(fyne.NewSize(CellSize+4, CellSize+4))
		glow.Move(fyne.NewPos(x-2, y-2))
		content.Add(glow)

		apple := canvas.NewCircle(FoodColor)
		apple.Resize(fyne.NewSize(CellSize-4, CellSize-4))
		apple.Move(fyne.NewPos(x+2, y+2))
		content.Add(apple)
	}

	for _, snake := range state.Snakes {
		role := gr.getUserById(snake.GetPlayerId(), state)
		if len(snake.Points) == 0 {
			continue
		}

		currX := snake.Points[0].GetX()
		currY := snake.Points[0].GetY()

		log.Printf("Rendering snake %d: head=(%d,%d), role=%v, points=%d",
			snake.GetPlayerId(), currX, currY, role, len(snake.Points))

		gr.drawSnakePart(content, currX, currY, role, 0, true)

		bodyPartIdx := 1
		for i := 1; i < len(snake.Points); i++ {
			dx := snake.Points[i].GetX()
			dy := snake.Points[i].GetY()

			absDX := dx
			if absDX < 0 {
				absDX = -absDX
			}
			absDY := dy
			if absDY < 0 {
				absDY = -absDY
			}

			steps := absDX
			if absDY > absDX {
				steps = absDY
			}

			for s := int32(0); s < steps; s++ {
				if config.GetWidth() > 0 {
					if dx > 0 {
						currX = (currX + 1) % config.GetWidth()
					} else if dx < 0 {
						currX = (currX - 1 + config.GetWidth()) % config.GetWidth()
					}
				}
				if config.GetHeight() > 0 {
					if dy > 0 {
						currY = (currY + 1) % config.GetHeight()
					} else if dy < 0 {
						currY = (currY - 1 + config.GetHeight()) % config.GetHeight()
					}
				}

				gr.drawSnakePart(content, currX, currY, role, bodyPartIdx, false)
				bodyPartIdx++
			}
		}
	}
}

func (gr *GameRenderer) drawSnakePart(content *fyne.Container, x, y int32, role pb.NodeRole, index int, isHead bool) {
	posX := float32(x) * CellSize
	posY := float32(y) * CellSize

	if isHead {
		var headColor = MasterSnakeHead
		switch role {
		case pb.NodeRole_MASTER:
			headColor = MasterSnakeHead
		case pb.NodeRole_NORMAL:
			headColor = NormalSnakeHead
		case pb.NodeRole_DEPUTY:
			headColor = DeputySnakeHead
		}

		head := canvas.NewRectangle(headColor)
		head.CornerRadius = 6
		head.Resize(fyne.NewSize(CellSize-2, CellSize-2))
		head.Move(fyne.NewPos(posX+1, posY+1))
		content.Add(head)

		eye1 := canvas.NewCircle(GameFieldDark)
		eye1.Resize(fyne.NewSize(4, 4))
		eye1.Move(fyne.NewPos(posX+6, posY+6))
		content.Add(eye1)

		eye2 := canvas.NewCircle(GameFieldDark)
		eye2.Resize(fyne.NewSize(4, 4))
		eye2.Move(fyne.NewPos(posX+14, posY+6))
		content.Add(eye2)
	} else {
		var bodyColor = MasterSnakeBody
		switch role {
		case pb.NodeRole_MASTER:
			bodyColor = MasterSnakeBody
		case pb.NodeRole_NORMAL:
			bodyColor = NormalSnakeBody
		case pb.NodeRole_DEPUTY:
			bodyColor = DeputySnakeBody
		}

		sizeReduction := float32(index) * 0.3
		if sizeReduction > 4 {
			sizeReduction = 4
		}

		body := canvas.NewRectangle(bodyColor)
		body.CornerRadius = 4
		body.Resize(fyne.NewSize(CellSize-sizeReduction, CellSize-sizeReduction))
		body.Move(fyne.NewPos(posX+sizeReduction/2, posY+sizeReduction/2))
		content.Add(body)
	}
}

func (gr *GameRenderer) getUserById(id int32, state *pb.GameState) pb.NodeRole {
	for _, player := range state.Players.Players {
		if player.GetId() == id {
			return player.GetRole()
		}
	}
	return 0
}
