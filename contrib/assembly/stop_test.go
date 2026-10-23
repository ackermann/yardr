package assembly

// What an assembly script leaves behind when its session ends. The daemon
// stops a script as soon as it has reported, by signalling its process group
// with TERM, and a session can be stopped before that: what the script
// started (its test command and everything below it) and what it made (its
// temporary files and directory) must be gone by then, and when it is
// stopped. Three things pin it:
//
//   - reportWatch, the yard's own store looking at what the script made at each
//     report, so that every run of every script in this package is held to
//     "nothing of the run is left at bead done" (yard.exec);
//   - the TERM tests of merge-local and integrate-train (watcher_test.go,
//     integrate_train_watcher_test.go), which hold their test command's child
//     to dying with the script, and the removal to surviving a second stop;
//   - the TERM test of the scripts that have a directory or a file to take
//     away and nothing else to stop, run under dash (below).
//
// A second stop is the daemon's: it signals a session's process group on every
// pass until the session is gone, so a script taking its files away is sent
// TERM again, and so is the rm it has started, which dies of it unless the
// script ignored TERM first (gateRm).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// reportWatches is the report watch of each yard whose store this process
// serves, by the yard's home (serveYard).
var reportWatches sync.Map // string -> *reportWatch

// reportWatch looks at what a script has made whenever the script ends its
// session with a call to the store: bead done, advance, hold or close. It is
// the store of the yard that sees the call, as the daemon would, and not a
// yardr of the script's own that records it, which would cost a process for
// every yardr call of every run.
type reportWatch struct {
	mu     sync.Mutex
	record string   // what the script now running made (recordMktemp), none between runs
	left   []string // what was still there at each report that found something
}

// handler is next, noting the reports that come to it.
func (w *reportWatch) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if method, ok := strings.CutPrefix(r.URL.Path, "/call/"); ok {
			switch method {
			case "Done", "AdvanceNote", "CloseBead", "SetHold":
				w.saw(method)
			}
		}
		next.ServeHTTP(rw, r)
	})
}

// arm has the watch look at what record says a script made, from now on.
func (w *reportWatch) arm(record string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.record, w.left = record, nil
}

// disarm stops the watch and returns what it found at the script's reports.
func (w *reportWatch) disarm() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.record = ""
	return w.left
}

func (w *reportWatch) saw(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.record == "" {
		return
	}
	if still := madeStill(w.record); len(still) > 0 {
		w.left = append(w.left, fmt.Sprintf("%s with %v still there", call, still))
	}
}

// mktempRecorder is the script every recordMktemp links to, written once by
// TestMain. A script written for each run costs about 240 ms the first time macOS
// executes it, and a link to one that has run costs nothing: the first version of
// the watch wrote one per run, and doubled the wall time of the yard tests.
var mktempRecorder string

// writeMktempRecorder writes the mktemp that notes each path it makes in the
// file made beside the directory it is found in: ${0%/*} is that directory
// whichever link it was reached by.
func writeMktempRecorder(path string) error {
	real, err := exec.LookPath("mktemp")
	if err != nil {
		return err
	}
	script := "#!/bin/sh\nf=$(" + shellQuote(real) + " \"$@\") || exit\nprintf '%s\\n' \"$f\" >>\"${0%/*}/../made\"\nprintf '%s\\n' \"$f\"\n"
	return os.WriteFile(path, []byte(script), 0o755)
}

// recordMktemp puts a mktemp into bin, which the script's PATH has first, that
// notes in a file each path it makes, and returns the file (bin's neighbour
// made). A script's temporary files cannot be found by giving it a TMPDIR of its
// own: mktemp with no template, which most of the scripts use, ignores TMPDIR on
// macOS and asks the system for the user's temporary directory. What it was
// handed can be asked after.
func recordMktemp(t *testing.T, bin string) string {
	t.Helper()
	if err := os.Symlink(mktempRecorder, filepath.Join(bin, "mktemp")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(bin), "made")
}

// madeStill is what the paths in record, a recordMktemp file, name that is
// still there.
func madeStill(record string) []string {
	b, err := os.ReadFile(record)
	if err != nil {
		return nil
	}
	var still []string
	for _, path := range strings.Split(string(b), "\n") {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			still = append(still, path)
		}
	}
	return still
}

