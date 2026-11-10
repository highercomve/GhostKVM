// Package portal is the screen backend for Wayland desktops that implement
// the XDG InputCapture portal (GNOME 45+, KDE Plasma 6.1+, Hyprland through
// xdg-desktop-portal-hyprland). The desktop tells GhostKVM when the pointer
// crosses a barrier on the peer's edge (Activated), and GhostKVM hands the
// pointer back with Release, placing the cursor where it returns.
package portal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/highercomve/GhostKVM/internal/screen"
)

const (
	busName   = "org.freedesktop.portal.Desktop"
	objPath   = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	ifaceIC   = "org.freedesktop.portal.InputCapture"
	ifaceReq  = "org.freedesktop.portal.Request"
	ifaceSess = "org.freedesktop.portal.Session"

	capKeyboard = 1
	capPointer  = 2

	barrierID = 1
)

// Logf is where the backend reports events; replace to redirect.
var Logf = func(string, ...any) {}

// Backend is an InputCapture session.
type Backend struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	version uint32
	desktop string
	session dbus.ObjectPath
	ei      *eiClient

	mu         sync.Mutex
	zones      []screen.Monitor
	zoneSet    uint32
	edge       *edgeSpec // barrier GhostKVM wants, re-applied when zones change
	active     bool
	activation uint32
	lastEnable time.Time

	acts   chan screen.Activation
	deacts chan struct{}
}

type edgeSpec struct {
	mon  screen.Monitor
	side string
}

var (
	_ screen.Backend     = (*Backend)(nil)
	_ screen.EdgeWatcher = (*Backend)(nil)
)

// Available reports whether the desktop offers the InputCapture portal.
func Available() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	v, err := conn.Object(busName, objPath).GetProperty(ifaceIC + ".version")
	return err == nil && v.Value() != nil
}

// New starts an InputCapture session. The desktop may ask the user for
// permission the first time; where the portal supports it, the permission
// is remembered with a restore token.
func New(desktop string) (*Backend, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	b := &Backend{
		conn: conn, obj: conn.Object(busName, objPath), desktop: desktop,
		acts: make(chan screen.Activation, 4), deacts: make(chan struct{}, 4),
	}
	v, err := b.obj.GetProperty(ifaceIC + ".version")
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("this desktop has no InputCapture portal: %w", err)
	}
	b.version, _ = v.Value().(uint32)

	if err := b.watchSignals(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := b.createSession(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := b.refreshZones(); err != nil {
		b.Close()
		return nil, err
	}
	fd, err := b.connectEIS()
	if err != nil {
		b.Close()
		return nil, err
	}
	if b.ei, err = newEIClient(fd, Logf); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// Close ends the session; the desktop drops all barriers and captures.
func (b *Backend) Close() {
	if b.session != "" {
		b.conn.Object(busName, b.session).Call(ifaceSess+".Close", 0)
	}
	if b.ei != nil {
		b.ei.Close()
	}
	b.conn.Close()
}

func (b *Backend) Name() string        { return b.desktop }
func (b *Backend) EdgeSwitching() bool { return true }

func (b *Backend) Monitors() ([]screen.Monitor, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.zones) == 0 {
		return nil, errors.New("the desktop reported no screens")
	}
	return append([]screen.Monitor(nil), b.zones...), nil
}

// CursorPos is not available on Wayland; edges are reported by Activations.
func (b *Backend) CursorPos() (float64, float64, error) { return 0, 0, screen.ErrUnsupported }

// MoveCursor ends the current capture and asks the desktop to put the cursor
// at (x, y). Without an active capture there is nothing to hand back.
func (b *Backend) MoveCursor(x, y float64) error {
	b.mu.Lock()
	active, id := b.active, b.activation
	b.active = false
	b.mu.Unlock()
	if !active {
		return screen.ErrUnsupported
	}
	return b.obj.Call(ifaceIC+".Release", 0, b.session, map[string]dbus.Variant{
		"activation_id":   dbus.MakeVariant(id),
		"cursor_position": dbus.MakeVariant(struct{ X, Y float64 }{x, y}),
	}).Err
}

// The desktop hides the cursor itself while input is captured.
func (b *Backend) HideCursor() error { return nil }
func (b *Backend) ShowCursor() error { return nil }

func (b *Backend) Activations() <-chan screen.Activation { return b.acts }
func (b *Backend) Deactivations() <-chan struct{}        { return b.deacts }

// WatchEdge puts a pointer barrier on the given side of mon and enables
// capture. The barrier must be on the outer edge of the desktop.
func (b *Backend) WatchEdge(mon screen.Monitor, side string) error {
	b.mu.Lock()
	b.edge = &edgeSpec{mon: mon, side: side}
	b.mu.Unlock()
	if err := b.applyBarrier(); err != nil {
		return err
	}
	return b.obj.Call(ifaceIC+".Enable", 0, b.session, map[string]dbus.Variant{}).Err
}

// ---------------------------------------------------------------- session

func token() string {
	var b [8]byte
	rand.Read(b[:])
	return "ghostkvm_" + hex.EncodeToString(b[:])
}

// requestPath is where the portal will create the Request object for a call
// made with handle_token tok, so we can subscribe before calling.
func (b *Backend) requestPath(tok string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(b.conn.Names()[0], ":"), ".", "_")
	return dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/portal/desktop/request/%s/%s", sender, tok))
}

