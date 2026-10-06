package config

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What the stand-in providers print as values: none may show in an error.
const provided = "pr0vided-s3cret"

// providerT is a stand-in provider: a script with body, and the path it is
// at.
func providerT(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// An env_command's stdout is read as an env_file: its entries are the
// crew member's environment. It is run in the crew member's directory, without a
// shell, told who asks and for which yard, with nothing to read.
func TestCrewEnvFromCommand(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := providerT(t, `# as a secrets tool prints them
echo "# for $YARDR_CREW"
echo
echo "LINEAR_API_KEY=`+provided+`"
echo "WHO=$YARDR_CREW $YARD_CREW"
echo "HOME_OF=$YARDR_HOME"
echo "PWD_OF=$(pwd -P)"
echo "ARGS=$#:$1:$2"
echo "STDIN=$(cat | wc -c | tr -d ' ')"
echo "noise" >&2`)
	for name, cmd := range map[string]any{"string": p + " one $HOME", "list": []any{p, "one", "$HOME"}} {
		env, err := ReadCrewEnv(context.Background(), "yardmaster", dir, "/y", map[string]any{"env_command": cmd})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for k, want := range map[string]string{"LINEAR_API_KEY": provided, "WHO": "yardmaster yardmaster", "HOME_OF": "/y",
			"PWD_OF": dir, "ARGS": "2:one:$HOME", "STDIN": "0"} {
			if got := env.Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", name, k, got, want)
			}
		}
		if env.Command != p+" one $HOME" || env.File != "" || !env.Set() || env.From() != "env_command "+p+" one $HOME" ||
			strings.Join(env.CommandKeys, " ") != "LINEAR_API_KEY WHO HOME_OF PWD_OF ARGS STDIN" {
			t.Errorf("%s: %+v", name, env)
		}
	}
	// A crew member without a directory is asked for in the yard's home.
	env, err := ReadCrewEnv(context.Background(), "yardmaster", "", dir, map[string]any{"env_command": p})
	if err != nil || env.Get("PWD_OF") != dir {
		t.Fatalf("in the home: %v, %v", env, err)
	}
	// Neither setting: nothing, and no error.
	if env, err := ReadCrewEnv(context.Background(), "yardmaster", dir, "/y", map[string]any{}); err != nil || env.Set() || len(env.Env) != 0 {
		t.Fatalf("no setting: %+v, %v", env, err)
	}
}

