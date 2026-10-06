package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The crew were the residents until this release. What a script, a role
// file or a session from before the upgrade says in the old words still
// works, and is not listed: the command resident, --resident, the variable
// YARDR_RESIDENT, a kind that said resident in --on and --where, the label
// and the scope resident:<name>. yardr answers with the new words.
func TestTheResidentsWordsStillWork(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	if out := mustAs(t, st, "jo", "resident", "add", "clerk", "--runner", "manual", "--on", "resident_gave_up,held"); !strings.HasPrefix(out, "crew member clerk saved") {
		t.Fatalf("resident add = %q", out)
	}
	clerk, err := st.GetCrew(ctx, "clerk")
	if err != nil || !slices.Equal(clerk.On, []string{"crew_gave_up", "held"}) {
		t.Fatalf("clerk hears of %v, %v; want the kinds new events carry", clerk, err)
	}
	if out := mustAs(t, st, "jo", "resident", "list"); !strings.Contains(out, "clerk") {
		t.Fatalf("resident list = %q", out)
	}
	if out := mustAs(t, st, "jo", "resident", "set", "clerk", "--max-age", "6h"); !strings.HasPrefix(out, "crew member clerk saved") {
		t.Fatalf("resident set = %q", out)
	}
	mustAs(t, st, "jo", "resident", "journal", "clerk", "the old word still writes")
	var shown map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "resident", "show", "clerk", "--json")), &shown); err != nil || shown["crew"] == nil || shown["resident"] != nil {
		t.Fatalf("resident show --json has the fields %v, %v; want crew", shown, err)
	}

	// The flag, as a prompt typed before the upgrade names it.
	if out := mustAs(t, st, "clerk", "prime", "--resident", "clerk"); !strings.HasPrefix(out, "# yardr crew member: clerk\n") {
		t.Fatalf("prime --resident = %q", out)
	}
	mustAs(t, st, "jo", "mail", "clerk", "are you there")
	if out := mustAs(t, st, "clerk", "inbox", "--resident", "clerk"); !strings.Contains(out, "are you there") {
		t.Fatalf("inbox --resident = %q", out)
	}
	// The variable, as a session started before the upgrade has it.
	t.Setenv("YARDR_RESIDENT", "clerk")
	if out := mustAs(t, st, "clerk", "inbox"); !strings.Contains(out, "are you there") {
		t.Fatalf("inbox with YARDR_RESIDENT = %q", out)
	}
	mustAs(t, st, "clerk", "remember", "deploys are frozen")
	t.Setenv("YARDR_RESIDENT", "")
	for _, scope := range []string{"resident:clerk", "crew:clerk"} {
		if out := mustAs(t, st, "jo", "recall", "--scope", scope); !strings.Contains(out, "crew:clerk") || !strings.Contains(out, "deploys are frozen") {
			t.Fatalf("recall --scope %s = %q", scope, out)
		}
	}
	if out := mustAs(t, st, "jo", "bead", "list", "-t", "journal", "-l", "resident:clerk"); !strings.Contains(out, "clerk-journal") {
		t.Fatalf("bead list -l resident:clerk = %q", out)
	}

	mustAs(t, st, "jo", "subscribe", "clerk", "yardr", "--where", "kind=resident_started")
	if clerk, _ = st.GetCrew(ctx, "clerk"); len(clerk.Subscriptions) != 1 || clerk.Subscriptions[0].Where["kind"] != "crew_started" {
		t.Fatalf("subscriptions = %v", clerk.Subscriptions)
	}
	mustAs(t, st, "jo", "unsubscribe", "clerk", "yardr", "--where", "kind=resident_started")
	if clerk, _ = st.GetCrew(ctx, "clerk"); len(clerk.Subscriptions) != 0 {
		t.Fatalf("subscriptions after unsubscribe = %v", clerk.Subscriptions)
	}
	if err := st.PutRig(ctx, store.Rig{Name: "notes", Path: t.TempDir(), Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a task", Rig: "notes"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "bead", "watch", b.ID, "--resident", "clerk")
	if clerk, _ = st.GetCrew(ctx, "clerk"); len(clerk.Subscriptions) != 1 || clerk.Subscriptions[0].Where["bead"] != b.ID {
		t.Fatalf("subscriptions after bead watch --resident = %v", clerk.Subscriptions)
	}
	mustAs(t, st, "jo", "bead", "unwatch", b.ID, "--resident", "clerk")
	mustAs(t, st, "jo", "resident", "rm", "clerk")
	if _, err := st.GetCrew(ctx, "clerk"); err == nil {
		t.Fatal("resident rm left the crew member")
	}

	// Help has the new words only, and the old command has help of its own.
	for _, args := range [][]string{{"--help"}, {"crew", "--help"}, {"prime", "--help"}, {"inbox", "--help"}, {"reply", "--help"},
		{"thread", "open", "--help"}, {"thread", "state", "--help"}, {"bead", "watch", "--help"}, {"bead", "unwatch", "--help"},
		{"pack", "check", "--help"}, {"pack", "apply", "--help"}, {"remember", "--help"}} {
		out := mustAs(t, st, "jo", args...)
		if !strings.Contains(out, "crew") || strings.Contains(strings.ToLower(out), "resident") {
			t.Errorf("yardr %s names the residents, or not the crew:\n%s", strings.Join(args, " "), out)
		}
	}
	if out := mustAs(t, st, "jo", "resident", "--help"); !strings.Contains(out, "yardr resident [command]") {
		t.Errorf("resident --help = %q", out)
	}
}

// pack apply takes the flags a script wrote when the crew were the
// residents: --resident <pack's>=<yard's> and --bind resident:<name>=<kind>.
func TestPackApplyTakesTheResidentsFlags(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	args := []string{"apply", "../../packs/starter", "--rig", "web", "--yes", "--resident", "yardmaster=boss",
		"--bind", "builders=claude:opus", "--bind", "reviewers=codex", "--bind", "resident:yardmaster=claude",
		"--members", "backlog=", "--members", "decisions=ann,boss"}
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("pack apply in the old words: %v\n%s", err, out)
	}
	boss, err := a.st.GetCrew(context.Background(), "boss")
	if err != nil || boss.Config["kind"] != "claude" {
		t.Fatalf("the crew member the old flags named = %+v, %v", boss, err)
	}
	recs, err := a.st.Packs(context.Background())
	if err != nil || len(recs) != 1 || recs[0].Crew["yardmaster"] != "boss" {
		t.Fatalf("the record = %+v, %v", recs, err)
	}
}

