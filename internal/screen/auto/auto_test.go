package auto

import (
	"testing"

	"github.com/highercomve/GhostKVM/internal/screen"
)

func TestWaylandDesktopsFallBackToHotkey(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	t.Setenv("DISPLAY", ":0") // XWayland: must not be mistaken for an X session
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	t.Setenv("XDG_CURRENT_DESKTOP", "ubuntu:GNOME")

	b, err := Detect("auto")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(screen.None); !ok || b.EdgeSwitching() {
		t.Fatalf("got %T (edge %v), want hotkey-only None", b, b.EdgeSwitching())
	}
	if b.Name() != "GNOME (Wayland)" {
		t.Errorf("name = %q", b.Name())
	}
}

func TestUnknownChoice(t *testing.T) {
	if _, err := Detect("mutter"); err == nil {
		t.Fatal("want an error for an unknown backend")
	}
}
