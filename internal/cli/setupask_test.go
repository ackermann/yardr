package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	y.own.SetPackReader(func(ctx context.Context, name string, yard store.API) (*store.PackReading, error) {
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
	out, err = answerFrom(t, y.a, "CLIPHONE_ACCOUNT=1555\n", b.ID, "1", "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1, asked by yardmaster: setup of pack clipphone, preset read to crew yardmaster\njo gave CLIPHONE_ACCOUNT\njo approved it: approved, to be carried out\n"; out != want {
		t.Fatalf("answer printed %q, want %q", out, want)
	}
}

// A setup ask is refused for the combinations it takes none of, for no
// grantee, and for a pack the binary does not embed.
func TestBeadAskSetupRefusals(t *testing.T) {
	y, b := setupYardCLI(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"run":           {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--run", "user add x", "--context", "C"}, "one of the two"},
		"secret":        {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--secret", "crm", "--context", "C"}, "one of the two"},
		"as":            {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--as", "ada", "--context", "C"}, "--setup refuses --as"},
		"for-user":      {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--for-user", "ada", "--context", "C"}, "not --setup"},
		"no grantee":    {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--context", "C"}, "--crew <name> or --group <name>"},
		"crew without":  {[]string{"ask", b.ID, "Q", "--crew", "yardmaster", "--context", "C", "--option", "A: B"}, "they go with --setup <pack>"},
		"no such pack":  {[]string{"ask", b.ID, "Q", "--setup", "nosuch", "--crew", "yardmaster", "--context", "C"}, "embeds no pack"},
		"writer option": {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C", "--option", "A: B"}, "takes no --option"},
		"bad recommend": {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C", "--recommend", "9", "--why", "W"}, "--recommend 9 names none"},
		"operator asks": {[]string{"ask", b.ID, "Q", "--setup", "clipphone", "--crew", "yardmaster", "--context", "C"}, "needs no approval"},
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
}
