package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGetenvPrefersNewName(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "new")
	t.Setenv("YARD_ACTOR", "old")
	if got := Getenv("ACTOR"); got != "new" {
		t.Fatalf("both set: %q, want new", got)
	}
	if got := Actor(); got != "new" {
		t.Fatalf("Actor = %q, want new", got)
	}
	t.Setenv("YARDR_ACTOR", "")
	if got := Getenv("ACTOR"); got != "old" {
		t.Fatalf("only old set: %q, want old", got)
	}
	t.Setenv("YARD_ACTOR", "")
	if got := Getenv("ACTOR"); got != "" {
		t.Fatalf("neither set: %q", got)
	}
}

func TestEnvSetsBothNames(t *testing.T) {
	if got := Env("BEAD", "web-3"); !slices.Equal(got, []string{"YARDR_BEAD=web-3", "YARD_BEAD=web-3"}) {
		t.Fatalf("Env = %v", got)
	}
}

func TestIsVar(t *testing.T) {
	for key, want := range map[string]bool{"YARDR_BEAD": true, "YARD_BEAD": true, "YARDR_SESSION": false,
		"BEAD": false, "YARDR_": false, "PATH": false} {
		if got := IsVar(key, "BEAD", "HOME"); got != want {
			t.Errorf("IsVar(%q, BEAD, HOME) = %v", key, got)
		}
	}
	if !IsVar("YARD_ANYTHING") || IsVar("YARDS_X") {
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
	if got := DefaultHome(u); got != filepath.Join(u, ".yard") {
		t.Fatalf("only ~/.yard exists: %s", got)
	}
	if err := os.Mkdir(filepath.Join(u, ".yardr"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultHome(u); got != filepath.Join(u, ".yardr") {
		t.Fatalf("both exist: %s", got)
	}
}

func TestLoadHome(t *testing.T) {
	u := t.TempDir()
	t.Setenv("HOME", u)
	t.Setenv("YARDR_HOME", "")
	t.Setenv("YARD_HOME", filepath.Join(u, "old"))
	c, err := Load()
	if err != nil || c.Home != filepath.Join(u, "old") {
		t.Fatalf("YARD_HOME fallback: %+v %v", c, err)
	}
	t.Setenv("YARDR_HOME", filepath.Join(u, "new"))
	if c, err = Load(); err != nil || c.Home != filepath.Join(u, "new") {
		t.Fatalf("YARDR_HOME: %+v %v", c, err)
	}

	t.Setenv("YARDR_HOME", "")
	t.Setenv("YARD_HOME", "")
	os.Mkdir(filepath.Join(u, ".yard"), 0o700)
	if c, err = Load(); err != nil || c.Home != filepath.Join(u, ".yard") || !c.IsLegacyHome() {
		t.Fatalf("legacy home: %+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(u, ".yardr")); err == nil {
		t.Fatal("Load created ~/.yardr next to the legacy home")
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
// agents and the gate are started with it and run elsewhere, where the
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
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "YARDR_HOME") {
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

// The default home is compared resolved: a trailing slash, a symlink to it
// and the old ~/.yard where only that exists all count (yard-0wys).
func TestIsDefaultHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	legacy := filepath.Join(user, ".yard")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if !IsDefaultHome(legacy) || IsDefaultHome(filepath.Join(user, ".yardr")) {
		t.Fatal("only ~/.yard exists: it is the default, ~/.yardr is not")
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
