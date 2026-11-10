package gui

import (
	"fyne.io/fyne/v2"

	"github.com/highercomve/GhostKVM/assets"
)

// Tray icons follow the link state; the window and launcher use the app icon.
var (
	appIcon          = fyne.NewStaticResource("ghostkvm.png", assets.AppIconPNG)
	iconConnected    = fyne.NewStaticResource("ghostkvm-connected.png", assets.TrayConnectedPNG)
	iconDisconnected = fyne.NewStaticResource("ghostkvm-waiting.png", assets.TrayWaitingPNG)
	iconPeerActive   = fyne.NewStaticResource("ghostkvm-active.png", assets.TrayActivePNG)
)
