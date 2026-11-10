// Package auto picks the screen backend for the current session.
package auto

import (
	"fmt"
	"os"
	"strings"

	"github.com/highercomve/GhostKVM/internal/screen"
	"github.com/highercomve/GhostKVM/internal/screen/hyprland"
	"github.com/highercomve/GhostKVM/internal/screen/x11"
)

// Detect returns the backend named by choice ("auto", "hyprland", "x11" or
// "none"). With "auto" it uses Hyprland when running under it, X11 in an X
// session, and otherwise the hotkey-only None backend: Wayland desktops such
// as GNOME, KDE and Sway don't let apps read or move the cursor.
func Detect(choice string) (screen.Backend, error) {
	switch strings.ToLower(choice) {
	case "hyprland":
		return hyprland.New()
	case "x11":
		return x11.New()
	case "none":
		return screen.None{Desktop: desktopName()}, nil
	case "", "auto":
	default:
		return nil, fmt.Errorf("screen_backend must be auto, hyprland, x11 or none, got %q", choice)
	}

	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		if b, err := hyprland.New(); err == nil {
			return b, nil
		}
	}
	// Under Wayland, $DISPLAY points at XWayland, which only sees the pointer
	// over X windows, so use X11 only in a real X session.
	if os.Getenv("DISPLAY") != "" && (os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("XDG_SESSION_TYPE") == "x11") {
		if b, err := x11.New(); err == nil {
			return b, nil
		}
	}
	return screen.None{Desktop: desktopName()}, nil
}

// desktopName is a readable name for the session, e.g. "GNOME (Wayland)".
func desktopName() string {
	d := os.Getenv("XDG_CURRENT_DESKTOP")
	if i := strings.IndexByte(d, ':'); i >= 0 {
		d = d[i+1:] // "ubuntu:GNOME" -> "GNOME"
	}
	if d == "" {
		d = "this desktop"
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		d += " (Wayland)"
	}
	return d
}
