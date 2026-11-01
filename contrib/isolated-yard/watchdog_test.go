package isolatedyard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/testload"
)

// scriptPath is the isolated-yard script, by the path a yard's watchdog is
// started with.
func scriptPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "contrib/isolated-yard/isolated-yard")
}

// psRow is a line of ps: a process, its parent and its command line as fields.
type psRow struct {
	pid, ppid int
	command   []string
}

// processes lists the machine's processes as ps shows them.
func processes(t *testing.T) []psRow {
	t.Helper()
	out, err := exec.Command("ps", "axww", "-o", "pid=", "-o", "ppid=", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var rows []psRow
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			rows = append(rows, psRow{pid, ppid, f[2:]})
		}
	}
	return rows
}

// runs reports whether the command line ends with the script's verb and the
// yard d: how the script's watchdog and down for d are told from the rest.
func (p psRow) runs(verb, d string) bool {
	n := len(p.command)
	return n >= 2 && p.command[n-2] == verb && p.command[n-1] == d
}

// watchdogsOf are the pids of the watchdogs started for the yard d, found by
// their command line: the script's watchdog command for d, behind D/e until
// the wrapper has executed it. What the watchdog has forked and not yet
// replaced (the program it runs each second, until the system lets it start)
// has the same command line, so a match whose parent is a match is no
// watchdog of its own.
func watchdogsOf(t *testing.T, d string) []int {
	t.Helper()
	parent := map[int]int{}
	for _, p := range processes(t) {
		if p.runs("watchdog", d) {
			parent[p.pid] = p.ppid
		}
	}
	var pids []int
	for pid, ppid := range parent {
		if _, forked := parent[ppid]; !forked {
			pids = append(pids, pid)
		}
	}
	return pids
}

// downsOf are the pids of the shells running the script's down for the yard d,
// found by their command line as the watchdogs are: the watchdog that has found
// its owner gone (it execs down), and the subshells down forks for its listings,
// which share the command line. They are sorted, so that two lists are equal
// when the same shells run.
func downsOf(t *testing.T, d string) []int {
	t.Helper()
	var pids []int
	for _, p := range processes(t) {
		if p.runs("down", d) {
			pids = append(pids, p.pid)
		}
	}
	slices.Sort(pids)
	return pids
}

// runningOf lists what runs that names the yard d, a process a line with its
// state and age: what a wait on a down says it was waiting on when it gave up.
func runningOf(d string) string {
	out, err := exec.Command("ps", "axww", "-o", "pid=", "-o", "ppid=", "-o", "stat=", "-o", "etime=", "-o", "command=").Output()
	if err != nil {
		return fmt.Sprintf("ps: %v", err)
	}
	var running []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, d) {
			running = append(running, strings.TrimSpace(line))
		}
	}
	if len(running) == 0 {
		return "nothing of it"
	}
	return strings.Join(running, "\n")
}

// waitWhileDown is waitFor for a wait on a down of the yard d, with a fuse
// against a down that is stuck, not against one that is slow. A down is some
// hundred process starts and a listing of the whole process table for each of
// its stages; what that costs is what the machine makes it, a few seconds on an
// idle box and some ten times that at a load of a hundred, and no minute
// written for the idle box covers every load (yardr-c3nm). So the fuse is not
// the time the wait has run, nor the time that went by with the down unchanged,
// which a slow look stretches, but the looks that found it unchanged: no down
// there at all (a watchdog that never decides), or the same shells of it (a
// command that does not return). A down that goes on forks a subshell for each
// listing, and changes what downsOf finds as often as it makes progress.
func waitWhileDown(t *testing.T, d string, fuse time.Duration, cond func() bool) bool {
	t.Helper()
	return waitUnchanged(fuse, 250*time.Millisecond, func() []int { return downsOf(t, d) }, cond)
}

// waitUnchanged polls cond at each look until it holds, and reports whether it
// did before what watch finds stood still for the fuse (testload.Scale). The
// stand-still is the looks since what is watched last changed, counted, not
// timed: a look takes what the machine makes it (the pause stretched past its
// length, a slow listing, a cond that starts a process), and a stand-still
// timed in seconds spent its fuse on that, not on what is watched (yardr-ka44).
func waitUnchanged(fuse, look time.Duration, watch func() []int, cond func() bool) bool {
	var seen []int
	// The fuse in looks, scaled once, as the wait is armed. A load that rises
	// after that stretches the looks, and so the time the count spans; one that
	// falls cannot shorten a look below its length, so the wait that gives up
	// has outlasted the fuse.
	fuseLooks := int(testload.Scale(fuse) / look)
	// still counts the looks since the one that found a change, and the first
	// look is taken for one.
	for still := 0; ; still++ {
		if cond() {
			return true
		}
		if now := watch(); !slices.Equal(now, seen) {
			seen, still = now, 0
		} else if still > fuseLooks {
			return false
		}
		time.Sleep(look)
	}
}

