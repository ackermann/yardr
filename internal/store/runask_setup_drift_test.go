package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A drift pack's old declaration in the yard: one minted source with a
// read call, an update call and a limits file, and one mint input.
const driftOldDecl = `name: setupmail
base: https://setupmail.example.com/v1
credential:
  env: SETUP_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [SETUP_ACCOUNT]
  mint_command: [setup-mint]
calls:
  search: { kind: read, request: { method: GET, path: /messages } }
  draft: { kind: update, request: { method: POST, path: /drafts } }
limits: { command: [setup-limits] }
`

const driftOldMint = "#!/bin/sh\n# mint a token from the account, old\n"
const driftOldLimits = "#!/bin/sh\n# limits, old\n"

// The same source as the pack brings it updated: a new base, a new read
// call and a new update call, and new bytes for the mint and the limits
// file.
const driftNewDecl = `name: setupmail
base: https://setupmail.example.com/v2
credential:
  env: SETUP_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [SETUP_ACCOUNT]
  mint_command: [setup-mint]
calls:
  search: { kind: read, request: { method: GET, path: /messages } }
  thread: { kind: read, request: { method: GET, path: /threads } }
  draft: { kind: update, request: { method: POST, path: /drafts } }
  send: { kind: update, request: { method: POST, path: /send } }
limits: { command: [setup-limits] }
`

const driftNewMint = "#!/bin/sh\n# mint a token from the account, new\n"
const driftNewLimits = "#!/bin/sh\n# limits, new\n"

func driftPresets() []AskPreset {
	return []AskPreset{
		{Name: "read", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read"}}}},
		{Name: "drafts", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read", "draft"}}}},
		{Name: "all", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read", "draft", "send"}}}},
	}
}

func driftPack() *setupPack {
	return &setupPack{label: "embedded in yardr test1",
		sources: []setupSource{{name: "setupmail", text: driftNewDecl,
			files: map[string]string{"setup-mint": driftNewMint, "setup-limits": driftNewLimits}}},
		presets: driftPresets()}
}

// driftYard plants the old source with its files and its account given,
// and serves the new pack: a drift card's yard.
func driftYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	file := yardSourceWithAccount(t, y, driftOldDecl, "setup-mint", driftOldMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	dir := filepath.Join(y.home, "sources", "setupmail")
	if err := os.WriteFile(filepath.Join(dir, "setup-limits"), []byte(driftOldLimits), 0o700); err != nil {
		t.Fatal(err)
	}
	y.setSetupPack(driftPack())
	_ = file
	return y
}

// A pack update changing a declaration, a command file and the mint cards
// each diff as the replacement row does: the declaration keys old against
// new, each file's old and new sha256 with its unified diff.
func TestSetupDriftDiffsMatchReplacement(t *testing.T) {
	t.Parallel()
	y := driftYard(t)
	seq := y.ask(t, setupAsk(), "yardmaster")
	setup := y.asked(t, seq).Ask.Setup
	joined := strings.Join(setup.Does, "\n")
	for _, want := range []string{
		"it would replace the source setupmail the yard has under that name.",
		"source setupmail base:",
		"https://setupmail.example.com/v1",
		"https://setupmail.example.com/v2",
		"source setupmail calls:",
		"command file setup-limits",
		"sha256 ",
		" -> sha256 ",
		"command file setup-mint, copied to <home>/sources/setupmail/setup-mint: sha256 ",
		" -> sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the drift card lacks %q:\n%s", want, joined)
		}
	}
	if len(setup.Carried) != 1 || len(setup.Carried[0].Old) == 0 {
		t.Fatalf("the drift card carries no file diffs: %+v", setup.Carried)
	}
	var mint, limits *AskFileDiff
	for i := range setup.Carried[0].Old {
		d := &setup.Carried[0].Old[i]
		if d.Program == "setup-mint" {
			mint = d
		}
		if d.Program == "setup-limits" {
			limits = d
		}
	}
	if mint == nil || mint.OldSHA256 == mint.NewSHA256 || !strings.Contains(mint.Diff, "-# mint a token from the account, old") || !strings.Contains(mint.Diff, "+# mint a token from the account, new") {
		t.Errorf("the mint diff = %+v", mint)
	}
	if limits == nil || limits.OldSHA256 == limits.NewSHA256 || !strings.Contains(limits.Diff, "-# limits, old") || !strings.Contains(limits.Diff, "+# limits, new") {
		t.Errorf("the limits diff = %+v", limits)
	}
	// The same update as a pack apply card: the diffs read the same.
	packSeq := y.ask(t, Ask{Question: "Apply setupmail?", To: "mckean",
		Context: []string{"the yardmaster reads mail to work beads"},
		Run:     &AskRun{Argv: []string{"pack apply setupmail"}}}, "yardmaster")
	run := y.asked(t, packSeq).Ask.Run
	runJoined := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"it would replace the source setupmail the yard has under that name.",
		"source setupmail base:",
		"command file setup-limits",
		"command file setup-mint, copied to <home>/sources/setupmail/setup-mint: sha256 ",
	} {
		if !strings.Contains(runJoined, want) {
			t.Errorf("the pack card lacks %q:\n%s", want, runJoined)
		}
	}
	if len(run.Carried) != 1 || len(run.Carried[0].Old) != len(setup.Carried[0].Old) {
		t.Errorf("the pack card carries %d diffs, the drift card %d", len(run.Carried[0].Old), len(setup.Carried[0].Old))
	}
}

