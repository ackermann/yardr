// Package scripttest writes the stand-in scripts of tests: a fake herdr, a
// provider, a harness, a git or gh first on PATH.
//
// A stand-in is not a new executable file. On macOS the first exec of an
// executable file that was not there before takes hundreds of milliseconds,
// and seconds beside a -race suite, where every later exec of it takes a few
// (13 Oct 2026, load 29: a median of 1310 ms for a new script each, 6 ms for
// a new symlink each to a script that had run; yardr-2h90). So a test binary
// has one launcher (one more for bash, where a pack's script is in that), run
// once, and each stand-in is a symlink to it with the script's body beside
// the link, in <path>.sh.
package scripttest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// launcher runs the body beside the link it was started through: $0 is the
// link's path, so <dir>/herdr runs <dir>/herdr.sh, with the arguments, the
// standard streams and the $0 a script at <dir>/herdr would have had. There
// is one for each shell a body is written for.
const launcher = "#!%s\n. \"$0.sh\"\n"

var (
	mu        sync.Mutex
	dir       string                // holds the launchers
	launchers = map[string]string{} // by shell: /bin/sh, /bin/bash
)

// launcherPath writes the test binary's launcher for shell on first use and
// runs it once, so that the slow first exec is paid here and not inside a
// test's deadline.
func launcherPath(shell string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if p := launchers[shell]; p != "" {
		return p, nil
	}
	if dir == "" {
		d, err := os.MkdirTemp("", "yardr-scripttest-")
		if err != nil {
			return "", err
		}
		dir = d
	}
	p := filepath.Join(dir, filepath.Base(shell))
	if err := os.WriteFile(p, fmt.Appendf(nil, launcher, shell), 0o755); err != nil {
		return "", err
	}
	first := p + "-first"
	if err := link(p, first, ":\n"); err != nil {
		return "", err
	}
	if out, err := exec.Command(first).CombinedOutput(); err != nil {
		return "", fmt.Errorf("scripttest: the first run of the launcher for %s: %v: %s", shell, err, out)
	}
	launchers[shell] = p
	return p, nil
}

func link(to, path, body string) error {
	if err := os.WriteFile(path+".sh", []byte(body), 0o600); err != nil {
		return err
	}
	if got, err := os.Readlink(path); err == nil && got == to {
		return nil // written before: only its body is new
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(to, path)
}

// Put makes path a stand-in that runs body, a script for sh: a "#!/bin/sh"
// line at its head is a comment there. The body is in path+".sh". Put on a
// path that is a stand-in already gives it a new body.
func Put(path, body string) error {
	return put("/bin/sh", path, body)
}

func put(shell, path, body string) error {
	to, err := launcherPath(shell)
	if err != nil {
		return err
	}
	return link(to, path, body)
}

// Write is Put for a test, which fails where the stand-in cannot be written.
func Write(t testing.TB, path, body string) {
	t.Helper()
	if err := Put(path, body); err != nil {
		t.Fatal(err)
	}
}

// Relink makes a stand-in of every script under dir that is an executable
// file of its own, with the file's text for its body. It is for the scripts
// yardr itself wrote there from what a test gave it (a declared source's
// commands under <home>/sources/<name>, which source.CopyCommandFiles
// copies): each copy is a new file, and slow to start as one. A file whose
// first line is neither "#!/bin/sh" nor "#!/bin/bash" is left as it is.
func Relink(t testing.TB, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, shell := range []string{"/bin/sh", "/bin/bash"} {
			if bytes.HasPrefix(body, []byte("#!"+shell+"\n")) {
				return put(shell, path, string(body))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Remove removes the launchers: a package's TestMain calls it after m.Run.
// The stand-ins are in their tests' own directories and go with those.
func Remove() {
	if dir != "" {
		os.RemoveAll(dir)
	}
}