// request makes a portal call that answers through a Request object and
// waits for its Response.
func (b *Backend) request(timeout time.Duration, method string, call func(tok string) []any) (map[string]dbus.Variant, error) {
	tok := token()
	path := b.requestPath(tok)
	ch := make(chan *dbus.Signal, 1)
	b.conn.Signal(ch)
	defer b.conn.RemoveSignal(ch)
	if err := b.conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ifaceReq), dbus.WithMatchMember("Response")); err != nil {
		return nil, err
	}
	defer b.conn.RemoveMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ifaceReq), dbus.WithMatchMember("Response"))

	if err := b.obj.Call(ifaceIC+"."+method, 0, call(tok)...).Err; err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	deadline := time.After(timeout)
	for {
		select {
		case s := <-ch:
			if s.Path != path || s.Name != ifaceReq+".Response" || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			results, _ := s.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, fmt.Errorf("%s: the request was declined", method)
			default:
				return nil, fmt.Errorf("%s: the desktop could not complete it", method)
			}
		case <-deadline:
			return nil, fmt.Errorf("%s: no answer from the desktop", method)
		}
	}
}

func tokenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ghostkvm", "portal-restore-token")
}

func (b *Backend) createSession() error {
	caps := uint32(capKeyboard | capPointer)
	if b.version >= 2 {
		// CreateSession2 + Start: the permission can be remembered.
		var res map[string]dbus.Variant
		err := b.obj.Call(ifaceIC+".CreateSession2", 0, map[string]dbus.Variant{
			"session_handle_token": dbus.MakeVariant(token()),
		}).Store(&res)
		if err != nil {
			return fmt.Errorf("CreateSession2: %w", err)
		}
		b.session, _ = res["session_handle"].Value().(dbus.ObjectPath)
		if b.session == "" {
			return errors.New("CreateSession2 returned no session")
		}
		opts := func(tok string) map[string]dbus.Variant {
			o := map[string]dbus.Variant{
				"handle_token": dbus.MakeVariant(tok),
				"capabilities": dbus.MakeVariant(caps),
				"persist_mode": dbus.MakeVariant(uint32(2)), // until revoked
			}
			if t, err := os.ReadFile(tokenPath()); err == nil && len(t) > 0 {
				o["restore_token"] = dbus.MakeVariant(strings.TrimSpace(string(t)))
			}
			return o
		}
		// The user may take a while to answer the permission dialog.
		res, err = b.request(5*time.Minute, "Start", func(tok string) []any {
			return []any{b.session, "", opts(tok)}
		})
		if err != nil {
			return err
		}
		if t, ok := res["restore_token"].Value().(string); ok && t != "" {
			os.MkdirAll(filepath.Dir(tokenPath()), 0o700)
			os.WriteFile(tokenPath(), []byte(t), 0o600)
		}
		return nil
	}

	// Version 1: CreateSession starts the session immediately.
	res, err := b.request(5*time.Minute, "CreateSession", func(tok string) []any {
		return []any{"", map[string]dbus.Variant{
			"handle_token":         dbus.MakeVariant(tok),
			"session_handle_token": dbus.MakeVariant(token()),
			"capabilities":         dbus.MakeVariant(caps),
		}}
	})
	if err != nil {
		return err
	}
	switch h := res["session_handle"].Value().(type) {
	case dbus.ObjectPath:
		b.session = h
	case string:
		b.session = dbus.ObjectPath(h)
	}
	if b.session == "" {
		return errors.New("CreateSession returned no session")
	}
	return nil
}

func (b *Backend) refreshZones() error {
	res, err := b.request(30*time.Second, "GetZones", func(tok string) []any {
		return []any{b.session, map[string]dbus.Variant{"handle_token": dbus.MakeVariant(tok)}}
	})
	if err != nil {
		return err
	}
	var raw []struct {
		W, H uint32
		X, Y int32
	}
	if err := dbus.Store([]any{res["zones"].Value()}, &raw); err != nil {
		return fmt.Errorf("reading zones: %w", err)
	}
	zones := make([]screen.Monitor, 0, len(raw))
	for _, z := range raw {
		zones = append(zones, screen.Monitor{X: float64(z.X), Y: float64(z.Y), Width: float64(z.W), Height: float64(z.H)})
	}
	sort.Slice(zones, func(i, j int) bool {
		if zones[i].Y != zones[j].Y {
			return zones[i].Y < zones[j].Y
		}
		return zones[i].X < zones[j].X
	})
	for i := range zones {
		zones[i].Name = fmt.Sprintf("Screen %d", i+1)
	}
	set, _ := res["zone_set"].Value().(uint32)
	b.mu.Lock()
	b.zones, b.zoneSet = zones, set
	b.mu.Unlock()
	return nil
}

