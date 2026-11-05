package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"yardr.dev/yardr/internal/mcp"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The help names what the server is built on and the revisions it
// speaks, as its floor asks: a person setting up a client reads here
// whether theirs can connect at all.
func TestMCPHelpNamesTheSDKAndItsRevisions(t *testing.T) {
	t.Parallel()
	out, err := run(t, (&app{env: testEnv(t, t.TempDir())}).mcpCmd(), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"official Go SDK for MCP", "modelcontextprotocol", "go-sdk, v1.7.0",
		"2026-07-28", "2025-06-18", "2025-11-25",
		mcp.FloorMessage, "--connection",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("yardr mcp --help lacks %q:\n%s", want, out)
		}
	}
}

// The command stands in the root's tree, where a client config's
// "yardr mcp" finds it.
func TestMCPIsRegistered(t *testing.T) {
	t.Parallel()
	root := (&app{env: testEnv(t, t.TempDir())}).rootCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "mcp" {
			return
		}
	}
	t.Fatal("the root has no mcp command")
}

// yardr mcp serves a client that started it, over the streams that
// client speaks on (the process's own stdin and stdout in use; pipes
// of the test's here, through the mcpServe seam): a legacy handshake,
// the six tools, and a filing whose history reads "ada via testconn".
// The socket half of that mark (the header) is internal/rpc's test;
// here the store is the command's own, as a test's is.
func TestMCPServeOverStdio(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Kind: store.DepotDir, Path: t.TempDir()}, "ada"); err != nil {
		t.Fatal(err)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	was := mcpServe
	mcpServe = func(ctx context.Context, server *mcp.Server) error {
		return server.Serve(ctx, &sdkmcp.IOTransport{Reader: stdinR, Writer: stdoutW})
	}
	defer func() { mcpServe = was }()

	a := &app{st: st, actor: "ada", env: testEnv(t, t.TempDir())}
	cmd := a.mcpCmd()
	cmd.SetArgs([]string{"--connection", "testconn"})
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	enc := json.NewEncoder(stdinW)
	dec := json.NewDecoder(stdoutR)
	send := func(v map[string]any) {
		t.Helper()
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		t.Helper()
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("reading the server: %v", err)
		}
		return msg
	}
	call := func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		msg := next()
		if _, isErr := msg["error"]; isErr {
			t.Fatalf("%s answered %v", method, msg["error"])
		}
		return msg
	}

	hello := call(1, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "stand-in", "version": "1"},
	})
	if result, _ := hello["result"].(map[string]any); result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("the handshake answered %v", hello)
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	list := call(2, "tools/list", map[string]any{})
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != len(mcp.Tools()) {
		t.Fatalf("tools/list over stdio = %v", result)
	}
	filed := call(3, "tools/call", map[string]any{
		"name":      mcp.ToolAskForSomething,
		"arguments": map[string]any{"request": "Write the report"},
	})
	result, _ = filed["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	beadID, _ := structured["work"].(string)
	if beadID == "" {
		t.Fatalf("the filing over stdio answered %v", filed)
	}
	evs, err := st.Events(ctx, beadID, 10)
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, e := range evs {
		if e.Kind == "created" && store.Via(e) == "testconn" && e.Actor == "ada" {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("the filed work's history is unmarked: %+v", evs)
	}

	// The client going away ends the server, cleanly.
	_ = stdinW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("yardr mcp after the client left = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("yardr mcp did not end when the client went away")
	}
}
