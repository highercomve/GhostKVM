//go:build screenshots

// ghostkvm-screenshots renders GhostKVM's window with sample data and writes
// the images used by the README and the website. It runs the real UI code
// off-screen, so the pictures always match the app and never show the
// machine they were made on.
//
//	go run -tags screenshots ./cmd/ghostkvm-screenshots docs/images
package main

import (
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"

	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/gui"
	"github.com/highercomve/GhostKVM/internal/kvm"
	"github.com/highercomve/GhostKVM/internal/screen"
)

func sample() kvm.Status {
	return kvm.Status{
		Mode:      "desktop",
		Connected: true,
		Config: config.Config{
			BluetoothName: "GhostKVM",
			Hotkey:        "Ctrl+Alt+K",
			EdgePush:      40,
			Ignore:        []string{"YubiKey"},
			Peer: config.Peer{
				Name: "", Side: "left", Monitor: "DP-1",
				Width: 1920, Height: 1080, Speed: 1.2,
			},
		},
		Monitors:      []screen.Monitor{{Name: "DP-1", X: 0, Y: 0, Width: 2560, Height: 1440}},
		Desktop:       "Hyprland",
		EdgeSwitching: true,
		Output:        "bluetooth",
		PeerName:      "Chromebook",
		PeerDevice:    "Chromebook",
		Devices: []kvm.DeviceInfo{
			{Path: "/dev/input/event3", Name: "Mechanical Keyboard", Kind: "keyboard", Used: true},
			{Path: "/dev/input/event5", Name: "Wireless Mouse", Kind: "mouse", Used: true},
			{Path: "/dev/input/event7", Name: "Security Key", Kind: "keyboard", Ignored: true},
		},
	}
}

func main() {
	out := "docs/images"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	const cfg = "~/.config/ghostkvm/config.toml"

	connected := sample()

	peer := sample()
	peer.Mode = "peer"

	waiting := sample()
	waiting.Connected = false
	waiting.PeerName, waiting.PeerDevice = "other device", ""

	above := sample()
	above.PeerName, above.PeerDevice = "Pixel Tablet", "Pixel Tablet" // any Bluetooth device works
	above.Config.Peer.Side = "top"
	above.Config.Peer.Width, above.Config.Peer.Height = 1600, 1000
	above.Monitors = []screen.Monitor{
		{Name: "DP-1", X: 0, Y: 0, Width: 2560, Height: 1440},
		{Name: "HDMI-A-1", X: 2560, Y: 180, Width: 1920, Height: 1080},
	}

	scenes := []struct {
		file  string
		st    kvm.Status
		size  fyne.Size
		light bool
	}{
		{"app.png", connected, fyne.NewSize(1120, 980), false},
		{"app-light.png", connected, fyne.NewSize(1120, 980), true},
		{"app-peer.png", peer, fyne.NewSize(1120, 640), false},
		{"app-waiting.png", waiting, fyne.NewSize(1120, 640), false},
		{"app-two-monitors.png", above, fyne.NewSize(1120, 640), false},
		{"app-full.png", connected, fyne.NewSize(1120, 1720), false},
	}
	for _, s := range scenes {
		img := gui.Preview(s.st, cfg, s.size, s.light)
		write(filepath.Join(out, s.file), img)
	}
}

func write(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	log.Print("wrote ", path)
}
