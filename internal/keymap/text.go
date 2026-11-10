package keymap

import (
	"fmt"
	"strings"
)

// HID usages for keys that have no printable character.
const (
	UsageEnter     = 0x28
	UsageEscape    = 0x29
	UsageBackspace = 0x2A
	UsageTab       = 0x2B
	UsageSpace     = 0x2C
)

// charUsage maps printable ASCII to a HID usage and whether Shift is needed,
// for a US keyboard layout (the Chromebook's input layout must match).
var charUsage = map[rune]struct {
	usage byte
	shift bool
}{}

func init() {
	for i, c := range "abcdefghijklmnopqrstuvwxyz" {
		charUsage[c] = struct {
			usage byte
			shift bool
		}{byte(0x04 + i), false}
		charUsage[c-'a'+'A'] = struct {
			usage byte
			shift bool
		}{byte(0x04 + i), true}
	}
	set := func(chars string, usages []byte, shift bool) {
		for i, c := range chars {
			charUsage[c] = struct {
				usage byte
				shift bool
			}{usages[i], shift}
		}
	}
	set("1234567890", []byte{0x1E, 0x1F, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27}, false)
	set("!@#$%^&*()", []byte{0x1E, 0x1F, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27}, true)
	set("-=[]\\;'`,./", []byte{0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, false)
	set("_+{}|:\"~<>?", []byte{0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, true)
	set(" \n\t", []byte{UsageSpace, UsageEnter, UsageTab}, false)
}

// CharUsage returns the HID usage for a character and whether it needs Shift.
func CharUsage(r rune) (usage byte, shift bool, ok bool) {
	u, ok := charUsage[r]
	return u.usage, u.shift, ok
}

// comboNames maps key names used in combos to HID usages.
var comboNames = map[string]byte{
	"enter": UsageEnter, "return": UsageEnter, "esc": UsageEscape, "escape": UsageEscape,
	"backspace": UsageBackspace, "tab": UsageTab, "space": UsageSpace,
	"capslock": 0x39, "printscreen": 0x46, "scrolllock": 0x47, "pause": 0x48,
	"insert": 0x49, "home": 0x4A, "pageup": 0x4B, "delete": 0x4C, "end": 0x4D, "pagedown": 0x4E,
	"right": 0x4F, "left": 0x50, "down": 0x51, "up": 0x52, "menu": 0x65,
	"f1": 0x3A, "f2": 0x3B, "f3": 0x3C, "f4": 0x3D, "f5": 0x3E, "f6": 0x3F,
	"f7": 0x40, "f8": 0x41, "f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"minus": 0x2D, "equal": 0x2E, "comma": 0x36, "period": 0x37, "slash": 0x38,
}

// ParseCombo parses key combos like "ctrl+l", "ctrl+shift+t", "Return",
// "search" or "alt+tab" into a HID modifier bitmap and one key usage. A combo
// of only modifiers (e.g. "search", which opens the ChromeOS launcher) has
// usage 0.
func ParseCombo(s string) (mods byte, usage byte, err error) {
	parts := strings.Split(s, "+")
	for i, part := range parts {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" && i == len(parts)-1 && i > 0 { // "ctrl++" means ctrl and plus
			p = "+"
		}
		switch p {
		case "ctrl", "control":
			mods |= ModLCtrl
			continue
		case "shift":
			mods |= ModLShift
			continue
		case "alt":
			mods |= ModLAlt
			continue
		case "super", "meta", "win", "search", "launcher":
			mods |= ModLMeta
			continue
		}
		if usage != 0 {
			return 0, 0, fmt.Errorf("combo %q has more than one non-modifier key", s)
		}
		if u, ok := comboNames[p]; ok {
			usage = u
			continue
		}
		if r := []rune(p); len(r) == 1 {
			if u, shift, ok := CharUsage(r[0]); ok {
				usage = u
				if shift {
					mods |= ModLShift
				}
				continue
			}
		}
		return 0, 0, fmt.Errorf("unknown key %q in combo %q", part, s)
	}
	if mods == 0 && usage == 0 {
		return 0, 0, fmt.Errorf("empty combo")
	}
	return mods, usage, nil
}
