// Package gui is GhostKVM's desktop app: a tray icon that keeps running in
// the background, and a window to arrange the peer and change settings.
package gui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/kvm"
	"github.com/highercomve/GhostKVM/internal/update"
	"github.com/highercomve/GhostKVM/internal/version"
)

// App is the running desktop app.
type App struct {
	k       *kvm.KVM
	cfgPath string
	fa      fyne.App
	win     fyne.Window
	desk    desktop.App // nil when the platform has no tray

	cfg config.Config // last applied config, the base for edits

	// header
	linkPill  *pill
	modePill  *pill
	switchBtn *widget.Button
	savedNote *widget.Label
	savedAt   time.Time
	updateBtn *widget.Button

	pendingUpdate *update.Release

	// layout
	editor  *layoutEditor
	summary *widget.Label

	// peer settings
	nameEntry   *widget.Entry
	resSelect   *widget.Select
	widthEntry  *widget.Entry
	heightEntry *widget.Entry
	customRes   *fyne.Container
	speedSlider *widget.Slider
	speedValue  *widget.Label

	// switching
	pushSlider  *widget.Slider
	pushValue   *widget.Label
	hotkeyEntry *widget.Entry

	// devices
	deviceBox *fyne.Container
	deviceSig string

	// tray
	trayStatus *fyne.MenuItem
	traySwitch *fyne.MenuItem
	trayUpdate *fyne.MenuItem
	trayMenu   *fyne.Menu
	lastIcon   fyne.Resource

	filling bool // true while controls are set from config (no apply)
}

var resolutions = []string{"1366 × 768", "1536 × 864", "1600 × 900", "1920 × 1080", "1920 × 1200", "2560 × 1440", "Custom…"}

var current fyne.App

// Quit asks a running app to exit (safe from any goroutine).
func Quit() {
	if current != nil {
		fyne.Do(current.Quit)
	}
}

// Run builds the app and blocks until the user quits. onQuit is called
// before the app exits so the caller can stop the KVM cleanly.
func Run(k *kvm.KVM, cfgPath string, onQuit func()) {
	a := &App{k: k, cfgPath: cfgPath, fa: app.NewWithID("io.ghostkvm.app")}
	current = a.fa
	a.fa.Settings().SetTheme(ghostTheme{})
	a.fa.SetIcon(appIcon)
	a.win = a.fa.NewWindow("GhostKVM")
	a.win.SetIcon(appIcon)

	if st, err := k.Status(); err == nil {
		a.cfg = st.Config
	}
	a.win.SetContent(a.build())
	a.win.Resize(fyne.NewSize(920, 760))
	a.fillSettings()

	if d, ok := a.fa.(desktop.App); ok {
		a.desk = d
		a.setupTray()
		// Closing the window keeps GhostKVM running in the tray.
		a.win.SetCloseIntercept(func() { a.win.Hide() })
	}

	stop := make(chan struct{})
	go a.poll(stop)
	a.maybeCheckForUpdates()
	a.fa.Lifecycle().SetOnStopped(func() {
		close(stop)
		onQuit()
	})
	a.win.Show()
	a.fa.Run()
}

// ---------------------------------------------------------------- building

func (a *App) build() fyne.CanvasObject {
	content := container.NewVBox(
		a.buildLayoutCard(),
		container.NewGridWithColumns(2, a.buildPeerCard(), a.buildSwitchCard()),
		a.buildDevicesCard(),
		a.buildFooter(),
	)
	scroll := container.NewVScroll(container.New(&inset{20, 4}, content))
	return container.NewBorder(a.buildHeader(), nil, nil, nil, scroll)
}

