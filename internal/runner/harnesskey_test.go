package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// keyFile writes body as user's key file of claude under home, as the
// yard's led login writes the operator's.
func keyFile(t *testing.T, home, user, body string, mode os.FileMode) {
	t.Helper()
	p, err := store.HarnessKeyFile(home, "claude", user)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// harnessKey reads the key of the login a session burns, and the kind's
// login_env alone of it: each user's own file, nothing for an absent one,
// a kind with no login_env, no user or no home, and Blocked for a file
// others can read.
func TestHarnessKeyIsTheLoginsOwn(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	keyFile(t, home, "op", "ANTHROPIC_API_KEY=sk-op\nYARDR_ACTOR=someone-else\nOTHER=1\n", 0o600)
	keyFile(t, home, "ada", "ANTHROPIC_API_KEY=sk-ada\n", 0o600)
	for _, c := range []struct {
		name, home, kind, user string
		want                   []string
	}{
		{"the operator's", home, "claude", "op", []string{"ANTHROPIC_API_KEY=sk-op"}},
		{"ada's", home, "claude", "ada", []string{"ANTHROPIC_API_KEY=sk-ada"}},
		{"an absent file", home, "claude", "hal", nil},
		{"no login_env", home, "codex", "op", nil},
		{"no user", home, "claude", "", nil},
		{"no home", "", "claude", "op", nil},
		{"a name with a separator", home, "claude", "../op", nil},
	} {
		got, err := harnessKey(nil, c.home, c.kind, c.user, "group g")
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: harnessKey = %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	keyFile(t, home, "eve", "ANTHROPIC_API_KEY=sk-eve\n", 0o644)
	if _, err := harnessKey(nil, home, "claude", "eve", "group g"); err == nil || !dispatch.IsBlocked(err) ||
		!strings.Contains(err.Error(), "group g: the claude key of eve's login") || strings.Contains(err.Error(), "sk-eve") {
		t.Errorf("a file open to others: %v, want Blocked naming the session and no value", err)
	}
}

// A bead session reads the key of the login it burns (Session.Login), under
// its group's env_file, which wins where both name a variable; yardr's own
// entries win over both. A login_for group of a user with no file starts
// without a key, as it did before the yard kept one, and one whose user's
// file is there by hand starts on it.
func TestGroupEnvReadsTheLoginsHarnessKey(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	keyFile(t, home, "op", "ANTHROPIC_API_KEY=sk-op\nYARDR_ACTOR=someone-else\n", 0o600)
	h := &Herdr{YardHome: home}
	env := func(t *testing.T, login string, cfg map[string]any) map[string]string {
		t.Helper()
		got, err := h.groupEnv(dispatch.Job{Group: store.Group{Name: "g", Config: cfg}, Session: store.Session{ID: "s1", Login: login}})
		if err != nil {
			t.Fatal(err)
		}
		return envMap(got)
	}
	if got := env(t, "op", map[string]any{"kind": "claude"}); got["ANTHROPIC_API_KEY"] != "sk-op" || got["YARDR_ACTOR"] != "" {
		t.Errorf("a group on the operator's login: %v", got)
	}
	if got := env(t, "ada", map[string]any{"kind": "claude", "login_for": "ada"}); got["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("a login_for group of ada, with no file of hers, reads %q", got["ANTHROPIC_API_KEY"])
	}
	keyFile(t, home, "ada", "ANTHROPIC_API_KEY=sk-ada\n", 0o600)
	if got := env(t, "ada", map[string]any{"kind": "claude", "login_for": "ada"}); got["ANTHROPIC_API_KEY"] != "sk-ada" {
		t.Errorf("a login_for group of ada, with her file by hand, reads %q", got["ANTHROPIC_API_KEY"])
	}
	own := filepath.Join(t.TempDir(), "g.env")
	if err := os.WriteFile(own, []byte("ANTHROPIC_API_KEY=sk-group\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := env(t, "op", map[string]any{"kind": "claude", "env_file": own}); got["ANTHROPIC_API_KEY"] != "sk-group" {
		t.Errorf("the group's env_file under the key: %q, want the group's", got["ANTHROPIC_API_KEY"])
	}
	if got := env(t, "op", map[string]any{"kind": "codex"}); got["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("a kind with no login_env reads %q", got["ANTHROPIC_API_KEY"])
	}
}

// The key reaches the pane a bead's agent starts in, through the socket
// request that opens it, and the session's identity is yardr's.
func TestHerdrStartGivesTheHarnessKey(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	keyFile(t, home, "op", "ANTHROPIC_API_KEY=sk-op\n", 0o600)
	wt := seedEmptyRepo(t)
	h, calls, logs := envHerdr(t, wt)
	h.YardHome = home
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Stage: "build"},
		Group:   store.Group{Name: "g", Config: map[string]any{"kind": "claude"}},
		Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
		Session: store.Session{ID: "s1", Login: "op"},
	}
	handle, err := h.Start(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	env := envOf(apiCall(t, readCalls(t, calls), "tab.create"))
	if env["ANTHROPIC_API_KEY"] != "sk-op" || env["YARDR_ACTOR"] != "yard-13-build" {
		t.Errorf("the pane's env: %v", env)
	}
	if strings.Contains(logs.String(), "sk-op") || strings.Contains(fmt.Sprint(handle), "sk-op") || strings.Contains(cliCalls(readCalls(t, calls)), "sk-op") {
		t.Errorf("the key shows outside the pane's environment")
	}
}

// A crew member's line session reads the operator's key, and a
// conversation its user's: the login each burns. Its own files win.
func TestCrewSessionEnvReadsTheLoginsHarnessKey(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	keyFile(t, home, "op", "ANTHROPIC_API_KEY=sk-op\n", 0o600)
	keyFile(t, home, "hal", "ANTHROPIC_API_KEY=sk-hal\n", 0o600)
	halPath := filepath.Join(t.TempDir(), "hal.env")
	if err := os.WriteFile(halPath, []byte("CLAUDE_CONFIG_DIR=/tmp/hal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Herdr{YardHome: home, Store: st}
	env := func(t *testing.T, cfg map[string]any, user string) map[string]string {
		t.Helper()
		got, err := h.crewSessionEnv(ctx, store.CrewMember{Name: "yardmaster", Config: cfg}, t.TempDir(), user)
		if err != nil {
			t.Fatal(err)
		}
		return envMap(got)
	}
	if got := env(t, map[string]any{"kind": "claude"}, ""); got["ANTHROPIC_API_KEY"] != "sk-op" {
		t.Errorf("the line session: %v", got)
	}
	if got := env(t, map[string]any{"kind": "claude", "env_for_hal": halPath}, "hal"); got["ANTHROPIC_API_KEY"] != "sk-hal" || got["CLAUDE_CONFIG_DIR"] != "/tmp/hal" {
		t.Errorf("hal's conversation: %v", got)
	}
	own := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(own, []byte("ANTHROPIC_API_KEY=sk-crew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := env(t, map[string]any{"kind": "claude", "env_file": own}, ""); got["ANTHROPIC_API_KEY"] != "sk-crew" {
		t.Errorf("the crew member's env_file under the key: %q, want its own", got["ANTHROPIC_API_KEY"])
	}
	// With no operator set, the line session burns nobody's key.
	if got, err := (&Herdr{YardHome: home}).crewSessionEnv(ctx, store.CrewMember{Name: "yardmaster", Config: map[string]any{"kind": "claude"}}, t.TempDir(), ""); err != nil || len(got) != 0 {
		t.Errorf("with no store: %q, %v", got, err)
	}
}
