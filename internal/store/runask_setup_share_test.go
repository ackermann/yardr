package store

import (
	"os"
	"path/filepath"
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
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{}, "", "mckean"); err == nil ||
		!strings.Contains(err.Error(), "no candidate was taken (--account <source>)") {
		t.Errorf("an approval with neither value nor pick was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_ACCOUNT": "new@example.com"}, "setupcal", "mckean"); err == nil ||
		!strings.Contains(err.Error(), "not both: --secret or --account <source>") {
		t.Errorf("an approval with both value and pick was recorded or refused otherwise: %v", err)
	}
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Account != "setupcal" || len(done.Given) != 0 {
		t.Errorf("the approval took %+v, want the candidate", done)
	}
	// Typing another is the other pick, on a second card.
	seq = y.ask(t, setupAsk(), "yardmaster")
	done, err = y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_ACCOUNT": "new@example.com"}, "", "mckean")
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
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean")
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
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean")
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
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "nosuch", "mckean")
	if err == nil || !strings.Contains(err.Error(), "lists no candidate nosuch: it lists setupcal") {
		t.Errorf("an unknown candidate was taken or refused otherwise: %v", err)
	}
	y2, _ := runYard(t)
	y2.setSetupPack(setupMailPack())
	seq2 := y2.ask(t, setupAsk(), "yardmaster")
	_, err = y2.ApproveSetup(y2.ctx, y2.bead, seq2, 1, nil, "setupcal", "mckean")
	if err == nil || !strings.Contains(err.Error(), "lists no candidate to take: type the account SETUP_ACCOUNT") {
		t.Errorf("a candidate with none listed was taken or refused otherwise: %v", err)
	}
}
