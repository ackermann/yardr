package store

import (
	"slices"
	"strings"
	"testing"
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
	q, err := y.UserAsks(y.ctx, "mckean")
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

// A dropped delivery's sender stands one mapping card for the
// operator showing the member id, the channel and at most 80
// characters of the message; picking a user writes their identity
// and the sender offers no second card, by then or once mapped.
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
	if !strings.Contains(open.Question, "A message reached talk from U999 in channel C1 (first words: '") ||
		!strings.HasSuffix(open.Question, "'). Who is this?") || strings.Contains(open.Question, "TAIL-MARKER") {
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
	// Only the operator picks who a sender is.
	pick := pickFor(t, y, open.Bead, "ada")
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "ada"); err == nil {
		t.Fatal("ada picked who a sender is")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsIn("talk", "U999") {
		t.Fatalf("ada = %+v, want U999 mapped in talk", u.Identities)
	}
	// Once mapped, the sender offers no card.
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a card for a mapped sender = %v, %v", created, err)
	}
}

// A mapping written by hand meanwhile makes the card stale: its pick
// writes nothing and leaves it open. Declining it leaves the sender
// unmapped, and their next delivery offers a card again. A source
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
	if err := y.SetIdentity(y.ctx, "bo", "talk", Identity{ID: "U777"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	pick := pickFor(t, y, open.Bead, "ada")
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pick, "", "mckean"); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale pick = %v, want a stale refusal", err)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || u.IsIn("talk", "U777") {
		t.Fatalf("ada after a stale pick = %+v, %v", u, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after a stale pick = %d, want the card left open", len(q.Open))
	}
	// Declining a card for a sender still unmapped offers a card
	// again on their next delivery.
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U555", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	var declined *OpenAsk
	for _, o := range mappingQueue(t, y).Open {
		if o.Identity != nil && o.Identity.Sender == "U555" {
			c := o
			declined = &c
		}
	}
	if declined == nil {
		t.Fatal("no card for U555")
	}
	if err := y.AddAnswer(y.ctx, declined.Bead, Answer{Ask: declined.Ask, Decline: true, Words: "not now"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U555", "C1", "me again"); err != nil || !created {
		t.Fatalf("a card after a decline = %v, %v", created, err)
	}
}

// Someone new with a valid new name in the pick's words adds that user
// and writes their identity in the one answering write; empty, invalid
// and taken words refuse the answer and leave the card open, no user
// added.
func TestClaimIdentitySomeoneNew(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	open := mappingQueue(t, y).Open[0]
	someone := pickFor(t, y, open.Bead, MappingSomeoneNew)
	for words, want := range map[string]string{
		"":           "carries no words",
		"not a name": "use letters",
		"ada":        "there is a user ada already",
	} {
		if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, someone, words, "mckean"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("someone new with %q = %v, want %q", words, err, want)
		}
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after refused picks = %d, want the card left open", len(q.Open))
	}
	if _, err := y.GetUser(y.ctx, "bo"); err == nil {
		t.Fatal("a refused pick added its user")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, someone, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "bo")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsIn("talk", "U999") {
		t.Fatalf("bo = %+v, want U999 mapped in talk", u.Identities)
	}
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "again"); err != nil || created {
		t.Fatalf("a card for a mapped sender = %v, %v", created, err)
	}
}

// Nobody ignores the sender: no second card stands for them, and none
// is offered again until the entry is removed or the sender is mapped
// directly, which clears it in the same write.
func TestClaimIdentityNobodyIgnores(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	if created, err := y.ClaimIdentity(y.ctx, "talk", "U999", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	open := mappingQueue(t, y).Open[0]
	nobody := pickFor(t, y, open.Bead, MappingNobody)
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, nobody, "", "ada"); err == nil {
		t.Fatal("ada ignored a sender")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, nobody, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || !slices.Equal(src.Ignored, []string{"U999"}) {
		t.Fatalf("ignored = %+v, %v", src, err)
	}
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
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pickFor(t, y, card.Bead, MappingNobody), "", "mckean"); err != nil {
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

// A mapping written by hand meanwhile makes the someone-new and nobody
// picks stale too: no user is added, nothing is ignored, and the card
// stays open.
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
	if err := y.SetIdentity(y.ctx, "bo", "talk", Identity{ID: "U777"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pickFor(t, y, open.Bead, MappingSomeoneNew), "hal", "mckean"); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale someone-new pick = %v, want a stale refusal", err)
	}
	if _, err := y.GetUser(y.ctx, "hal"); err == nil {
		t.Fatal("a stale pick added its user")
	}
	if err := y.AnswerAsk(y.ctx, open.Bead, open.Ask, pickFor(t, y, open.Bead, MappingNobody), "", "mckean"); err == nil || !strings.Contains(err.Error(), "names bo already") {
		t.Fatalf("a stale nobody pick = %v, want a stale refusal", err)
	}
	if src, err := y.GetSource(y.ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after a stale pick = %+v, %v", src, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after stale picks = %d, want the card left open", len(q.Open))
	}
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
	if err := y.AnswerAsk(y.ctx, first.Bead, first.Ask, pickFor(t, y, first.Bead, MappingSomeoneNew), "hal", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.AnswerAsk(y.ctx, second.Bead, second.Ask, pickFor(t, y, second.Bead, MappingSomeoneNew), "hal", "mckean"); err == nil || !strings.Contains(err.Error(), "there is a user hal already") {
		t.Fatalf("the second someone-new hal = %v, want a taken-name refusal", err)
	}
	if u, err := y.GetUser(y.ctx, "hal"); err != nil || !u.IsIn("talk", "U111") || u.IsIn("talk", "U222") {
		t.Fatalf("hal = %+v, %v, want U111 alone mapped", u, err)
	}
	if q := mappingQueue(t, y); len(q.Open) != 1 || q.Open[0].Identity.Sender != "U222" {
		t.Fatalf("open asks = %+v, want the second card left open", q.Open)
	}
}
