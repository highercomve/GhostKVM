// Package keymap translates Linux evdev key codes to USB HID keyboard usages
// (usage page 0x07) and parses hotkey strings like "Ctrl+Alt+K".
package keymap

import (
	"fmt"
	"strings"
)

// HID modifier bits (byte 0 of a boot keyboard report).
const (
	ModLCtrl  = 0x01
	ModLShift = 0x02
	ModLAlt   = 0x04
	ModLMeta  = 0x08
	ModRCtrl  = 0x10
	ModRShift = 0x20
	ModRAlt   = 0x40
	ModRMeta  = 0x80
)

// Modifier maps an evdev modifier key to its HID modifier bit.
var Modifier = map[uint16]byte{
	29: ModLCtrl, 42: ModLShift, 56: ModLAlt, 125: ModLMeta,
	97: ModRCtrl, 54: ModRShift, 100: ModRAlt, 126: ModRMeta,
}

// Usage maps evdev key codes to HID usages. Limited to 0x00-0x65, the range
// GhostKVM's report map declares; media keys are not forwarded.
var Usage = map[uint16]byte{
	1: 0x29,                                                                                    // Esc
	2: 0x1E, 3: 0x1F, 4: 0x20, 5: 0x21, 6: 0x22, 7: 0x23, 8: 0x24, 9: 0x25, 10: 0x26, 11: 0x27, // 1..0
	12: 0x2D, 13: 0x2E, 14: 0x2A, 15: 0x2B, // - = Backspace Tab
	16: 0x14, 17: 0x1A, 18: 0x08, 19: 0x15, 20: 0x17, 21: 0x1C, 22: 0x18, 23: 0x0C, 24: 0x12, 25: 0x13, // q..p
	26: 0x2F, 27: 0x30, 28: 0x28, // [ ] Enter
	30: 0x04, 31: 0x16, 32: 0x07, 33: 0x09, 34: 0x0A, 35: 0x0B, 36: 0x0D, 37: 0x0E, 38: 0x0F, // a..l
	39: 0x33, 40: 0x34, 41: 0x35, 43: 0x31, // ; ' ` backslash
	44: 0x1D, 45: 0x1B, 46: 0x06, 47: 0x19, 48: 0x05, 49: 0x11, 50: 0x10, // z..m
	51: 0x36, 52: 0x37, 53: 0x38, // , . /
	55: 0x55, 57: 0x2C, 58: 0x39, // KP* Space CapsLock
	59: 0x3A, 60: 0x3B, 61: 0x3C, 62: 0x3D, 63: 0x3E, 64: 0x3F, 65: 0x40, 66: 0x41, 67: 0x42, 68: 0x43, // F1..F10
	69: 0x53, 70: 0x47, // NumLock ScrollLock
	71: 0x5F, 72: 0x60, 73: 0x61, 74: 0x56, 75: 0x5C, 76: 0x5D, 77: 0x5E, 78: 0x57, // KP7 8 9 - 4 5 6 +
	79: 0x59, 80: 0x5A, 81: 0x5B, 82: 0x62, 83: 0x63, // KP1 2 3 0 .
	86: 0x64, 87: 0x44, 88: 0x45, // 102nd F11 F12
	96: 0x58, 98: 0x54, 99: 0x46, // KPEnter KP/ SysRq
	102: 0x4A, 103: 0x52, 104: 0x4B, 105: 0x50, 106: 0x4F, 107: 0x4D, 108: 0x51, 109: 0x4E, // Home Up PgUp Left Right End Down PgDn
	110: 0x49, 111: 0x4C, 119: 0x48, 127: 0x65, // Insert Delete Pause Menu
}

var names = map[string]uint16{
	"esc": 1, "escape": 1, "tab": 15, "enter": 28, "return": 28, "space": 57, "backspace": 14,
	"capslock": 58, "scrolllock": 70, "pause": 119, "insert": 110, "delete": 111, "home": 102,
	"end": 107, "pageup": 104, "pagedown": 109, "up": 103, "down": 108, "left": 105, "right": 106,
	"f1": 59, "f2": 60, "f3": 61, "f4": 62, "f5": 63, "f6": 64, "f7": 65, "f8": 66, "f9": 67,
	"f10": 68, "f11": 87, "f12": 88, "grave": 41, "`": 41, "menu": 127,
}

func init() {
	for i, c := range "qwertyuiop" {
		names[string(c)] = uint16(16 + i)
	}
	for i, c := range "asdfghjkl" {
		names[string(c)] = uint16(30 + i)
	}
	for i, c := range "zxcvbnm" {
		names[string(c)] = uint16(44 + i)
	}
	for i, c := range "1234567890" {
		names[string(c)] = uint16(2 + i)
	}
}

// Hotkey is a set of modifiers (either side) plus one key.
type Hotkey struct {
	Ctrl, Shift, Alt, Meta bool
	Key                    uint16
}

// ParseHotkey parses strings like "Ctrl+Alt+K" or "Super+Shift+F12".
func ParseHotkey(s string) (Hotkey, error) {
	var h Hotkey
	for _, part := range strings.Split(s, "+") {
		p := strings.ToLower(strings.TrimSpace(part))
		switch p {
		case "ctrl", "control":
			h.Ctrl = true
		case "shift":
			h.Shift = true
		case "alt":
			h.Alt = true
		case "super", "meta", "win", "search":
			h.Meta = true
		default:
			code, ok := names[p]
			if !ok {
				return h, fmt.Errorf("unknown key %q in hotkey %q", part, s)
			}
			if h.Key != 0 {
				return h, fmt.Errorf("hotkey %q has more than one non-modifier key", s)
			}
			h.Key = code
		}
	}
	if h.Key == 0 {
		return h, fmt.Errorf("hotkey %q has no key", s)
	}
	return h, nil
}

// Matches reports whether key was just pressed with exactly the hotkey's
// modifiers held (mods is a HID modifier bitmap).
func (h Hotkey) Matches(key uint16, mods byte) bool {
	return key == h.Key &&
		h.Ctrl == (mods&(ModLCtrl|ModRCtrl) != 0) &&
		h.Shift == (mods&(ModLShift|ModRShift) != 0) &&
		h.Alt == (mods&(ModLAlt|ModRAlt) != 0) &&
		h.Meta == (mods&(ModLMeta|ModRMeta) != 0)
}
