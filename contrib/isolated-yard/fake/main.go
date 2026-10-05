// Command fake is the stand-in agent of an isolated yard (see
// ../isolated-yard, which builds it as `claude`). It starts no model: it
// draws Claude Code's prompt box, which is what herdr's claude manifest reads
// as an idle agent, so a session reaches `running`, and it appends what it
// was started with and every line it is sent to agents.log beside its bin
// directory. Started with -p it answers "ok" and exits, or fails (see main).
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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

	box := func() { fmt.Printf("%s\n❯ \n%s\n", rule, rule) }
	box()
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for in.Scan() {
		if line := strings.TrimSpace(in.Text()); line != "" {
			logf("prompt %q", line)
			// As Claude Code echoes a message it took, above its input: that
			// is where yardr looks for a first prompt that arrived.
			fmt.Printf("❯ %s\n", line)
		}
		box()
	}
	logf("exit")
}
