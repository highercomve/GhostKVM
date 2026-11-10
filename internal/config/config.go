// Package config loads ~/.config/ghostkvm/config.toml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Peer is a machine placed next to one of the desktop's monitors.
type Peer struct {
	Name    string  `toml:"name" json:"name"`
	Side    string  `toml:"side" json:"side"`       // left | right | top | bottom
	Monitor string  `toml:"monitor" json:"monitor"` // desktop monitor it touches; empty = first
	Width   float64 `toml:"width" json:"width"`     // peer screen size, used to track its pointer
	Height  float64 `toml:"height" json:"height"`
	Speed   float64 `toml:"speed" json:"speed"` // multiplier for mouse motion sent to the peer
}

// Config is the whole file.
type Config struct {
	BluetoothName string   `toml:"bluetooth_name" json:"bluetooth_name"`
	Hotkey        string   `toml:"hotkey" json:"hotkey"`
	EdgePush      float64  `toml:"edge_push" json:"edge_push"`                               // how far to keep pushing past an edge before switching
	Ignore        []string `toml:"ignore" json:"ignore"`                                     // input device names (substring) never grabbed
	ScreenBackend string   `toml:"screen_backend,omitempty" json:"screen_backend,omitempty"` // auto | hyprland | x11 | none
	Peer          Peer     `toml:"peer" json:"peer"`
}

// Default is written to disk the first time GhostKVM runs.
const Default = `# GhostKVM configuration

# Name the desktop advertises as a Bluetooth keyboard+mouse.
bluetooth_name = "GhostKVM"

# Switch between the desktop and the peer from anywhere.
hotkey = "Ctrl+Alt+K"

# Pixels you have to keep pushing past the edge before control moves over.
# Stops accidental switches when you only touch the edge.
edge_push = 40

# Input devices never captured (matched as a substring of the device name).
ignore = ["ydotoold", "YubiKey", "AVRCP"]

# How GhostKVM sees your screen: auto, hyprland, x11, portal or none. "auto"
# uses Hyprland or X11 when available, then the Wayland InputCapture portal
# (GNOME 45+, KDE Plasma 6.1+); other desktops switch with the hotkey and tray.
screen_backend = "auto"

[peer]
name    = "chromebook"
side    = "left"        # left | right | top | bottom of the desktop monitor
monitor = "HDMI-A-1"    # which desktop monitor it sits next to
width   = 1920          # Chromebook screen resolution
height  = 1080
speed   = 1.5           # raise if the Chromebook pointer feels slow
`

// Path returns the config file location.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ghostkvm", "config.toml")
}

// Load reads the config, writing the default first if none exists.
func Load(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(Default), 0o644); err != nil {
			return nil, err
		}
	}
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate fills defaults and checks values.
func (c *Config) Validate() error {
	if c.BluetoothName == "" {
		c.BluetoothName = "GhostKVM"
	}
	if c.Hotkey == "" {
		c.Hotkey = "Ctrl+Alt+K"
	}
	if c.EdgePush <= 0 {
		c.EdgePush = 40
	}
	if c.Peer.Speed <= 0 {
		c.Peer.Speed = 1
	}
	if c.Peer.Name == "" {
		c.Peer.Name = "peer"
	}
	c.Peer.Side = strings.ToLower(c.Peer.Side)
	switch c.Peer.Side {
	case "left", "right", "top", "bottom":
	default:
		return fmt.Errorf("peer side must be left, right, top or bottom, got %q", c.Peer.Side)
	}
	if c.Peer.Width < 100 || c.Peer.Height < 100 || c.Peer.Width > 16384 || c.Peer.Height > 16384 {
		return fmt.Errorf("peer resolution %vx%v is not valid", c.Peer.Width, c.Peer.Height)
	}
	if c.Peer.Speed > 20 {
		return fmt.Errorf("speed %v is too high (max 20)", c.Peer.Speed)
	}
	return nil
}

var saveMu sync.Mutex

// Save writes the config, replacing the file atomically. Safe to call from
// several goroutines: saves are serialised and each uses its own temp file.
func Save(path string, c *Config) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# GhostKVM configuration (saved by the GhostKVM app; comments are not kept)\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op once renamed
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
