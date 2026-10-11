package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// standIn is a script in place of a command a declaration names, which does
// body.
func standIn(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "command")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// The runner hands the command the JSON object it is given on stdin, and
// what the command printed, one JSON object, is read's.
func TestRunAnObjectInAnObjectOut(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	p := standIn(t, `cat >`+seen+`; echo '{"id": "c1", "url": "https://linear.app/acme/issue/YRD-1#c1"}'`)
	var got struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	what := "command thread.reply (" + p + " reply)"
	err := Run(context.Background(), what, []string{p, "reply"}, map[string]any{"thread": "YRD-1", "body": "an answer", "key": "k-1"}, nil, t.TempDir(), 0, func(out []byte) error {
		return json.Unmarshal(out, &got)
	})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := os.ReadFile(seen)
	var obj map[string]any
	if err := json.Unmarshal(in, &obj); err != nil || obj["thread"] != "YRD-1" || obj["body"] != "an answer" || obj["key"] != "k-1" || len(obj) != 3 {
		t.Fatalf("the command's stdin = %s, %v", in, err)
	}
	if got.ID != "c1" || got.URL != "https://linear.app/acme/issue/YRD-1#c1" {
		t.Fatalf("the answer = %+v", got)
	}

	// No object in: the command's stdin is closed. read may be nil when any
	// one object answers.
	if err := os.WriteFile(seen, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = standIn(t, `cat >`+seen+`; echo '{}'`)
	if err := Run(context.Background(), what, []string{p}, nil, nil, t.TempDir(), 0, nil); err != nil {
		t.Fatal(err)
	}
	if in, _ := os.ReadFile(seen); len(in) != 0 {
		t.Fatalf("the command's stdin = %q", in)
	}

	// read's error is the command's failure, in the caller's words.
	bad := errors.New("its answer has no provider")
	if err := Run(context.Background(), what, []string{p}, nil, nil, t.TempDir(), 0, func([]byte) error { return bad }); err == nil ||
		!strings.Contains(err.Error(), what+": its answer has no provider") {
		t.Fatalf("err = %v", err)
	}
}

// Whatever is not exit 0 with one JSON object on stdout is a failure whose
// error ends with the command's stderr and never holds its stdout.
func TestRunFailures(t *testing.T) {
	const printed = "sk-what-a-command-must-not-leak"
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"exit 1":      {`echo '{"id": "` + printed + `"}'; echo "linear: not logged in" >&2; exit 1`, "exit status 1; its stderr ended: linear: not logged in"},
		"nothing":     {`exit 0`, "it printed nothing"},
		"no JSON":     {`echo "` + printed + ` 42%"`, "what it printed is not one JSON object"},
		"a list":      {`echo '[{"id": "c1"}]'`, "its answer is no JSON object"},
		"two objects": {`echo '{"id": "c1"} {"id": "c2"}'`, "more than one JSON object"},
		"too much":    {`head -c 70000 /dev/zero | tr '\0' ' '; echo '{}'`, "printed more than 65536 bytes"},
	} {
		p := standIn(t, tc.body)
		what := "command thread.read (" + p + " read)"
		err := Run(context.Background(), what, []string{p, "read"}, map[string]any{"thread": "YRD-1"}, nil, t.TempDir(), 0, nil)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if !strings.HasPrefix(err.Error(), what+": ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %q, want %q after %q", name, err, tc.want, what)
		}
		if strings.Contains(err.Error(), printed) {
			t.Errorf("%s: the error holds the command's stdout: %q", name, err)
		}
	}
}

// A command that does not answer in time is given up on. Whether it had
// started by then is the machine's affair, so nothing of its stderr is
// asked for here; internal/limits holds that test.
func TestRunTimeout(t *testing.T) {
	p := standIn(t, `exec sleep 300`)
	start := time.Now()
	what := "command thread.state (" + p + " state)"
	err := Run(context.Background(), what, []string{p, "state"}, nil, nil, t.TempDir(), 50*time.Millisecond, nil)
	if err == nil || !strings.Contains(err.Error(), what+": no answer within 50ms") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > time.Minute {
		t.Fatalf("took %s: the sleep was waited for", took)
	}
}

