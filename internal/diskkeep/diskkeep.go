// Package diskkeep keeps the machine's disk free for landings' builds.
// Before a landing's tests run, the assembly keeps the disk: under
// disk_keep_gb free on GOCACHE's volume it rotates GOCACHE at a fresh
// sibling directory (so builders that start afterwards use it), removes a
// rotated directory once it has stood aside for 30 minutes, and clears
// go-build directories in GOTMPDIR older than 3 hours. It logs what it did
// as a disk_kept event, and says it gave up when free space is still under
// 10 GB afterwards.
package diskkeep

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Actor is who the disk_kept event is told as: the yard itself, whatever
// ran the keeping.
const Actor = "yard"

// GB is a gigabyte, the unit disk_keep_gb and the gave-up floor are read in.
const GB = 1 << 30

// GaveUpGB is the free space below which a keeping that is over still says
// it gave up: rotation and pruning freed nothing like enough, and a person
// has to look at the disk.
const GaveUpGB = 10

// OldGrace is how long a rotated GOCACHE directory stands aside before a
// landing removes it: builds that started before the rotation still write
// to it, and files are never taken from under a running build.
const OldGrace = 30 * time.Minute

// TmpAge is how old a go-build directory in GOTMPDIR must be before it is
// cleared: a build's own scratch is fresh, and only the stale is taken.
const TmpAge = 3 * time.Hour

// freeDisk reports the bytes free for an unprivileged process on the volume
// holding path; tests stand a fake in.
var freeDisk = func(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// goEnv runs go env for the names; tests stand a fake in.
var goEnv = func(names ...string) (string, error) {
	out, err := exec.Command("go", append([]string{"env"}, names...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// goEnvSet runs go env -w for one assignment; tests stand a fake in.
var goEnvSet = func(kv string) error {
	return exec.Command("go", "env", "-w", kv).Run()
}

// hasGo reports whether go is on PATH; tests stand a fake in. Without go
// there is no GOCACHE to rotate and no go-tmp to clear, so there is nothing
// to keep.
var hasGo = func() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// now is the time; tests stand a fake in.
var now = time.Now

// Report is what one keeping did.
type Report struct {
	// Acted is whether the disk was under the threshold and anything was
	// done at all.
	Acted bool `json:"acted"`
	// FreeBefore and FreeAfter are the free bytes on GOCACHE's volume
	// before and after.
	FreeBefore uint64 `json:"free_before"`
	FreeAfter  uint64 `json:"free_after"`
	// RotatedFrom and RotatedTo are the GOCACHE the keeping moved away
	// from and the fresh sibling it points at now.
	RotatedFrom string `json:"rotated_from"`
	RotatedTo   string `json:"rotated_to"`
	// Removed is every directory the keeping took from the disk: rotated
	// GOCACHE directories past their grace, and stale go-tmp.
	Removed []string `json:"removed"`
	// GaveUp is whether free space is still under 10 GB after the keeping.
	GaveUp bool `json:"gave_up"`
	// Errors is what went wrong past the rotation, each a sentence: a
	// directory that could not be removed stays for the next landing.
	Errors []string `json:"errors,omitempty"`
}

// Lines says the report in human lines: nothing when nothing was done.
func (r *Report) Lines() []string {
	if r == nil || !r.Acted {
		return nil
	}
	lines := []string{
		fmt.Sprintf("disk kept: free %s -> %s", gigabytes(r.FreeBefore), gigabytes(r.FreeAfter)),
		fmt.Sprintf("rotated GOCACHE %s -> %s", r.RotatedFrom, r.RotatedTo),
	}
	for _, dir := range r.Removed {
		lines = append(lines, "removed "+dir)
	}
	for _, err := range r.Errors {
		lines = append(lines, "disk keep: "+err)
	}
	if r.GaveUp {
		lines = append(lines, fmt.Sprintf("gave up: free space still under %d GB after keeping the disk", GaveUpGB))
	}
	return lines
}

// gigabytes says bytes in gigabytes with one decimal.
func gigabytes(n uint64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/GB)
}

// Keep keeps the disk when it is under the threshold in settings, and
// reports what it did. Over the threshold, with the keeping turned off, or
// without go, it does nothing and reports that, with no event: an idle
// keeping leaves no record.
//
// Past the rotation everything is best effort: a directory that cannot be
// removed, or a free-space reading that fails, joins the report's errors
// and the disk_kept event carries them, so the landing goes on to its
// tests with the disk as it is. Only what fails before anything was done
// is an error.
func Keep(ctx context.Context, st store.API, settings map[string]string) (*Report, error) {
	thresholdGB := uint64(store.DiskKeepGB(settings))
	if thresholdGB == 0 || !hasGo() {
		return &Report{}, nil
	}
	gocache, err := goEnv("GOCACHE")
	if err != nil {
		return nil, fmt.Errorf("go env GOCACHE: %w", err)
	}
	if gocache == "" {
		return nil, fmt.Errorf("go env GOCACHE: empty")
	}
	free, err := freeDisk(existingDir(gocache))
	if err != nil {
		return nil, fmt.Errorf("free space on %s: %w", gocache, err)
	}
	// Comparing whole gigabytes avoids overflowing when a valid setting is
	// larger than the largest byte count uint64 can hold.
	if free/GB >= thresholdGB {
		return &Report{FreeBefore: free, FreeAfter: free}, nil
	}
	rep := &Report{Acted: true, FreeBefore: free, FreeAfter: free, Removed: []string{}}
	next, err := freshSibling(gocache)
	if err != nil {
		return nil, err
	}
	if err := goEnvSet("GOCACHE=" + next); err != nil {
		return nil, fmt.Errorf("go env -w GOCACHE=%s: %w", next, err)
	}
	// The rotation took only if go now answers with the new directory: a
	// GOCACHE from the environment wins over the file `go env -w` wrote,
	// and recording the old one as stood aside would lose it.
	if cur, err := goEnv("GOCACHE"); err != nil {
		return nil, fmt.Errorf("go env GOCACHE: %w", err)
	} else if cur != next {
		return nil, fmt.Errorf("GOCACHE is %s after go env -w GOCACHE=%s: the environment overrides the go env file", cur, next)
	}
	rep.RotatedFrom, rep.RotatedTo = gocache, next
	at := now()
	if err := st.RecordGOCACHEOld(ctx, gocache, at); err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("could not record %s as rotated away from: %v", gocache, err))
	}
	rep.Removed = append(rep.Removed, removeOld(ctx, st, rep, next, at)...)
	rep.Removed = append(rep.Removed, pruneTmp(rep, at)...)
	if free, err := freeDisk(existingDir(next)); err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("could not read free space after: %v", err))
	} else {
		rep.FreeAfter = free
	}
	rep.GaveUp = rep.FreeAfter < GaveUpGB*GB
	data := map[string]any{
		"free_before":  rep.FreeBefore,
		"free_after":   rep.FreeAfter,
		"rotated_from": rep.RotatedFrom,
		"rotated_to":   rep.RotatedTo,
		"removed":      rep.Removed,
		"gave_up":      rep.GaveUp,
	}
	if len(rep.Errors) > 0 {
		data["errors"] = rep.Errors
	}
	if err := st.LogDispatch(ctx, "", store.DiskKept, Actor, data); err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("could not log the %s event: %v", store.DiskKept, err))
	}
	return rep, nil
}

