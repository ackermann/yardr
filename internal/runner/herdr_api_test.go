package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
)

// TestMain pins herdr's socket to a path nothing listens on: a test that
// reaches the socket without a fake of its own (fakeHerdrAPI) fails, and
// never opens a tab in whatever herdr the developer has running.
func TestMain(m *testing.M) {
	os.Setenv("HERDR_SOCKET_PATH", filepath.Join(os.TempDir(), "yardr-no-herdr.sock"))
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}

// fakeHerdrAPI listens as herdr's API socket, logs every request to calls
// as one line, "<method> <params as JSON>", and answers like herdr: ids
// for what creates a workspace or a tab, ok for the rest. A fake herdr
// binary logging to the same file (fakeHerdrBin) keeps the CLI's calls
// and the socket's apart by the dot in a method name.
func fakeHerdrAPI(t *testing.T, calls string) string {
	t.Helper()
	// Not under t.TempDir: see fakeHerdr on the socket path limit.
	dir, err := os.MkdirTemp("", "yh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						ID     string          `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					if f, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
						fmt.Fprintf(f, "%s %s\n", req.Method, req.Params)
						f.Close()
					}
					result := `{"type":"ok"}`
					switch req.Method {
					case "workspace.create":
						result = `{"type":"workspace_created","workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"}}`
					case "tab.create":
						result = `{"type":"tab_created","tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}`
					}
					fmt.Fprintf(c, "{\"id\":%q,\"result\":%s}\n", req.ID, result)
				}
			}()
		}
	}()
	return sock
}

// apiCall is the params of the last request for method in calls (the
// fake API's log, read), or nil when there was none.
func apiCall(t *testing.T, calls, method string) map[string]any {
	t.Helper()
	var last map[string]any
	for _, l := range strings.Split(calls, "\n") {
		if p, ok := strings.CutPrefix(l, method+" "); ok {
			last = nil
			if err := json.Unmarshal([]byte(p), &last); err != nil {
				t.Fatalf("%s params %q: %v", method, p, err)
			}
		}
	}
	return last
}

// envOf is the pane environment a logged tab.create or workspace.create
// carried.
func envOf(params map[string]any) map[string]string {
	out := map[string]string{}
	env, _ := params["env"].(map[string]any)
	for k, v := range env {
		out[k], _ = v.(string)
	}
	return out
}

// cliCalls is calls without the socket's lines: what went through the
// herdr binary, so on a command line.
func cliCalls(calls string) string {
	var out []string
	for _, l := range strings.Split(calls, "\n") {
		if method, _, _ := strings.Cut(l, " "); !strings.Contains(method, ".") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// A request over the socket is classified as the CLI's are: herdr's own
// error object is the request's failure, a server that is down (no socket,
// an outage code, a connection that answers nothing) the runtime's. Lines
// that are not the answer (an event) are skipped.
func TestHerdrCallClassifiesErrors(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "yh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			switch {
			case strings.Contains(line, `"tab.create"`):
				c.Write([]byte(`{"event":"pane_exited","data":{}}` + "\n" + `{"id":"yard-tab.create","result":{"type":"tab_created","tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}` + "\n"))
			case strings.Contains(line, `"taken"`):
				c.Write([]byte(`{"id":"x","error":{"code":"agent_exists","message":"taken"}}` + "\n"))
			case strings.Contains(line, `"starting"`):
				c.Write([]byte(`{"id":"x","error":{"code":"server_unavailable","message":"starting"}}` + "\n"))
			}
			c.Close()
		}
	}()
	h := &Herdr{Socket: sock}
	raw, err := h.call(ctx, "tab.create", map[string]any{"cwd": "/w"})
	if err != nil || !strings.Contains(string(raw), `"t2"`) {
		t.Fatalf("answer after an event: %s, %v", raw, err)
	}
	var herr *herdrError
	if _, err := h.call(ctx, "agent.start", map[string]any{"name": "taken"}); err == nil || dispatch.IsRuntime(err) ||
		!errors.As(err, &herr) || herr.Code != "agent_exists" {
		t.Fatalf("herdr error object classified as runtime: %v", err)
	}
	if _, err := h.call(ctx, "agent.start", map[string]any{"name": "starting"}); !dispatch.IsRuntime(err) ||
		!errors.As(err, &herr) || herr.Code != "server_unavailable" {
		t.Fatalf("outage code not a runtime error with herdr's error in the chain: %v", err)
	}
	if _, err := h.call(ctx, "agent.start", map[string]any{"name": "silent"}); !dispatch.IsRuntime(err) {
		t.Fatalf("a connection closed without an answer is not a runtime error: %v", err)
	}
	down := &Herdr{Socket: filepath.Join(dir, "none.sock")}
	if _, err := down.call(ctx, "tab.create", nil); !dispatch.IsRuntime(err) {
		t.Fatalf("no socket is not a runtime error: %v", err)
	}
}
