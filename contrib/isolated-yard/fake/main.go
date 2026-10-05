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
	// as a harness does that is not logged in: no prompt box, and what it
	// is sent is taken and shown nowhere (the terminal's echo is off), so a
	// first prompt sent meanwhile has not arrived for yardr. Removing the
	// file is the login: the box comes, and yardr sends the prompt again.
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
				logf("login screen took %q", strings.TrimSpace(line))
			case <-time.After(200 * time.Millisecond):
			}
		}
		stty("echo")
		fmt.Println("Login successful")
		logf("logged in")
	}

	box := func() { fmt.Printf("%s\n❯ \n%s\n", rule, rule) }
	box()
	for line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			logf("prompt %q", line)
			// As Claude Code echoes a message it took, above its input: that
			// is where yardr looks for a first prompt that arrived.
			fmt.Printf("❯ %s\n", line)
			// A mayor's first prompt asks it to greet its operator by mail
			// (runner.Greets): the stand-in does, so a chat opened on it
			// gets a first message.
			if m := greet.FindStringSubmatch(line); m != nil {
				out, err := exec.Command(m[1], "mail", m[2], "Hello, this is the stand-in mayor: logged in and ready.").CombinedOutput()
				logf("greeted %s: %v %s", m[2], err, strings.TrimSpace(string(out)))
				fmt.Printf("⏺ Greeted %s by mail.\n", m[2])
			}
		}
		box()
	}
	logf("exit")
}

// greet finds, in a first prompt, the mail command it asks to greet with:
// yardr's path and the user to write to.
var greet = regexp.MustCompile("greet them with `(\\S+) mail (\\S+) \"<text>\"`")

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
