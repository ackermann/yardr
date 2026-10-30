package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// exposePack is the starter pack in a directory of the test's, asking that
// its yardmaster be exposed to the yard's peers and nothing else of the
// operator.
func exposePack(t *testing.T) string {
	t.Helper()
	dir := openPack(t, "linear")
	path := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(text), "    open_threads: [linear]\n") {
		t.Fatalf("pack.yaml: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(text), "    open_threads: [linear]\n", "    expose_to_peers: true\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// linkedTo adds a peer to a's yard.
func linkedTo(t *testing.T, a *app, name string) {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddPeer(context.Background(), store.Peer{Name: name, Key: store.Public(k)}, "me"); err != nil {
		t.Fatal(err)
	}
}

// answerExpose answers "Expose <name> to <peer>?" with yes for the peers
// in yes and everything else with its default. It returns the titles of
// the confirmations asked, in order, and fails on such a question whose
// default is yes.
func answerExpose(t *testing.T, yes *[]string) *[]string {
	t.Helper()
	var asked []string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			c, ok := f.(*huh.Confirm)
			if !ok {
				continue
			}
			title := strings.TrimSpace(strings.SplitN(c.View(), "\n", 2)[0])
			asked = append(asked, title)
			peer, is := strings.CutPrefix(title, "Expose boss to ")
			if !is {
				continue
			}
			if c.GetValue() != false {
				t.Errorf("%q defaults to yes", title)
			}
			if slices.Contains(*yes, strings.TrimSuffix(peer, "?")) {
				c.WithKeyMap(huh.NewDefaultKeyMap())
				c.Focus()
				c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			}
		}
		return nil
	})
	return &asked
}

func exposedTo(t *testing.T, a *app, peer string) string {
	t.Helper()
	p, err := a.st.GetPeer(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(p.Expose, ",")
}

// A pack that asks to have its crew member exposed (expose_to_peers) makes
// the import ask the operator, once per peer the yard has, after its other
// questions and with no as the default. pack check says so under what an
// import would ask and exposes nobody. A yes exposes the crew member on
// that link, a no is recorded, and the second apply asks nothing; a peer
// linked later is asked about alone.
func TestPackApplyAsksTheOperatorWhomToExposeTo(t *testing.T) {
	a := openApp(t)
	setInteractive(t, true)
	ctx := context.Background()
	dir := exposePack(t)
	linkedTo(t, a, "north")
	linkedTo(t, a, "south")
	yes := []string{"north"}
	asked := answerExpose(t, &yes)
	args := append([]string{"apply", dir, "--depot", "web"}, starterFlags...)

	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nan import would ask\n",
		"  the operator: expose boss to north, south (each as yardr peer allow <peer> --expose boss)? [no]\n",
		"\nexpose to peers, for crew member boss\n  asks      north, south: the operator is asked, once per peer, and the answer without one is no\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if exposedTo(t, a, "north")+exposedTo(t, a, "south") != "" {
		t.Fatal("pack check exposed someone")
	}

	if out, err = run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(*asked); n < 3 || !slices.Equal((*asked)[n-2:], []string{"Expose boss to north?", "Expose boss to south?"}) || !strings.HasPrefix((*asked)[n-3], "Apply pack starter") {
		t.Fatalf("asked %q, want one question per peer, after the confirmation", *asked)
	}
	if exposedTo(t, a, "north") != "boss" || exposedTo(t, a, "south") != "" {
		t.Fatalf("north exposes %q, south %q\n%s", exposedTo(t, a, "north"), exposedTo(t, a, "south"), out)
	}
	wantText(t, out, "\ncrew member boss is now exposed to north: ",
		"\ncrew member boss was not exposed to south: the no is recorded and not asked again (yardr peer allow <peer> --expose boss exposes it after all)\n")
	if show, err := run(t, a.peerCmd(), "show", "north"); err != nil || !strings.Contains(show, "  expose   boss") {
		t.Fatalf("peer show: %v\n%s", err, show)
	}

	// Twice is once.
	*asked = nil
	head, _ := a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "nothing changed") || len(*asked) != 0 {
		t.Fatalf("second apply: %v, asked %q\n%s", err, *asked, out)
	}
	wantText(t, out, "  has       north: it is exposed there already, and nothing is asked\n",
		"  declined  south: an earlier import was told no (yardr peer allow <peer> --expose boss exposes it)\n")
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("the second apply logged %+v", evs)
	}

	// A peer linked since is asked about, and no other.
	linkedTo(t, a, "east")
	yes = []string{"east"}
	if out, err = run(t, a.packCmd(), args...); err != nil || !slices.Equal(*asked, []string{"Expose boss to east?"}) || exposedTo(t, a, "east") != "boss" {
		t.Fatalf("with a new peer: %v, asked %q\n%s", err, *asked, out)
	}
}

