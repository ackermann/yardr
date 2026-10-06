package rigcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
)

// A full gate takes a slot of the machine before it runs: no more than N
// run at once, whoever starts them and from whichever yard, since N gates of
// five cores each on a ten-core machine make every one of them slow, and
// tests with timing in them fail.
//
// A slot is an exclusive flock on a file slot-<i> (1..N) in SlotDir, held by
// the yardr that runs the check and by nothing it starts. The kernel drops
// it when that process ends, however it ends, so a slot cannot leak; the
// files stay. A run that finds none free holds a flock on a file of its own
// in waiting/ for as long as it waits, which is how the waiting are counted
// (Count) and how one that goes first (Slots.First) is seen by the others.
//
// The limit is each run's own: a run with limit N takes one of the first N
// slots. Yards of one machine with different limits therefore share the
// lower slots, and the highest limit is what holds for the machine.

// SlotsSetting is the yard's setting for how many full gates run at once on
// the machine; DefaultSlots is what a yard without it gets.
const (
	SlotsSetting = "gate_slots"
	DefaultSlots = 2
)

// DefaultSlotWait bounds the wait for a slot. Past it the check could not
// run, which is not a red gate.
const DefaultSlotWait = 30 * time.Minute

// SlotVar is set in the environment of a check that runs under a slot. A
// yardr rig check started from inside one (a gate whose tests run a gate)
// takes no slot of its own: its load is the outer gate's, and with every
// slot taken by gates waiting on their own tests none would ever end.
const SlotVar = "YARDR_GATE_SLOT"

// slotPoll is how often a waiting run looks for a free slot.
var slotPoll = 200 * time.Millisecond

// ErrSlotWait is a wait for a slot that ran past its bound.
var ErrSlotWait = errors.New("no gate slot")

// Slots is how one run takes its slot.
type Slots struct {
	// Dir holds the slot files (SlotDir).
	Dir string
	// Limit is how many gates may run at once; 0 or less is no limit, and
	// the run takes no slot.
	Limit int
	// First: the others waiting stand back for this run, and so does a run
	// that only arrives. It is for the refinery and the integrator, whose
	// one gate at a time is the gate every approved bead waits behind, while
	// builders and reviewers can be eight to its one.
	First bool
	// Wait bounds the wait; 0 is DefaultSlotWait.
	Wait time.Duration
	// Notify is told, in one line each, that the run waits and that it got
	// its slot. It may be nil.
	Notify func(line string)
	// Waiting is told true when the run starts to wait for a slot and false
	// when it has one: what the yard shows of a gate (running.go). It may be
	// nil.
	Waiting func(waiting bool)
	// Holder is written into the slot file, for a person who looks.
	Holder string
}

// SlotDir is where the machine's slots are: YARDR_GATE_DIR, else
// <home>/.local/state/yardr/gates, with the home the user database names,
// not $HOME. Every yard of the user shares it, and so does an isolated yard
// (contrib/isolated-yard), whose emptied environment has a HOME and an
// XDG_STATE_HOME of its own but runs on the same cores. A user the database
// does not know gets /tmp/yardr-gates-<uid>.
func SlotDir() string {
	if d := config.Getenv("GATE_DIR"); d != "" {
		return d
	}
	uid := strconv.Itoa(os.Getuid())
	if u, err := user.LookupId(uid); err == nil && filepath.IsAbs(u.HomeDir) {
		return filepath.Join(u.HomeDir, ".local", "state", "yardr", "gates")
	}
	return "/tmp/yardr-gates-" + uid
}

// SlotLimit is the limit a run goes by: YARDR_GATE_SLOTS when it is set (a
// run with no yard to ask, or one run told otherwise), else the yard's
// gate_slots setting, else DefaultSlots. 0 is no limit.
func SlotLimit(settings map[string]string) (int, error) {
	name, v := "YARDR_GATE_SLOTS", config.Getenv("GATE_SLOTS")
	if v == "" {
		name, v = SlotsSetting, settings[SlotsSetting]
	}
	if v == "" {
		return DefaultSlots, nil
	}
	return ParseSlots(name, v)
}

// ParseSlots reads a limit: a whole number, 0 for none.
func ParseSlots(name, v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s %q: a whole number of gates that may run at once on the machine, 0 for no limit", name, v)
	}
	return n, nil
}

func waitingDir(dir string) string { return filepath.Join(dir, "waiting") }

// Acquire takes a slot, waiting for one when all are taken, and returns what
// gives it back and how long the wait was. With no limit it takes none.
func (s Slots) Acquire(ctx context.Context) (release func(), waited time.Duration, err error) {
	if s.Limit <= 0 {
		return func() {}, 0, nil
	}
	if err := os.MkdirAll(waitingDir(s.Dir), 0o700); err != nil {
		return nil, 0, fmt.Errorf("gate slots: %w", err)
	}
	f, err := s.try()
	if err != nil {
		return nil, 0, err
	}
	if f != nil {
		return s.held(f), 0, nil
	}

	// Wait, and be seen waiting.
	kind := "wait"
	if s.First {
		kind = "first"
	}
	mark, err := holdNew(filepath.Join(waitingDir(s.Dir), fmt.Sprintf("%s-%d-%d", kind, os.Getpid(), time.Now().UnixNano())))
	if err != nil {
		return nil, 0, fmt.Errorf("gate slots: %w", err)
	}
	defer func() {
		os.Remove(mark.Name())
		mark.Close()
	}()
	bound := s.Wait
	if bound <= 0 {
		bound = DefaultSlotWait
	}
	start := time.Now()
	if s.Waiting != nil {
		s.Waiting(true)
	}
	c, _ := Count(s.Dir)
	if c.Running >= s.Limit {
		s.notify("%s running on this machine; waiting for a slot", gates(c.Running))
	} else {
		// A slot is free and this run stands back for one that goes first.
		s.notify("a gate that goes first is waiting for a slot on this machine; waiting for the next one")
	}
	tick := time.NewTicker(slotPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, time.Since(start), fmt.Errorf("waiting for a gate slot: %w", ctx.Err())
		case <-tick.C:
		}
		if f, err = s.try(); err != nil {
			return nil, time.Since(start), err
		}
		if f != nil {
			waited = time.Since(start)
			s.notify("got a slot after waiting %s", waited.Round(time.Second))
			if s.Waiting != nil {
				s.Waiting(false)
			}
			return s.held(f), waited, nil
		}
		if time.Since(start) >= bound {
			c, _ := Count(s.Dir)
			return nil, time.Since(start), fmt.Errorf("%w within %s: %s running on this machine, limit %d (slots in %s)",
				ErrSlotWait, bound, gates(c.Running), s.Limit, s.Dir)
		}
	}
}

func (s Slots) notify(format string, args ...any) {
	if s.Notify != nil {
		s.Notify(fmt.Sprintf(format, args...))
	}
}

// gates is n gates in words: 1 gate is, 2 gates are.
func gates(n int) string {
	if n == 1 {
		return "1 gate is"
	}
	return fmt.Sprintf("%d gates are", n)
}

// try takes a free slot, or returns nil when there is none for this run:
// all are taken, or one that goes first is waiting and this run does not.
func (s Slots) try() (*os.File, error) {
	if !s.First && firstWaiting(s.Dir) {
		return nil, nil
	}
	for i := 1; i <= s.Limit; i++ {
		f, err := os.OpenFile(filepath.Join(s.Dir, fmt.Sprintf("slot-%d", i)), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("gate slots: %w", err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		f.Close()
	}
	return nil, nil
}

// held notes who holds the slot in its file and returns what frees it. The
// file is opened close-on-exec, as Go opens every file: the check does not
// inherit the lock, so a process the check leaves behind cannot keep a slot.
func (s Slots) held(f *os.File) func() {
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d since %s %s\n", os.Getpid(), time.Now().Format(time.RFC3339), s.Holder)), 0)
	return func() { f.Close() }
}

// holdNew creates path and holds an exclusive flock on it. Whoever counts
// the waiting removes a file nobody holds, and may do so between this
// file's creation and its lock: then the lock is on a file with no name,
// and it is made again.
func holdNew(path string) (*os.File, error) {
	for range 50 {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			// Somebody counting has it for an instant.
			f.Close()
			time.Sleep(5 * time.Millisecond)
			continue
		}
		mine, err1 := f.Stat()
		named, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && os.SameFile(mine, named) {
			return f, nil
		}
		f.Close()
	}
	return nil, fmt.Errorf("could not hold %s", path)
}

// isHeld reports whether a process holds an exclusive flock on path. It
// asks by taking a shared one for an instant, which two that ask at once
// both get; a run that tries for the slot in that instant finds it taken
// and looks again at its next poll. With reap, a file nobody holds is
// removed, under the shared lock: its owner cannot take it in between and
// be left holding a file with no name.
func isHeld(path string, reap bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	lock := syscall.LOCK_SH
	if reap {
		// Reapers must exclude each other. With shared locks, two readers
		// could both inspect a stale inode; after one unlinks it and a waiter
		// creates a new marker at the same path, the other could unlink that
		// live marker without holding its lock.
		lock = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), lock|syscall.LOCK_NB); err != nil {
		return true
	}
	if reap {
		// Only remove the inode we locked. The name may have been replaced
		// between Open and Flock by another reaper.
		mine, err1 := f.Stat()
		named, err2 := os.Stat(path)
		if err1 == nil && err2 == nil && os.SameFile(mine, named) {
			os.Remove(path)
		}
	}
	return false
}

// waiters lists the runs waiting in dir by the kind in their file's name
// ("first", "wait"), and removes the files of those that are gone.
func waiters(dir string) (first, other int) {
	entries, _ := os.ReadDir(waitingDir(dir))
	for _, e := range entries {
		switch {
		case !isHeld(filepath.Join(waitingDir(dir), e.Name()), true):
		case strings.HasPrefix(e.Name(), "first-"):
			first++
		default:
			other++
		}
	}
	return first, other
}

func firstWaiting(dir string) bool {
	first, _ := waiters(dir)
	return first > 0
}

// SlotCount is what runs and waits on the machine now.
type SlotCount struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// Count reads dir: the slots that are held, whatever limit their holders
// went by, and the runs waiting for one. A directory that is not there
// holds none.
func Count(dir string) (SlotCount, error) {
	var c SlotCount
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "slot-") {
			continue
		}
		if isHeld(filepath.Join(dir, e.Name()), false) {
			c.Running++
		}
	}
	first, other := waiters(dir)
	c.Waiting = first + other
	return c, nil
}
