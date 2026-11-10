// Package kvm moves keyboard and mouse control between the local Hyprland
// desktop and a peer reached through a Bluetooth HID link, as if the peer
// were an extra display next to one of the desktop's monitors.
package kvm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/highercomve/GhostKVM/internal/blehid"
	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/evdev"
	"github.com/highercomve/GhostKVM/internal/keymap"
	"github.com/highercomve/GhostKVM/internal/screen"
)

// HID is the output side: anything that can send mouse and keyboard reports.
type HID interface {
	MouseMove(buttons byte, dx, dy int16, wheel int8) error
	KeyReport(mods byte, keys ...byte) error
	HostSubscribed() bool
}

var _ HID = (*blehid.Peripheral)(nil)

type devMsg struct {
	dev    *evdev.Device
	events []evdev.Event
	err    error
}

// KVM is the switching state machine. All state is owned by Run's goroutine.
type KVM struct {
	cfg *config.Config
	hid HID
	scr screen.Backend
	ew  screen.EdgeWatcher // non-nil when the desktop reports edge crossings itself

	peerDevice  string // Bluetooth name of the connected device, refreshed every 2s
	watchedEdge string // last edge handed to ew.WatchEdge
	edgeProblem string // why edge switching isn't working, for the UI
	hotkey      keymap.Hotkey

	devices map[string]*evdev.Device // open, captured-capable devices by path
	skipped map[string]skipInfo      // paths we looked at and do not use
	msgs    chan devMsg
	cmds    chan func() // work from other goroutines (the web UI), run in Run's loop
	stopped chan struct{}

	mon      screen.Monitor   // desktop monitor the peer touches
	monitors []screen.Monitor // all desktop monitors, for the UI

	// held state, tracked in both modes
	mods     byte
	keysDown map[uint16]bool // evdev codes of non-modifier keys held
	buttons  byte            // HID button bits

	// per-device partial frame
	frame map[*evdev.Device]*frameAcc

	remote bool

	// local mode
	push        float64
	lastQuery   time.Time
	atEdge      bool
	edgeFrac    float64
	pendingSwap bool // hotkey pressed locally; switch once keys are released

	// remote mode
	vx, vy     float64 // tracked peer pointer position
	overshoot  float64
	remoteKeys []byte // HID usages currently held on the peer
	accX, accY float64
	accWheel   int
}

type frameAcc struct{ dx, dy, wheel int32 }

type skipInfo struct {
	name, kind string // kind is "" for devices that are neither keyboard nor mouse
}

func kindOf(d *evdev.Device) string {
	switch {
	case d.IsMouse:
		return "mouse"
	case d.IsKbd:
		return "keyboard"
	}
	return ""
}

func edgeWatcher(scr screen.Backend) screen.EdgeWatcher {
	ew, _ := scr.(screen.EdgeWatcher)
	return ew
}

// New prepares a KVM. Call Run to start it.
func New(cfg *config.Config, hid HID, scr screen.Backend) (*KVM, error) {
	hk, err := keymap.ParseHotkey(cfg.Hotkey)
	if err != nil {
		return nil, err
	}
	return &KVM{
		cfg: cfg, hid: hid, scr: scr, hotkey: hk, ew: edgeWatcher(scr),
		devices:  map[string]*evdev.Device{},
		skipped:  map[string]skipInfo{},
		msgs:     make(chan devMsg, 256),
		cmds:     make(chan func()),
		stopped:  make(chan struct{}),
		keysDown: map[uint16]bool{},
		frame:    map[*evdev.Device]*frameAcc{},
	}, nil
}

