package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// tellFile is a source that tells people: its credential's name and its
// address name are both a user's to give, in their env file.
const tellFile = `name: tell
base: https://tell.example.com
credential: { env: TELL_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string, max: 1024 } }
    request: { method: POST, path: /messages.json, body: { user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: TELL_CHAT }, text: text }
`

// userRemovedAsGrants is how many as grants the newest user_removed
// event says went with the user.
func userRemovedAsGrants(t *testing.T, s store.API, ctx context.Context) string {
	t.Helper()
	evs, err := s.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "user_removed" {
			return fmt.Sprint(evs[i].Data["as_grants"])
		}
	}
	t.Fatal("no user_removed event")
	return ""
}

// Removing a user who was given a credential and an as grant takes both
// with them, and the default env file with them: a user added again
// under the name has neither, giving an address does not bring the
// credential back, and a call as them is refused for want of the grant.
func TestDeleteUserRemovesAsGrantAndCredential(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	s, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSource(ctx, "tell.yaml", tellFile, "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "users", "ada.env")
	keyed, err := s.KeyUserCredential(ctx, "ada", "crm", map[string]string{"CRM_TOKEN": "ada-token-not-shown"}, "ada")
	if err != nil || keyed.File != file {
		t.Fatalf("giving ada her credential = %+v, %v", keyed, err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := userRemovedAsGrants(t, s, ctx); got != "1" {
		t.Fatalf("the user_removed event says %s as grants went, want 1", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("ada's default env file stands after her removal: %v", err)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 0 {
		t.Fatalf("grants after ada went = %+v", gs)
	}

	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.GetUser(ctx, "ada"); err != nil || u.EnvFile != "" {
		t.Fatalf("the new ada = %+v, %v", u, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the new ada has a file standing for her: %v", err)
	}
	// Giving her address does not bring the credential back.
	if _, err := s.KeyUser(ctx, "ada", "tell", map[string]string{"TELL_CHAT": "ada-chat"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "TELL_CHAT=ada-chat\n" {
		t.Fatalf("the new ada's file holds %q", b)
	}
	if _, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", Actor: "yardmaster"}); err == nil ||
		!strings.Contains(err.Error(), "is not granted it") {
		t.Fatalf("a call after the grant went with ada = %v", err)
	}
}

// Two users pointed at one custom file: removing the first is refused
// whole — her record, her as grant, and both a credential name and an
// address name in the file untouched — until the second is moved to a
// file of her own; then only the first's now-exclusive file is struck,
// its other lines kept.
func TestDeleteUserSharedFileRefused(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	s, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := crmYard(t, s)
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutSource(ctx, "tell.yaml", tellFile, "jo"); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "shared.env")
	const body = "# ours\nCRM_TOKEN=ada-token\nTELL_CHAT=ada-chat\nOTHER=kept\n"
	if err := os.WriteFile(shared, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.SetUser(ctx, u, map[string]string{store.UserEnvFile: shared}, u); err != nil {
			t.Fatal(err)
		}
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteUser(ctx, "ada", "jo")
	resolved, rerr := filepath.EvalSymlinks(shared)
	if rerr != nil {
		t.Fatal(rerr)
	}
	want := fmt.Sprintf("user ada is not removed: bia's env_file is the same file (%s); move one of them first (yardr user set bia env_file=<file>) and remove ada again", resolved)
	if err == nil || err.Error() != want {
		t.Fatalf("removing ada over a shared file = %v\nwant %q", err, want)
	}
	if _, err := s.GetUser(ctx, "ada"); err != nil {
		t.Fatalf("ada went on a refused removal: %v", err)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 1 || gs[0].As != "ada" {
		t.Fatalf("ada's grant after a refused removal = %+v", gs)
	}
	if b, _ := os.ReadFile(shared); string(b) != body {
		t.Fatalf("the shared file after a refused removal holds %q", b)
	}

	own := filepath.Join(t.TempDir(), "bia.env")
	if _, err := s.SetUser(ctx, "bia", map[string]string{store.UserEnvFile: own}, "bia"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(shared); string(b) != "# ours\nOTHER=kept\n" {
		t.Fatalf("ada's file struck of her names holds %q", b)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 0 {
		t.Fatalf("grants after ada went = %+v", gs)
	}
	if _, err := s.GetUser(ctx, "bia"); err != nil {
		t.Fatalf("bia went with ada: %v", err)
	}
}

// A second user whose custom file is the first's default file blocks
// the first's removal in the same way, and that default file is not
// deleted. A file that cannot be cleared refuses it too, untouched.
func TestDeleteUserBlockedByDefaultAndUnclearable(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	s, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := crmYard(t, s)
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "users", "ada.env")
	if _, err := s.KeyUserCredential(ctx, "ada", "crm", map[string]string{"CRM_TOKEN": "ada-token"}, "ada"); err != nil {
		t.Fatal(err)
	}
	// Point bia at ada's default file through a link to it; ada's
	// record names the file itself, as the giving set it, and the two
	// count as the one file.
	link := filepath.Join(t.TempDir(), "ada-link.env")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUser(ctx, "bia", map[string]string{store.UserEnvFile: link}, "bia"); err != nil {
		t.Fatal(err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteUser(ctx, "ada", "jo")
	resolved, rerr := filepath.EvalSymlinks(file)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil || !strings.Contains(err.Error(), "user ada is not removed: bia's env_file is the same file ("+resolved+")") {
		t.Fatalf("removing ada with bia on her default file = %v", err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "CRM_TOKEN=ada-token") {
		t.Fatalf("ada's default file was touched: %q", b)
	}
	if _, err := s.GetUser(ctx, "ada"); err != nil {
		t.Fatalf("ada went on a refused removal: %v", err)
	}

	// Move bia away, then point ada at a file of her own the yard may
	// not write: open to others, it is refused as it is read, and
	// nothing changes.
	if _, err := s.SetUser(ctx, "bia", map[string]string{store.UserEnvFile: filepath.Join(t.TempDir(), "bia.env")}, "bia"); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "ada-own.env")
	if err := os.WriteFile(custom, []byte("CRM_TOKEN=ada-token\nOTHER=kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUser(ctx, "ada", map[string]string{store.UserEnvFile: custom}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(custom, 0o644); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteUser(ctx, "ada", "jo")
	resolvedCustom, rerr := filepath.EvalSymlinks(custom)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil || !strings.Contains(err.Error(), "user ada is not removed: their credential is not cleared from "+resolvedCustom) {
		t.Fatalf("removing ada with an unclearable file = %v", err)
	}
	if _, err := s.GetUser(ctx, "ada"); err != nil {
		t.Fatalf("ada went on a refused clearing: %v", err)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 1 {
		t.Fatalf("ada's grant after a refused clearing = %+v", gs)
	}
	if b, _ := os.ReadFile(custom); !strings.Contains(string(b), "CRM_TOKEN=ada-token") {
		t.Fatalf("ada's file was struck on a refused removal: %q", b)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "CRM_TOKEN=ada-token") {
		t.Fatalf("ada's default file was touched: %q", b)
	}
}