// A command sees PATH, HOME, the variable credential.env names and
// YARDR_SOURCE_BASE, and nothing else of the yard's environment.
func TestRunEnv(t *testing.T) {
	d, err := Parse("lin.yaml", []byte("name: lin\nbase: https://api.linear.app\ncredential: { env: LINEAR_API_KEY }\n"+
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_SESSION", "not-for-a-command")
	t.Setenv("LINEAR_API_KEY", "not-this-one")
	seen := filepath.Join(t.TempDir(), "seen")
	p := standIn(t, `env >`+seen+`; echo '{}'`)
	env := d.CommandEnv("the-token")
	if err := Run(context.Background(), "command thread.open ("+p+" open)", []string{p, "open"}, nil, env, t.TempDir(), 0, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(seen)
	have := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		have[l] = true
	}
	for _, want := range []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LINEAR_API_KEY=the-token",
		"YARDR_SOURCE_BASE=https://api.linear.app",
	} {
		if !have[want] {
			t.Errorf("the command's environment lacks %q:\n%s", want, out)
		}
		delete(have, want)
	}
	// What is left is the script's own: the yard's environment reached the
	// command as these four and nothing else.
	for left := range have {
		if name, _, _ := strings.Cut(left, "="); name == "_" || name == "PWD" || name == "SHLVL" || name == "OLDPWD" {
			continue
		}
		t.Errorf("the command sees more than its own: %q", left)
	}
}

// source add's part: each relative program is a file beside the
// declaration's file, copied under <home>/sources/<name>/ with its path
// kept, mode 0700; an absolute one is the operator's and is not copied.
// show's part lists each command with its file and the file's sha256, and
// rm's removes the directory.
func TestCommandFiles(t *testing.T) {
	text := "name: lin\nbase: https://api.linear.app\ncredential: { env: LINEAR_API_KEY }\n" +
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"thread:\n  reply: [linear/thread, reply]\n  posted: [linear/thread, posted]\n  open: [/opt/lin/open, open]\n  place: team\n"
	d, err := Parse("lin.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, c := range d.Commands() {
		keys = append(keys, c.Key)
	}
	if strings.Join(keys, " ") != "thread.reply thread.posted thread.open" {
		t.Fatalf("commands = %v", keys)
	}
	declDir, home := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(declDir, "linear"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CheckCommandFiles(declDir); err == nil || !strings.Contains(err.Error(), "a command's program linear/thread is a file beside the declaration's file") {
		t.Fatalf("a missing program: %v", err)
	}
	if err := d.CopyCommandFiles(declDir, home); err == nil {
		t.Fatal("a missing program was copied")
	}
	// The copy fails before anything is written.
	if _, err := os.Stat(filepath.Join(home, "sources", "lin")); !os.IsNotExist(err) {
		t.Fatalf("a failed copy left %v", err)
	}
	if err := os.WriteFile(filepath.Join(declDir, "linear", "thread"), []byte("#!/bin/sh\necho '{}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale file of a declaration this one replaces does not survive.
	stale := filepath.Join(home, "sources", "lin", "stale")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.CheckCommandFiles(declDir); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(home, "sources", "lin", "linear", "thread")
	fi, err := os.Stat(copied)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the copy = %v, %v", fi, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale file was kept")
	}
	if prog := (Command{Key: "thread.reply", Argv: []string{"linear/thread", "reply"}}).Program(home, "lin"); prog != copied {
		t.Fatalf("program = %q, want %q", prog, copied)
	}
	if prog := (Command{Key: "thread.open", Argv: []string{"/opt/lin/open", "open"}}).Program(home, "lin"); prog != "/opt/lin/open" {
		t.Fatalf("the operator's program = %q", prog)
	}

	files := d.CommandFiles(home)
	if len(files) != 3 {
		t.Fatalf("files = %+v", files)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\necho '{}'\n"))
	f := files[0]
	if f.Key != "thread.reply" || f.Program != "linear/thread" || !f.Declared || f.Path != copied || f.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("thread.reply's file = %+v", f)
	}
	if f := files[2]; f.Key != "thread.open" || f.Declared || f.Path != "" || f.SHA256 != "" {
		t.Fatalf("thread.open's file = %+v", f)
	}
	// The copy gone, the sha256 is too.
	if err := RemoveCommandFiles(home, "lin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "lin")); !os.IsNotExist(err) {
		t.Fatalf("the directory stays: %v", err)
	}
	if f := d.CommandFiles(home)[0]; f.SHA256 != "" || f.Path != copied {
		t.Fatalf("after rm = %+v", f)
	}
}
