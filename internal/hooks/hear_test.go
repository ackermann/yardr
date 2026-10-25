package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// chimeNormalize turns a delivery into a message in the chat its body
// names: fields.chat is what the hear key matches.
const chimeNormalize = `#!/bin/sh
chat=$(sed -n 's/.*"chat": *"\([^"]*\)".*/\1/p')
printf '{"type":"message","action":"create","text":"hi","fields":{"chat":["%s"]}}' "$chat"
`

// chimeText is a declared source that tells and receives: notify through
// the message call with the chat id under CHIME_CHAT, a secret-verified
// hook with keys [chat], and hear on the chat field. from narrows whom it
// hears; "" hears every user whose env file sets the address.
func chimeText(from string) string {
	hear := "  field: chat\n  address: { env: CHIME_CHAT }\n"
	if from != "" {
		hear += "  from: " + from + "\n"
	}
	return `name: chime
base: https://chime.example.com
credential: { env: CHIME_TOKEN }
calls:
  message:
    kind: update
    params: { chat: { type: string }, text: { type: string } }
    request: { method: POST, path: /send, body: { chat: "{chat}", text: "{text}" } }
notify:
  call: message
  address: { param: chat, env: CHIME_CHAT }
  text: text
hook:
  verify: { scheme: secret, signature: { header: Chime-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [chat]
hear:
` + hear
}

// hearDepot is a depot whose receiver serves the declared source chime,
// hearing as from says, with hook chime.
func hearDepot(t *testing.T, normalize, from string) *depot {
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	text := chimeText(from)
	d, err := source.Parse("chime.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(normalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "chime.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "chime.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "chime", Source: "chime", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// chime posts a delivery from the chat to the chime hook.
func (r *depot) chime(t *testing.T, chat, text string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"chat": chat, "text": text})
	return r.post(t, "/hooks/chime", body, map[string]string{"Chime-Secret": secret})
}

// tellChime tells the user through chime at the chat: their env file sets
// CHIME_CHAT to it. The user is told nothing here; the file is what the
// hear key reads.
func tellChime(t *testing.T, r *depot, user, chat string) {
	t.Helper()
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), user+".env")
	if err := os.WriteFile(file, []byte("CHIME_CHAT="+chat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetUser(ctx, user, map[string]string{store.UserNotify: "chime", store.UserEnvFile: file}, user); err != nil {
		t.Fatal(err)
	}
}

// A source with hear and from: operator keeps a delivery from the
// operator's chat, and drops one from any other chat: answered 200,
// recorded nowhere, counted beside refusals. Another user's chat is
// dropped like a stranger's, though their env file sets the address and
// they are told through the source.
func TestHearFromOperatorKeepsOperatorDropsStranger(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "operator")
	ctx := context.Background()
	const marker = "MARKER-4wta-must-not-be-kept"
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddUser(ctx, "zed", "op"); err != nil {
		t.Fatal(err)
	}
	tellChime(t, r, "op", "111")
	tellChime(t, r, "zed", "222")
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "chime", Scope: store.ScopeAll}, "ann"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	if code := r.chime(t, "111", "the shed lands Monday"); code != http.StatusOK {
		t.Fatalf("the operator's chat answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	page, err := r.st.Inbox(ctx, "ann", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("ann's inbox holds %d, want the delivery", page.Total)
	}
	// Another user's chat is dropped like a stranger's, and a
	// stranger's too: answered 200, recorded nowhere.
	for _, chat := range []string{"222", "999"} {
		r.now = r.now.Add(time.Second)
		if code := r.chime(t, chat, "stranger words "+marker); code != http.StatusOK {
			t.Fatalf("chat %s answered %d", chat, code)
		}
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("dropped deliveries recorded: %d hook events", n)
	}
	page, err = r.st.Inbox(ctx, "ann", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("ann's inbox holds %d, want only the operator's delivery", page.Total)
	}
	if got := r.rc.Rejected()["stranger"]; got != 2 {
		t.Fatalf("stranger count %d, want 2", got)
	}
	// A stranger's burst is one bounded entry: the first drop logs its
	// sum at once, the rest are counted and said when the window ends.
	for i := 0; i < 50; i++ {
		r.now = r.now.Add(time.Second)
		r.chime(t, "999", "burst words "+marker)
	}
	r.rc.Flush(false) // the window has not ended
	if got, want := r.rejections(t), []string{"chime stranger 200 1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("within the window: %v, want %v", got, want)
	}
	r.now = r.now.Add(hooks.RejectWindow)
	r.rc.Flush(false)
	if got, want := r.rejections(t), []string{"chime stranger 200 1", "chime stranger 200 51"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after the window: %v, want %v", got, want)
	}
	if h, err := r.st.GetHook(ctx, "chime"); err != nil || h.RefusedFor != "stranger" {
		t.Fatalf("hook refused for %+v %v, want stranger", h, err)
	}
	// A dropped delivery leaves no text of the stranger's behind: no
	// event, no payload, and no sum carries it.
	evs, err := r.st.Events(ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), marker) {
			t.Fatalf("%s event carries the stranger's text: %s", e.Kind, raw)
		}
	}
	for _, e := range r.hookEvents(t) {
		raw, err := r.st.HookPayload(ctx, e.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), marker) {
			t.Fatalf("payload of %d carries the stranger's text: %s", e.Seq, raw)
		}
	}
	// Made an operator, zed is heard as op is: from: operator is the role,
	// not the operator setting.
	if _, err := r.st.SetUser(ctx, "zed", map[string]string{store.UserRole: store.RoleOperator}, "op"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Second)
	if code := r.chime(t, "222", "zed's words"); code != http.StatusOK {
		t.Fatalf("zed's chat answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 2 {
		t.Fatalf("want 2 hook events once zed is an operator, got %d", n)
	}
}

