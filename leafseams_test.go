package main

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// The store-as-leaf splits move shared definitions into internal/store,
// but two pieces of knowledge cannot move with them, and are pinned here
// instead: the store's no-sync switch holds its full YARDR_ key, since
// the store cannot import config for the prefix, and config's command
// runner expands a leading ~/ itself, since it cannot import the store
// for ExpandPath (the store imports source, which imports config). If
// either side's rule changes, these fail naming the two to change
// together.
func TestNoSyncKeyMatchesConfig(t *testing.T) {
	t.Parallel()
	if store.NoSyncEnv != config.Key("STORE_NOSYNC") {
		t.Errorf("store.NoSyncEnv = %q, want config.Key(\"STORE_NOSYNC\") = %q", store.NoSyncEnv, config.Key("STORE_NOSYNC"))
	}
}

func TestTildeRuleMatchesExpandPath(t *testing.T) {
	prog := "~/definitely-not-a-yardr-program"
	for _, home := range []string{t.TempDir(), "relative-home"} {
		t.Setenv("HOME", home)
		want, err := store.ExpandPath(prog)
		if err != nil {
			t.Fatalf("store.ExpandPath(%q): %v", prog, err)
		}
		// Missing names the program it looked for, so a ~/ program refused
		// names the path config expanded it to: the same one.
		err = config.Command{What: "seam", Argv: []string{prog}}.Missing()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("with HOME=%q a ~/ program is refused as %v, want it naming %q", home, err, want)
		}
	}
}