// Run processes input until ctx is cancelled. It always leaves the desktop
// in local mode (devices released, cursor restored) when it returns.
func (k *KVM) Run(ctx context.Context) error {
	defer close(k.stopped)
	if err := k.refreshMonitor(); err != nil {
		return err
	}
	k.syncEdge()
	k.refreshPeerDevice()
	var acts <-chan screen.Activation
	var deacts <-chan struct{}
	if k.ew != nil {
		acts, deacts = k.ew.Activations(), k.ew.Deactivations()
	}
	k.scanDevices()
	if len(k.devices) == 0 {
		return fmt.Errorf("no keyboards or mice readable in /dev/input (is your user in the 'input' group?)")
	}
	defer func() {
		k.leaveRemote("shutting down")
		for _, d := range k.devices {
			d.Close()
		}
	}()

	// One mouse report per tick, carrying all motion since the last one.
	// Sending faster than the BLE link drains only builds a queue in BlueZ,
	// which feels like lag followed by a catch-up jump.
	flush := time.NewTicker(15 * time.Millisecond)
	rescan := time.NewTicker(2 * time.Second)
	defer flush.Stop()
	defer rescan.Stop()

	if k.scr.EdgeSwitching() {
		log.Printf("ready (%s): %s is %s of %s; push the mouse off that edge or press %s",
			k.scr.Name(), k.peerName(), k.cfg.Peer.Side, k.mon.Name, k.cfg.Hotkey)
	} else {
		log.Printf("ready (%s): edge switching isn't available on this desktop; press %s or use the tray to switch",
			k.scr.Name(), k.cfg.Hotkey)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-k.cmds:
			f()
		case m := <-k.msgs:
			if m.err != nil {
				if k.devices[m.dev.Path] == m.dev {
					log.Printf("input device gone: %s (%s)", m.dev.Name, m.dev.Path)
					m.dev.Close()
					delete(k.devices, m.dev.Path)
				}
				delete(k.frame, m.dev)
				continue
			}
			for _, ev := range m.events {
				k.handle(m.dev, ev)
			}
		case <-flush.C:
			if k.remote {
				if !k.hid.HostSubscribed() {
					k.leaveRemote(k.peerName() + " disconnected")
					continue
				}
				k.flushMotion(false)
			}
		case a := <-acts:
			k.onActivation(a)
		case <-deacts:
			k.leaveRemote("the desktop ended the capture")
		case <-rescan.C:
			k.refreshPeerDevice()
			k.scanDevices()
			if !k.remote {
				k.refreshMonitor()
				k.syncEdge()
			}
		}
	}
}

// ---------------------------------------------------------------- devices

