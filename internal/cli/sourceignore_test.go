package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// talkIgnore declares the identity-hearing source talk: fields.sender
// holds a member id, matched against a user's identity in talk.
const talkIgnore = `name: talk
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

// ignoreYard is a yard whose operator is jo, with ada as a second user
// and the identity-hearing source talk declared.
func ignoreYard(t *testing.T) (*app, store.API, *store.Store) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, u := range []string{"jo", "ada"} {
		if _, err := st.AddUser(ctx, u, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "talk.yaml", talkIgnore, "jo"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "jo"}, st, own
}

// ignoreSender ignores the sender in talk through the mapping card's
// nobody pick, as only that pick adds an entry.
func ignoreSender(t *testing.T, own *store.Store, sender string) {
	t.Helper()
	ctx := context.Background()
	if created, err := own.ClaimIdentity(ctx, "talk", sender, "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity for %s = %v, %v", sender, created, err)
	}
	q, err := own.UserAsks(ctx, "jo")
	if err != nil {
		t.Fatal(err)
	}
	var card *store.OpenAsk
	for _, o := range q.Open {
		if o.Identity != nil && o.Identity.Sender == sender {
			c := o
			card = &c
		}
	}
	if card == nil {
		t.Fatalf("no card for %s", sender)
	}
	view, err := own.Bead(ctx, card.Bead)
	if err != nil {
		t.Fatal(err)
	}
	pick := 0
	for i, o := range view.Asks[0].Options {
		if o.Label == store.MappingNobody {
			pick = i + 1
		}
	}
	if err := own.AnswerAsk(ctx, card.Bead, card.Ask, pick, "", "jo", false); err != nil {
		t.Fatal(err)
	}
}

// yardr source ignore lists the ignored senders of a source that hears
// by identity, and --rm removes one; yardr source show prints them with
// the rest of the source. A source with no identity hearing ignores
// nothing, and removing is the operator's.
func TestSourceIgnore(t *testing.T) {
	t.Parallel()
	a, st, own := ignoreYard(t)
	ctx := context.Background()
	if out, err := run(t, a.sourceCmd(), "ignore", "talk"); err != nil || out != "talk: no sender is ignored\n" {
		t.Fatalf("ignore of none: %q, %v", out, err)
	}
	ignoreSender(t, own, "U999")
	ignoreSender(t, own, "U888")
	if out, err := run(t, a.sourceCmd(), "ignore", "talk"); err != nil || out != "U888\nU999\n" {
		t.Fatalf("ignore: %q, %v", out, err)
	}
	a.json = true
	out, err := run(t, a.sourceCmd(), "ignore", "talk")
	a.json = false
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Source  string   `json:"source"`
		Ignored []string `json:"ignored"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Source != "talk" || len(got.Ignored) != 2 || got.Ignored[0] != "U888" || got.Ignored[1] != "U999" {
		t.Fatalf("ignore --json = %s, %v", out, err)
	}
	if out, err := run(t, a.sourceCmd(), "show", "talk"); err != nil ||
		!strings.Contains(out, "  hear: keeps a delivery whose sender is some user's identity in talk (yardr user identity); anything else follows the hook's --unknown word\n") ||
		!strings.Contains(out, "  ignored: U888, U999\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
	// Removing is the operator's; typed, it prints what the run ask's
	// approval records without the command's name.
	ada := &app{st: st, actor: "ada"}
	if _, err := run(t, ada.sourceCmd(), "ignore", "talk", "--rm", "U999"); err == nil || !strings.Contains(err.Error(), "un-ignoring a sender in a source is the operator's (jo)") {
		t.Fatalf("ada removing = %v", err)
	}
	if out, err := run(t, a.sourceCmd(), "ignore", "talk", "--rm", "U999"); err != nil || out != "talk: U999 is no longer ignored\n" {
		t.Fatalf("ignore --rm: %q, %v", out, err)
	}
	if out, err := run(t, a.sourceCmd(), "ignore", "talk"); err != nil || out != "U888\n" {
		t.Fatalf("ignore after the removal: %q, %v", out, err)
	}
	if out, err := run(t, a.sourceCmd(), "show", "talk"); err != nil || !strings.Contains(out, "  ignored: U888\n") {
		t.Fatalf("show after the removal: %q, %v", out, err)
	}
	if _, err := run(t, a.sourceCmd(), "ignore", "talk", "--rm", "U999"); err == nil || !strings.Contains(err.Error(), "is not ignored") {
		t.Fatalf("removing what is not ignored = %v", err)
	}
	if _, err := run(t, a.sourceCmd(), "ignore", "talk", "--rm", ""); err == nil || !strings.Contains(err.Error(), "give the ignored id to remove") {
		t.Fatalf("removing nothing = %v", err)
	}
	if _, err := st.PutSource(ctx, "chime.yaml", "name: chime\nbase: https://chime.example.com\ncredential: { env: CHIME_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "ignore", "chime"); err == nil || !strings.Contains(err.Error(), "hears no sender by identity") {
		t.Fatalf("ignore of a source with no identity hearing = %v", err)
	}
	if _, err := run(t, a.sourceCmd(), "ignore", "chime", "--rm", "U999"); err == nil || !strings.Contains(err.Error(), "hears no sender by identity") {
		t.Fatalf("ignore --rm of a source with no identity hearing = %v", err)
	}
	if out, err := run(t, a.sourceCmd(), "show", "chime"); err != nil || strings.Contains(out, "ignored:") {
		t.Fatalf("show of a source with no identity hearing: %q, %v", out, err)
	}
	if _, err := run(t, a.sourceCmd(), "ignore", "nowhere"); err == nil {
		t.Fatalf("ignore of no source was listed")
	}
}