// Without a terminal, with --yes and with --json nothing is asked and
// nobody is exposed: the import goes through, records no answer, and names
// the command that exposes. Run by anyone but the operator in a terminal
// it asks nothing either, and says whose it is.
func TestPackApplyExposesNobodyUnasked(t *testing.T) {
	dir := exposePack(t)
	args := append([]string{"apply", dir, "--depot", "web", "--yes"}, starterFlags...)
	const names = "crew member boss was not exposed to north: nothing is asked here, and the answer without a terminal is no. yardr peer allow <peer> --expose boss exposes it, a peer at a time"
	record := func(a *app) map[string]map[string]bool {
		recs, err := a.st.Packs(context.Background())
		if err != nil || len(recs) != 1 {
			t.Fatalf("records = %+v, %v", recs, err)
		}
		return recs[0].Expose
	}

	a := openApp(t)
	linkedTo(t, a, "north")
	setInteractive(t, false)
	out, err := run(t, a.packCmd(), args...)
	if err != nil || !strings.Contains(out, "applied pack starter to depot web") || !strings.Contains(out, names) {
		t.Fatalf("%v\n%s", err, out)
	}
	if exposedTo(t, a, "north") != "" || record(a) != nil {
		t.Fatalf("north exposes %q, record %v", exposedTo(t, a, "north"), record(a))
	}
	// --yes in a terminal asks nothing either.
	setInteractive(t, true)
	yes := []string{"north"}
	asked := answerExpose(t, &yes)
	if out, err = run(t, a.packCmd(), args...); err != nil || len(*asked) != 0 || !strings.Contains(out, names) || exposedTo(t, a, "north") != "" {
		t.Fatalf("--yes in a terminal: %v, asked %q\n%s", err, *asked, out)
	}
	// And --json names the command as data.
	a.json = true
	out, err = run(t, a.packCmd(), args...)
	var got struct {
		Expose struct {
			Answers map[string]map[string]bool `json:"answers"`
			Notes   []string                   `json:"notes"`
		} `json:"expose"`
		Plan   pack.Plan   `json:"plan"`
		Result pack.Result `json:"result"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got.Expose.Notes) != 1 || got.Expose.Notes[0] != names ||
		len(got.Expose.Answers) != 0 || len(got.Result.Exposed) != 0 || len(got.Plan.Expose) != 1 || !slices.Equal(got.Plan.Expose[0].Asks, []string{"north"}) {
		t.Fatalf("json: %v\n%s", err, out)
	}
	a.json = false

	// Anyone but the operator: the rest of the import is done.
	a.actor = "sam"
	*asked = nil
	out, err = run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web"}, starterFlags...)...)
	if err != nil || slices.ContainsFunc(*asked, func(s string) bool { return strings.HasPrefix(s, "Expose ") }) || exposedTo(t, a, "north") != "" ||
		!strings.Contains(out, "crew member boss was not exposed to north: only the operator (me) may expose someone of the yard to a peer, and this is sam.") {
		t.Fatalf("as sam: %v, asked %q\n%s", err, *asked, out)
	}
}
