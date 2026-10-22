package store

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// yardSourceWithAccount adds the source text declares to the yard with its
// mint copy on disk and its env file holding values: a candidate the setup
// card can list. The mint copy is written under
// <home>/sources/<name>/<prog> with the bytes the pack carries, so the two
// sides' programs match by file bytes.
func yardSourceWithAccount(t *testing.T, y *askYard, text, prog, mint string, values map[string]string) string {
	t.Helper()
	if _, err := y.PutSource(y.ctx, "yard.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	d, err := y.GetSource(y.ctx, mustDeclName(t, text))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(y.home, "sources", d.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, prog), []byte(mint), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(y.home, "sources", d.Name+".env")
	if _, err := y.SetSource(y.ctx, d.Name, SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for n, v := range values {
		lines = append(lines, n+"="+v)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func mustDeclName(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if name, ok := strings.CutPrefix(line, "name: "); ok {
			return strings.TrimSpace(name)
		}
	}
	t.Fatal("the declaration names no source")
	return ""
}

// A second source's card lists the first by name with its account address,
// to take it or type another.
func TestSetupCardListsCandidateWithAddress(t *testing.T) {
	y, _ := runYard(t)
	file := yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", setupMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	setup := y.asked(t, seq).Ask.Setup
	if len(setup.Candidates) != 1 || setup.Candidates[0].Source != "setupcal" ||
		setup.Candidates[0].File != file || setup.Candidates[0].Values["SETUP_ACCOUNT"] != "ops@example.com" {
		t.Fatalf("the candidates = %+v, want setupcal with its address in %s", setup.Candidates, file)
	}
	joined := strings.Join(setup.Does, "\n")
	for _, want := range []string{
		"the account SETUP_ACCOUNT: already given for setupcal (SETUP_ACCOUNT=ops@example.com, in " + file + ")",
		"Take it with --account setupcal, or type another unseen",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if !strings.Contains(lines, "answer with: yardr bead answer "+y.bead+" "+itoa(seq)+" <n> --secret, or <n> --account <source>") {
		t.Errorf("the terminal card lacks the --account answer:\n%s", lines)
	}
	// The device view of this same card withholds its candidate in both the
	// field and the sentence that names it. The store's card remains whole.
	card := y.asked(t, seq).Ask.setup()
	if len(card.Candidates) != 1 {
		t.Fatalf("the card lists %+v, want setupcal", card.Candidates)
	}
	bare := card.WithoutCandidates()
	if len(bare.Candidates) != 0 {
		t.Errorf("the card without candidates lists %+v", bare.Candidates)
	}
	told := strings.Join(bare.Does, "\n")
	for _, secret := range []string{file, "ops@example.com", "setupcal"} {
		if strings.Contains(told, secret) {
			t.Errorf("the card without candidates still says %q:\n%s", secret, told)
		}
	}
	if want := "the account SETUP_ACCOUNT: type it unseen. It is written to " + card.AccountFile + ", named in the answer, never the value."; !slices.Contains(bare.Does, want) {
		t.Errorf("the card without candidates lacks %q:\n%s", want, told)
	}
	if len(bare.Does) != len(card.Does) {
		t.Errorf("the sentences are %d, want the card's %d with the account sentence replaced:\n%s", len(bare.Does), len(card.Does), told)
	}
	for i := range card.Does {
		if i < len(bare.Does) && !strings.Contains(card.Does[i], "already given") && card.Does[i] != bare.Does[i] {
			t.Errorf("sentence %d moved:\n%s\nwant\n%s", i, bare.Does[i], card.Does[i])
		}
	}
	rest, was := bare, *card
	rest.Does, rest.Candidates, was.Does, was.Candidates = nil, nil, nil, nil
	if !reflect.DeepEqual(rest, was) {
		t.Errorf("the rest of the card moved:\n%+v\nwant\n%+v", rest, was)
	}
	if len(card.Candidates) != 1 || !strings.Contains(strings.Join(card.Does, "\n"), file) {
		t.Errorf("the card it was made from lost its candidates: %+v", card)
	}
}

// Every shape of the account sentence that names a candidate is replaced by
// the one the same card writes with none, and so is a sentence in other
// words: a card kept in the store was written by the build of its day, and
// the sentence is found by what it says of a candidate.
func TestSetupCardWithoutCandidatesAnyWords(t *testing.T) {
	account, own := []string{"SETUP_ACCOUNT", "SETUP_REGION"}, "/yard/sources/setupmail.env"
	candidates := []AskCandidate{{Source: "setupcal", File: "/yard/sources/setupcal.env",
		Values: map[string]string{"SETUP_ACCOUNT": "ops@example.com", "SETUP_REGION": "eu-west"}}}
	secrets := []string{"/yard/sources/setupcal.env", "ops@example.com", "eu-west", "setupcal"}
	grants := "grants to crew yardmaster — pick one:"
	for _, c := range []struct {
		name    string
		given   bool
		missing []string
		line    string // the sentence as a build with other words wrote it, else the sentence as this build writes it
	}{
		{name: "a first setup"},
		{name: "an update that holds the account", given: true},
		{name: "an update that lacks some of it", missing: []string{"SETUP_REGION"}},
		{name: "an update that lacks all of it", missing: account},
		{name: "another build's words", line: "setupcal already has ops@example.com and eu-west in /yard/sources/setupcal.env: take it with --account setupcal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := c.line
			if line == "" {
				line = safeSourceCardLine(setupAccountLine(account, own, c.given, c.missing, candidates))
			}
			card := SetupAsked{Pack: "setupmail", Account: account, AccountFile: own, Given: c.given, Missing: c.missing,
				Candidates: candidates, Does: []string{"pack setupmail: it adds 1 source.", line, grants}}
			bare := card.WithoutCandidates()
			want := []string{"pack setupmail: it adds 1 source.", safeSourceCardLine(setupAccountLine(account, own, c.given, c.missing, nil)), grants}
			if !slices.Equal(bare.Does, want) || len(bare.Candidates) != 0 {
				t.Errorf("the card without candidates says %q and lists %+v, want %q and none", bare.Does, bare.Candidates, want)
			}
			for _, secret := range secrets {
				if strings.Contains(strings.Join(bare.Does, "\n"), secret) {
					t.Errorf("the card without candidates still says %q: %q", secret, bare.Does)
				}
			}
			if len(card.Candidates) != 1 || card.Does[1] != line {
				t.Errorf("the card it was made from moved: %+v", card)
			}
		})
	}
	// A card that lists none is told as it is.
	plain := SetupAsked{Account: account, AccountFile: own, Does: []string{"pack setupmail: it adds 1 source.", safeSourceCardLine(setupAccountLine(account, own, false, nil, nil)), grants}}
	if got := plain.WithoutCandidates(); !reflect.DeepEqual(got, plain) {
		t.Errorf("a card with no candidates was changed: %+v", got)
	}
}

// With a single match the card still lists it and still needs the pick:
// neither a silent reuse nor an empty approval settles the account.
func TestSetupCardSingleMatchStillNeedsPick(t *testing.T) {
	y, _ := runYard(t)
	yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", setupMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if len(y.asked(t, seq).Ask.Setup.Candidates) != 1 {
		t.Fatalf("a single match is not listed: %+v", y.asked(t, seq).Ask.Setup.Candidates)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "no candidate was taken (--account <source>)") {
		t.Errorf("an approval with neither value nor pick was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_ACCOUNT": "new@example.com"}, "setupcal", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "not both: --secret or --account <source>") {
		t.Errorf("an approval with both value and pick was recorded or refused otherwise: %v", err)
	}
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Account != "setupcal" || len(done.Given) != 0 {
		t.Errorf("the approval took %+v, want the candidate", done)
	}
	// Typing another is the other pick, on a second card.
	seq = y.ask(t, setupAsk(), "yardmaster")
	done, err = y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_ACCOUNT": "new@example.com"}, "", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Given) != 1 || done.Account != "" {
		t.Errorf("typing another took %+v, want the typed names", done)
	}
}

// A credential a user gave for themselves is never listed: it lives in the
// user's file, never a source's.
func TestSetupCandidateExcludesUserCredential(t *testing.T) {
	y, _ := runYard(t)
	// The yard holds the source with its mint, but no yard-level account.
	if _, err := y.PutSource(y.ctx, "setupcal.yaml", setupCalDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(y.home, "sources", "setupcal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-mint"), []byte(setupMint), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.KeyUserCredential(y.ctx, "ada", "setupcal",
		map[string]string{"SETUP_ACCOUNT": "ada@example.com"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if got := y.asked(t, seq).Ask.Setup.Candidates; len(got) != 0 {
		t.Fatalf("a user's own credential is listed: %+v", got)
	}
}

// A source whose hand-set env_file resolves to a user's file is never
// listed either, even on a custom shared path.
func TestSetupCandidateExcludesSharedUserFile(t *testing.T) {
	y, _ := runYard(t)
	shared := filepath.Join(t.TempDir(), "shared.env")
	if err := os.WriteFile(shared, []byte("SETUP_ACCOUNT=ops@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: shared}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "setupcal.yaml", setupCalDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(y.home, "sources", "setupcal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-mint"), []byte(setupMint), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupcal", SourceEnvFile, shared, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if got := y.asked(t, seq).Ask.Setup.Candidates; len(got) != 0 {
		t.Fatalf("a source file shared with a user is listed: %+v", got)
	}
}

// Changing a candidate env file in place refuses the approval stale: the
// path and the source settings are the same, and only the values moved.
func TestSetupStaleCandidateChanged(t *testing.T) {
	y, _ := runYard(t)
	file := yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", setupMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if err := os.WriteFile(file, []byte("SETUP_ACCOUNT=other@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "the candidate setupcal held SETUP_ACCOUNT as carded: its file no longer holds it") {
		t.Errorf("an approval past a changed candidate was recorded or refused otherwise: %v", err)
	}
}

// A static source never shares, even where the variable name matches.
func TestSetupCandidateStaticNeverShares(t *testing.T) {
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "setupstatic.yaml", setupStaticDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(y.home, "sources", "setupstatic.env")
	if _, err := y.SetSource(y.ctx, "setupstatic", SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("STATIC_TOKEN=tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := setupMailPack()
	f.sources = []setupSource{{name: "setupstatic", text: setupStaticDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setupstatic", Calls: []string{"read"}}}}}
	y.setSetupPack(f)
	ask := setupAsk()
	ask.Recommend, ask.Why = 0, ""
	seq := y.ask(t, ask, "yardmaster")
	if got := y.asked(t, seq).Ask.Setup.Candidates; len(got) != 0 {
		t.Fatalf("a static source shares: %+v", got)
	}
}

// A hand-set mint shares only where the full effective argv matches.
func TestSetupCandidateHandSetMintShares(t *testing.T) {
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "setupmail.yaml", setupDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupmail", SourceMintCommand, "/usr/bin/my-mint --fast", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "setupcal.yaml", setupCalDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupcal", SourceMintCommand, "/usr/bin/my-mint --fast", "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(y.home, "sources", "setupcal.env")
	if _, err := y.SetSource(y.ctx, "setupcal", SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("SETUP_ACCOUNT=ops@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	got := y.asked(t, seq).Ask.Setup.Candidates
	if len(got) != 1 || got[0].Source != "setupcal" {
		t.Fatalf("a matching hand-set mint is not listed: %+v", got)
	}
}

// Differing hand-set arguments break sharing.
func TestSetupCandidateHandSetArgsBreak(t *testing.T) {
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "setupmail.yaml", setupDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupmail", SourceMintCommand, "/usr/bin/my-mint --fast", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "setupcal.yaml", setupCalDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupcal", SourceMintCommand, "/usr/bin/my-mint --slow", "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(y.home, "sources", "setupcal.env")
	if _, err := y.SetSource(y.ctx, "setupcal", SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("SETUP_ACCOUNT=ops@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if got := y.asked(t, seq).Ask.Setup.Candidates; len(got) != 0 {
		t.Fatalf("hand-set mints with differing arguments share: %+v", got)
	}
}

// A picked approval records the candidate taken, and the card reads took.
func TestSetupApprovalTakesCandidate(t *testing.T) {
	y, _ := runYard(t)
	yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", setupMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Account != "setupcal" {
		t.Fatalf("the approval took %+v", done)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.Account != "setupcal" {
		t.Fatalf("the carrying record holds %+v, %v", rec, err)
	}
	if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
		t.Error("a picked approval handed values to the memory")
	}
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if !strings.Contains(lines, "approved, to be carried out: setup of pack setupmail, preset read to crew yardmaster; took setupcal") {
		t.Errorf("the approved card reads:\n%s", lines)
	}
}

// Taking a source the card lists none of is refused with what it lists.
func TestSetupApprovalUnknownCandidateRefused(t *testing.T) {
	y, _ := runYard(t)
	yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", setupMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "nosuch", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "lists no candidate nosuch: it lists setupcal") {
		t.Errorf("an unknown candidate was taken or refused otherwise: %v", err)
	}
	y2, _ := runYard(t)
	y2.setSetupPack(setupMailPack())
	seq2 := y2.ask(t, setupAsk(), "yardmaster")
	_, err = y2.ApproveSetup(y2.ctx, y2.bead, seq2, 1, nil, "setupcal", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "lists no candidate to take: type the account SETUP_ACCOUNT") {
		t.Errorf("a candidate with none listed was taken or refused otherwise: %v", err)
	}
}
