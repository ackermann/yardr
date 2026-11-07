package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// The pack a setup CLI test asks for: one minted source with two presets.
const cliSetupDecl = `name: clipphone
base: https://clipphone.example.com
credential:
  env: CLIPHONE_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [CLIPHONE_ACCOUNT]
  mint_command: [clip-mint]
calls:
  ring: { kind: update, request: { method: POST, path: /ring } }
  log: { kind: read, request: { method: GET, path: /log } }
`

const cliSetupMint = "#!/bin/sh\n# mint a token from the account\n"

func cliSetupReading() *store.PackReading {
	sum := sha256.Sum256([]byte(cliSetupMint))
	return &store.PackReading{Name: "clipphone", Kind: "source", Label: "embedded in yardr test",
		Sources: []store.AskSource{{Name: "clipphone", Text: cliSetupDecl,
			Files: []store.AskFile{{Program: "clip-mint", Text: cliSetupMint, SHA256: hex.EncodeToString(sum[:])}}}},
		Presets: []store.AskPreset{
			{Name: "read", Grants: []store.AskPresetGrant{{Source: "clipphone", Calls: []string{"read"}}}},
			{Name: "ring", Grants: []store.AskPresetGrant{{Source: "clipphone", Calls: []string{"read", "ring"}}}},
		}}
}

// setupYardCLI is a run yard whose server reads the one embedded pack
// clipphone.
func setupYardCLI(t *testing.T) (*cliYard, *store.Bead) {
	t.Helper()
	y, b := runYardCLI(t, "setup", airyKey(t))
	y.own.SetPackReader(func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
		if name != "clipphone" {
			return nil, errFakeNoPackCLI(name)
		}
		return cliSetupReading(), nil
	})
	return y, b
}

func errFakeNoPackCLI(name string) error {
	return &fakeNoPackCLI{name}
}

type fakeNoPackCLI struct{ name string }

func (e *fakeNoPackCLI) Error() string {
	return "pack " + e.name + ": yardr embeds no pack of that name"
}

// yardr bead ask --setup stores the setup with the yard's card and prints
// how to approve; yardr bead answer <n> --secret approves it with the
// account typed unseen.
func TestBeadAskSetupAndAnswer(t *testing.T) {
	t.Parallel()
	y, b := setupYardCLI(t)
	out, err := y.as(t, "yardmaster", "ask", b.ID, "Set up clipphone for the yardmaster?",
		"--setup", "clipphone", "--crew", "yardmaster",
		"--context", "The yardmaster rings the person when a bead needs them.",
		"--recommend", "1", "--why", "reading the log is the job")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + b.ID + " for jo: to approve, yardr bead answer " + b.ID + " 1 <preset> --secret\n"; out != want {
		t.Fatalf("ask printed %q, want %q", out, want)
	}
	notes, err := y.st.Notes(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var setup *store.AskSetup
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == 1 {
			setup = r.Ask.Setup
		}
	}
	if setup == nil || len(setup.Presets) != 2 || len(setup.Account) != 1 || setup.Account[0] != "CLIPHONE_ACCOUNT" {
		t.Fatalf("the stored setup = %+v", setup)
	}
	// A setup takes the account, so its card is weighty (design yardr-ekmx
	// 3.3): with no terminal each way of approving it declines before it
	// reads a value, whatever is on its input, and names where to approve
	// it; and at a terminal it asks for the ask's number first.
	for _, tc := range []struct {
		args  []string
		again string
	}{
		{[]string{b.ID, "1", "1", "--secret"}, "1 --secret"},
		{[]string{b.ID, "1", "1", "--account", "other"}, "1 --account other"},
	} {
		in := strings.NewReader("CLIPHONE_ACCOUNT=1555\n")
		c := y.a.beadCmd()
		c.SetIn(in)
		_, err := run(t, c, append([]string{"answer"}, tc.args...)...)
		if err == nil || !strings.HasPrefix(err.Error(), "ask 1 is weighty (credential") ||
			!strings.HasSuffix(err.Error(), "cannot approve it. Approve it in footplate, or in a terminal on the yard's machine: yardr bead answer "+b.ID+" 1 "+tc.again) ||
			in.Len() != len("CLIPHONE_ACCOUNT=1555\n") {
			t.Fatalf("approving a setup with no terminal (%v) = %v (%d bytes of input left)", tc.args, err, in.Len())
		}
	}
	in := strings.NewReader("9\nCLIPHONE_ACCOUNT=1555\n")
	c := y.a.beadCmd()
	c.SetIn(in)
	atTerminal(y.a)
	if _, err := run(t, c, "answer", b.ID, "1", "1", "--secret"); err == nil || !strings.Contains(err.Error(), "its number was not typed back, so nothing was done") ||
		in.Len() != len("CLIPHONE_ACCOUNT=1555\n") {
		t.Fatalf("approving a setup with the wrong number = %v (%d bytes of input left)", err, in.Len())
	}
	y.a.inTerm = nil
	if got := askState(context.Background(), y.st, b.ID, 1); got != "open" {
		t.Fatalf("the setup ask is %q after the declined approvals, want it open", got)
	}
	out, err = answerFrom(t, y.a, "CLIPHONE_ACCOUNT=1555\n", b.ID, "1", "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1, asked by yardmaster: setup of pack clipphone, preset read to crew yardmaster\njo gave CLIPHONE_ACCOUNT\njo approved it: approved, to be carried out\n"; out != want {
		t.Fatalf("answer printed %q, want %q", out, want)
	}
	beadAskSetupSourceBase(t)
}

// beadAskSetupSourceBase checks that --source-base reaches a setup ask's
// stored card and names the host its calls will use.
func beadAskSetupSourceBase(t *testing.T) {
	y, b := setupYardCLI(t)
	decl := strings.Replace(cliSetupDecl, "base: https://clipphone.example.com\n",
		"base: https://clipphone.example.com\nbases:\n  us: https://clipphone.example.com\n  eu: https://eu.clipphone.example.com\n", 1)
	if decl == cliSetupDecl {
		t.Fatal("the test adds no bases")
	}
	reading := cliSetupReading()
	reading.Sources[0].Text = decl
	y.own.SetPackReader(func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
		if name != "clipphone" {
			return nil, errFakeNoPackCLI(name)
		}
		return reading, nil
	})
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Set up clipphone for the yardmaster?",
		"--setup", "clipphone", "--crew", "yardmaster", "--source-base", "eu",
		"--context", "The yardmaster rings the person when a bead needs them."); err != nil {
		t.Fatal(err)
	}
	notes, err := y.st.Notes(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var setup *store.AskSetup
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == 1 {
			setup = r.Ask.Setup
		}
	}
	if setup == nil || setup.Base != "eu" {
		t.Fatalf("the stored setup = %+v", setup)
	}
	joined := strings.Join(setup.Does, "\n")
	if want := "source clipphone: its calls go to https://eu.clipphone.example.com (base eu)."; !strings.Contains(joined, want) {
		t.Errorf("the card lacks %q:\n%s", want, joined)
	}
}

