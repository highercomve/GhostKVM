// Package evdev reads Linux input devices (/dev/input/event*) and can grab
// them exclusively, so the compositor stops seeing their events. Pure Go,
// no cgo. Needs read access to the device nodes (the "input" group).
package evdev

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Event types and codes used by GhostKVM (see linux/input-event-codes.h).
const (
	EvSyn = 0x00
	EvKey = 0x01
	EvRel = 0x02

	SynReport = 0

	RelX      = 0x00
	RelY      = 0x01
	RelHWheel = 0x06
	RelWheel  = 0x08

	BtnLeft   = 0x110
	BtnRight  = 0x111
	BtnMiddle = 0x112
	BtnSide   = 0x113
	BtnExtra  = 0x114

	KeyA = 30
	KeyZ = 44
)

// Event is one input_event.
type Event struct {
	Type  uint16
	Code  uint16
	Value int32
}

// Device is an open input device.
type Device struct {
	Path    string
	Name    string
	IsMouse bool // has relative X/Y and a left button
	IsKbd   bool // has letter keys
	f       *os.File
	grabbed bool
}

func ioc(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | uintptr('E')<<8 | nr
}

const (
	iocRead  = 2
	iocWrite = 1
)

func ioctlBuf(fd uintptr, req uintptr, buf []byte) (int, error) {
	n, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func testBit(bits []byte, n int) bool {
	return n/8 < len(bits) && bits[n/8]&(1<<(n%8)) != 0
}

// Open opens a device and reads its name and capabilities.
func Open(path string) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{Path: path, f: f}
	fd := f.Fd()

	name := make([]byte, 256)
	if n, err := ioctlBuf(fd, ioc(iocRead, 0x06, 256), name); err == nil {
		d.Name = strings.TrimRight(string(name[:n]), "\x00")
	}

	evBits := make([]byte, 4)
	keyBits := make([]byte, 96) // KEY_MAX 0x2ff
	relBits := make([]byte, 2)
	ioctlBuf(fd, ioc(iocRead, 0x20+0, uintptr(len(evBits))), evBits)
	if testBit(evBits, EvKey) {
		ioctlBuf(fd, ioc(iocRead, 0x20+EvKey, uintptr(len(keyBits))), keyBits)
	}
	if testBit(evBits, EvRel) {
		ioctlBuf(fd, ioc(iocRead, 0x20+EvRel, uintptr(len(relBits))), relBits)
	}
	d.IsMouse = testBit(relBits, RelX) && testBit(relBits, RelY) && testBit(keyBits, BtnLeft)
	letters := 0
	for k := KeyA; k <= KeyZ; k++ {
		if testBit(keyBits, k) {
			letters++
		}
	}
	d.IsKbd = letters >= 10
	return d, nil
}

// Grab takes (true) or releases (false) exclusive access to the device.
func (d *Device) Grab(on bool) error {
	if d.grabbed == on {
		return nil
	}
	var v uintptr
	if on {
		v = 1
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.f.Fd(), ioc(iocWrite, 0x90, 4), v)
	if errno != 0 {
		return fmt.Errorf("grab %s: %w", d.Path, errno)
	}
	d.grabbed = on
	return nil
}

// Read blocks for the next batch of events.
func (d *Device) Read(buf []Event) (int, error) {
	const size = 24 // struct input_event on 64-bit: timeval(16) + type + code + value
	raw := make([]byte, size*len(buf))
	n, err := d.f.Read(raw)
	if err != nil {
		return 0, err
	}
	count := n / size
	for i := 0; i < count; i++ {
		b := raw[i*size:]
		buf[i] = Event{
			Type:  binary.LittleEndian.Uint16(b[16:]),
			Code:  binary.LittleEndian.Uint16(b[18:]),
			Value: int32(binary.LittleEndian.Uint32(b[20:])),
		}
	}
	return count, nil
}

// Close releases any grab and closes the device.
func (d *Device) Close() error {
	d.Grab(false)
	return d.f.Close()
}

// List returns the paths of all event devices, in numeric order.
func List() []string {
	paths, _ := filepath.Glob("/dev/input/event*")
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		return paths[i] < paths[j]
	})
	return paths
}
