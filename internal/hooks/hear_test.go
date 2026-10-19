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
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.home = home
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	text := fmt.Sprintf(talkText, "normalize")
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
	r := identityDepot(t, "")
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
	q, err := r.st.UserAsks(ctx, "op")
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
	if q, err := r.st.UserAsks(ctx, "op"); err != nil || len(q.Open) != 2 {
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
	if err := r.st.AnswerAsk(ctx, card.Bead, card.Ask, pick, "", "op"); err != nil {
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
		if q, err := rr.st.UserAsks(ctx, "op"); err != nil || len(q.Open) != 0 {
			t.Fatalf("%s: open asks = %v, %v, want none", word, q, err)
		}
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