func (a *App) buildHeader() fyne.CanvasObject {
	logo := canvas.NewImageFromResource(appIcon)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(40, 40))

	title := widget.NewLabelWithStyle("GhostKVM", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameHeadingText
	sub := widget.NewLabel("One keyboard and mouse for this computer and your Chromebook")
	sub.Importance = widget.LowImportance
	sub.SizeName = theme.SizeNameCaptionText
	brand := container.NewHBox(logo, container.New(&tightVBox{gap: -10}, title, sub))

	a.linkPill = newPill()
	a.modePill = newPill()
	a.savedNote = widget.NewLabel("")
	a.savedNote.Importance = widget.SuccessImportance
	a.savedNote.SizeName = theme.SizeNameCaptionText
	a.switchBtn = widget.NewButtonWithIcon("Switch", theme.ViewRefreshIcon(), a.toggle)
	a.switchBtn.Importance = widget.HighImportance
	a.updateBtn = widget.NewButtonWithIcon("Update", theme.DownloadIcon(), a.showUpdateDialog)
	a.updateBtn.Importance = widget.WarningImportance
	a.updateBtn.Hide()

	right := container.NewHBox(a.savedNote, container.NewCenter(a.updateBtn), container.NewCenter(a.linkPill), container.NewCenter(a.modePill), container.NewCenter(a.switchBtn))
	bar := container.NewBorder(nil, nil, brand, right)

	bg := canvas.NewRectangle(pal().card)
	line := canvas.NewRectangle(pal().border)
	line.SetMinSize(fyne.NewSize(1, 1))
	return container.NewBorder(nil, line, nil, nil, container.NewStack(bg, container.New(&inset{20, 12}, bar)))
}

func (a *App) buildLayoutCard() fyne.CanvasObject {
	a.editor = newLayoutEditor(func(side, monitor string) {
		c := a.cfg
		c.Peer.Side, c.Peer.Monitor = side, monitor
		a.apply(c)
	})
	a.summary = widget.NewLabel("")
	a.summary.Wrapping = fyne.TextWrapWord
	a.summary.Importance = widget.LowImportance
	editor := container.New(&minHeight{h: 300}, a.editor)
	return newCard("Screen layout", "Drag the Chromebook to the side of your monitor where it sits on your desk.",
		container.NewBorder(nil, a.summary, nil, nil, editor))
}

func (a *App) buildPeerCard() fyne.CanvasObject {
	a.nameEntry = widget.NewEntry()
	a.nameEntry.SetPlaceHolder("chromebook")
	a.nameEntry.OnSubmitted = func(string) { a.commitName() }

	a.widthEntry = widget.NewEntry()
	a.heightEntry = widget.NewEntry()
	apply := widget.NewButton("Apply", a.commitCustomRes)
	a.customRes = container.NewHBox(fixedWidth(a.widthEntry, 80), widget.NewLabel("×"), fixedWidth(a.heightEntry, 80), apply)
	a.customRes.Hide()
	a.resSelect = widget.NewSelect(resolutions, func(s string) {
		if w, h, ok := parseRes(s); ok {
			a.customRes.Hide()
			if a.filling {
				return
			}
			c := a.cfg
			c.Peer.Width, c.Peer.Height = float64(w), float64(h)
			a.apply(c)
			return
		}
		a.widthEntry.SetText(strconv.Itoa(int(a.cfg.Peer.Width)))
		a.heightEntry.SetText(strconv.Itoa(int(a.cfg.Peer.Height)))
		a.customRes.Show()
	})

	a.speedValue = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
	a.speedSlider = widget.NewSlider(0.5, 5)
	a.speedSlider.Step = 0.1
	a.speedSlider.OnChanged = func(v float64) { a.speedValue.SetText(fmt.Sprintf("%.1f×", v)) }
	a.speedSlider.OnChangeEnded = func(v float64) {
		c := a.cfg
		c.Peer.Speed = round1(v)
		a.apply(c)
	}

	return newCard("Chromebook", "", container.NewVBox(
		field("Name", a.nameEntry, "Shown in the layout and the tray. Press Enter to save."),
		field("Screen resolution", container.NewVBox(a.resSelect, a.customRes), "On the Chromebook: Settings › Device › Displays."),
		field("Pointer speed", container.NewBorder(nil, nil, nil, fixedWidth(a.speedValue, 48), a.speedSlider), "Raise it if the pointer feels slow on the Chromebook."),
	))
}

