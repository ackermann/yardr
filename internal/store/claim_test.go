package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claimFile is a source that tells a user and hears them back: notify
// through message with the chat under CHIME_CHAT, a secret-verified
// hook with keys [chat], and hear on chat from the operator, with the
// filter a claim approval subscribes the hearer with beside the address.
const claimFile = `name: chime
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
  field: chat
  address: { env: CHIME_CHAT }
  from: operator
  subscribe: { type: message }
`

// claimYard is a secret yard with the source chime declared.
func claimYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := secretYard(t)
	if _, err := y.PutSource(y.ctx, "chime.yaml", claimFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func claimQueue(t *testing.T, y *askYard) *AskQueue {
	t.Helper()
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// A claim from an unknown address stands in the operator's queue as a
// user key run ask carrying the address; approving it writes the
// address into the operator's env file (0600) exactly where yardr
// user key writes it, and no second claim waits while one does or
// once the address is set. The card keeps at most 80 characters of
// the message.
func TestClaimAddressApproveWritesEnvFile(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	long := strings.Repeat("word ", 30) + "TAIL-MARKER-must-not-be-kept"
	created, err := y.ClaimAddress(y.ctx, "chime", "123456789", long)
	if err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	q := claimQueue(t, y)
	if len(q.Open) != 1 {
		t.Fatalf("open asks = %d, want 1", len(q.Open))
	}
	open := q.Open[0]
	if open.Run == nil || open.Run.Command != "yardr user key mckean chime" {
		t.Fatalf("the claim's run = %+v", open.Run)
	}
	if !strings.Contains(open.Question, "A message reached chime from chat 123456789 (first words: '") ||
		!strings.HasSuffix(open.Question, "'). Is this you?") {
		t.Fatalf("the question = %q", open.Question)
	}
	if strings.Contains(open.Question, "TAIL-MARKER") || strings.Contains(strings.Join(open.Run.Does, " "), "TAIL-MARKER") {
		t.Fatalf("the card keeps more than the first words: %q %+v", open.Question, open.Run.Does)
	}
	if !strings.Contains(strings.Join(open.Run.Does, " "), "CHIME_CHAT=123456789") {
		t.Fatalf("the card does not show the address: %v", open.Run.Does)
	}
	// One pending claim per source and operator.
	if created, err := y.ClaimAddress(y.ctx, "chime", "999", "another stranger"); err != nil || created {
		t.Fatalf("a second claim = %v, %v", created, err)
	}
	if q := claimQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after a second delivery = %d, want 1", len(q.Open))
	}
	// Only the operator approves their own claim.
	if _, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "ada"); err == nil {
		t.Fatal("ada approved the operator's claim")
	}
	done, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	u, err := y.GetUser(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	wantFile := filepath.Join(y.home, "users", "mckean.env")
	if u.EnvFile != wantFile {
		t.Fatalf("env_file = %q, want %q", u.EnvFile, wantFile)
	}
	if b, _ := os.ReadFile(wantFile); string(b) != "CHIME_CHAT=123456789\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(wantFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode = %v, %v", st.Mode(), err)
	}
	// Once the address is set, no claim is offered.
	if created, err := y.ClaimAddress(y.ctx, "chime", "555", "again"); err != nil || created {
		t.Fatalf("a claim with the address set = %v, %v", created, err)
	}
}

