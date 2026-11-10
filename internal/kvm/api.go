package kvm

import (
	"errors"
	"sort"

	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/hypr"
	"github.com/highercomve/GhostKVM/internal/keymap"
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
	Mode      string         `json:"mode"` // "desktop" or "peer"
	Connected bool           `json:"connected"`
	Config    config.Config  `json:"config"`
	Monitors  []hypr.Monitor `json:"monitors"`
	Devices   []DeviceInfo   `json:"devices"`
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
		st.Monitors = append([]hypr.Monitor(nil), k.monitors...)
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
			err = errors.New(k.cfg.Peer.Name + " is not connected")
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
