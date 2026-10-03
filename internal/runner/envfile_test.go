package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const envSecret = "lin_api_s3cret"

func envFileT(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mayor.env")
	body := "# outbound Linear\nLINEAR_API_KEY=" + envSecret + "\nYARDR_ACTOR=someone-else\n"
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// envHerdr is a fake herdr logging every call to the returned file.
func envHerdr(t *testing.T, wt string) (*Herdr, string, *bytes.Buffer) {
	calls := filepath.Join(t.TempDir(), "calls")
	var logs bytes.Buffer
	h := &Herdr{YardHome: "/y", YardBin: "/bin/yardr",
		Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
case "$1 $2" in
  "workspace create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"}}}' ;;
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  "tab create") echo '{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, calls, &logs
}

func readCalls(t *testing.T, path string) string {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// checkEnvStart asserts the env file's entry reached the herdr call that
// opens the pane, the identity was not overridden by the file, and the
// value shows in neither the handle nor the logs.
func checkEnvStart(t *testing.T, calls, paneCall, actor string, handle map[string]any, logs string) {
	t.Helper()
	var call string
	for _, l := range strings.Split(calls, "\n") {
		if strings.HasPrefix(l, paneCall+" ") {
			call = l + " "
		}
	}
	if !strings.Contains(call, " --env LINEAR_API_KEY="+envSecret+" ") {
		t.Errorf("%s lacks the env file's entry:\n%s", paneCall, call)
	}
	if !strings.Contains(call, " --env YARDR_ACTOR="+actor+" ") || strings.Contains(call, "someone-else") {
		t.Errorf("env file overrode the identity:\n%s", call)
	}
	if h := fmt.Sprint(handle); strings.Contains(h, envSecret) {
		t.Errorf("handle holds the value: %s", h)
	}
	if strings.Contains(logs, envSecret) {
		t.Errorf("logs show the value:\n%s", logs)
	}
	if !strings.Contains(logs, "LINEAR_API_KEY") {
		t.Errorf("debug log lacks the key names:\n%s", logs)
	}
}

func TestStartResidentLoadsEnvFile(t *testing.T) {
	h, calls, logs := envHerdr(t, "")
	r := store.Resident{Name: "mayor", Dir: t.TempDir(), Config: map[string]any{"env_file": envFileT(t, 0o600)}}
	handle, err := h.StartResident(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	checkEnvStart(t, readCalls(t, calls), "workspace create", "mayor", handle, logs.String())
}

func TestHerdrStartLoadsGroupEnvFile(t *testing.T) {
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	h, calls, logs := envHerdr(t, wt)
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Stage: "review"},
		Group:   store.Group{Name: "yard-review", Config: map[string]any{"env_file": envFileT(t, 0o600)}},
		Rig:     &store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	handle, err := h.Start(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	checkEnvStart(t, readCalls(t, calls), "tab create", "yard-13-review", handle, logs.String())
}

// A file others can read, or none at all, refuses the start before herdr is
// asked for anything, as Blocked, and the error shows no value.
func TestEnvFileRefusedBlocksStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.env")
	for name, path := range map[string]string{"0644": envFileT(t, 0o644), "0640": envFileT(t, 0o640),
		"missing": missing, "directory": t.TempDir()} {
		h, calls, logs := envHerdr(t, "")
		cfg := map[string]any{"env_file": path}
		_, rerr := h.StartResident(context.Background(), store.Resident{Name: "mayor", Dir: t.TempDir(), Config: cfg})
		_, gerr := h.Start(context.Background(), dispatch.Job{Bead: store.Bead{ID: "yard-13", Stage: "new"},
			Group: store.Group{Name: "g", Config: cfg}, Rig: &store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"}})
		for who, err := range map[string]error{"resident": rerr, "group": gerr} {
			if !dispatch.IsBlocked(err) {
				t.Errorf("%s %s: err = %v, want Blocked", name, who, err)
			} else if strings.Contains(err.Error(), envSecret) || !strings.Contains(err.Error(), "env_file") {
				t.Errorf("%s %s: error %q", name, who, err)
			}
		}
		if c := readCalls(t, calls); c != "" {
			t.Errorf("%s: herdr called despite the refusal:\n%s", name, c)
		}
		if strings.Contains(logs.String(), envSecret) {
			t.Errorf("%s: logs show the value", name)
		}
	}
}

// A resident's herdr agent and workspace carry the yard's name, so two
// yards on one machine (herdr lists their agents together, and refuses a
// second "mayor") are told apart: the `name` setting, else the home's
// directory name; a yard with neither names them after the resident alone.
func TestStartResidentNamesTheYard(t *testing.T) {
	for _, tc := range []struct {
		home, setting, agent, label string
	}{
		{"/Users/jo/.yardr", "", "yardr-mayor", "yardr mayor"},
		{"/tmp/y1", "", "y1-mayor", "y1 mayor"},
		{"/tmp/y1", "Prod/Web", "prod-web-mayor", "Prod/Web mayor"},
		{"", "", "mayor", "mayor"},
	} {
		h, calls, _ := envHerdr(t, "")
		h.YardHome = tc.home
		if tc.setting != "" {
			st := storetest.New(t)
			if err := st.SetSetting(context.Background(), "name", tc.setting, "me"); err != nil {
				t.Fatal(err)
			}
			h.Store = st
		}
		handle, err := h.StartResident(context.Background(), store.Resident{Name: "mayor", Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		got := readCalls(t, calls)
		if !strings.Contains(got, "--label "+tc.label+" --no-focus") || !strings.Contains(got, "\nagent start "+tc.agent+" --kind claude") ||
			handle["agent"] != tc.agent {
			t.Errorf("home %q, name %q: agent %v, calls:\n%s", tc.home, tc.setting, handle["agent"], got)
		}
	}
}
