package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// loginFolderAccount reads the account a folder's login names: Claude's
// folder writes its address, and anything else — no folder, no login, no
// file — reads as none.
func TestLoginFolderAccount(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return filepath.Dir(p)
	}
	in := write("in/.claude.json", `{"oauthAccount":{"emailAddress":"hal@example.com"}}`)
	write("out/.claude.json", `{"oauthAccount":{"emailAddress":""}}`)
	write("signedout/.claude.json", `{"numStartups":3}`)
	write("broken/.claude.json", `{"oauthAccount":`)
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"the folder's account", []string{"CLAUDE_CONFIG_DIR=" + in}, "hal@example.com"},
		{"the last folder wins, as the harness reads it", []string{"CLAUDE_CONFIG_DIR=" + in, "CLAUDE_CONFIG_DIR=" + dir + "/missing"}, ""},
		{"no folder in the env", []string{"CODEX_HOME=" + in}, ""},
		{"no env at all", nil, ""},
		{"an empty address is none", []string{"CLAUDE_CONFIG_DIR=" + dir + "/out"}, ""},
		{"signed out is none", []string{"CLAUDE_CONFIG_DIR=" + dir + "/signedout"}, ""},
		{"a broken file is none", []string{"CLAUDE_CONFIG_DIR=" + dir + "/broken"}, ""},
		{"a missing folder is none", []string{"CLAUDE_CONFIG_DIR=" + dir + "/missing"}, ""},
	} {
		if got := loginFolderAccount(tc.env); got != tc.want {
			t.Errorf("%s: account %q, want %q", tc.name, got, tc.want)
		}
	}
}

// defaultClaudeAccount is the machine's own login: the folder the
// environment names, else the file in the user's home.
func TestDefaultClaudeAccount(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := defaultClaudeAccount(); got != "" {
		t.Fatalf("no login anywhere: account %q, want none", got)
	}
	if err := os.WriteFile(filepath.Join(user, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"me@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultClaudeAccount(); got != "me@example.com" {
		t.Errorf("the home's login: account %q, want me@example.com", got)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"other@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", other)
	if got := defaultClaudeAccount(); got != "other@example.com" {
		t.Errorf("the environment's folder: account %q, want other@example.com", got)
	}
}

// groupFolderAccount reads the folder the group's env_file names, and
// nothing else: a group with no file, or one that cannot be read, has no
// account to show.
func TestGroupFolderAccount(t *testing.T) {
	login := t.TempDir()
	if err := os.WriteFile(filepath.Join(login, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"hal@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "hal.env")
	if err := os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR="+login+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": env}}
	if got := groupFolderAccount(g); got != "hal@example.com" {
		t.Errorf("account %q, want hal@example.com", got)
	}
	g.Config = map[string]any{"kind": "claude", "login_for": "hal"}
	if got := groupFolderAccount(g); got != "" {
		t.Errorf("no env_file: account %q, want none", got)
	}
	g.Config["env_file"] = filepath.Join(t.TempDir(), "missing.env")
	if got := groupFolderAccount(g); got != "" {
		t.Errorf("an unreadable env_file: account %q, want none", got)
	}
	open := filepath.Join(t.TempDir(), "open.env")
	if err := os.WriteFile(open, []byte("CLAUDE_CONFIG_DIR="+login+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.Config["env_file"] = open
	if got := groupFolderAccount(g); got != "" {
		t.Errorf("an env_file open to others: account %q, want none", got)
	}
}

// loginFolder is a login folder holding the account address, with an env
// file carrying it, both as the operator lays them.
func loginFolder(t *testing.T, account string) (login, env string) {
	t.Helper()
	login = t.TempDir()
	if account != "" {
		if err := os.WriteFile(filepath.Join(login, ".claude.json"),
			[]byte(`{"oauthAccount":{"emailAddress":"`+account+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env = filepath.Join(t.TempDir(), "group.env")
	if err := os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR="+login+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return login, env
}

// group show prints, beside login_for, the account the folder's login
// names, so a mismatch shows to the eye; --json carries it as
// folder_login. Where the folder names none, or the group declares none,
// the line stands as it did.
func TestGroupShowPrintsFolderLogin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	_, logged := loginFolder(t, "hal@example.com")
	_, fresh := loginFolder(t, "")
	if err := st.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": logged}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "fresh", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": fresh}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "plain", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "env_file": logged}}, "me"); err != nil {
		t.Fatal(err)
	}
	out := mustAs(t, st, "me", "group", "show", "hal-builders")
	if !strings.Contains(out, "  login_for=hal (folder login: hal@example.com)\n") {
		t.Errorf("group show lacks the folder login beside login_for:\n%s", out)
	}
	var shown struct {
		Group       store.Group
		FolderLogin string `json:"folder_login"`
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "me", "group", "show", "hal-builders", "--json")), &shown); err != nil ||
		shown.FolderLogin != "hal@example.com" {
		t.Errorf("group show --json folder_login = %+v, %v", shown, err)
	}
	if out := mustAs(t, st, "me", "group", "show", "fresh"); !strings.Contains(out, "  login_for=hal\n") ||
		strings.Contains(out, "folder login") {
		t.Errorf("a folder with no login shows as:\n%s", out)
	}
	if out := mustAs(t, st, "me", "group", "show", "plain"); strings.Contains(out, "folder login") {
		t.Errorf("a group without login_for shows a folder login:\n%s", out)
	}
	if out := mustAs(t, st, "me", "group", "show", "plain", "--json"); strings.Contains(out, "folder_login") {
		t.Errorf("a group without login_for carries folder_login:\n%s", out)
	}
}

// doctor names a group the operator's for routing whose folder's login
// names another account; the same account, no folder login, or no machine
// login to tell it by warn of nothing.
func TestDoctorNamesForeignFolderLogin(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.WriteFile(filepath.Join(user, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"me@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	_, foreign := loginFolder(t, "hal@example.com")
	_, own := loginFolder(t, "me@example.com")
	_, fresh := loginFolder(t, "")
	for _, g := range []store.Group{
		{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": foreign}},
		{Name: "mine", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": own}},
		{Name: "unlogged", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": fresh}},
		{Name: "hal-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": foreign}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	rep, _ := doctorJSON(t, st)
	joined := strings.Join(rep.State.Config, "\n")
	if !strings.Contains(joined, "group builders is the operator's for routing and its folder's login names hal@example.com, not the operator's (me@example.com)") {
		t.Errorf("doctor's config notes lack the foreign login:\n%s", joined)
	}
	for _, name := range []string{"mine", "unlogged", "hal-builders"} {
		for _, n := range rep.State.Config {
			if strings.Contains(n, "group "+name+" is the operator's for routing") {
				t.Errorf("doctor names %s's folder login: %q", name, n)
			}
		}
	}
	// With no machine login there is nothing to tell the folder's by.
	if err := os.Remove(filepath.Join(user, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	rep, _ = doctorJSON(t, st)
	for _, n := range rep.State.Config {
		if strings.Contains(n, "its folder's login names") {
			t.Errorf("doctor names a folder login with no machine login to tell it by: %q", n)
		}
	}
}
