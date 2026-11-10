package gui

import (
	"image/color"
	"math"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/highercomve/GhostKVM/internal/hypr"
)

// layoutEditor draws the desktop monitors to scale with the peer's screen
// next to one of them. Drag the peer box and drop it near any side of any
// monitor; it snaps there and onChange is called.
type layoutEditor struct {
	widget.BaseWidget

	monitors  []hypr.Monitor
	side      string
	monitor   string
	peerW     float64
	peerH     float64
	peerName  string
	onChange  func(side, monitor string)
	peerFront bool // peer currently has keyboard+mouse

	// view transform world -> widget, frozen while dragging
	scale      float64
	offX, offY float64

	dragging bool
	dragRect fyne.Position // top-left of the peer box while dragging (widget coords)
}

func newLayoutEditor(onChange func(side, monitor string)) *layoutEditor {
	e := &layoutEditor{onChange: onChange, side: "left", peerW: 1920, peerH: 1080, peerName: "peer"}
	e.ExtendBaseWidget(e)
	return e
}

func (e *layoutEditor) set(monitors []hypr.Monitor, side, monitor string, w, h float64, name string, peerFront bool) {
	if e.dragging {
		return
	}
	same := e.side == side && e.monitor == monitor && e.peerW == w && e.peerH == h &&
		e.peerName == name && e.peerFront == peerFront && len(e.monitors) == len(monitors)
	if same {
		for i := range monitors {
			if monitors[i] != e.monitors[i] {
				same = false
			}
		}
	}
	if same {
		return
	}
	e.monitors, e.side, e.monitor, e.peerW, e.peerH, e.peerName, e.peerFront = monitors, side, monitor, w, h, name, peerFront
	e.Refresh()
}

type rect struct{ x, y, w, h float64 }

func (r rect) center() (float64, float64) { return r.x + r.w/2, r.y + r.h/2 }

func (e *layoutEditor) targetMonitor() (hypr.Monitor, bool) {
	for _, m := range e.monitors {
		if m.Name == e.monitor {
			return m, true
		}
	}
	if len(e.monitors) > 0 {
		return e.monitors[0], true
	}
	return hypr.Monitor{}, false
}

// peerRect places the peer screen (world coords) on one side of m, centred.
func (e *layoutEditor) peerRect(m hypr.Monitor, side string) rect {
	w, h := e.peerW, e.peerH
	switch side {
	case "left":
		return rect{m.X - w, m.Y + (m.Height-h)/2, w, h}
	case "right":
		return rect{m.X + m.Width, m.Y + (m.Height-h)/2, w, h}
	case "top":
		return rect{m.X + (m.Width-w)/2, m.Y - h, w, h}
	default:
		return rect{m.X + (m.Width-w)/2, m.Y + m.Height, w, h}
	}
}