// Without from every user whose env file sets the address is heard, told
// through the source or told nowhere: another user's chat is recorded,
// and only a stranger's is dropped.
func TestHearWithoutFromHearsEveryAddress(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "")
	ctx := context.Background()
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddUser(ctx, "zed", "op"); err != nil {
		t.Fatal(err)
	}
	tellChime(t, r, "op", "111")
	tellChime(t, r, "zed", "222")
	// Told nowhere: hearing follows the address.
	if _, err := r.st.SetUser(ctx, "zed", map[string]string{store.UserNotify: ""}, "zed"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	if code := r.chime(t, "222", "zed's line"); code != http.StatusOK {
		t.Fatalf("another user's chat answered %d", code)
	}
	if code := r.chime(t, "999", "a stranger's line"); code != http.StatusOK {
		t.Fatalf("a stranger's chat answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
}

// A delivery whose envelope carries no field to match is dropped: nothing
// of it can be a user's address.
func TestHearWithoutFieldDrops(t *testing.T) {
	const fieldless = `#!/bin/sh
printf '%s' '{"type":"message","action":"create","text":"hi"}'
`
	r := hearDepot(t, fieldless, "operator")
	ctx := context.Background()
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	tellChime(t, r, "op", "111")
	r.now = time.Now()
	if code := r.chime(t, "111", "the shed lands Monday"); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a delivery with no field recorded: %d hook events", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
}

// ---- hearing by identity -------------------------------------------

const talkNormalize = `#!/bin/sh
sender=$(cat | sed -n 's/.*"sender": *"\([^"]*\)".*/\1/p')
printf '{"type":"message","action":"create","text":"hi","actor":{"id":"%s"},"fields":{"sender":["%s"],"channel":["C1"]}}' "$sender" "$sender"
`

const talkText = `name: talk
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
  normalize: [%s]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`

// identityDepot is a yard like hearDepot's whose source hears by
// identity: fields.sender holds a member id, matched against a user's
// identity in talk. unknown is the hook's --unknown word, "" for a
// hook made without it.
func identityDepot(t *testing.T, unknown string) *depot {
	return identityDepotWithText(t, unknown, fmt.Sprintf(talkText, "normalize"))
}

// hookOnlyIdentityDepot is an identity-hearing source with only its inbound
// hook: the receiver must still offer the mapping card for an unmapped sender.
func hookOnlyIdentityDepot(t *testing.T, unknown string) *depot {
	text := `name: talk
hook:
  verify: { scheme: secret, signature: { header: Talk-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`
	return identityDepotWithText(t, unknown, text)
}

func identityDepotWithText(t *testing.T, unknown, text string) *depot {
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.home = home
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	d, err := source.Parse("talk.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(talkNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "talk.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "talk.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "talk", Source: "talk", SecretFile: secretFile, Unknown: unknown}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// talk posts a delivery from the sender to the talk hook.
func (r *depot) talk(t *testing.T, sender, text string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sender": sender, "text": text})
	return r.post(t, "/hooks/talk", body, map[string]string{"Talk-Secret": secret})
}

func mapTalk(t *testing.T, r *depot, user, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.st.GetUser(ctx, user); err != nil {
		if _, err := r.st.AddUser(ctx, user, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.st.SetIdentity(ctx, user, "talk", store.Identity{ID: id}, user); err != nil {
		t.Fatal(err)
	}
}

func speakerOf(t *testing.T, e store.Event) string {
	t.Helper()
	fields, _ := e.Data["fields"].(map[string]any)
	if fields == nil {
		return ""
	}
	switch v := fields["speaker"].(type) {
	case []any:
		if len(v) > 0 {
			s, _ := v[0].(string)
			return s
		}
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// A normalize command cannot set speaker: the field is derived by the
// yard for a delivery it heard by identity, and another source's value
// would make the inbox line claim a sender the yard never matched.
func TestHearByAddressCannotSetSpeaker(t *testing.T) {
	const speakerNormalize = `#!/bin/sh
printf '%s' '{"type":"message","action":"create","actor":{"id":"U123"},"fields":{"chat":["123"],"speaker":["ada"]}}'
`
	r := hearDepot(t, speakerNormalize, "")
	if _, err := r.st.AddUser(context.Background(), "op", "me"); err != nil {
		t.Fatal(err)
	}
	tellChime(t, r, "op", "123")
	r.now = time.Now()
	if code := r.chime(t, "123", "hello"); code != http.StatusOK {
		t.Fatalf("delivery answered %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("hook events = %d, want one kept delivery", len(evs))
	}
	fields, _ := evs[0].Data["fields"].(map[string]any)
	if _, ok := fields[store.SpeakerKey]; ok {
		t.Fatalf("a normalized speaker reached the event: %+v", fields[store.SpeakerKey])
	}
}

// Hearing by identity: a mapped sender is kept with their yard name as
// speaker, an unmapped one is dropped under unknown (its mapping card
// is TestHearByIdentityOffersMappingCard, which sets an operator for
// it to ask), kept as outside under allow, and dropped with none
// under ignore.
func TestHearByIdentity(t *testing.T) {
	// Mapped under the default word, and unmapped under it.
	r := identityDepot(t, "")
	mapTalk(t, r, "ada", "U012")
	r.now = time.Now()
	if code := r.talk(t, "U012", "mapped words"); code != http.StatusOK {
		t.Fatalf("mapped answered %d", code)
	}
	if code := r.talk(t, "U999", "unmapped words"); code != http.StatusOK {
		t.Fatalf("unmapped answered %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("want 1 hook event, got %d", len(evs))
	}
	if got := speakerOf(t, evs[0]); got != "ada" {
		t.Fatalf("speaker = %q, want ada", got)
	}
	actor, _ := evs[0].Data["actor"].(map[string]any)
	if actor["id"] != "U012" {
		t.Fatalf("actor = %v, want its id left as the member id", actor)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
	if h, err := r.st.GetHook(context.Background(), "talk"); err != nil || h.Unknown != store.HookUnknownUnknown {
		t.Fatalf("hook without --unknown stored %+v %v, want unknown %q", h, err, store.HookUnknownUnknown)
	}

	// Unmapped under allow is kept as outside.
	r = identityDepot(t, store.HookUnknownAllow)
	mapTalk(t, r, "ada", "U012")
	r.now = time.Now()
	r.talk(t, "U999", "outside words")
	evs = r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("allow: want 1 hook event, got %d", len(evs))
	}
	if got := speakerOf(t, evs[0]); got != store.SpeakerOutside {
		t.Fatalf("allow: speaker = %q, want %q", got, store.SpeakerOutside)
	}
	if got := r.rc.Rejected()["stranger"]; got != 0 {
		t.Fatalf("allow: stranger count %d, want 0", got)
	}

	// Unmapped under ignore is dropped, with no card offered: no hook
	// event, and the stranger count the drop's only record besides the
	// bounded refusal sum.
	r = identityDepot(t, store.HookUnknownIgnore)
	mapTalk(t, r, "ada", "U012")
	r.now = time.Now()
	r.talk(t, "U999", "ignored words")
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("ignore: want 0 hook events, got %d", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("ignore: stranger count %d, want 1", got)
	}

	// Each of the three words stores on the hook as given.
	for _, word := range store.HookUnknowns {
		rr := identityDepot(t, word)
		if h, err := rr.st.GetHook(context.Background(), "talk"); err != nil || h.Unknown != word {
			t.Fatalf("hook with --unknown %s stored %+v %v", word, h, err)
		}
	}
}

// An unmapped delivery under unknown offers the operator one mapping
// card per source and sender, showing the member id, the channel and
// the first words; picking a user on it writes their identity, and
// the sender's next delivery is heard as theirs. Under allow and
// under ignore no card is offered.
func TestHearByIdentityOffersMappingCard(t *testing.T) {
	ctx := context.Background()
	r := hookOnlyIdentityDepot(t, "")
	if _, err := r.st.AddUser(ctx, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddUser(ctx, "ada", "op"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	mapTalk(t, r, "ada", "U012")
	r.now = time.Now()
	if code := r.talk(t, "U999", "hello there"); code != http.StatusOK {
		t.Fatalf("unmapped answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a dropped delivery recorded: %d hook events", n)
	}
	q, err := r.st.UserAsks(ctx, "op", "op")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 {
		t.Fatalf("open asks = %d, want the one mapping card", len(q.Open))
	}
	card := q.Open[0]
	if card.Identity == nil || card.Identity.Source != "talk" || card.Identity.Sender != "U999" || card.Identity.Channel != "C1" {
		t.Fatalf("the card maps %+v", card.Identity)
	}
	if !strings.Contains(card.Question, "A message reached talk from U999 in channel C1 (first words: 'hi'). Who is this?") {
		t.Fatalf("the question = %q", card.Question)
	}
	// A second delivery from the same sender offers no second card,
	// and one from another sender offers its own.
	r.now = r.now.Add(time.Second)
	r.talk(t, "U999", "again")
	r.now = r.now.Add(time.Second)
	r.talk(t, "U888", "someone else")
	if q, err := r.st.UserAsks(ctx, "op", "op"); err != nil || len(q.Open) != 2 {
		t.Fatalf("open asks = %v, %v, want 2", q, err)
	}
	// Picking ada on the first card writes ada's identity for U999.
	view, err := r.st.Bead(ctx, card.Bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Asks) != 1 {
		t.Fatalf("the card bead holds %d asks", len(view.Asks))
	}
	pick := 0
	for i, o := range view.Asks[0].Options {
		if o.Label == "ada" {
			pick = i + 1
		}
	}
	if pick == 0 {
		t.Fatalf("the card offers %v, want ada among them", view.Asks[0].Options)
	}
	if err := r.st.AnswerAsk(ctx, card.Bead, card.Ask, pick, "", "op", true); err != nil {
		t.Fatal(err)
	}
	u, err := r.st.GetUser(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsIn("talk", "U999") {
		t.Fatalf("ada after the pick = %+v, want U999 mapped in talk", u.Identities)
	}
	r.now = r.now.Add(time.Second)
	if code := r.talk(t, "U999", "mapped now"); code != http.StatusOK {
		t.Fatalf("the mapped sender answered %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 || speakerOf(t, evs[0]) != "ada" {
		t.Fatalf("hook events = %v, want the mapped delivery as ada", evs)
	}

	// Under allow the sender is heard and no card is offered; under
	// ignore it is dropped and none is offered either.
	for _, word := range []string{store.HookUnknownAllow, store.HookUnknownIgnore} {
		rr := identityDepot(t, word)
		if _, err := rr.st.AddUser(ctx, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := rr.st.AddUser(ctx, "ada", "op"); err != nil {
			t.Fatal(err)
		}
		if err := rr.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		mapTalk(t, rr, "ada", "U012")
		rr.now = time.Now()
		rr.talk(t, "U999", "unmapped words")
		if q, err := rr.st.UserAsks(ctx, "op", "op"); err != nil || len(q.Open) != 0 {
			t.Fatalf("%s: open asks = %v, %v, want none", word, q, err)
		}
	}
}

// nobodyPick answers the mapping card waiting for the sender with its
// nobody pick, ignoring them.
func nobodyPick(t *testing.T, r *depot, sender string) {
	t.Helper()
	ctx := context.Background()
	q, err := r.st.UserAsks(ctx, "op", "op")
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
		t.Fatalf("no mapping card for %s", sender)
	}
	view, err := r.st.Bead(ctx, card.Bead)
	if err != nil {
		t.Fatal(err)
	}
	pick := 0
	for i, o := range view.Asks[0].Options {
		if o.Label == store.MappingNobody {
			pick = i + 1
		}
	}
	if pick == 0 {
		t.Fatalf("the card offers %v, want the nobody pick among them", view.Asks[0].Options)
	}
	if err := r.st.AnswerAsk(ctx, card.Bead, card.Ask, pick, "", "op", false); err != nil {
		t.Fatal(err)
	}
}

// An ignored sender is dropped without a second card under unknown and
// under allow alike: the operator answered for them in particular.
// Un-ignoring hears them again -- a fresh card under unknown, as an
// outsider under allow -- as does mapping them directly.
func TestHearByIdentityIgnoredSender(t *testing.T) {
	ctx := context.Background()
	identityUsers := func(t *testing.T, r *depot) {
		t.Helper()
		if _, err := r.st.AddUser(ctx, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.AddUser(ctx, "ada", "op"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		mapTalk(t, r, "ada", "U012")
	}
	openCards := func(t *testing.T, r *depot) int {
		t.Helper()
		q, err := r.st.UserAsks(ctx, "op", "op")
		if err != nil {
			t.Fatal(err)
		}
		return len(q.Open)
	}

	// Under unknown an ignored sender offers no second card, and after
	// un-ignoring their next message offers a fresh one.
	r := identityDepot(t, "")
	identityUsers(t, r)
	r.now = time.Now()
	r.talk(t, "U999", "hello there")
	if n := openCards(t, r); n != 1 {
		t.Fatalf("open asks = %d, want the mapping card", n)
	}
	nobodyPick(t, r, "U999")
	r.now = r.now.Add(time.Second)
	r.talk(t, "U999", "again")
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("an ignored delivery recorded: %d hook events", n)
	}
	if n := openCards(t, r); n != 0 {
		t.Fatalf("open asks = %d, want no second card", n)
	}
	if err := r.st.UnignoreSource(ctx, "talk", "U999", "op"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Second)
	r.talk(t, "U999", "back again")
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("an un-ignored delivery recorded under unknown: %d hook events", n)
	}
	if n := openCards(t, r); n != 1 {
		t.Fatalf("open asks = %d, want a fresh card", n)
	}

	// Under allow an ignored sender is dropped, not heard as outside,
	// and after un-ignoring is heard as an outsider again.
	rr := identityDepot(t, store.HookUnknownAllow)
	identityUsers(t, rr)
	rr.now = time.Now()
	rr.talk(t, "U999", "outside words")
	if evs := rr.hookEvents(t); len(evs) != 1 || speakerOf(t, evs[0]) != store.SpeakerOutside {
		t.Fatalf("hook events = %v, want the delivery as outside", evs)
	}
	if n := openCards(t, rr); n != 0 {
		t.Fatalf("open asks = %d, want none under allow", n)
	}
	if created, err := rr.own.ClaimIdentity(ctx, "talk", "U999", "C1", "hi"); err != nil || !created {
		t.Fatalf("ClaimIdentity = %v, %v", created, err)
	}
	nobodyPick(t, rr, "U999")
	rr.now = rr.now.Add(time.Second)
	rr.talk(t, "U999", "ignored words")
	if n := len(rr.hookEvents(t)); n != 1 {
		t.Fatalf("an ignored delivery heard under allow: %d hook events", n)
	}
	if got := rr.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
	if err := rr.st.UnignoreSource(ctx, "talk", "U999", "op"); err != nil {
		t.Fatal(err)
	}
	rr.now = rr.now.Add(time.Second)
	rr.talk(t, "U999", "outside again")
	if evs := rr.hookEvents(t); len(evs) != 2 || speakerOf(t, evs[1]) != store.SpeakerOutside {
		t.Fatalf("hook events = %v, want the delivery as outside again", evs)
	}

	// Mapping an ignored sender directly clears the entry in the same
	// write: their next delivery is heard as theirs.
	ru := identityDepot(t, "")
	identityUsers(t, ru)
	ru.now = time.Now()
	ru.talk(t, "U777", "hello there")
	nobodyPick(t, ru, "U777")
	if err := ru.st.SetIdentity(ctx, "ada", "talk", store.Identity{ID: "U777"}, "op"); err != nil {
		t.Fatal(err)
	}
	ru.now = ru.now.Add(time.Second)
	ru.talk(t, "U777", "mapped now")
	if evs := ru.hookEvents(t); len(evs) != 1 || speakerOf(t, evs[0]) != "ada" {
		t.Fatalf("hook events = %v, want the mapped delivery as ada", evs)
	}
}

// duplicateUsers stands in for a store that already holds an id twice,
// as one made before the one-to-one write could: hearing reads it as
// unmapped until one identity is removed.
type duplicateUsers struct {
	hooks.Store
	users []store.User
}

func (d duplicateUsers) Users(context.Context) ([]store.User, error) { return d.users, nil }

func TestHearByIdentityHeldTwiceIsUnmapped(t *testing.T) {
	r := identityDepot(t, "")
	mapTalk(t, r, "ada", "U012")
	dup := duplicateUsers{Store: r.own, users: []store.User{
		{Name: "ada", Identities: map[string]store.Identity{"talk": {ID: "U012"}}},
		{Name: "bo", Identities: map[string]store.Identity{"talk": {ID: "U012"}}},
	}}
	rc2 := hooks.NewReceiver(dup, r.home, nil)
	rc2.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = rc2.Handler()
	r.rc = rc2
	r.now = time.Now()
	if code := r.talk(t, "U012", "ambiguous words"); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("an id held twice was heard: %d hook events", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
}

// A delivery whose sender is a crew member's identity in the source is
// the yard's own post coming back: dropped with no mapping card under
// every Unknown word, whether the echo carries the member id or only
// the bot id the key holds. The yard
// holds no ignore entry for either: the drop is the crew match, not
// the ignore list. A mapped member's delivery beside it is heard.
func TestHearByIdentityDropsCrewEcho(t *testing.T) {
	ctx := context.Background()
	for _, word := range []string{store.HookUnknownUnknown, store.HookUnknownAllow, store.HookUnknownIgnore} {
		r := identityDepot(t, word)
		if _, err := r.st.AddUser(ctx, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.AddUser(ctx, "ada", "op"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "op"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, "ann", "talk", store.Identity{ID: "U012", Key: "B345"}, "op"); err != nil {
			t.Fatal(err)
		}
		mapTalk(t, r, "ada", "U777")
		if src, err := r.st.GetSource(ctx, "talk"); err != nil || len(src.Ignored) != 0 {
			t.Fatalf("%s: ignored = %+v, %v, want no ignore entry", word, src, err)
		}
		r.now = time.Now()
		// A mapped member is heard, as ever.
		if code := r.talk(t, "U777", "a member's line"); code != http.StatusOK {
			t.Fatalf("%s: mapped answered %d", word, code)
		}
		// The yard's own posts, by the member id and by the bot id
		// alone, are dropped: answered 200, recorded nowhere.
		for _, sender := range []string{"U012", "b345"} {
			r.now = r.now.Add(time.Second)
			if code := r.talk(t, sender, "the yard's own echo"); code != http.StatusOK {
				t.Fatalf("%s: echo from %s answered %d", word, sender, code)
			}
		}
		evs := r.hookEvents(t)
		if len(evs) != 1 || speakerOf(t, evs[0]) != "ada" {
			t.Fatalf("%s: hook events = %v, want the member's delivery alone", word, evs)
		}
		if q, err := r.st.UserAsks(ctx, "op", "op"); err != nil || len(q.Open) != 0 {
			t.Fatalf("%s: open asks = %v, %v, want no mapping card", word, q, err)
		}
		if got := r.rc.Rejected()["self"]; got != 2 {
			t.Fatalf("%s: self count %d, want 2", word, got)
		}
		if got := r.rc.Rejected()["stranger"]; got != 0 {
			t.Fatalf("%s: stranger count %d, want 0", word, got)
		}
	}
}

// A user whose address cannot be read hears nothing: the delivery is
// dropped, never answered with an error the service would retry.
func TestHearUnreadableAddressDrops(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "")
	ctx := context.Background()
	if _, err := r.st.AddUser(ctx, "ghost", "me"); err != nil {
		t.Fatal(err)
	}
	// An env file that is not there: the store keeps the path and never
	// reads it, so this is set, not refused.
	if _, err := r.st.SetUser(ctx, "ghost", map[string]string{store.UserNotify: "chime",
		store.UserEnvFile: filepath.Join(t.TempDir(), "missing.env")}, "ghost"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	if code := r.chime(t, "333", "nobody reads this file"); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a delivery with no readable address recorded: %d hook events", n)
	}
}

// ---- hearing a crew member of a linked yard ------------------------

// peerTalkDepot is identityDepot with the operator op, the user ada mapped
// to U012 in talk, and the linked yard yardr-plantime, whose crew member
// the mayor is not yet mapped to any sender.
func peerTalkDepot(t *testing.T, unknown string) *depot {
	t.Helper()
	ctx := context.Background()
	r := identityDepot(t, unknown)
	for _, name := range []string{"op", "ada"} {
		if _, err := r.st.AddUser(ctx, name, "op"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	mapTalk(t, r, "ada", "U012")
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "plantime.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddPeer(ctx, store.Peer{Name: "yardr-plantime", Key: yardkey.Public(key)}, "op"); err != nil {
		t.Fatal(err)
	}
	return r
}

// mapMayor says that the sender id in talk is the mayor of yardr-plantime.
func mapMayor(t *testing.T, r *depot, id string) {
	t.Helper()
	if _, err := r.st.SetPeerIdentity(context.Background(), "yardr-plantime", store.PeerIdentity{Source: "talk", ID: id, Crew: "mayor"}, "op"); err != nil {
		t.Fatal(err)
	}
}

// A sender a link maps to a crew member of its yard is kept under the hook's
// default word and under the other two, because the mapping is the consent to
// hear them, as a user's is. The speaker is <crew>@<peer>, the member id stays
// as the actor, and no mapping card is offered. A user's deliveries beside it
// are heard as theirs, and a stranger's are dropped with a card as ever. A
// subscription filters on the speaker like any other.
func TestHearByIdentityPeersCrew(t *testing.T) {
	ctx := context.Background()
	for _, word := range []string{"", store.HookUnknownAllow, store.HookUnknownIgnore} {
		r := peerTalkDepot(t, word)
		mapMayor(t, r, "U777")
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "op"); err != nil {
			t.Fatal(err)
		}
		sub := store.Subscription{Source: "talk", Scope: store.ScopeAll, Where: map[string]string{store.SpeakerKey: "mayor@yardr-plantime"}}
		if err := r.st.AddSubscription(ctx, "ann", sub, "op"); err != nil {
			t.Fatal(err)
		}
		r.now = time.Now()
		for _, from := range []string{"U777", "U012", "U999"} {
			r.now = r.now.Add(time.Second)
			if code := r.talk(t, from, "words from "+from); code != http.StatusOK {
				t.Fatalf("%q: %s answered %d", word, from, code)
			}
		}
		evs := r.hookEvents(t)
		// The stranger is dropped under unknown and under ignore, and heard
		// as outside under allow.
		kept := 2
		if word == store.HookUnknownAllow {
			kept = 3
		}
		if len(evs) != kept {
			t.Fatalf("%q: %d hook events, want %d", word, len(evs), kept)
		}
		if got := speakerOf(t, evs[0]); got != "mayor@yardr-plantime" {
			t.Fatalf("%q: the mayor's speaker = %q, want mayor@yardr-plantime", word, got)
		}
		if actor, _ := evs[0].Data["actor"].(map[string]any); actor["id"] != "U777" {
			t.Fatalf("%q: actor = %v, want the member id left as it was", word, actor)
		}
		if got := speakerOf(t, evs[1]); got != "ada" {
			t.Fatalf("%q: the user's speaker = %q, want ada", word, got)
		}
		// Only the mayor's delivery reaches the subscription on the speaker.
		for i, e := range evs {
			deliveries, _ := e.Data["deliveries"].(map[string]any)
			if _, reached := deliveries["ann"]; reached != (i == 0) {
				t.Fatalf("%q: event %d (speaker %q) deliveries = %v", word, i, speakerOf(t, e), deliveries)
			}
		}
		// A mapped crew member is no stranger; the unmapped one is, except
		// under allow, which hears every sender.
		wantStrangers := 1
		if word == store.HookUnknownAllow {
			wantStrangers = 0
		}
		if got := r.rc.Rejected()["stranger"]; got != wantStrangers {
			t.Fatalf("%q: stranger count %d, want %d", word, got, wantStrangers)
		}
		// And no card for the mayor: under unknown the stranger's alone.
		q, err := r.st.UserAsks(ctx, "op", "op")
		if err != nil {
			t.Fatal(err)
		}
		wantCards := 0
		if word == "" {
			wantCards = 1
		}
		if len(q.Open) != wantCards {
			t.Fatalf("%q: %d open mapping cards, want %d", word, len(q.Open), wantCards)
		}
		for _, card := range q.Open {
			if card.Identity == nil || card.Identity.Sender != "U999" {
				t.Fatalf("%q: a card for %+v, want U999's alone", word, card.Identity)
			}
		}
	}
}

// Mapping an ignored sender to a crew member of a linked yard clears the
// entry in the same write, as mapping a user does: they are heard from their
// next delivery, and the ignore list no longer names them.
func TestHearByIdentityPeerMappingClearsIgnore(t *testing.T) {
	ctx := context.Background()
	r := peerTalkDepot(t, "")
	r.now = time.Now()
	r.talk(t, "U777", "hello there")
	nobodyPick(t, r, "U777")
	r.now = r.now.Add(time.Second)
	r.talk(t, "U777", "ignored words")
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("an ignored delivery recorded: %d hook events", n)
	}
	if src, err := r.st.GetSource(ctx, "talk"); err != nil || len(src.Ignored) != 1 {
		t.Fatalf("ignored = %+v, %v, want U777", src, err)
	}
	mapMayor(t, r, "U777")
	if src, err := r.st.GetSource(ctx, "talk"); err != nil || len(src.Ignored) != 0 {
		t.Fatalf("ignored after the mapping = %+v, %v, want none", src, err)
	}
	r.now = r.now.Add(time.Second)
	r.talk(t, "U777", "mapped now")
	if evs := r.hookEvents(t); len(evs) != 1 || speakerOf(t, evs[0]) != "mayor@yardr-plantime" {
		t.Fatalf("hook events = %v, want the mapped delivery as mayor@yardr-plantime", evs)
	}
}

// An id that a user and a crew member of a linked yard both hold names
// nobody, as an id two users hold does. The store refuses to write it, so
// this stands in for a store that held it already. The sender is unmapped, so
// a user's word never goes to someone a link also maps.
func TestHearByIdentityHeldByAUserAndAPeerIsUnmapped(t *testing.T) {
	r := peerTalkDepot(t, "")
	mapMayor(t, r, "U777")
	clash := duplicateUsers{Store: r.own, users: []store.User{
		{Name: "ada", Identities: map[string]store.Identity{"talk": {ID: "U777"}}},
	}}
	rc2 := hooks.NewReceiver(clash, r.home, nil)
	rc2.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = rc2.Handler()
	r.rc = rc2
	r.now = time.Now()
	if code := r.talk(t, "U777", "ambiguous words"); code != http.StatusOK {
		t.Fatalf("answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("an id held by a user and a peer's crew member was heard: %d hook events", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 1 {
		t.Fatalf("stranger count %d, want 1", got)
	}
}
