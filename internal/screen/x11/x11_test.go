package x11

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestXvfb runs the backend against a throwaway Xvfb server. Skipped when
// Xvfb isn't installed.
func TestXvfb(t *testing.T) {
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed")
	}
	display := fmt.Sprintf(":%d", 90+os.Getpid()%9)
	x := exec.Command("Xvfb", display, "-screen", "0", "1600x900x24", "-nolisten", "tcp")
	if err := x.Start(); err != nil {
		t.Fatal(err)
	}
	defer x.Process.Kill()
	t.Setenv("DISPLAY", display)

	var b *Backend
	var err error
	for i := 0; i < 50; i++ { // wait for the server to come up
		if b, err = New(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}

	mons, err := b.Monitors()
	if err != nil || len(mons) == 0 {
		t.Fatalf("monitors: %v %v", mons, err)
	}
	if mons[0].Width != 1600 || mons[0].Height != 900 {
		t.Errorf("monitor size = %vx%v, want 1600x900", mons[0].Width, mons[0].Height)
	}
	if err := b.MoveCursor(321, 123); err != nil {
		t.Fatal(err)
	}
	px, py, err := b.CursorPos()
	if err != nil || px != 321 || py != 123 {
		t.Errorf("cursor = %v,%v (%v), want 321,123", px, py, err)
	}
	if err := b.HideCursor(); err != nil {
		t.Errorf("hide: %v", err)
	}
	if err := b.ShowCursor(); err != nil {
		t.Errorf("show: %v", err)
	}
}
