package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind is how this copy of GhostKVM was installed, which decides the update
// bundle to download and how it is put in place.
type Kind string

const (
	KindTarball Kind = "tar"     // binary from the .tar.xz, make install-local or go install
	KindManaged Kind = "managed" // deb/rpm/pacman or not writable: notify only
)

// Install describes the running installation.
type Install struct {
	Kind Kind
	// Path is the file that gets replaced.
	Path string
	// CanSelfUpdate is false when the update has to go through a package
	// manager or needs rights the user doesn't have; Reason says why.
	CanSelfUpdate bool
	Reason        string
}

// PlatformKey is the manifest key for this install, e.g. "linux-x86_64-tar".
func (i Install) PlatformKey() string {
	return runtime.GOOS + "-" + Arch() + "-" + string(i.Kind)
}

// DetectInstall inspects the running executable.
func DetectInstall() Install {
	exe, err := os.Executable()
	if err != nil {
		return Install{Kind: KindManaged, Reason: "cannot locate the running executable"}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// Files owned by the system package manager must be updated through it.
	for _, dir := range []string{"/usr/bin/", "/usr/sbin/", "/bin/", "/opt/"} {
		if strings.HasPrefix(exe, dir) {
			return Install{Kind: KindManaged, Path: exe, Reason: "your system package manager"}
		}
	}
	return writable(Install{Kind: KindTarball, Path: exe})
}

// writable marks the install self-updatable when the user can replace it,
// which needs write access to the directory holding it.
func writable(i Install) Install {
	dir := filepath.Dir(i.Path)
	f, err := os.CreateTemp(dir, ".ghostkvm-update-check-*")
	if err != nil {
		i.Kind = KindManaged
		i.Reason = "an administrator (no permission to write to " + dir + ")"
		return i
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	i.CanSelfUpdate = true
	return i
}
