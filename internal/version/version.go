// Package version names the build a yardr process runs, and notices when
// the binary it was started from has been replaced on disk (a `go install`),
// so the long-lived server and daemon can exec the new build themselves.
package version

import (
	"debug/buildinfo"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Header carries a process's revision on every rpc request and reply.
const Header = "X-Yardr-Revision"

// Current is the revision this process was built from, or "" when the build
// carries no VCS stamp (go run, test binaries).
func Current() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return revision(bi.Settings)
}

// OfFile is the revision of the binary at path.
func OfFile(path string) (string, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	return revision(bi.Settings), nil
}

func revision(settings []debug.BuildSetting) string {
	var rev, modified string
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-modified"
	}
	return rev
}

// Show is rev for people: "unknown" when there is none.
func Show(rev string) string {
	if rev == "" {
		return "unknown"
	}
	return rev
}

// Binary is the executable a process was started from, as it was then.
type Binary struct {
	Path    string
	Running string // the revision this process runs
	// Exec replaces the process; syscall.Exec unless a test fakes it. A fake
	// returns nil to stand for "the new build now runs".
	Exec func(path string, argv, env []string) error
	// ReadRevision reads the revision of the binary at a path (OfFile).
	ReadRevision func(path string) (string, error)

	mu     sync.Mutex
	start  fileID
	failed fileID // a replacement that could not be exec'd; not retried
}

type fileID struct {
	dev, ino uint64
	size     int64
	mtime    time.Time
}

func stat(path string) (fileID, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileID{}, err
	}
	id := fileID{size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id.dev, id.ino = uint64(st.Dev), st.Ino
	}
	return id, nil
}

// Self records this process's executable as it is now.
func Self() (*Binary, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return Watch(path, Current())
}

// Watch records the binary at path, which runs revision running.
func Watch(path, running string) (*Binary, error) {
	id, err := stat(path)
	if err != nil {
		return nil, err
	}
	return &Binary{Path: path, Running: running, Exec: syscall.Exec, ReadRevision: OfFile, start: id}, nil
}

// Replacement reports the revision of a different build now installed at the
// binary's path. Only a file that changed since start counts: a client that
// is merely older than this process never makes it exec anything (no
// downgrade). A changed file with the same clean revision is a reinstall of
// this build, not a replacement; a "-modified" build cannot be told apart
// from another one, so any change to it counts.
func (b *Binary) Replacement() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, err := stat(b.Path)
	if err != nil || id == b.start || id == b.failed {
		return "", false
	}
	// A binary still being written does not parse yet: its ELF section
	// headers come last.
	rev, err := b.ReadRevision(b.Path)
	if err != nil {
		return "", false
	}
	if rev == b.Running && !strings.HasSuffix(rev, "-modified") {
		return "", false
	}
	return rev, true
}

// Replace execs the binary at Path with this process's arguments and
// environment plus env (KEY=value, overriding), keeping the pid. It returns
// only if the exec failed (or a fake Exec succeeded); a failed replacement
// is not offered again until the file changes once more.
func (b *Binary) Replace(env ...string) error {
	id, _ := stat(b.Path)
	err := b.Exec(b.Path, os.Args, mergeEnv(os.Environ(), env))
	if err != nil {
		b.mu.Lock()
		b.failed = id
		b.mu.Unlock()
	}
	return err
}

// mergeEnv returns base with extra's keys replaced, since getenv takes the
// first of duplicate keys.
func mergeEnv(base, extra []string) []string {
	keys := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		keys[k] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !keys[k] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
