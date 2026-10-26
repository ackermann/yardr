// Package version names the build a yardr process runs, and notices when
// the binary it was started from has been replaced on disk by a newer build
// (a `go install` after a merge), so the long-lived server and daemon can
// exec the new build themselves.
package version

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Header carries a process's revision on every rpc request and reply.
const Header = "X-Yardr-Revision"

// Build is what a binary says about where it came from.
type Build struct {
	// Revision is the 12-character VCS revision ("-modified" when the tree
	// was dirty), else the module version of an @version install, else "".
	Revision string
	// Time is the commit time (vcs.time, or a pseudo-version's timestamp);
	// zero when the build does not say, and the file's mtime stands in.
	Time time.Time
}

// Current is the revision this process was built from, or "" when the build
// carries neither a VCS stamp nor a module version (go run, test binaries).
func Current() string { return CurrentBuild().Revision }

// CurrentBuild is the build this process runs.
func CurrentBuild() Build {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Build{}
	}
	return buildOf(bi)
}

// OfFile is the build of the binary at path.
func OfFile(path string) (Build, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return Build{}, err
	}
	return buildOf(bi), nil
}

// buildOf reads a build's revision and time. `go install pkg@version`
// builds in the module cache, where there is no VCS to stamp, so only
// Main.Version says which build it is; a pseudo-version carries the same
// 12-character revision and the commit time a VCS stamp would.
func buildOf(bi *debug.BuildInfo) Build {
	b := Build{Revision: revision(bi.Settings)}
	for _, s := range bi.Settings {
		if s.Key == "vcs.time" {
			b.Time, _ = time.Parse(time.RFC3339, s.Value)
		}
	}
	if b.Revision != "" {
		return b
	}
	v := bi.Main.Version
	if v == "" || v == "(devel)" {
		return Build{}
	}
	if t, rev, ok := pseudo(v); ok {
		return Build{Revision: rev, Time: t}
	}
	return Build{Revision: v}
}

// pseudo splits a pseudo-version (v0.0.0-20261002150405-abcdef123456, or
// with a pre-release or +incompatible around it) into its commit time and
// revision.
func pseudo(v string) (time.Time, string, bool) {
	v, _, _ = strings.Cut(v, "+")
	i := strings.LastIndexByte(v, '-')
	if i < 0 || len(v)-i-1 != 12 {
		return time.Time{}, "", false
	}
	rev, rest := v[i+1:], v[:i]
	j := strings.LastIndexAny(rest, "-.")
	if j < 0 || len(rest)-j-1 != 14 {
		return time.Time{}, "", false
	}
	t, err := time.Parse("20060102150405", rest[j+1:])
	if err != nil {
		return time.Time{}, "", false
	}
	return t, rev, true
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

// tag is the release this build was made for (v1.2.3). contrib/release/build
// stamps it with -ldflags -X; a `go install` or `go build` leaves it empty.
// It only names the build for people: which build replaces which is decided
// by Revision and Time alone, so a release archive and a `go install` are
// ordered against each other like two installs.
var tag string

// Describe names this build for people: "v1.2.3 (abcdef123456)" for a
// release build, else the revision as the server and daemon log it.
func Describe() string { return describe(tag, Current()) }

func describe(tag, rev string) string {
	switch {
	case tag == "":
		return Show(rev)
	case rev == "":
		return tag
	}
	return tag + " (" + rev + ")"
}

// Tag is the release this build was made for, or "" when it is not a
// release build.
func Tag() string { return tag }

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
	// Built is the commit time of the running build; zero when the build
	// does not say, and the file's mtime at start stands in.
	Built time.Time
	// Exec replaces the process; syscall.Exec unless a test fakes it. A fake
	// returns nil to stand for "the new build now runs".
	Exec func(path string, argv, env []string) error
	// ReadBuild reads the build of the binary at a path (OfFile).
	ReadBuild func(path string) (Build, error)
	// Skipped, if set, hears once about each installed build that is not
	// taken as a replacement, and why, so the refusal is visible.
	Skipped func(rev, reason string)

	mu       sync.Mutex
	start    fileID
	failed   fileID // a replacement that could not be exec'd; not retried
	rejected fileID // a build older than or the same as this one; not re-read
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
	cur := CurrentBuild()
	b, err := Watch(path, cur.Revision)
	if err != nil {
		return nil, err
	}
	b.Built = cur.Time
	return b, nil
}

// Watch records the binary at path, which runs revision running.
func Watch(path, running string) (*Binary, error) {
	id, err := stat(path)
	if err != nil {
		return nil, err
	}
	return &Binary{Path: path, Running: running, Exec: syscall.Exec, ReadBuild: OfFile, start: id}, nil
}

