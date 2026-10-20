package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/yardkey"
)

// peerRuleYard is runYard with a second link, north, and the identity-hearing
// source talk: what the rows about one peer's link are tried against.
func peerRuleYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddPeer(y.ctx, Peer{Name: "north", Key: yardkey.Public(k), Calls: "the-north"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "talk.yaml", talkFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

// intakeYard is peerRuleYard with the depot web, the flow incoming of the
// store, the exec group builders and a route that sends beads from airy to
// builders in the flow's first stage: what an intake says who works.
func intakeYard(t *testing.T) *askYard {
	t.Helper()
	y := peerRuleYard(t)
	if err := y.PutDepot(y.ctx, Depot{Name: "web", Base: "origin/main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFlow(arrivalFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "incoming"
	if _, err := y.PutFlow(y.ctx, *f, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "make build"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	label := "from-airy"
	if _, err := y.AddRoute(y.ctx, Route{Stage: "arrived", Label: &label, Group: "builders"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

// peerOf is the link called name as the yard holds it.
func (y *askYard) peerOf(t *testing.T, name string) *Peer {
	t.Helper()
	p, err := y.GetPeer(y.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// refusedOpen approves the ask seq as the context says and holds the refusal
// to want, and the ask to be open still with no answer on the bead.
func (y *askYard) refusedOpen(t *testing.T, ctx context.Context, seq uint64, want string) {
	t.Helper()
	if _, err := y.ApproveAsk(ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ApproveAsk = %v, want %q", err, want)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("the refused approval left %d answers", len(answers))
	}
}

// A crew member proposes what a link carries of any kind: the card says what
// the link carries each way after the command and before it, and which kinds
// the command adds and stops, and the operator's approval makes the change
// under the command's own check, marked with the ask it was approved on.
func TestRunAskPeerAllowAnyKind(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := peerRuleYard(t)
	p := y.peerOf(t, "airy")
	key := p.Key
	as := "the link with airy (it calls itself yardr, key " + key[:8] + "…)"

	// Every kind, each way, is a card: none is the operator's alone.
	for _, kind := range PeerKinds {
		for _, flag := range []string{"--send", "--receive"} {
			y.ask(t, runAsk("peer allow airy "+flag+" "+kind), "yardmaster")
		}
	}

	seq := y.ask(t, runAsk("peer allow airy --receive ask,bead --send bead"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:        []string{"peer", "allow", "airy", "--send", "bead", "--receive", "ask", "--receive", "bead"},
		Description: "Set which messages a peer link carries and its mail limit",
		Does: []string{
			as + " will carry bead to it and take ask, bead from it. It carries nothing to it and takes nothing from it now.",
			"That adds bead to what it carries to it and adds ask, bead to what it takes from it.",
		},
		Back: "yardr peer allow airy --no-send bead --no-receive ask --no-receive bead",
		Seen: map[string]string{"peer airy": key, "peer airy carries": seen(p.Send, p.Receive)},
	}
	if !slices.Equal(run.Argv, want.Argv) || run.Description != want.Description || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != "" || !sameMap(run.Seen, want.Seen) {
		t.Fatalf("the ask stores\n%+v\nwant\n%+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"  the yard says: " + want.Does[0],
		"  the yard says: " + want.Does[1],
		"  anyone can take it back: yardr peer allow airy --no-send bead --no-receive ask --no-receive bead",
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if got := "peer airy: send bead; receive ask, bead; expose nobody"; done.Result != got {
		t.Fatalf("the approval printed %q, want %q", done.Result, got)
	}
	if p := y.peerOf(t, "airy"); !slices.Equal(p.Send, []string{PeerBead}) || !slices.Equal(p.Receive, []string{PeerAsk, PeerBead}) {
		t.Fatalf("the link carries %v out and %v in, want bead out and ask, bead in", p.Send, p.Receive)
	}
	evs, err := y.EventsOfKind(y.ctx, "peer_allowed", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("peer_allowed events = %d, %v", len(evs), err)
	}
	y.approved(t, evs[0], seq)

	// What the link carries now stands on the next card, with the kinds
	// the command stops: taking one back is the operator's alone to undo.
	seq = y.ask(t, runAsk("peer allow airy --send mail --no-send bead"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if wantDoes := []string{
		as + " will carry mail to it and take ask, bead from it. It carries bead to it and takes ask, bead from it now.",
		"That adds mail to what it carries to it and stops carrying bead to it.",
	}; !slices.Equal(run.Does, wantDoes) || run.Back != "yardr peer allow airy --no-send mail --send bead" || run.BackBy != "only the operator" {
		t.Fatalf("the mixed ask stores %q, %q by %q", run.Does, run.Back, run.BackBy)
	}

	// Stopping alone needs no approval, and the rest is no card either.
	for command, want := range map[string]string{
		"peer allow airy --no-send bead":                "needs no approval: run it yourself",
		"peer allow airy --send bead":                   "the link carries bead to it and takes ask, bead from it already, so the command would change nothing",
		"peer allow airy --send thread --expose ada":    "it is the operator's alone, typed in a terminal and never proposed as a run ask",
		"peer allow airy --send thread --trust ada=ada": "it is the operator's alone, typed in a terminal and never proposed as a run ask",
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, runAsk(command), "yardmaster"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--run %q = %v, want %q", command, err, want)
		}
	}
}

// When a kind is both added and stopped, the write lets the stop win. The
// card and its no-op check use that resulting state too.
func TestRunAskPeerAllowCardsTheResultOfConflictingKinds(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := peerRuleYard(t)
	if _, err := y.AddAsk(y.ctx, y.bead, runAsk("peer allow airy --send bead --no-send bead"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "the link carries nothing to it and takes nothing from it already") {
		t.Fatalf("adding and stopping an absent kind = %v", err)
	}
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Send: []string{PeerBead}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, runAsk("peer allow airy --send bead --no-send bead"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := []string{
		"the link with airy (it calls itself yardr, key " + y.peerOf(t, "airy").Key[:8] + "…) will carry nothing to it and take nothing from it. It carries bead to it and takes nothing from it now.",
		"That stops carrying bead to it.",
	}
	if !slices.Equal(run.Does, want) || run.Back != "yardr peer allow airy --send bead" || run.BackBy != "only the operator" {
		t.Fatalf("the ask stores %q, %q by %q", run.Does, run.Back, run.BackBy)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err != nil {
		t.Fatal(err)
	}
	if p := y.peerOf(t, "airy"); len(p.Send) != 0 {
		t.Fatalf("the link carries %v after stop wins", p.Send)
	}
}

// An approval that arrived through the reach of the peer a command is about
// is refused, with words that say why, and leaves the ask open and the yard
// as it was: a peer does not widen its own link, say where its own beads go
// or say who its own crew is on its own word. The same card approved from
// this yard, in a terminal or from a device paired with it, runs.
func TestRunAskPeerCardsRefuseAnApprovalThroughThePeersOwnReach(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := intakeYard(t)
	own := WithReach(y.ctx, "airy")
	const how = "Approve it from this yard, in a terminal here or in footplate paired with this yard directly. Nothing ran, and the ask stays open"

	allow := y.ask(t, runAsk("peer allow airy --receive bead"), "yardmaster")
	y.refusedOpen(t, own, allow, "ask 1: this approval arrived through the reach of airy (a name of airy taken as mckean), and a peer must not widen its own link. "+how)
	limit := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	y.refusedOpen(t, own, limit, "and a peer must not widen its own link")
	intake := y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	y.refusedOpen(t, own, intake, "and a peer must not say where its own beads go. "+how)
	identity := y.ask(t, runAsk("peer identity airy mayor --source talk --id U1"), "yardmaster")
	y.refusedOpen(t, own, identity, "and a peer must not say who its own crew is here. "+how)

	if p := y.peerOf(t, "airy"); len(p.Receive) != 0 || p.MailsAnHour() != 30 || len(p.Intakes) != 0 || len(p.Identities) != 0 {
		t.Fatalf("the refused approvals changed the link: %+v", p)
	}
	for _, kind := range []string{"peer_allowed", "peer_intake", "peer_identity_set"} {
		if evs, err := y.EventsOfKind(y.ctx, kind, 10); err != nil || len(evs) != 0 {
			t.Fatalf("the refused approvals logged %d %s events, %v", len(evs), kind, err)
		}
	}

	// A card about another link is no card about this peer's own: the
	// peer is the one the command names, and the path is the context's,
	// whatever the arguments say.
	north := y.ask(t, runAsk("peer allow north --receive bead"), "yardmaster")
	if _, err := y.ApproveAsk(own, y.bead, north, "mckean"); err != nil {
		t.Fatalf("an approval through airy's reach of a card about north = %v", err)
	}

	// From this yard the same cards run: from a device of the operator on
	// the listener, and in a terminal (no mark on the call). The link takes
	// beads only once the last of them is approved, and the intake's card,
	// written before, holds: that the link takes them is no part of what it
	// changes.
	phone := WithVia(y.ctx, "mckean", "phone")
	if _, err := y.ApproveAsk(phone, y.bead, intake, "mckean"); err != nil {
		t.Fatalf("the approval from a paired device = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, identity, "mckean"); err != nil {
		t.Fatalf("the approval of the identity in a terminal = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, allow, "mckean"); err != nil {
		t.Fatalf("the approval in a terminal = %v", err)
	}
	if p := y.peerOf(t, "airy"); !slices.Equal(p.Receive, []string{PeerBead}) || len(p.Intakes) != 1 || len(p.Identities) != 1 {
		t.Fatalf("the approvals from this yard left the link as %+v", p)
	}
	evs, err := y.EventsOfKind(y.ctx, "peer_intake", 10)
	if err != nil || len(evs) != 1 || Via(evs[0]) != "phone" {
		t.Fatalf("the intake was logged as %+v, %v, want it through the phone", evs, err)
	}
	y.approved(t, evs[0], intake)
}

// The two cards of an intake and the refusals of what is no card: the card
// names the depot, the flow and who works the first stage, then what the link
// has now; the approval sets it under the command's own check; taking none is
// anyone's, and so no card; and what the command itself refuses is refused
// to the asker.
func TestRunAskPeerIntake(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := intakeYard(t)
	key := y.peerOf(t, "airy").Key
	as := "the link with airy (it calls itself yardr, key " + key[:8] + "…)"

	seq := y.ask(t, runAsk("peer intake --limit=3 airy --flow incoming --depot web"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:        []string{"peer", "intake", "airy", "--depot", "web", "--flow", "incoming", "--limit", "3"},
		Description: "Say where the beads a peer sends go: the depot and the flow",
		Does: []string{
			as + " takes its beads for the depot web as beads of this yard there, pinned to the flow incoming and labelled from-airy: at most 3 of them are open at once.",
			"Its beads arrive in the stage arrived, which the script of the group builders works: it runs on each unseen.",
			"A bead of it that names no remote goes to this intake.",
			"It has no intake for the depot web now.",
			"The link takes no beads from it yet, so each is refused until yardr peer allow airy --receive bead.",
		},
		Back: "yardr peer intake airy --depot web --none",
		Seen: map[string]string{"peer airy": key, "peer airy intakes": seen([]PeerIntake(nil))},
	}
	if !slices.Equal(run.Argv, want.Argv) || run.Description != want.Description || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != "" || !sameMap(run.Seen, want.Seen) {
		t.Fatalf("the ask stores\n%+v\nwant\n%+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"      yardr peer intake airy --depot web --flow incoming --limit 3",
		"  what the command does: Say where the beads a peer sends go: the depot and the flow",
		"  anyone can take it back: yardr peer intake airy --depot web --none",
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}

	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	const line = "peer airy: depot web, flow incoming (default): its beads arrive in the stage arrived, which the script of the group builders works: it runs on each unseen; at most 3 open, 0 now. The link does not take its beads yet: yardr peer allow airy --receive bead"
	if done.Result != line {
		t.Fatalf("the approval printed\n%s\nwant\n%s", done.Result, line)
	}
	if p := y.peerOf(t, "airy"); !slices.Equal(p.Intakes, []PeerIntake{{Depot: "web", Flow: "incoming", Limit: 3, Default: true}}) {
		t.Fatalf("the intakes after the approval = %+v", p.Intakes)
	}
	evs, err := y.EventsOfKind(y.ctx, "peer_intake", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("peer_intake events = %d, %v", len(evs), err)
	}
	y.approved(t, evs[0], seq)

	// What stands is on the next card, and it is taken back by the one
	// who set it: replacing an intake is the operator's to undo.
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Receive: []string{PeerBead}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, runAsk("peer intake airy --depot web --flow incoming --limit 5"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if !slices.Contains(run.Does, "It has the flow incoming for the depot web now, at most 3 open and takes the beads that name no remote.") ||
		slices.ContainsFunc(run.Does, func(s string) bool { return strings.HasPrefix(s, "The link takes no beads") }) ||
		run.Back != "yardr peer intake airy --depot web --flow incoming --limit 3 --default" || run.BackBy != "only the operator" {
		t.Fatalf("the replacing ask stores %q, %q by %q", run.Does, run.Back, run.BackBy)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "no"}, "mckean")

	// Marking another depot's intake the default moves the mark, and the
	// card says which intake loses it and how to give it back.
	if err := y.PutDepot(y.ctx, Depot{Name: "api", Base: "origin/main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, runAsk("peer intake airy --depot api --flow incoming --default"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if !slices.Contains(run.Does, "A bead of it that names no remote goes to this intake.") ||
		!slices.Contains(run.Does, "The intake for the depot web takes them now, and does not after this; taking this one back leaves them none to go to until yardr peer intake airy --depot web --flow incoming --limit 3 --default.") ||
		run.Back != "yardr peer intake airy --depot api --none" || run.BackBy != "" {
		t.Fatalf("the ask that moves the default stores %q, %q by %q", run.Does, run.Back, run.BackBy)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "no"}, "mckean")

	for _, c := range []struct{ command, want string }{
		{"peer intake airy --depot web --flow incoming --limit 3", "its intake for the depot web is the flow incoming already, at most 3 open, so the command would change nothing"},
		{"peer intake airy --depot web --none", "needs no approval: run it yourself"},
		{"peer intake airy --none", "needs no approval: run it yourself"},
		{"peer intake airy --depot web --none --flow incoming", "--none removes intakes: it goes with --depot alone"},
		{"peer intake airy --depot web", "say where its beads go: --depot <depot> --flow <flow>"},
		{"peer intake airy --flow incoming", "say where its beads go: --depot <depot> --flow <flow>"},
		{"peer intake airy --depot web --flow incoming --limit 0", "--limit is a number of open beads, at least 1"},
		{"peer intake airy --depot web --flow incoming --limit many", "--limit is a number of open beads, at least 1"},
		{"peer intake airy --depot web --flow incoming --depot api", "--depot once, not 2 times"},
		{"peer intake airy --depot nowhere --flow incoming", "not found"},
		{"peer intake airy --depot web --flow nowhere", "flow nowhere: not found"},
		{"peer intake nobody --depot web --flow incoming", "peer nobody: not found"},
		{"peer intake --depot web --flow incoming", "it names no peer"},
		{"peer intake airy north --depot web --flow incoming", "one peer, not airy and north"},
		{"peer intake airy --depot web --flow incoming --actor mckean", "peer intake with --actor is the operator's to type in a terminal"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, runAsk(c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q = %v, want %q", c.command, err, c.want)
		}
	}

	// Nobody but the operator's own approval runs it, and nobody but a
	// crew member or a user asks: the checks of every row.
	seq = y.ask(t, runAsk("peer intake airy --depot web --flow incoming --default"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "yardmaster"); err == nil || !strings.Contains(err.Error(), "approving a command is the operator's (mckean)") {
		t.Fatalf("the asker's own approval = %v", err)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "no"}, "mckean")
}

// An intake whose flow is a file of the depot's repository is read before
// the write that stores the ask and the one that approves it, since git does
// not run inside one: a directory depot's flows are read from its directory,
// and the card says who works the first stage of what it found there.
func TestRunAskPeerIntakeOfARepositoryFlow(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := intakeYard(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".yardr", "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "flows", "review.flow"), []byte(arrivalFlow), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := y.PutDepot(y.ctx, Depot{Name: "notes", Path: dir, Kind: DepotDir}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	notes, label := "notes", "from-airy"
	if _, err := y.AddRoute(y.ctx, Route{Stage: "arrived", Depot: &notes, Label: &label, Group: "reviewers"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, runAsk("peer intake airy --depot notes --flow review"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if len(run.Does) < 2 || !strings.HasPrefix(run.Does[1], "Its beads arrive in the stage arrived, which agents of the group reviewers work: WORK STARTS ON EACH UNSEEN, on the other yard's text") {
		t.Fatalf("the card of an intake of a repository flow says %q", run.Does)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(done.Result, "peer airy: depot notes, flow review (default): its beads arrive in the stage arrived, which agents of the group reviewers work") {
		t.Fatalf("the approval printed %q", done.Result)
	}

	// A flow the repository has not is refused to the asker, in the
	// command's own words.
	if _, err := y.AddAsk(y.ctx, y.bead, runAsk("peer intake north --depot notes --flow nowhere"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "flow nowhere: not found") {
		t.Fatalf("an intake of a flow the repository has not = %v", err)
	}
}

// An approval of an intake is held to the yard the card was written from:
// the link's intakes changed, the first stage's workers changed, or the
// depot or the flow gone, and nothing runs.
func TestRunAskPeerIntakeStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := intakeYard(t)
	seq := y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	if _, err := y.SetPeerIntake(y.ctx, "airy", &PeerIntake{Depot: "web", Flow: "incoming", Limit: 7}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written when the link with airy held other intakes: they were changed since (peer_intake")
	if p := y.peerOf(t, "airy"); p.Intakes[0].Limit != 7 {
		t.Fatalf("the refused approval changed the intake: %+v", p.Intakes)
	}

	// Who works the first stage changed: the sentence the card says is
	// not the one the yard says now.
	y = intakeYard(t)
	seq = y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	rs, err := y.Routes(y.ctx)
	if err != nil || len(rs) != 1 {
		t.Fatalf("routes = %+v, %v", rs, err)
	}
	if err := y.DeleteRoute(y.ctx, rs[0].ID, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "the card is from before that changed")

	// The link is another one under its name.
	y = intakeYard(t)
	seq = y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	if err := y.DeletePeer(y.ctx, "airy", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "peer airy: not found") {
		t.Fatalf("an approval of an intake of a link that is gone = %v", err)
	}
}

// That the link takes the peer's beads is no part of what an intake changes:
// the operator approves the link's --receive bead and the intake in either
// order, and the intake's card, written while the link took none, still runs,
// its answer carrying the card as it stands.
func TestRunAskPeerIntakeHoldsWhenTheLinkTakesBeadsFirst(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := intakeYard(t)
	intake := y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	allow := y.ask(t, runAsk("peer allow airy --receive bead"), "yardmaster")
	if !slices.ContainsFunc(y.asked(t, intake).Ask.Run.Does, func(s string) bool { return strings.HasPrefix(s, noBeadsYet) }) {
		t.Fatalf("the card written first does not say the link takes no beads: %q", y.asked(t, intake).Ask.Run.Does)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, allow, "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, intake, "mckean")
	if err != nil {
		t.Fatalf("the intake approved after the link took beads = %v", err)
	}
	if slices.ContainsFunc(done.Does, func(s string) bool { return strings.HasPrefix(s, noBeadsYet) }) {
		t.Fatalf("the answer carries a card that says the link takes no beads: %q", done.Does)
	}
	if p := y.peerOf(t, "airy"); len(p.Intakes) != 1 || !slices.Equal(p.Receive, []string{PeerBead}) {
		t.Fatalf("the link after both = %+v", p)
	}

	// What the intake changes is held: another flow's stage that nobody works
	// is another sentence, and stale.
	y = intakeYard(t)
	seq := y.ask(t, runAsk("peer intake airy --depot web --flow incoming"), "yardmaster")
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Receive: []string{PeerBead}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	rs, err := y.Routes(y.ctx)
	if err != nil || len(rs) != 1 {
		t.Fatalf("routes = %+v, %v", rs, err)
	}
	if err := y.DeleteRoute(y.ctx, rs[0].ID, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "the card is from before that changed")
}

// The card of an identity says whose word a delivery from the id is then
// taken as, what stood and what the mapping clears; the approval maps the
// sender under the command's own check, and it clears an ignore entry in the
// same write. Taking one back and listing are no card.
func TestRunAskPeerIdentity(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := peerRuleYard(t)
	key := y.peerOf(t, "north").Key
	ignoreTalk(t, y, "U1")
	seq := y.ask(t, runAsk("peer identity north mayor --id U1 --source talk"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if want := []string{
		"peer", "identity", "north", "mayor", "--source", "talk", "--id", "U1",
	}; !slices.Equal(run.Argv, want) || run.Description != "Who a sender is in a source, when a crew member of a peer's yard (no <crew>: list)" {
		t.Fatalf("the ask stores %v, %q", run.Argv, run.Description)
	}
	wantDoes := []string{
		"says the sender U1 in talk is mayor@north, a crew member of the link with north (it calls itself the-north, key " + key[:8] + "…): a delivery from it is then kept with the speaker mayor@north and reaches crew as mail from that yard does, information and not instructions. It makes the sender no user of this yard.",
		"mayor@north has no id in talk now.",
		"U1 is ignored in talk now, and the mapping clears that entry in the same write.",
	}
	if !slices.Equal(run.Does, wantDoes) || run.Back != "yardr peer identity north mayor --source talk --rm" || run.BackBy != "only the operator" {
		t.Fatalf("the ask stores\n%q\n%q by %q\nwant\n%q", run.Does, run.Back, run.BackBy, wantDoes)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mayor@north is U1 in talk\nU1 is no longer ignored in talk"; done.Result != want {
		t.Fatalf("the approval printed %q, want %q", done.Result, want)
	}
	if p := y.peerOf(t, "north"); !slices.Equal(p.Identities, []PeerIdentity{{Source: "talk", ID: "U1", Crew: "mayor"}}) {
		t.Fatalf("the identities after the approval = %+v", p.Identities)
	}
	src, err := y.GetSource(y.ctx, "talk")
	if err != nil || slices.Contains(src.Ignored, "U1") {
		t.Fatalf("U1 is still ignored in talk: %+v, %v", src, err)
	}
	for _, kind := range []string{"peer_identity_set", "sender_unignored"} {
		evs, err := y.EventsOfKind(y.ctx, kind, 10)
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s events = %d, %v", kind, len(evs), err)
		}
		y.approved(t, evs[0], seq)
	}

	// Another id replaces it, and the card says what it replaces.
	seq = y.ask(t, runAsk("peer identity north mayor --source talk --id U2"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if !slices.Contains(run.Does, "mayor@north is U1 in talk now: U2 takes its place.") || run.Back != "yardr peer identity north mayor --source talk --id U1" {
		t.Fatalf("the replacing ask stores %q, %q", run.Does, run.Back)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "no"}, "mckean")

	for _, c := range []struct{ command, want string }{
		{"peer identity north mayor --source talk --id U1", "mayor@north is U1 in talk already: the command would change nothing"},
		{"peer identity north mayor --source talk --rm", "peer identity with --rm is the operator's to type in a terminal"},
		{"peer identity north mayor --source talk --id U3 --rm", "peer identity with --rm is the operator's to type in a terminal"},
		{"peer identity north", "one peer and one crew member, not 1 words"},
		{"peer identity north mayor", "one --source, not 0"},
		{"peer identity north mayor --source talk", "one --id, not 0"},
		{"peer identity north mayor --source talk --id U3 --id U4", "one --id, not 2"},
		{"peer identity north mayor --source linear --id U3", "linear is no source this yard declares"},
		{"peer identity north may@or --source talk --id U3", `crew member "may@or" of a peer: use letters, digits`},
		{"peer identity nobody mayor --source talk --id U3", "peer nobody: not found"},
		{"peer identity north mayor --source talk --id U3 --key boss", "peer identity with --key is the operator's to type in a terminal"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, runAsk(c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q = %v, want %q", c.command, err, c.want)
		}
	}

	// An id that names another speaker is refused to the asker, as the
	// command refuses it: one id names one speaker of the yard.
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U9"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, runAsk("peer identity north mayor --source talk --id U9"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "U9 in talk names ada already") {
		t.Fatalf("an id a user has = %v", err)
	}
}

// Approving a peer identity run ask ends a mapping card for its sender in
// the same write, as setting that identity from a terminal does.
func TestRunAskPeerIdentityEndsMappingCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := peerRuleYard(t)
	card := claimCard(t, y, "U1")
	seq := y.ask(t, runAsk("peer identity north mayor --source talk --id U1"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, card.Bead, "U1 is mayor@north in talk")
	if d := wantDecided(t, y, card.Bead, "taken back"); d.Words != "U1 is mayor@north in talk" {
		t.Fatalf("the mapping card's ask ended as %+v, want it taken back with the mapping as the reason", d)
	}
	wantCardOpen(t, y, y.bead)
}

// An approval of an identity is held to the yard the card was written from:
// the id the crew member has, the source and the link.
func TestRunAskPeerIdentityStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := peerRuleYard(t)
	seq := y.ask(t, runAsk("peer identity north mayor --source talk --id U1"), "yardmaster")
	if _, err := y.SetPeerIdentity(y.ctx, "north", PeerIdentity{Source: "talk", ID: "U2", Crew: "mayor"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written when mayor@north held another id in talk: it was set since (peer_identity_set")

	y = peerRuleYard(t)
	if _, err := y.SetPeerIdentity(y.ctx, "north", PeerIdentity{Source: "talk", ID: "U2", Crew: "mayor"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, runAsk("peer identity north mayor --source talk --id U1"), "yardmaster")
	if _, err := y.RemovePeerIdentity(y.ctx, "north", "mayor", "talk", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written when mayor@north held another id in talk: it was removed since (peer_identity_removed")

	// The id is taken by a user meanwhile: the command refuses it at the
	// approval as it does a terminal.
	y = peerRuleYard(t)
	seq = y.ask(t, runAsk("peer identity north mayor --source talk --id U1"), "yardmaster")
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U1"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "U1 in talk names ada already") || !strings.Contains(err.Error(), "Nothing ran") {
		t.Fatalf("an approval of an id a user has since = %v", err)
	}
	if p := y.peerOf(t, "north"); len(p.Identities) != 0 {
		t.Fatalf("the refused approval mapped %+v", p.Identities)
	}
}
