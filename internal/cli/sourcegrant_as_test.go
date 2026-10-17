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

// yardr source grant --as <user> is given by that user alone, the
// operator being refused it while still giving a plain grant; source
// show, crew show and group list say "as <user>" for who may call, and
// revoke by the same words takes only that grant back.
func TestSourceGrantAs(t *testing.T) {
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
	jo, ada := &app{st: st, actor: "jo"}, &app{st: st, actor: "ada"}
	file := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(calledSource, "BASE", "https://crm.example.com", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, jo.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, jo.sourceCmd(), "grant", "crm", "read", "--crew", "yardmaster", "--as", "ada"); err == nil ||
		!strings.Contains(err.Error(), "granting a call of a source as ada is ada's to give") ||
		!strings.Contains(err.Error(), "this is the operator (jo)") {
		t.Fatalf("the operator granting as ada = %v", err)
	}
	out, err := run(t, ada.sourceCmd(), "grant", "crm", "read", "--crew", "yardmaster", "--as", "ada")
	if err != nil || out != "crew member yardmaster as ada may call crm: read\n" {
		t.Fatalf("ada granting as herself = %q, %v", out, err)
	}
	if out, err = run(t, ada.sourceCmd(), "grant", "crm", "customer", "--group", "builders", "--as", "ada"); err != nil ||
		out != "group builders as ada may call crm: customer\n" {
		t.Fatalf("ada granting a group as herself = %q, %v", out, err)
	}
	if out, err = run(t, jo.sourceCmd(), "grant", "crm", "read", "--group", "builders"); err != nil ||
		out != "group builders may call crm: read\n" {
		t.Fatalf("a plain grant by the operator = %q, %v", out, err)
	}

	if out, err = run(t, jo.sourceCmd(), "show", "crm"); err != nil ||
		!strings.Contains(out, "crew member yardmaster as ada (through read)") ||
		!strings.Contains(out, "group builders as ada") || !strings.Contains(out, "group builders (through read)") {
		t.Fatalf("source show = %q, %v", out, err)
	}
	if out, err = run(t, jo.crewCmd(), "show", "yardmaster"); err != nil ||
		!strings.Contains(out, "  may call crm as ada: read\n") {
		t.Fatalf("crew show = %q, %v", out, err)
	}
	if out, err = run(t, jo.groupCmd(), "show", "builders"); err != nil ||
		!strings.Contains(out, "  calls crm: read\n") || !strings.Contains(out, "  calls crm as ada: customer\n") {
		t.Fatalf("group show = %q, %v", out, err)
	}
	if out, err = run(t, jo.groupCmd(), "list"); err != nil ||
		!strings.Contains(out, "crm: read") || !strings.Contains(out, "crm as ada: customer") {
		t.Fatalf("group list = %q, %v", out, err)
	}

	// Anyone takes an as grant back, by the same words, and the plain
	// one stands.
	out, err = run(t, jo.sourceCmd(), "revoke", "crm", "read", "--crew", "yardmaster", "--as", "ada")
	if err != nil || out != "crew member yardmaster as ada may call crm: nothing\n" {
		t.Fatalf("revoking as ada = %q, %v", out, err)
	}
	if out, err = run(t, jo.crewCmd(), "show", "yardmaster"); err != nil || strings.Contains(out, "may call crm") {
		t.Fatalf("crew show after the revoke = %q, %v", out, err)
	}
	if out, err = run(t, jo.sourceCmd(), "show", "crm"); err != nil ||
		!strings.Contains(out, "group builders (through read)") || strings.Contains(out, "yardmaster as ada") {
		t.Fatalf("source show after the revoke = %q, %v", out, err)
	}
}
