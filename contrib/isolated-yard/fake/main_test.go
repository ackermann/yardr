package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/testload"
)

// screen is as much of a terminal as the stand-in draws on: lines of text
// and a cursor, with the carriage return, the line feed and the three
// sequences the stand-in sends (cursor up, cursor right, erase below).
type screen struct {
	rows     [][]rune
	row, col int
}

func (s *screen) write(text string) {
	r := []rune(text)
	for i := 0; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\r':
			s.col = 0
		case c == '\n':
			s.row++
		case c == '\x1b' && i+1 < len(r) && r[i+1] == '[':
			j := i + 2
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(string(r[i+2 : j]))
			switch r[j] {
			case 'A':
				s.row = max(0, s.row-max(1, n))
			case 'C':
				s.col += max(1, n)
			case 'J':
				if s.row < len(s.rows) {
					s.rows[s.row] = s.rows[s.row][:min(s.col, len(s.rows[s.row]))]
					s.rows = s.rows[:s.row+1]
				}
			}
			i = j
		default:
			for len(s.rows) <= s.row {
				s.rows = append(s.rows, nil)
			}
			for len(s.rows[s.row]) <= s.col {
				s.rows[s.row] = append(s.rows[s.row], ' ')
			}
			s.rows[s.row][s.col] = c
			s.col++
		}
	}
}

