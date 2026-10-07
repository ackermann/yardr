package rigcheck

import (
	"os"
	"os/exec"
	"path/filepath"
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
// "rpc" when the call has YARDR_TEST_RPC. gofmt finds nothing to format.
const fakeGo = `#!/bin/sh
echo "${YARDR_TEST_RPC:+rpc }$*" >>"$FAKE/calls"
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

// Both gates run a go step once more when it failed because a file of Go's
// build cache was gone, and only then: a second failure stands, and a step
// that failed of itself is not run again. Both runs' output is printed.
func TestCheckRunsAgainWhenTheBuildCacheLostAFile(t *testing.T) {
	const notice = "gate: the Go build cache lost a file under us; running again"
	const failing = "--- FAIL: TestX (0.00s)"
	full := []string{"vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}
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
	}{
		{"full: vet once", checkFull, "vet", "1", cacheLost, true,
			[]string{full[0], full[0], full[1], full[2]}, 1, true},
		{"full: vet twice", checkFull, "vet", "2", cacheLost, false,
			[]string{full[0], full[0]}, 2, true},
		{"full: test once", checkFull, "test", "1", cacheLost, true,
			[]string{full[0], full[1], full[1], full[2]}, 1, true},
		{"full: a failing test", checkFull, "test", "1", failing, false,
			[]string{full[0], full[1]}, 1, false},
		{"light: build once", checkLight, "build", "1", cacheLost, true,
			[]string{light[0], light[1], light[1], light[2]}, 1, true},
		{"light: test once", checkLight, "test", "1", cacheLost, true,
			[]string{light[0], light[1], light[2], light[2]}, 1, true},
		{"light: test twice", checkLight, "test", "2", cacheLost, false,
			[]string{light[0], light[1], light[2], light[2]}, 2, true},
		{"light: a failing test", checkLight, "test", "1", failing, false,
			light, 1, false},
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
			cmd.Env = append(checkEnv(os.Environ()),
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
