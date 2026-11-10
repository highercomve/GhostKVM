package keymap

import "testing"

func TestParseCombo(t *testing.T) {
	cases := []struct {
		in    string
		mods  byte
		usage byte
	}{
		{"ctrl+l", ModLCtrl, 0x0F},
		{"Return", 0, UsageEnter},
		{"ctrl+shift+t", ModLCtrl | ModLShift, 0x17},
		{"search", ModLMeta, 0},
		{"alt+tab", ModLAlt, UsageTab},
		{"ctrl+?", ModLCtrl | ModLShift, 0x38},
	}
	for _, c := range cases {
		m, u, err := ParseCombo(c.in)
		if err != nil || m != c.mods || u != c.usage {
			t.Errorf("ParseCombo(%q) = %#x %#x %v, want %#x %#x", c.in, m, u, err, c.mods, c.usage)
		}
	}
	if _, _, err := ParseCombo("ctrl+nope"); err == nil {
		t.Error("want an error for an unknown key")
	}
}

func TestCharUsage(t *testing.T) {
	if u, s, ok := CharUsage('A'); !ok || u != 0x04 || !s {
		t.Errorf("A = %#x %v %v", u, s, ok)
	}
	if u, s, ok := CharUsage('\n'); !ok || u != UsageEnter || s {
		t.Errorf("newline = %#x %v %v", u, s, ok)
	}
	if _, _, ok := CharUsage('é'); ok {
		t.Error("non-ASCII should not be typeable")
	}
}
