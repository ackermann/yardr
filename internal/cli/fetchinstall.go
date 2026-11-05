package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
)

// The onboarding's offer to install a program that is not on PATH, from the
// file yardr pins for it (store.Fetch): the steps of docs/doctor.md, "A
// program fetched on a Mac", done by yardr itself, on a yes, at a terminal.
// herdr's own installer (https://herdr.dev/install.sh) is never run, nor any
// script: only the pinned file, once its size and sha256 are those yardr
// pinned, and then only with its version flag.

// fetchHTTP downloads a pinned file. A redirect is followed, since herdr's
// release address answers with one to its host's storage, but only to https,
// and the file is kept only if it is the size and the sha256 yardr pinned,
// whichever host sent it. The bound is for a host that hangs, sized for the
// largest of the files on a slow link.
var fetchHTTP = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		switch {
		case req.URL.Scheme != "https":
			return errors.New("it was sent on to an address that is not https")
		case len(via) > 5:
			return errors.New("it was sent on more than 5 times")
		}
		return nil
	},
}

// offerInstall is the onboarding's offer to a person at a terminal to install
// the program n names from its pinned download, and what it does with a yes.
// It reports whether the program is in place when it returns: installed, or
// there already at the place the download would put it, with the directory on
// this process's PATH, so that the rest of the run, and the daemon it starts,
// find the program. False is no offer made (no terminal, nothing pinned for
// this system, something else at the place), an offer refused, or an install
// that failed, which has said why in one line. Its error is the person
// leaving: the form left, or ctrl+c while the file came down.
//
// A variable so that no test downloads anything: a test of the offer puts the
// real one back (realOfferInstall).
var offerInstall = func(ctx context.Context, w io.Writer, n need) (bool, error) {
	d := n.download
	if d == nil || !interactive() {
		return false, nil
	}
	dir := filepath.Dir(d.Path)
	// A file there already is the program, installed and not on PATH
	// (docs/doctor.md, step 1): the person ran herdr's installer, which puts
	// it here, in a terminal that does not have the directory. Fetching over
	// it could put an older one over a newer, which `herdr update` kept.
	if fi, err := os.Lstat(d.Path); err == nil {
		if !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			return false, nil
		}
		fmt.Fprintf(w, "%s is at %s already.\n", n.Program, tilde(d.Path))
		sayPath(w, dir)
		return true, nil
	}
	size := fmt.Sprintf("%.1f MB", float64(d.Size)/1e6)
	yes := true
	if err := ask(huh.NewConfirm().
		Title(fmt.Sprintf("Install %s %s into %s now?", n.Program, d.Version, tilde(dir))).
		Description(fmt.Sprintf("Downloads %s (%s), checks it against the size and sha256 yardr pins, and runs %s --version before it keeps it. No script is run and no shell profile is changed.",
			d.URL, size, n.Program)).
		Value(&yes)); err != nil {
		return false, err
	}
	if !yes {
		return false, nil
	}
	fmt.Fprintf(w, "Downloading %s %s (%s)…\n", n.Program, d.Version, size)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	says, err := installDownload(ctx, fetchHTTP, d)
	if ctx.Err() != nil {
		return false, huh.ErrUserAborted
	}
	if err != nil {
		fmt.Fprintf(w, "%s was not installed: %v; nothing of it was kept.\n", n.Program, err)
		return false, nil
	}
	fmt.Fprintf(w, "Installed %s %s at %s: it says %q.\n", n.Program, d.Version, tilde(d.Path), says)
	sayPath(w, dir)
	return true, nil
}

// sayPath puts dir on this process's PATH and, where it was not on it, says
// so once, with the line a new terminal needs (the one install.sh prints):
// yardr writes no shell profile.
func sayPath(w io.Writer, dir string) {
	if !pathAdd(dir) {
		return
	}
	fmt.Fprintf(w, "%s is not on PATH here, so yardr looks in it for this run. A new terminal needs this line in your shell's startup file (~/.zshrc, ~/.bashrc or ~/.profile):\n\n    export PATH=\"%s:$PATH\"\n\n",
		tilde(dir), strings.Replace(tilde(dir), "~", "$HOME", 1))
}

