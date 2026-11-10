// Package auto picks the screen backend for the current session.
package auto

import (
	"fmt"
	"os"
	"strings"

	"github.com/highercomve/GhostKVM/internal/screen"
	"github.com/highercomve/GhostKVM/internal/screen/hyprland"
	"github.com/highercomve/GhostKVM/internal/screen/portal"
	"github.com/highercomve/GhostKVM/internal/screen/x11"
)

// Detect returns the backend named by choice ("auto", "hyprland", "x11",
// "portal" or "none"). With "auto" it uses Hyprland when running under it,
// X11 in an X session, the InputCapture portal on Wayland desktops that
// offer it (GNOME 45+, KDE Plasma 6.1+), and otherwise the hotkey-only None
// backend.
func Detect(choice string) (screen.Backend, error) {
	switch strings.ToLower(choice) {
	case "hyprland":
		return hyprland.New()
	case "x11":
		return x11.New()
	case "portal":
		return portal.New(desktopName())
	case "none":
		return screen.None{Desktop: desktopName()}, nil
	case "", "auto":
	default:
		return nil, fmt.Errorf("screen_backend must be auto, hyprland, x11, portal or none, got %q", choice)
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
	if os.Getenv("WAYLAND_DISPLAY") != "" && portalAvailable() {
		b, err := portal.New(desktopName())
		if err == nil {
			return b, nil
		}
		fallbackReason = err.Error()
	}
	return screen.None{Desktop: desktopName()}, nil
}

// portalAvailable is swapped out by tests, which must not open a real session.
var portalAvailable = portal.Available

// fallbackReason says why the portal could not be used, when it couldn't.
var fallbackReason string

// FallbackReason explains a hotkey-only fallback, or is empty.
func FallbackReason() string { return fallbackReason }

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
