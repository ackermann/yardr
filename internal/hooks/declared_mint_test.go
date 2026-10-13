package hooks_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
)

// acmeMinted is acmeThread with the two names that together select a
// minted credential, beside the variable the minted token is handed under.
var acmeMinted = strings.Replace(acmeThread, "  env: ACME_TOKEN\n", "  env: ACME_TOKEN\n  mint: [ACME_APP_ID, ACME_APP_KEY]\n", 1)

// mintDecl is acmeMinted with the thread commands in dir and the operator's
// mint_command, which answers token and records what it was handed.
func mintDecl(t *testing.T, dir, token string) (hooks.Declared, func() (runs int, stdin []string)) {
	t.Helper()
	ad := threadDecl(t, acmeMinted, acmeScripts(dir))
	mint := filepath.Join(dir, "mint")
	body := "#!/bin/sh\nd=" + dir + "\n" +
		"n=$(($(cat $d/mint.runs 2>/dev/null || echo 0)+1)); echo $n >$d/mint.runs\n" +
		"{ cat; echo; } >>$d/mint.stdin\n" +
		"printf '{\"token\":\"" + token + "\",\"expires_at\":\"2030-01-01T00:00:00Z\"}'\n"
	scripttest.Write(t, mint, body)
	ad.MintCommand = mint
	read := func() (int, []string) {
		raw, _ := os.ReadFile(filepath.Join(dir, "mint.runs"))
		var n int
		fmt.Sscanf(string(raw), "%d", &n)
		stdin, _ := os.ReadFile(filepath.Join(dir, "mint.stdin"))
		lines := strings.Split(strings.TrimSuffix(string(stdin), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
		return n, lines
	}
	return ad, read
}

// A crew member that holds only the mint's names replies, opens and sets
// state: the token is minted with the caller's scope and handed to the
// thread commands under credential.env.
func TestDeclaredMintReplyOpenState(t *testing.T) {
	const token = "tok-acme-mint-not-to-be-seen"
	dir := t.TempDir()
	ad, read := mintDecl(t, dir, token)
	ctx := context.Background()
	env := map[string]string{"ACME_APP_ID": "app-1", "ACME_APP_KEY": "keys/app.pem"}
	cred, err := ad.CredentialFrom(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cred, "mint:v1:") {
		t.Fatalf("the crew member's credential = %q: the mint's inputs go by reference", cred)
	}

	in := hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "ACME-1"}}
	if _, err := ad.Reply(ctx, in, "", "Hi", "acme-1:hi", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Open(ctx, "acme-team", "Title", "Body", "open-1", "", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Opened(ctx, "acme-team", "open-1", cred, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.SetState(ctx, "ACME-1", "done", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Forward(ctx, "ACME-1", "done", cred); err != nil {
		t.Fatal(err)
	}

	// Every thread command ran with the minted token, none with the inputs.
	for _, cmd := range []string{"reply", "open", "state"} {
		if raw, _ := os.ReadFile(filepath.Join(dir, cmd+".token")); string(raw) != token {
			t.Fatalf("%s ran with %q", cmd, raw)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "opened.stdin")); !strings.Contains(string(raw), `"key":"open-1"`) {
		t.Fatalf("opened saw %q", raw)
	}
	// The mint ran per scope: the thread, then the place opened in.
	runs, stdin := read()
	if runs != 2 {
		t.Fatalf("the mint ran %d times, want per scope", runs)
	}
	want := []string{
		`{"inputs":{"ACME_APP_ID":"app-1","ACME_APP_KEY":"keys/app.pem"},"scope":"ACME-1"}`,
		`{"inputs":{"ACME_APP_ID":"app-1","ACME_APP_KEY":"keys/app.pem"},"scope":"acme-team"}`,
	}
	if strings.Join(stdin, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the mint's stdin:\n%s", strings.Join(stdin, "\n"))
	}
}

// Credential and CredentialFrom keep the one precedence: the mint's names
// when set together, one of them alone refused, else the static variable.
func TestDeclaredMintCredential(t *testing.T) {
	dir := t.TempDir()
	ad, _ := mintDecl(t, dir, "tok-acme-mint")
	get := func(env map[string]string) func(string) string {
		return func(name string) string { return env[name] }
	}
	// The ways, the minted one first: names only, for crew show and the
	// pack plan.
	ways := hooks.CredentialWays("acme", ad.D)
	if len(ways) != 2 || ways[0].Kind != "minted" || strings.Join(ways[0].Names, ",") != "ACME_APP_ID,ACME_APP_KEY" ||
		ways[1].Kind != "API key" || strings.Join(ways[1].Names, ",") != "ACME_TOKEN" {
		t.Fatalf("ways = %+v", ways)
	}
	if got := hooks.CredentialNames("acme", ad.D); got != "ACME_APP_ID and ACME_APP_KEY, or ACME_TOKEN" {
		t.Fatalf("names = %q", got)
	}
	if way, ok := hooks.CredentialSet("acme", []string{"ACME_APP_ID", "ACME_APP_KEY", "ACME_TOKEN"}, ad.D); !ok || way.Kind != "minted" {
		t.Fatalf("with every name the minted way is not the one set: %+v %v", way, ok)
	}
	if way, ok := hooks.CredentialSet("acme", []string{"ACME_TOKEN"}, ad.D); !ok || way.Kind != "API key" {
		t.Fatalf("with the static name: %+v %v", way, ok)
	}

	for name, env := range map[string]map[string]string{
		"the mint's names": {"ACME_APP_ID": "app-1", "ACME_APP_KEY": "k"},
		"both ways":        {"ACME_APP_ID": "app-1", "ACME_APP_KEY": "k", "ACME_TOKEN": "tok-static"},
	} {
		if cred, err := ad.CredentialFrom(get(env)); err != nil || !strings.HasPrefix(cred, "mint:v1:") {
			t.Errorf("%s: %q %v", name, cred, err)
		}
	}
	if cred, err := ad.CredentialFrom(get(map[string]string{"ACME_TOKEN": "tok-static"})); err != nil || cred != "tok-static" {
		t.Errorf("the static variable: %q %v", cred, err)
	}
	// One name alone is refused, and not passed over for the static one.
	if _, err := ad.CredentialFrom(get(map[string]string{"ACME_APP_ID": "app-1", "ACME_TOKEN": "tok-static"})); err == nil || !strings.Contains(err.Error(), "is set without") {
		t.Errorf("one name alone: %v", err)
	}
	// Nothing set: what would, for the caller to say where.
	var missing *hooks.MissingCredential
	if _, err := ad.CredentialFrom(get(nil)); !errors.As(err, &missing) || missing.Names != "ACME_APP_ID and ACME_APP_KEY, or ACME_TOKEN" {
		t.Errorf("nothing set: %v", err)
	}
	// In a pane the process's environment is what is read.
	t.Setenv("ACME_APP_ID", "app-1")
	t.Setenv("ACME_APP_KEY", "k")
	if cred, err := ad.Credential(context.Background()); err != nil || !strings.HasPrefix(cred, "mint:v1:") {
		t.Errorf("in a pane: %q %v", cred, err)
	}
}

// Without the operator's command a minted credential fails naming the
// setting, and what a failing mint says or prints is never the token.
func TestDeclaredMintRefusals(t *testing.T) {
	const token = "tok-acme-mint-not-to-be-seen"
	ctx := context.Background()
	in := hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "ACME-1"}}
	env := map[string]string{"ACME_APP_ID": "app-1", "ACME_APP_KEY": "k"}
	get := func(name string) string { return env[name] }

	dir := t.TempDir()
	bare := threadDecl(t, acmeMinted, acmeScripts(dir))
	cred, err := bare.CredentialFrom(get)
	if err != nil {
		t.Fatal(err)
	}
	var noCommand *source.NoMintCommand
	if _, err := bare.Reply(ctx, in, "bear", "Hi", "k", cred); !errors.As(err, &noCommand) ||
		!strings.Contains(err.Error(), "yardr source set acme mint_command=<program> [args...]") {
		t.Fatalf("without the command: %v", err)
	}

	dir = t.TempDir()
	ad := threadDecl(t, acmeMinted, acmeScripts(dir))
	mint := filepath.Join(dir, "mint")
	body := "#!/bin/sh\necho '{\"token\":\"" + token + "\"}'\necho the vault is locked >&2\nexit 3\n"
	scripttest.Write(t, mint, body)
	ad.MintCommand = mint
	_, err = ad.Reply(ctx, in, "", "Hi", "k", cred)
	if err == nil || !strings.Contains(err.Error(), "the vault is locked") {
		t.Fatalf("a failing mint: %v", err)
	}
	for _, secret := range []string{token, "app-1", `"k"`, cred} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the failure shows %q: %v", secret, err)
		}
	}
	// What starts as a reference but holds no mint's inputs is refused
	// without showing it: it may hold secrets.
	if _, err := ad.Reply(ctx, in, "", "Hi", "k", "mint:v1:%%%"); err == nil ||
		!strings.Contains(err.Error(), "holds no mint's inputs") || strings.Contains(err.Error(), "%%%") {
		t.Fatalf("a corrupt reference: %v", err)
	}
}
