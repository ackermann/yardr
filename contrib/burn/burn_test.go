// Package burn tests contrib/burn/burn, which runs a Go package's tests over
// and over on a machine kept busy.
package burn

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/testload"
)

func TestMain(m *testing.M) {
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}

// fuse is how long a test lets burn, or what it waits for, take on an idle
// box; testload.Scale sizes it for a loaded gate leg. It is against a hang,
// never a window an effect has to fit in.
const fuse = 2 * time.Minute

// rig is what burn runs in: stand-ins first on its PATH for go, which builds
// a test binary that is a stand-in too, and for yes, which keeps no cpu busy
// but records its pid and nice level and stays until it is killed. Everything
// they record is a file of the rig's dir, which they find in $RIG. A test
// about the levels burn reads and gives has stand-ins for ps and nice as well
// (psSays, niceRecords).
type rig struct {
	t   *testing.T
	dir string
	// cwd is where burn runs, "" for this package's directory, and env more
	// of its environment: for the test whose go is the machine's own (goModule).
	cwd string
	env []string
}

// testBin is what the stand-in test binary does, with the name of the test it
// is asked to run in $name, or "package" where it is asked to run none. It
// waits until every yes-loop of the run has recorded itself, since burn starts
// them and does not wait for them, then does what the rig's files for the name
// say: block, fail on the runs listed, skip, run no test, or pass.
const testBin = `
case "$1" in
-test.run=*) name=${1#-test.run=^}; name=${name%\$} ;;
*) name=package ;;
esac
while [ "$(( $(wc -l < "$RIG/spinners" 2>/dev/null || echo 0) ))" -lt "$SPINNERS" ]; do sleep 0.01; done
n=$(( $(cat "$RIG/ran-$name" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$RIG/ran-$name"
pwd > "$RIG/cwd"
ps -o nice= -p $$ > "$RIG/test-nice"
if [ -f "$RIG/block-$name" ]; then
  echo $$ > "$RIG/child"
  exec sleep 600
fi
if grep -qx "$n" "$RIG/fail-$name" 2>/dev/null; then
  echo "=== RUN   $name"
  echo "    x_test.go:12: run $n went wrong"
  echo "--- FAIL: $name (0.00s)"
  echo FAIL
  exit 1
fi
if [ -f "$RIG/skip-$name" ]; then
  echo "--- SKIP: $name (0.00s)"
  echo PASS
  exit 0
fi
if [ -f "$RIG/none-$name" ]; then
  echo "testing: warning: no tests to run"
  echo PASS
  exit 0
fi
echo "--- PASS: $name (0.00s)"
echo PASS
`

// goStandIn answers the two calls burn makes: where the package is, and a
// test binary for it, which is a link to the launcher of testbin beside a copy
// of its body, as scripttest makes a stand-in (a new executable file is slow
// to start on macOS).
const goStandIn = `
case "$1" in
list) echo "$RIG/pkg" ;;
test) ln -s "$(readlink "$RIG/testbin")" "$4" && cp "$RIG/testbin.sh" "$4.sh" ;;
*) echo "go: unexpected $*" >&2; exit 1 ;;
esac
`