// fit computes a transform showing every monitor plus room for the peer on
// any side, so the view does not jump when the peer is moved.
func (e *layoutEditor) fit(size fyne.Size) {
	if len(e.monitors) == 0 || size.Width < 10 || size.Height < 10 {
		e.scale = 0
		return
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	grow := func(r rect) {
		minX, minY = math.Min(minX, r.x), math.Min(minY, r.y)
		maxX, maxY = math.Max(maxX, r.x+r.w), math.Max(maxY, r.y+r.h)
	}
	for _, m := range e.monitors {
		grow(rect{m.X, m.Y, m.Width, m.Height})
	}
	if m, ok := e.targetMonitor(); ok {
		grow(e.peerRect(m, e.side))
	}
	// leave room around the arrangement so the peer can be dragged to
	// another side without leaving the view
	padW, padH := (maxX-minX)*0.18, (maxY-minY)*0.18
	minX, maxX, minY, maxY = minX-padW, maxX+padW, minY-padH, maxY+padH
	pad := float64(theme.Padding() * 2)
	sw := (float64(size.Width) - 2*pad) / (maxX - minX)
	sh := (float64(size.Height) - 2*pad) / (maxY - minY)
	e.scale = math.Min(sw, sh)
	e.offX = (float64(size.Width)-(maxX-minX)*e.scale)/2 - minX*e.scale
	e.offY = (float64(size.Height)-(maxY-minY)*e.scale)/2 - minY*e.scale
}

func (e *layoutEditor) toWidget(r rect) (fyne.Position, fyne.Size) {
	return fyne.NewPos(float32(r.x*e.scale+e.offX), float32(r.y*e.scale+e.offY)),
		fyne.NewSize(float32(r.w*e.scale), float32(r.h*e.scale))
}

func (e *layoutEditor) currentPeerWidget() (fyne.Position, fyne.Size, bool) {
	m, ok := e.targetMonitor()
	if !ok || e.scale == 0 {
		return fyne.Position{}, fyne.Size{}, false
	}
	p, s := e.toWidget(e.peerRect(m, e.side))
	if e.dragging {
		p = e.dragRect
	}
	return p, s, true
}

// Dragged implements fyne.Draggable.
func (e *layoutEditor) Dragged(ev *fyne.DragEvent) {
	if !e.dragging {
		p, s, ok := e.currentPeerWidget()
		start := ev.Position.Subtract(ev.Dragged)
		if !ok || start.X < p.X || start.Y < p.Y || start.X > p.X+s.Width || start.Y > p.Y+s.Height {
			return // drag did not start on the peer box
		}
		e.dragging = true
		e.dragRect = p
	}
	e.dragRect = e.dragRect.Add(ev.Dragged)
	e.Refresh()
}

// DragEnd snaps the peer to the nearest side of the nearest monitor.
func (e *layoutEditor) DragEnd() {
	if !e.dragging {
		return
	}
	_, s, _ := e.currentPeerWidget()
	cx := (float64(e.dragRect.X+s.Width/2) - e.offX) / e.scale
	cy := (float64(e.dragRect.Y+s.Height/2) - e.offY) / e.scale
	e.dragging = false

	best, bestSide, bestMon := math.Inf(1), e.side, e.monitor
	for _, m := range e.monitors {
		for _, side := range []string{"left", "right", "top", "bottom"} {
			px, py := e.peerRect(m, side).center()
			if d := math.Hypot(px-cx, py-cy); d < best {
				best, bestSide, bestMon = d, side, m.Name
			}
		}
	}
	changed := bestSide != e.side || bestMon != e.monitor
	e.side, e.monitor = bestSide, bestMon
	e.Refresh()
	if changed && e.onChange != nil {
		e.onChange(bestSide, bestMon)
	}
}

func (e *layoutEditor) CreateRenderer() fyne.WidgetRenderer {
	return &layoutRenderer{e: e, bg: canvas.NewRectangle(color.Transparent)}
}

type layoutRenderer struct {
	e       *layoutEditor
	bg      *canvas.Rectangle
	objects []fyne.CanvasObject
	size    fyne.Size
}

func (r *layoutRenderer) Layout(size fyne.Size) {
	r.size = size
	r.rebuild()
}

func (r *layoutRenderer) MinSize() fyne.Size { return fyne.NewSize(420, 250) }

func (r *layoutRenderer) Refresh() { r.rebuild() }

func (r *layoutRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *layoutRenderer) Destroy() {}

func (r *layoutRenderer) rebuild() {
	e := r.e
	if !e.dragging {
		e.fit(r.size)
	}
	r.bg.Resize(r.size)
	r.objects = []fyne.CanvasObject{r.bg}
	if e.scale == 0 {
		return
	}

	pl := pal()
	label := func(text string, pos fyne.Position, size fyne.Size, col color.Color, bold bool, ts float32) {
		t := canvas.NewText(text, col)
		t.TextSize = ts
		t.TextStyle.Bold = bold
		ms := t.MinSize()
		if ms.Width > size.Width-12 || ms.Height > size.Height/2 {
			return // does not fit: leave it out rather than spill over
		}
		t.Move(fyne.NewPos(pos.X+(size.Width-ms.Width)/2, pos.Y+(size.Height-ms.Height)/2))
		t.Resize(ms)
		r.objects = append(r.objects, t)
	}
	box := func(pos fyne.Position, size fyne.Size, fill, stroke color.Color, width, radius float32) {
		b := canvas.NewRectangle(fill)
		b.StrokeColor = stroke
		b.StrokeWidth = width
		b.CornerRadius = radius
		b.Move(pos)
		b.Resize(size)
		r.objects = append(r.objects, b)
	}

	for _, m := range e.monitors {
		p, s := e.toWidget(rect{m.X, m.Y, m.Width, m.Height})
		stroke := pl.border
		if m.Name == e.monitor {
			stroke = alpha(pl.muted, 0xC0)
		}
		box(p, s, pl.elevated, stroke, 1.5, 10)
		label(m.Name, p.Add(fyne.NewPos(0, -9)), s, pl.text, true, 14)
		label(fmtRes(m.Width, m.Height)+"  ·  this computer", p.Add(fyne.NewPos(0, 11)), s, pl.muted, false, 12)
	}

	accent := pl.accent
	if e.peerFront {
		accent = pl.peer
	}
	// highlight the edge the pointer crosses
	if m, ok := e.targetMonitor(); ok && !e.dragging {
		mp, ms := e.toWidget(rect{m.X, m.Y, m.Width, m.Height})
		line := canvas.NewRectangle(accent)
		line.CornerRadius = 2
		const t = 4
		switch e.side {
		case "left":
			line.Move(fyne.NewPos(mp.X-t/2, mp.Y+8))
			line.Resize(fyne.NewSize(t, ms.Height-16))
		case "right":
			line.Move(fyne.NewPos(mp.X+ms.Width-t/2, mp.Y+8))
			line.Resize(fyne.NewSize(t, ms.Height-16))
		case "top":
			line.Move(fyne.NewPos(mp.X+8, mp.Y-t/2))
			line.Resize(fyne.NewSize(ms.Width-16, t))
		default:
			line.Move(fyne.NewPos(mp.X+8, mp.Y+ms.Height-t/2))
			line.Resize(fyne.NewSize(ms.Width-16, t))
		}
		r.objects = append(r.objects, line)
	}

	p, s, _ := e.currentPeerWidget()
	gap := float32(6) // keep the peer visually separate from the monitor
	p, s = p.Add(fyne.NewPos(gap, gap)), fyne.NewSize(s.Width-2*gap, s.Height-2*gap)
	fill := alpha(accent, 0x26)
	if e.peerFront {
		fill = alpha(accent, 0x40)
	}
	if e.dragging {
		fill = alpha(accent, 0x55)
	}
	box(p, s, fill, accent, 2, 10)
	label(e.peerName, p.Add(fyne.NewPos(0, -9)), s, pl.text, true, 14)
	sub := fmtRes(e.peerW, e.peerH)
	if e.peerFront {
		sub += "  ·  in control"
	} else {
		sub += "  ·  drag to move"
	}
	label(sub, p.Add(fyne.NewPos(0, 11)), s, pl.muted, false, 12)
	canvas.Refresh(e)
}

func fmtRes(w, h float64) string {
	return strconv.Itoa(int(w)) + "×" + strconv.Itoa(int(h))
}
