package rigcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Depot yardr's own full gate, as it is in this checkout.
const checkFull = "../../.yardr/check"

// What go vet said on 7 Oct 2026, when a file of the shared build cache was
// gone under a gate.
const cacheLost = "vet: internal/store/sources.go:10:2: could not import yardr.dev/yardr/internal/source (open ../../../../Library/Caches/go-build/43/439f5c1e-d: no such file or directory)"

// A go that fails one of its steps (its first argument) the first few times
// with a line of its own, and writes down every call: the arguments, after
// "rpc" when the call has YARDR_TEST_RPC and after the GOOS it was given.
// gofmt finds nothing to format.
const fakeGo = `#!/bin/sh
echo "${YARDR_TEST_RPC:+rpc }${GOOS:+$GOOS }$*" >>"$FAKE/calls"
if [ "$1" = "$FAKE_STEP" ]; then
  n=$(cat "$FAKE/n" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/n"
  if [ "$n" -le "$FAKE_TIMES" ]; then
    echo "$FAKE_LINE" >&2
    exit 1
  fi
fi
echo "ran $1"
`

// Both gates, and the test command that is to replace them, run a go step
// once more when it failed because a file of Go's build cache was gone, and
// only then: a second failure stands, and a step
// that failed of itself is not run again. Both runs' output is printed.
func TestCheckRunsAgainWhenTheBuildCacheLostAFile(t *testing.T) {
	const notice = "the Go build cache lost a file under us; running again"
	const failing = "--- FAIL: TestX (0.00s)"
	full := []string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}
	light := []string{"vet ./...", "build ./...", "test -race ./..."}
	for _, c := range []struct {
		name   string
		script string
		step   string // the step that fails,
		times  string // this many times,
		line   string // saying this
		ok     bool
		calls  []string
		lines  int // how often the output has line
		again  bool
		env    string // one more variable of the script's environment
		summed string // the line a failed step's summary starts with
	}{
		{"full: vet once", checkFull, "vet", "1", cacheLost, true,
			[]string{full[0], full[0], full[1], full[2], full[3]}, 1, true, "", ""},
		{"full: vet twice", checkFull, "vet", "2", cacheLost, false,
			[]string{full[0], full[0]}, 2, true, "", ""},
		{"full: test once", checkFull, "test", "1", cacheLost, true,
			[]string{full[0], full[1], full[2], full[2], full[3]}, 1, true, "", ""},
		{"full: a failing test", checkFull, "test", "1", failing, false,
			full[:3], 1, false, "", ""},
		{"light: build once", checkLight, "build", "1", cacheLost, true,
			[]string{light[0], light[1], light[1], light[2]}, 1, true, "", ""},
		{"light: test once", checkLight, "test", "1", cacheLost, true,
			[]string{light[0], light[1], light[2], light[2]}, 1, true, "", ""},
		{"light: test twice", checkLight, "test", "2", cacheLost, false,
			[]string{light[0], light[1], light[2], light[2]}, 2, true, "", ""},
		{"light: a failing test", checkLight, "test", "1", failing, false,
			light, 1, false, "", ""},
		// .yardr/test is the light gate run plainly and the full one for a
		// landing. A step that failed for good is followed by the lines
		// that say what failed: a failing test's a second time.
		{"test: build once", yardTest, "build", "1", cacheLost, true,
			[]string{light[0], light[1], light[1], light[2]}, 1, true, "", ""},
		{"test: test twice", yardTest, "test", "2", cacheLost, false,
			[]string{light[0], light[1], light[2], light[2]}, 2, true, "", "test: FAILED: go test -race ./..."},
		{"test: a failing test", yardTest, "test", "1", failing, false,
			light, 2, false, "", "test: FAILED: go test -race ./...\n" + failing},
		{"test, landing: vet once", yardTest, "vet", "1", cacheLost, true,
			[]string{full[0], full[0], full[1], full[2], full[3]}, 1, true, "YARDR_LANDING=1", ""},
		{"test, landing: a failing test", yardTest, "test", "1", failing, false,
			full[:3], 2, false, "YARDR_LANDING=1", "test: FAILED: go test -race ./...\n" + failing},
	} {
		t.Run(c.name, func(t *testing.T) {
			script, err := os.ReadFile(c.script)
			if err != nil {
				t.Fatal(err)
			}
			fake, dir := t.TempDir(), t.TempDir()
			for name, text := range map[string]string{"go": fakeGo, "gofmt": "#!/bin/sh\n"} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			// A branch that changed go.mod: the light gate tests every
			// package, and asks go for no list of them.
			mod := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// Without the YARDR_TEST_RPC of the run this test is in (CI's rpc
			// job, the full gate's last step): the fake go writes "rpc" for
			// it, and only the script's own step is to have it. The same
			// holds of GOOS, which the full gate sets for its linux vet.
			env := slices.DeleteFunc(checkEnv(os.Environ()), func(kv string) bool {
				return strings.HasPrefix(kv, "YARDR_TEST_RPC=") || strings.HasPrefix(kv, "GOOS=") ||
					strings.HasPrefix(kv, "YARDR_LANDING=") || strings.HasPrefix(kv, "YARDR_BASE=")
			})
			if c.env != "" {
				env = append(env, c.env)
			}
			cmd.Env = append(env,
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				BaseVar+"="+gitT(t, dir, "rev-parse", "HEAD"),
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_STEP="+c.step, "FAKE_TIMES="+c.times, "FAKE_LINE="+c.line)
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err == nil) != c.ok {
				t.Errorf("err %v, want ok %v", err, c.ok)
			}
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			if got, want := strings.TrimSpace(string(calls)), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
			}
			if n := strings.Count(out, c.line); n != c.lines {
				t.Errorf("the output has the failing line %d times, want %d", n, c.lines)
			}
			if strings.Contains(out, notice) != c.again {
				t.Errorf("the output says it ran again: %v, want %v", !c.again, c.again)
			}
			if strings.Contains(out, "test: FAILED") != (c.summed != "") || !strings.Contains(out, c.summed) {
				t.Errorf("the output's summary of a failed step: want %q", c.summed)
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
			// The log the step's output was kept in is gone again.
			if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}