// A setup ask is refused for the combinations it takes none of, for no
// grantee, and for a pack the binary does not embed.
func TestBeadAskSetupRefusals(t *testing.T) {
	t.Parallel()
	y, b := setupYardCLI(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"run":                          {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--run", "user add x", "--context", "C"}, "one of the two"},
		"secret":                       {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--secret", "crm", "--context", "C"}, "one of the two"},
		"as":                           {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--as", "ada", "--context", "C"}, "--setup refuses --as"},
		"for-user":                     {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--for-user", "ada", "--context", "C"}, "not --setup"},
		"no grantee":                   {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--context", "C"}, "--crew <name> or --group <name>"},
		"crew without":                 {[]string{"ask", b.ID, "Q", "--crew", "yardmaster", "--context", "C", "--option", "A: B"}, "they go with --setup <pack>"},
		"base without":                 {[]string{"ask", b.ID, "Q", "--source-base", "eu", "--context", "C", "--option", "A: B"}, "it goes with --setup <pack>"},
		"base of a pack without bases": {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--source-base", "eu", "--context", "C"}, "pack clipphone declares no bases"},
		"no such pack":                 {[]string{"ask", b.ID, "Q", "--setup", "nosuch", "--crew", "yardmaster", "--context", "C"}, "embeds no pack"},
		"writer option":                {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C", "--option", "A: B"}, "takes no --option"},
		"bad recommend":                {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C", "--recommend", "9", "--why", "W"}, "--recommend 9 names none"},
		"operator asks":                {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C"}, "needs no approval"},
	} {
		args, want := tc.args, tc.want
		who := "yardmaster"
		if name == "operator asks" {
			who = "jo"
		}
		if out, err := y.as(t, who, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: asked or refused otherwise: %v\n%s", name, err, out)
		}
	}
}

// A pick with --secret settles a setup ask alone: --secret alone on one
// needs its pick, and a pick with --secret on any other ask is refused
// as it always was.
func TestBeadAnswerSetupRefusals(t *testing.T) {
	t.Parallel()
	y, b := setupYardCLI(t)
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Set up clipphone?", "--setup", "clipphone",
		"--crew", "yardmaster", "--context", "The yardmaster rings the person."); err != nil {
		t.Fatal(err)
	}
	if out, err := answerFrom(t, y.a, "CLIPHONE_ACCOUNT=1555\n", b.ID, "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "pick its preset with the account") {
		t.Errorf("--secret alone on a setup was taken or refused otherwise: %v\n%s", err, out)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Ring now or later?", "--context", "The person waits.",
		"--option", "Now: ring at once", "--option", "Later: ring at six"); err != nil {
		t.Fatal(err)
	}
	if out, err := answerFrom(t, y.a, "CLIPHONE_ACCOUNT=1555\n", b.ID, "2", "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "takes nothing else") {
		t.Errorf("a pick with --secret on an option ask was taken or refused otherwise: %v\n%s", err, out)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "The token for the CRM", "--secret", "crm",
		"--context", "Create one read-only."); err != nil {
		t.Fatal(err)
	}
	if out, err := answerFrom(t, y.a, "CRM_TOKEN=s3cret\n", b.ID, "3", "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "takes nothing else") {
		t.Errorf("a pick with --secret on a secret ask was taken or refused otherwise: %v\n%s", err, out)
	}
	// A crew member's setup, or secret, is refused before the gate asks
	// for the number or a value is read.
	for _, args := range [][]string{{b.ID, "1", "1", "--secret"}, {b.ID, "3", "--secret"}} {
		if err := unprompted(t, y, "yardmaster", nil, args...); err == nil || !strings.Contains(err.Error(), "yardmaster is a crew member") {
			t.Errorf("answer %v as a crew member = %v", args, err)
		}
	}
}

// yardr bead answer <n> --account takes a listed candidate's account
// instead of typing one: the preset picked and who took which source.
func TestBeadAnswerSetupAccount(t *testing.T) {
	t.Parallel()
	y, b := setupYardCLI(t)
	ctx := context.Background()
	const other = `name: clipphone2
base: https://clipphone.example.com
credential:
  env: CLIPHONE_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [CLIPHONE_ACCOUNT]
  mint_command: [clip-mint]
calls:
  ring: { kind: update, request: { method: POST, path: /ring } }
  log: { kind: read, request: { method: GET, path: /log } }
`
	if _, err := y.st.PutSource(ctx, "clipphone.yaml", cliSetupDecl, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetSource(ctx, "clipphone", store.SourceMintCommand, "/usr/bin/my-mint --fast", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutSource(ctx, "clipphone2.yaml", other, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetSource(ctx, "clipphone2", store.SourceMintCommand, "/usr/bin/my-mint --fast", "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "clipphone2.env")
	if err := os.WriteFile(file, []byte("CLIPHONE_ACCOUNT=1555\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetSource(ctx, "clipphone2", store.SourceEnvFile, file, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Set up clipphone?", "--setup", "clipphone",
		"--crew", "yardmaster", "--context", "The yardmaster rings the person."); err != nil {
		t.Fatal(err)
	}
	out, err := y.as(t, "jo", "answer", b.ID, "1", "1", "--account", "clipphone2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1, asked by yardmaster: setup of pack clipphone, preset read to crew yardmaster\njo took clipphone2\njo approved it: approved, to be carried out\n"; out != want {
		t.Fatalf("answer printed %q, want %q", out, want)
	}
	// --secret and --account together settle nothing, and --account alone
	// needs its pick.
	y2, b2 := setupYardCLI(t)
	if _, err := y2.as(t, "yardmaster", "ask", b2.ID, "Set up clipphone?", "--setup", "clipphone",
		"--crew", "yardmaster", "--context", "C"); err != nil {
		t.Fatal(err)
	}
	if out, err := answerFrom(t, y2.a, "CLIPHONE_ACCOUNT=1555\n", b2.ID, "1", "1", "--secret", "--account", "clipphone2"); err == nil ||
		!strings.Contains(err.Error(), "one of the two") {
		t.Errorf("--secret with --account was taken or refused otherwise: %v\n%s", err, out)
	}
	if out, err := y2.as(t, "jo", "answer", b2.ID, "1", "--account", "clipphone2"); err == nil ||
		!strings.Contains(err.Error(), "with its preset pick") {
		t.Errorf("--account alone was taken or refused otherwise: %v\n%s", err, out)
	}
}

// A drift card whose file holds every name is kept with a bare preset
// pick: no --secret, no --account.
func TestBeadAnswerSetupKeep(t *testing.T) {
	t.Parallel()
	y, b := setupYardCLI(t)
	ctx := context.Background()
	if _, err := y.st.PutSource(ctx, "clipphone.yaml", cliSetupDecl, "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "clipphone.env")
	if err := os.WriteFile(file, []byte("CLIPHONE_ACCOUNT=1555\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetSource(ctx, "clipphone", store.SourceEnvFile, file, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Set up clipphone?", "--setup", "clipphone",
		"--crew", "yardmaster", "--context", "The yardmaster rings the person."); err != nil {
		t.Fatal(err)
	}
	out, err := y.as(t, "jo", "answer", b.ID, "1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1, asked by yardmaster: setup of pack clipphone, preset read to crew yardmaster\njo kept the standing account\njo approved it: approved, to be carried out\n"; out != want {
		t.Fatalf("answer printed %q, want %q", out, want)
	}
}
