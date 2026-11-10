package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Palette, taken from the app icon: slate surfaces, one teal accent, blue
// for "the other computer has control".
type palette struct {
	bg, card, elevated, border, text, muted, accent, onAccent, peer, hover, input color.NRGBA
}

var (
	darkPalette = palette{
		bg:       rgb(0x13, 0x16, 0x1D),
		card:     rgb(0x1C, 0x20, 0x29),
		elevated: rgb(0x25, 0x2A, 0x35),
		border:   rgb(0x2D, 0x33, 0x40),
		text:     rgb(0xE8, 0xEB, 0xF1),
		muted:    rgb(0x8E, 0x96, 0xA6),
		accent:   rgb(0x2E, 0xC4, 0x95),
		onAccent: rgb(0x07, 0x24, 0x1A),
		peer:     rgb(0x5B, 0x8C, 0xFF),
		hover:    rgb(0x2C, 0x32, 0x3E),
		input:    rgb(0x16, 0x19, 0x21),
	}
	lightPalette = palette{
		bg:       rgb(0xF3, 0xF4, 0xF7),
		card:     rgb(0xFF, 0xFF, 0xFF),
		elevated: rgb(0xEE, 0xF0, 0xF4),
		border:   rgb(0xE1, 0xE4, 0xEA),
		text:     rgb(0x1A, 0x1D, 0x24),
		muted:    rgb(0x67, 0x6F, 0x7E),
		accent:   rgb(0x13, 0x9A, 0x70), // darker teal: readable on white
		onAccent: rgb(0xFF, 0xFF, 0xFF),
		peer:     rgb(0x2F, 0x66, 0xE8),
		hover:    rgb(0xE8, 0xEB, 0xF0),
		input:    rgb(0xF7, 0xF8, 0xFA),
	}
)

func rgb(r, g, b uint8) color.NRGBA { return color.NRGBA{r, g, b, 0xFF} }

func alpha(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }

// pal returns the palette for the variant Fyne is currently using.
func pal() palette {
	if fyne.CurrentApp() != nil && fyne.CurrentApp().Settings().ThemeVariant() == theme.VariantLight {
		return lightPalette
	}
	return darkPalette
}

type ghostTheme struct{}

func (ghostTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	p := darkPalette
	if v == theme.VariantLight {
		p = lightPalette
	}
	switch n {
	case theme.ColorNameBackground:
		return p.bg
	case theme.ColorNameButton:
		return p.elevated
	case theme.ColorNameDisabledButton:
		return alpha(p.elevated, 0x90)
	case theme.ColorNameDisabled:
		return alpha(p.muted, 0xA0)
	case theme.ColorNameForeground:
		return p.text
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnSuccess:
		return p.onAccent
	case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameSuccess:
		return p.accent
	case theme.ColorNameHover:
		return p.hover
	case theme.ColorNamePressed:
		return p.border
	case theme.ColorNameInputBackground:
		return p.input
	case theme.ColorNameInputBorder:
		return p.border
	case theme.ColorNamePlaceHolder:
		return p.muted
	case theme.ColorNameSeparator:
		return p.border
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground:
		return p.card
	case theme.ColorNameHeaderBackground:
		return p.card
	case theme.ColorNameSelection:
		return alpha(p.accent, 0x40)
	case theme.ColorNameScrollBar:
		return alpha(p.muted, 0x80)
	}
	return theme.DefaultTheme().Color(n, v)
}

func (ghostTheme) Font(s fyne.TextStyle) fyne.Resource     { return theme.DefaultTheme().Font(s) }
func (ghostTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

func (ghostTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameText:
		return 14
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameInputBorder:
		return 1
	}
	return theme.DefaultTheme().Size(n)
}
