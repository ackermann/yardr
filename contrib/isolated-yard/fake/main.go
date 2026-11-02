// Command fake is the stand-in agent of an isolated yard (see
// ../isolated-yard, which builds it as `claude`). It starts no model: it
// draws Claude Code's prompt box, which is what herdr's claude manifest reads
// as an idle agent, so a session reaches `running`, and it appends what it
// was started with and every line it is sent to agents.log beside its bin
// directory. Started with --version it prints Claude Code's version line and
// exits; with -p it answers "ok" and exits, or fails; started
// with auth login it plays Claude Code's login command, and with a
// file login-needed it waits at a login screen first, and with a file answer
// it says that file's text to every prompt it takes, and with a file busy it
// stays in the turn of a prompt it took (see main). A first
// prompt that asks for a greeting by mail gets one, and with a file run its
// lines are run as commands when the first prompt is taken (see runFile).
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

const rule = "────────────────────────────────────────────────────────────"

func main() {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(filepath.Dir(exe)), "agents.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format(time.TimeOnly), os.Getpid(), fmt.Sprintf(format, args...))
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "YARDR_") || strings.HasPrefix(kv, "HERDR_SOCKET_PATH=") {
			env = append(env, kv)
		}
	}
	logf("start exe=%s cwd=%s args=%q %s", exe, cwd, os.Args[1:], strings.Join(env, " "))

	// Its version flag (claude --version, what yardr doctor runs): the line
	// Claude Code prints, for yardr takes a program called claude that does
	// not say so for another program (harnesses/claude.yaml,
	// version_contains). No screen is drawn, as for a one-shot call.
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("2.1.310 (Claude Code)")
		logf("exit")
		return
	}

	// A one-shot call (claude -p <prompt>, what yardr doctor --try makes):
	// one word and out. With a file print-fails beside agents.log it fails
	// as a harness does that cannot answer: the file's text on stderr and
	// status 1.
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		if why, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(exe)), "print-fails")); err == nil {
			fmt.Fprint(os.Stderr, string(why))
			logf("exit 1")
			os.Exit(1)
		}
		fmt.Println("ok")
		logf("exit")
		return
	}

	// Its login command (claude auth login, what yardr harness login plays):
	// the lines Claude Code 2.1.288 printed on pipes (harnesses/claude.yaml,
	// login_recipe), then one line read. The line good#code is the login:
	// it takes away login-needed and print-fails, so a stand-in started
	// after it is logged in and its one-shot call answers. Any other line
	// ends it as the real one ends on a line that is no code. It opens no
	// browser, and the address it prints is of no host.
	if len(os.Args) > 2 && os.Args[1] == "auth" && os.Args[2] == "login" {
		fmt.Println("Opening browser to sign in…")
		fmt.Println("If the browser didn't open, visit: https://stand-in.invalid/oauth/authorize?code=true")
		fmt.Print("Paste code here if prompted > ")
		code, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(code) != "good#code" {
			fmt.Fprintln(os.Stderr, "Login failed: Request failed with status code 400")
			logf("exit 1")
			os.Exit(1)
		}
		for _, name := range []string{"login-needed", "print-fails"} {
			os.Remove(filepath.Join(filepath.Dir(filepath.Dir(exe)), name))
		}
		logf("exit")
		return
	}

	// Lines come from a goroutine of their own, so that the screen below
	// can take them while it waits.
	lines := make(chan string)
	go func() {
		defer close(lines)
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for in.Scan() {
			lines <- in.Text()
		}
	}()

	// With a file login-needed beside agents.log it sits at a login screen,
	// as a harness does that is not logged in: no prompt box, and input is
	// held without showing it (the terminal's echo is off), so the first
	// prompt reaches the agent only after login. Removing the file is login:
	// the box comes and the held prompt is handled. The log says when a
	// prompt is held: a line that arrives after the file is gone was typed
	// after the login and is drawn as such (process), so a caller that sends
	// a prompt and then logs in waits for that line, not for a while.
	//
	// The screen is Claude Code's menu of login methods, drawn as the
	// harness file reads it live (harnesses/claude.yaml, login): the
	// `Select login method:` line the runner matches on, the methods with
	// the selected option carrying the input mark, then the code-paste
	// line. The menu's words follow the transcription in
	// internal/runner/login_test.go; what must hold exactly is the
	// `Select login method:` line and the marked option, since without
	// them the screen matches no login pattern and no led login is
	// offered. There is no menu to answer: the led login plays
	// `auth login`, not the menu.
	var held []string
	if login := filepath.Join(filepath.Dir(filepath.Dir(exe)), "login-needed"); exists(login) {
		for _, l := range []string{
			"Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.",
			"Select login method:",
			"❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise",
			"  2. Anthropic Console account · API usage billing",
			"  3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI",
			"Paste code here if prompted>",
		} {
			fmt.Println(l)
		}
		logf("login screen")
		stty("-echo")
		defer stty("echo")
		for exists(login) {
			select {
			case line, ok := <-lines:
				if !ok {
					logf("exit")
					return
				}
				// Keep the first prompt that arrives while the harness is at
				// login. Retries are the same pending prompt; once the person
				// logs in, handle it instead of silently losing it.
				if len(held) == 0 {
					held = append(held, line)
					// Without the text: the prompt is logged when it is handled,
					// and isolated-yard down counts the stand-in's starts by
					// searching this log for ' start ', which a second copy of
					// a prompt would give one more chance to match.
					logf("held a prompt")
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		stty("echo")
		fmt.Println("Login successful")
		logf("logged in")
	}

	// The box leaves the cursor after its mark, as Claude Code's does: what
	// is typed into the pane is echoed by the terminal where the cursor is,
	// and yardr reads an agent's input from the mark's line (runner.InputText,
	// promptPlace). Left below the box, the cursor put typed text under the
	// bottom rule and the input read as empty while text waited in it
	// (yardr-0g9g).
	box := func() { fmt.Printf("%s\n❯ \n%s\n\x1b[2A\x1b[2C", rule, rule) }
	greeted, first := false, true
	// process handles a line the stand-in took. below is how many rows under
	// the mark's line the cursor is: the Enter that sent a typed line moved
	// it there, and a held line was never typed into the box, so its cursor
	// is still on the mark's line.
	process := func(line string, below int) {
		// Redraw from the mark's line: the typed text and the bottom rule
		// go, and the message and a new box are drawn in their place.
		fmt.Print("\r")
		if below > 0 {
			fmt.Printf("\x1b[%dA", below)
		}
		fmt.Print("\x1b[J")
		if line = strings.TrimSpace(line); line != "" {
			logf("prompt %q", line)
			// As Claude Code echoes a message it took, above its input: that
			// is where yardr looks for a first prompt that arrived.
			fmt.Printf("❯ %s\n", line)
			// A yardmaster's first prompt asks it to greet its operator by mail
			// (runner.Greets): the stand-in does, so a chat opened on it
			// gets a first message.
			if m := greet.FindStringSubmatch(line); m != nil && !greeted {
				greeted = true
				out, err := exec.Command(m[1], "mail", m[2], "Hello, this is the stand-in yardmaster: logged in and ready.").CombinedOutput()
				logf("greeted %s: %v %s", m[2], err, strings.TrimSpace(string(out)))
				fmt.Printf("⏺ Greeted %s by mail.\n", m[2])
			}
			// With a file run beside agents.log its lines are run when the
			// first prompt is taken: by then yardr has started the session, so
			// what a line runs is a command from that session's own pane.
			if first {
				first = false
				dir := filepath.Dir(filepath.Dir(exe))
				if n, err := runFile(filepath.Join(dir, "run"), filepath.Join(dir, "run.out")); n > 0 || err != nil {
					logf("ran %d lines of run: %v", n, err)
					fmt.Printf("⏺ Ran %d lines of run.\n", n)
				}
			}
			// With a file answer beside agents.log it says that file's text
			// to what it took, as a harness says the error it stops on (a
			// model at capacity, a limit hit), and waits for input again.
			if say, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(exe)), "answer")); err == nil {
				for _, l := range strings.Split(strings.TrimSpace(string(say)), "\n") {
					fmt.Printf("⏺ %s\n", l)
				}
				logf("answered")
			}
			// With a file busy beside agents.log it is in the middle of a
			// turn for as long as the file is there: the line Claude Code
			// draws while it works, which herdr's claude manifest reads as a
			// working agent, and no box. Removing the file ends the turn.
			if busy := filepath.Join(filepath.Dir(filepath.Dir(exe)), "busy"); exists(busy) {
				fmt.Print(busyLine + "\n")
				logf("busy")
				for exists(busy) {
					time.Sleep(100 * time.Millisecond)
				}
				fmt.Print("\x1b[1A\x1b[J")
				logf("busy over")
			}
		}
		box()
	}
	box()
	for _, line := range held {
		process(line, 0)
	}
	for line := range lines {
		process(line, typedRows(line))
	}
	logf("exit")
}