// runWatchdog runs cmd, a watchdog for the yard d, to its end, and returns what
// it printed and how it ended. A watchdog that takes the yard down is waited
// for as long as its down goes on (waitWhileDown); one that does not must end
// of its own. One that is stuck is killed, and its error says what of the yard
// was running.
func runWatchdog(t *testing.T, cmd *exec.Cmd, d string, fuse time.Duration) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	var err error
	if !waitWhileDown(t, d, fuse, func() bool {
		select {
		case err = <-ended:
			return true
		default:
			return false
		}
	}) {
		cmd.Process.Kill()
		<-ended
		return out.String(), fmt.Errorf("the watchdog did not end, and no down of %s changed in %v; running of it:\n%s", d, testload.Scale(fuse), runningOf(d))
	}
	return out.String(), err
}

// withOwner is env with YARDR_TEST_OWNER set to owner, whatever it was.
func withOwner(env []string, owner string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return config.IsVar(key, config.TestOwnerEnv)
	})
	return append(env, config.Env(config.TestOwnerEnv, owner)...)
}

// yardr-q4bk: a test binary killed before its cleanups leaves the herdr
// server of its yards and their stand-in agents, which look for no owner. So
// every yard made under a test binary has a watchdog that takes it down when
// the binary is gone. Here the binary is this one, which lives: the yard's
// watchdog is one of its processes, down finds it, and a watchdog run by hand
// with an owner that is gone does what the cleanup would have done, through
// down: the yard is removed, the status is 0, and the yard's own watchdog,
// which down stops with the rest, is gone with it.
func TestWatchdogTakesTheYardDownWhenItsOwnerIsGone(t *testing.T) {
	d := bare(t)
	var dog int
	if !waitFor(watchdogStartFuse, func() bool {
		pids := watchdogsOf(t, d)
		if len(pids) > 1 {
			t.Fatalf("the yard has %d watchdogs, want one: %v", len(pids), pids)
		}
		if len(pids) == 1 {
			dog = pids[0]
		}
		return dog != 0
	}) {
		t.Fatalf("the yard %s has no watchdog", d)
	}
	out, err := script(t, "pids", d)
	if err != nil {
		t.Fatalf("isolated-yard pids: %v\n%s", err, out)
	}
	if !slices.Contains(strings.Fields(out), strconv.Itoa(dog)) {
		t.Errorf("pids lists %q, which lacks the watchdog (pid %d): down would not stop it", out, dog)
	}
	if !running(dog) {
		t.Fatalf("the watchdog (pid %d) is not running although this binary is", dog)
	}

	// By hand, through D/e as the yard's own is started, with the name of
	// another owner: one that has ended. It is waited for while its down goes
	// on, and killed when the down stands still (runWatchdog), so a broken
	// watch cannot hang the package test.
	owner := deadOwner(t)
	cmd := exec.Command(filepath.Join(d, "e"), "env", config.Key(config.TestOwnerEnv)+"="+owner, scriptPath(t), "watchdog", d)
	out, err = runWatchdog(t, cmd, d, ownerGoneFuse)
	if err != nil {
		t.Fatalf("watchdog through %s/e: %v\n%s", d, err, out)
	}
	if !strings.Contains(out, "stopped and removed "+d) {
		t.Errorf("a watchdog whose owner is gone printed:\n%s\nwant down's \"stopped and removed %s\"", out, d)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("%s is still there (%v)", d, err)
	}
	if running(dog) {
		t.Errorf("the yard's own watchdog (pid %d) outlived the down", dog)
	}
}

