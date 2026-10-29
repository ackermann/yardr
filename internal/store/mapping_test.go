package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// talkFile is a source that hears by identity: fields.sender holds a
// member id, matched against a user's identity in talk.
const talkFile = `name: talk
base: https://talk.example.com
credential: { env: TALK_TOKEN }
calls:
  message:
    kind: update
    params: { text: { type: string } }
    request: { method: POST, path: /send, body: { text: "{text}" } }
hook:
  verify: { scheme: secret, signature: { header: Talk-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`

// mappingYard is an ask yard whose operator is mckean, with ada as a
// second user and the identity-hearing source talk declared.
func mappingYard(t *testing.T) *askYard {
	t.Helper()
	y := answerYard(t)
	if err := y.SetSetting(y.ctx, OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "talk.yaml", talkFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func mappingQueue(t *testing.T, y *askYard) *AskQueue {
	t.Helper()
	q, err := y.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func pickFor(t *testing.T, y *askYard, bead string, label string) int {
	t.Helper()
	view, err := y.Bead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Asks) != 1 {
		t.Fatalf("the card bead holds %d asks", len(view.Asks))
	}
	for i, o := range view.Asks[0].Options {
		if o.Label == label {
			return i + 1
		}
	}
	t.Fatalf("the card offers %v, want %s among them", view.Asks[0].Options, label)
	return 0
}

// claimCard offers the mapping card for the sender in talk and returns it
// as the operator's queue holds it.
func claimCard(t *testing.T, y *askYard, sender string) OpenAsk {
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
	return *card
}

// wantCardClosed checks that the card's bead is closed with reason.
func wantCardClosed(t *testing.T, y *askYard, bead, reason string) {
	t.Helper()
	b, err := y.GetBead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "closed" || b.CloseReason == nil || *b.CloseReason != reason {
		got := "no reason"
		if b.CloseReason != nil {
			got = *b.CloseReason
		}
		t.Fatalf("card bead %s is %s with %q, want it closed with %q", bead, b.Status, got, reason)
	}
}

// wantCardOpen checks that the card's bead is still open.
func wantCardOpen(t *testing.T, y *askYard, bead string) {
	t.Helper()
	b, err := y.GetBead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "open" {
		t.Fatalf("card bead %s is %s, want it open", bead, b.Status)
	}
}

// cardHeldCount is the waiting count shown on a mapping card's ask.
func cardHeldCount(t *testing.T, y *askYard, bead string) int {
	t.Helper()
	view, err := y.Bead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Asks) != 1 || view.Asks[0].Identity == nil {
		t.Fatalf("card %s asks = %+v, want one mapping ask", bead, view.Asks)
	}
	return view.Asks[0].Identity.Held
}

// wantDecided checks that the operator's decided list holds the card's ask
// in state, and answers the row.
func wantDecided(t *testing.T, y *askYard, bead, state string) DecidedAsk {
	t.Helper()
	q, err := y.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range q.Decided {
		if d.Bead == bead {
			if d.State != state {
				t.Fatalf("card %s is %s among the decided, want %s", bead, d.State, state)
			}
			return d
		}
	}
	t.Fatalf("card %s is not among the decided: %+v", bead, q.Decided)
	return DecidedAsk{}
}

// mapBehindTheCards writes the user's identity in the source as the store
// held it before a mapping ended the cards for its sender: through no
// setter, so the card stays open for a sender who is mapped, as the
// stale pick (mappingStale) is for.
func mapBehindTheCards(t *testing.T, y *askYard, user, source string, id Identity) {
	t.Helper()
	err := y.write(func(w *wtx) error {
		u, err := getUser(w.Tx, user)
		if err != nil {
			return err
		}
		u.Identities = map[string]Identity{source: id}
		return put(w.Tx, bUsers, user, u)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A held delivery's sender stands one mapping card for the operator,
// showing the member id and channel without message text that agents
// could read before the answer. Picking a user writes their identity,
// closes the card's bead with what was written, and the sender offers
// no second card, by then or once mapped.
func TestClaimIdentityPickWritesIdentity(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	long := strings.Repeat("word ", 30) + "TAIL-MARKER-must-not-be-kept"
	created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", long)
	if err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	q := mappingQueue(t, y)
	if len(q.Open) != 1 {
		t.Fatalf("open asks = %d, want 1", len(q.Open))
	}
	open := q.Open[0]
	if open.Identity == nil || open.Identity.Source != "talk" || open.Identity.Sender != "U999" || open.Identity.Channel != "C1" {
		t.Fatalf("the card maps %+v", open.Identity)
	}
	if open.Question != "A message reached talk from U999 in channel C1. Who is this?" || strings.Contains(open.Question, "TAIL-MARKER") {
		t.Fatalf("the question = %q", open.Question)
	}
	// One pending card per source and sender; another sender has its own.
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a second card for one sender = %v, %v", created, err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U888", "C2", "hi"); err != nil || !created {
		t.Fatalf("a card for another sender = %v, %v", created, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 2 {
		t.Fatalf("open asks = %d, want 2", len(q.Open))
	}
	// Only the operator picks who a sender is, and a refusal leaves the
	// card's bead open.
	pick := pickFor(t, y, open.Bead, "ada")
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "ada", false); err == nil {
		t.Fatal("ada picked who a sender is")
	}
	wantCardOpen(t, y, open.Bead)
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsIn("talk", "U999") {
		t.Fatalf("ada = %+v, want U999 mapped in talk", u.Identities)
	}
	// The pick ended the card: its bead is closed with what was written,
	// and its ask stands answered, not taken back. The other sender's card
	// stands.
	wantCardClosed(t, y, open.Bead, "U999 is ada in talk")
	wantDecided(t, y, open.Bead, "answered")
	if q := mappingQueue(t, y); len(q.Open) != 1 || q.Open[0].Identity.Sender != "U888" {
		t.Fatalf("open asks after the pick = %+v, want U888's card alone", q.Open)
	}
	// Once mapped, the sender offers no card.
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a card for a mapped sender = %v, %v", created, err)
	}
}

// A card that stands for a sender mapped since (one from before a
// mapping ended its cards) is stale: its pick writes nothing and leaves
// it open. Declining a card closes its bead as declined, leaves the
// sender unmapped, and their next delivery offers a new card. A source
// with no identity hearing, no sender and a sender with a control
// character offer none, and no writer may forge a mapping ask.
func TestClaimIdentityStaleDeclineAndRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	if created, err := y.ClaimIdentity(y.ctx, "chime", "U999", "C1", "hi"); err != nil || created {
		t.Fatalf("a card for a source with no identity hearing = %v, %v", created, err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "nowhere", "U999", "C1", "hi"); err != nil || created {
		t.Fatalf("a card for no source = %v, %v", created, err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "", "C1", "hi"); err != nil || created {
		t.Fatalf("a card with no sender = %v, %v", created, err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U99\x1b9", "C1", "hi"); err != nil || created {
		t.Fatalf("a card for a sender with a control character = %v, %v", created, err)
	}
	forged := Ask{Question: "Who is this?", To: "mckean",
		Context:  []string{"a delivery brought it"},
		Identity: &AskIdentity{Source: "talk", Sender: "U999", Users: []string{"ada"}}}
	if _, err := y.AddAsk(y.ctx, y.bead, forged, "yardmaster"); err == nil || !strings.Contains(err.Error(), "only from a dropped delivery") && !strings.Contains(err.Error(), "written by the yard") {
		t.Fatalf("AddAsk = %v, want a mapping-origin refusal", err)
	}

	if created, err := y.ClaimIdentity(y.ctx, "talk", "U777", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	open := mappingQueue(t, y).Open[0]
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	mapBehindTheCards(t, y, "bo", "talk", Identity{ID: "U777"})
	pick := pickFor(t, y, open.Bead, "ada")
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "mckean", true); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale pick = %v, want a stale refusal", err)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || u.IsIn("talk", "U777") {
		t.Fatalf("ada after a stale pick = %+v, %v", u, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after a stale pick = %d, want the card left open", len(q.Open))
	}
	wantCardOpen(t, y, open.Bead)
	// Declining a card for a sender still unmapped deletes its held
	// delivery, closes the bead, and offers a fresh card on the next
	// delivery.
	declined := holdTalk(t, y, "U555", "before decline")
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers before decline = %v, want none", got)
	}
	if err := y.DeclineAsk(y.ctx, declined.Bead, declined.Ask, "not now", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers after decline = %v, want none", got)
	}
	wantCardClosed(t, y, declined.Bead, "declined by mckean")
	if got := cardHeldCount(t, y, declined.Bead); got != 0 {
		t.Fatalf("declined card holds %d messages, want none", got)
	}
	wantDecided(t, y, declined.Bead, "declined")
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after a decline = %+v, %v", src, err)
	}
	if holders, err := mappedTo(y, "talk", "U555"); err != nil || len(holders) != 0 {
		t.Fatalf("U555 is mapped to %v after a decline, %v", holders, err)
	}
	again := holdTalk(t, y, "U555", "after decline")
	if again.Bead == declined.Bead {
		t.Fatalf("the next delivery's card is the declined one, %s", again.Bead)
	}
	wantCardOpen(t, y, again.Bead)
	wantCardClosed(t, y, declined.Bead, "declined by mckean")
	if err := y.AnswerAsk(y.ctx, again.Bead, again.Ask, pickFor(t, y, again.Bead, "ada"), "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	events, err := y.Events(y.ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var held []Event
	for _, event := range events {
		if event.Kind == "hook" {
			held = append(held, event)
		}
	}
	if len(held) != 1 || hookSpeakers(t, y)[0] != "ada" {
		t.Fatalf("released events = %+v, want only the post-decline message as ada", held)
	}
	if payload, err := y.HookPayload(y.ctx, held[0].Seq); err != nil || string(payload) != "after decline" {
		t.Fatalf("released payload = %q, %v, want only the post-decline message", payload, err)
	}
}

// A decline ends a mapping card as a pick that writes does, so it is
// the operator the card asks alone to give: another user's decline is
// refused whole, and the card stands open for the operator.
func TestMappingDeclineIsTheOperatorsAlone(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	card := claimCard(t, y, "U555")
	if err := y.DeclineAsk(y.ctx, card.Bead, card.Ask, "not now", "ada"); err == nil || !strings.Contains(err.Error(), "the operator") {
		t.Fatalf("a decline by ada = %v, want it refused as the operator's alone", err)
	}
	wantCardOpen(t, y, card.Bead)
	if q := mappingQueue(t, y); len(q.Open) != 1 || q.Open[0].Bead != card.Bead {
		t.Fatalf("open asks after a refused decline = %+v, want the card left open", q.Open)
	}
	if err := y.DeclineAsk(y.ctx, card.Bead, card.Ask, "not now", "mckean"); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, card.Bead, "declined by mckean")

	allowed := claimCard(t, y, "U556")
	var got Object
	y.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActAnswerIdentityAsk && actor == "ada" {
			got = obj
			return nil
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if err := y.DeclineAsk(y.ctx, allowed.Bead, allowed.Ask, "not now", "ada"); err != nil {
		t.Fatalf("a mapping decline allowed by policy: %v", err)
	}
	if got.Kind != KindAsk || got.Name != allowed.Bead || got.Seq != uint64(allowed.Ask) || got.To != "mckean" {
		t.Fatalf("mapping answer policy object = %+v, want ask %s/%d for mckean", got, allowed.Bead, allowed.Ask)
	}
	wantCardClosed(t, y, allowed.Bead, "declined by ada")
}

// mappedTo is the users who hold the sender's id in the source.
func mappedTo(y *askYard, source, sender string) (holders []string, err error) {
	err = y.read(func(tx *bolt.Tx) (err error) {
		holders, err = identityHolders(tx, source, sender)
		return err
	})
	return holders, err
}

// Someone new with a valid new name in the pick's words adds that user
// and writes their identity in the one answering write, closing the
// card's bead; empty, invalid and taken words refuse the answer and leave
// the card open, no user added.
func TestClaimIdentitySomeoneNew(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	open := holdTalk(t, y, "U999", "someone new body")
	someone := pickFor(t, y, open.Bead, MappingSomeoneNew)
	for words, want := range map[string]string{
		"":           "carries no words",
		"not a name": "use letters",
		"ada":        "there is a user ada already",
	} {
		if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, someone, words, "mckean", true); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("someone new with %q = %v, want %q", words, err, want)
		}
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after refused picks = %d, want the card left open", len(q.Open))
	}
	wantCardOpen(t, y, open.Bead)
	if _, err := y.GetUser(y.ctx, "bo"); err == nil {
		t.Fatal("a refused pick added its user")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, someone, "bo", "mckean", true); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "bo")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsIn("talk", "U999") {
		t.Fatalf("bo = %+v, want U999 mapped in talk", u.Identities)
	}
	wantCardClosed(t, y, open.Bead, "U999 is bo in talk")
	wantDecided(t, y, open.Bead, "answered")
	if got := cardHeldCount(t, y, open.Bead); got != 0 {
		t.Fatalf("someone-new card holds %d messages, want none", got)
	}
	if got := hookSpeakers(t, y); !slices.Equal(got, []string{"bo"}) {
		t.Fatalf("hook speakers after someone new = %v, want the held delivery as bo", got)
	}
	events, err := y.Events(y.ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var held []Event
	for _, event := range events {
		if event.Kind == "hook" {
			held = append(held, event)
		}
	}
	if len(held) != 1 {
		t.Fatalf("released events = %+v, want one held delivery", held)
	}
	if payload, err := y.HookPayload(y.ctx, held[0].Seq); err != nil || string(payload) != "someone new body" {
		t.Fatalf("released payload = %q, %v", payload, err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a card for a mapped sender = %v, %v", created, err)
	}
}

// Nobody ignores the sender and closes the card's bead: no second card
// stands for them, and none is offered again until the entry is removed
// or the sender is mapped directly, which clears it in the same write.
func TestClaimIdentityNobodyIgnores(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	open := holdTalk(t, y, "U999", "nobody body")
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers before nobody = %v, want none", got)
	}
	nobody := pickFor(t, y, open.Bead, MappingNobody)
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, nobody, "", "ada", false); err == nil {
		t.Fatal("ada ignored a sender")
	}
	wantCardOpen(t, y, open.Bead)
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, nobody, "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers after nobody = %v, want none", got)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || !slices.Equal(src.Ignored, []string{"U999"}) {
		t.Fatalf("ignored = %+v, %v", src, err)
	}
	wantCardClosed(t, y, open.Bead, "U999 is ignored in talk")
	if got := cardHeldCount(t, y, open.Bead); got != 0 {
		t.Fatalf("ignored card holds %d messages, want none", got)
	}
	wantDecided(t, y, open.Bead, "answered")
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a card for an ignored sender = %v, %v", created, err)
	}
	// Mapping the sender directly clears the entry in the same write.
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U999"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after the mapping = %+v, %v", src, err)
	}
	// Removing the identity later does not put the id back on the
	// list: the next message offers a fresh card, as for any sender
	// never ignored.
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "once more"); err != nil || !created {
		t.Fatalf("a card after the removal = %v, %v", created, err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after the removal = %+v, %v", src, err)
	}
	// Un-ignoring offers a fresh card on the next message.
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U888", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	var card *OpenAsk
	for _, o := range mappingQueue(t, y).Open {
		if o.Identity != nil && o.Identity.Sender == "U888" {
			c := o
			card = &c
		}
	}
	if card == nil {
		t.Fatal("no card for U888")
	}
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pickFor(t, y, card.Bead, MappingNobody), "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	if err := y.UnignoreSource(y.ctx, "talk", "U888", "ada"); err == nil {
		t.Fatal("ada un-ignored a sender")
	}
	if err := y.UnignoreSource(y.ctx, "talk", "U888", "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U888", "C1", "me again"); err != nil || !created {
		t.Fatalf("a card after un-ignoring = %v, %v", created, err)
	}
	if err := y.UnignoreSource(y.ctx, "talk", "U888", "mckean"); err == nil || !strings.Contains(err.Error(), "is not ignored") {
		t.Fatalf("removing what is not ignored = %v", err)
	}
}