// removeOld removes the recorded GOCACHE directories whose grace has
// passed, except the live one, and drops each from the record once it is
// gone. A directory that cannot be removed stays recorded for the next
// landing.
func removeOld(ctx context.Context, st store.API, rep *Report, live string, at time.Time) []string {
	olds, err := st.GOCACHEOldDirs(ctx)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("could not list rotated GOCACHE directories: %v", err))
		return nil
	}
	var removed []string
	for _, old := range olds {
		if old.Dir == "" || old.Dir == "/" || old.Dir == live || at.Sub(old.At) < OldGrace {
			continue
		}
		if err := os.RemoveAll(old.Dir); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("could not remove %s: %v", old.Dir, err))
			continue
		}
		if err := st.RemoveGOCACHEOld(ctx, old.Dir); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("could not drop %s from the record: %v", old.Dir, err))
			continue
		}
		removed = append(removed, old.Dir)
	}
	return removed
}

// pruneTmp removes the go-build directories in GOTMPDIR older than TmpAge.
// Without a GOTMPDIR, or with none there, there is nothing to clear.
func pruneTmp(rep *Report, at time.Time) []string {
	tmp, err := goEnv("GOTMPDIR")
	if err != nil || tmp == "" {
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("go env GOTMPDIR: %v", err))
		}
		return nil
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		if !os.IsNotExist(err) {
			rep.Errors = append(rep.Errors, fmt.Sprintf("could not read %s: %v", tmp, err))
		}
		return nil
	}
	var removed []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "go-build") || !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || at.Sub(info.ModTime()) < TmpAge {
			continue
		}
		dir := filepath.Join(tmp, e.Name())
		if err := os.RemoveAll(dir); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("could not remove %s: %v", dir, err))
			continue
		}
		removed = append(removed, dir)
	}
	return removed
}

// existingDir is path where it is a directory, else the nearest directory
// above it that is: a GOCACHE rotated to but not yet built in does not
// exist, and free space is its volume's all the same.
func existingDir(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// freshSibling is the next fresh sibling directory after dir: its numeric
// suffix raised by one (go-build-8 gives go-build-9), or -1 where it has
// none, past any that already exist.
func freshSibling(dir string) (string, error) {
	next := dir
	for range 100 {
		next = nextName(next)
		if _, err := os.Stat(next); os.IsNotExist(err) {
			return next, nil
		}
	}
	return "", fmt.Errorf("no fresh sibling directory after %s", dir)
}

// nextName raises the numeric suffix of dir's last path element by one.
func nextName(dir string) string {
	base := filepath.Base(dir)
	if i := strings.LastIndex(base, "-"); i >= 0 {
		if n, err := strconv.Atoi(base[i+1:]); err == nil {
			return filepath.Join(filepath.Dir(dir), base[:i]+"-"+strconv.Itoa(n+1))
		}
	}
	return dir + "-1"
}
