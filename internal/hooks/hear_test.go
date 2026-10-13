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
// hears; "" hears every user told through it.
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
// recorded nowhere, counted beside refusals. A user told through the
// source is still told, but their chat is dropped like a stranger's.
func TestHearFromOperatorKeepsToldDropsStranger(t *testing.T) {
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
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "chime", Scope: store.ScopeAll}, "me"); err != nil {
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
	// Another told user's chat is dropped like a stranger's, and a
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

// Without from every user told through the source is heard: another told
// user's chat is recorded, and only a stranger's is dropped.
func TestHearWithoutFromHearsEveryToldUser(t *testing.T) {
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
	r.now = time.Now()
	if code := r.chime(t, "222", "zed's line"); code != http.StatusOK {
		t.Fatalf("another told user's chat answered %d", code)
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
// of it can be a told address.
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

// A user told through the source whose address cannot be read hears
// nothing: the delivery is dropped, never answered with an error the
// service would retry.
func TestHearUnreadableAddressDrops(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "")
	ctx := context.Background()
	if _, err := r.st.AddUser(ctx, "ghost", "me"); err != nil {
		t.Fatal(err)
	}
	// Told through chime at a file that is not there: the store keeps the
	// path and never reads it, so this is set, not refused.
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