// assertNothingLeft fails unless everything the script made, as record says, is gone.
func assertNothingLeft(t *testing.T, record string) {
	t.Helper()
	if still := madeStill(record); len(still) > 0 {
		t.Errorf("the script left %v behind", still)
	}
}

// touch makes file, empty.
func touch(t *testing.T, file string) {
	t.Helper()
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// awaitFile waits for file to exist. The one-minute deadline is a fuse against
// a script that never gets there, not a window the fixture must fit in.
func awaitFile(t *testing.T, file, what string) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(file); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s never appeared", what, filepath.Base(file))
		}
	}
}

// gateRm puts an rm into bin, which the script's PATH has first, that stands
// still once the test has made the file arm: it makes held, waits for the file
// release, and only then removes what it was asked to. It holds a script inside
// the removal of its files, where the daemon's second stop finds it.
func gateRm(t *testing.T, bin string) (arm, held, release string) {
	t.Helper()
	rm, err := exec.LookPath("rm")
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(bin)
	arm, held, release = filepath.Join(dir, "rm-arm"), filepath.Join(dir, "rm-held"), filepath.Join(dir, "rm-release")
	wrapper := "#!/bin/sh\n" +
		"if [ -e " + shellQuote(arm) + " ] && [ ! -e " + shellQuote(release) + " ]; then\n" +
		"  : >" + shellQuote(held) + "\n" +
		"  while [ ! -e " + shellQuote(release) + " ]; do " + shellQuote(sleep) + " 0.05; done\n" +
		"fi\n" +
		"exec " + shellQuote(rm) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "rm"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return arm, held, release
}

// awaitTestChild waits for the test command to write the pid of its child to
// file and returns it. The command starts a moment after the watcher's sleep
// does, so a test that signals the script as soon as it sees that sleep could
// signal it before the child exists, and have nothing to look for after. The
// one-minute deadline is a fuse against a command that never starts, not a
// window the fixture must fit in.
func awaitTestChild(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test command never wrote its child's pid to %s", file)
		}
	}
}

// assertGone fails unless process pid is gone within the wait: a killed
// process takes a moment to go, one nothing killed never does.
func assertGone(t *testing.T, pid int, what string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s (pid %d) outlived the script", what, pid)
			return
		}
	}
}

