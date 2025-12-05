package view

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"image/color"
)

// CustomTheme представляет кастомную тему приложения
type CustomTheme struct{}

var _ fyne.Theme = (*CustomTheme)(nil)

// Color возвращает цвет для заданного имени
func (ct *CustomTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.RGBA{R: 26, G: 32, B: 44, A: 255} // Темный синий фон
	case theme.ColorNameButton:
		return color.RGBA{R: 88, G: 101, B: 242, A: 255} // Яркий синий для кнопок
	case theme.ColorNamePrimary:
		return color.RGBA{R: 139, G: 92, B: 246, A: 255} // Фиолетовый акцент
	case theme.ColorNameForeground:
		return color.RGBA{R: 226, G: 232, B: 240, A: 255} // Светлый текст
	case theme.ColorNameHover:
		return color.RGBA{R: 109, G: 122, B: 255, A: 255} // Светлее при наведении
	case theme.ColorNameFocus:
		return color.RGBA{R: 167, G: 139, B: 250, A: 255} // Светло-фиолетовый фокус
	case theme.ColorNameSuccess:
		return color.RGBA{R: 52, G: 211, B: 153, A: 255} // Зеленый успех
	case theme.ColorNameError:
		return color.RGBA{R: 248, G: 113, B: 113, A: 255} // Красная ошибка
	case theme.ColorNameInputBackground:
		return color.RGBA{R: 45, G: 55, B: 72, A: 255} // Темнее для полей ввода
	default:
		return theme.DefaultTheme().Color(name, variant)
	}
}

// Font возвращает шрифт для заданного стиля
func (ct *CustomTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

// Icon возвращает иконку для заданного имени
func (ct *CustomTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Size возвращает размер для заданного имени
func (ct *CustomTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 14
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNameSubHeadingText:
		return 18
	case theme.SizeNamePadding:
		return 8
	case theme.SizeNameInlineIcon:
		return 20
	default:
		return theme.DefaultTheme().Size(name)
	}
}

// Цветовая палитра для игровых элементов
var (
	// Цвета для змей
	MasterSnakeHead = color.RGBA{R: 239, G: 68, B: 68, A: 255}   // Красный
	MasterSnakeBody = color.RGBA{R: 185, G: 28, B: 28, A: 255}   // Темно-красный
	NormalSnakeHead = color.RGBA{R: 16, G: 185, B: 129, A: 255}  // Зеленый
	NormalSnakeBody = color.RGBA{R: 5, G: 150, B: 105, A: 255}   // Темно-зеленый
	DeputySnakeHead = color.RGBA{R: 167, G: 139, B: 250, A: 255} // Фиолетовый
	DeputySnakeBody = color.RGBA{R: 124, G: 58, B: 237, A: 255}  // Темно-фиолетовый

	// Цвета игрового поля
	GameFieldDark   = color.RGBA{R: 30, G: 41, B: 59, A: 255}  // Темная клетка
	GameFieldLight  = color.RGBA{R: 45, G: 55, B: 72, A: 255}  // Светлая клетка
	GameFieldBorder = color.RGBA{R: 71, G: 85, B: 105, A: 255} // Граница

	// Цвета еды
	FoodColor = color.RGBA{R: 251, G: 191, B: 36, A: 255} // Золотой
	FoodGlow  = color.RGBA{R: 245, G: 158, B: 11, A: 255} // Оранжевое свечение

	// Цвета UI
	CardBackground  = color.RGBA{R: 45, G: 55, B: 72, A: 255}   // Фон карточек
	AccentGradient1 = color.RGBA{R: 88, G: 101, B: 242, A: 255} // Начало градиента
	AccentGradient2 = color.RGBA{R: 139, G: 92, B: 246, A: 255} // Конец градиента
)
