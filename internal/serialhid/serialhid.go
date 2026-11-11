// Package serialhid is GhostKVM's wired output: a serial-to-USB-HID bridge
// that the other device sees as a USB keyboard and mouse. It speaks the
// CH9329 serial protocol, implemented both by WCH's CH9329 chip (sold as
// "CH9329 cables") and by GhostKVM's ESP32-S3 firmware in firmware/esp32s3.
//
// Frame: 57 AB | addr | cmd | len | data[len] | sum, where sum is all
// preceding bytes added mod 256. The device answers each command with
// cmd|0x80 (cmd|0xC0 on error).
package serialhid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	cmdGetInfo   = 0x01
	cmdKeyboard  = 0x02
	cmdMouseAbs  = 0x04
	cmdMouseRel  = 0x05
	cmdConsumer  = 0x40 // GhostKVM firmware extension: 16-bit consumer usage
	absMax       = 4095
	statusPeriod = time.Second
)

// Logf is where the package reports events; replace to redirect.
var Logf = func(string, ...any) {}

// Device is an open bridge.
type Device struct {
	path string
	f    *os.File

	wmu     sync.Mutex
	buttons byte

	mu        sync.Mutex
	ready     bool // the controlled device enumerated the USB keyboard/mouse
	lastReply time.Time
	ghostFW   bool // GhostKVM firmware (vs a CH9329 chip)

	done chan struct{}
}

// Open opens the bridge on a serial port (e.g. /dev/serial/by-id/...) at the
// given baud rate (115200 for GhostKVM's firmware; a CH9329 ships at 9600).
func Open(path string, baud int) (*Device, error) {
	speed, ok := bauds[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate %d", baud)
	}
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := makeRaw(f, speed); err != nil {
		f.Close()
		return nil, fmt.Errorf("configure %s: %w", path, err)
	}
	// The port stays non-blocking: Go's poller then handles reads, and
	// Close interrupts a pending read instead of hanging.
	d := &Device{path: path, f: f, done: make(chan struct{})}
	go d.readLoop()
	go d.pollStatus()
	return d, nil
}

var bauds = map[int]uint32{
	9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600,
	115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800, 921600: unix.B921600,
}

