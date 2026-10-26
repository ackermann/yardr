// Package burn tests contrib/burn/burn, which runs a Go package's tests over
// and over on a machine kept busy.
package burn

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
// they record is a file of the rig's dir, which they find in $RIG.
type rig struct {
	t   *testing.T
	dir string
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
	cmd := exec.Command("./burn", args...)
	cmd.Env = []string{
		"PATH=" + filepath.Join(r.dir, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + r.dir,
		"TMPDIR=" + filepath.Join(r.dir, "tmp"),
		"RIG=" + r.dir,
		"SPINNERS=" + strconv.Itoa(spinners),
	}
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
// beside the tests, and all gone, with burn's work directory, at the end.
func TestBurnCountsTheRunsThatPass(t *testing.T) {
	t.Parallel()
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
		"6 yes-loops (3 x 2 cpus) at nice 7",
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
	if got := strings.Fields(r.read("spinner-nice")); len(got) != 6 || strings.Join(got, " ") != strings.Repeat("7 ", 5)+"7" {
		t.Errorf("the loops' nice levels = %v, want six of 7", got)
	}
	if got := r.read("test-nice"); got != "7" {
		t.Errorf("the tests' nice level = %q, want the loops' 7", got)
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
	}{
		{"no package", nil, "usage: burn"},
		{"no count", []string{"-n", "0", "./pkg", "TestA"}, "-n is 0, want at least 1"},
		{"a count that is no number", []string{"-n", "many", "./pkg", "TestA"}, "-n is many, want a whole number"},
		{"no factor", []string{"-x", "0", "./pkg", "TestA"}, "-x is 0, want at least 1"},
		{"no cpus", []string{"-c", "0", "./pkg", "TestA"}, "-c is 0, want at least 1"},
		{"a nice level that is no number", []string{"-N", "-3", "./pkg", "TestA"}, "-N is -3, want a whole number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
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
