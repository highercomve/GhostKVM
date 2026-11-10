// Package assets holds GhostKVM's icons. The SVGs are the sources; the PNGs
// are rendered from them by `make icons` (needs rsvg-convert).
package assets

import _ "embed"

//go:embed ghostkvm.png
var AppIconPNG []byte

//go:embed tray-connected.png
var TrayConnectedPNG []byte

//go:embed tray-waiting.png
var TrayWaitingPNG []byte

//go:embed tray-active.png
var TrayActivePNG []byte