const yesStandIn = `
echo $$ >> "$RIG/spinners"
ps -o nice= -p $$ >> "$RIG/spinner-nice"
exec sleep 600
`

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir()}
	for _, d := range []string{"bin", "pkg", "tmp"} {
		if err := os.Mkdir(filepath.Join(r.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scripttest.Write(t, filepath.Join(r.dir, "bin", "go"), goStandIn)
	scripttest.Write(t, filepath.Join(r.dir, "bin", "yes"), yesStandIn)
	scripttest.Write(t, filepath.Join(r.dir, "testbin"), testBin)
	// A yes-loop that outlives a test that failed is no one's to find: kill
	// what the rig recorded.
	t.Cleanup(func() {
		for _, pid := range r.pids("spinners") {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		for _, pid := range r.pids("child") {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return r
}

// set writes a file of the rig's: what the stand-in test binary reads.
func (r *rig) set(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// goModule makes the rig's go the machine's own, building in a module of the
// rig's with files (name to text), for the test whose subject is what a real
// build carries, which no stand-in can say. The stand-in go gets a new body
// that runs the real one, so the rest of the rig stays as it is. The build
// cache is the machine's, so that the standard library is not built again for
// each test. GOFLAGS is the gate's, whatever the caller exported, so that a
// caller's -race cannot turn the detector on for a run that asks for none.
func (r *rig) goModule(files map[string]string) {
	r.t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		r.t.Fatal(err)
	}
	cache, err := exec.Command(goBin, "env", "GOCACHE").Output()
	if err != nil {
		r.t.Fatal(err)
	}
	scripttest.Write(r.t, filepath.Join(r.dir, "bin", "go"), "exec \"$REAL_GO\" \"$@\"\n")
	r.cwd = filepath.Join(r.dir, "mod")
	if err := os.Mkdir(r.cwd, 0o755); err != nil {
		r.t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(r.cwd, name), []byte(text), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	// The real go finds its compiler on the caller's PATH, wherever that is;
	// the stand-ins stay first.
	r.env = append(r.env, "REAL_GO="+goBin, "GOCACHE="+strings.TrimSpace(string(cache)),
		"GOFLAGS=-trimpath", "GOTOOLCHAIN=local",
		"PATH="+filepath.Join(r.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// psSays makes burn take level for the nice level it runs at: the stand-in ps
// answers every question with it, padded as the real one pads its answer.
func (r *rig) psSays(level string) {
	r.t.Helper()
	scripttest.Write(r.t, filepath.Join(r.dir, "bin", "ps"), "echo '  "+level+"'\n")
}

// niceRecords makes nice write down the increment it is asked for, a line of
// nice-calls for each, and run the command without changing its level: what
// burn gives nice is then read from the file, and no machine's own levels, or
// its limits on them, come into it.
func (r *rig) niceRecords() {
	r.t.Helper()
	scripttest.Write(r.t, filepath.Join(r.dir, "bin", "nice"), `
[ "$1" = -n ] || { echo "nice: unexpected $*" >&2; exit 1; }
echo "$2" >> "$RIG/nice-calls"
shift 2
exec "$@"
`)
}

func (r *rig) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// pids are the process ids a stand-in recorded in file, one to a line.
func (r *rig) pids(file string) []int {
	var out []int
	for _, f := range strings.Fields(r.read(file)) {
		if pid, err := strconv.Atoi(f); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// burn starts the script on the rig with args, and the factor of yes-loops
// the stand-in test binary waits for.
func (r *rig) burn(spinners int, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	r.t.Helper()
	script, err := filepath.Abs("burn")
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Dir = r.cwd
	cmd.Env = append([]string{
		"PATH=" + filepath.Join(r.dir, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + r.dir,
		"TMPDIR=" + filepath.Join(r.dir, "tmp"),
		"RIG=" + r.dir,
		"SPINNERS=" + strconv.Itoa(spinners),
	}, r.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	return cmd, &stdout, &stderr
}

// finish waits for the script to end and answers its exit status.
func finish(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	within := testload.Scale(fuse)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &exit):
			return exit.ExitCode()
		}
		t.Fatal(err)
	case <-time.After(within):
		cmd.Process.Kill()
		t.Fatalf("burn did not end within %s", within)
	}
	return -1
}

// gone says whether no process has the id pid any more.
func gone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

// psNice is the nice level ps says pid runs at: what burn reads of itself, and
// the stand-ins record of themselves.
func psNice(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o nice= -p %d: %v", pid, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps -o nice= -p %d said %q", pid, out)
	}
	return n
}

// waitFor polls until ok, for a fuse.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	within := testload.Scale(fuse)
	for end := time.Now().Add(within); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: not within %s", what, within)
		}
	}
}

// leftover is what burn's work directory left in the rig's TMPDIR.
func (r *rig) leftover() []string {
	r.t.Helper()
	ents, err := os.ReadDir(filepath.Join(r.dir, "tmp"))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// Every test is run as many times as asked, in the package's directory, and
// a run that fails is counted and shown, with the lines that say why. The
// loops are FACTOR times the cpus it was told, at the nice level it was told
// beside the tests, and all gone, with burn's work directory, at the end. That
// level is the one told, unless this process is nicer than it already, as a
// yard session is and the gate may be: burn starts at this process's level,
// which nothing can lower, and the loops and the tests stay at it. The real
// ps and nice are used here, since it is what an operator sees.
func TestBurnCountsTheRunsThatPass(t *testing.T) {
	t.Parallel()
	level := max(7, psNice(t, os.Getpid()))
	r := newRig(t)
	r.set("fail-TestA", "2\n4\n")
	cmd, stdout, stderr := r.burn(6, "-n", "5", "-x", "3", "-c", "2", "-N", "7", "./pkg", "TestA", "TestB")
	if code := finish(t, cmd); code != 1 {
		t.Fatalf("exit status = %d, want 1: a run failed\n%s", code, stderr)
	}
	if got, want := stdout.String(), "TestA: 3/5 passed\nTestB: 5/5 passed\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	for _, want := range []string{
		fmt.Sprintf("6 yes-loops (3 x 2 cpus) at nice %d,", level), // the comma, so that 7 does not match 70
		"burn: TestA, run 2 failed (exit 1):",
		"x_test.go:12: run 2 went wrong",
		"burn: TestA, run 4 failed (exit 1):",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr.String(), "run 1 failed") || strings.Contains(stderr.String(), "TestB, run") {
		t.Errorf("stderr names a run that passed:\n%s", stderr)
	}
	if got, want := r.read("cwd"), filepath.Join(r.dir, "pkg"); got != want {
		t.Errorf("the tests ran in %q, want the package's directory %q", got, want)
	}
	if got, want := strings.Fields(r.read("spinner-nice")), slices.Repeat([]string{strconv.Itoa(level)}, 6); !slices.Equal(got, want) {
		t.Errorf("the loops' nice levels = %v, want six of %d", got, level)
	}
	if got, want := r.read("test-nice"), strconv.Itoa(level); got != want {
		t.Errorf("the tests' nice level = %q, want the loops' %s", got, want)
	}
	spinners := r.pids("spinners")
	if len(spinners) != 6 {
		t.Fatalf("%d loops recorded, want 3 x 2", len(spinners))
	}
	for _, pid := range spinners {
		if !gone(pid) {
			t.Errorf("loop %d is still running after burn ended", pid)
		}
	}
	if left := r.leftover(); len(left) != 0 {
		t.Errorf("burn left %v in its TMPDIR", left)
	}
	// The real ps and nice above prove the visible level on this machine. These
	// rows hold the cases this machine cannot necessarily supply: nice(1) adds
	// what it is given to the level it starts at, and a process cannot lower
	// its own, so burn gives nice the difference when it needs to and runs no
	// nice where its own level is already the greater. The stand-ins make each
	// row hold regardless of the gate's own nice level.
	for _, tc := range []struct {
		name  string
		ask   string // -N
		own   string // the level ps says burn runs at
		level string // where the loops and the tests end up: the greater of ask and own
		more  string // what nice is given, by the loop and by each run of the test; "" where none is run
	}{
		{"burn is less nice than asked", "7", "3", "7", "4"},
		{"burn is as nice as asked", "7", "7", "7", ""},
		{"burn is nicer than asked", "7", "12", "12", ""},
		{"burn is below 0, as root can be", "7", "-5", "7", "12"},
		// The shell would read 010 as octal, as nice does not.
		{"-N has leading zeros", "010", "3", "10", "7"},
	} {
		t.Run("nice level/"+tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.psSays(tc.own)
			r.niceRecords()
			cmd, stdout, stderr := r.burn(1, "-n", "2", "-x", "1", "-c", "1", "-N", tc.ask, "./pkg", "TestA")
			if code := finish(t, cmd); code != 0 {
				t.Fatalf("exit status = %d, want 0\n%s", code, stderr)
			}
			if got, want := stdout.String(), "TestA: 2/2 passed\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if want := "1 yes-loops (1 x 1 cpus) at nice " + tc.level + ","; !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			var want []string // the loop, then the two runs of the test
			if tc.more != "" {
				want = slices.Repeat([]string{tc.more}, 3)
			}
			if got := strings.Fields(r.read("nice-calls")); !slices.Equal(got, want) {
				t.Errorf("nice was given %v, want %v", got, want)
			}
		})
	}
}

// A leading 0 is decimal where burn's checks read it but octal where the
// shell's arithmetic does, so -x and -c are read as the decimal numbers the
// checks take them for: -x 08 starts 8 loops, not an error, and -c 010 counts
// 10 cpus, not 8. The banner prints the normalised values.
func TestBurnReadsFactorAndCpusAsDecimal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		x, c  string
		loops int
	}{
		{"-x 08 starts 8 loops", "08", "1", 8},
		{"-c 010 counts 10 cpus", "1", "010", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			cmd, stdout, stderr := r.burn(tc.loops, "-n", "1", "-x", tc.x, "-c", tc.c, "./pkg", "TestA")
			if code := finish(t, cmd); code != 0 {
				t.Fatalf("exit status = %d, want 0\n%s", code, stderr)
			}
			if got, want := stdout.String(), "TestA: 1/1 passed\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			x, err := strconv.Atoi(tc.x)
			if err != nil {
				t.Fatal(err)
			}
			c, err := strconv.Atoi(tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("%d yes-loops (%d x %d cpus)", tc.loops, x, c); !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			if got := len(r.pids("spinners")); got != tc.loops {
				t.Errorf("%d loops recorded, want %d", got, tc.loops)
			}
		})
	}
}

// With -k DIR burn keeps the whole output of every failing run in
// DIR/<label>-run-N.out, the directory made when missing, and each shown
// failure names its file. The kept files are the data: every failing run is
// saved, not only the five shown, and a passing run's output is not. Without
// -k nothing is written.
func TestBurnKeepsFailingRunsWithK(t *testing.T) {
	t.Parallel()
	t.Run("named runs", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.set("fail-TestA", "1\n2\n3\n4\n5\n6\n7\n")
		keep := filepath.Join(r.dir, "missing", "kept")
		cmd, stdout, stderr := r.burn(1, "-n", "7", "-x", "1", "-c", "1", "-k", keep, "./pkg", "TestA")
		if code := finish(t, cmd); code != 1 {
			t.Fatalf("exit status = %d, want 1: a run failed\n%s", code, stderr)
		}
		if got, want := stdout.String(), "TestA: 0/7 passed\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		for n := 1; n <= 7; n++ {
			path := filepath.Join(keep, fmt.Sprintf("TestA-run-%d.out", n))
			b, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("run %d: no kept output in %s: %v", n, path, err)
				continue
			}
			for _, want := range []string{
				"=== RUN   TestA",                   // dropped from the screenful
				fmt.Sprintf("run %d went wrong", n), // kept in the screenful
				"--- FAIL: TestA (0.00s)",
			} {
				if !strings.Contains(string(b), want) {
					t.Errorf("run %d: kept output lacks %q:\n%s", n, want, b)
				}
			}
		}
		if !strings.Contains(stderr.String(), filepath.Join(keep, "TestA-run-1.out")) {
			t.Errorf("stderr does not name the kept file:\n%s", stderr)
		}
		if strings.Contains(stderr.String(), "run 6 failed") {
			t.Errorf("stderr shows more than the first five failures:\n%s", stderr)
		}
		if !strings.Contains(stderr.String(), "2 more failed runs, not shown") {
			t.Errorf("stderr lacks the unshown count:\n%s", stderr)
		}
	})
	t.Run("whole package", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.set("fail-package", "2\n")
		keep := filepath.Join(r.dir, "kept")
		cmd, stdout, stderr := r.burn(1, "-n", "2", "-x", "1", "-c", "1", "-k", keep, "./pkg")
		if code := finish(t, cmd); code != 1 {
			t.Fatalf("exit status = %d, want 1: a run failed\n%s", code, stderr)
		}
		if got, want := stdout.String(), "./pkg: 1/2 passed\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		// The label is the package path; every character outside
		// [A-Za-z0-9._-] is _ in the file name.
		path := filepath.Join(keep, "._pkg-run-2.out")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("no kept output in %s: %v", path, err)
		}
		for _, want := range []string{"=== RUN   package", "run 2 went wrong", "--- FAIL: package (0.00s)"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("kept output lacks %q:\n%s", want, b)
			}
		}
		if !strings.Contains(stderr.String(), path) {
			t.Errorf("stderr does not name the kept file:\n%s", stderr)
		}
		if _, err := os.Stat(filepath.Join(keep, "._pkg-run-1.out")); !os.IsNotExist(err) {
			t.Errorf("the passing run's output was kept: %v", err)
		}
	})
	t.Run("without -k nothing is written", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.set("fail-TestA", "1\n")
		cmd, _, stderr := r.burn(1, "-n", "2", "-x", "1", "-c", "1", "./pkg", "TestA")
		if code := finish(t, cmd); code != 1 {
			t.Fatalf("exit status = %d, want 1: a run failed\n%s", code, stderr)
		}
		var kept []string
		err := filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".out") {
				kept = append(kept, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 0 {
			t.Errorf("without -k burn wrote %v", kept)
		}
		if strings.Contains(stderr.String(), "kept in") {
			t.Errorf("stderr names a kept file without -k:\n%s", stderr)
		}
	})
}

