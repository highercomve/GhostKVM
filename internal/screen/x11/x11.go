// Package x11 is the screen backend for X11 sessions: GNOME or KDE on Xorg,
// XFCE, Cinnamon, MATE, i3 and other X window managers. Pure Go (no libX11).
package x11

import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"

	"github.com/highercomve/GhostKVM/internal/screen"
)

// Backend talks to the X server named by $DISPLAY.
type Backend struct {
	conn     *xgb.Conn
	root     xproto.Window
	scr      *xproto.ScreenInfo
	hasRandr bool
	hasFixes bool

	mu     sync.Mutex
	hidden bool
}

var _ screen.Backend = (*Backend)(nil)

// New connects to the X server.
func New() (*Backend, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect to X server: %w", err)
	}
	scr := xproto.Setup(conn).DefaultScreen(conn)
	b := &Backend{conn: conn, root: scr.Root, scr: scr}
	if randr.Init(conn) == nil {
		if _, err := randr.QueryVersion(conn, 1, 5).Reply(); err == nil {
			b.hasRandr = true
		}
	}
	if xfixes.Init(conn) == nil {
		if _, err := xfixes.QueryVersion(conn, 4, 0).Reply(); err == nil {
			b.hasFixes = true
		}
	}
	return b, nil
}

func (b *Backend) Name() string        { return "X11" }
func (b *Backend) EdgeSwitching() bool { return true }

// Monitors lists active monitors through RandR 1.5, or the whole screen
// as one monitor when RandR is missing.
func (b *Backend) Monitors() ([]screen.Monitor, error) {
	if b.hasRandr {
		rep, err := randr.GetMonitors(b.conn, b.root, true).Reply()
		if err == nil && len(rep.Monitors) > 0 {
			mons := make([]screen.Monitor, 0, len(rep.Monitors))
			for i, m := range rep.Monitors {
				name := fmt.Sprintf("Monitor %d", i+1)
				if a, err := xproto.GetAtomName(b.conn, m.Name).Reply(); err == nil && a.Name != "" {
					name = a.Name
				}
				mons = append(mons, screen.Monitor{
					Name: name, X: float64(m.X), Y: float64(m.Y),
					Width: float64(m.Width), Height: float64(m.Height),
				})
			}
			return mons, nil
		}
	}
	return []screen.Monitor{{
		Name: "Screen", Width: float64(b.scr.WidthInPixels), Height: float64(b.scr.HeightInPixels),
	}}, nil
}

func (b *Backend) CursorPos() (float64, float64, error) {
	rep, err := xproto.QueryPointer(b.conn, b.root).Reply()
	if err != nil {
		return 0, 0, err
	}
	return float64(rep.RootX), float64(rep.RootY), nil
}

func (b *Backend) MoveCursor(x, y float64) error {
	return xproto.WarpPointerChecked(b.conn, 0, b.root, 0, 0, 0, 0, int16(x), int16(y)).Check()
}

// HideCursor hides the pointer through XFixes. X hides it only while this
// connection is open, so a crash can never leave it invisible.
func (b *Backend) HideCursor() error {
	if !b.hasFixes {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hidden {
		return nil
	}
	if err := xfixes.HideCursorChecked(b.conn, b.root).Check(); err != nil {
		return err
	}
	b.hidden = true
	return nil
}

func (b *Backend) ShowCursor() error {
	if !b.hasFixes {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.hidden {
		return nil
	}
	b.hidden = false
	return xfixes.ShowCursorChecked(b.conn, b.root).Check()
}