func (b *Backend) connectEIS() (int, error) {
	var fd dbus.UnixFD
	if err := b.obj.Call(ifaceIC+".ConnectToEIS", 0, b.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return -1, fmt.Errorf("ConnectToEIS: %w", err)
	}
	return int(fd), nil
}

// barrier returns the barrier line for one side of a zone, per the portal
// spec: barriers sit on the top/left edge of their pixels, so the right and
// bottom edges are at x+width and y+height.
func barrier(z screen.Monitor, side string) (x1, y1, x2, y2 int32) {
	x, y := int32(z.X), int32(z.Y)
	w, h := int32(z.Width), int32(z.Height)
	switch side {
	case "left":
		return x, y, x, y + h - 1
	case "right":
		return x + w, y, x + w, y + h - 1
	case "top":
		return x, y, x + w - 1, y
	default:
		return x, y + h, x + w - 1, y + h
	}
}

func (b *Backend) applyBarrier() error {
	b.mu.Lock()
	edge, zoneSet, zones := b.edge, b.zoneSet, b.zones
	b.mu.Unlock()
	if edge == nil {
		return nil
	}
	// Match the monitor GhostKVM picked to a portal zone by position.
	zone := edge.mon
	for _, z := range zones {
		if z.X == edge.mon.X && z.Y == edge.mon.Y {
			zone = z
		}
	}
	x1, y1, x2, y2 := barrier(zone, edge.side)
	res, err := b.request(30*time.Second, "SetPointerBarriers", func(tok string) []any {
		return []any{b.session, map[string]dbus.Variant{"handle_token": dbus.MakeVariant(tok)},
			[]map[string]dbus.Variant{{
				"barrier_id": dbus.MakeVariant(uint32(barrierID)),
				"position":   dbus.MakeVariant(struct{ X1, Y1, X2, Y2 int32 }{x1, y1, x2, y2}),
			}}, zoneSet}
	})
	if err != nil {
		return err
	}
	if failed, _ := res["failed_barriers"].Value().([]uint32); len(failed) > 0 {
		return fmt.Errorf("the desktop refused a barrier on the %s edge of %s: it must be an outer edge of your screens", edge.side, zone.Name)
	}
	Logf("portal: barrier on the %s edge of %s (%d,%d)-(%d,%d)", edge.side, zone.Name, x1, y1, x2, y2)
	return nil
}

// ---------------------------------------------------------------- signals

func (b *Backend) watchSignals() error {
	if err := b.conn.AddMatchSignal(dbus.WithMatchInterface(ifaceIC)); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 16)
	b.conn.Signal(ch)
	go func() {
		for s := range ch {
			if !strings.HasPrefix(s.Name, ifaceIC+".") || len(s.Body) < 2 {
				continue
			}
			if p, _ := s.Body[0].(dbus.ObjectPath); p != b.session {
				continue
			}
			opts, _ := s.Body[1].(map[string]dbus.Variant)
			b.onSignal(strings.TrimPrefix(s.Name, ifaceIC+"."), opts)
		}
	}()
	return nil
}

func (b *Backend) onSignal(name string, opts map[string]dbus.Variant) {
	switch name {
	case "Activated":
		id, _ := opts["activation_id"].Value().(uint32)
		var pos struct{ X, Y float64 }
		if v, ok := opts["cursor_position"]; ok {
			dbus.Store([]any{v.Value()}, &pos)
		}
		b.mu.Lock()
		b.active, b.activation = true, id
		b.mu.Unlock()
		select {
		case b.acts <- screen.Activation{X: pos.X, Y: pos.Y}:
		default:
		}
	case "Deactivated":
		id, _ := opts["activation_id"].Value().(uint32)
		b.mu.Lock()
		current := b.active && b.activation == id
		if current {
			b.active = false
		}
		b.mu.Unlock()
		if current {
			select {
			case b.deacts <- struct{}{}:
			default:
			}
		}
	case "ZonesChanged":
		go func() {
			if err := b.refreshZones(); err != nil {
				Logf("portal: zones: %v", err)
				return
			}
			if err := b.applyBarrier(); err != nil {
				Logf("portal: %v", err)
			}
		}()
	case "Disabled":
		// Re-enable, but never in a tight loop if the desktop keeps refusing.
		b.mu.Lock()
		retry := b.edge != nil && time.Since(b.lastEnable) > 5*time.Second
		if retry {
			b.lastEnable = time.Now()
		}
		b.mu.Unlock()
		if retry {
			go b.obj.Call(ifaceIC+".Enable", 0, b.session, map[string]dbus.Variant{})
		}
	}
}
