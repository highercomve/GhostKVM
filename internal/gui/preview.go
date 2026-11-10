//go:build screenshots

package gui

import (
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/highercomve/GhostKVM/internal/kvm"
)

// Preview renders the real main window off-screen for the given status,
// with Fyne's software renderer. Built only with -tags screenshots (see
// cmd/ghostkvm-screenshots), so the app binary does not carry the test driver.
func Preview(st kvm.Status, cfgPath string, size fyne.Size, light bool) image.Image {
	forcedLight = &light
	fa := test.NewApp()
	fa.Settings().SetTheme(ghostTheme{})
	a := &App{cfgPath: cfgPath, fa: fa, cfg: st.Config}
	w := fa.NewWindow("GhostKVM")
	w.SetPadded(false)
	w.SetContent(a.build())
	w.Resize(size)
	a.fillSettings()
	a.update(st)
	// The pills and labels got their text after the first layout; lay the
	// window out again so their new sizes are used, as the live app does on
	// its next status update.
	w.Content().Refresh()
	w.Resize(size.AddWidthHeight(1, 1))
	w.Resize(size)
	img := w.Canvas().Capture()
	w.Close()
	return img
}