// yardr-q4bk: the watchdog the yard is started with does it by itself. A yard
// made for an owner that has ended is taken down at the watchdog's first look,
// and nothing of it is left. The wait is for that down to end, however long
// the machine makes it: it fails for a watchdog that starts none and for a down
// that stands still (waitWhileDown).
func TestWatchdogOfAYardMadeForAGoneOwnerTakesItDown(t *testing.T) {
	t.Parallel()
	d := bareEnv(t, withOwner(os.Environ(), deadOwner(t)))
	if !waitWhileDown(t, d, ownerGoneFuse, func() bool {
		_, err := os.Stat(d)
		return os.IsNotExist(err)
	}) {
		t.Fatalf("%s was not taken down by its watchdog, and no down of it changed in %v; running of it:\n%s", d, testload.Scale(ownerGoneFuse), runningOf(d))
	}
	if !waitFor(ownerGoneFuse, func() bool { return len(watchdogsOf(t, d)) == 0 }) {
		t.Errorf("the watchdog of %s is still running", d)
	}
}

// stubAlive answers the watchdog with the statuses in D/answers, one a call
// (the first line is removed as it is used), and with none left it removes the
// yard's mark and says 0, as a yard that is gone is for the next look.
const stubAlive = `#!/bin/sh
d=$(dirname "$0")/..
status=$(head -n 1 "$d/answers")
if [ -z "$status" ]; then
  rm -f "$d/.isolated-yard"
  exit 0
fi
tail -n +2 "$d/answers" >"$d/answers.next" && mv "$d/answers.next" "$d/answers"
exit "$status"
`

// yardr-q4bk: only alive's 1 (the owner is gone) and 2 (no name to go by) take
// a yard down. A signal that ends alive, a failed start and the like say
// nothing about the owner, and a watchdog that took one for a gone owner
// would take down yards in use whenever the machine is short of processes.
// The yards here are made by hand, with an alive that answers as told, so
// nothing runs but the watchdog: it looks again after a 3 and a 137 and ends
// with the yard when that goes (the yard stays), and for a 1 and a 2 it takes
// the yard down at once.
func TestWatchdogTakesAYardDownOnlyForAGoneOwner(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		answers string
		down    bool
	}{
		{"statuses that say nothing of the owner", "3\n137\n", false},
		{"a gone owner", "1\n", true},
		{"no name to go by", "2\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, err := os.MkdirTemp("/tmp", "iy.")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(d) })
			for path, body := range map[string]string{
				".isolated-yard": d + "\n",
				"e":              "#!/bin/sh\nexit 0\n",
				"answers":        c.answers,
			} {
				if err := os.WriteFile(filepath.Join(d, path), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(d, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "bin", "alive"), []byte(stubAlive), 0o755); err != nil {
				t.Fatal(err)
			}

			// Not through D/e: the yard has nothing a watchdog does not use,
			// and the name it needs is the one thing it is given.
			cmd := exec.Command(scriptPath(t), "watchdog", d)
			cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", config.Key(config.TestOwnerEnv) + "=1 1"}
			out, err := runWatchdog(t, cmd, d, watchdogRunFuse)
			if err != nil {
				t.Fatalf("watchdog: %v\n%s", err, out)
			}
			_, statErr := os.Stat(d)
			switch {
			case c.down && (!os.IsNotExist(statErr) || !strings.Contains(out, "stopped and removed "+d)):
				t.Errorf("the watchdog left %s (%v) after %q:\n%s", d, statErr, c.answers, out)
			case !c.down && (statErr != nil || strings.Contains(out, "taking the yard down") || !strings.Contains(out, "no yard at "+d)):
				t.Errorf("the watchdog took %s down (%v) after %q, want it to look again:\n%s", d, statErr, c.answers, out)
			}
		})
	}
}

// yardr-c3nm: a down on a loaded machine is slow, not stuck, and a wait that
// gave up on the clock failed the watchdog tests in the loads that made it
// slow. The wait gives up when what it watches (the shells of a down) has not
// changed for the fuse in looks, however long it has run and however long the
// looks have taken (yardr-ka44). The watch here is scripted, one answer a
// look, and the looks are a millisecond apart.
func TestWaitUnchangedGivesUpOnlyWhenWhatItWatchesStandsStill(t *testing.T) {
	t.Parallel()
	const fuse, look = 200 * time.Millisecond, time.Millisecond
	for _, c := range []struct {
		name string
		// watch is the n-th answer of the watch; the wait is met when it has
		// run twice the fuse as the machine scales it, if met is set.
		watch func(n int) []int
		met   bool
	}{
		{"nothing to watch", func(int) []int { return nil }, false},
		{"what it watches stands still", func(int) []int { return []int{7} }, false},
		{"what it watches changes at every look", func(n int) []int { return []int{n} }, true},
		// A stand-still of four looks in every five, a few milliseconds: a
		// down in one long command, as the listing of a loaded machine is.
		// The wait that counted from its start would give up at the first.
		{"it stands still for less than the fuse between changes", func(n int) []int { return []int{n / 5} }, true},
		// The same, with each look as long as a loaded machine makes it: a few
		// looks that together take longer than the fuse. The wait that timed
		// the stand-still gave up at the second look that found no change
		// (yardr-ka44).
		{"it stands still for a few looks that take longer than the fuse", func(n int) []int {
			time.Sleep(testload.Scale(fuse) / 2)
			return []int{n / 5}
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var n int
			start := time.Now()
			got := waitUnchanged(fuse, look, func() []int { n++; return c.watch(n) }, func() bool {
				return c.met && time.Since(start) > 2*testload.Scale(fuse)
			})
			took := time.Since(start)
			if got != c.met {
				t.Errorf("waitUnchanged = %v after %v, want %v", got, took, c.met)
			}
			// The fuse is at least what the test wrote, however the machine
			// scales it: the wait that gives up never does so before it, and
			// the one that is met has outlasted it.
			if !c.met && took < fuse || c.met && took <= fuse {
				t.Errorf("waitUnchanged = %v after %v, which the fuse of %v does not allow", got, took, fuse)
			}
		})
	}
}