// makeRaw configures 8N1 raw mode at the given speed, like cfmakeraw.
func makeRaw(f *os.File, speed uint32) error {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	t.Ispeed, t.Ospeed = speed, speed
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

// Close stops the bridge, releasing every key and button first.
func (d *Device) Close() error {
	d.KeyReport(0)
	d.MouseMove(0, 0, 0, 0)
	close(d.done)
	return d.f.Close()
}

// ---------------------------------------------------------------- frames

// frame builds one CH9329 frame.
func frame(cmd byte, data ...byte) []byte {
	b := append([]byte{0x57, 0xAB, 0x00, cmd, byte(len(data))}, data...)
	var sum byte
	for _, x := range b {
		sum += x
	}
	return append(b, sum)
}

func (d *Device) send(cmd byte, data ...byte) error {
	_, err := d.f.Write(frame(cmd, data...))
	return err
}

func (d *Device) readLoop() {
	buf := make([]byte, 256)
	var pending []byte
	for {
		n, err := d.f.Read(buf)
		if err != nil {
			select {
			case <-d.done:
			default:
				Logf("USB bridge %s: %v", d.path, err)
				d.mu.Lock()
				d.ready = false
				d.mu.Unlock()
			}
			return
		}
		pending = append(pending, buf[:n]...)
		pending = d.parse(pending)
	}
}

// parse consumes complete reply frames and returns the unparsed rest.
func (d *Device) parse(b []byte) []byte {
	for {
		i := 0
		for i+1 < len(b) && !(b[i] == 0x57 && b[i+1] == 0xAB) {
			i++
		}
		b = b[i:]
		if len(b) < 6 {
			return b
		}
		total := 5 + int(b[4]) + 1
		if len(b) < total {
			return b
		}
		var sum byte
		for _, x := range b[:total-1] {
			sum += x
		}
		if sum == b[total-1] {
			d.onReply(b[3], b[5:total-1])
		}
		b = b[total:]
	}
}

func (d *Device) onReply(cmd byte, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastReply = time.Now()
	switch {
	case cmd == cmdGetInfo|0x80 && len(data) >= 2:
		ready := data[1] == 1
		if ready != d.ready {
			if ready {
				Logf("USB bridge: the other device is using the keyboard and mouse")
			} else {
				Logf("USB bridge: the other device is not connected")
			}
		}
		d.ready = ready
		d.ghostFW = len(data) >= 4 && data[3] == 'G'
	case cmd&0xC0 == 0xC0 && len(data) >= 1:
		Logf("USB bridge: command 0x%02X failed with 0x%02X", cmd&0x3F, data[0])
	}
}

func (d *Device) pollStatus() {
	t := time.NewTicker(statusPeriod)
	defer t.Stop()
	for {
		d.wmu.Lock()
		d.send(cmdGetInfo)
		d.wmu.Unlock()
		select {
		case <-d.done:
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- kvm.HID

// HostSubscribed reports whether the controlled device has the USB keyboard
// and mouse up, according to the bridge's latest answer.
func (d *Device) HostSubscribed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready && time.Since(d.lastReply) < 3*statusPeriod
}

// HostName names the output for the window and logs.
func (d *Device) HostName() string { return "USB device" }

// MouseMove sends relative motion; CH9329 frames carry -127..127, so large
// moves are split.
func (d *Device) MouseMove(buttons byte, dx, dy int16, wheel int8) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.buttons = buttons
	x, y, w := int(dx), int(dy), int(wheel)
	for {
		sx, sy, sw := clamp(x), clamp(y), clamp(w)
		if err := d.send(cmdMouseRel, 0x01, buttons, byte(int8(sx)), byte(int8(sy)), byte(int8(sw))); err != nil {
			return err
		}
		x, y, w = x-sx, y-sy, w-sw
		if x == 0 && y == 0 && w == 0 {
			return nil
		}
	}
}

// MoveAbs puts the pointer at (fx, fy), fractions 0..1 of the other
// device's screen: exact, with no acceleration or drift.
func (d *Device) MoveAbs(fx, fy float64) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	x := uint16(min(max(fx, 0), 1) * absMax)
	y := uint16(min(max(fy, 0), 1) * absMax)
	return d.send(cmdMouseAbs, 0x02, d.buttons, byte(x), byte(x>>8), byte(y), byte(y>>8), 0)
}

// KeyReport sends the keyboard state: modifiers and up to six usages.
func (d *Device) KeyReport(mods byte, keys ...byte) error {
	r := make([]byte, 8)
	r[0] = mods
	copy(r[2:], keys)
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return d.send(cmdKeyboard, r...)
}

// ConsumerKey sends a media key (consumer usage, 0 releases). Only GhostKVM's
// firmware supports it; a CH9329 has a different, fixed media-key layout.
func (d *Device) ConsumerKey(usage uint16) error {
	d.mu.Lock()
	ghost := d.ghostFW
	d.mu.Unlock()
	if !ghost {
		return nil
	}
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return d.send(cmdConsumer, byte(usage), byte(usage>>8))
}

func clamp(v int) int { return max(-127, min(127, v)) }

// ---------------------------------------------------------------- ports

// Port is a serial port offered for the bridge.
type Port struct {
	Path string // stable path, /dev/serial/by-id/... when available
	Name string // readable name for the window
}

// Ports lists serial ports without opening them: GhostKVM never probes
// ports on its own, since other hardware may be attached.
func Ports() []Port {
	var ports []Port
	seen := map[string]bool{}
	ids, _ := filepath.Glob("/dev/serial/by-id/*")
	sort.Strings(ids)
	for _, p := range ids {
		target, err := filepath.EvalSymlinks(p)
		if err == nil {
			seen[target] = true
		}
		ports = append(ports, Port{Path: p, Name: filepath.Base(p) + " (" + filepath.Base(target) + ")"})
	}
	for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*"} {
		devs, _ := filepath.Glob(pattern)
		sort.Strings(devs)
		for _, p := range devs {
			if !seen[p] {
				ports = append(ports, Port{Path: p, Name: filepath.Base(p)})
			}
		}
	}
	return ports
}

// ErrNoPort is returned when USB output is chosen without a port.
var ErrNoPort = errors.New("choose the serial port of the USB bridge in Settings")
