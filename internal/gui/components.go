package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ---------------------------------------------------------------- card

// card is a rounded, bordered panel with a title, an optional caption and
// content. Colours come from the palette on every refresh.
type card struct {
	widget.BaseWidget
	content fyne.CanvasObject
}

func newCard(title, caption string, content fyne.CanvasObject) *card {
	t := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	t.SizeName = theme.SizeNameSubHeadingText
	head := []fyne.CanvasObject{t}
	if caption != "" {
		c := widget.NewLabel(caption)
		c.Importance = widget.LowImportance
		c.SizeName = theme.SizeNameCaptionText
		c.Wrapping = fyne.TextWrapWord
		head = append(head, c)
	}
	body := container.NewBorder(container.New(&tightVBox{gap: -8}, head...), nil, nil, nil, content)
	c := &card{content: body}
	c.ExtendBaseWidget(c)
	return c
}

func (c *card) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = 12
	bg.StrokeWidth = 1
	r := &cardRenderer{bg: bg, pad: container.New(&inset{18, 14}, c.content)}
	r.Refresh()
	return r
}

type cardRenderer struct {
	bg  *canvas.Rectangle
	pad *fyne.Container
}

func (r *cardRenderer) Layout(s fyne.Size) { r.bg.Resize(s); r.pad.Resize(s) }
func (r *cardRenderer) MinSize() fyne.Size { return r.pad.MinSize() }
func (r *cardRenderer) Refresh() {
	p := pal()
	r.bg.FillColor, r.bg.StrokeColor = p.card, p.border
	r.bg.Refresh()
	r.pad.Refresh()
}
func (r *cardRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.pad} }
func (r *cardRenderer) Destroy()                     {}

// ---------------------------------------------------------------- pill

// pill is a rounded status badge: a coloured dot and a short text.
type pill struct {
	widget.BaseWidget
	text string
	col  color.NRGBA
}

func newPill() *pill {
	p := &pill{}
	p.ExtendBaseWidget(p)
	return p
}

func (p *pill) set(text string, col color.NRGBA) {
	if p.text == text && p.col == col {
		return
	}
	p.text, p.col = text, col
	p.Refresh()
}

func (p *pill) CreateRenderer() fyne.WidgetRenderer {
	r := &pillRenderer{p: p, bg: canvas.NewRectangle(color.Transparent), dot: canvas.NewCircle(color.Transparent), txt: canvas.NewText("", color.Black)}
	r.txt.TextSize = 13
	r.Refresh()
	return r
}

type pillRenderer struct {
	p   *pill
	bg  *canvas.Rectangle
	dot *canvas.Circle
	txt *canvas.Text
}

func (r *pillRenderer) MinSize() fyne.Size {
	t := r.txt.MinSize()
	return fyne.NewSize(t.Width+38, 30)
}

func (r *pillRenderer) Layout(s fyne.Size) {
	r.bg.Resize(s)
	r.bg.CornerRadius = s.Height / 2
	r.dot.Move(fyne.NewPos(13, s.Height/2-4))
	r.dot.Resize(fyne.NewSize(8, 8))
	t := r.txt.MinSize()
	r.txt.Move(fyne.NewPos(28, (s.Height-t.Height)/2))
	r.txt.Resize(t)
}

func (r *pillRenderer) Refresh() {
	r.bg.FillColor = alpha(r.p.col, 0x22)
	r.bg.StrokeColor = alpha(r.p.col, 0x55)
	r.bg.StrokeWidth = 1
	r.dot.FillColor = r.p.col
	r.txt.Text = r.p.text
	r.txt.Color = pal().text
	r.bg.Refresh()
	r.dot.Refresh()
	r.txt.Refresh()
	r.Layout(r.p.Size())
}

func (r *pillRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.dot, r.txt}
}
func (r *pillRenderer) Destroy() {}

// ---------------------------------------------------------------- layouts

// inset pads its children by fixed horizontal and vertical amounts.
type inset struct{ h, v float32 }

func (l *inset) Layout(objs []fyne.CanvasObject, s fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(l.h, l.v))
		o.Resize(fyne.NewSize(s.Width-2*l.h, s.Height-2*l.v))
	}
}

func (l *inset) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := fyne.NewSize(0, 0)
	for _, o := range objs {
		m = m.Max(o.MinSize())
	}
	return m.Add(fyne.NewSize(2*l.h, 2*l.v))
}

// tightVBox stacks children with a custom gap (negative pulls labels
// together, since Fyne labels carry their own padding).
type tightVBox struct{ gap float32 }

func (l *tightVBox) Layout(objs []fyne.CanvasObject, s fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		h := o.MinSize().Height
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(s.Width, h))
		y += h + l.gap
	}
}

func (l *tightVBox) MinSize(objs []fyne.CanvasObject) fyne.Size {
	w, h := float32(0), float32(0)
	for i, o := range objs {
		m := o.MinSize()
		w = max(w, m.Width)
		h += m.Height
		if i > 0 {
			h += l.gap
		}
	}
	return fyne.NewSize(w, h)
}

// field is a label above a control, with an optional hint below.
func field(label string, control fyne.CanvasObject, hint string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	objs := []fyne.CanvasObject{l, control}
	if hint != "" {
		h := widget.NewLabel(hint)
		h.Importance = widget.LowImportance
		h.SizeName = theme.SizeNameCaptionText
		h.Wrapping = fyne.TextWrapWord
		objs = append(objs, h)
	}
	return container.New(&tightVBox{gap: 0}, objs...)
}

// fixedWidth gives o a fixed width inside an HBox.
func fixedWidth(o fyne.CanvasObject, w float32) fyne.CanvasObject {
	return container.New(&fixedW{w}, o)
}

type fixedW struct{ w float32 }

func (l *fixedW) Layout(objs []fyne.CanvasObject, s fyne.Size) {
	for _, o := range objs {
		o.Resize(fyne.NewSize(l.w, s.Height))
	}
}

func (l *fixedW) MinSize(objs []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objs {
		h = max(h, o.MinSize().Height)
	}
	return fyne.NewSize(l.w, h)
}
