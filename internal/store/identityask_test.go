package store

import (
	"slices"
	"strings"
	"testing"
)

// identityYard is a mapping yard with the identity-hearing source talk
// and the source chime, which hears nothing by identity.
func identityYard(t *testing.T) *askYard {
	t.Helper()
	y := mappingYard(t)
	const chime = `name: chime
base: https://chime.example.com
credential: { env: CHIME_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	if _, err := y.PutSource(y.ctx, "chime.yaml", chime, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

// ignoreTalk ignores the sender in talk through the mapping card's
// nobody pick, as only that pick adds an entry.
func ignoreTalk(t *testing.T, y *askYard, sender string) {
	t.Helper()
	if created, err := y.ClaimIdentity(y.ctx, "talk", sender, "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity for %s = %v, %v", sender, created, err)
	}
	var card *OpenAsk
	for _, o := range mappingQueue(t, y).Open {
		if o.Identity != nil && o.Identity.Sender == sender {
			c := o
			card = &c
		}
	}
	if card == nil {
		t.Fatalf("no card for %s", sender)
	}
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pickFor(t, y, card.Bead, MappingNobody), "", "mckean", false); err != nil {
		t.Fatal(err)
	}
}

// A typed --run "user identity ..." proposes the same write a terminal
// mapping makes: its card states the user, the source and what stood,
// and its approval writes the identity, approved by the user or the
// operator. --rm is refused, as on the crew row.
func TestRunAskUserIdentity(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	seq := y.ask(t, listAsk("mckean", `user identity ada --source talk --id U999`), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if !slices.Equal(run.Argv, []string{"user", "identity", "ada", "--source", "talk", "--id", "U999"}) {
		t.Fatalf("the ask stores %+v", run.Argv)
	}
	does := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"says the user ada is U999 in talk: a thread opened there for them is assigned to U999.",
		"talk hears by identity, so a delivery from U999 is then heard as theirs.",
		"They have no identity in talk now.",
	} {
		if !strings.Contains(does, want) {
			t.Fatalf("the card says:\n%s\nwant %q", does, want)
		}
	}
	if run.Description != "Who a user is in a source (no --source: list)" ||
		run.Back != "yardr user identity ada --source talk --rm" || run.BackBy != "ada or the operator" {
		t.Fatalf("the run ask = %+v", run)
	}
	for actor, want := range map[string]string{"yardmaster": "approving this command is mckean's, whom it was asked of", "ada": "approving this command is mckean's, whom it was asked of"} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, actor, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("an approval by %s = %v, want %q", actor, err, want)
		}
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user ada is U999 in talk" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || !u.IsIn("talk", "U999") {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "identity_set", 10)
	if len(evs) != 1 || evs[0].Data["user"] != "ada" || evs[0].Data["source"] != "talk" || evs[0].Data["id"] != "U999" {
		t.Fatalf("identity_set events = %+v", evs)
	}
	y.approved(t, evs[0], seq)

	// The user approves their own on their own card.
	seq = y.ask(t, listAsk("ada", "user identity ada --source talk --id U111 --key ada"), "yardmaster")
	done, err = y.ApproveAsk(y.ctx, y.bead, seq, "ada", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user ada is U111 / ada in talk" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || !u.IsIn("talk", "U111") || !u.IsIn("talk", "ADA") {
		t.Fatalf("ada = %+v, %v", u, err)
	}

	// --rm is typed, not proposed; a repeated flag and no id or key
	// are refused at asking, as on the crew row.
	for _, command := range []string{
		"user identity ada --source talk --rm",
		"user identity ada --source talk --id U999 --rm",
		"user identity ada --source talk --id U1 --id U2",
		"user identity ada --source talk --id U1 --source chime",
		"user identity ada --source talk",
		"user identity ada",
		"user identity --source talk --id U1",
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", command), "yardmaster"); err == nil {
			t.Fatalf("--run %q was proposed", command)
		}
	}
}

// A user identity card whose identity was set meanwhile is stale and
// runs nothing; an id another user holds meanwhile is refused in the
// write that would set it.
func TestRunAskUserIdentityStaleAndClash(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	seq := y.ask(t, listAsk("mckean", "user identity ada --source talk --id U999"), "yardmaster")
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U111"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err == nil || !strings.Contains(err.Error(), "held another identity in talk: it was set since") {
		t.Fatalf("the approval after a set = %v", err)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || !u.IsIn("talk", "U111") {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	// Another user holding the id meanwhile is refused where it would
	// be written, not as stale: ada's own identity is as the card saw.
	seq = y.ask(t, listAsk("mckean", "user identity ada --source talk --id U222"), "yardmaster")
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetIdentity(y.ctx, "bo", "talk", Identity{ID: "U222"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("the approval over a clash = %v", err)
	}
}

// Mapping an ignored sender on a user identity card clears their entry
// in the same write, and the card says so.
func TestRunAskUserIdentityClearsIgnored(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	ignoreTalk(t, y, "U999")
	seq := y.ask(t, listAsk("mckean", "user identity ada --source talk --id U999"), "yardmaster")
	does := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	if want := "U999 is ignored in talk now, and the mapping clears that entry in the same write."; !strings.Contains(does, want) {
		t.Fatalf("the card says:\n%s\nwant %q", does, want)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user ada is U999 in talk\nU999 is no longer ignored in talk" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after the approval = %+v, %v", src, err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "sender_unignored", 10)
	if len(evs) != 1 || evs[0].Data["source"] != "talk" || evs[0].Data["sender"] != "U999" {
		t.Fatalf("sender_unignored events = %+v", evs)
	}
	y.approved(t, evs[0], seq)
}

// A typed --run "source ignore <source> --rm <id>" proposes the
// un-ignore on a card: its approval removes the entry, and the sender's
// next message follows the hook's word again. Listing names nothing to
// remove, and a source with no identity hearing ignores nothing.
func TestRunAskSourceIgnore(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	ignoreTalk(t, y, "U999")
	ignoreTalk(t, y, "U888")
	seq := y.ask(t, listAsk("mckean", "source ignore talk --rm U999"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if !slices.Equal(run.Argv, []string{"source", "ignore", "talk", "--rm", "U999"}) {
		t.Fatalf("the ask stores %+v", run.Argv)
	}
	does := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"removes U999 from the senders ignored in talk, so their next message follows the hook's word again: under unknown a fresh mapping card is offered, under allow it is heard as an outsider.",
		"U888 stays ignored in talk.",
	} {
		if !strings.Contains(does, want) {
			t.Fatalf("the card says:\n%s\nwant %q", does, want)
		}
	}
	if run.Description != "List a source's ignored senders, or stop ignoring one" || run.BackBy != "nobody" {
		t.Fatalf("the run ask = %+v", run)
	}
	for _, actor := range []string{"yardmaster", "ada"} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, actor, false); err == nil || !strings.Contains(err.Error(), "approving a command is the operator's (mckean)") {
			t.Fatalf("an approval by %s = %v", actor, err)
		}
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "source talk: U999 is no longer ignored" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || !slices.Equal(src.Ignored, []string{"U888"}) {
		t.Fatalf("ignored after the approval = %+v, %v", src, err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "sender_unignored", 10)
	if len(evs) != 1 || evs[0].Data["source"] != "talk" || evs[0].Data["sender"] != "U999" {
		t.Fatalf("sender_unignored events = %+v", evs)
	}
	y.approved(t, evs[0], seq)

	// Listing proposes nothing; a repeated --rm, an id the list does
	// not name, and a source with no identity hearing are refused.
	for command, want := range map[string]string{
		"source ignore talk":                 "names nothing to remove",
		"source ignore talk --rm U1 --rm U2": "--rm once",
		"source ignore talk --rm U777":       "would change nothing",
		"source ignore chime --rm U999":      "no identity hearing",
		"source ignore nowhere --rm U999":    "not found",
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", command), "yardmaster"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--run %q = %v, want %q", command, err, want)
		}
	}
}

// A source ignore card whose entry is gone meanwhile approves nothing;
// one whose list otherwise changed is stale by its sentences.
func TestRunAskSourceIgnoreStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	ignoreTalk(t, y, "U999")
	ignoreTalk(t, y, "U888")
	seq := y.ask(t, listAsk("mckean", "source ignore talk --rm U999"), "yardmaster")
	if err := y.UnignoreSource(y.ctx, "talk", "U999", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err == nil || !strings.Contains(err.Error(), "nothing is left to change") {
		t.Fatalf("the approval after a removal = %v", err)
	}
	seq = y.ask(t, listAsk("mckean", "source ignore talk --rm U888"), "yardmaster")
	ignoreTalk(t, y, "U777")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err == nil || !strings.Contains(err.Error(), "the card is from before that changed") {
		t.Fatalf("the approval after another ignore = %v", err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || !slices.Equal(src.Ignored, []string{"U777", "U888"}) {
		t.Fatalf("ignored after refused approvals = %+v, %v", src, err)
	}
}

// The source ignore row refuses its asker in the words the store
// function refuses with: the operator's alone, and a run ask may
// propose it.
func TestRunAskSourceIgnoreUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := identityYard(t)
	ignoreTalk(t, y, "U999")
	c, argv, err := parseRow([]string{"source ignore talk --rm U999"})
	if err != nil {
		t.Fatal(err)
	}
	var rowErr, directErr error
	if err := y.write(func(w *wtx) error {
		rowErr = c.check(y.ctx, w, argv, "yardmaster")
		directErr = w.unignoreSender(y.ctx, "talk", "U999", "yardmaster", nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if directErr == nil || rowErr == nil || rowErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct source ignore = %v", rowErr, directErr)
	}
	if class, ok := IsOperatorOnly(rowErr); !ok || class != Askable {
		t.Fatalf("source ignore refusal = %v; operator-only class is %v, %v", rowErr, class, ok)
	}
	if err := y.write(func(w *wtx) error { return c.check(y.ctx, w, argv, "mckean") }); err != nil {
		t.Fatalf("operator's source ignore check = %v, want allowed", err)
	}
}