// lines is the screen's text, without what trails each line and the screen.
func (s *screen) lines() []string {
	var out []string
	for _, r := range s.rows {
		out = append(out, strings.TrimRight(string(r), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// standIn is a stand-in started as isolated-yard installs it (D/bin/claude),
// its output drawn on a screen.
type standIn struct {
	t   *testing.T
	d   string
	pid int
	in  io.WriteCloser
	out *bufio.Reader
	scr screen
}

func start(t *testing.T, loginNeeded bool) *standIn {
	t.Helper()
	d := t.TempDir()
	exe := filepath.Join(d, "bin", "claude")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if loginNeeded {
		if err := os.WriteFile(filepath.Join(d, "login-needed"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(exe)
	// A session of its own, whatever the test itself was started from.
	cmd.Env = append(os.Environ(), "YARDR_SESSION=sess1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		_ = cmd.Wait()
	})
	return &standIn{t: t, d: d, pid: cmd.Process.Pid, in: in, out: bufio.NewReader(out)}
}

// drawFuse is how long draw waits for the stand-in before it gives up on it:
// a fuse against a stand-in that never draws, not a window its start must fit
// in. Beside a full race run the start of a binary just built, and its answer
// to a line it is sent, took longer than the 10 s this once was (yardr-g9tn).
// It is written for an idle box and draw and waitLog size it to the machine
// (testload.Scale).
const drawFuse = time.Minute

// draw reads the stand-in's output onto the screen up to the next thing that
// ends with end.
func (s *standIn) draw(end string) {
	s.t.Helper()
	var got strings.Builder
	done := make(chan error, 1)
	go func() {
		for !strings.HasSuffix(got.String(), end) {
			c, _, err := s.out.ReadRune()
			if err != nil {
				done <- err
				return
			}
			got.WriteRune(c)
		}
		done <- nil
	}()
	fuse := testload.Scale(drawFuse)
	select {
	case err := <-done:
		if err != nil {
			s.t.Fatalf("the stand-in's output ended at %q: %v", got.String(), err)
		}
	case <-time.After(fuse):
		s.t.Fatalf("no %q from the stand-in in %s", end, fuse)
	}
	// As the pane's terminal passes it on: a line feed returns the carriage.
	s.scr.write(strings.ReplaceAll(got.String(), "\n", "\r\n"))
}

// waitLog waits for the stand-in to say in its log (agents.log beside its bin
// directory) what a test waits on, a line that ends with what, where nothing
// it draws says so: a line it holds at the login screen shows nowhere. The
// pause between two looks is no fuse; the wait ends at the fuse (drawFuse,
// sized to the machine) and then fails with the log it found.
func (s *standIn) waitLog(what string) {
	s.t.Helper()
	log := filepath.Join(s.d, "agents.log")
	fuse := testload.Scale(drawFuse)
	for deadline := time.Now().Add(fuse); ; time.Sleep(10 * time.Millisecond) {
		raw, _ := os.ReadFile(log)
		if strings.Contains(string(raw), " "+what+"\n") {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no %q in the stand-in's log in %s:\n%s", what, fuse, raw)
		}
	}
}

// boxEnd is what the stand-in's box ends with: the cursor put after the mark.
const boxEnd = "\x1b[2A\x1b[2C"

func (s *standIn) want(what string, want ...string) {
	s.t.Helper()
	got := s.scr.lines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		s.t.Errorf("%s: the screen shows\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Text typed into the stand-in's pane shows on the line of its mark, where
// yardr reads an agent's input, and a line it took shows above a new, empty
// box. Found building yardr-ps05: the cursor was left under the box, so the
// terminal echoed typed text below the bottom rule and the input read as
// empty while text waited in it.
func TestTypedTextShowsInTheInput(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	s.want("at start", rule, "❯", rule)

	// What the pane's terminal does with typed text: it echoes it where the
	// cursor is.
	s.scr.write("abc")
	s.want("typed", rule, "❯ abc", rule)

	// Enter: the terminal's echo of it, and the line reaches the stand-in.
	s.scr.write("\r\n")
	if _, err := io.WriteString(s.in, "abc\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("taken", rule, "❯ abc", rule, "❯", rule)

	s.scr.write("de")
	s.want("typed again", rule, "❯ abc", rule, "❯ de", rule)
}

// The login screen is Claude Code's menu of login methods, as the harness
// file reads it live: the `Select login method:` line, the methods with the
// selected option carrying the input mark, then the code-paste line. A
// prompt held at it was never typed into the box: it is drawn as taken from
// where the box left the cursor, on the mark's line, and not from a row
// further up.
func TestHeldPromptIsDrawnFromTheInputLine(t *testing.T) {
	s := start(t, true)
	s.draw("if prompted>\n")
	// Sent while the terminal's echo is off: nothing shows.
	if _, err := io.WriteString(s.in, "first prompt\n"); err != nil {
		t.Fatal(err)
	}
	// Logged in only once the stand-in holds the line: one it has not read yet
	// is taken as typed after the login, and drawn from another row.
	s.waitLog("held a prompt")
	if err := os.Remove(filepath.Join(s.d, "login-needed")); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.draw(boxEnd)
	screen := []string{
		"Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.",
		"Select login method:",
		"❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise",
		"  2. Anthropic Console account · API usage billing",
		"  3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI",
		"Paste code here if prompted>",
		"Login successful",
		rule, "❯ first prompt", rule,
	}
	s.want("after login", append(slices.Clone(screen), "❯", rule)...)

	s.scr.write("abc")
	s.want("typed", append(slices.Clone(screen), "❯ abc", rule)...)
}

func TestWrappedRows(t *testing.T) {
	for _, c := range []struct{ n, cols, want int }{
		{0, 80, 1},
		{3, 80, 1},
		{78, 80, 1}, // fills the row: no wrap until another character comes
		{79, 80, 2},
		{158, 80, 2},
		{159, 80, 3},
	} {
		if got := wrappedRows(c.n, c.cols); got != c.want {
			t.Errorf("wrappedRows(%d, %d) = %d, want %d", c.n, c.cols, got, c.want)
		}
	}
}

// With a file answer the stand-in says its text to a prompt it takes, above
// a new box: how a test has a harness stop on an error of its own.
func TestAnswerIsSaidToAPromptTaken(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	if err := os.WriteFile(filepath.Join(s.d, "answer"), []byte("API Error: Repeated 529 Overloaded errors\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.scr.write("go\r\n")
	if _, err := io.WriteString(s.in, "go\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("answered", rule, "❯ go", "⏺ API Error: Repeated 529 Overloaded errors", rule, "❯", rule)
}

// With a file busy the stand-in stays in the turn of a prompt it takes: the
// line of a turn that runs and no box, which herdr reads as a working agent,
// until the file is removed. It is how a trial has an agent at work when the
// yard is stopped (yardr-0pk6).
func TestBusyKeepsTheTurnOpen(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	busy := filepath.Join(s.d, "busy")
	if err := os.WriteFile(busy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.scr.write("go\r\n")
	if _, err := io.WriteString(s.in, "go\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(busyLine + "\n")
	s.want("in the turn", rule, "❯ go", busyLine)
	if err := os.Remove(busy); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("the turn over", rule, "❯ go", rule, "❯", rule)
}

// With a file run the stand-in runs its lines as commands of its own when it
// takes its first prompt, their output and status to run.out, and goes on
// taking prompts: how a trial runs yardr from an agent's pane, which the
// stand-in otherwise leaves no shell in (yardr-mpxg). A later prompt runs
// nothing again.
func TestRunFileIsRunOnTheFirstPrompt(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	run := "echo out $YARDR_SESSION; echo err >&2\n\nread x; echo \"read=$x\"; exit 3\necho $PPID\n"
	if err := os.WriteFile(filepath.Join(s.d, "run"), []byte(run), 0o600); err != nil {
		t.Fatal(err)
	}
	s.scr.write("go\r\n")
	if _, err := io.WriteString(s.in, "go\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("ran", rule, "❯ go", "⏺ Ran 3 lines of run.", rule, "❯", rule)

	pid := strconv.Itoa(s.pid)
	who := "pid=" + pid + " session=sess1"
	want := strings.Join([]string{
		who + " $ echo out $YARDR_SESSION; echo err >&2",
		"out sess1",
		"err",
		who + " exit 0",
		// No input reaches a command: read finds the end, not a prompt.
		who + " $ read x; echo \"read=$x\"; exit 3",
		"read=",
		who + " exit 3",
		// A command is the stand-in's child.
		who + " $ echo $PPID",
		pid,
		who + " exit 0",
	}, "\n") + "\n"
	got, err := os.ReadFile(filepath.Join(s.d, "run.out"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("run.out is\n%s\nwant\n%s", got, want)
	}

	// Still there, and the file is not run a second time.
	s.scr.write("more\r\n")
	if _, err := io.WriteString(s.in, "more\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("second prompt", rule, "❯ go", "⏺ Ran 3 lines of run.", rule, "❯ more", rule, "❯", rule)
	if again, _ := os.ReadFile(filepath.Join(s.d, "run.out")); string(again) != want {
		t.Errorf("after a second prompt run.out is\n%s\nwant\n%s", again, want)
	}
}

// Without a file run nothing is run and no run.out is made.
func TestNoRunFileRunsNothing(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	s.scr.write("go\r\n")
	if _, err := io.WriteString(s.in, "go\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("taken", rule, "❯ go", rule, "❯", rule)
	if _, err := os.Stat(filepath.Join(s.d, "run.out")); err == nil {
		t.Error("run.out was made with no file run")
	}
}

// Started as claude auth login the stand-in plays Claude Code's login
// command on pipes (yardr-p7ex): the three lines, the last of which does
// not end, and one line read. good#code is the login, which takes away
// what made it logged out; any other line fails as the real one does.
func TestAuthLogin(t *testing.T) {
	d := t.TempDir()
	exe := filepath.Join(d, "bin", "claude")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	const printed = "Opening browser to sign in…\nIf the browser didn't open, visit: https://stand-in.invalid/oauth/authorize?code=true\nPaste code here if prompted > "
	login := func(code string) (string, string, error) {
		for _, name := range []string{"login-needed", "print-fails"} {
			if err := os.WriteFile(filepath.Join(d, name), []byte("Not logged in\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(exe, "auth", "login", "--claudeai")
		cmd.Stdin = strings.NewReader(code + "\n")
		var out, errs strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errs
		err := cmd.Run()
		return out.String(), errs.String(), err
	}
	if out, errs, err := login("nope"); err == nil || out != printed || errs != "Login failed: Request failed with status code 400\n" {
		t.Errorf("a line that is no code: %v, %q, %q", err, out, errs)
	}
	if _, err := os.Stat(filepath.Join(d, "print-fails")); err != nil {
		t.Errorf("a login that failed logged it in: %v", err)
	}
	if out, errs, err := login("good#code"); err != nil || out != printed || errs != "" {
		t.Errorf("the code: %v, %q, %q", err, out, errs)
	}
	for _, name := range []string{"login-needed", "print-fails"} {
		if _, err := os.Stat(filepath.Join(d, name)); !os.IsNotExist(err) {
			t.Errorf("%s after the login: %v", name, err)
		}
	}
	if out, err := exec.Command(exe, "-p", "x").Output(); err != nil || string(out) != "ok\n" {
		t.Errorf("the one-shot call after the login: %v, %q", err, out)
	}
	// Its version flag says who it is, as Claude Code's does: yardr takes a
	// program called claude that does not for another program, and doctor
	// then finds no harness in a yard made on this stand-in. It draws no
	// screen, so it ends at once with its stdin as it is.
	if out, err := exec.Command(exe, "--version").Output(); err != nil || string(out) != "2.1.310 (Claude Code)\n" {
		t.Errorf("the version flag: %v, %q", err, out)
	}
}