// Declining a claim writes nothing, and a later delivery may offer a
// claim again. A source with no hear key offers none.
func TestClaimAddressDeclineAndNoAskCases(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	if created, err := y.ClaimAddress(y.ctx, "crm", "111", "hi"); err != nil || created {
		t.Fatalf("a claim for a source with no hear key = %v, %v", created, err)
	}
	if created, err := y.ClaimAddress(y.ctx, "nowhere", "111", "hi"); err != nil || created {
		t.Fatalf("a claim for no source = %v, %v", created, err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "", "hi"); err != nil || created {
		t.Fatalf("a claim with no address = %v, %v", created, err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "111", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	if err := y.AddAnswer(y.ctx, open.Bead, Answer{Ask: open.Ask, Decline: true, Words: "not me"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if u.EnvFile != "" {
		if _, err := os.Lstat(u.EnvFile); !os.IsNotExist(err) {
			t.Fatalf("a declined claim wrote %s: %v", u.EnvFile, err)
		}
	}
	if q := claimQueue(t, y); len(q.Open) != 0 {
		t.Fatalf("open asks after a decline = %d, want 0", len(q.Open))
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "222", "me again"); err != nil || !created {
		t.Fatalf("a claim after a decline = %v, %v", created, err)
	}
}

// An address given by hand after the claim was written makes the
// approval stale: it writes nothing, rather than overwriting the
// newer address with the claimed one.
func TestClaimApprovalStaleWhenAddressGivenSince(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	if created, err := y.ClaimAddress(y.ctx, "chime", "111", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	file := filepath.Join(t.TempDir(), "mckean.env")
	if err := os.WriteFile(file, []byte("CHIME_CHAT=222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserEnvFile: file}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean"); err == nil || !strings.Contains(err.Error(), "one is set now") {
		t.Fatalf("ApproveAsk = %v, want a stale refusal", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "CHIME_CHAT=222\n" {
		t.Fatalf("the file holds %q after a stale approval", b)
	}
}

// A malformed env file cannot be read to tell whether the operator's
// address is already set. Do not offer a claim that could overwrite it.
func TestClaimAddressRefusedWhenEnvFileCannotBeRead(t *testing.T) {
	y := claimYard(t)
	file := filepath.Join(y.home, "users", "mckean.env")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	want := "not-a-key\nCHIME_CHAT=222\n"
	if err := os.WriteFile(file, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "111", "hi"); err == nil || created {
		t.Fatalf("ClaimAddress = %v, %v; want a read error and no claim", created, err)
	}
	if q := claimQueue(t, y); len(q.Open) != 0 {
		t.Fatalf("open asks = %d, want none", len(q.Open))
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != want {
		t.Fatalf("the env file holds %q, %v; want it unchanged", got, err)
	}
}

// Text from outside the yard is data for a terminal to show, never
// something for it to act on: a claim whose address holds a control
// character is offered not at all, and one whose message holds them
// shows the message with them dropped.
func TestClaimAddressDropsControlCharacters(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	if created, err := y.ClaimAddress(y.ctx, "chime", "123\x1b[31m", "hi"); err != nil || created {
		t.Fatalf("a claim for an address with a control character = %v, %v", created, err)
	}
	for _, address := range []string{" 123", "123 ", "\u00a0123"} {
		if created, err := y.ClaimAddress(y.ctx, "chime", address, "hi"); err != nil || created {
			t.Fatalf("a claim for a padded address %q = %v, %v", address, created, err)
		}
	}
	if q := claimQueue(t, y); len(q.Open) != 0 {
		t.Fatalf("open asks = %d, want none", len(q.Open))
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "123", "hi\x1b[31m there \u202enow"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	if strings.ContainsAny(open.Question, "\x1b\x7f") || strings.ContainsRune(open.Question, '\u202e') ||
		!strings.Contains(open.Question, "hi[31m there now") {
		t.Fatalf("the question = %q, want the message with controls dropped", open.Question)
	}
}

// A typed --run "user key ..." carries no address and is refused: the
// value at a terminal is typed unseen, and a card carries only an
// address a delivery brought.
func TestUserKeyRunAskWithoutAddressRefused(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	ask := Ask{Question: "Give mckean their chime address?", To: "mckean",
		Context: []string{"a delivery brought it"},
		Run:     &AskRun{Argv: []string{"user key mckean chime"}}}
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "carries no address") {
		t.Fatalf("AddAsk = %v, want a carries-no-address refusal", err)
	}
}

// AddAsk is also a local API, so it must not let a caller invent the
// address that only a dropped hook delivery may carry on the claim card.
func TestUserKeyRunAskCannotSupplyAddress(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	ask := Ask{Question: "A message came from this address; is it yours?", To: "mckean",
		Context: []string{"a delivery brought it"},
		Run:     &AskRun{Argv: []string{"user key mckean chime"}, Value: "attacker-chat"}}
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "only from a dropped delivery") {
		t.Fatalf("AddAsk = %v, want an address-origin refusal", err)
	}
	if q := claimQueue(t, y); len(q.Open) != 0 {
		t.Fatalf("open asks = %d, want no forged claim", len(q.Open))
	}
}

// An approved claim subscribes the yard's hearer to the claimed address
// beside writing it, so no terminal step is left after the claim: scope
// all, the hear field with the address, and the filter the declaration's
// hear key names beside it. The card says by name who will hear the chat,
// and the approval carries the subscription on its answer and its event.
func TestClaimApprovalSubscribesHearer(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	y.crew(t, map[string]any{"kind": "claude"})
	if err := y.SetSetting(y.ctx, HearerSetting, "yardmaster", "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "123456789", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	const sub = "chime all chat=123456789 type=message"
	want := "subscribes yardmaster to " + sub + " (kept once if yardmaster has it already), " +
		"so yardmaster hears this chat; taken back with yardr unsubscribe yardmaster chime --scope all --where chat=123456789 --where type=message."
	if len(open.Run.Does) != 2 || open.Run.Does[1] != want {
		t.Fatalf("the card says %q", open.Run.Does)
	}
	done, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set; yardmaster subscribed: "+sub {
		t.Fatalf("the approval printed %q", done.Result)
	}
	r, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != sub {
		t.Fatalf("yardmaster subscribes to %v", r.Subscriptions)
	}
	evs, _ := y.EventsOfKind(y.ctx, "subscribed", 10)
	if len(evs) != 1 || evs[0].Actor != "mckean" || evs[0].Data["crew"] != "yardmaster" || evs[0].Data["subscription"] != sub {
		t.Fatalf("subscribed events = %+v", evs)
	}
	got, _ := json.Marshal(evs[0].Data["approved"])
	wantApproved, _ := json.Marshal(map[string]any{"bead": open.Bead, "ask": open.Ask})
	if string(got) != string(wantApproved) {
		t.Fatalf("the event carries approved %s, want %s", got, wantApproved)
	}
}

// With no hearer set, or one naming no crew member, an approved claim
// writes the address alone, as before: the card offers no subscription.
func TestClaimWithoutHearerWritesAddressAlone(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	if created, err := y.ClaimAddress(y.ctx, "chime", "111", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	if len(open.Run.Does) != 1 {
		t.Fatalf("the card says %q", open.Run.Does)
	}
	done, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set" {
		t.Fatalf("the approval printed %q", done.Result)
	}

	other := claimYard(t)
	if err := other.SetSetting(other.ctx, HearerSetting, "nobody", "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := other.ClaimAddress(other.ctx, "chime", "222", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open = claimQueue(t, other).Open[0]
	if len(open.Run.Does) != 1 {
		t.Fatalf("the card says %q", open.Run.Does)
	}
	done, err = other.ApproveAsk(other.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set" {
		t.Fatalf("the approval printed %q", done.Result)
	}
}

// A hearer set since the claim card was written makes the approval
// stale: another crew member would hear the operator's chat than the
// one the card names. Nothing is written and the ask stays open.
func TestClaimApprovalStaleWhenHearerSetSince(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	y.crew(t, map[string]any{"kind": "claude"})
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(y.ctx, HearerSetting, "yardmaster", "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "111", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	if err := y.SetSetting(y.ctx, HearerSetting, "clerk", "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err == nil || !strings.Contains(err.Error(), "subscribes clerk to chime all chat=111 type=message") || !strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("ApproveAsk = %v", err)
	}
	if q := claimQueue(t, y); len(q.Open) != 1 {
		t.Fatalf("open asks after a refused approval = %d, want 1", len(q.Open))
	}
	u, err := y.GetUser(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if u.EnvFile != "" {
		if _, err := os.Lstat(u.EnvFile); !os.IsNotExist(err) {
			t.Fatalf("a stale approval wrote %s: %v", u.EnvFile, err)
		}
	}
	c, err := y.GetCrew(y.ctx, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Subscriptions) != 0 {
		t.Fatalf("clerk subscribes to %v", c.Subscriptions)
	}
}

// A hearer subscribed to the claimed chat already keeps the subscription
// once: the approval still writes the address, and says so.
func TestClaimApprovalKeepsHearerSubscriptionOnce(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := claimYard(t)
	y.crew(t, map[string]any{"kind": "claude"})
	if err := y.SetSetting(y.ctx, HearerSetting, "yardmaster", "mckean"); err != nil {
		t.Fatal(err)
	}
	const sub = "chime all chat=123456789 type=message"
	if err := y.AddSubscription(y.ctx, "yardmaster", Subscription{Source: "chime", Scope: ScopeAll,
		Where: map[string]string{"chat": "123456789", "type": "message"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "123456789", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	done, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set; yardmaster already subscribed: "+sub {
		t.Fatalf("the approval printed %q", done.Result)
	}
	r, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != sub {
		t.Fatalf("yardmaster subscribes to %v", r.Subscriptions)
	}
}

// A source whose hear key names no filter subscribes the hearer to the
// address alone: the subscription is scope all on the hear field.
func TestClaimApprovalSubscribesHearerToAddressAlone(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	plain := strings.Replace(claimFile, "  subscribe: { type: message }\n", "", 1)
	if _, err := y.PutSource(y.ctx, "chime.yaml", plain, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.crew(t, map[string]any{"kind": "claude"})
	if err := y.SetSetting(y.ctx, HearerSetting, "yardmaster", "mckean"); err != nil {
		t.Fatal(err)
	}
	if created, err := y.ClaimAddress(y.ctx, "chime", "555", "hi"); err != nil || !created {
		t.Fatalf("ClaimAddress = %v, %v", created, err)
	}
	open := claimQueue(t, y).Open[0]
	const sub = "chime all chat=555"
	if len(open.Run.Does) != 2 || !strings.Contains(open.Run.Does[1], "subscribes yardmaster to "+sub+" ") {
		t.Fatalf("the card says %q", open.Run.Does)
	}
	done, err := y.ApproveAsk(y.ctx, open.Bead, open.Ask, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user mckean: CHIME_CHAT is set; yardmaster subscribed: "+sub {
		t.Fatalf("the approval printed %q", done.Result)
	}
}