// A run that exits 0 without the test's own PASS line is no pass: go test
// calls a name that matches no test, and a test that skips, ok.
func TestBurnCountsOnlyARealPass(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"none", "skip"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.set(mode+"-TestA", "")
			cmd, stdout, stderr := r.burn(1, "-n", "3", "-x", "1", "-c", "1", "./pkg", "TestA")
			if code := finish(t, cmd); code != 1 {
				t.Fatalf("exit status = %d, want 1\n%s", code, stderr)
			}
			if got, want := stdout.String(), "TestA: 0/3 passed\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
		})
	}
}

// With no test named the package's whole run is what is burned: it passes when
// the binary exits 0 and ends in PASS, and is counted under the package.
func TestBurnRunsThePackageWhenNoTestIsNamed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.set("fail-package", "2\n")
	cmd, stdout, stderr := r.burn(1, "-n", "3", "-x", "1", "-c", "1", "./pkg")
	if code := finish(t, cmd); code != 1 {
		t.Fatalf("exit status = %d, want 1: a run failed\n%s", code, stderr)
	}
	if got, want := stdout.String(), "./pkg: 2/3 passed\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "burn: ./pkg, run 2 failed (exit 1):") {
		t.Errorf("stderr lacks the failed run:\n%s", stderr)
	}
	if got := r.read("ran-package"); got != "3" {
		t.Errorf("the package was run %q times, want 3", got)
	}
}