func (k *KVM) ignored(name string) bool {
	for _, s := range k.cfg.Ignore {
		if s != "" && strings.Contains(strings.ToLower(name), strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func (k *KVM) scanDevices() {
	for _, path := range evdev.List() {
		if _, ok := k.devices[path]; ok {
			continue
		}
		if _, ok := k.skipped[path]; ok {
			continue
		}
		d, err := evdev.Open(path)
		if err != nil {
			continue // permission denied or vanished; try again next scan
		}
		kind := kindOf(d)
		if kind == "" || k.ignored(d.Name) {
			d.Close()
			k.skipped[path] = skipInfo{name: d.Name, kind: kind}
			continue
		}
		log.Printf("using %s: %s (%s)", kind, d.Name, path)
		k.devices[path] = d
		if k.remote {
			if err := d.Grab(true); err != nil {
				log.Print(err)
			}
		}
		go k.reader(d)
	}
	// forget skipped paths that disappeared so a new device there is examined
	present := map[string]bool{}
	for _, p := range evdev.List() {
		present[p] = true
	}
	for p := range k.skipped {
		if !present[p] {
			delete(k.skipped, p)
		}
	}
}

func (k *KVM) reader(d *evdev.Device) {
	buf := make([]evdev.Event, 64)
	for {
		n, err := d.Read(buf)
		if err != nil {
			k.msgs <- devMsg{dev: d, err: err}
			return
		}
		evs := make([]evdev.Event, n)
		copy(evs, buf[:n])
		k.msgs <- devMsg{dev: d, events: evs}
	}
}

func (k *KVM) grabAll(on bool) {
	for _, d := range k.devices {
		if err := d.Grab(on); err != nil {
			log.Print(err)
		}
	}
}

// hostNamer is implemented by HID outputs that know the connected device.
type hostNamer interface{ HostName() string }

func (k *KVM) refreshPeerDevice() {
	if n, ok := k.hid.(hostNamer); ok && k.hid.HostSubscribed() {
		k.peerDevice = n.HostName()
	} else if !k.hid.HostSubscribed() {
		k.peerDevice = ""
	}
}

// PeerName is what to call the other device: the user's nickname, else its
// Bluetooth name, else "other device".
func (k *KVM) peerName() string {
	switch {
	case k.cfg.Peer.Name != "":
		return k.cfg.Peer.Name
	case k.peerDevice != "":
		return k.peerDevice
	}
	return "other device"
}

// syncEdge tells an EdgeWatcher backend which edge to watch, when it changed.
func (k *KVM) syncEdge() {
	if k.ew == nil {
		return
	}
	key := fmt.Sprintf("%v|%s", k.mon, k.cfg.Peer.Side)
	if key == k.watchedEdge {
		return
	}
	if err := k.ew.WatchEdge(k.mon, k.cfg.Peer.Side); err != nil {
		log.Printf("edge switching: %v", err)
		k.edgeProblem = err.Error()
		k.watchedEdge = ""
		return
	}
	k.watchedEdge, k.edgeProblem = key, ""
}

// onActivation handles the desktop reporting that the pointer crossed the
// watched edge. The desktop has captured input at this point, so when
// GhostKVM can't switch it must hand input straight back.
func (k *KVM) onActivation(a screen.Activation) {
	if k.remote {
		return
	}
	reason := ""
	switch {
	case !k.hid.HostSubscribed():
		reason = k.peerName() + " is not connected"
	case k.buttons != 0 || k.mods != 0 || len(k.keysDown) != 0:
		reason = "a key or button is held"
	}
	if reason != "" {
		m := k.mon
		x := math.Max(m.X+3, math.Min(m.X+m.Width-4, a.X))
		y := math.Max(m.Y+3, math.Min(m.Y+m.Height-4, a.Y))
		if err := k.scr.MoveCursor(x, y); err != nil && !errors.Is(err, screen.ErrUnsupported) {
			log.Printf("hand input back: %v", err)
		}
		log.Printf("edge reached but not switching: %s", reason)
		return
	}
	k.enterRemote(k.fracAlongEdge(a.X, a.Y), "edge")
}

func (k *KVM) refreshMonitor() error {
	mons, err := k.scr.Monitors()
	if err != nil {
		return fmt.Errorf("%s monitors: %w", k.scr.Name(), err)
	}
	if len(mons) == 0 {
		return fmt.Errorf("%s reports no monitors", k.scr.Name())
	}
	k.monitors = mons
	for _, m := range mons {
		if m.Name == k.cfg.Peer.Monitor {
			k.mon = m
			return nil
		}
	}
	if k.mon.Name == "" {
		log.Printf("monitor %q not found, using %s", k.cfg.Peer.Monitor, mons[0].Name)
	}
	k.mon = mons[0]
	return nil
}

// ---------------------------------------------------------------- events

func (k *KVM) handle(d *evdev.Device, ev evdev.Event) {
	switch ev.Type {
	case evdev.EvKey:
		k.handleKey(ev.Code, ev.Value)
	case evdev.EvRel:
		f := k.frame[d]
		if f == nil {
			f = &frameAcc{}
			k.frame[d] = f
		}
		switch ev.Code {
		case evdev.RelX:
			f.dx += ev.Value
		case evdev.RelY:
			f.dy += ev.Value
		case evdev.RelWheel:
			f.wheel += ev.Value
		}
	case evdev.EvSyn:
		if ev.Code == evdev.SynReport {
			if f := k.frame[d]; f != nil && (f.dx != 0 || f.dy != 0 || f.wheel != 0) {
				k.handleMotion(float64(f.dx), float64(f.dy), int(f.wheel))
				*f = frameAcc{}
			}
		}
	}
}

func buttonBit(code uint16) byte {
	switch code {
	case evdev.BtnLeft:
		return 1
	case evdev.BtnRight:
		return 2
	case evdev.BtnMiddle:
		return 4
	}
	return 0
}

func (k *KVM) handleKey(code uint16, value int32) {
	if value == 2 {
		return // autorepeat: the peer repeats on its own
	}
	down := value == 1

	if bit := buttonBit(code); bit != 0 {
		if down {
			k.buttons |= bit
		} else {
			k.buttons &^= bit
		}
		if k.remote {
			k.flushMotion(true) // button change goes out now, with any pending motion
		}
		return
	}
	if code >= evdev.BtnLeft && code < 0x160 {
		return // other mouse buttons: not forwarded
	}

	if mod, ok := keymap.Modifier[code]; ok {
		if down {
			k.mods |= mod
		} else {
			k.mods &^= mod
		}
	} else if down {
		k.keysDown[code] = true
	} else {
		delete(k.keysDown, code)
	}

	if down && k.hotkey.Matches(code, k.mods) {
		if k.remote {
			k.leaveRemote("hotkey")
		} else {
			k.pendingSwap = true
		}
		return
	}

	if !k.remote {
		if k.pendingSwap && k.mods == 0 && len(k.keysDown) == 0 && k.buttons == 0 {
			k.pendingSwap = false
			frac := 0.5
			if x, y, err := k.scr.CursorPos(); err == nil {
				frac = k.fracAlongEdge(x, y)
			}
			k.enterRemote(frac, "hotkey")
		}
		return
	}

	// remote: forward the keyboard state
	if usage, ok := keymap.Usage[code]; ok {
		if down {
			k.remoteKeys = addKey(k.remoteKeys, usage)
		} else {
			k.remoteKeys = removeKey(k.remoteKeys, usage)
		}
	}
	k.hid.KeyReport(k.mods, k.remoteKeys...)
}

func addKey(keys []byte, u byte) []byte {
	for _, x := range keys {
		if x == u {
			return keys
		}
	}
	if len(keys) == 6 {
		return keys // boot report holds 6 keys
	}
	return append(keys, u)
}

func removeKey(keys []byte, u byte) []byte {
	out := keys[:0]
	for _, x := range keys {
		if x != u {
			out = append(out, x)
		}
	}
	return out
}

// toward returns how much a motion pushes in the direction of the peer.
func (k *KVM) toward(dx, dy float64) float64 {
	switch k.cfg.Peer.Side {
	case "left":
		return -dx
	case "right":
		return dx
	case "top":
		return -dy
	default:
		return dy
	}
}

func (k *KVM) handleMotion(dx, dy float64, wheel int) {
	if k.remote {
		k.remoteMotion(dx, dy, wheel)
		return
	}
	t := k.toward(dx, dy)
	// Never switch mid-drag or with keys held: Hyprland would miss the
	// release once we grab the devices, and the key would stay stuck.
	if !k.scr.EdgeSwitching() || k.ew != nil || t <= 0 || k.buttons != 0 || k.mods != 0 || len(k.keysDown) != 0 || !k.hid.HostSubscribed() {
		k.push = 0
		return
	}
	// Ask Hyprland where the cursor is, at most every 10ms.
	if time.Since(k.lastQuery) >= 10*time.Millisecond {
		k.lastQuery = time.Now()
		x, y, err := k.scr.CursorPos()
		if err != nil {
			return
		}
		k.atEdge = k.isAtEdge(x, y)
		k.edgeFrac = k.fracAlongEdge(x, y)
	}
	if !k.atEdge {
		k.push = 0
		return
	}
	k.push += t
	if k.push >= k.cfg.EdgePush {
		k.push = 0
		k.atEdge = false
		k.enterRemote(k.edgeFrac, "edge")
	}
}

func (k *KVM) isAtEdge(x, y float64) bool {
	m := k.mon
	inX := x >= m.X-1 && x <= m.X+m.Width
	inY := y >= m.Y-1 && y <= m.Y+m.Height
	switch k.cfg.Peer.Side {
	case "left":
		return inY && x <= m.X+1
	case "right":
		return inY && x >= m.X+m.Width-2
	case "top":
		return inX && y <= m.Y+1
	default:
		return inX && y >= m.Y+m.Height-2
	}
}

// fracAlongEdge is the cursor's position along the shared edge, 0..1.
func (k *KVM) fracAlongEdge(x, y float64) float64 {
	m := k.mon
	var f float64
	if k.cfg.Peer.Side == "left" || k.cfg.Peer.Side == "right" {
		f = (y - m.Y) / m.Height
	} else {
		f = (x - m.X) / m.Width
	}
	return math.Max(0, math.Min(1, f))
}

// ---------------------------------------------------------------- switching

func (k *KVM) enterRemote(frac float64, why string) {
	if k.remote {
		return
	}
	k.grabAll(true)
	k.remote = true
	k.overshoot = 0
	k.accX, k.accY, k.accWheel = 0, 0, 0
	k.remoteKeys = k.remoteKeys[:0]

	if err := k.scr.HideCursor(); err != nil {
		log.Printf("hide cursor: %v", err)
	}

	k.placePeerPointer(frac)
	log.Printf("-> %s (%s)", k.peerName(), why)
}

// placePeerPointer pins the peer's pointer against the edge it enters from,
// then slides it along that edge, so we know where it is.
func (k *KVM) placePeerPointer(frac float64) {
	W, H := k.cfg.Peer.Width, k.cfg.Peer.Height
	big := 2 * math.Max(W, H)
	switch k.cfg.Peer.Side {
	case "left": // peer is left of us: enter on its right edge
		k.sendMove(big, -big)
		k.sendMove(0, frac*H)
		k.vx, k.vy = W-1, frac*H
	case "right": // enter on its left edge
		k.sendMove(-big, -big)
		k.sendMove(0, frac*H)
		k.vx, k.vy = 0, frac*H
	case "top": // enter on its bottom edge
		k.sendMove(-big, big)
		k.sendMove(frac*W, 0)
		k.vx, k.vy = frac*W, H-1
	default: // bottom: enter on its top edge
		k.sendMove(-big, -big)
		k.sendMove(frac*W, 0)
		k.vx, k.vy = frac*W, 0
	}
}

// sendMove sends a relative move immediately.
func (k *KVM) sendMove(dx, dy float64) {
	x, y := int(dx), int(dy)
	for x != 0 || y != 0 {
		sx, sy := clamp(x, 32767), clamp(y, 32767)
		k.hid.MouseMove(k.buttons, int16(sx), int16(sy), 0)
		x -= sx
		y -= sy
	}
}

func clamp(v, limit int) int {
	if v > limit {
		return limit
	}
	if v < -limit {
		return -limit
	}
	return v
}

func (k *KVM) leaveRemote(why string) {
	if !k.remote {
		return
	}
	k.remote = false
	// Release everything on the peer so nothing stays stuck.
	k.remoteKeys = k.remoteKeys[:0]
	k.hid.KeyReport(0)
	k.hid.MouseMove(0, 0, 0, 0)
	k.grabAll(false)

	W, H := k.cfg.Peer.Width, k.cfg.Peer.Height
	m := k.mon
	var x, y float64
	switch k.cfg.Peer.Side {
	case "left":
		x, y = m.X+3, m.Y+k.vy/H*m.Height
	case "right":
		x, y = m.X+m.Width-4, m.Y+k.vy/H*m.Height
	case "top":
		x, y = m.X+k.vx/W*m.Width, m.Y+3
	default:
		x, y = m.X+k.vx/W*m.Width, m.Y+m.Height-4
	}
	if why != "edge" {
		x, y = m.X+m.Width/2, m.Y+m.Height/2
	}
	if err := k.scr.MoveCursor(x, y); err != nil && !errors.Is(err, screen.ErrUnsupported) {
		log.Printf("move cursor: %v", err)
	}
	if err := k.scr.ShowCursor(); err != nil {
		log.Printf("show cursor: %v", err)
	}
	log.Printf("<- desktop (%s)", why)
}

// remoteMotion forwards motion to the peer and tracks where its pointer is.
func (k *KVM) remoteMotion(dx, dy float64, wheel int) {
	sp := k.cfg.Peer.Speed
	dx, dy = dx*sp, dy*sp
	W, H := k.cfg.Peer.Width, k.cfg.Peer.Height

	nx, ny := k.vx+dx, k.vy+dy
	// Distance pushed past the edge that leads back to the desktop.
	var past float64
	switch k.cfg.Peer.Side {
	case "left": // desktop is right of the peer
		past = nx - (W - 1)
	case "right":
		past = -nx
	case "top":
		past = ny - (H - 1)
	default:
		past = -ny
	}
	if past > 0 && k.buttons == 0 {
		k.overshoot += past
		if k.overshoot >= k.cfg.EdgePush {
			k.vx, k.vy = math.Max(0, math.Min(W-1, nx)), math.Max(0, math.Min(H-1, ny))
			k.leaveRemote("edge")
			return
		}
	} else {
		k.overshoot = 0
	}
	k.vx = math.Max(0, math.Min(W-1, nx))
	k.vy = math.Max(0, math.Min(H-1, ny))

	k.accX += dx
	k.accY += dy
	k.accWheel += wheel
}

// flushMotion sends all accumulated motion as one report. With force it
// sends even when there is no motion (to deliver a button change).
func (k *KVM) flushMotion(force bool) {
	x, y := int(k.accX), int(k.accY)
	if x == 0 && y == 0 && k.accWheel == 0 && !force {
		return
	}
	sx, sy, sw := clamp(x, 32767), clamp(y, 32767), clamp(k.accWheel, 127)
	k.hid.MouseMove(k.buttons, int16(sx), int16(sy), int8(sw))
	k.accX -= float64(sx)
	k.accY -= float64(sy)
	k.accWheel -= sw
}