// pathAdd puts dir on this process's PATH after the directories it has, as
// herdrProgram does for what the login shell finds, and reports whether it
// was not there. What the process starts from then on, the daemon included
// (app.environ), finds a program in it.
func pathAdd(dir string) bool {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	if slices.Contains(dirs, dir) {
		return false
	}
	os.Setenv("PATH", strings.Join(append(dirs, dir), string(os.PathListSeparator)))
	return true
}

// installDownload puts the file d pins at d.Path and returns the first line
// its version flag printed. It follows docs/doctor.md, "A program fetched on a
// Mac": the file is downloaded and kept in memory, its size and sha256 are
// compared with the pinned ones, and only a file that matches is written, to
// a temporary file beside d.Path so that it is run where it will be kept
// (a temporary directory can be mounted noexec) and moved without leaving the
// file system. A failure anywhere leaves nothing of the file behind, and
// nothing at all before the download has proved to be the pinned one; a
// directory made for it may stay. A file at d.Path is never replaced, not one
// that appears while this downloads.
//
// It takes a download that is the program, as herdr's is; one that is a member
// of an archive, or is run to put the program in place, is for a program that
// fetches those steps.
func installDownload(ctx context.Context, hc *http.Client, d *store.Download) (string, error) {
	if d.Member != "" || len(d.Run) > 0 {
		return "", errors.New("the pinned file is not the program itself, and only that is installed here")
	}
	if err := requireAbsent(d.Path); err != nil {
		return "", err
	}
	file, err := downloadPinned(ctx, hc, d)
	if err != nil {
		return "", err
	}
	return placeProgram(ctx, d.Path, file)
}

// requireAbsent is an error when something is at path, which nothing here
// replaces: a link, even a dangling one, is there.
func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s is there already, and is not replaced", path)
	}
	return nil
}

// downloadPinned is the file at d.URL, once it is the size and the sha256 d
// pins. No more than that size is read, whatever is sent.
func downloadPinned(ctx context.Context, hc *http.Client, d *store.Download) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the download failed: %w", cause(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the download was answered with HTTP %d", resp.StatusCode)
	}
	file, err := io.ReadAll(io.LimitReader(resp.Body, d.Size+1))
	if err != nil {
		return nil, fmt.Errorf("the download failed: %w", cause(err))
	}
	switch got := int64(len(file)); {
	case got > d.Size:
		return nil, fmt.Errorf("the download is larger than the %d bytes yardr pins", d.Size)
	case got < d.Size:
		return nil, fmt.Errorf("the download is %d bytes, not the %d yardr pins", got, d.Size)
	}
	sum := sha256.Sum256(file)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, d.SHA256) {
		return nil, fmt.Errorf("the download's sha256 (%.12s…) is not the one yardr pins (%.12s…)", got, d.SHA256)
	}
	return file, nil
}

// cause is err without the address a failed request names: a redirect to a
// release host's storage carries a signed token in its address, and a line for
// a person is no place for one.
func cause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// placeProgram writes file at path, mode 0755, through a temporary file in its
// directory (made if there is none): the file is run with --version there
// first, and moved over only if it answers, so that a program that does not
// run here replaces nothing. The temporary file is removed on every path out.
func placeProgram(ctx context.Context, path string, file []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("the program cannot be written: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".fetch-*")
	if err != nil {
		return "", fmt.Errorf("the program cannot be written: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(file)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return "", fmt.Errorf("the program cannot be written: %w", err)
	}
	says, err := versionOf(ctx, tmp.Name())
	if err != nil {
		return "", err
	}
	if err := requireAbsent(path); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("the program cannot be put in place: %w", err)
	}
	return says, nil
}

// versionOf runs the program at path with --version, as the harnesses'
// version flags are run (probe), and returns the first line it printed. The
// file is a program that answers: one that fails, hangs or prints nothing is
// none that can be kept. A ctx that ended is its error, for the caller to
// tell from a program that failed.
func versionOf(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	ownGroup(cmd)
	out, err := cmd.CombinedOutput()
	line := versionLine(string(out))
	switch cerr := ctx.Err(); {
	case errors.Is(cerr, context.DeadlineExceeded):
		return "", fmt.Errorf("the file did not answer --version within %s", versionTimeout)
	case cerr != nil:
		return "", cerr
	case err != nil:
		return "", fmt.Errorf("the file does not run here (%s)", strings.TrimSuffix(strings.TrimSpace(err.Error()+": "+line), ":"))
	case line == "":
		return "", errors.New("the file ran but printed no version")
	}
	return line, nil
}