// A drift preset's held calls read held and its new ones read new; a new
// read call under a standing read grant reads covered with no grant
// needed, and a new update call reads waiting on its preset.
func TestSetupDriftPresetHeldNewCoveredWaiting(t *testing.T) {
	t.Parallel()
	y := driftYard(t)
	if _, err := y.GrantSource(y.ctx, "setupmail", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, setupAsk(), "yardmaster")
	options := y.asked(t, seq).Ask.Options
	if len(options) != 4 {
		t.Fatalf("the options = %+v, want three presets with You decide last", options)
	}
	read, drafts, all := options[0].Consequence, options[1].Consequence, options[2].Consequence
	for _, want := range []string{
		"may make every read call of the source setupmail",
		"thread (GET /threads) (new)",
		"(held)",
		"New read call thread is covered by the standing read grant, no grant needed.",
	} {
		if !strings.Contains(read, want) {
			t.Errorf("the read preset lacks %q:\n%s", want, read)
		}
	}
	for _, want := range []string{"(held)", "may make the call draft (POST /drafts)", "(new)"} {
		if !strings.Contains(drafts, want) {
			t.Errorf("the drafts preset lacks %q:\n%s", want, drafts)
		}
	}
	if strings.Contains(drafts, "waiting on the preset") {
		t.Errorf("the drafts preset waits, and it names no new update:\n%s", drafts)
	}
	for _, want := range []string{
		"may make the call send (POST /send)",
		"(new)",
		"waiting on the preset all",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the all preset lacks %q:\n%s", want, all)
		}
	}
	// A drift card whose pack would change anything is approved even
	// where the preset is held: the update itself still wants applying.
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "", "mckean", true)
	if err != nil {
		t.Fatalf("an approval of a held preset on drift was refused: %v", err)
	}
	if !done.Keep {
		t.Errorf("the approval kept nothing: %+v", done)
	}
}

// A drift card whose file holds every name reads given with its file,
// kept with a bare preset pick, typed another to change it, or a
// candidate's file to take.
func TestSetupDriftAccountGivenKeep(t *testing.T) {
	t.Parallel()
	y := driftYard(t)
	yardSourceWithAccount(t, y, setupCalDecl, "setup-mint", driftNewMint,
		map[string]string{"SETUP_ACCOUNT": "cal@example.com"})
	seq := y.ask(t, setupAsk(), "yardmaster")
	setup := y.asked(t, seq).Ask.Setup
	if !setup.Given || len(setup.Missing) != 0 {
		t.Fatalf("the drift card is given=%v missing=%v, want given with none missing", setup.Given, setup.Missing)
	}
	joined := strings.Join(setup.Does, "\n")
	for _, want := range []string{
		"the account SETUP_ACCOUNT: given, in ",
		"approve keeping it with a bare preset pick",
		"already given for setupcal",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the drift card lacks %q:\n%s", want, joined)
		}
	}
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if !strings.Contains(lines, "<n>, <n> --secret, or <n> --account <source>") {
		t.Errorf("the terminal card lacks the bare pick answer:\n%s", lines)
	}
	// Kept with a bare pick: no values, no candidate.
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Keep || len(done.Given) != 0 || done.Account != "" {
		t.Errorf("the approval kept %+v", done)
	}
	// Typed another to change it.
	seq = y.ask(t, setupAsk(), "yardmaster")
	done, err = y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_ACCOUNT": "new@example.com"}, "", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Given) != 1 || done.Given[0] != "SETUP_ACCOUNT" || done.Keep {
		t.Errorf("the approval gave %+v", done)
	}
	// A candidate's file to take.
	seq = y.ask(t, setupAsk(), "yardmaster")
	done, err = y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "setupcal", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Account != "setupcal" || done.Keep {
		t.Errorf("the approval took %+v", done)
	}
	// A partial typing is refused: the whole account changes it, or the
	// bare pick keeps it.
	seq = y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "x", "EXTRA": "y"}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "given a name it did not ask for") {
		t.Errorf("an approval with a forged name was recorded or refused otherwise: %v", err)
	}
}

// Where the updated declaration needs a mint input name the old file does
// not set, the drift card names the missing names and the answer types
// them.
func TestSetupDriftAccountMissing(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	yardSourceWithAccount(t, y, driftOldDecl, "setup-mint", driftOldMint,
		map[string]string{"SETUP_ACCOUNT": "ops@example.com"})
	dir := filepath.Join(y.home, "sources", "setupmail")
	if err := os.WriteFile(filepath.Join(dir, "setup-limits"), []byte(driftOldLimits), 0o700); err != nil {
		t.Fatal(err)
	}
	needsProject := strings.Replace(driftNewDecl, "mint: [SETUP_ACCOUNT]", "mint: [SETUP_ACCOUNT, SETUP_PROJECT]", 1)
	f := driftPack()
	f.sources = []setupSource{{name: "setupmail", text: needsProject,
		files: map[string]string{"setup-mint": driftNewMint, "setup-limits": driftNewLimits}}}
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	setup := y.asked(t, seq).Ask.Setup
	if setup.Given || len(setup.Missing) != 1 || setup.Missing[0] != "SETUP_PROJECT" {
		t.Fatalf("the drift card is given=%v missing=%v, want missing SETUP_PROJECT", setup.Given, setup.Missing)
	}
	joined := strings.Join(setup.Does, "\n")
	for _, want := range []string{
		"given, in ",
		"but missing SETUP_PROJECT",
		"type SETUP_PROJECT unseen",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the drift card lacks %q:\n%s", want, joined)
		}
	}
	// Kept is refused: the missing name wants typing.
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, nil, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "missing account SETUP_PROJECT") {
		t.Errorf("an approval keeping a missing account was recorded or refused otherwise: %v", err)
	}
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, 1,
		map[string]string{"SETUP_PROJECT": "proj-1"}, "", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Given) != 1 || done.Given[0] != "SETUP_PROJECT" {
		t.Errorf("the approval gave %+v, want the missing name", done)
	}
}