func (a *App) buildSwitchCard() fyne.CanvasObject {
	a.pushValue = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
	a.pushSlider = widget.NewSlider(5, 200)
	a.pushSlider.Step = 5
	a.pushSlider.OnChanged = func(v float64) { a.pushValue.SetText(fmt.Sprintf("%d px", int(v))) }
	a.pushSlider.OnChangeEnded = func(v float64) {
		c := a.cfg
		c.EdgePush = v
		a.apply(c)
	}

	a.hotkeyEntry = widget.NewEntry()
	a.hotkeyEntry.SetPlaceHolder("Ctrl+Alt+K")
	a.hotkeyEntry.OnSubmitted = func(string) { a.commitHotkey() }

	tip := widget.NewLabel("Switching waits while you hold a mouse button or a key, so drags and shortcuts never get cut in half.")
	tip.Wrapping = fyne.TextWrapWord
	tip.Importance = widget.LowImportance
	tip.SizeName = theme.SizeNameCaptionText

	return newCard("Switching", "", container.NewVBox(
		field("Edge resistance", container.NewBorder(nil, nil, nil, fixedWidth(a.pushValue, 56), a.pushSlider), "How far to keep pushing past the edge before control moves over."),
		field("Hotkey", a.hotkeyEntry, "Switches from either computer, and always brings you back. Press Enter to save."),
		tip,
	))
}

func (a *App) buildDevicesCard() fyne.CanvasObject {
	a.deviceBox = container.NewVBox(widget.NewLabel("Looking for keyboards and mice…"))
	return newCard("Devices", "Switched-on devices control the Chromebook while it has control. Turn one off to keep it on this computer only.", a.deviceBox)
}

func (a *App) buildFooter() fyne.CanvasObject {
	path := a.cfgPath
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
		path = "~" + path[len(home):]
	}
	l := widget.NewLabel(fmt.Sprintf("GhostKVM %s  ·  %s  ·  Close the window to keep running in the tray", version.Version, path))
	l.Importance = widget.LowImportance
	l.SizeName = theme.SizeNameCaptionText
	l.Alignment = fyne.TextAlignCenter
	l.Truncation = fyne.TextTruncateEllipsis
	check := widget.NewButton("Check for updates", func() { go a.checkForUpdates(true) })
	check.Importance = widget.LowImportance
	return container.NewVBox(l, container.NewCenter(check))
}

// minHeight keeps a child at least h tall.
type minHeight struct{ h float32 }

func (l *minHeight) Layout(objs []fyne.CanvasObject, s fyne.Size) {
	for _, o := range objs {
		o.Resize(s)
	}
}

func (l *minHeight) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := fyne.NewSize(0, l.h)
	for _, o := range objs {
		m = m.Max(o.MinSize())
	}
	return m
}

func (a *App) fillSettings() {
	a.filling = true
	defer func() { a.filling = false }()
	c := a.cfg
	a.nameEntry.SetText(c.Peer.Name)
	res := fmt.Sprintf("%d × %d", int(c.Peer.Width), int(c.Peer.Height))
	if slices.Contains(resolutions, res) {
		a.resSelect.SetSelected(res)
		a.customRes.Hide()
	} else {
		a.resSelect.SetSelected("Custom…")
		a.widthEntry.SetText(strconv.Itoa(int(c.Peer.Width)))
		a.heightEntry.SetText(strconv.Itoa(int(c.Peer.Height)))
		a.customRes.Show()
	}
	a.speedSlider.SetValue(c.Peer.Speed)
	a.speedValue.SetText(fmt.Sprintf("%.1f×", c.Peer.Speed))
	a.pushSlider.SetValue(c.EdgePush)
	a.pushValue.SetText(fmt.Sprintf("%d px", int(c.EdgePush)))
	a.hotkeyEntry.SetText(c.Hotkey)
}

