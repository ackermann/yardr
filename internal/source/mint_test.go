package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
)

// mintDecl is the declaration of minted: crm with the two names that
// together select a minted credential.
func mintDecl(t *testing.T) *Declaration {
	t.Helper()
	d, err := Parse("crm.yaml", []byte(minted))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The precedence both credential paths keep: the mint's names when set
// together, one of them alone refused, else the static variable.
func TestSelectMintPrecedence(t *testing.T) {
	d := mintDecl(t)
	get := func(file map[string]string) func(string) string {
		return func(name string) string { return file[name] }
	}
	file := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": " keys/app.pem "}
	value, inputs, err := d.Credential.Select("CRM_TOKEN", get(file))
	if err != nil || value != "" || inputs["CRM_APP_ID"] != "app-1" || inputs["CRM_APP_KEY"] != "keys/app.pem" || len(inputs) != 2 {
		t.Fatalf("set together: %q %+v, %v", value, inputs, err)
	}
	// One of them alone is refused, and not passed over for the static
	// variable: a caller meant to mint would call as another.
	file["CRM_TOKEN"] = "tok-static"
	for _, drop := range []string{"CRM_APP_ID", "CRM_APP_KEY"} {
		part := map[string]string{}
		for k, v := range file {
			part[k] = v
		}
		delete(part, drop)
		if _, _, err := d.Credential.Select("CRM_TOKEN", get(part)); err == nil || !strings.Contains(err.Error(), "is set without") || !strings.Contains(err.Error(), "takes every name") {
			t.Errorf("without %s: %v", drop, err)
		}
	}
	// Else the static variable, used as it is set.
	delete(file, "CRM_APP_ID")
	delete(file, "CRM_APP_KEY")
	if value, inputs, err := d.Credential.Select("CRM_TOKEN", get(file)); err != nil || value != "tok-static" || inputs != nil {
		t.Fatalf("static: %q %+v, %v", value, inputs, err)
	}
	// Nothing set: what would, for the caller to say where.
	delete(file, "CRM_TOKEN")
	var missing *Missing
	if _, _, err := d.Credential.Select("CRM_TOKEN", get(file)); !errors.As(err, &missing) || missing.Names != "CRM_APP_ID and CRM_APP_KEY, or CRM_TOKEN" {
		t.Fatalf("nothing set: %v", err)
	}
	// Without a mint the static variable is the whole of it.
	one, err := Parse("crm.yaml", []byte(crm))
	if err != nil {
		t.Fatal(err)
	}
	if value, inputs, err := one.Credential.Select("CRM_TOKEN", get(map[string]string{"CRM_TOKEN": "tok-1"})); err != nil || value != "tok-1" || inputs != nil {
		t.Fatalf("no mint: %q %+v, %v", value, inputs, err)
	}
	if _, _, err := one.Credential.Select("CRM_TOKEN", get(nil)); !errors.As(err, &missing) || missing.Names != "CRM_TOKEN" {
		t.Fatalf("no mint, nothing set: %v", err)
	}
}

// A reference carries the mint's inputs to the call, which resolves it with
// the caller's scope; anything else is the static credential, used as it is.
func TestMintRefRoundTrip(t *testing.T) {
	in := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "keys/app.pem"}
	ref := MintRef(in)
	if !strings.HasPrefix(ref, "mint:v1:") {
		t.Fatalf("ref = %q", ref)
	}
	got, err := UnmintRef(ref, []string{"CRM_APP_ID", "CRM_APP_KEY"})
	if err != nil || len(got) != 2 || got["CRM_APP_ID"] != "app-1" || got["CRM_APP_KEY"] != "keys/app.pem" {
		t.Fatalf("unmint = %+v, %v", got, err)
	}
	// Order does not matter: the reference holds a JSON object.
	if got, err := UnmintRef(ref, []string{"CRM_APP_KEY", "CRM_APP_ID"}); err != nil || len(got) != 2 {
		t.Fatalf("unmint in another order = %+v, %v", got, err)
	}
	if got, err := UnmintRef("tok-static", []string{"CRM_APP_ID", "CRM_APP_KEY"}); err != nil || got != nil {
		t.Fatalf("a static credential = %+v, %v", got, err)
	}
	for name, ref := range map[string]string{
		"no base64":   "mint:v1:%%%",
		"no JSON":     "mint:v1:" + "e30", // "00", valid base64, no JSON object
		"other names": MintRef(map[string]string{"OTHER": "x"}),
		"fewer names": MintRef(map[string]string{"CRM_APP_ID": "app-1"}),
		"more names":  MintRef(map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "k", "OTHER": "x"}),
	} {
		if _, err := UnmintRef(ref, []string{"CRM_APP_ID", "CRM_APP_KEY"}); err == nil || !strings.Contains(err.Error(), "holds no mint's inputs") {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), ref) {
			t.Errorf("%s shows the credential: %v", name, err)
		}
	}
}

