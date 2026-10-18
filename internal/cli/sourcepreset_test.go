package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// presetMail is a yard's google-mail declaration with the calls the
// embedded pack's drafts preset grants: read, and draft by its name.
const presetMail = `name: google-mail
base: https://gmail.googleapis.com
credential: { env: GOOGLE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  search: { kind: read, request: { method: GET, path: /gmail/v1/users/me/messages } }
  draft: { kind: update, request: { method: POST, path: /gmail/v1/users/me/drafts } }
`

func presetYard(t *testing.T, decl string) (*app, *app) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "google-mail.yaml")
	if err := os.WriteFile(file, []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	jo := &app{st: st, actor: "jo"}
	if _, err := run(t, jo.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	return jo, &app{st: st, actor: "ada"}
}

// yardr source grant --preset grants the preset's calls of the source,
// from the embedded pack that declares it, each as if by name.
func TestSourceGrantPreset(t *testing.T) {
	jo, _ := presetYard(t, presetMail)
	out, err := run(t, jo.sourceCmd(), "grant", "google-mail", "--preset", "drafts", "--group", "builders", "--crew", "yardmaster")
	if err != nil {
		t.Fatalf("grant --preset drafts = %v\n%s", err, out)
	}
	want := "group builders may call google-mail: draft (update), read\ncrew member yardmaster may call google-mail: draft (update), read\n"
	if out != want {
		t.Fatalf("grant --preset drafts prints %q, want %q", out, want)
	}
	gs, err := jo.st.SourceGrants(context.Background(), "google-mail")
	if err != nil || len(gs) != 2 || strings.Join(gs[0].Calls, ",") != "draft,read" || strings.Join(gs[1].Calls, ",") != "draft,read" {
		t.Fatalf("the grants after --preset drafts = %+v, %v", gs, err)
	}
	if out, err = run(t, jo.sourceCmd(), "show", "google-mail"); err != nil ||
		!strings.Contains(out, "group builders (through read)") || !strings.Contains(out, "crew member yardmaster (through read)") {
		t.Fatalf("source show = %q, %v", out, err)
	}
}

// --preset with --as is the named user's alone to give, as any as
// grant: the operator is refused it, and the user gives it.
func TestSourceGrantPresetAs(t *testing.T) {
	jo, ada := presetYard(t, presetMail)
	if _, err := run(t, jo.sourceCmd(), "grant", "google-mail", "--preset", "drafts", "--crew", "yardmaster", "--as", "ada"); err == nil ||
		!strings.Contains(err.Error(), "preset drafts of pack google-mail: granting a call of a source as ada is ada's to give, and nobody else's") {
		t.Fatalf("the operator granting a preset as ada = %v", err)
	}
	out, err := run(t, ada.sourceCmd(), "grant", "google-mail", "--preset", "drafts", "--crew", "yardmaster", "--as", "ada")
	if err != nil || out != "crew member yardmaster as ada may call google-mail: draft (update), read\n" {
		t.Fatalf("ada granting a preset as herself = %q, %v", out, err)
	}
}

// An unknown preset, a source no embedded pack declares, a preset call
// the yard's declaration has not, and read or a call beside --preset
// are each refused.
func TestSourceGrantPresetRefusals(t *testing.T) {
	jo, _ := presetYard(t, presetMail)
	for _, c := range []struct{ args, want []string }{
		{[]string{"grant", "google-mail", "--preset", "nosuch", "--crew", "yardmaster"},
			[]string{"pack google-mail has no preset nosuch", "yardr pack show google-mail", "in a terminal"}},
		{[]string{"grant", "google-mail", "read", "--preset", "drafts", "--crew", "yardmaster"},
			[]string{"want the source alone with --preset <name>"}},
		{[]string{"grant", "google-mail", "--crew", "yardmaster"},
			[]string{`accepts 2 arg(s), received 1`}},
	} {
		if _, err := run(t, jo.sourceCmd(), c.args...); err == nil {
			t.Fatalf("%s granted", strings.Join(c.args, " "))
		} else {
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error = %q, want %q", strings.Join(c.args, " "), err, want)
				}
			}
		}
	}

	// A source no embedded pack declares, and a preset call the yard's
	// declaration has not, are granted by name instead.
	file := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(calledSource, "BASE", "https://crm.example.com", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, jo.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, jo.sourceCmd(), "grant", "crm", "--preset", "read", "--crew", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "source crm: no embedded pack declares it") ||
		!strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("a preset of a source no pack declares = %v", err)
	}
	jo, _ = presetYard(t, strings.Replace(presetMail, "  draft: { kind: update, request: { method: POST, path: /gmail/v1/users/me/drafts } }\n", "", 1))
	if _, err := run(t, jo.sourceCmd(), "grant", "google-mail", "--preset", "drafts", "--crew", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), `preset drafts of pack google-mail: source google-mail has no call "draft"`) {
		t.Fatalf("a preset call the declaration has not = %v", err)
	}
}