// A change in load after a wait is armed changes the next wait, not this one.
func TestWaitUnchangedKeepsItsFuseWhenLoadFalls(t *testing.T) {
	t.Setenv(config.Key(testload.Env), "1")
	t.Setenv(config.Key("TEST_LOADAVG"), "40")
	t.Setenv(config.Key("TEST_CORES"), "10")
	const fuse = 50 * time.Millisecond
	start := time.Now()
	looks := 0
	got := waitUnchanged(fuse, 5*time.Millisecond, func() []int {
		looks++
		if looks == 2 {
			t.Setenv(config.Key("TEST_LOADAVG"), "10")
		}
		return nil
	}, func() bool { return time.Since(start) > 2*fuse })
	if !got {
		t.Fatalf("waitUnchanged gave up after load fell, before its armed fuse of %v", 4*fuse)
	}
}

// yardr-c3nm: downsOf finds the shells that run down for one yard by the end of
// their command line, and neither the downs of another yard nor a process that
// only names this one.
func TestDownsOfFindsTheShellsRunningDownForOneYard(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nread line\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := fmt.Sprintf("/tmp/iy.downsof.%d.mine", os.Getpid())
	start := func(verb, d string) int {
		cmd := exec.Command("/bin/sh", path, verb, d)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stdin.Close(); cmd.Wait() })
		return cmd.Process.Pid
	}
	down := start("down", mine)
	start("down", mine+"x")
	start("watchdog", mine)
	if got := downsOf(t, mine); !slices.Equal(got, []int{down}) {
		t.Errorf("downsOf(%s) = %v, want only the down for it, pid %d", mine, got, down)
	}
}

// yardr-c3nm: the first run of a file written a moment ago waits behind the
// machine's other first runs, seconds beside a busy build, and the watchdog of
// a yard made for an owner that is gone runs alive at once and yardr in its
// down, inside the time a test gives it. So bare runs each once before it
// returns, where nothing times it. go is a stand-in here, and the programs it
// makes log their runs: no build and no first run to time, only the runs.
func TestBareRunsWhatItsWatchdogStartsOnce(t *testing.T) {
	t.Parallel()
	bin, ran := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	const goStub = `#!/bin/sh
[ "$1" = build ] && [ "$2" = -o ] || exit 2
printf '#!/bin/sh\necho "%s $*" >>"%s"\n' "$(basename "$3")" "$ISOLATED_TEST_LOG" >"$3"
chmod +x "$3"
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goStub), 0o755); err != nil {
		t.Fatal(err)
	}
	// No name for a test binary, so the yard's watchdog has nothing to watch
	// and runs nothing: what the log holds when bare returns is bare's own.
	bareEnv(t, []string{"PATH=" + bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "ISOLATED_TEST_LOG=" + ran})
	raw, err := os.ReadFile(ran)
	if err != nil {
		t.Fatalf("bare ran none of the programs it made: %v", err)
	}
	var runs []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			runs = append(runs, line)
		}
	}
	for _, want := range []string{"alive", "yardr version"} {
		if n := slices.Index(runs, want); n < 0 {
			t.Errorf("bare returned without running %q once; it ran %q", want, runs)
		}
	}
	if slices.Contains(runs, "claude") {
		t.Errorf("bare started the stand-in agent: %q", runs)
	}
}
