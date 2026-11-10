package portal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
)

// eiClient is a minimal EI receiver client (libei protocol 1.x), written to
// the spec in libei's proto/protocol.xml. The InputCapture portal requires
// an EI connection, and the compositor sends captured input over it. GhostKVM
// reads input straight from /dev/input instead, so this client only keeps the
// connection healthy: it completes the handshake, answers pings, binds the
// seat and discards device events.
type eiClient struct {
	conn   *net.UnixConn
	wmu    sync.Mutex
	nextID uint64
	objs   map[uint64]string // object id -> interface name
	caps   uint64            // seat capabilities we will bind
	logf   func(string, ...any)
	done   chan struct{}
}

var ne = binary.NativeEndian // EI uses the EIS implementation's native byte order

// Interfaces we announce, all at version 1: we only need their basics.
var eiInterfaces = []string{
	"ei_connection", "ei_callback", "ei_pingpong", "ei_seat", "ei_device",
	"ei_pointer", "ei_pointer_absolute", "ei_scroll", "ei_button", "ei_keyboard",
}

// Capabilities worth binding on the seat (input capture of keyboard and mouse).
var eiWanted = map[string]bool{
	"ei_pointer": true, "ei_pointer_absolute": true, "ei_scroll": true,
	"ei_button": true, "ei_keyboard": true,
}

func newEIClient(fd int, logf func(string, ...any)) (*eiClient, error) {
	f := os.NewFile(uintptr(fd), "eis")
	defer f.Close() // FileConn dups the descriptor
	c, err := net.FileConn(f)
	if err != nil {
		return nil, fmt.Errorf("EIS socket: %w", err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, errors.New("EIS socket is not a unix socket")
	}
	e := &eiClient{conn: uc, nextID: 1, objs: map[uint64]string{0: "ei_handshake"}, logf: logf, done: make(chan struct{})}
	go e.readLoop()
	return e, nil
}

func (e *eiClient) Close() error {
	return e.conn.Close()
}

// ---------------------------------------------------------------- encoding

type msg struct{ b []byte }

func (m *msg) u32(v uint32) { m.b = ne.AppendUint32(m.b, v) }
func (m *msg) u64(v uint64) { m.b = ne.AppendUint64(m.b, v) }
func (m *msg) str(s string) {
	n := len(s) + 1 // including the NUL
	m.u32(uint32(n))
	m.b = append(m.b, s...)
	m.b = append(m.b, 0)
	for len(m.b)%4 != 0 {
		m.b = append(m.b, 0)
	}
}

func (e *eiClient) send(obj uint64, opcode uint32, args func(*msg)) error {
	m := &msg{b: make([]byte, 16, 64)}
	if args != nil {
		args(m)
	}
	ne.PutUint64(m.b[0:], obj)
	ne.PutUint32(m.b[8:], uint32(len(m.b)))
	ne.PutUint32(m.b[12:], opcode)
	e.wmu.Lock()
	defer e.wmu.Unlock()
	_, err := e.conn.Write(m.b)
	return err
}

// ---------------------------------------------------------------- decoding

type reader struct {
	b   []byte
	err bool
}

func (r *reader) u32() uint32 {
	if len(r.b) < 4 {
		r.err = true
		return 0
	}
	v := ne.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *reader) u64() uint64 {
	if len(r.b) < 8 {
		r.err = true
		return 0
	}
	v := ne.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

func (r *reader) str() string {
	n := int(r.u32())
	if n == 0 {
		return ""
	}
	padded := (n + 3) &^ 3
	if len(r.b) < padded {
		r.err = true
		return ""
	}
	s := string(r.b[:n-1])
	r.b = r.b[padded:]
	return s
}

func (e *eiClient) readLoop() {
	defer close(e.done)
	buf := make([]byte, 0, 8192)
	chunk := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(4*16))
	for {
		n, oobn, _, _, err := e.conn.ReadMsgUnix(chunk, oob)
		closeFDs(oob[:oobn]) // keymaps arrive as fds; we don't need them
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				e.logf("EIS connection: %v", err)
			}
			return
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 16 {
			length := int(ne.Uint32(buf[8:]))
			if length < 16 {
				e.logf("EIS: bad message length %d", length)
				return
			}
			if len(buf) < length {
				break
			}
			obj := ne.Uint64(buf[0:])
			op := ne.Uint32(buf[12:])
			e.handle(obj, op, &reader{b: buf[16:length]})
			buf = buf[length:]
		}
	}
}

func closeFDs(oob []byte) {
	if len(oob) == 0 {
		return
	}
	cmsgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, c := range cmsgs {
		if fds, err := syscall.ParseUnixRights(&c); err == nil {
			for _, fd := range fds {
				syscall.Close(fd)
			}
		}
	}
}

// handle reacts to the events that matter for a healthy connection.
func (e *eiClient) handle(obj uint64, op uint32, r *reader) {
	switch e.objs[obj] {
	case "ei_handshake":
		switch op {
		case 0: // handshake_version: introduce ourselves as a receiver
			e.send(0, 0, func(m *msg) { m.u32(1) })          // handshake_version(1)
			e.send(0, 2, func(m *msg) { m.u32(1) })          // context_type(receiver)
			e.send(0, 3, func(m *msg) { m.str("GhostKVM") }) // name
			for _, itf := range eiInterfaces {
				itf := itf
				e.send(0, 4, func(m *msg) { m.str(itf); m.u32(1) }) // interface_version
			}
			e.send(0, 1, nil) // finish
		case 2: // connection(serial, new_id, version)
			r.u32()
			id := r.u64()
			e.objs[id] = "ei_connection"
			delete(e.objs, 0)
		}
	case "ei_connection":
		switch op {
		case 0: // disconnected
			r.u32()
			reason := r.u32()
			e.logf("EIS disconnected us (reason %d): %s", reason, r.str())
		case 1: // seat(new_id, version)
			e.objs[r.u64()] = "ei_seat"
		case 3: // ping(new_id, version): reply on the new pingpong object
			id := r.u64()
			e.send(id, 0, func(m *msg) { m.u64(0) }) // ei_pingpong.done (destructor)
		}
	case "ei_seat":
		switch op {
		case 0: // destroyed
			delete(e.objs, obj)
		case 2: // capability(mask, interface)
			mask := r.u64()
			if eiWanted[r.str()] {
				e.caps |= mask
			}
		case 3: // done: bind what we want
			caps := e.caps
			e.send(obj, 1, func(m *msg) { m.u64(caps) }) // bind
		case 4: // device(new_id, version)
			e.objs[r.u64()] = "ei_device"
		}
	case "ei_device":
		switch op {
		case 0: // destroyed
			delete(e.objs, obj)
		case 5: // interface(new_id, name, version)
			id := r.u64()
			e.objs[id] = r.str()
		}
	default:
		// ei_pointer, ei_keyboard, ...: input events we don't use. Track only
		// their destruction (opcode 0 is "destroyed" on every interface).
		if op == 0 && obj != 0 {
			delete(e.objs, obj)
		}
	}
}