// mintScript is a mint_command that records what it was handed and answers
// a token per run: dir/stdin counts the runs (one JSON object per line),
// dir/env.<n> the environment of run n, dir/base.<n> YARDR_SOURCE_BASE.
// Each token lives an hour from now.
func mintScript(t *testing.T, dir, now string) string {
	t.Helper()
	p := filepath.Join(dir, "mint")
	body := `#!/bin/sh
d=` + dir + `
n=$(($(cat $d/runs 2>/dev/null || echo 0)+1)); echo $n >$d/runs
{ cat; echo; } >>$d/stdin
env | sort >$d/env.$n
printf '%s' "$YARDR_SOURCE_BASE" >$d/base.$n
printf '{"token":"tok-for-run-%s","expires_at":"%s"}' "$n" "` + now + `"
`
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func mintRuns(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "runs"))
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(string(raw), "%d", &n)
	return n
}

// A mint runs the command with {inputs, scope} on stdin and keeps the token
// in this process's memory only: per credential and scope, asked for again
// shortly before it expires.
func TestMintRunsAndCaches(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	t.Cleanup(SetMintClock(func() time.Time { return now }))
	t.Setenv("YARDR_SESSION", "not-for-a-mint")
	t.Setenv("OTHER_SERVICE_TOKEN", "not-for-a-mint")
	setEgress(t)
	dir := t.TempDir()
	script := mintScript(t, dir, "2026-10-11T13:00:00Z")
	d := mintDecl(t)
	ctx := context.Background()
	argv := []string{script}
	inputs := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": dir + "/app.pem"}

	token, expires, err := Mint(ctx, d, argv, inputs, "acme/webapp#7")
	if err != nil || token != "tok-for-run-1" || !expires.Equal(time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("mint = %q %v, %v", token, expires, err)
	}
	// The command got the inputs and the scope on stdin, the base in its
	// environment, and nothing of the yard's or the credential's own.
	raw, err := os.ReadFile(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"inputs":{"CRM_APP_ID":"app-1","CRM_APP_KEY":"` + dir + `/app.pem"},"scope":"acme/webapp#7"}` + "\n"; string(raw) != want {
		t.Fatalf("stdin = %q, want %q", raw, want)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "base.1")); string(raw) != "https://crm.example.com/api/v2" {
		t.Fatalf("YARDR_SOURCE_BASE = %q", raw)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "env.1"))
	if strings.Contains(string(raw), "CRM_APP_ID") || strings.Contains(string(raw), "tok-for") {
		t.Fatalf("the command's environment holds the credential:\n%s", raw)
	}
	// It got the proxy and CA variables of the process that asked, which a
	// mint behind a company's proxy needs to reach its service, and none of
	// the yard's: not the session's identity, not a credential, not a proxy
	// setting that is not among the nine.
	for _, kv := range egressEnviron {
		if !strings.Contains("\n"+string(raw), "\n"+kv+"\n") {
			t.Errorf("the command's environment lacks %q:\n%s", kv, raw)
		}
	}
	for _, not := range []string{"not-for-a-mint", "ALL_PROXY=", "CURL_CA_BUNDLE="} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the command's environment holds %q:\n%s", not, raw)
		}
	}

	// Within the token's life nothing is asked again: the same scope uses
	// the token in hand.
	if token, _, err := Mint(ctx, d, argv, inputs, "acme/webapp#7"); err != nil || token != "tok-for-run-1" || mintRuns(t, dir) != 1 {
		t.Fatalf("again: %q %v (%d runs)", token, err, mintRuns(t, dir))
	}
	// Another scope has a token of its own, and so has another credential.
	if token, _, err := Mint(ctx, d, argv, inputs, "acme/other#1"); err != nil || token != "tok-for-run-2" {
		t.Fatalf("another scope: %q %v", token, err)
	}
	other := map[string]string{"CRM_APP_ID": "app-2", "CRM_APP_KEY": dir + "/app.pem"}
	if token, _, err := Mint(ctx, d, argv, other, "acme/webapp#7"); err != nil || token != "tok-for-run-3" {
		t.Fatalf("another credential: %q %v", token, err)
	}
	// Held while the token is asked for, so calls at once ask once.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := Mint(ctx, d, argv, inputs, "acme/race#1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := mintRuns(t, dir); n != 4 {
		t.Fatalf("calls at once ran the command %d times, want 4", n)
	}

	// The cache holds the tokens, but its keys hold no secret value: the
	// credential's identity in them is a hash of the inputs.
	mintedTokens.Lock()
	defer mintedTokens.Unlock()
	for k := range mintedTokens.m {
		for _, secret := range []string{"app-1", "app-2", "app.pem", "tok-for-run-1"} {
			if strings.Contains(k, secret) {
				t.Errorf("a cache key holds %q: %q", secret, k)
			}
		}
		// The source, the command and the scope are no secret, and key it.
		if !strings.Contains(k, "crm") || !strings.Contains(k, script) || !strings.Contains(k, "acme/") {
			t.Errorf("a cache key names no source, command and scope: %q", k)
		}
	}
}

// A mint command sees the asking user's name: USER and LOGNAME as the
// process that asks has them, since a helper that reads the user's
// keyring looks it up by user name.
func TestMintSeesUser(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	t.Cleanup(SetMintClock(func() time.Time { return now }))
	t.Setenv("USER", "mint-user")
	t.Setenv("LOGNAME", "mint-login")
	p := filepath.Join(t.TempDir(), "mint")
	body := `#!/bin/sh
[ "$USER" = "mint-user" ] || { echo "USER is ${USER-unset}" >&2; exit 1; }
[ "$LOGNAME" = "mint-login" ] || { echo "LOGNAME is ${LOGNAME-unset}" >&2; exit 1; }
printf '{"token":"tok-1","expires_at":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}'
`
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "app.pem"}
	token, _, err := Mint(context.Background(), mintDecl(t), []string{p}, inputs, "s")
	if err != nil || token != "tok-1" {
		t.Fatalf("mint = %q, %v", token, err)
	}
}

// Without USER or LOGNAME in the asking process (the server and the daemon
// start with neither: server.ServerEnv passes none), the mint still runs
// under the process's own user: each empty name falls back to the OS
// user's, so a helper that reads the keyring by user name answers with one.
func TestMintWithoutUserEnv(t *testing.T) {
	me, err := config.LookupUsername(user.Current)
	if err != nil || me == "" {
		t.Skipf("the system names no user for this process: %v", err)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	t.Cleanup(SetMintClock(func() time.Time { return now }))
	for _, tc := range []struct {
		name, user, login string
	}{
		{name: "both empty"},
		{name: "USER empty", login: "mint-login"},
		{name: "LOGNAME empty", user: "mint-user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("USER", tc.user)
			t.Setenv("LOGNAME", tc.login)
			wantUser, wantLogin := tc.user, tc.login
			if wantUser == "" {
				wantUser = me
			}
			if wantLogin == "" {
				wantLogin = me
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "want-user"), []byte(wantUser), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "want-login"), []byte(wantLogin), 0o644); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "mint")
			body := `#!/bin/sh
want_user=$(cat ` + dir + `/want-user)
want_login=$(cat ` + dir + `/want-login)
[ "$USER" = "$want_user" ] || { echo "USER is ${USER-unset}" >&2; exit 1; }
[ "$LOGNAME" = "$want_login" ] || { echo "LOGNAME is ${LOGNAME-unset}" >&2; exit 1; }
printf '{"token":"tok-1","expires_at":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}'
`
			if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			inputs := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "app.pem"}
			token, _, err := Mint(context.Background(), mintDecl(t), []string{p}, inputs, "s")
			if err != nil || token != "tok-1" {
				t.Fatalf("mint = %q, %v", token, err)
			}
		})
	}
}

// Five minutes before a token expires it is no longer used.
func TestMintExpiryMargin(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	base, clock := now, &now
	t.Cleanup(SetMintClock(func() time.Time { return *clock }))
	dir := t.TempDir()
	script := mintScript(t, dir, "2026-10-11T13:00:00Z")
	d := mintDecl(t)
	ctx := context.Background()
	argv := []string{script}
	inputs := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "app.pem"}

	if _, _, err := Mint(ctx, d, argv, inputs, "s"); err != nil {
		t.Fatal(err)
	}
	*clock = base.Add(54 * time.Minute)
	if token, _, err := Mint(ctx, d, argv, inputs, "s"); err != nil || token != "tok-for-run-1" || mintRuns(t, dir) != 1 {
		t.Fatalf("54 minutes on: %q %v (%d runs)", token, err, mintRuns(t, dir))
	}
	*clock = base.Add(56 * time.Minute)
	if token, _, err := Mint(ctx, d, argv, inputs, "s"); err != nil || token != "tok-for-run-2" {
		t.Fatalf("56 minutes on: %q %v", token, err)
	}
}

func TestMintPrunesExpiredCacheEntries(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	t.Cleanup(SetMintClock(func() time.Time { return now }))
	stale, live := "stale-mint-cache-entry", "live-mint-cache-entry"
	mintedTokens.Lock()
	mintedTokens.m[stale] = mintedToken{token: "expired", expires: now.Add(-time.Second)}
	mintedTokens.m[live] = mintedToken{token: "valid", expires: now.Add(time.Hour)}
	mintedTokens.Unlock()

	dir := t.TempDir()
	script := mintScript(t, dir, "2026-10-11T13:00:00Z")
	if _, _, err := Mint(context.Background(), mintDecl(t), []string{script},
		map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "app.pem"}, "new-scope"); err != nil {
		t.Fatal(err)
	}
	mintedTokens.Lock()
	defer mintedTokens.Unlock()
	if _, ok := mintedTokens.m[stale]; ok {
		t.Error("expired token remained cached after a mint")
	}
	if _, ok := mintedTokens.m[live]; !ok {
		t.Error("unexpired token was pruned")
	}
}

// What goes wrong with the mint command is said with its stderr, never with
// what it printed: a command that fails may have printed anything.
func TestMintRefusals(t *testing.T) {
	t.Cleanup(SetMintClock(time.Now))
	d := mintDecl(t)
	ctx := context.Background()
	inputs := map[string]string{"CRM_APP_ID": "app-1", "CRM_APP_KEY": "app.pem"}

	var noCommand *NoMintCommand
	if _, _, err := Mint(ctx, d, nil, inputs, "s"); !errors.As(err, &noCommand) || !strings.Contains(err.Error(), "yardr source set crm mint_command=<program> [args...]") {
		t.Fatalf("no mint_command: %v", err)
	}

	script := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "mint")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"missing":              {"exit 0", "no such file"}, // replaced below with a program that is not there
		"an exit":              {`echo "the vault is locked" >&2; echo '{"token":"tok-shown-nowhere"}'; exit 3`, "exit status 3"},
		"no JSON":              {`printf 'not json'`, "its answer is no {token, expires_at}"},
		"no token":             {`printf '{"token":"","expires_at":"2026-10-11T13:00:00Z"}'`, "holds no token"},
		"a token of two lines": {`printf '{"token":"tok\\ntwo","expires_at":"2026-10-11T13:00:00Z"}'`, "a control character"},
		"no expiry":            {`printf '{"token":"tok-1"}'`, "holds no expiry yardr reads"},
		"an expiry past":       {`printf '{"token":"tok-1","expires_at":"2020-01-01T00:00:00Z"}'`, "already expired"},
	} {
		argv := []string{script(t, tc.body)}
		if name == "missing" {
			argv = []string{filepath.Join(t.TempDir(), "nope")}
		}
		_, _, err := Mint(ctx, d, argv, inputs, "s")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), "source crm") {
			t.Errorf("%s names no source: %v", name, err)
		}
		for _, secret := range []string{"tok-shown-nowhere", "tok-1", "tok\ntwo", "app-1", "app.pem"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s shows %q: %v", name, secret, err)
			}
		}
	}
	if _, _, err := Mint(ctx, d, []string{script(t, `echo "the vault is locked" >&2; exit 3`)}, inputs, "s"); err == nil || !strings.Contains(err.Error(), "the vault is locked") {
		t.Errorf("stderr is not carried: %v", err)
	}
}
