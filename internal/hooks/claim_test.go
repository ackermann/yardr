package hooks_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A fake delivery from an unknown chat is dropped as before, and one
// claim ask appears in the operator's queue carrying the chat;
// approving it writes CHIME_CHAT to the operator's env file (0600),
// and the next delivery from that chat is heard.
func TestClaimFromUnknownChatEndToEnd(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "operator")
	ctx := context.Background()
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	if code := r.chime(t, "999", "hi"); code != http.StatusOK {
		t.Fatalf("an unknown chat answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a dropped delivery recorded: %d hook events", n)
	}
	q, err := r.st.UserAsks(ctx, "op", "op")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 {
		t.Fatalf("open asks = %d, want the one claim", len(q.Open))
	}
	claim := q.Open[0]
	if claim.Run == nil || claim.Run.Command != "yardr user key op chime" ||
		!strings.Contains(strings.Join(claim.Run.Does, " "), "CHIME_CHAT=999") {
		t.Fatalf("the claim = %+v", claim.Run)
	}
	if !strings.Contains(claim.Question, "A message reached chime from chat 999 (first words: 'hi'). Is this you?") {
		t.Fatalf("the question = %q", claim.Question)
	}
	// A second unknown delivery offers no second claim while one waits.
	r.now = r.now.Add(time.Second)
	if code := r.chime(t, "888", "hi"); code != http.StatusOK {
		t.Fatalf("a second unknown chat answered %d", code)
	}
	if q, err := r.st.UserAsks(ctx, "op", "op"); err != nil || len(q.Open) != 1 {
		t.Fatalf("open asks = %v, %v, want 1", q, err)
	}
	done, err := r.st.ApproveAsk(ctx, claim.Bead, claim.Ask, "op", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "user op: CHIME_CHAT is set" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	u, err := r.st.GetUser(ctx, "op")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(u.EnvFile)
	if err != nil || string(b) != "CHIME_CHAT=999\n" {
		t.Fatalf("the env file holds %q, %v", b, err)
	}
	if st, err := os.Stat(u.EnvFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the env file's mode = %v, %v", st.Mode(), err)
	}
	// The next delivery from that chat is heard.
	r.now = r.now.Add(time.Second)
	if code := r.chime(t, "999", "hi"); code != http.StatusOK {
		t.Fatalf("the claimed chat answered %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event after the claim, got %d", n)
	}
}

// Declining a claim writes nothing, and no claim is offered while the
// operator has an address in the source already.
func TestClaimDeclinedAndAlreadyAddressed(t *testing.T) {
	r := hearDepot(t, chimeNormalize, "operator")
	ctx := context.Background()
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	if code := r.chime(t, "999", "hi"); code != http.StatusOK {
		t.Fatalf("an unknown chat answered %d", code)
	}
	q, err := r.st.UserAsks(ctx, "op", "op")
	if err != nil || len(q.Open) != 1 {
		t.Fatalf("open asks = %v, %v", q, err)
	}
	claim := q.Open[0]
	if err := r.st.AddAnswer(ctx, claim.Bead, store.Answer{Ask: claim.Ask, Decline: true, Words: "not me"}, "op"); err != nil {
		t.Fatal(err)
	}
	if u, err := r.st.GetUser(ctx, "op"); err != nil || u.EnvFile != "" {
		t.Fatalf("op after a decline = %+v, %v: want no env_file", u, err)
	}
	// With an address set, a stranger's delivery offers no claim.
	tellChime(t, r, "op", "111")
	r.now = r.now.Add(time.Second)
	if code := r.chime(t, "999", "hi"); code != http.StatusOK {
		t.Fatalf("a stranger's chat answered %d", code)
	}
	if q, err := r.st.UserAsks(ctx, "op", "op"); err != nil || len(q.Open) != 0 {
		t.Fatalf("open asks = %v, %v, want none", q, err)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a dropped delivery recorded: %d hook events", n)
	}
}
