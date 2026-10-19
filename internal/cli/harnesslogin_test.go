package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// loginStandIn is a claude that plays what `claude auth login` of Claude
// Code 2.1.288 printed on pipes, with no credentials (harnesses/claude.yaml,
// login_recipe): the line that it opens the browser, the line with the
// address, and the prompt it reads one line on, which does not end. The
// code good#s1 logs it in; any other line ends it as the real one ended on
// a line that is no code. With a file `browser` beside it the login is made
// on the page it opened itself, and it reads nothing. What the real one
// prints when a login is made was not run, so this one prints nothing then.
// Its one-shot call answers once it is logged in. It writes down beside
// itself how it was started (logins, calls) and its pid.
const loginStandIn = `dir=${0%/*}
case "$1" in
--version) echo "2.1.288 (Claude Code)"; exit 0 ;;
-p) echo "$*" >> "$dir/calls"
    [ -e "$dir/logged-in" ] && { echo ok; exit 0; }
    echo "Not logged in · Please run /login"; exit 1 ;;
esac
[ "$1 $2" = "auth login" ] || { echo "unknown: $*" >&2; exit 2; }
echo "$*" >> "$dir/logins"
echo $$ > "$dir/pid"
echo 'Opening browser to sign in…'
echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&state=s1"
printf 'Paste code here if prompted > '
[ -e "$dir/browser" ] && { : > "$dir/logged-in"; exit 0; }
IFS= read -r code
[ -e "$dir/echo-input" ] && {
  printf '\nPaste code here if prompted > %s\n' "$code"
  echo "Login failed: invalid $code" >&2
  exit 1
}
[ "$code" = "good#s1" ] && { : > "$dir/logged-in"; exit 0; }
echo "Login failed: Request failed with status code 400" >&2
exit 1
`

// loginPage is the address the stand-in prints.
const loginPage = "https://claude.com/cai/oauth/authorize?code=true&state=s1"

// ledLogin is a run of yardr harness login claude --json against the
// stand-in, led as a program leads it: by the JSON lines it prints and the
// JSON lines it is answered with, and by nothing else.
type ledLogin struct {
	t       *testing.T
	dir     string // holds the stand-in and what it wrote down
	answers io.WriteCloser
	lines   chan string
	printed strings.Builder // every line it printed
	exit    chan int
}

type loginWaitReadyGate struct {
	store.API
	entered chan struct{}
	release chan struct{}
}

func (s *loginWaitReadyGate) LoginWaitReady(ctx context.Context, id string) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.API.LoginWaitReady(ctx, id)
}

// leadStandIn starts the run on a home with no yard and no terminal. The
// one real call is the stand-in's own.
func leadStandIn(t *testing.T) *ledLogin {
	t.Helper()
	return leadStandInAs(t, &app{}, loginStandIn, "harness", "login", "claude", "--json")
}

// leadStandInAs starts the run of args on a, on a home with no yard and no
// terminal, with script as the claude it logs in: a run that needs a yard
// passes an app with one. The one real call is the stand-in's own.
func leadStandInAs(t *testing.T, a *app, script string, args ...string) *ledLogin {
	t.Helper()
	noHome(t)
	setInteractive(t, false)
	stub(t, &tryHarnessCall, realTry)
	l := &ledLogin{t: t, dir: fakeTools(t, map[string]string{"claude": script}), lines: make(chan string, 16), exit: make(chan int, 1)}
	in, answers := io.Pipe()
	l.answers = answers
	a.stdin = in
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// The run's needs go to its command's writer, which the test reads
	// line by line.
	a.stdout = w
	go func() {
		defer close(l.lines)
		for s := bufio.NewScanner(r); s.Scan(); {
			l.lines <- s.Text()
		}
	}()
	go func() {
		l.exit <- execute(a, args)
		w.Close()
	}()
	t.Cleanup(func() {
		answers.Close()
		for range l.lines {
		}
		r.Close()
	})
	return l
}

// next is the next line the run prints, as the object it is.
func (l *ledLogin) next() map[string]any {
	l.t.Helper()
	select {
	case line, ok := <-l.lines:
		if !ok {
			l.t.Fatalf("the run ended, and another line was expected; it printed:\n%s", l.printed.String())
		}
		l.printed.WriteString(line + "\n")
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			l.t.Fatalf("a line that is no JSON object: %q", line)
		}
		return v
	case <-time.After(time.Minute):
		l.t.Fatalf("no line within a minute; it printed:\n%s", l.printed.String())
	}
	return nil
}

// need is the next line, which must be the need of that name.
func (l *ledLogin) need(name string) map[string]any {
	l.t.Helper()
	v := l.next()
	if v["need"] != name {
		l.t.Fatalf("the next line is %v, want the need %s", v, name)
	}
	return v
}

func (l *ledLogin) answer(line string) {
	l.t.Helper()
	if _, err := io.WriteString(l.answers, line+"\n"); err != nil {
		l.t.Fatal(err)
	}
}

// ends is the status the run ended with, after its last line: nothing more
// is printed.
func (l *ledLogin) ends() int {
	l.t.Helper()
	select {
	case code := <-l.exit:
		if line, more := <-l.lines; more {
			l.t.Errorf("a line after the end: %q", line)
		}
		return code
	case <-time.After(time.Minute):
		l.t.Fatalf("the run did not end; it printed:\n%s", l.printed.String())
	}
	return -1
}

// wrote is what the stand-in wrote down in the file name, "" for none.
func (l *ledLogin) wrote(name string) string {
	b, _ := os.ReadFile(filepath.Join(l.dir, name))
	return string(b)
}

// A program leads Claude Code's login by JSON alone (yardr-p7ex): it is
// asked for the method, which becomes the login command's flag, is given the
// page to sign in on and asked for what the page gave, and that goes to the
// login command and is printed nowhere. Once the command has ended well it
// is asked whether to make the one real call, told that it costs a model
// call, and no call is made before its yes. The end says the call answered.
func TestHarnessLoginLedByJSON(t *testing.T) {
	l := leadStandIn(t)
	choose := l.need("choose")
	if opts, _ := json.Marshal(choose["options"]); string(opts) != `["Claude subscription","Anthropic Console (API usage billing)"]` ||
		choose["text"] != "How do you log in to Claude Code?" {
		t.Fatalf("choose: %v", choose)
	}
	l.answer(`{"choose":"Anthropic Console (API usage billing)"}`)
	if open := l.need("open_url"); open["url"] != loginPage {
		t.Fatalf("open_url: %v", open)
	}
	if input := l.need("input"); input["text"] != "Paste code here if prompted >" || input["secret"] != true {
		t.Fatalf("input: %v", input)
	}
	l.answer(`{"input":" good#s1 "}`)
	confirm := l.need("confirm")
	if text, _ := confirm["text"].(string); !strings.Contains(text, "Check that Claude Code answers?") || !strings.Contains(text, "costs a model call") {
		t.Fatalf("confirm: %v", confirm)
	}
	if calls := l.wrote("calls"); calls != "" {
		t.Fatalf("a real call before it was confirmed: %q", calls)
	}
	l.answer(`{"confirm":true}`)
	done := l.next()
	try, _ := done["try"].(map[string]any)
	if done["done"] != true || done["kind"] != "claude" || done["proved"] != true || try["state"] != tryOK || try["output"] != "ok" {
		t.Fatalf("done: %v", done)
	}
	if code := l.ends(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if got := l.wrote("logins"); got != "auth login --console\n" {
		t.Errorf("the login command was started as %q", got)
	}
	if got := l.wrote("calls"); got != "-p "+tryPrompt+"\n" {
		t.Errorf("the real calls: %q, want the one of doctor --try", got)
	}
	if !strings.Contains(l.printed.String(), `"url":"`+loginPage+`"`) {
		t.Errorf("the address is not printed as it is:\n%s", l.printed.String())
	}
	if strings.Contains(l.printed.String(), "good#s1") {
		t.Errorf("what was put in is printed:\n%s", l.printed.String())
	}
}

// With stdin closed nobody answers: the run asks for the method, refuses in
// one JSON line and ends with status 1, and the login command is never
// started.
func TestHarnessLoginStdinClosed(t *testing.T) {
	l := leadStandIn(t)
	l.answers.Close()
	l.need("choose")
	refusal := l.next()
	if msg, _ := refusal["error"].(string); !strings.Contains(msg, "stdin was closed before a login method was chosen") || !strings.Contains(msg, "nothing was started") {
		t.Fatalf("the refusal: %v", refusal)
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := l.wrote("logins"); got != "" {
		t.Errorf("the login command was started: %q", got)
	}
}

// Stdin closing while the login command waits stops the command, its
// process with it: who led the login is gone, and nothing is left listening
// for a sign-in nobody will finish.
func TestHarnessLoginStopsWithItsLeader(t *testing.T) {
	l := leadStandIn(t)
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	l.need("input")
	l.answers.Close()
	if msg, _ := l.next()["error"].(string); !strings.Contains(msg, "stdin was closed before claude auth login --claudeai ended: the login was stopped") {
		t.Fatalf("the error: %q", msg)
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(l.wrote("pid")))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Errorf("the login command (pid %d) after the run: %v, want no such process", pid, err)
	}
}

// A login the harness refuses ends with what the harness said, status 1,
// and no question about a real call: there is no login to prove.
func TestHarnessLoginFails(t *testing.T) {
	l := leadStandIn(t)
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	l.need("input")
	// A line that answers nothing asked is passed by.
	l.answer(`{"confirm":true}`)
	l.answer(`{"input":"wrong#s1"}`)
	msg, _ := l.next()["error"].(string)
	if !strings.Contains(msg, "claude auth login --claudeai: exit status 1: Login failed: Request failed with status code 400") {
		t.Fatalf("the error: %q", msg)
	}
	if strings.Contains(msg, "wrong#s1") {
		t.Errorf("the error repeats what was put in: %q", msg)
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if calls := l.wrote("calls"); calls != "" {
		t.Errorf("a real call after a login that failed: %q", calls)
	}
}

// A harness can echo what it was given in a prompt or an error. Neither
// line yardr prints may repeat the secret input.
func TestHarnessLoginRedactsEchoedInput(t *testing.T) {
	l := leadStandIn(t)
	if err := os.WriteFile(filepath.Join(l.dir, "echo-input"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	l.need("input")
	l.answer(`{"input":"secret#s1"}`)
	echod := l.need("input")
	if text, _ := echod["text"].(string); strings.Contains(text, "secret#s1") {
		t.Errorf("the echoed prompt repeats the input: %v", echod)
	}
	msg, _ := l.next()["error"].(string)
	if strings.Contains(msg, "secret#s1") {
		t.Errorf("the failure repeats the input: %q", msg)
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if strings.Contains(l.printed.String(), "secret#s1") {
		t.Errorf("a JSON line repeats the input:\n%s", l.printed.String())
	}
}

// A login made on the page the harness opened itself ends with no input
// given, and an input sent after it is passed by. A no to the real call
// ends the run well, with nothing proved and no call made.
func TestHarnessLoginEndsInTheBrowser(t *testing.T) {
	l := leadStandIn(t)
	if err := os.WriteFile(filepath.Join(l.dir, "browser"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	l.need("input")
	l.need("confirm")
	l.answer(`{"input":"late#s1"}`)
	l.answer(`{"confirm":false}`)
	done := l.next()
	try, _ := done["try"].(map[string]any)
	if done["done"] != true || done["proved"] != false || try["state"] != tryNotTried || try["output"] != "the check was declined" {
		t.Fatalf("done: %v", done)
	}
	if code := l.ends(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if calls := l.wrote("calls"); calls != "" {
		t.Errorf("a real call after a no: %q", calls)
	}
}

// What a program answers with that the run cannot go by ends it, in words
// that do not repeat the line: a method that is none, a line that is no
// JSON, an input of several lines.
func TestHarnessLoginRefusesAnswers(t *testing.T) {
	for name, c := range map[string]struct {
		answers []string
		want    string
	}{
		"no method":     {[]string{`{"choose":"--console"}`}, `"--console" is no login method of claude: Claude subscription, Anthropic Console (API usage billing)`},
		"no JSON":       {[]string{`{"choose":"Claude subscription"}`, `secret#s1`}, "an answer is one JSON object a line, and a line on stdin was none"},
		"several lines": {[]string{`{"choose":"Claude subscription"}`, `{"input":"secret#s1\nmore"}`}, "an input is one line, and the one given had several: the login was stopped"},
	} {
		t.Run(name, func(t *testing.T) {
			l := leadStandIn(t)
			l.need("choose")
			l.answer(c.answers[0])
			for _, a := range c.answers[1:] {
				l.need("open_url")
				l.need("input")
				l.answer(a)
			}
			msg, _ := l.next()["error"].(string)
			if msg != c.want {
				t.Errorf("the error: %q, want %q", msg, c.want)
			}
			if code := l.ends(); code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
		})
	}
}

// A login nobody finishes ends when its time is up, and says so.
func TestHarnessLoginTimesOut(t *testing.T) {
	stub(t, &loginTimeout, 300*time.Millisecond)
	l := leadStandIn(t)
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	for {
		v := l.next()
		if msg, ok := v["error"].(string); ok {
			if msg != "claude auth login --claudeai did not end within 300ms: the login was stopped" {
				t.Errorf("the error: %q", msg)
			}
			break
		}
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}

// Only a kind with a recipe is led, and only a harness that is installed:
// each refusal is one JSON line. Without --json the command says what a
// person runs in a terminal, and starts nothing.
func TestHarnessLoginWithoutARecipe(t *testing.T) {
	noHome(t)
	setInteractive(t, false)
	dir := fakeTools(t, map[string]string{"claude": loginStandIn})
	var errs bytes.Buffer
	ask := func(want int, args ...string) string {
		t.Helper()
		out, code := executeOut(&app{stdin: strings.NewReader(""), stderr: &errs}, args...)
		if code != want {
			t.Fatalf("yardr %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, out)
		}
		return out
	}
	wantText(t, ask(1, "harness", "login", "kimi", "--json"),
		`{"error":"yardr has no login recipe for kimi: it is logged in in a terminal, on its own screen"}`)
	wantText(t, ask(1, "harness", "login", "cursor", "--json"), `{"error":"yardr has no login recipe for cursor:`)
	wantText(t, ask(0, "harness", "login", "kimi"), "yardr knows no login command of Kimi: start kimi in a terminal and log in on its own screen.")
	wantText(t, ask(0, "harness", "login", "claude"), "Claude Code is logged in with its own command, in a terminal:\n  claude auth login\n",
		"A program leads the same login for a person without one: yardr harness login claude --json")
	if ask(1, "harness", "login", "nope"); !strings.Contains(errs.String(), "nope is no harness kind") {
		t.Errorf("a kind herdr does not start: %q", errs.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "logins")); err == nil {
		t.Error("the login command was started without --json")
	}

	fakeTools(t, nil)
	wantText(t, ask(1, "harness", "login", "claude", "--json"),
		`{"error":"claude is not on PATH, so there is nothing to log in: `+agents.InstallHint("claude"))
}

// harness show prints a kind's recipe as its file has it, and doctor --json
// says of each harness whether its kind has one: what a program marks a
// kind by that needs a terminal once to log in.
func TestHarnessLoginRecipeShown(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"claude": "echo 2.1.288\n", "kimi": "echo 1.0\n"})
	stub(t, &loginShell, func() string { return "" })
	m := machineOf(t)
	if claude, kimi := m["claude"], m["kimi"]; !claude.Found || !claude.HarnessLogin || !kimi.Found || kimi.HarnessLogin {
		t.Errorf("harness_login: claude %+v, kimi %+v", claude, kimi)
	}
	if m["git"].HarnessLogin || m["herdr"].HarnessLogin {
		t.Errorf("a program that is no harness has a login: %+v, %+v", m["git"], m["herdr"])
	}

	show := func(args ...string) string {
		out, _ := executeOut(&app{}, append([]string{"harness", "show"}, args...)...)
		return out
	}
	var shown agents.Harness
	if err := json.Unmarshal([]byte(show("claude", "--json")), &shown); err != nil || shown.LoginRecipe == nil {
		t.Fatalf("show claude --json: %v, %+v", err, shown.LoginRecipe)
	}
	if r := shown.LoginRecipe; strings.Join(r.Argv, " ") != "auth login" || len(r.Methods) != 2 || r.Methods[1].Args[0] != "--console" ||
		!strings.Contains(r.OpenURL, "visit: (https://") || r.Input != "^Paste code here if prompted" ||
		r.Verified != (agents.Verified{From: "claude auth login, Claude Code 2.1.288", On: "2026-10-13"}) {
		t.Errorf("claude's recipe: %+v", r)
	}
	wantText(t, show("claude"), "Login led by a program, with no terminal (yardr harness login claude --json)",
		"  the command: claude auth login", `  method "Anthropic Console (API usage billing)": --console`,
		"  where it takes input:   ^Paste code here if prompted",
		"(read from claude auth login, Claude Code 2.1.288 on 2026-10-13; it may stop matching when the harness prints something else)")
	shown = agents.Harness{}
	if err := json.Unmarshal([]byte(show("kimi", "--json")), &shown); err != nil || shown.LoginRecipe != nil {
		t.Fatalf("show kimi --json: %v, %+v", err, shown.LoginRecipe)
	}
	if out := show("kimi"); strings.Contains(out, "Login led by a program") {
		t.Errorf("show kimi names a login it has no recipe for:\n%s", out)
	}
	list, _ := executeOut(&app{}, "harness", "list")
	wantText(t, list, "a fetch for a Mac, a login a program leads\n")
}

// loginStandInGroup is the stand-in that also writes down the login folder
// it was started with, for a login led for a group.
var loginStandInGroup = strings.Replace(loginStandIn, "dir=${0%/*}\n",
	"dir=${0%/*}\necho \"$CLAUDE_CONFIG_DIR\" >> \"$dir/envseen\"\n", 1)

// heldStore is a store execute must not close: the test's own store, which
// its cleanup closes after the run.
type heldStore struct{ store.API }

func (heldStore) Close(context.Context) error { return nil }

// A login led with --group runs the login command and the proving call with
// the group's env joined, over the process's own: both land in the folder
// the group's env names, and what was put in is still printed nowhere.
func TestHarnessLoginGroupLandsInGroupFolder(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	login := t.TempDir()
	env := filepath.Join(t.TempDir(), "hal.env")
	if err := os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR="+login+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "env_file": env}}, "me"); err != nil {
		t.Fatal(err)
	}
	// An ambient folder the group's env must win over.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	l := leadStandInAs(t, &app{st: heldStore{st}, actor: "me"}, loginStandInGroup,
		"harness", "login", "claude", "--group", "hal-builders", "--json")
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	l.need("input")
	l.answer(`{"input":"good#s1"}`)
	l.need("confirm")
	l.answer(`{"confirm":true}`)
	done := l.next()
	try, _ := done["try"].(map[string]any)
	if done["done"] != true || done["kind"] != "claude" || done["proved"] != true || try["state"] != tryOK {
		t.Fatalf("done: %v", done)
	}
	if code := l.ends(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if got, want := l.wrote("envseen"), login+"\n"+login+"\n"; got != want {
		t.Errorf("the folders the login command and the proving call saw: %q, want both %q", got, login)
	}
	if strings.Contains(l.printed.String(), "good#s1") {
		t.Errorf("what was put in is printed:\n%s", l.printed.String())
	}
}

// A group login for a group there is none of, or one with no env_file, is
// refused naming why; a kind with no recipe says to log in in a terminal
// with the group's env set, with --json and without.
func TestHarnessLoginGroupRefusals(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	env := filepath.Join(t.TempDir(), "hal.env")
	if err := os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR="+t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "hal-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": env}},
		{Name: "bare", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}
	noHome(t)
	setInteractive(t, false)
	dir := fakeTools(t, map[string]string{"claude": loginStandIn})
	ask := func(args ...string) (string, error) {
		t.Helper()
		return run(t, a.rootCmd(), args...)
	}
	if _, err := ask("harness", "login", "claude", "--group", "ghost", "--json"); err == nil ||
		!strings.Contains(err.Error(), "group ghost: not found") {
		t.Errorf("an unknown group: %v", err)
	}
	if _, err := ask("harness", "login", "claude", "--group", "bare", "--json"); err == nil ||
		!strings.Contains(err.Error(), "group bare names no env_file") {
		t.Errorf("a group with no env_file: %v", err)
	}
	if _, err := ask("harness", "login", "kimi", "--group", "hal-builders", "--json"); err == nil ||
		!strings.Contains(err.Error(), "yardr has no login recipe for kimi: log it in in a terminal with group hal-builders's env set ("+env+")") {
		t.Errorf("a kind with no recipe: %v", err)
	}
	out, err := ask("harness", "login", "kimi", "--group", "hal-builders")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "log it in in a terminal with group hal-builders's env set ("+env+")")
	out, err = ask("harness", "login", "claude", "--group", "hal-builders")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "With --group hal-builders the same login is led with that group's env")
	if _, err := os.Stat(filepath.Join(dir, "logins")); err == nil {
		t.Error("the login command was started without --json")
	}
}

// A led login whose input is given through a secret ask: the input need
// names the wait, the yardmaster's ask names it, and the giving hands the
// value to the login as its stdin answer. The value is printed nowhere.
func TestHarnessLoginInputGivenThroughSecretAsk(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Log the harness in"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	gate := &loginWaitReadyGate{API: heldStore{st}, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate.release)
		}
	})
	l := leadStandInAs(t, &app{st: gate, actor: "me"}, loginStandIn, "harness", "login", "claude", "--json")
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the leader did not wait for the login registration")
	}
	select {
	case line := <-l.lines:
		t.Fatalf("the input need printed before the wait was ready: %s", line)
	default:
	}
	close(gate.release)
	released = true
	input := l.need("input")
	login, _ := input["login"].(string)
	if login == "" {
		t.Fatalf("the input need names no wait: %v", input)
	}
	seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "The code the sign-in page showed", To: "op",
		Context: []string{"The login waits for what the sign-in page showed."},
		Secret:  &store.AskSecret{Login: login}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.GiveSecret(ctx, b.ID, seq, map[string]string{"INPUT": "good#s1"}, "op"); err != nil {
		t.Fatalf("GiveSecret immediately after the need: %v", err)
	}
	l.need("confirm")
	l.answer(`{"confirm":true}`)
	done := l.next()
	try, _ := done["try"].(map[string]any)
	if done["done"] != true || done["kind"] != "claude" || done["proved"] != true || try["state"] != tryOK {
		t.Fatalf("done: %v", done)
	}
	if code := l.ends(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if strings.Contains(l.printed.String(), "good#s1") {
		t.Errorf("what was given is printed:\n%s", l.printed.String())
	}
}

// An input neither stdin nor the yard answers ends the login with a JSON
// error: the wait runs out, and nothing of the login is left listening.
func TestHarnessLoginInputTimesOut(t *testing.T) {
	stub(t, &loginInputTimeout, 300*time.Millisecond)
	st := storetest.New(t)
	l := leadStandInAs(t, &app{st: heldStore{st}, actor: "me"}, loginStandIn, "harness", "login", "claude", "--json")
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	if input := l.need("input"); input["login"] == "" {
		t.Fatalf("the input need names no wait: %v", input)
	}
	msg, _ := l.next()["error"].(string)
	if msg != "no input was given within 300ms: the login was stopped" {
		t.Fatalf("the error: %q", msg)
	}
	if code := l.ends(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(l.wrote("pid")))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Errorf("the login command (pid %d) after the run: %v, want no such process", pid, err)
	}
}

// An input answered on stdin leaves its wait in the yard: the reference is
// free for another wait once the answer is in.
func TestHarnessLoginStdinAnswerLeavesTheWait(t *testing.T) {
	st := storetest.New(t)
	l := leadStandInAs(t, &app{st: heldStore{st}, actor: "me"}, loginStandIn, "harness", "login", "claude", "--json")
	l.need("choose")
	l.answer(`{"choose":"Claude subscription"}`)
	l.need("open_url")
	input := l.need("input")
	login, _ := input["login"].(string)
	if login == "" {
		t.Fatalf("the input need names no wait: %v", input)
	}
	l.answer(`{"input":"good#s1"}`)
	l.need("confirm")
	l.answer(`{"confirm":true}`)
	if done := l.next(); done["done"] != true {
		t.Fatalf("done: %v", done)
	}
	if code := l.ends(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	// The wait is gone: the reference takes a new one. A call with its
	// context already ended registers and leaves at once where nothing
	// waits, and is refused as a duplicate where the old one still does.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ended, stop := context.WithCancel(context.Background())
		stop()
		_, err := st.LoginWait(ended, login)
		if errors.Is(err, context.Canceled) {
			break
		}
		if err == nil || !strings.Contains(err.Error(), "already waits") || time.Now().After(deadline) {
			t.Fatalf("a wait under the answered reference: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