// A data race shows in a burn only when its test binary carries the race
// detector, which -r builds in: without it a test that races passes however
// often it runs, and with it the run fails and says why, whatever GOFLAGS the
// caller had. The package is a real one, built by the real go, since the
// stand-ins cannot tell whether a binary has the detector: what a plain build
// lacks is the bug this pins.
func TestBurnRBuildsTheTestsWithTheRaceDetector(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		args   []string
		env    []string // more of burn's environment
		code   int
		stdout string
		stderr []string
	}{
		{"a plain build has no detector", nil, nil, 0, "TestRace: 1/1 passed\n", nil},
		{"-r builds it in", []string{"-r"}, nil, 1, "TestRace: 0/1 passed\n",
			[]string{"burn: TestRace, run 1 failed (exit 1):", "WARNING: DATA RACE"}},
		// Of a flag repeated in GOFLAGS the last wins, so a -race put there
		// before the caller's flags would lose to this one.
		{"-r holds against a GOFLAGS that turns it off", []string{"-r"}, []string{"GOFLAGS=-trimpath -race=false"}, 1,
			"TestRace: 0/1 passed\n", []string{"WARNING: DATA RACE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.goModule(map[string]string{
				"go.mod": "module example.com/racy\n\ngo 1.21\n",
				// Two goroutines add to one variable with nothing between them.
				"racy_test.go": `package racy

import "testing"

func TestRace(t *testing.T) {
	var x int
	done := make(chan bool)
	go func() { x++; done <- true }()
	x++
	<-done
	_ = x
}
`,
			})
			r.env = append(r.env, tc.env...)
			args := append(tc.args, "-n", "1", "-x", "1", "-c", "1", ".", "TestRace")
			cmd, stdout, stderr := r.burn(0, args...)
			if code := finish(t, cmd); code != tc.code {
				t.Fatalf("exit status = %d, want %d\n%s", code, tc.code, stderr)
			}
			if got := stdout.String(); got != tc.stdout {
				t.Fatalf("stdout = %q, want %q\n%s", got, tc.stdout, stderr)
			}
			for _, want := range tc.stderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// A signal ends burn with the loops, the run in flight and the work
// directory, and with the status a shell reports for it.
func TestBurnEndsItsLoopsOnASignal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.set("block-TestA", "")
	cmd, _, stderr := r.burn(4, "-n", "100", "-x", "2", "-c", "2", "./pkg", "TestA")
	waitFor(t, "the test to be running", func() bool { return len(r.pids("child")) == 1 })
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := finish(t, cmd); code != 143 {
		t.Fatalf("exit status = %d, want 143\n%s", code, stderr)
	}
	pids := append(r.pids("spinners"), r.pids("child")...)
	if len(pids) != 5 {
		t.Fatalf("%d processes recorded, want 4 loops and the test", len(pids))
	}
	for _, pid := range pids {
		if !gone(pid) {
			t.Errorf("process %d is still running after burn ended", pid)
		}
	}
	if left := r.leftover(); len(left) != 0 {
		t.Errorf("burn left %v in its TMPDIR", left)
	}
}

