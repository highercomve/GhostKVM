package serialhid

import (
	"bytes"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The worked example from the CH9329 protocol documentation: key "a".
func TestFrameMatchesSpec(t *testing.T) {
	got := frame(cmdKeyboard, 0, 0, 0x04, 0, 0, 0, 0, 0)
	want := []byte{0x57, 0xAB, 0x00, 0x02, 0x08, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = % X\nwant    % X", got, want)
	}
}

// openPTY returns a fake bridge: the controller end for the Device under
// test and the peer end playing the bridge.
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	// Non-blocking so read deadlines work on the bridge's end.
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Skip("no pty:", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, "/dev/pts/" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func readFrames(t *testing.T, m *os.File, want int) [][]byte {
	t.Helper()
	var frames [][]byte
	var buf []byte
	deadline := time.Now().Add(2 * time.Second)
	chunk := make([]byte, 512)
	for len(frames) < want && time.Now().Before(deadline) {
		m.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _ := m.Read(chunk)
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 6 && len(buf) >= 6+int(buf[4]) {
			l := 6 + int(buf[4])
			frames = append(frames, append([]byte(nil), buf[:l]...))
			buf = buf[l:]
		}
	}
	return frames
}

func TestDeviceTalksCH9329(t *testing.T) {
	m, slave := openPTY(t)
	d, err := Open(slave, 115200)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// The device polls GET_INFO; answer as a bridge whose host is ready.
	first := readFrames(t, m, 1)
	if len(first) == 0 || first[0][3] != cmdGetInfo {
		t.Fatalf("expected a GET_INFO poll, got % X", first)
	}
	m.Write(frame(cmdGetInfo|0x80, 0x30, 0x01, 0x00, 'G', 0, 0, 0, 0))
	for i := 0; i < 50 && !d.HostSubscribed(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.HostSubscribed() {
		t.Fatal("bridge reported ready but HostSubscribed is false")
	}

	// A 300px move is split into frames of at most 127.
	if err := d.MouseMove(1, 300, -5, 0); err != nil {
		t.Fatal(err)
	}
	d.MoveAbs(0.5, 1)
	d.KeyReport(0x02, 0x04)
	frames := readFrames(t, m, 5)
	var rel [][]byte
	var abs, kb []byte
	for _, f := range frames {
		switch f[3] {
		case cmdMouseRel:
			rel = append(rel, f)
		case cmdMouseAbs:
			abs = f
		case cmdKeyboard:
			kb = f
		}
	}
	sumX := 0
	for _, f := range rel {
		sumX += int(int8(f[7]))
		if f[5] != 0x01 || f[6] != 1 {
			t.Errorf("relative frame % X: want marker 01 and left button", f)
		}
	}
	if len(rel) != 3 || sumX != 300 {
		t.Errorf("relative frames %d with dx sum %d, want 3 frames summing to 300", len(rel), sumX)
	}
	if abs == nil || abs[5] != 0x02 || int(abs[7])|int(abs[8])<<8 != 2047 || int(abs[9])|int(abs[10])<<8 != 4095 {
		t.Errorf("absolute frame % X, want x=2047 y=4095", abs)
	}
	if kb == nil || kb[5] != 0x02 || kb[7] != 0x04 {
		t.Errorf("keyboard frame % X, want shift + a", kb)
	}
}
