package cli

import (
	"context"
	"encoding/json"
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
	t.Parallel()
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
	// source grants lists the three, a row a call, each with its own
	// grant: given at a terminal, so no card. Its flags narrow it, and
	// --json is the records.
	gs, err := st.SourceGrants(ctx, "crm")
	if err != nil || len(gs) != 3 {
		t.Fatalf("the grants = %+v, %v", gs, err)
	}
	// Each call's own time, as the row says it: crew yardmaster as ada,
	// group builders, group builders as ada, in the store's order.
	at := func(i int, call string) string { return gs[i].Given[call].At.Local().Format("2 Jan 15:04") }
	if out, err = run(t, jo.sourceCmd(), "grants"); err != nil {
		t.Fatalf("source grants = %q, %v", out, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "SOURCE") ||
		!rowOf(lines[1], "crm", "crew member yardmaster as ada", "read (2 calls)", at(0, "read"), "ada") ||
		!rowOf(lines[2], "crm", "group builders", "read (2 calls)", at(1, "read"), "jo") ||
		!rowOf(lines[3], "crm", "group builders as ada", "customer", at(2, "customer"), "ada") ||
		lines[4] != "3 calls granted of 1 source to 2 holders: 0 may change something outside the yard, 2 call as a user." ||
		lines[5] != "take one back: yardr source revoke <source> <call> --crew|--group <holder> [--as <user>]" {
		t.Fatalf("source grants =\n%s", out)
	}
	if out, err = run(t, jo.sourceCmd(), "grants", "--group", "builders", "--as", "ada"); err != nil ||
		strings.Count(out, "\ncrm ") != 1 || !strings.Contains(out, "group builders as ada") ||
		!strings.Contains(out, "1 call granted of 1 source to 1 holder: 0 may change something outside the yard, 1 calls as a user.") {
		t.Fatalf("source grants --group builders --as ada = %q, %v", out, err)
	}
	if out, err = run(t, jo.sourceCmd(), "grants", "--crew", "yardmaster", "--source", "crm"); err != nil ||
		strings.Count(out, "\ncrm ") != 1 || !strings.Contains(out, "crew member yardmaster as ada") {
		t.Fatalf("source grants --crew yardmaster --source crm = %q, %v", out, err)
	}
	if out, err = run(t, jo.sourceCmd(), "grants", "--source", "nothing"); err != nil || out != "no call of a source is granted\n" {
		t.Fatalf("source grants of no source = %q, %v", out, err)
	}
	jo.json = true
	out, err = run(t, jo.sourceCmd(), "grants", "--group", "builders")
	jo.json = false
	var listed []store.SourceGrant
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed) != 2 ||
		listed[0].Given["read"].By != "jo" || listed[1].Given["customer"].By != "ada" {
		t.Fatalf("source grants --json --group builders = %q, %v", out, err)
	}
	// A later read call is covered by both the plain and as-user read
	// grants, so source add names both holders of the widened access.
	added := strings.Replace(calledSource, "calls:\n", "calls:\n  orders: { kind: read, request: { method: GET, path: /orders } }\n", 1)
	added = strings.Replace(added, "BASE", "https://crm.example.com", 1)
	if err := os.WriteFile(file, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, jo.sourceCmd(), "add", file); err != nil ||
		!strings.Contains(out, "through their grant of read, crew member yardmaster as ada, group builders may now call: orders") {
		t.Fatalf("source add with a new read call = %q, %v", out, err)
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

// rowOf reports whether line is a row of yardr source grants with these
// cells, in order.
func rowOf(line string, source, holder, calls, granted, by string) bool {
	rest, ok := strings.CutPrefix(line, source+" ")
	for _, cell := range []string{holder, calls, granted} {
		var i int
		if i = strings.Index(rest, cell); !ok || i < 0 || strings.TrimSpace(rest[:i]) != "" {
			return false
		}
		rest = rest[i+len(cell):]
	}
	return ok && strings.HasSuffix(rest, "  "+by)
}
