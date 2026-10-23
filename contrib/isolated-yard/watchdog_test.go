package isolatedyard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

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

// watchdogsOf are the pids of the watchdogs started for the yard d, found by
// their command line: the script's watchdog command for d, behind D/e until
// the wrapper has executed it. What the watchdog has forked and not yet
// replaced (the program it runs each second, until the system lets it start)
// has the same command line, so a match whose parent is a match is no
// watchdog of its own.
func watchdogsOf(t *testing.T, d string) []int {
	t.Helper()
	out, err := exec.Command("ps", "axww", "-o", "pid=", "-o", "ppid=", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	parent := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[len(f)-2] != "watchdog" || f[len(f)-1] != d {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			parent[pid] = ppid
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
	// another owner: one that has ended. Bound this command as well as the
	// background watches, so a broken watch cannot hang the package test.
	owner := deadOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), testload.Scale(ownerGoneFuse))
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(d, "e"), "env", config.Key(config.TestOwnerEnv)+"="+owner, scriptPath(t), "watchdog", d)
	raw, err := cmd.CombinedOutput()
	out = string(raw)
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
// made for an owner that has ended is taken down within a look or two of its
// making, and nothing of it is left.
func TestWatchdogOfAYardMadeForAGoneOwnerTakesItDown(t *testing.T) {
	t.Parallel()
	d := bareEnv(t, withOwner(os.Environ(), deadOwner(t)))
	if !waitFor(ownerGoneFuse, func() bool {
		_, err := os.Stat(d)
		return os.IsNotExist(err)
	}) {
		t.Fatalf("%s was not taken down by its watchdog", d)
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
			ctx, cancel := context.WithTimeout(context.Background(), testload.Scale(watchdogRunFuse))
			defer cancel()
			cmd := exec.CommandContext(ctx, scriptPath(t), "watchdog", d)
			cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", config.Key(config.TestOwnerEnv) + "=1 1"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("watchdog: %v\n%s", err, out)
			}
			_, statErr := os.Stat(d)
			switch {
			case c.down && (!os.IsNotExist(statErr) || !strings.Contains(string(out), "stopped and removed "+d)):
				t.Errorf("the watchdog left %s (%v) after %q:\n%s", d, statErr, c.answers, out)
			case !c.down && (statErr != nil || strings.Contains(string(out), "taking the yard down") || !strings.Contains(string(out), "no yard at "+d)):
				t.Errorf("the watchdog took %s down (%v) after %q, want it to look again:\n%s", d, statErr, c.answers, out)
			}
		})
	}
}