// With an env_file too both are read, and a key in both has the command's
// value: once, so the pane's environment and the daemon's lookup agree.
func TestCrewEnvCommandWinsOverFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(file, []byte("LINEAR_API_KEY=from-the-file\nGITHUB_TOKEN=gh-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"env_file": file, "env_command": providerT(t, `echo "LINEAR_API_KEY=`+provided+`"; echo OTHER=x`)}
	env, err := ReadCrewEnv(context.Background(), "yardmaster", "", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(env.Env, " "); got != "GITHUB_TOKEN=gh-file LINEAR_API_KEY="+provided+" OTHER=x" {
		t.Errorf("env = %q", got)
	}
	if strings.Join(env.Keys(), " ") != "GITHUB_TOKEN LINEAR_API_KEY OTHER" || strings.Join(env.FileKeys, " ") != "LINEAR_API_KEY GITHUB_TOKEN" ||
		!strings.HasPrefix(env.From(), "env_file "+file+" with env_command ") {
		t.Errorf("%+v", env)
	}
	// The file refused: no environment, though the command answered.
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	env, err = ReadCrewEnv(context.Background(), "yardmaster", "", "", cfg)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") || len(env.Env) != 0 || env.FileErr == nil || env.CommandErr != nil {
		t.Fatalf("refused file: %+v, %v", env, err)
	}
}

// reached is a context whose deadline has passed once the returned func is
// called, and not a moment before: the limit of an env_command, at a time
// the test decides.
func reached() (context.Context, func()) {
	d := &deadline{Context: context.Background(), done: make(chan struct{})}
	return d, func() { close(d.done) }
}

type deadline struct {
	context.Context
	done chan struct{}
}

func (d *deadline) Done() <-chan struct{} { return d.done }

func (d *deadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// A provider that is not there, fails, hangs or prints a line that is no
// KEY=VALUE gives no environment and an error that names the command, how
// it ended and the end of its stderr: never its stdout, never a value.
func TestCrewEnvCommandFailures(t *testing.T) {
	// No case waits for the limit, so none is decided by how fast the
	// machine starts a script: a provider that answers has an hour to, and
	// the one that hangs says when it does, which is when its time is up.
	old := EnvCommandTimeout
	EnvCommandTimeout = time.Hour
	t.Cleanup(func() { EnvCommandTimeout = old })
	hanging := filepath.Join(t.TempDir(), "hanging")
	if err := syscall.Mkfifo(hanging, 0o600); err != nil {
		t.Fatal(err)
	}
	secret := `echo "LINEAR_API_KEY=` + provided + `"; `
	for name, tc := range map[string]struct {
		cmd  string
		want []string
	}{
		"missing":     {filepath.Join(t.TempDir(), "nope"), []string{"no such file"}},
		"not on PATH": {"yardr-no-such-provider --env", []string{"env_command yardr-no-such-provider --env: ", "not found"}},
		"exit": {providerT(t, secret+`for i in 1 2 3 4 5 6 7; do echo "line $i" >&2; done; echo "vault is locked" >&2; exit 3`),
			[]string{"exit status 3", "its stderr ended: line 4 | line 5 | line 6 | line 7 | vault is locked"}},
		// The child outlives the script's shell unless the group is killed.
		// It is there before the time is up: a kill that lands while the
		// shell forks misses the child on macOS, and the sleep is left.
		"hangs": {providerT(t, secret+`echo "waiting for a password" >&2; (: >`+hanging+`; exec sleep 300) & wait`),
			[]string{"no answer within 1h0m0s", "waiting for a password"}},
		"bad line": {providerT(t, secret+`echo "export TOKEN=`+provided+`"`), []string{" line 2: want KEY=VALUE"}},
		"bad key":  {providerT(t, `echo "1TOKEN=`+provided+`"`), []string{" line 1: want KEY=VALUE"}},
	} {
		ctx := context.Background()
		if name == "hangs" {
			// The child's open of the fifo returns when this one does: the
			// script has said what it waits for, and waits.
			var timeUp func()
			ctx, timeUp = reached()
			go func() {
				if f, err := os.Open(hanging); err == nil {
					f.Close()
				}
				timeUp()
			}()
		}
		start := time.Now()
		env, err := ReadCrewEnv(ctx, "yardmaster", "", "", map[string]any{"env_command": tc.cmd})
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, w := range append(tc.want, "env_command "+tc.cmd) {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", name, err, w)
			}
		}
		if strings.Contains(err.Error(), provided) || strings.Contains(err.Error(), "line 1 |") {
			t.Errorf("%s: error shows a value or too much: %q", name, err)
		}
		if len(env.Env) != 0 || env.CommandErr == nil || env.Command != tc.cmd {
			t.Errorf("%s: %+v", name, env)
		}
		// Not the script's sleep: it was killed, not waited for.
		if took := time.Since(start); took > 2*time.Minute {
			t.Errorf("%s: took %s", name, took)
		}
	}
}

// The limit itself: a provider that does not answer within
// EnvCommandTimeout is given up on and said to have been. Whether it had
// started by then is the machine's affair, so nothing of its stderr is
// asked for here.
func TestCrewEnvCommandTimeout(t *testing.T) {
	old := EnvCommandTimeout
	EnvCommandTimeout = 50 * time.Millisecond
	t.Cleanup(func() { EnvCommandTimeout = old })
	p := providerT(t, `echo "LINEAR_API_KEY=`+provided+`"; sleep 300`)
	start := time.Now()
	env, err := ReadCrewEnv(context.Background(), "yardmaster", "", "", map[string]any{"env_command": p})
	if err == nil || !strings.Contains(err.Error(), "env_command "+p+": no answer within 50ms") || strings.Contains(err.Error(), provided) {
		t.Fatalf("err = %v", err)
	}
	if len(env.Env) != 0 || env.CommandErr == nil {
		t.Errorf("%+v", env)
	}
	if took := time.Since(start); took > 2*time.Minute {
		t.Errorf("took %s", took)
	}
}

// What doctor asks: whether the program is there, without running it.
func TestEnvCommandMissing(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	p := providerT(t, "touch "+ran)
	if err := EnvCommandMissing("", map[string]any{"env_command": p + " x"}); err != nil {
		t.Errorf("there: %v", err)
	}
	if err := EnvCommandMissing(filepath.Dir(p), map[string]any{"env_command": "./provider"}); err != nil {
		t.Errorf("relative to the directory: %v", err)
	}
	if err := EnvCommandMissing("", map[string]any{}); err != nil {
		t.Errorf("none: %v", err)
	}
	err := EnvCommandMissing("", map[string]any{"env_command": "yardr-no-such-provider --env"})
	if err == nil || !strings.Contains(err.Error(), "env_command yardr-no-such-provider --env") {
		t.Errorf("missing: %v", err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the command was run")
	}
}

// A provider whose shell starts its next command just as its time is up
// leaves nothing running. The kill of its group can land while the shell
// forks, and on macOS the child then outlives it; whether one round lands
// so is chance, hence the rounds.
func TestCrewEnvCommandKillsChildForkedAtTheLimit(t *testing.T) {
	old := EnvCommandTimeout
	EnvCommandTimeout = time.Hour
	t.Cleanup(func() { EnvCommandTimeout = old })
	dir := t.TempDir()
	hanging, pidfile := filepath.Join(dir, "hanging"), filepath.Join(dir, "pid")
	if err := syscall.Mkfifo(hanging, 0o600); err != nil {
		t.Fatal(err)
	}
	// The time is up when the shell has opened the fifo, and what it does
	// next is fork the sleep.
	p := providerT(t, `echo $$ >`+pidfile+`; echo "waiting for a password" >&2; : >`+hanging+`; sleep 300`)
	left := 0
	for round := 0; round < 20; round++ {
		ctx, timeUp := reached()
		go func() {
			if f, err := os.Open(hanging); err == nil {
				f.Close()
			}
			timeUp()
		}()
		_, err := ReadCrewEnv(ctx, "yardmaster", "", "", map[string]any{"env_command": p})
		if err == nil || !strings.Contains(err.Error(), "no answer within 1h0m0s") {
			t.Fatalf("round %d: err = %v", round, err)
		}
		b, err := os.ReadFile(pidfile)
		if err != nil {
			t.Fatal(err)
		}
		// The provider is its group's leader, so the group has its pid.
		pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pgid <= 1 {
			t.Fatalf("round %d: pid %q: %v", round, b, err)
		}
		// A killed child is in the group until it is reaped, which is not
		// ours to do: it is given a moment.
		gone := false
		for wait := time.Now().Add(5 * time.Second); !gone && time.Now().Before(wait); time.Sleep(10 * time.Millisecond) {
			gone = syscall.Kill(-pgid, 0) == syscall.ESRCH
		}
		if !gone {
			left++
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	if left > 0 {
		t.Errorf("%d of 20 providers left a process running", left)
	}
}
