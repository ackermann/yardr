package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGetenv(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "new")
	if got := Getenv("ACTOR"); got != "new" {
		t.Fatalf("Getenv(ACTOR) = %q, want new", got)
	}
	if got := Actor(); got != "new" {
		t.Fatalf("Actor = %q, want new", got)
	}
	t.Setenv("YARDR_ACTOR", "")
	if got := Getenv("ACTOR"); got != "" {
		t.Fatalf("unset: %q", got)
	}
}

// Without YARDR_ACTOR the actor is the login name: $USER, else the OS
// user's, else "unknown". The --actor flag says so ("default $YARDR_ACTOR,
// else $USER"), and a login name that is nobody's is refused with both
// ways to name one (yardr-sang).
func TestActorFallsBackToLogin(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "kloni")
	if got := Actor(); got != "kloni" {
		t.Fatalf("USER=kloni: Actor = %q, want kloni", got)
	}
	t.Setenv("USER", "")
	if got := Actor(); got == "" || got == "kloni" {
		t.Fatalf("no USER: Actor = %q", got)
	}
}

func TestEnv(t *testing.T) {
	if got := Env("BEAD", "web-3"); !slices.Equal(got, []string{"YARDR_BEAD=web-3"}) {
		t.Fatalf("Env = %v", got)
	}
}

func TestIsVar(t *testing.T) {
	for key, want := range map[string]bool{"YARDR_BEAD": true, "YARDS_BEAD": false, "YARDR_SESSION": false,
		"BEAD": false, "YARDR_": false, "PATH": false} {
		if got := IsVar(key, "BEAD", "HOME"); got != want {
			t.Errorf("IsVar(%q, BEAD, HOME) = %v", key, got)
		}
	}
	if !IsVar("YARDR_ANYTHING") || IsVar("YARDS_X") {
		t.Error("IsVar with no names: any yardr variable")
	}
}

func TestDefaultHome(t *testing.T) {
	u := t.TempDir()
	if got := DefaultHome(u); got != filepath.Join(u, ".yardr") {
		t.Fatalf("neither exists: %s", got)
	}
	if err := os.Mkdir(filepath.Join(u, ".yard"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultHome(u); got != filepath.Join(u, ".yardr") {
		t.Fatalf("only ~/.yard exists: %s", got)
	}
}

func TestLoadHome(t *testing.T) {
	u := t.TempDir()
	t.Setenv("HOME", u)
	t.Setenv("YARDR_HOME", filepath.Join(u, "new"))
	c, err := Load()
	if err != nil || c.Home != filepath.Join(u, "new") {
		t.Fatalf("YARDR_HOME: %+v %v", c, err)
	}

	// The home yard had is not read: a ~/.yard left behind is not the yard.
	t.Setenv("YARDR_HOME", "")
	os.Mkdir(filepath.Join(u, ".yard"), 0o700)
	if c, err = Load(); err != nil || c.Home != filepath.Join(u, ".yardr") {
		t.Fatalf("default home: %+v %v", c, err)
	}
}

func TestParseRetention(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour, "off": 0, "0": 0, "0d": 0,
	} {
		if got, err := ParseRetention(in); err != nil || got != want {
			t.Errorf("ParseRetention(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "-1d", "-5h", "1.5d"} {
		if _, err := ParseRetention(in); err == nil {
			t.Errorf("ParseRetention(%q) accepted", in)
		}
	}
	t.Setenv("YARDR_EVENT_RETENTION", "")
	if got, err := Retention("EVENT_RETENTION", time.Hour); err != nil || got != time.Hour {
		t.Fatalf("unset: %v %v", got, err)
	}
	t.Setenv("YARDR_EVENT_RETENTION", "7d")
	if got, err := Retention("EVENT_RETENTION", time.Hour); err != nil || got != 7*24*time.Hour {
		t.Fatalf("7d: %v %v", got, err)
	}
	t.Setenv("YARDR_EVENT_RETENTION", "soon")
	if _, err := Retention("EVENT_RETENTION", time.Hour); err == nil {
		t.Fatal("bad value accepted")
	}
}

// A home whose socket would not fit a unix socket path is refused, and
// nothing is created for it.
// A relative YARDR_HOME is made absolute at load, once: the server, the
// agents and the scripts are started with it and run elsewhere, where the
// relative path would name another directory (or none: exit 2).
func TestLoadMakesHomeAbsolute(t *testing.T) {
	// A short directory: the absolute socket path must fit MaxSocket.
	dir, err := os.MkdirTemp("", "ych")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(dir)
	t.Setenv("YARDR_HOME", "y1/")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "y1"); c.Home != want {
		t.Fatalf("Home = %q, want %q", c.Home, want)
	}
	if st, err := os.Stat(filepath.Join(dir, "y1")); err != nil || !st.IsDir() {
		t.Fatalf("home not created where it was named: %v", err)
	}
	if !filepath.IsAbs(c.Socket()) {
		t.Fatalf("Socket = %q", c.Socket())
	}
}

func TestLoadRefusesALongHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), strings.Repeat("h", MaxSocket))
	t.Setenv("YARDR_HOME", home)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "YARDR_HOME") || !strings.Contains(err.Error(), "--home") {
		t.Fatalf("Load = %v", err)
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("the refused home was created")
	}
	// Exactly at the bound is fine.
	short := t.TempDir()
	home = short + "/" + strings.Repeat("h", MaxSocket-len(short)-len("/yard.sock")-1)
	t.Setenv("YARDR_HOME", home)
	if c, err := Load(); err != nil || len(c.Socket()) != MaxSocket {
		t.Fatalf("Load at the bound: %v %v", c, err)
	}
}

