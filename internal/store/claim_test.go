package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claimFile is a source that tells a user and hears them back: notify
// through message with the chat under CHIME_CHAT, a secret-verified
// hook with keys [chat], and hear on chat from the operator.
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
	if open.Run == nil || open.Run.Command != "yardr user key mckean chime" || open.Run.Value != "123456789" {
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
