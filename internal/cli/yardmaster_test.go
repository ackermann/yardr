package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A yard set up before a yard had a default pack, whose yardmaster the
// operator called chief, has its role in <home>/roles/chief.md, and its
// crew member's prompt is that path. Init again leaves all of it as it is: the role is
// not refreshed, though never edited, no yardmaster.md is written beside it
// and nothing of a pack is installed. The crew member keeps its name and
// its prompt.
func TestInitKeepsAYardmasterOfAnotherName(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me", env: testEnv(t, home)}
	// The yard as the build before made it and the store's upgrade left it.
	dir := filepath.Join(home, "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("# Chief\n\nThe build before wrote this for Jo.\n")
	// .shipped.json is where that build recorded the hash of each role it
	// wrote, to tell an edited one from its own.
	manifest := []byte(`{"chief.md": "sha256:0000"}`)
	for name, body := range map[string][]byte{"chief.md": old, ".shipped.json": manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prompt := filepath.Join(dir, "chief.md")
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "chief", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "prompt": prompt}}, "me"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"default_kind": "claude", "operator": "Jo", "yardmaster": "chief"} {
		if err := st.SetSetting(ctx, k, v, "Jo"); err != nil {
			t.Fatal(err)
		}
	}

	out, err := run(t, a.rootCmd(), "init")
	if err != nil {
		t.Fatalf("init again: %v\n%s", err, out)
	}
	for _, want := range []string{"yardmaster chief, as set before", "  yardmaster chief, already a crew member: kept as it was",
		"  roles      " + dir + ", from an earlier yardr, is read as before", "init no longer writes or refreshes it"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q:\n%s", want, out)
		}
	}
	rs, err := st.Crew(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "chief" || rs[0].Config["prompt"] != prompt {
		t.Fatalf("crew members = %+v %v", rs, err)
	}
	if b, _ := os.ReadFile(prompt); !bytes.Equal(b, old) {
		t.Errorf("chief.md was rewritten:\n%s", b)
	}
	var files []string
	for p := range snapshot(t, home) {
		files = append(files, strings.TrimPrefix(p, home+"/"))
	}
	if slices.Sort(files); !slices.Equal(files, []string{"roles/.shipped.json", "roles/chief.md"}) {
		t.Errorf("init again wrote into the home: %v", files)
	}
}

// Help lists every command and flag yardr takes: none is hidden. A word
// yardr had before was kept for a release as a hidden command or flag
// (yardr-4c01 dropped the last), so one that help does not list is one
// nobody was told of.
func TestHelpListsEveryCommandAndFlag(t *testing.T) {
	t.Parallel()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			t.Errorf("%s is hidden", c.CommandPath())
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				t.Errorf("%s --%s is hidden", c.CommandPath(), f.Name)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk((&app{st: storetest.New(t), actor: "me"}).rootCmd())
}
