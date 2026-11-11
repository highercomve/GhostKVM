package kvm

import (
	"errors"
	"sort"

	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/keymap"
	"github.com/highercomve/GhostKVM/internal/screen"
)

// ErrStopped is returned by the API once Run has returned.
var ErrStopped = errors.New("kvm is not running")

// do runs f on Run's goroutine and waits for it.
func (k *KVM) do(f func()) error {
	done := make(chan struct{})
	select {
	case k.cmds <- func() { f(); close(done) }:
		<-done
		return nil
	case <-k.stopped:
		return ErrStopped
	}
}

// DeviceInfo describes one keyboard or mouse for the UI.
type DeviceInfo struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // keyboard | mouse
	Used    bool   `json:"used"`
	Ignored bool   `json:"ignored"`
}

// Status is a snapshot of the KVM for the UI.
type Status struct {
	Mode          string           `json:"mode"` // "desktop" or "peer"
	Connected     bool             `json:"connected"`
	Config        config.Config    `json:"config"`
	Monitors      []screen.Monitor `json:"monitors"`
	Desktop       string           `json:"desktop"`                  // screen backend, e.g. "Hyprland", "X11", "GNOME (Wayland)"
	EdgeSwitching bool             `json:"edge_switching"`           // false: switch with the hotkey or tray
	EdgeProblem   string           `json:"edge_problem,omitempty"`   // why edge switching isn\'t working right now
	PeerName      string           `json:"peer_name"`                // nickname, else the connected device\'s Bluetooth name, else "other device"
	PeerDevice    string           `json:"peer_device,omitempty"`    // Bluetooth name of the connected device
	Output        string           `json:"output"`                   // bluetooth | usb
	OutputProblem string           `json:"output_problem,omitempty"` // why the output couldn\'t start
	Devices       []DeviceInfo     `json:"devices"`
}

// Status returns the current state.
func (k *KVM) Status() (Status, error) {
	var st Status
	err := k.do(func() {
		st.Mode = "desktop"
		if k.remote {
			st.Mode = "peer"
		}
		st.Connected = k.hid.HostSubscribed()
		st.Config = *k.cfg
		st.Config.Ignore = append([]string(nil), k.cfg.Ignore...)
		st.Monitors = append([]screen.Monitor(nil), k.monitors...)
		st.Desktop = k.scr.Name()
		st.EdgeSwitching = k.scr.EdgeSwitching()
		st.EdgeProblem = k.edgeProblem
		st.PeerName = k.peerName()
		st.PeerDevice = k.peerDevice
		st.Output = k.cfg.Output
		st.OutputProblem = k.outputProblem
		for path, d := range k.devices {
			st.Devices = append(st.Devices, DeviceInfo{Path: path, Name: d.Name, Kind: kindOf(d), Used: true})
		}
		for path, s := range k.skipped {
			if s.kind != "" {
				st.Devices = append(st.Devices, DeviceInfo{Path: path, Name: s.name, Kind: s.kind, Ignored: true})
			}
		}
		sort.Slice(st.Devices, func(i, j int) bool {
			if st.Devices[i].Name != st.Devices[j].Name {
				return st.Devices[i].Name < st.Devices[j].Name
			}
			return st.Devices[i].Path < st.Devices[j].Path
		})
	})
	return st, err
}

// Apply validates and switches to a new configuration without restarting.
// The Bluetooth name is only used at startup.
func (k *KVM) Apply(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	hk, err := keymap.ParseHotkey(c.Hotkey)
	if err != nil {
		return err
	}
	return k.do(func() {
		if k.remote && k.cfg.Peer != c.Peer {
			k.leaveRemote("layout changed")
		}
		ignoreChanged := !equalStrings(k.cfg.Ignore, c.Ignore)
		*k.cfg = c
		k.hotkey = hk
		k.refreshMonitor()
		k.syncEdge()
		if ignoreChanged {
			// Drop devices that are now ignored; re-examine everything else.
			for path, d := range k.devices {
				if k.ignored(d.Name) {
					d.Close()
					delete(k.devices, path)
				}
			}
			k.skipped = map[string]skipInfo{}
			k.scanDevices()
		}
	})
}

// Toggle moves control to the peer, or back to the desktop.
func (k *KVM) Toggle() error {
	var err error
	if e := k.do(func() {
		if k.remote {
			k.leaveRemote("UI")
			return
		}
		if !k.hid.HostSubscribed() {
			err = errors.New(k.peerName() + " is not connected")
			return
		}
		k.enterRemote(0.5, "UI")
	}); e != nil {
		return e
	}
	return err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- control API

// ErrNotConnected is returned when the peer is not subscribed to input.
var ErrNotConnected = errors.New("the other computer is not connected")

// PeerHID gives direct access to the peer's keyboard and mouse, for the
// control socket. Reports sent through it reach the peer whichever computer
// currently has the user's keyboard and mouse.
func (k *KVM) PeerHID() HID { return k.hid }

// MovePeerTo puts the peer's pointer at (x, y) in peer pixels: it pins the
// pointer to the top-left corner, then moves it. Accurate when the peer has
// mouse acceleration off.
func (k *KVM) MovePeerTo(x, y float64) error {
	if !k.hid.HostSubscribed() {
		return ErrNotConnected
	}
	return k.do(func() {
		W, H := k.cfg.Peer.Width, k.cfg.Peer.Height
		x, y = clampF(x, 0, W-1), clampF(y, 0, H-1)
		if am, ok := k.hid.(absMover); ok { // exact, no acceleration
			am.MoveAbs(x/(W-1), y/(H-1))
			k.vx, k.vy = x, y
			return
		}
		big := 2 * max(W, H)
		k.sendMove(-big, -big)
		k.sendMove(x, y)
		k.vx, k.vy = x, y
	})
}

// MovePeerBy moves the peer's pointer by (dx, dy) peer pixels.
func (k *KVM) MovePeerBy(dx, dy float64) error {
	if !k.hid.HostSubscribed() {
		return ErrNotConnected
	}
	return k.do(func() {
		k.sendMove(dx, dy)
		k.vx = clampF(k.vx+dx, 0, k.cfg.Peer.Width-1)
		k.vy = clampF(k.vy+dy, 0, k.cfg.Peer.Height-1)
	})
}

// reconnecter is implemented by outputs whose link can be refreshed.
type reconnecter interface{ Reconnect() error }

// Reconnect refreshes the link to the other device (Bluetooth): it drops the
// connection and the device reconnects within seconds. Control comes back
// to this computer first, since the device is briefly gone.
func (k *KVM) Reconnect() error {
	rc, ok := k.hid.(reconnecter)
	if !ok {
		return errors.New("this connection has nothing to reconnect")
	}
	var err error
	if e := k.do(func() {
		k.leaveRemote("reconnecting")
		err = rc.Reconnect()
	}); e != nil {
		return e
	}
	return err
}

// SetMode gives the user's keyboard and mouse to the peer (true) or back
// to this computer (false).
func (k *KVM) SetMode(peer bool) error {
	var err error
	if e := k.do(func() {
		switch {
		case peer == k.remote:
		case !peer:
			k.leaveRemote("control socket")
		case !k.hid.HostSubscribed():
			err = ErrNotConnected
		default:
			k.enterRemote(0.5, "control socket")
		}
	}); e != nil {
		return e
	}
	return err
}

func clampF(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }
