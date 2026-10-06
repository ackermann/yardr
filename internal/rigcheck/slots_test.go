package rigcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func TestMain(m *testing.M) {
	// Started as the holder of TestSlotFreedWhenItsProcessIsKilled: run one
	// check under a slot, and be killed.
	if dir := os.Getenv("RIGCHECK_TEST_HOLD"); dir != "" {
		os.Unsetenv(SlotVar)
		depot := store.Depot{Name: "r", Path: dir, Base: "main", Check: "echo $$ > " + filepath.Join(dir, "pid") + "; exec sleep 60"}
		_, err := RunWith(context.Background(), depot, "", Options{Slots: &Slots{Dir: dir, Limit: 1}})
		fmt.Fprintln(os.Stderr, "holder ended:", err)
		os.Exit(3)
	}
	// These tests run inside a gate that holds a slot, and its mark must not
	// keep the runs here from taking theirs. Every one names its own
	// directory: the machine's slots are never touched.
	os.Unsetenv(SlotVar)
	slotPoll = 10 * time.Millisecond
	os.Exit(m.Run())
}

// slotDepot is a depot whose gate is the command, with no repository behind it.
func slotDepot(t *testing.T, check string) store.Depot {
	t.Helper()
	return store.Depot{Name: "r", Path: t.TempDir(), Base: "main", Check: check}
}

// lines collects what a run was told.
type lines struct {
	mu  sync.Mutex
	got []string
}

func (l *lines) add(s string) { l.mu.Lock(); l.got = append(l.got, s); l.mu.Unlock() }
func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.got, "\n")
}

// take holds a slot of dir, as a running gate does, until the test ends or
// the returned func is called.
func take(t *testing.T, dir string, limit int) func() {
	t.Helper()
	release, waited, err := Slots{Dir: dir, Limit: limit, Wait: time.Second}.Acquire(context.Background())
	if err != nil || waited != 0 {
		t.Fatalf("taking a free slot: waited %s, %v", waited, err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(release) })
	return func() { once.Do(release) }
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 1000 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

// With one slot, two checks started together run one after the other: the
// second starts when the first has ended, and says that it waited.
func TestSlotsOneAtATime(t *testing.T) {
	t.Parallel()
	dir, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
	depot := slotDepot(t, "echo start >> "+log+"; sleep 0.3; echo end >> "+log)
	var told lines
	var wg sync.WaitGroup
	waits := make([]float64, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := RunWith(context.Background(), depot, "", Options{Timeout: time.Minute,
				Slots: &Slots{Dir: dir, Limit: 1, Notify: told.add}})
			if err != nil || !res.Pass {
				t.Errorf("run %d: %+v, %v", i, res, err)
				return
			}
			waits[i] = res.SlotWaited
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(log)
	if string(b) != "start\nend\nstart\nend\n" {
		t.Fatalf("the two checks overlapped:\n%s", b)
	}
	if (waits[0] > 0) == (waits[1] > 0) {
		t.Fatalf("waits %v: exactly one run waited", waits)
	}
	got := told.String()
	if !strings.HasPrefix(got, "1 gate is running on this machine; waiting for a slot\ngot a slot after waiting ") || strings.Count(got, "\n") != 1 {
		t.Fatalf("lines:\n%s", got)
	}
	if c, err := Count(dir); err != nil || c != (SlotCount{}) {
		t.Fatalf("after both: %+v, %v", c, err)
	}
}

// A slot is its holder's for as long as the process lives and no longer: a
// depot check killed with SIGKILL, which runs no cleanup, frees it at once,
// even while the check it started still runs.
func TestSlotFreedWhenItsProcessIsKilled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	holder := exec.Command(os.Args[0], "-test.run=^$")
	holder.Env = append(os.Environ(), "RIGCHECK_TEST_HOLD="+dir)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		holder.Process.Kill()
		holder.Wait()
		// The check outlives its yardr; it is this test's to end.
		if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	waitFor(t, "the holder's check runs under the slot", func() bool {
		_, err := os.Stat(filepath.Join(dir, "pid"))
		c, _ := Count(dir)
		return err == nil && c.Running == 1
	})
	// Taken: a run with a short bound gets none.
	if _, _, err := (Slots{Dir: dir, Limit: 1, Wait: 50 * time.Millisecond}).Acquire(context.Background()); !errors.Is(err, ErrSlotWait) {
		t.Fatalf("while the holder lives: %v, want ErrSlotWait", err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	holder.Wait()
	release, _, err := Slots{Dir: dir, Limit: 1, Wait: 5 * time.Second}.Acquire(context.Background())
	if err != nil {
		t.Fatalf("after SIGKILL: %v", err)
	}
	release()
}

// The wait is bounded, and running past the bound is not a result: the
// check could not run. Nothing of the check was started.
func TestSlotWaitBound(t *testing.T) {
	t.Parallel()
	dir, ran := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	take(t, dir, 1)
	var told lines
	start := time.Now()
	res, err := RunWith(context.Background(), slotDepot(t, "touch "+ran), "", Options{Timeout: time.Minute,
		Slots: &Slots{Dir: dir, Limit: 1, Wait: 150 * time.Millisecond, Notify: told.add}})
	if res != nil || !errors.Is(err, ErrSlotWait) {
		t.Fatalf("got %+v, %v; want ErrSlotWait", res, err)
	}
	for _, want := range []string{"within 150ms", "1 gate is running on this machine", "limit 1", dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Errorf("gave up after %s, before the bound", time.Since(start))
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the check ran without a slot")
	}
	if got := told.String(); got != "1 gate is running on this machine; waiting for a slot" {
		t.Errorf("lines:\n%s", got)
	}
	// It is no longer counted as waiting.
	if c, _ := Count(dir); c != (SlotCount{Running: 1}) {
		t.Errorf("count %+v, want 1 running and none waiting", c)
	}
}

// The check's timeout starts when the check does: a wait longer than the
// timeout leaves the check all of it.
func TestSlotWaitDoesNotEatTheTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release := take(t, dir, 1)
	const timeout = 2 * time.Second
	go func() {
		time.Sleep(timeout + 300*time.Millisecond)
		release()
	}()
	res, err := RunWith(context.Background(), slotDepot(t, "true"), "", Options{Timeout: timeout,
		Slots: &Slots{Dir: dir, Limit: 1}})
	if err != nil || !res.Pass || res.TimedOut {
		t.Fatalf("got %+v, %v; want a pass", res, err)
	}
	if res.SlotWaited < timeout.Seconds() {
		t.Fatalf("waited %.2fs, want more than the timeout of %s", res.SlotWaited, timeout)
	}
	// And the timeout still holds for the check itself.
	res, err = RunWith(context.Background(), slotDepot(t, "sleep 30"), "", Options{Timeout: 100 * time.Millisecond,
		Slots: &Slots{Dir: dir, Limit: 1}})
	if err != nil || res.Pass || !res.TimedOut {
		t.Fatalf("a check past its timeout: %+v, %v", res, err)
	}
}

// Limit 0 is no limit: the run takes no slot and touches no directory.
func TestSlotsNoLimit(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "never-made")
	res, err := RunWith(context.Background(), slotDepot(t, `test -z "$`+SlotVar+`"`), "", Options{Timeout: time.Minute,
		Slots: &Slots{Dir: dir, Limit: 0, Notify: func(s string) { t.Errorf("told %q", s) }}})
	if err != nil || !res.Pass {
		t.Fatalf("got %+v, %v", res, err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a run with no limit made the slot directory")
	}
}

// The light gate takes no slot, so it runs while every slot is taken. It
// takes one when the depot says so, and so does the full gate run for want of
// a light one.
func TestLightGateTakesNoSlot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	take(t, dir, 1)
	slots := func() *Slots {
		return &Slots{Dir: dir, Limit: 1, Wait: 100 * time.Millisecond}
	}
	depot := slotDepot(t, "exit 4")
	depot.CheckLight = `test -z "$` + SlotVar + `"`
	res, err := RunWith(context.Background(), depot, "", Options{Light: true, Timeout: time.Minute, Slots: slots()})
	if err != nil || !res.Pass || !res.Light || res.SlotWaited != 0 {
		t.Fatalf("light gate with every slot taken: %+v, %v", res, err)
	}
	depot.LightSlot = true
	if _, err := RunWith(context.Background(), depot, "", Options{Light: true, Timeout: time.Minute, Slots: slots()}); !errors.Is(err, ErrSlotWait) {
		t.Fatalf("light gate of a depot with light_slot: %v, want ErrSlotWait", err)
	}
	// No light gate: --light runs the full one, which is as heavy as ever.
	depot.LightSlot, depot.CheckLight = false, ""
	if _, err := RunWith(context.Background(), depot, "", Options{Light: true, Timeout: time.Minute, Slots: slots()}); !errors.Is(err, ErrSlotWait) {
		t.Fatalf("full gate under --light: %v, want ErrSlotWait", err)
	}
}

// A check under a slot is told so, and a depot check started from inside it
// takes none: with every slot held by gates waiting on gates of their own
// tests, none would end.
func TestCheckInsideAGateTakesNoSlot(t *testing.T) {
	dir := t.TempDir()
	res, err := RunWith(context.Background(), slotDepot(t, `test "$`+SlotVar+`" = 1`), "", Options{Timeout: time.Minute,
		Slots: &Slots{Dir: dir, Limit: 1}})
	if err != nil || !res.Pass {
		t.Fatalf("the check is not told it holds a slot: %+v, %v", res, err)
	}
	take(t, dir, 1)
	t.Setenv(SlotVar, "1")
	res, err = RunWith(context.Background(), slotDepot(t, "true"), "", Options{Timeout: time.Minute,
		Slots: &Slots{Dir: dir, Limit: 1, Wait: 100 * time.Millisecond}})
	if err != nil || !res.Pass {
		t.Fatalf("inside a gate, with every slot taken: %+v, %v", res, err)
	}
}

// A run that goes first gets the next free slot before one that waited
// longer, and one that only arrives stands back for it too. Both are
// counted while they wait.
func TestSlotFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release := take(t, dir, 1)
	var order lines
	var wg sync.WaitGroup
	wait := func(name string, first bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, _, err := Slots{Dir: dir, Limit: 1, First: first, Wait: 30 * time.Second}.Acquire(context.Background())
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			order.add(name)
			time.Sleep(50 * time.Millisecond)
			rel()
		}()
	}
	wait("builder", false)
	waitFor(t, "the builder waits", func() bool { c, _ := Count(dir); return c.Waiting == 1 })
	wait("refinery", true)
	waitFor(t, "both wait", func() bool { c, _ := Count(dir); return c == SlotCount{Running: 1, Waiting: 2} })
	release()
	// A run arriving now, with the slot free for an instant, does not take
	// it from under the one that goes first.
	var told lines
	rel, waited, err := Slots{Dir: dir, Limit: 1, Wait: 30 * time.Second, Notify: told.add}.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order.add("late")
	rel()
	wg.Wait()
	if got := order.String(); !strings.HasPrefix(got, "refinery\n") {
		t.Fatalf("order:\n%s\nwant the refinery first", got)
	}
	if waited == 0 {
		t.Errorf("the late run did not wait; lines:\n%s", told.String())
	}
	if c, _ := Count(dir); c != (SlotCount{}) {
		t.Errorf("after all: %+v", c)
	}
	if e, _ := os.ReadDir(waitingDir(dir)); len(e) != 0 {
		t.Errorf("%d files left in waiting/", len(e))
	}
}

// A released slot is free when the release returns, also while a child the
// process forked has not yet exec'd and still has its copy of the slot's
// descriptor: the copy made here stands for that one. With the lock given up
// only by closing, the slot stayed taken until the child's exec, and a count
// right after the release said a gate was running (TestSlotFirst failed so,
// with the package's other tests starting their checks beside it).
func TestSlotFreeAtReleaseWhileAForkedChildHasItsDescriptor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := Slots{Dir: dir, Limit: 1}
	if err := os.MkdirAll(waitingDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := s.try()
	if err != nil || f == nil {
		t.Fatalf("taking a free slot: %v, %v", f, err)
	}
	child, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(child)
	defer syscall.Close(child)
	release := s.held(f)
	if c, _ := Count(dir); c != (SlotCount{Running: 1}) {
		t.Fatalf("while held: %+v", c)
	}
	release()
	if c, _ := Count(dir); c != (SlotCount{}) {
		t.Errorf("after the release: %+v", c)
	}
	take(t, dir, 1)
}

// Yards with different limits share the lower slots.
func TestSlotLimitsShareTheDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	take(t, dir, 2)
	take(t, dir, 2)
	if _, _, err := (Slots{Dir: dir, Limit: 2, Wait: 50 * time.Millisecond}).Acquire(context.Background()); !errors.Is(err, ErrSlotWait) {
		t.Fatalf("a third of two: %v", err)
	}
	take(t, dir, 3)
	if c, _ := Count(dir); c != (SlotCount{Running: 3}) {
		t.Fatalf("count %+v, want 3 running", c)
	}
}

func TestSlotLimit(t *testing.T) {
	for _, tc := range []struct {
		env, setting string
		want         int
		bad          bool
	}{
		{"", "", DefaultSlots, false},
		{"", "0", 0, false},
		{"", "5", 5, false},
		{"0", "5", 0, false},
		{"3", "", 3, false},
		{"", "-1", 0, true},
		{"two", "", 0, true},
	} {
		t.Setenv("YARDR_GATE_SLOTS", tc.env)
		got, err := SlotLimit(map[string]string{SlotsSetting: tc.setting})
		if got != tc.want || (err != nil) != tc.bad {
			t.Errorf("env %q, setting %q: %d, %v; want %d (error: %v)", tc.env, tc.setting, got, err, tc.want, tc.bad)
		}
	}
}

// The slots are found without the environment: a yard whose HOME and
// XDG_STATE_HOME are its own (contrib/isolated-yard) shares them.
func TestSlotDirIgnoresHome(t *testing.T) {
	t.Setenv("YARDR_GATE_DIR", "")
	t.Setenv("YARD_GATE_DIR", "")
	want := SlotDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	if got := SlotDir(); got != want || !filepath.IsAbs(got) {
		t.Fatalf("SlotDir = %q with another HOME, %q before", got, want)
	}
	t.Setenv("YARDR_GATE_DIR", "/somewhere/else")
	if got := SlotDir(); got != "/somewhere/else" {
		t.Fatalf("SlotDir = %q with YARDR_GATE_DIR set", got)
	}
}
