package gui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/highercomve/GhostKVM/internal/serialhid"
	"github.com/highercomve/GhostKVM/internal/update"
)

const (
	outBluetooth = "Bluetooth: this computer acts as a wireless keyboard and mouse"
	outUSB       = "USB bridge: a wired keyboard and mouse through an ESP32-S3 board or CH9329 cable"

	boardGhost  = "GhostKVM ESP32-S3 board (115200 baud)"
	boardCH9329 = "CH9329 cable as it ships (9600 baud)"
)

// buildConnectionCard lets the user pick how input reaches the other device.
func (a *App) buildConnectionCard() fyne.CanvasObject {
	a.portSelect = widget.NewSelect(nil, nil)
	a.portSelect.PlaceHolder = "Choose the board's serial port"
	refresh := widget.NewButton("Refresh", a.refreshPorts)
	a.boardSelect = widget.NewSelect([]string{boardGhost, boardCH9329}, nil)

	a.usbBox = container.NewVBox(
		field("Serial port", container.NewBorder(nil, nil, nil, refresh, a.portSelect),
			"The board's “UART” port, connected to this computer. Its native “USB” port goes to the other device."),
		field("Board", a.boardSelect, "Flash the GhostKVM board once with: make flash PORT=<this port>"),
	)
	a.outputSelect = widget.NewRadioGroup([]string{outBluetooth, outUSB}, func(s string) {
		if s == outUSB {
			a.refreshPorts()
			a.usbBox.Show()
		} else {
			a.usbBox.Hide()
		}
	})
	a.outputSelect.Required = true

	save := widget.NewButton("Save and restart", a.saveConnection)
	save.Importance = widget.HighImportance
	reconnect := widget.NewButton("Reconnect", a.reconnect)
	hint := widget.NewLabel("Bluetooth lagging? Reconnect gives the link a fresh start; the device is back in a few seconds.")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	hint.SizeName = theme.SizeNameCaptionText
	return newCard("Connection", "How the keyboard and mouse reach the other device. Changing it restarts GhostKVM.",
		container.NewVBox(a.outputSelect, a.usbBox, hint, container.NewHBox(reconnect, layout.NewSpacer(), save)))
}

// refreshPorts lists serial ports without opening them: other hardware may be
// attached, so GhostKVM never probes ports on its own.
func (a *App) refreshPorts() {
	a.ports = serialhid.Ports()
	names := make([]string, len(a.ports))
	sel := ""
	for i, p := range a.ports {
		names[i] = p.Name
		if p.Path == a.cfg.USBPort {
			sel = p.Name
		}
	}
	a.portSelect.Options = names
	if sel != "" {
		a.portSelect.SetSelected(sel)
	}
	a.portSelect.Refresh()
}

func (a *App) fillConnection() {
	if a.cfg.Output == "usb" {
		a.outputSelect.SetSelected(outUSB)
	} else {
		a.outputSelect.SetSelected(outBluetooth)
	}
	if a.cfg.USBBaud == 9600 {
		a.boardSelect.SetSelected(boardCH9329)
	} else {
		a.boardSelect.SetSelected(boardGhost)
	}
}

func (a *App) saveConnection() {
	c := a.cfg
	if a.outputSelect.Selected == outUSB {
		c.Output = "usb"
		idx := a.portSelect.SelectedIndex()
		if idx < 0 || idx >= len(a.ports) {
			dialog.ShowError(fmt.Errorf("choose the board's serial port first"), a.win)
			return
		}
		c.USBPort = a.ports[idx].Path
		c.USBBaud = 115200
		if a.boardSelect.Selected == boardCH9329 {
			c.USBBaud = 9600
		}
	} else {
		c.Output = "bluetooth"
	}
	if c.Output == a.cfg.Output && c.USBPort == a.cfg.USBPort && c.USBBaud == a.cfg.USBBaud {
		return
	}
	a.applyThen(c, a.restart)
}

// restart starts a new GhostKVM and quits this one; the new one waits for
// this process to release Bluetooth, the bridge and the control socket.
func (a *App) restart() {
	exe, err := os.Executable()
	if err == nil {
		err = update.Relaunch(update.Install{Path: strings.TrimSuffix(exe, " (deleted)")})
	}
	if err != nil {
		dialog.ShowError(fmt.Errorf("saved, but couldn't restart: %w. Restart GhostKVM yourself", err), a.win)
		return
	}
	a.fa.Quit()
}

// reconnect refreshes the Bluetooth link to the other device.
func (a *App) reconnect() {
	go func() {
		err := a.k.Reconnect()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, a.win)
				return
			}
			a.savedAt = time.Now()
			a.savedNote.SetText("Reconnecting…")
		})
	}()
}