// Replacement reports the revision of a newer build now installed at the
// binary's path. Only a file that changed since start counts: a client that
// is merely older than this process never makes it exec anything. Nor does
// an installed build that is not newer (see Newer): a `go install` of an
// older checkout or of this same commit, dirty or not, is skipped, so a
// downgrade never meets the store's one-way migrations. A build made without
// a VCS stamp carries no revision, and is newer by its file's time alone: its
// revision is then "" with true, so a caller goes by the bool.
func (b *Binary) Replacement() (string, bool) {
	rev, reason, ok := b.replacement()
	if reason != "" && b.Skipped != nil {
		b.Skipped(rev, reason)
	}
	return rev, ok
}

func (b *Binary) replacement() (rev, skipped string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, err := stat(b.Path)
	if err != nil || id == b.start || id == b.failed || id == b.rejected {
		return "", "", false
	}
	// A binary still being written does not parse yet: its ELF section
	// headers come last.
	cand, err := b.ReadBuild(b.Path)
	if err != nil {
		return "", "", false
	}
	running := Build{Revision: b.Running, Time: b.Built}
	if running.Time.IsZero() {
		running.Time = b.start.mtime
	}
	if cand.Time.IsZero() {
		cand.Time = id.mtime
	}
	if reason := Newer(running, cand); reason != "" {
		b.rejected = id
		return cand.Revision, reason, false
	}
	return cand.Revision, "", true
}

// Newer is "" when cand is a newer build than running, else why it is not.
// Both builds' times must be filled in (commit time, or file mtime). The
// same commit is never newer, dirty or not: a "-modified" build carries its
// commit's time, so it cannot be ordered against another one. Two release
// versions are ordered by semver rather than by time.
func Newer(running, cand Build) string {
	base := func(rev string) string { return strings.TrimSuffix(rev, "-modified") }
	if cand.Revision != "" && base(cand.Revision) == base(running.Revision) {
		return fmt.Sprintf("same revision %s as the running build", base(cand.Revision))
	}
	if c, ok := compareSemver(cand.Revision, running.Revision); ok {
		if c <= 0 {
			return fmt.Sprintf("version %s is not newer than the running %s", cand.Revision, running.Revision)
		}
		return ""
	}
	if !cand.Time.After(running.Time) {
		return fmt.Sprintf("built %s, not after the running build's %s",
			cand.Time.UTC().Format(time.RFC3339), running.Time.UTC().Format(time.RFC3339))
	}
	return ""
}

// compareSemver orders two release versions (vX.Y.Z, optionally with a
// pre-release); ok is false unless both are.
func compareSemver(a, b string) (int, bool) {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0, true
	case pa.pre == "": // a release sorts after its pre-releases
		return 1, true
	case pb.pre == "":
		return -1, true
	default:
		return comparePrerelease(pa.pre, pb.pre), true
	}
}

func comparePrerelease(a, b string) int {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aa) && i < len(bb); i++ {
		x, y := aa[i], bb[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn < yn {
				return -1
			}
			if xn > yn {
				return 1
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	if len(aa) < len(bb) {
		return -1
	}
	if len(aa) > len(bb) {
		return 1
	}
	return 0
}

type semver struct {
	n   [3]int
	pre string
}

func parseSemver(v string) (semver, bool) {
	var s semver
	v, ok := strings.CutPrefix(v, "v")
	if !ok {
		return s, false
	}
	v, _, _ = strings.Cut(v, "+")
	v, s.pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}

// Replace execs the binary at Path with this process's arguments and
// environment plus env (KEY=value, overriding), keeping the pid. It returns
// only if the exec failed (or a fake Exec succeeded); a failed replacement
// is not offered again until the file changes once more.
func (b *Binary) Replace(env ...string) error {
	id, _ := stat(b.Path)
	CloseOnExecAboveStdio()
	err := b.Exec(b.Path, os.Args, mergeEnv(os.Environ(), env))
	if err != nil {
		b.mu.Lock()
		b.failed = id
		b.mu.Unlock()
	}
	return err
}

// CloseOnExecAboveStdio marks every open descriptor above 2 close-on-exec,
// so the process exec'd next starts with none of this one's. Descriptors the
// C library opened itself carry no such flag (macOS's resolver keeps a
// netsrc socket from its first getaddrinfo on), and after an exec the new
// image's library opens its own beside the inherited ones: without this,
// every replace keeps one more, one per install (yardr-b4ep). Nothing the
// new image needs is inherited: the server re-opens the store, the daemon
// re-takes its lock. A descriptor opened by another goroutine while this
// runs may miss the marking; the next replace marks it.
//
// Replace calls it, and so does the start of every command that runs a
// depot's own code (an exec group's script, the quiet run's tests): Go's
// os/exec closes nothing above 2 in a child, so the socket the library keeps
// for as long as the daemon lives would otherwise reach every landing's test
// command.
func CloseOnExecAboveStdio() {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
	}
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
