// Package screen is GhostKVM's view of the local desktop: where the monitors
// are, where the cursor is, and moving and hiding the cursor. Each desktop
// needs its own backend (Hyprland, X11, ...); desktops that don't let apps
// read the cursor get the None backend and switch with the hotkey.
package screen

import "errors"

// ErrUnsupported means the backend can't do this on the current desktop.
var ErrUnsupported = errors.New("not supported on this desktop")

// Monitor is one output in global layout coordinates (logical pixels).
type Monitor struct {
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Backend talks to one kind of desktop.
type Backend interface {
	// Name is shown in logs and the window, e.g. "Hyprland" or "X11".
	Name() string
	// EdgeSwitching reports whether CursorPos works, so pushing the pointer
	// off a screen edge can switch to the other computer.
	EdgeSwitching() bool
	Monitors() ([]Monitor, error)
	CursorPos() (x, y float64, err error)
	MoveCursor(x, y float64) error
	// HideCursor and ShowCursor bracket the time the other computer has
	// control. Backends that can't hide it return nil and do nothing.
	HideCursor() error
	ShowCursor() error
}

// None is the fallback for desktops without a cursor API (GNOME, KDE and
// Sway on Wayland): no edge switching from the desktop, the hotkey and tray
// still switch, and pushing past the peer's edge still comes back.
type None struct{ Desktop string }

func (n None) Name() string {
	if n.Desktop != "" {
		return n.Desktop
	}
	return "this desktop"
}
func (None) EdgeSwitching() bool { return false }

// Monitors returns one placeholder screen so the layout can still be shown.
func (None) Monitors() ([]Monitor, error) {
	return []Monitor{{Name: "This computer", Width: 1920, Height: 1080}}, nil
}
func (None) CursorPos() (float64, float64, error) { return 0, 0, ErrUnsupported }
func (None) MoveCursor(float64, float64) error    { return ErrUnsupported }
func (None) HideCursor() error                    { return nil }
func (None) ShowCursor() error                    { return nil }
