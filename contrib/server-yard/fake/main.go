// Command herdr is a stand-in for herdr's server and CLI, for the
// server-yard test: it answers what yardr asks while a yard is set up,
// with one canned agent that is always working. State lives in
// HERDR_FAKE_STATE (calls.log, the pane, agents.log, server.pid); the API
// socket is HERDR_SOCKET_PATH. With HERDR_FAKE_STATE/logged-out present, a
// prompt is refused as herdr refuses one for an agent at a login screen,
// and no session comes up.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var state = os.Getenv("HERDR_FAKE_STATE")

func logCall(args []string) {
	if state == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(state, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "herdr %s\n", strings.Join(args, " "))
}

// result prints a CLI result on stdout, as herdr does.
func result(v string) {
	fmt.Printf("{\"result\":%s}\n", v)
}

// failure prints a CLI error object on stderr, as herdr does, and exits 1.
func failure(id, code, message string) {
	fmt.Fprintf(os.Stderr, "{\"error\":{\"code\":%q,\"message\":%q},\"id\":%q}\n", code, message, id)
	os.Exit(1)
}

// flag is the value of --name in args, "" when it takes none.
func flag(args []string, name string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func panePath() string { return filepath.Join(state, "pane") }

// shellPath holds the pid the fake gives as every pane's shell: the
// server's own, a process that lives as long as the panes it stands for and
// that no yardr command descends from. yardr starts no agent in a pane
// whose shell herdr does not tell (runner.Herdr.shellKnown).
func shellPath() string { return filepath.Join(state, "server.pid") }

// server runs the API socket: one JSON object per line,
// {"id","method","params"} answered by {"id","result"}, the protocol
// yardr's runner speaks (internal/runner/herdr_api.go).
func server(sock string) {
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake herdr: listen %s: %v\n", sock, err)
		os.Exit(1)
	}
	defer l.Close()
	if state != "" {
		os.WriteFile(shellPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			sc := bufio.NewScanner(c)
			sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for sc.Scan() {
				var req struct {
					ID     string          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method == "" {
					continue
				}
				logCall([]string{req.Method, string(req.Params)})
				out := `{"type":"ok"}`
				switch req.Method {
				case "workspace.create":
					out = `{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"}}`
				case "tab.create":
					out = `{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}`
				case "events.subscribe":
					out = `{"type":"subscription_started"}`
				}
				fmt.Fprintf(c, "{\"id\":%q,\"result\":%s}\n", req.ID, out)
			}
		}()
	}
}

func main() {
	args := os.Args[1:]
	logCall(args)
	if state == "" {
		fmt.Fprintln(os.Stderr, "fake herdr: HERDR_FAKE_STATE is not set")
		os.Exit(2)
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake herdr: no command")
		os.Exit(2)
	}
	sock := os.Getenv("HERDR_SOCKET_PATH")
	switch args[0] {
	case "server":
		if len(args) > 1 {
			failure("cli:server:"+args[1], "unknown", "fake herdr knows no server "+args[1])
		}
		if sock == "" {
			fmt.Fprintln(os.Stderr, "fake herdr: HERDR_SOCKET_PATH is not set")
			os.Exit(2)
		}
		server(sock)
	case "status":
		// Up is a socket that answers: dial it, as the server's own
		// clients do.
		c, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			fmt.Println("status: not running")
			os.Exit(1)
		}
		c.Close()
		fmt.Printf("status: running\nversion: 0.9.0-fake\nsocket: %s\n", sock)
	case "agent":
		if len(args) < 2 {
			failure("cli:agent", "unknown", "fake herdr: agent needs a verb")
		}
		switch args[1] {
		case "list":
			result(`{"agents":[]}`)
		case "start":
			name := ""
			if len(args) > 2 {
				name = args[2]
			}
			kind, pane := flag(args, "kind"), flag(args, "pane")
			if f, err := os.OpenFile(filepath.Join(state, "agents.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintf(f, "start %s kind=%s pane=%s\n", name, kind, pane)
				f.Close()
			}
			// The harness's screen: its input's mark, as HarnessScreen
			// reads it (internal/runner/herdr.go).
			os.WriteFile(panePath(), []byte("❯ \n"), 0o600)
			fmt.Printf("{\"result\":{\"type\":\"agent_started\",\"agent\":{\"agent_name\":%q}}}\n", name)
		case "prompt":
			name, text := "", ""
			if len(args) > 2 {
				name = args[2]
			}
			if len(args) > 3 {
				text = args[3]
			}
			if _, err := os.Stat(filepath.Join(state, "logged-out")); err == nil {
				if f, err := os.OpenFile(filepath.Join(state, "calls.log"), os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
					fmt.Fprintf(f, "prompt refused (logged-out)\n")
					f.Close()
				}
				failure("cli:agent:prompt", "agent_blocked", "agent "+name+" is blocked and requires interactive input")
			}
			// The prompt as a message the agent took, its answer, and
			// its input again: what arrived and the wait read (❯ is
			// Claude Code's mark, harnesses/claude.yaml).
			f, _ := os.OpenFile(panePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if f != nil {
				fmt.Fprintf(f, "❯ %s\n⏺ Working on it.\n❯ \n", text)
				f.Close()
			}
			result(`{"agent":{"agent_status":"working"},"type":"agent_prompted"}`)
		case "get":
			name := ""
			if len(args) > 2 {
				name = args[2]
			}
			fmt.Printf("{\"result\":{\"agent\":{\"agent_name\":%q,\"agent_status\":\"working\"},\"type\":\"agent\"}}\n", name)
		default:
			result(`{"type":"ok"}`)
		}
	case "pane":
		if len(args) > 1 && args[1] == "read" {
			// Raw text, not a JSON result, as herdr prints it.
			b, _ := os.ReadFile(panePath())
			os.Stdout.Write(b)
			return
		}
		if len(args) > 1 && args[1] == "process-info" {
			pid, err := os.ReadFile(shellPath())
			if err != nil {
				failure("cli:pane:process-info", "no_process", "fake herdr's server is not running")
			}
			fmt.Printf("{\"result\":{\"process_info\":{\"pane_id\":%q,\"shell_pid\":%s}}}\n", flag(args, "pane"), strings.TrimSpace(string(pid)))
			return
		}
		result(`{"type":"ok"}`)
	default:
		result(`{"type":"ok"}`)
	}
}