// busyLine is what the stand-in shows while it is in a turn (see the file
// busy): Claude Code's line for a turn that runs.
const busyLine = "✻ Working…"

// greet finds, in a first prompt, the mail command it asks to greet with:
// yardr's path and the user to write to.
var greet = regexp.MustCompile("greet them with `(\\S+) mail (\\S+) \"<text>\"`")

// runFile runs each line of the file run as a command (sh -c), one after the
// other, and appends to the file out the line, what it printed and the status
// it ended with. The commands are children of the stand-in, so of the pane's
// shell: yardr tells them as the session's own (the store finds a caller by
// its pane's shell), which nothing started from outside the pane is. They get
// the stand-in's environment and directory and no input, so that none can
// take a prompt meant for the stand-in, and their output goes to the file
// alone: the pane's screen stays what yardr reads an agent by. It returns how
// many lines it ran: none where there is no file.
func runFile(run, out string) (int, error) {
	text, err := os.ReadFile(run)
	if err != nil {
		return 0, nil
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	for _, line := range strings.Split(string(text), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		n++
		// The pid and the session say whose line it was: every stand-in of
		// the yard that takes a first prompt runs the file.
		who := fmt.Sprintf("pid=%d session=%s", os.Getpid(), os.Getenv("YARDR_SESSION"))
		fmt.Fprintf(f, "%s $ %s\n", who, line)
		cmd := exec.Command("/bin/sh", "-c", line)
		cmd.Stdout, cmd.Stderr = f, f
		status := 0
		if err := cmd.Run(); err != nil {
			// A shell that did not start has no status of its own: -1,
			// and why.
			status = -1
			if cmd.ProcessState != nil {
				status = cmd.ProcessState.ExitCode()
			} else {
				fmt.Fprintf(f, "%v\n", err)
			}
		}
		fmt.Fprintf(f, "%s exit %d\n", who, status)
	}
	return n, nil
}

// typedRows is how many rows of the terminal a typed line took, behind the
// box's mark: one, or more where the terminal wrapped it, as it does a first
// prompt longer than the pane is wide. The Enter that ended the line left the
// cursor that many rows under the mark's line. Each character is counted as
// one column, and a stand-in that runs in no terminal counts one row.
func typedRows(line string) int {
	cols, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 {
		return 1
	}
	return wrappedRows(utf8.RuneCountInString(line), cols)
}

// wrappedRows is the rows that n typed characters take after the two columns
// of the mark, in a terminal cols wide. A line that fills its last row to the
// end takes no further one: the terminal wraps when the next character comes,
// not before.
func wrappedRows(n, cols int) int {
	return max(1, (2+n+cols-1)/cols)
}

// stty sets the terminal the stand-in runs in, where it has one.
func stty(arg string) {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
