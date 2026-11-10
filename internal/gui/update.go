package gui

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/highercomve/GhostKVM/internal/update"
	"github.com/highercomve/GhostKVM/internal/version"
)

// updateCheckInterval limits automatic checks to one a day.
const updateCheckInterval = 24 * time.Hour

const releasesPage = "https://github.com/highercomve/GhostKVM/releases/latest"

// updateState is kept apart from config.toml, which is the user's file.
type updateState struct {
	LastCheck      time.Time `json:"last_check"`
	SkippedVersion string    `json:"skipped_version,omitempty"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ghostkvm", "update-state.json")
}

func loadUpdateState() updateState {
	var s updateState
	if data, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func saveUpdateState(s updateState) {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.MkdirAll(filepath.Dir(statePath()), 0o755)
	os.WriteFile(statePath(), data, 0o644)
}

// maybeCheckForUpdates runs the automatic check at startup, at most daily,
// and only for release builds.
func (a *App) maybeCheckForUpdates() {
	if !update.IsReleaseVersion(version.Version) {
		return
	}
	if time.Since(loadUpdateState().LastCheck) < updateCheckInterval {
		return
	}
	go a.checkForUpdates(false)
}

// checkForUpdates queries the manifest. A manual check reports every
// outcome; the automatic one only offers an update the user hasn't skipped.
func (a *App) checkForUpdates(manual bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := update.Check(ctx, version.Version)
	if err == nil {
		s := loadUpdateState()
		s.LastCheck = time.Now()
		saveUpdateState(s)
	}
	fyne.Do(func() {
		switch {
		case err != nil:
			if manual {
				dialog.ShowError(err, a.win)
			}
		case st.DevBuild:
			if manual {
				dialog.ShowInformation("Development build",
					"This is a development build ("+version.Version+"), so it doesn't update itself. The latest release is v"+st.Latest+".", a.win)
			}
		case st.Update == nil:
			a.setPendingUpdate(nil)
			if manual {
				dialog.ShowInformation("Up to date", "GhostKVM "+version.Version+" is the latest version.", a.win)
			}
		default:
			a.setPendingUpdate(st.Update)
			if manual || loadUpdateState().SkippedVersion != st.Update.Version {
				a.showUpdateDialog()
			}
		}
	})
}

// setPendingUpdate shows or hides the header button and tray entry.
func (a *App) setPendingUpdate(rel *update.Release) {
	a.pendingUpdate = rel
	if rel == nil {
		a.updateBtn.Hide()
	} else {
		a.updateBtn.SetText("Update to v" + rel.Version)
		a.updateBtn.Show()
	}
	if a.trayUpdate != nil {
		a.trayUpdate.Label = "Check for updates…"
		if rel != nil {
			a.trayUpdate.Label = "Update to v" + rel.Version + "…"
		}
		a.trayMenu.Refresh()
	}
}

// showUpdateDialog offers the pending update: install and restart when this
// copy can replace itself, otherwise point at the release page.
func (a *App) showUpdateDialog() {
	rel := a.pendingUpdate
	if rel == nil {
		return
	}
	a.win.Show()

	title := widget.NewLabelWithStyle("GhostKVM v"+rel.Version+" is available", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameSubHeadingText
	current := widget.NewLabel("You have " + version.Version + ".")
	current.Importance = widget.LowImportance
	notes := widget.NewLabel(rel.Notes)
	if rel.Notes == "" {
		notes.SetText("See the release page for what's new.")
	}
	notes.Wrapping = fyne.TextWrapWord
	notesScroll := container.NewVScroll(notes)
	notesScroll.SetMinSize(fyne.NewSize(460, 160))

	progress := widget.NewProgressBar()
	progress.Hide()
	status := widget.NewLabel("")
	status.Importance = widget.LowImportance
	status.Wrapping = fyne.TextWrapWord

	var d *dialog.CustomDialog
	later := widget.NewButton("Later", func() { d.Hide() })
	skip := widget.NewButton("Skip this version", func() {
		s := loadUpdateState()
		s.SkippedVersion = rel.Version
		saveUpdateState(s)
		d.Hide()
	})
	var primary *widget.Button
	if rel.Install.CanSelfUpdate {
		primary = widget.NewButton("Update and restart", nil)
		primary.OnTapped = func() {
			primary.Disable()
			later.Disable()
			skip.Disable()
			progress.Show()
			status.SetText("Downloading and checking the signature…")
			go a.installUpdate(rel, progress, status, func() {
				primary.Enable()
				later.Enable()
				skip.Enable()
			})
		}
	} else {
		status.SetText("This copy is updated by " + rel.Install.Reason + ".")
		primary = widget.NewButton("Open release page", func() {
			if u, err := url.Parse(releasesPage); err == nil {
				a.fa.OpenURL(u)
			}
			d.Hide()
		})
	}
	primary.Importance = widget.HighImportance

	body := container.NewVBox(title, current, widget.NewSeparator(), notesScroll, progress, status)
	buttons := container.NewHBox(skip, layout.NewSpacer(), later, primary)
	d = dialog.NewCustomWithoutButtons("Update available", container.NewBorder(nil, buttons, nil, nil, body), a.win)
	d.Show()
}

// installUpdate downloads, verifies and installs the update, then starts the
// new version and quits; the new one waits for this process to exit before
// it takes over Bluetooth and the control socket.
func (a *App) installUpdate(rel *update.Release, progress *widget.ProgressBar, status *widget.Label, restore func()) {
	err := update.Apply(context.Background(), rel, func(done, total int64) {
		if total > 0 {
			fyne.Do(func() { progress.SetValue(float64(done) / float64(total)) })
		}
	})
	if err == nil {
		err = update.Relaunch(rel.Install)
	}
	fyne.Do(func() {
		if err != nil {
			progress.Hide()
			status.SetText("")
			restore()
			dialog.ShowError(err, a.win)
			return
		}
		status.SetText("Restarting GhostKVM…")
		a.fa.Quit()
	})
}
