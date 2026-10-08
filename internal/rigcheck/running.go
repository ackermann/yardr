package rigcheck

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A yardr depot check run for a bead is known to the yard while it runs: an
// agent that has committed and waits for its gate is idle to herdr, and
// without this the idle watch tells the yardmaster of every one of them (a gate
// takes five to ten minutes, a builders' idle limit is five).
//
// Each such run holds an exclusive flock on a file of its own in
// <home>/gating, as a run waiting for a slot holds one in the slots'
// waiting/ (slots.go): the kernel drops the lock when the process ends,
// however it ends, so a gate that was killed is not a gate that runs, and
// whoever reads the directory removes the files nobody holds. The file says
// which bead, light or full, since when, and whether the run still waits
// for a slot.
//
// It is in the yard's home, not with the machine's slots: a bead is one
// yard's, the slots are every yard's, and a light gate takes no slot.

// RunningDir is the directory of the running gates, in the yard's home.
const RunningDir = "gating"

// Gate is one yardr depot check running for a bead.
type Gate struct {
	Bead  string `json:"bead"`
	Depot string `json:"depot"`
	// Light: the light gate was asked for (--light), also when the depot has
	// none and its full gate runs instead.
	Light bool `json:"light,omitempty"`
	// Waiting: the run has no slot of the machine yet.
	Waiting bool `json:"waiting,omitempty"`
	// Since is when the run started, the wait for a slot included.
	Since time.Time `json:"since"`
	// Dir is where the check runs, and Pid the yardr that runs it.
	Dir string `json:"dir,omitempty"`
	Pid int    `json:"pid,omitempty"`
}

// Kind is "light" or "full".
func (g Gate) Kind() string {
	if g.Light {
		return "light"
	}
	return "full"
}

// Show is the gate in a few words, as of now: "gating 6m (light)", or
// "waiting 2m (full)" for one that waits for a slot.
func (g Gate) Show(now time.Time) string {
	verb := "gating"
	if g.Waiting {
		verb = "waiting"
	}
	return fmt.Sprintf("%s %s (%s)", verb, age(now.Sub(g.Since)), g.Kind())
}

// age is d as a person reads it at a glance: 45s, 6m, 1h05m.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// Running is a gate's file, held for as long as the gate runs.
type Running struct {
	// path is the file's name, which is not f.Name(): holdNew opened it
	// under another.
	path string
	f    *os.File
	g    Gate
}

// MarkRunning records g in home until End. Without a home, or a bead, there
// is nothing to record, and the nil it returns takes every call.
func MarkRunning(home string, g Gate) (*Running, error) {
	if home == "" || g.Bead == "" {
		return nil, nil
	}
	dir := filepath.Join(home, RunningDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if g.Pid == 0 {
		g.Pid = os.Getpid()
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d-%d", safe(g.Bead), g.Pid, time.Now().UnixNano()))
	f, err := holdNew(path)
	if err != nil {
		return nil, err
	}
	r := &Running{path: path, f: f, g: g}
	if err := r.write(); err != nil {
		r.End()
		return nil, err
	}
	return r, nil
}

// SetWaiting says whether the run waits for a slot.
func (r *Running) SetWaiting(waiting bool) {
	if r == nil || r.g.Waiting == waiting {
		return
	}
	r.g.Waiting = waiting
	_ = r.write()
}

// write puts the gate in its file: the new content first and the cut to its
// length after, so a reader in between finds a whole value, with the old
// one's end behind it, never half of one.
func (r *Running) write() error {
	data, err := json.Marshal(r.g)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := r.f.WriteAt(data, 0); err != nil {
		return err
	}
	return r.f.Truncate(int64(len(data)))
}

// End removes the record: the gate has ended.
func (r *Running) End() {
	if r == nil {
		return
	}
	os.Remove(r.path)
	r.f.Close()
}

// Gates are the gates running for beads of the yard in home now, the
// oldest first. The files of runs that are gone are removed. A home without
// the directory has none.
func Gates(home string) ([]Gate, error) {
	if home == "" {
		return nil, nil
	}
	dir := filepath.Join(home, RunningDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var gates []Gate
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !isHeld(path, true) {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		// A gate started by the build before this one, and still running,
		// names its depot as that build did.
		var g struct {
			Gate
			Old string `json:"rig"`
		}
		// The first value only: see write. A file not written yet is a gate
		// seen at the next look.
		err = json.NewDecoder(f).Decode(&g)
		f.Close()
		if err != nil || g.Bead == "" {
			continue
		}
		g.Depot = cmp.Or(g.Depot, g.Old)
		gates = append(gates, g.Gate)
	}
	slices.SortFunc(gates, func(a, b Gate) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return strings.Compare(a.Bead, b.Bead)
	})
	return gates, nil
}

// GateOf is the gate of gates that runs for bead: one that has its slot
// before one that waits, and of those the oldest.
func GateOf(gates []Gate, bead string) (Gate, bool) {
	var found Gate
	ok := false
	for _, g := range gates {
		if g.Bead != bead {
			continue
		}
		if !ok || found.Waiting && !g.Waiting {
			found, ok = g, true
		}
	}
	return found, ok
}