// A start that failed before the upgrade is in the journal under the kind
// that release logged, and is still the reason doctor and init give.
func TestLastStartFailureReadsTheOldKind(t *testing.T) {
	st, owned := storetest.NewOwned(t)
	ctx := context.Background()
	r, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.LogEvent(ctx, r.Journal, "resident_start_failed", "yard", map[string]any{"resident": "clerk", "reason": "claude: not logged in"}); err != nil {
		t.Fatal(err)
	}
	if got := lastStartFailure(ctx, st, *r); got != "claude: not logged in" {
		t.Fatalf("the last failed start = %q", got)
	}
}

// No command that help lists says resident anywhere in its help: the old
// words are read, and never printed.
func TestHelpNamesNoResidents(t *testing.T) {
	root := (&app{st: storetest.New(t), actor: "me"}).rootCmd()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		text := c.Short + "\n" + c.Long + "\n" + c.Example + "\n" + c.UsageString()
		if i := strings.Index(strings.ToLower(text), "resident"); i >= 0 {
			t.Errorf("%s: its help says %q", c.CommandPath(), text[max(0, i-40):min(len(text), i+40)])
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	old, _, err := root.Find([]string{"resident", "add"})
	if err != nil || old.Name() != "add" || !old.Parent().Hidden {
		t.Fatalf("yardr resident add is %v, %v; want the hidden command", old, err)
	}
}