// merge-pr, pr-merge and pr-train make one thing each to take away, a
// temporary directory or file, and have no test command to stop. Their EXIT
// trap removes it, but dash runs no EXIT trap for a shell that a signal kills,
// so a script with no trap for TERM left it behind wherever sh is dash
// (yardr-nz7e): the daemon stops a session with TERM. The scripts are run by
// dash itself where there is one, so that a machine whose sh is not dash sees
// it too, each with a yardr that blocks at its first call, after the script has
// made what it makes; the process group is signalled, as the daemon does. TERM
// is deferred until the foreground command that the script waits for is over,
// so the yardr that blocks dies of the same signal. The removal is held at its
// rm while the group is sent TERM a second time, as the daemon sends it again,
// and the rm, which is in the group, must go on: a script that ignores TERM
// across the removal passes the ignoring on to the rm it starts.
func TestPRScriptsKilledWithTermLeaveNoTempBehind(t *testing.T) {
	t.Parallel()
	shell := "sh"
	if dash, err := exec.LookPath("dash"); err == nil {
		shell = dash
	}
	for _, script := range []string{"merge-pr", "pr-merge", "pr-train"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			w := newWatcherRun(t, "true", "1799", 0)
			bin := filepath.Join(filepath.Dir(w.record), "bin")
			record := recordMktemp(t, bin)
			arm, held, release := gateRm(t, bin)
			// merge-pr looks for gh and jq before it asks anything of them, and
			// a machine need not have them.
			for _, tool := range []string{"gh", "jq"} {
				if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			path, err := filepath.Abs(script)
			if err != nil {
				t.Fatal(err)
			}
			// An input of the test cache, as merge-local is there.
			if _, err := os.ReadFile(path); err != nil {
				t.Fatal(err)
			}
			blocked := filepath.Join(filepath.Dir(w.record), "blocked")
			stub := "#!/bin/sh\nif [ ! -e " + shellQuote(blocked) + " ]; then : >" + shellQuote(blocked) + "; exec sleep 300; fi\ncat >/dev/null 2>&1\nexit 0\n"
			if err := os.WriteFile(filepath.Join(filepath.Dir(w.record), "stub"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(filepath.Dir(w.record), "out"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(shell, path)
			cmd.Dir, cmd.Stdout, cmd.Stderr = w.tmp, log, log
			cmd.Env = append(w.env, "YARDR_PR=1")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group := cmd.Process.Pid
			t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
			awaitFile(t, blocked, script+" never made its first yardr call")
			// The probe means something only if there is something to leave.
			if len(madeStill(record)) == 0 {
				out, _ := os.ReadFile(log.Name())
				t.Fatalf("%s had made nothing by its first yardr call: the stop proves nothing:\n%s", script, out)
			}
			touch(t, arm)
			if err := syscall.Kill(-group, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			awaitFile(t, held, script+" never began to remove what it made")
			if err := syscall.Kill(-group, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			touch(t, release)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				code := 0
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitCode()
				} else if err != nil {
					t.Fatalf("waiting for %s: %v", script, err)
				}
				if code != 143 {
					t.Errorf("%s run by %s and killed with TERM exits %d, want 143", script, shell, code)
				}
			case <-time.After(time.Minute):
				t.Fatalf("%s did not end when its process group was sent TERM", script)
			}
			assertNothingLeft(t, record)
		})
	}
}

// merge-local and integrate-train make the files of a test run (its output,
// the watcher's, the deadline's, the copy of the base's test script) before
// they read that script, and hold the bead if git cannot show it. The hold is
// the one place their files are still there when they report, and the daemon
// stops them as soon as they have: a yardr that looks at what the script made
// when it is told to hold the bead finds all four gone. The git that cannot
// show stands in for a base that changed under the run.
func TestScriptsTakeAwayTheFilesOfTheTestsBeforeTheyHold(t *testing.T) {
	t.Parallel()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		script string
		run    func(t *testing.T, test, limit string, hi int) *watcherRun
	}{
		{"merge-local", newWatcherRun},
		{"integrate-train", newIntegrateTrainWatcherRun},
	} {
		t.Run(tc.script, func(t *testing.T) {
			t.Parallel()
			w := tc.run(t, "check.sh", "1799", 0)
			dir := filepath.Dir(w.record)
			bin := filepath.Join(dir, "bin")
			record := recordMktemp(t, bin)
			// The base has the test script, so the run is of its copy.
			if err := os.WriteFile(filepath.Join(w.repo, "check.sh"), []byte("exit 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			watcherGit(t, w.repo, "add", "check.sh")
			watcherGit(t, w.repo, "commit", "-q", "-m", "the test script")
			git := "#!/bin/sh\nif [ \"$1\" = -C ] && [ \"$3\" = show ]; then echo 'fatal: bad object' >&2; exit 128; fi\nexec " + shellQuote(realGit) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
				t.Fatal(err)
			}
			// The yardr says what of the script's files is still there when it is
			// told to hold the bead, and that it was.
			seen := filepath.Join(dir, "seen")
			stub := "#!/bin/sh\ncat >/dev/null 2>&1\n" +
				"if [ \"$1 $2\" = 'bead hold' ]; then\n" +
				"  echo held >>" + shellQuote(seen) + "\n" +
				"  while read -r made; do [ ! -e \"$made\" ] || echo \"still there: $made\" >>" + shellQuote(seen) + "; done <" + shellQuote(record) + "\n" +
				"fi\nexit 0\n"
			if err := os.WriteFile(filepath.Join(dir, "stub"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(dir, "out"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := w.cmd()
			cmd.Stdout, cmd.Stderr = log, log
			err = cmd.Run()
			out, _ := os.ReadFile(log.Name())
			if err != nil {
				t.Fatalf("%s: %v\n%s", tc.script, err, out)
			}
			if want := "cannot read check.sh from main"; !strings.Contains(string(out), want) {
				t.Fatalf("%s did not hold for lack of the base's copy of check.sh (no %q):\n%s", tc.script, want, out)
			}
			if b, err := os.ReadFile(seen); err != nil || strings.TrimSpace(string(b)) != "held" {
				t.Errorf("%s told the yard to hold the bead with its files still there, or not at all: %q (%v)", tc.script, b, err)
			}
			assertNothingLeft(t, record)
		})
	}
}
