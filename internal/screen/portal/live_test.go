package portal

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestLiveInputCapture runs against the real desktop portal: it sets a
// barrier on the left edge of the first screen, pushes the cursor across it
// with ydotool (Hyprland: hyprctl parks the cursor) and expects an activation,
// then hands input back. Opt in with
// GHOSTKVM_LIVE_PORTAL=1; it briefly captures the real pointer.
func TestLiveInputCapture(t *testing.T) {
	if os.Getenv("GHOSTKVM_LIVE_PORTAL") != "1" {
		t.Skip("set GHOSTKVM_LIVE_PORTAL=1 to run against the real desktop")
	}
	Logf = t.Logf
	b, err := New("live test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	mons, err := b.Monitors()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("zones: %+v (portal version %d)", mons, b.version)
	m := mons[0]
	if err := b.WatchEdge(m, "left"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	// Park the cursor near the left edge, then push left across the barrier.
	y := m.Y + m.Height/2
	exec.Command("hyprctl", "eval", "hl.dispatch(hl.dsp.cursor.move({x="+itoa(int(m.X)+20)+", y="+itoa(int(y))+"}))").Run()
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 6; i++ {
		exec.Command("ydotool", "mousemove", "--", "-15", "0").Run()
		time.Sleep(40 * time.Millisecond)
	}

	select {
	case a := <-b.Activations():
		t.Logf("activated at %.0f,%.0f", a.X, a.Y)
		if err := b.MoveCursor(m.X+200, y); err != nil {
			t.Fatalf("release: %v", err)
		}
		t.Log("released; cursor asked to return 200px inside the screen")
	case <-time.After(6 * time.Second):
		t.Fatal("no activation after pushing the cursor across the barrier")
	}
}

func itoa(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
