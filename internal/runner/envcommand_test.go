package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// providerT is a stand-in provider, a crew member's env_command: a script with
// body that also counts its runs, one line each, in the file returned.
func providerT(t *testing.T, body string) (cmd, runs string) {
	t.Helper()
	dir := t.TempDir()
	cmd, runs = filepath.Join(dir, "provider"), filepath.Join(dir, "runs")
	writeProvider(t, cmd, runs, body)
	return cmd, runs
}

func writeProvider(t *testing.T, cmd, runs, body string) {
	t.Helper()
	if err := os.WriteFile(cmd, []byte("#!/bin/sh\necho \"$YARDR_CREW\" >> "+runs+"\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// What a provider prints reaches the pane as an env_file's entries do: in
// the socket request that opens it, under the same rule for yardr's own
// keys, with only the names logged. It is asked at every start.
func TestStartCrewLoadsEnvCommand(t *testing.T) {
	cmd, runs := providerT(t, `echo "# from the vault"; echo "LINEAR_API_KEY=`+envSecret+`"; echo "YARDR_ACTOR=someone-else"`)
	h, calls, logs := envHerdr(t, "")
	r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: map[string]any{"env_command": cmd + " --env"}}
	for range 2 {
		handle, err := h.StartCrew(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		checkEnvStart(t, readCalls(t, calls), "workspace.create", "yardmaster", handle, logs.String())
	}
	if got := readCalls(t, runs); got != "yardmaster\nyardmaster\n" {
		t.Errorf("the provider's runs = %q, want one per start", got)
	}
	if !strings.Contains(logs.String(), "env_command "+cmd+" --env") {
		t.Errorf("the log does not say where the environment came from:\n%s", logs)
	}
}

// An env_file and a provider: the pane has both, and the provider's value
// for a key in both.
func TestStartCrewEnvCommandWinsOverEnvFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(file, []byte("LINEAR_API_KEY=from-the-file\nGITHUB_TOKEN=gh-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, _ := providerT(t, `echo "LINEAR_API_KEY=`+envSecret+`"`)
	h, calls, logs := envHerdr(t, "")
	r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: map[string]any{"env_file": file, "env_command": cmd}}
	handle, err := h.StartCrew(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	got := readCalls(t, calls)
	checkEnvStart(t, got, "workspace.create", "yardmaster", handle, logs.String())
	if env := envOf(apiCall(t, got, "workspace.create")); env["GITHUB_TOKEN"] != "gh-file" {
		t.Errorf("the env_file's own key is gone: %v", env)
	}
	if strings.Contains(got+logs.String(), "from-the-file") {
		t.Error("the file's value for the key in both went somewhere")
	}
}

// A provider that fails, hangs or prints a line that is no KEY=VALUE:
// through the dispatcher the crew member does not start and herdr is asked for
// no pane, one crew_blocked names the command, how it ended and the end
// of its stderr, and what it printed on stdout is in no event, log or herdr
// call. Fixed, the next start asks it again and goes through.
func TestEnvCommandRefusedBlocksStart(t *testing.T) {
	// Long enough for a script's first run on a busy machine, which a
	// provider that answers must not be taken for a hang over.
	old := config.EnvCommandTimeout
	config.EnvCommandTimeout = 3 * time.Second
	t.Cleanup(func() { config.EnvCommandTimeout = old })
	prints := `echo "LINEAR_API_KEY=` + envSecret + `"; `
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"fails":    {prints + `echo "vault is locked" >&2; exit 3`, []string{"exit status 3", "vault is locked"}},
		"hangs":    {prints + `echo "password:" >&2; sleep 30`, []string{"no answer within 3s", "password:"}},
		"bad line": {prints + `echo "export TOKEN=` + envSecret + `"`, []string{"line 2: want KEY=VALUE"}},
	} {
		ctx := context.Background()
		h, calls, logs := envHerdr(t, "")
		cmd, runs := providerT(t, tc.body)
		st := storetest.New(t)
		def := store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir(), Config: map[string]any{"env_command": cmd}}
		if _, err := st.PutCrew(ctx, def, "me"); err != nil {
			t.Fatal(err)
		}
		var dlog bytes.Buffer
		d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"herdr": h},
			Log: slog.New(slog.NewTextHandler(&dlog, &slog.HandlerOptions{Level: slog.LevelDebug}))}
		// A second start, its backoff cleared: the same reason is said once.
		for i := range 2 {
			if i > 0 {
				if err := st.SetCrewEnabled(ctx, "yardmaster", true, "", "me"); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		r, _ := st.GetCrew(ctx, "yardmaster")
		got := readCalls(t, calls)
		if r.Live() || r.Failures == 0 || strings.Contains(got, "workspace.create") || strings.Contains(got, "agent start") {
			t.Fatalf("%s: started: %+v\n%s", name, r, got)
		}
		var reasons []string
		evs, _ := st.Events(ctx, r.Journal, 100)
		for _, e := range evs {
			if e.Kind == "crew_blocked" {
				reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
			}
		}
		if len(reasons) != 1 {
			t.Fatalf("%s: crew_blocked = %q, want one", name, reasons)
		}
		for _, w := range append(tc.want, "crew member yardmaster: env_command "+cmd) {
			if !strings.Contains(reasons[0], w) {
				t.Errorf("%s: crew_blocked %q lacks %q", name, reasons[0], w)
			}
		}
		all, _ := st.EventsSince(ctx, 0, 0)
		raw, _ := json.Marshal(all)
		res, _ := json.Marshal(r)
		for where, text := range map[string]string{"events": string(raw), "the crew member": string(res), "herdr's calls": got,
			"the runner's log": logs.String(), "the daemon's log": dlog.String()} {
			if strings.Contains(text, envSecret) {
				t.Errorf("%s: %s hold what the provider printed:\n%s", name, where, text)
			}
		}
		if !strings.Contains(dlog.String(), "crew start failed") {
			t.Errorf("%s: the daemon's log does not say so:\n%s", name, dlog.String())
		}

		before := strings.Count(readCalls(t, runs), "\n")
		if before != 2 {
			t.Errorf("%s: the provider ran %d times for two starts", name, before)
		}
		writeProvider(t, cmd, runs, prints)
		if err := st.SetCrewEnabled(ctx, "yardmaster", true, "", "me"); err != nil {
			t.Fatal(err)
		}
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		r, _ = st.GetCrew(ctx, "yardmaster")
		got = readCalls(t, calls)
		if !r.Live() || envOf(apiCall(t, got, "workspace.create"))["LINEAR_API_KEY"] != envSecret {
			t.Fatalf("%s: not started once the provider answered: %+v\n%s", name, r, got)
		}
		if after := strings.Count(readCalls(t, runs), "\n"); after != before+1 {
			t.Errorf("%s: the provider ran %d times for the start that went through, want 1", name, after-before)
		}
	}
}