// A card for a sender mapped since (one from before a mapping ended its
// cards) makes the someone-new and nobody picks stale too: no user is
// added, nothing is ignored, and the card stays open.
func TestClaimIdentityNewPicksAreStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U777", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	open := mappingQueue(t, y).Open[0]
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	mapBehindTheCards(t, y, "bo", "talk", Identity{ID: "U777"})
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pickFor(t, y, open.Bead, MappingSomeoneNew), "hal", "mckean", true); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale someone-new pick = %v, want a stale refusal", err)
	}
	if _, err := y.GetUser(y.ctx, "hal"); err == nil {
		t.Fatal("a stale pick added its user")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pickFor(t, y, open.Bead, MappingNobody), "", "mckean", false); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale nobody pick = %v, want a stale refusal", err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after a stale pick = %+v, %v", src, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after stale picks = %d, want the card left open", len(q.Open))
	}
	wantCardOpen(t, y, open.Bead)
}

// Two cards answered together write one identity at most: the same
// someone-new name on each adds the user once, and the second answer
// is refused with the card left open.
func TestClaimIdentitySomeoneNewNameTakenBetweenCards(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	for _, sender := range []string{"U111", "U222"} {
		if created, err := y.ClaimIdentity(y.ctx, "talk", sender, "C1", "hi"); err != nil || !created {
			t.Fatalf("ClaimIdentity for %s = %v, %v", sender, created, err)
		}
	}
	var first, second *OpenAsk
	for _, o := range mappingQueue(t, y).Open {
		if o.Identity == nil {
			continue
		}
		switch o.Identity.Sender {
		case "U111":
			c := o
			first = &c
		case "U222":
			c := o
			second = &c
		}
	}
	if first == nil || second == nil {
		t.Fatal("no card for each sender")
	}
	if err := y.AnswerAsk(y.ctx, first.Bead, first.Ask, pickFor(t, y, first.Bead, MappingSomeoneNew), "hal", "mckean", true); err != nil {
		t.Fatal(err)
	}
	if err := y.AnswerAsk(y.ctx, second.Bead, second.Ask, pickFor(t, y, second.Bead, MappingSomeoneNew), "hal", "mckean", true); err == nil || !strings.Contains(err.Error(), "there is a user hal already") {
		t.Fatalf("the second someone-new hal = %v, want a taken-name refusal", err)
	}
	if u, err := y.GetUser(y.ctx, "hal"); err != nil || !u.IsIn("talk", "U111") || u.IsIn("talk", "U222") {
		t.Fatalf("hal = %+v, %v, want U111 alone mapped", u, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 || q.Open[0].Identity.Sender != "U222" {
		t.Fatalf("open asks = %+v, want the second card left open", q.Open)
	}
}

// A sender mapped from a terminal ends the card that asks who they are:
// its bead closes with a reason naming the mapping, and its ask is taken
// back by the yard, so the operator's queue holds no card for someone who
// is mapped and the card no longer stands in the way of a fresh one. The
// id or the key names the sender, as hearing reads it, and another
// sender's card stands.
func TestMappingByHandEndsTheCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	card := holdTalk(t, y, "U777", "terminal mapping body")
	keyed := claimCard(t, y, "Dana")
	other := claimCard(t, y, "U888")
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetIdentity(y.ctx, "bo", "talk", Identity{ID: "U777"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := hookSpeakers(t, y); !slices.Equal(got, []string{"bo"}) {
		t.Fatalf("hook speakers after the terminal mapping = %v, want the held delivery as bo", got)
	}
	wantCardClosed(t, y, card.Bead, "U777 is bo in talk")
	if got := cardHeldCount(t, y, card.Bead); got != 0 {
		t.Fatalf("mapped card holds %d messages, want none", got)
	}
	if d := wantDecided(t, y, card.Bead, "taken back"); d.By != stageAskAuthor || d.Words != "U777 is bo in talk" {
		t.Fatalf("the card's ask ended as %+v, want taken back by the yard with the mapping as the reason", d)
	}
	// A key names the sender in any case.
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{Key: "dana"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, keyed.Bead, "Dana is ada in talk")
	wantCardOpen(t, y, other.Bead)
	if q := mappingQueue(t, y); len(q.Open) != 1 || q.Open[0].Bead != other.Bead {
		t.Fatalf("open asks = %+v, want U888's card alone", q.Open)
	}
	// The card takes no answer now.
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pickFor(t, y, card.Bead, "ada"), "", "mckean", false); err == nil || !strings.Contains(err.Error(), "asked no more") {
		t.Fatalf("a pick on the ended card = %v, want it refused as asked no more", err)
	}
	// And it no longer counts as pending: once the sender is unmapped, their
	// next delivery offers a fresh card.
	if err := y.SetIdentity(y.ctx, "bo", "talk", Identity{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if again := claimCard(t, y, "U777"); again.Bead == card.Bead {
		t.Fatalf("the next delivery's card is the ended one, %s", again.Bead)
	}
	closed := holdTalk(t, y, "U666", "gone with closed card")
	if _, err := y.CloseBead(y.ctx, closed.Bead, "closed by hand", Expect{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := cardHeldCount(t, y, closed.Bead); got != 0 {
		t.Fatalf("closed card holds %d messages, want none", got)
	}
	fresh := holdTalk(t, y, "U666", "after close")
	if fresh.Bead == closed.Bead {
		t.Fatalf("a delivery after closing its card reused %s", closed.Bead)
	}
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U666"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if got, want := hookSpeakers(t, y), []string{"bo", "ada"}; !slices.Equal(got, want) {
		t.Fatalf("hook speakers after mapping the new card = %v, want %v", got, want)
	}
	events, err := y.Events(y.ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var held []Event
	for _, event := range events {
		if event.Kind == "hook" {
			held = append(held, event)
		}
	}
	if len(held) != 2 {
		t.Fatalf("released events = %+v, want the terminal and fresh-card deliveries", held)
	}
	if payload, err := y.HookPayload(y.ctx, held[1].Seq); err != nil || string(payload) != "after close" {
		t.Fatalf("delivery after close = %q, %v, want only the fresh message", payload, err)
	}
}

// A mapping an approved run ask makes ends the card as a terminal one
// does. The bead the run ask stands on is no card and stays open.
func TestMappingByRunAskEndsTheCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	card := claimCard(t, y, "U999")
	seq := y.ask(t, listAsk("mckean", `user identity ada --source talk --id U999`), "yardmaster")
	wantCardOpen(t, y, card.Bead)
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, card.Bead, "U999 is ada in talk")
	wantCardOpen(t, y, y.bead)
}

// Later puts the card aside and You decide leaves it to the yard: neither
// closes its bead. A sender settled afterwards ends the card either way,
// taking the ask back where it still waited, and a You decide pick lets
// the next delivery offer a second card, which the answer to it ends with
// the first.
func TestMappingCardLaterAndYouDecideStayOpen(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	later := claimCard(t, y, "U111")
	if err := y.DeferAsk(y.ctx, later.Bead, later.Ask, "mckean"); err != nil {
		t.Fatal(err)
	}
	wantCardOpen(t, y, later.Bead)
	wantDecided(t, y, later.Bead, "put aside")

	decide := claimCard(t, y, "U222")
	if err := y.AnswerAsk(y.ctx, decide.Bead, decide.Ask, pickFor(t, y, decide.Bead, YouDecide), "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	wantCardOpen(t, y, decide.Bead)
	wantDecided(t, y, decide.Bead, "answered")
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after You decide = %+v, %v", src, err)
	}

	// Mapped from a terminal later, the put-aside card ends with its ask
	// taken back.
	if err := y.SetIdentity(y.ctx, "ada", "talk", Identity{ID: "U111"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, later.Bead, "U111 is ada in talk")
	wantDecided(t, y, later.Bead, "taken back")
	wantCardOpen(t, y, decide.Bead)

	// The answered card does not stand in the way of a second one, and
	// ignoring the sender on that ends both.
	second := claimCard(t, y, "U222")
	if second.Bead == decide.Bead {
		t.Fatalf("the next delivery's card is the answered one, %s", second.Bead)
	}
	if err := y.AnswerAsk(y.ctx, second.Bead, second.Ask, pickFor(t, y, second.Bead, MappingNobody), "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	wantCardClosed(t, y, second.Bead, "U222 is ignored in talk")
	wantCardClosed(t, y, decide.Bead, "U222 is ignored in talk")
	wantDecided(t, y, decide.Bead, "answered")
}

// An ignore entry written for a sender ends the card that asks who they
// are, taking its ask back; so does the entry of an id already on the
// list, which a card from before entries ended cards can stand beside.
func TestIgnoreEntryEndsTheCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	card := holdTalk(t, y, "U123", "ignored body")
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers before ignore = %v, want none", got)
	}
	ignore := func(sender string) {
		t.Helper()
		if err := y.write(func(w *wtx) error { return w.ignoreSender(y.ctx, "talk", sender, "mckean") }); err != nil {
			t.Fatal(err)
		}
	}
	ignore("U123")
	if got := hookSpeakers(t, y); len(got) != 0 {
		t.Fatalf("hook speakers after ignore = %v, want none", got)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || !slices.Equal(src.Ignored, []string{"U123"}) {
		t.Fatalf("ignored = %+v, %v", src, err)
	}
	wantCardClosed(t, y, card.Bead, "U123 is ignored in talk")
	if got := cardHeldCount(t, y, card.Bead); got != 0 {
		t.Fatalf("ignored card holds %d messages, want none", got)
	}
	if d := wantDecided(t, y, card.Bead, "taken back"); d.By != stageAskAuthor || d.Words != "U123 is ignored in talk" {
		t.Fatalf("the card's ask ended as %+v, want taken back by the yard with the entry as the reason", d)
	}

	stale := claimCard(t, y, "U124")
	err := y.write(func(w *wtx) error {
		src := new(Source)
		if _, err := get(w.Tx, bSources, "talk", src); err != nil {
			return err
		}
		src.Ignored = append(src.Ignored, "U124")
		return put(w.Tx, bSources, "talk", src)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCardOpen(t, y, stale.Bead)
	ignore("U124")
	wantCardClosed(t, y, stale.Bead, "U124 is ignored in talk")
}

// holdTalk holds one talk delivery from sender on its card, as the
// receiver holds a verified one, and answers the card.
func holdTalk(t *testing.T, y *askYard, sender, body string) OpenAsk {
	t.Helper()
	env := map[string]any{
		"source": "talk", "type": "message", "action": "create",
		"text": body, "mentions": []any{},
		"fields": map[string]any{"sender": []any{sender}, "channel": []any{"C1"}},
	}
	if held, err := y.HoldIdentity(y.ctx, "talk", "talk", sender, "C1", env, []byte(body), nil); err != nil || !held {
		t.Fatalf("HoldIdentity for %s = %v, %v", sender, held, err)
	}
	var card *OpenAsk
	for _, o := range mappingQueue(t, y).Open {
		if o.Identity != nil && o.Identity.Sender == sender {
			if card == nil || o.Ask > card.Ask {
				c := o
				card = &c
			}
		}
	}
	if card != nil {
		return *card
	}
	t.Fatalf("no card for %s after a hold", sender)
	return OpenAsk{}
}

func hookSpeakers(t *testing.T, y *askYard) []string {
	t.Helper()
	evs, err := y.Events(y.ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Kind != "hook" {
			continue
		}
		fields, _ := e.Data["fields"].(map[string]any)
		speakers, _ := fields["speaker"].([]any)
		if len(speakers) == 1 {
			if s, _ := speakers[0].(string); s != "" {
				out = append(out, s)
				continue
			}
		}
		out = append(out, "")
	}
	return out
}