func (a *App) setupTray() {
	a.trayStatus = fyne.NewMenuItem("Starting…", nil)
	a.trayStatus.Disabled = true
	a.traySwitch = fyne.NewMenuItem("Switch", a.toggle)
	open := fyne.NewMenuItem("Open GhostKVM", func() { a.win.Show(); a.win.RequestFocus() })
	a.trayUpdate = fyne.NewMenuItem("Check for updates…", func() {
		if a.pendingUpdate != nil {
			a.showUpdateDialog()
		} else {
			go a.checkForUpdates(true)
		}
	})
	a.trayMenu = fyne.NewMenu("GhostKVM", a.trayStatus, a.traySwitch, fyne.NewMenuItemSeparator(), open, a.trayUpdate)
	a.desk.SetSystemTrayMenu(a.trayMenu)
	a.desk.SetSystemTrayIcon(iconDisconnected)
	a.lastIcon = iconDisconnected
}

// ---------------------------------------------------------------- actions

func (a *App) apply(c config.Config) {
	// Controls fire their change handlers when fillSettings sets them; those
	// are not user edits. Previews have no KVM behind them.
	if a.filling || a.k == nil {
		return
	}
	go func() {
		err := a.k.Apply(c)
		var st kvm.Status
		if err == nil {
			st, err = a.k.Status()
		}
		if err == nil {
			err = config.Save(a.cfgPath, &st.Config)
		}
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, a.win)
				a.fillSettings() // put the controls back to what is applied
				return
			}
			a.cfg = st.Config
			a.update(st)
			a.savedAt = time.Now()
			a.savedNote.SetText("✓ Saved")
		})
	}()
}

func (a *App) commitName() {
	c := a.cfg
	c.Peer.Name = strings.TrimSpace(a.nameEntry.Text)
	if c.Peer.Name != a.cfg.Peer.Name {
		a.apply(c)
	}
}

func (a *App) commitHotkey() {
	c := a.cfg
	c.Hotkey = strings.TrimSpace(a.hotkeyEntry.Text)
	if c.Hotkey != a.cfg.Hotkey {
		a.apply(c)
	}
}

func (a *App) commitCustomRes() {
	w, err1 := strconv.Atoi(strings.TrimSpace(a.widthEntry.Text))
	h, err2 := strconv.Atoi(strings.TrimSpace(a.heightEntry.Text))
	if err1 != nil || err2 != nil {
		dialog.ShowError(fmt.Errorf("resolution must be two whole numbers, like 1920 and 1080"), a.win)
		return
	}
	c := a.cfg
	c.Peer.Width, c.Peer.Height = float64(w), float64(h)
	a.apply(c)
}

func (a *App) toggle() {
	go func() {
		err := a.k.Toggle()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, a.win)
			}
		})
	}()
}

func (a *App) setDeviceUsed(name string, used bool) {
	c := a.cfg
	c.Ignore = slices.DeleteFunc(slices.Clone(c.Ignore), func(s string) bool { return s == name })
	if !used {
		c.Ignore = append(c.Ignore, name)
	}
	a.apply(c)
}

// ---------------------------------------------------------------- status

