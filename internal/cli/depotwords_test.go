package cli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// A depot was a rig until this release. What a script, a role file or a
// session from before the upgrade says in the old words still works, and
// is not listed: the command rig, --rig and -r, --where rig= and a kind
// that said rig. yardr answers with the new words.
func TestTheRigWordsStillWork(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	if out := mustAs(t, st, "jo", "rig", "add", "web", t.TempDir(), "--kind", "dir"); !strings.HasPrefix(out, "depot web ") {
		t.Fatalf("rig add = %q", out)
	}
	mustAs(t, st, "jo", "depot", "add", "docs", t.TempDir(), "--kind", "dir")
	if out := mustAs(t, st, "jo", "rig", "list"); !strings.Contains(out, "web") || !strings.Contains(out, "docs") {
		t.Fatalf("rig list = %q", out)
	}

	// A bead's depot, by the old flag and by the letter it had.
	long := strings.TrimSpace(mustAs(t, st, "jo", "bead", "create", "by the old flag", "--rig", "web"))
	short := strings.TrimSpace(mustAs(t, st, "jo", "bead", "create", "by the old letter", "-r", "web"))
	for _, id := range []string{long, short} {
		if b, err := st.GetBead(ctx, id); err != nil || b.Depot == nil || *b.Depot != "web" {
			t.Fatalf("bead %s = %+v, %v; want it in web", id, b, err)
		}
	}
	for _, flag := range []string{"--rig", "-r", "--depot", "-d"} {
		if out := mustAs(t, st, "jo", "bead", "list", flag, "web"); !strings.Contains(out, long) || !strings.Contains(out, short) || !strings.Contains(out, "DEPOT") {
			t.Fatalf("bead list %s web = %q", flag, out)
		}
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "bead", "show", long, "--json")), &shown); err != nil || shown["depot"] != "web" || shown["rig"] != nil {
		t.Fatalf("bead show --json has the fields %v, %v; want depot", shown, err)
	}
	// update and route add ask whether the flag was given.
	mustAs(t, st, "jo", "bead", "update", short, "--rig", "docs")
	if b, err := st.GetBead(ctx, short); err != nil || b.Depot == nil || *b.Depot != "docs" {
		t.Fatalf("bead update --rig docs = %+v, %v", b, err)
	}
	mustAs(t, st, "jo", "group", "add", "people", "--runner", "manual")
	for _, flag := range []string{"--rig", "-r"} {
		id := strings.TrimSpace(mustAs(t, st, "jo", "route", "add", "--stage", "new", "--group", "people", flag, "web"))
		routes, err := st.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			if r.ID == id && (r.Depot == nil || *r.Depot != "web") {
				t.Fatalf("route add %s web = %+v; want it for web alone", flag, r)
			}
		}
		mustAs(t, st, "jo", "route", "rm", id)
	}

	// A filter by the old key and an old kind, stored and removed as the
	// new.
	if out := mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "rig=web", "--where", "kind=rig_updated"); out != "jo subscribed: yardr all depot=web kind=depot_updated\n" {
		t.Fatalf("subscribe --where rig=web = %q", out)
	}
	if out := mustAs(t, st, "jo", "unsubscribe", "jo", "yardr", "--where", "rig=web", "--where", "kind=rig_updated"); out != "jo: 1 subscription(s) removed\n" {
		t.Fatalf("unsubscribe --where rig=web = %q", out)
	}

	if out := mustAs(t, st, "jo", "rig", "check", "docs"); !strings.Contains(out, "depot docs") {
		t.Fatalf("rig check = %q", out)
	}
	mustAs(t, st, "jo", "bead", "close", short, "--reason", "done")
	mustAs(t, st, "jo", "rig", "rm", "docs")
	if _, err := st.GetDepot(ctx, "docs"); err == nil {
		t.Fatal("rig rm left the depot")
	}

	// Help has the new words only, and the old command has help of its own.
	old := regexp.MustCompile(`(?i)\brigs?\b|--rig|\s-r\b`)
	for _, args := range [][]string{{"--help"}, {"depot", "--help"}, {"depot", "add", "--help"}, {"depot", "check", "--help"},
		{"depot", "init", "--help"}, {"bead", "create", "--help"}, {"bead", "list", "--help"}, {"bead", "update", "--help"},
		{"route", "add", "--help"}, {"subscribe", "--help"}, {"pack", "check", "--help"}, {"pack", "apply", "--help"},
		{"peer", "intake", "--help"}} {
		out := mustAs(t, st, "jo", args...)
		if !strings.Contains(out, "depot") || old.MatchString(out) {
			t.Errorf("yardr %s names a rig (%q), or no depot:\n%s", strings.Join(args, " "), old.FindString(out), out)
		}
	}
	if out := mustAs(t, st, "jo", "rig", "--help"); !strings.Contains(out, "yardr rig [command]") {
		t.Errorf("rig --help = %q", out)
	}
}

// pack apply takes the flags a script wrote when a depot was a rig: --rig,
// which it requires as --depot, and --rig-roles, which --pack-roles
// excludes as it does --depot-roles.
func TestPackApplyTakesTheRigFlags(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	args := []string{"apply", "../../packs/starter", "--rig", "web", "--rig-roles", "--yes", "--crew", "yardmaster=boss",
		"--bind", "builders=claude:opus", "--bind", "reviewers=codex", "--bind", "crew:yardmaster=claude",
		"--members", "backlog=", "--members", "decisions=ann,boss"}
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("pack apply in the old words: %v\n%s", err, out)
	}
	recs, err := a.st.Packs(context.Background())
	if err != nil || len(recs) != 1 || recs[0].Depot != "web" || !recs[0].DepotRoles {
		t.Fatalf("the record = %+v, %v; want depot web with its own roles", recs, err)
	}
	if out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web", "--rig-roles", "--pack-roles"); err == nil || !strings.Contains(err.Error(), "depot-roles pack-roles") {
		t.Fatalf("--rig-roles with --pack-roles = %v\n%s", err, out)
	}
	if out, err := run(t, a.packCmd(), "check", "../../packs/starter"); err == nil || !strings.Contains(err.Error(), `"depot" not set`) {
		t.Fatalf("pack check with no depot = %v\n%s", err, out)
	}
}
