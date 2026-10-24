package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// standinGoogle puts a gws and a curl on PATH that answer as the real
// ones do for what google-mint runs: gws's login for op@example.com and
// its refresh credentials, and the token endpoint's access token. The
// mint's environment is a short list (PATH, HOME, USER, LOGNAME, the base
// and the proxy and CA variables of the process, of which a test binary has
// none: internal/storetest drops them) and names no endpoint, so a stand-in
// on PATH is how a test keeps it from Google.
func standinGoogle(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	for name, text := range map[string]string{
		"gws": `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"user":"op@example.com"}' ;;
"auth export") echo '{"client_id":"id-1","client_secret":"secret-1","refresh_token":"1//refresh-1"}' ;;
*) echo "stand-in gws takes auth status|export" >&2; exit 2 ;;
esac
`,
		"curl": `#!/bin/sh
cat >/dev/null
printf '{"access_token":"ya29.standin","expires_in":3599}\n200'
`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The tokens a process holds are keyed by the mint's program, which
	// is under this test's own home: no other test's token is taken.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cardOf is the card of ask seq on bead, its lines joined.
func cardOf(t *testing.T, st *store.Store, bead string, seq uint64) string {
	t.Helper()
	notes, err := st.Notes(context.Background(), bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range store.ResolveAsks(notes, notes[0].At) {
		if r.Note.Seq == seq && r.Ask.Run != nil {
			return strings.Join(r.Ask.Run.Does, "\n")
		}
	}
	t.Fatalf("no run ask %d on %s", seq, bead)
	return ""
}

// Applying google-mail by its card alone sets a working mint: the card
// names google-mint with where it is copied and its sha256, the carrying
// out copies exactly those bytes, the source mints with the copy though
// no mint_command was set, and the account given is all it takes for a
// token (yardr-66qd).
func TestCarryGoogleMailSetsItsMint(t *testing.T) {
	ctx := context.Background()
	standinGoogle(t)
	st, home, bead := carryYard(t)
	want, err := fs.ReadFile(packs.FS, "google-mail/google-mint")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.AddAsk(ctx, bead, packAskOf("google-mail"), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	card := cardOf(t, st, bead, seq)
	if line := "minted from GOOGLE_ACCOUNT by the declaration's own mint command [google-mint] (command file google-mint, copied to <home>/sources/google-mail/google-mint: sha256 " + sha(want) + ", mode 0700)"; !strings.Contains(card, line) {
		t.Fatalf("the card does not name the mint with its digest (%q):\n%s", line, card)
	}
	if _, err := st.ApproveAsk(ctx, bead, seq, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	copyAt := filepath.Join(home, "sources", "google-mail", "google-mint")
	got, err := os.ReadFile(copyAt)
	if err != nil || string(got) != string(want) {
		t.Fatalf("the copy of google-mint is not the pack's bytes: %v", err)
	}
	if info, err := os.Stat(copyAt); err != nil || info.Mode().Perm() != source.CopyMode {
		t.Fatalf("the copy stands at %v, %v", info, err)
	}
	src, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if src.MintCommand != "" || len(src.Mint) != 1 || src.Mint[0] != copyAt {
		t.Fatalf("the source mints with %q (mint_command %q), want the copy %s alone", src.Mint, src.MintCommand, copyAt)
	}
	// The account, as its secret card gives it: then the mint mints.
	if _, err := st.KeySource(ctx, "google-mail", map[string]string{"GOOGLE_ACCOUNT": "op@example.com"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if src, err = st.GetSource(ctx, "google-mail"); err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	if state, err := d.CredentialState(src.EnvFile, src.Mint); err != nil || !strings.HasSuffix(state, ", minted with "+copyAt) {
		t.Fatalf("credential state = %q, %v", state, err)
	}
	token, state, err := d.ReadCredential(ctx, src.EnvFile, src.Mint, source.Read)
	if err != nil || token != "ya29.standin" || !strings.Contains(state, "minted, good until") {
		t.Fatalf("the mint gave %q (%q), %v", token, state, err)
	}
}

// A pack whose google-mint changed after the yard applied it is a change
// the operator approves again: its card shows the copy's old and new
// sha256 with the diff counted, its plan names the file's change, and
// nothing of the yard's copy moves until one of them is approved.
func TestGoogleMintChangeIsShown(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	askApprove(t, st, bead, packAskOf("google-mail"))
	drain(t, st, home, "boot-one")
	copyAt := filepath.Join(home, "sources", "google-mail", "google-mint")
	before, err := os.ReadFile(copyAt)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(before), "set -eu\n", "set -eu\n# a later pack's mint\n", 1)
	if changed == string(before) {
		t.Fatal("the change changed nothing")
	}

	// The card: the server's reading of a pack whose mint changed.
	reading, err := readEmbeddedPack(ctx, "google-mail", st, home)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range reading.Sources[0].Files {
		if f.Program == "google-mint" {
			reading.Sources[0].Files[i].Text, reading.Sources[0].Files[i].SHA256 = changed, sha([]byte(changed))
		}
	}
	st.SetPackReader(func(context.Context, string, store.API) (*store.PackReading, error) { return reading, nil })
	seq, err := st.AddAsk(ctx, bead, packAskOf("google-mail"), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	card := cardOf(t, st, bead, seq)
	if line := "command file google-mint, copied to <home>/sources/google-mail/google-mint: sha256 " + sha(before) + " -> sha256 " + sha([]byte(changed)) + ", mode 0700 (1 insertions, 0 deletions)"; !strings.Contains(card, line) {
		t.Fatalf("the card does not show the mint's change (%q):\n%s", line, card)
	}

	// The plan a terminal apply prints and asks yes of.
	sub, err := fs.Sub(packs.FS, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	if err := fs.WalkDir(sub, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := fs.ReadFile(sub, p)
		m[p] = &fstest.MapFile{Data: data, Mode: 0o755}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m["google-mint"] = &fstest.MapFile{Data: []byte(changed), Mode: 0o755}
	p, err := pack.Load(m, "a later google-mail")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pack.Build(ctx, st, p, pack.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	var diff *pack.Diff
	for _, o := range plan.Objects {
		for i, c := range o.Changes {
			if o.Kind == "source" && c.Field == "file google-mint" {
				diff = &o.Changes[i]
			}
		}
	}
	if diff == nil || diff.Old != "sha256 "+sha(before)[:12] || diff.New != "sha256 "+sha([]byte(changed))[:12] {
		t.Fatalf("the plan shows the mint's change as %+v", diff)
	}

	// Asked and planned, approved by nobody: the yard's copy stands.
	if now, err := os.ReadFile(copyAt); err != nil || string(now) != string(before) {
		t.Fatalf("the copy moved before anyone approved the change: %v", err)
	}
}

// A mint_command the operator set by hand wins over the pack's own and
// is kept by the pack applied again; a crew member cannot set one, typed
// or by a card, since which program runs with a person's login is the
// operator's alone.
func TestGoogleMintHandSetWins(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	askApprove(t, st, bead, packAskOf("google-mail"))
	drain(t, st, home, "boot-one")
	mine := "/Users/me/bin/my-mint --quiet"
	if _, err := st.SetSource(ctx, "google-mail", store.SourceMintCommand, mine, "mckean"); err != nil {
		t.Fatal(err)
	}
	src, err := st.GetSource(ctx, "google-mail")
	if err != nil || strings.Join(src.Mint, " ") != mine {
		t.Fatalf("the source mints with %q, want the operator's %q: %v", src.Mint, mine, err)
	}

	// Applied again, from a yard whose copy was removed, so the card has
	// a change to carry: the operator's command stays.
	if err := os.Remove(filepath.Join(home, "sources", "google-mail", "google-mint")); err != nil {
		t.Fatal(err)
	}
	seq := askApprove(t, st, bead, packAskOf("google-mail"))
	drain(t, st, home, "boot-two")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the second apply's record = %+v", rec)
	}
	if src, err = st.GetSource(ctx, "google-mail"); err != nil || src.MintCommand != mine || strings.Join(src.Mint, " ") != mine {
		t.Fatalf("after the second apply: mint_command %q, mint %q, %v", src.MintCommand, src.Mint, err)
	}

	// Taken away, the pack's mints again.
	if _, err := st.SetSource(ctx, "google-mail", store.SourceMintCommand, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if src, err = st.GetSource(ctx, "google-mail"); err != nil || len(src.Mint) != 1 || src.Mint[0] != filepath.Join(home, "sources", "google-mail", "google-mint") {
		t.Fatalf("with the setting taken away the source mints with %q, %v", src.Mint, err)
	}

	// Not a crew member's, typed or asked.
	var op *store.OperatorOnlyError
	if _, err := st.SetSource(ctx, "google-mail", store.SourceMintCommand, "/tmp/evil", "yardmaster"); !errors.As(err, &op) {
		t.Fatalf("a crew member set mint_command: %v", err)
	}
	if _, err := st.AddAsk(ctx, bead, store.Ask{Question: "Set the mint?", To: "mckean", Context: []string{"c"},
		Run: &store.AskRun{Argv: []string{"source set google-mail mint_command=/tmp/evil"}}}, "yardmaster"); err == nil || !strings.Contains(err.Error(), "never proposed as a run ask") {
		t.Fatalf("a card that sets mint_command: %v", err)
	}
	if src, err = st.GetSource(ctx, "google-mail"); err != nil || src.MintCommand != "" {
		t.Fatalf("mint_command = %q, %v", src.MintCommand, err)
	}
}
