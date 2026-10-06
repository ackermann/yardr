// Command fake is the stand-in agent of an isolated yard (see
// ../isolated-yard, which builds it as `claude`). It starts no model: it
// draws Claude Code's prompt box, which is what herdr's claude manifest reads
// as an idle agent, so a session reaches `running`, and it appends what it
// was started with and every line it is sent to agents.log beside its bin
// directory. Started with -p it answers "ok" and exits, or fails, and with a
// file login-needed it waits at a login screen first (see main). A first
// prompt that asks for a greeting by mail gets one.
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
	// the box comes and the held prompt is handled.
	var held []string
	if login := filepath.Join(filepath.Dir(filepath.Dir(exe)), "login-needed"); exists(login) {
		fmt.Println("Not logged in · Run /login")
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
	greeted := false
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
			// A mayor's first prompt asks it to greet its operator by mail
			// (runner.Greets): the stand-in does, so a chat opened on it
			// gets a first message.
			if m := greet.FindStringSubmatch(line); m != nil && !greeted {
				greeted = true
				out, err := exec.Command(m[1], "mail", m[2], "Hello, this is the stand-in mayor: logged in and ready.").CombinedOutput()
				logf("greeted %s: %v %s", m[2], err, strings.TrimSpace(string(out)))
				fmt.Printf("⏺ Greeted %s by mail.\n", m[2])
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

// greet finds, in a first prompt, the mail command it asks to greet with:
// yardr's path and the user to write to.
var greet = regexp.MustCompile("greet them with `(\\S+) mail (\\S+) \"<text>\"`")

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
