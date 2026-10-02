package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