func (a *App) poll(stop chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		st, err := a.k.Status()
		if err == nil {
			fyne.Do(func() { a.update(st) })
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func sideWord(side string) string {
	switch side {
	case "left":
		return "left of"
	case "right":
		return "right of"
	case "top":
		return "above"
	}
	return "below"
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (a *App) update(st kvm.Status) {
	p := pal()
	peer := st.Config.Peer.Name
	peerTitle := titleCase(peer)

	icon := iconDisconnected
	if st.Connected {
		a.linkPill.set(peerTitle+" connected", p.accent)
		icon = iconConnected
	} else {
		a.linkPill.set("Waiting for "+peer, p.muted)
	}
	if st.Mode == "peer" {
		a.modePill.set("Control: "+peer, p.peer)
		a.switchBtn.SetText("Back to this computer")
		icon = iconPeerActive
	} else {
		a.modePill.set("Control: this computer", p.muted)
		a.switchBtn.SetText("Switch to " + peer)
	}
	if st.Connected {
		a.switchBtn.Enable()
	} else {
		a.switchBtn.Disable()
	}
	if !a.savedAt.IsZero() && time.Since(a.savedAt) > 2*time.Second {
		a.savedNote.SetText("")
		a.savedAt = time.Time{}
	}

	a.editor.set(st.Monitors, st.Config.Peer.Side, st.Config.Peer.Monitor,
		st.Config.Peer.Width, st.Config.Peer.Height, peer, st.Mode == "peer")
	summary := fmt.Sprintf("%s is %s %s. Move the pointer off the %s edge to switch, or press %s.",
		peerTitle, sideWord(st.Config.Peer.Side), st.Config.Peer.Monitor, st.Config.Peer.Side, st.Config.Hotkey)
	if !st.EdgeSwitching {
		back := map[string]string{"left": "right", "right": "left", "top": "bottom", "bottom": "top"}[st.Config.Peer.Side]
		summary = fmt.Sprintf("%s doesn't let apps follow the pointer, so switch to the %s with %s or the tray. To come back, push past the %s's %s edge or press %s again.",
			st.Desktop, peer, st.Config.Hotkey, peer, back, st.Config.Hotkey)
	}
	if !st.Connected {
		summary = fmt.Sprintf("Waiting for %s. On it, open Settings › Bluetooth and pair “%s”.", peer, st.Config.BluetoothName)
	}
	if a.summary.Text != summary {
		a.summary.SetText(summary)
	}
	a.updateDevices(st.Devices)

	if a.desk != nil {
		status := "Waiting for " + peer
		if st.Connected {
			status = peerTitle + " connected"
		}
		a.trayStatus.Label = status
		a.traySwitch.Label = a.switchBtn.Text
		a.traySwitch.Disabled = !st.Connected
		a.trayMenu.Refresh()
		if icon != a.lastIcon {
			a.desk.SetSystemTrayIcon(icon)
			a.lastIcon = icon
		}
	}
}

func (a *App) updateDevices(devs []kvm.DeviceInfo) {
	var sig strings.Builder
	for _, d := range devs {
		fmt.Fprintf(&sig, "%s|%s|%v;", d.Path, d.Name, d.Used)
	}
	if sig.String() == a.deviceSig {
		return
	}
	a.deviceSig = sig.String()

	// One row per device name: a mouse often appears as several nodes.
	type row struct {
		name, kind string
		used       bool
	}
	var rows []row
	seen := map[string]int{}
	for _, d := range devs {
		if i, ok := seen[d.Name]; ok {
			rows[i].used = rows[i].used || d.Used
			if d.Kind == "mouse" {
				rows[i].kind = "mouse"
			}
			continue
		}
		seen[d.Name] = len(rows)
		rows = append(rows, row{d.Name, d.Kind, d.Used})
	}
	objs := make([]fyne.CanvasObject, 0, len(rows)*2)
	for i, r := range rows {
		name := r.name
		kind := "Keyboard"
		if r.kind == "mouse" {
			kind = "Mouse"
		}
		title := widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		title.Truncation = fyne.TextTruncateEllipsis
		sub := widget.NewLabel(kind)
		sub.Importance = widget.LowImportance
		sub.SizeName = theme.SizeNameCaptionText
		chk := widget.NewCheck("", nil)
		chk.SetChecked(r.used)
		chk.OnChanged = func(on bool) { a.setDeviceUsed(name, on) }
		rowObj := container.NewBorder(nil, nil, nil, container.NewCenter(chk),
			container.New(&tightVBox{gap: -10}, title, sub))
		if i > 0 {
			objs = append(objs, widget.NewSeparator())
		}
		objs = append(objs, rowObj)
	}
	if len(objs) == 0 {
		objs = append(objs, widget.NewLabel("No keyboards or mice found."))
	}
	a.deviceBox.Objects = objs
	a.deviceBox.Refresh()
}

// ---------------------------------------------------------------- helpers

func parseRes(s string) (int, int, bool) {
	parts := strings.Split(s, "×")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	return w, h, err1 == nil && err2 == nil
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
