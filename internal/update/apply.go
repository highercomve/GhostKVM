package update

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aead.dev/minisign"
	"github.com/ulikunitz/xz"
)

// ProgressFunc receives download progress; total is -1 when unknown.
type ProgressFunc func(done, total int64)

// Apply downloads the release bundle, verifies its signature and replaces the
// running installation. It does not restart the app; call Relaunch for that.
func Apply(ctx context.Context, rel *Release, progress ProgressFunc) error {
	if rel == nil || !rel.Install.CanSelfUpdate {
		return errors.New("this installation cannot update itself")
	}
	pub, err := publicKey()
	if err != nil {
		return err
	}
	sig, err := decodeSignature(rel.Asset.Signature)
	if err != nil {
		return err
	}

	// Download next to the target so the final rename stays on one filesystem.
	dir := filepath.Dir(rel.Install.Path)
	tmp, err := os.CreateTemp(dir, ".ghostkvm-update-*")
	if err != nil {
		return fmt.Errorf("preparing download: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	verifier, err := download(ctx, rel.Asset.URL, tmp, progress)
	tmp.Close()
	if err != nil {
		return err
	}
	if !verifier.Verify(pub, sig) {
		return errors.New("the update's signature is not valid; it was not installed")
	}
	if err := checkTrustedComment(sig, path.Base(rel.Asset.URL), rel.Version); err != nil {
		return err
	}

	if rel.Install.Kind == KindTarball {
		return extractAndReplace(tmpName, rel.Install.Path, extractFromTarXz)
	}
	return fmt.Errorf("unsupported install kind %q", rel.Install.Kind)
}

func download(ctx context.Context, url string, w io.Writer, progress ProgressFunc) (*minisign.Reader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{} // no overall timeout: bundles are large; ctx cancels
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading update: server returned %s", resp.Status)
	}

	// The minisign reader hashes the stream so the signature is checked
	// without holding the bundle in memory.
	verifier := minisign.NewReader(resp.Body)
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := verifier.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return nil, fmt.Errorf("saving update: %w", err)
			}
			done += int64(n)
			if progress != nil {
				progress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			return verifier, nil
		}
		if rerr != nil {
			return nil, fmt.Errorf("downloading update: %w", rerr)
		}
	}
}

// checkTrustedComment makes sure the signature was made for this exact file
// and version, so an old signed bundle can't be replayed as an update.
func checkTrustedComment(sig []byte, file, version string) error {
	var s minisign.Signature
	if err := s.UnmarshalText(sig); err != nil {
		return fmt.Errorf("reading signature: %w", err)
	}
	fields := map[string]string{}
	for _, f := range strings.Fields(s.TrustedComment) {
		if k, v, ok := strings.Cut(f, ":"); ok {
			fields[k] = v
		}
	}
	if fields["file"] != file || strings.TrimPrefix(fields["version"], "v") != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("the update's signature is for %s %s, not %s %s; it was not installed",
			fields["file"], fields["version"], file, version)
	}
	return nil
}

// replaceFile atomically puts src in place of dst, keeping it executable.
func replaceFile(src, dst string) error {
	if err := os.Chmod(src, 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("replacing %s: %w", dst, err)
	}
	return nil
}

type extractFunc func(archive string, w io.Writer) error

func extractAndReplace(archive, dst string, extract extractFunc) error {
	out, err := os.CreateTemp(filepath.Dir(dst), ".ghostkvm-update-bin-*")
	if err != nil {
		return err
	}
	name := out.Name()
	defer os.Remove(name)
	err = extract(archive, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, dst)
}

// extractFromTarXz copies the ghostkvm binary out of the release .tar.xz.
func extractFromTarXz(archive string, w io.Writer) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading update archive: %w", err)
	}
	tr := tar.NewReader(xr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("update archive has no ghostkvm binary")
		}
		if err != nil {
			return fmt.Errorf("reading update archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == "ghostkvm" && path.Base(path.Dir(h.Name)) == "bin" {
			_, err = io.Copy(w, tr)
			return err
		}
	}
}

// WaitPIDEnv tells a relaunched GhostKVM which process to wait for: the old
// version must release Bluetooth and the control socket first.
const WaitPIDEnv = "GHOSTKVM_WAIT_PID"

// Relaunch starts the freshly installed version, which waits for this
// process to exit. The caller should quit right after it returns.
func Relaunch(inst Install) error {
	cmd := exec.Command(inst.Path, os.Args[1:]...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", WaitPIDEnv, os.Getpid()))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	markInheritedFDsCloseOnExec()
	return cmd.Start()
}

// WaitForPrevious blocks (up to timeout) while the process named by
// GHOSTKVM_WAIT_PID is still running.
func WaitForPrevious(timeout time.Duration) {
	pid, err := strconv.Atoi(os.Getenv(WaitPIDEnv))
	os.Unsetenv(WaitPIDEnv)
	if err != nil || pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil { // gone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