// What burn cannot run it says, with status 2, before it starts a loop.
func TestBurnRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
		ps   string // what a stand-in ps says burn's own nice level is; "" for the machine's
	}{
		{"no package", nil, "usage: burn", ""},
		{"no count", []string{"-n", "0", "./pkg", "TestA"}, "-n is 0, want at least 1", ""},
		{"a count that is no number", []string{"-n", "many", "./pkg", "TestA"}, "-n is many, want a whole number", ""},
		{"no factor", []string{"-x", "0", "./pkg", "TestA"}, "-x is 0, want at least 1", ""},
		{"no cpus", []string{"-c", "0", "./pkg", "TestA"}, "-c is 0, want at least 1", ""},
		{"a nice level that is no number", []string{"-N", "-3", "./pkg", "TestA"}, "-N is -3, want a whole number", ""},
		// ps says - of a process that is scheduled in real time.
		{"an own nice level ps cannot tell", []string{"./pkg", "TestA"}, `cannot tell burn's own nice level: ps said "-"`, "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if tc.ps != "" {
				r.psSays(tc.ps)
			}
			cmd, _, stderr := r.burn(0, tc.args...)
			if code := finish(t, cmd); code != 2 {
				t.Fatalf("exit status = %d, want 2\n%s", code, stderr)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want it to say %q", stderr, tc.want)
			}
			if got := r.read("spinners"); got != "" {
				t.Fatalf("burn started loops: %s", got)
			}
		})
	}
}