// The default home is compared resolved: a trailing slash and a symlink to
// it count (yard-0wys). The ~/.yard it was before the rename does not.
func TestIsDefaultHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	legacy := filepath.Join(user, ".yard")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(user, ".yardr")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{home, home + "/", link, filepath.Join(user, "x", "..", ".yardr")} {
		if !IsDefaultHome(h) {
			t.Errorf("%s is not the default home", h)
		}
	}
	for _, h := range []string{legacy, filepath.Join(t.TempDir(), "y3"), "/tmp/y3"} {
		if IsDefaultHome(h) {
			t.Errorf("%s is the default home", h)
		}
	}
}

// UseHome, the --home flag, wins over the variable, is
// made absolute once, and is what a child's environment then says.
func TestUseHome(t *testing.T) {
	dir, err := os.MkdirTemp("", "ych")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(dir)
	t.Setenv("YARDR_HOME", filepath.Join(dir, "variable"))
	if err := UseHome("y1/"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "y1")
	if os.Getenv("YARDR_HOME") != want {
		t.Fatalf("YARDR_HOME=%s, want %s", os.Getenv("YARDR_HOME"), want)
	}
	if c, err := Load(); err != nil || c.Home != want {
		t.Fatalf("Load = %+v, %v; want %s", c, err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "variable")); err == nil {
		t.Fatal("the variable's home was created")
	}
	if err := UseHome(""); err == nil || os.Getenv("YARDR_HOME") != want {
		t.Fatalf("UseHome(\"\") = %v, home %s", err, os.Getenv("YARDR_HOME"))
	}
}

// What a yard starts for work is started without the settings of the
// yard's server, and with everything else it had;
// where the environment can only be added to, they are set to nothing,
// which Getenv reads as unset.
func TestWithoutServerSettings(t *testing.T) {
	environ := []string{"PATH=/bin", "YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_BEAD=web-3",
		"YARDR_EVENT_RETENTION=1d", "YARDR_HOOK_PAYLOAD_RETENTION=3d",
		"YARDR_INBOX_RETENTION=5d", "YARDR_HOOK_ADDRESS=x"}
	before := slices.Clone(environ)
	got := WithoutServerSettings(environ)
	if want := []string{"PATH=/bin", "YARDR_BEAD=web-3", "YARDR_INBOX_RETENTION=5d", "YARDR_HOOK_ADDRESS=x"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutServerSettings = %v, want %v", got, want)
	}
	if !slices.Equal(environ, before) {
		t.Fatalf("the caller's environ was changed: %v", environ)
	}

	blank := BlankServerSettings()
	if len(blank) != len(ServerSettings) {
		t.Fatalf("BlankServerSettings = %v", blank)
	}
	for _, kv := range blank {
		k, v, _ := strings.Cut(kv, "=")
		if !IsVar(k, ServerSettings...) || v != "" {
			t.Errorf("BlankServerSettings has %q", kv)
		}
	}
	for _, name := range ServerSettings {
		t.Setenv(prefix+name, "")
		if got := Getenv(name); got != "" {
			t.Errorf("a blank %s reads as %q", name, got)
		}
	}
}
