package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// pushTestYard is a yard with the operator jo, jo's device phone, and the
// yard's key, connected to a stand-in edge with the link kept and the
// sender running.
type pushTestYard struct {
	st    *store.Store
	edge  *standin.Edge
	link  *standin.EdgeLink
	token string
	db    string
	log   *bytes.Buffer
}

func pushYard(t *testing.T) *pushTestYard {
	t.Helper()
	edge, srv := stubEdge(t)
	c := home(t)
	ctx := context.Background()
	token, _ := edgeYard(t, c, edge, srv, true)
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	pushTick = 20 * time.Millisecond
	t.Cleanup(func() { pushTick = time.Second })
	var buf bytes.Buffer
	stopEdge := KeepEdge(ctx, c, st, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}), slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(stopEdge)
	return &pushTestYard{st: st, edge: edge, link: nextLink(t, edge), token: token, db: c.DBPath(), log: &buf}
}

// nextPush is the next footplate frame on the link, waiting for it.
func nextPush(t *testing.T, l *standin.EdgeLink) standin.EdgePush {
	t.Helper()
	select {
	case p := <-l.PushEvents():
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("no push frame on the link")
		return standin.EdgePush{}
	}
}

// footGone waits until no row is due to be reported to the relay.
func footGone(t *testing.T, st *store.Store) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		rows, err := st.FootDue(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			return
		}
	}
	t.Fatal("rows still due")
}

// pushShape holds the frame to the relay's shape: only the fields the
// link contract carries, a random id, and never a token or platform.
func pushShape(t *testing.T, p standin.EdgePush, wantType, user, device, kind string) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(p.Raw, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"type": true, "event_id": true, "user": true, "device": true}
	if wantType == edgelink.PushIntent {
		want["kind"] = true
	}
	for name := range fields {
		if !want[name] {
			t.Fatalf("frame carries %q: %s", name, p.Raw)
		}
	}
	for name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("frame lacks %q: %s", name, p.Raw)
		}
	}
	if p.Type != wantType || p.User != user || p.Device != device || p.Kind != kind {
		t.Fatalf("frame = %+v", p)
	}
	if len(p.EventID) != 32 {
		t.Fatalf("event id %q is not 128 bits hex", p.EventID)
	}
	if _, err := hex.DecodeString(p.EventID); err != nil {
		t.Fatalf("event id %q is not hex", p.EventID)
	}
	if strings.Contains(string(p.Raw), "token") || strings.Contains(string(p.Raw), "platform") {
		t.Fatalf("frame holds a token or platform: %s", p.Raw)
	}
}

func ackPush(t *testing.T, l *standin.EdgeLink, eventID, refused string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.AckPush(ctx, eventID, refused); err != nil {
		t.Fatal(err)
	}
}

// Mail to the user and a newly open ask each make an intent per device of
// the user: three letters in one burst window make three frames with three
// ids, which the relay coalesces. Acked, the rows go, and the log holds
// no token.
func TestPushSenderReportsIntents(t *testing.T) {
	y := pushYard(t)
	ctx := context.Background()
	if _, err := y.st.AddDevice(ctx, "jo", "tablet", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.Mail(ctx, "jo", "the tests are green", "hal"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 2; i++ {
		p := nextPush(t, y.link)
		pushShape(t, p, edgelink.PushIntent, "jo", p.Device, "mail")
		ids = append(ids, p.EventID)
	}
	for _, text := range []string{"one", "two", "three"} {
		if err := y.st.Mail(ctx, "jo", text, "hal"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		p := nextPush(t, y.link)
		pushShape(t, p, edgelink.PushIntent, "jo", p.Device, "mail")
		ids = append(ids, p.EventID)
	}
	b, err := y.st.CreateBead(ctx, store.NewBead{Title: "Move the links"}, "hal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddAsk(ctx, b.ID, store.Ask{
		Question: "Move the links now?",
		To:       "jo",
		Context:  []string{"The move was tested on a copy and passed."},
	}, "hal"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		p := nextPush(t, y.link)
		pushShape(t, p, edgelink.PushIntent, "jo", p.Device, "decision")
		ids = append(ids, p.EventID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("event id %q twice", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		ackPush(t, y.link, id, "")
	}
	footGone(t, y.st)
	if strings.Contains(y.log.String(), y.token) {
		t.Fatalf("the log holds the device token:\n%s", y.log.String())
	}
}

// Intents queued while the link is down land with the same ids when it
// returns: the relay tells the retries from new events.
func TestPushSenderKeepsIdsAcrossLinkDown(t *testing.T) {
	y := pushYard(t)
	ctx := context.Background()
	if _, err := y.st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.Mail(ctx, "jo", "the tests are green", "hal"); err != nil {
		t.Fatal(err)
	}
	first := nextPush(t, y.link)
	pushShape(t, first, edgelink.PushIntent, "jo", "phone", "mail")
	// The link dies before the ack: the stand-in holds the frame and
	// sends nothing.
	y.link.Close()
	again := nextLink(t, y.edge)
	second := nextPush(t, again)
	if second.EventID != first.EventID {
		t.Fatalf("after the link returned: %s, want %s", second.EventID, first.EventID)
	}
	ackPush(t, again, second.EventID, "")
	footGone(t, y.st)
}

// The relay's answer ends the row: refused for good, it is dropped and
// never sent again; asked for again, it is tried again with the same id.
func TestPushSenderTakesTheRelaysAnswer(t *testing.T) {
	y := pushYard(t)
	ctx := context.Background()
	if _, err := y.st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.Mail(ctx, "jo", "first", "hal"); err != nil {
		t.Fatal(err)
	}
	first := nextPush(t, y.link)
	ackPush(t, y.link, first.EventID, "no registered device under this yard for that user and device")
	footGone(t, y.st)
	if got := len(y.link.Pushes()); got != 1 {
		t.Fatalf("the refused row was sent again: %d frames", got)
	}

	if err := y.st.Mail(ctx, "jo", "second", "hal"); err != nil {
		t.Fatal(err)
	}
	second := nextPush(t, y.link)
	ackPush(t, y.link, second.EventID, "the relay did not take the intent; send it again")
	third := nextPush(t, y.link)
	if third.EventID != second.EventID {
		t.Fatalf("the retry has id %s, want %s", third.EventID, second.EventID)
	}
	ackPush(t, y.link, third.EventID, "")
	footGone(t, y.st)

	// A refusal comes from the edge. Even if its text contains something
	// shaped like a push token, it is neither stored nor logged; only the
	// retry decision uses that text.
	if err := y.st.Mail(ctx, "jo", "third", "hal"); err != nil {
		t.Fatal(err)
	}
	fourth := nextPush(t, y.link)
	secret := "FCM-token-that-must-not-escape"
	retryText := "the relay did not take the intent; " + secret + " send it again"
	if !pushRetryable(retryText) {
		t.Fatal("the retry decision no longer recognizes the relay's retry wording")
	}
	var ackLog bytes.Buffer
	sender := &pushSender{st: y.st, log: slog.New(slog.NewTextHandler(&ackLog, nil))}
	sender.handleAck(edgelink.PushAck{EventID: fourth.EventID, Refused: retryText})
	db, err := os.ReadFile(y.db)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), secret) || strings.Contains(ackLog.String(), secret) {
		t.Fatalf("the relay's refusal reached the yard store or log: %s", secret)
	}
	ackPush(t, y.link, fourth.EventID, "")
	footGone(t, y.st)

	// An unmatched ack is also edge text. Neither its id nor its refusal is
	// useful in the yard log.
	sender.handleAck(edgelink.PushAck{EventID: secret, Refused: "permanent " + secret})
	if strings.Contains(ackLog.String(), secret) || !strings.Contains(ackLog.String(), "the relay refused a footplate row; dropped") {
		t.Fatalf("an ack's id or refusal reached the log, or its handling was not recorded: %s", ackLog.String())
	}
}

// Removing a device reports a removal naming only that user and device;
// removing a user reports one per device, and their queued intents go
// with them.
func TestPushSenderReportsRemovals(t *testing.T) {
	y := pushYard(t)
	ctx := context.Background()
	if _, err := y.st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	p := nextPush(t, y.link)
	pushShape(t, p, edgelink.PushRemove, "jo", "phone", "")
	ackPush(t, y.link, p.EventID, "")
	footGone(t, y.st)

	if _, err := y.st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"phone", "tablet"} {
		if _, err := y.st.AddDevice(ctx, "ada", name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := y.st.Mail(ctx, "ada", "hi ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		m := nextPush(t, y.link)
		pushShape(t, m, edgelink.PushIntent, "ada", m.Device, "mail")
	}
	if err := y.st.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 2; i++ {
		r := nextPush(t, y.link)
		pushShape(t, r, edgelink.PushRemove, "ada", r.Device, "")
		ids = append(ids, r.EventID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("both removals carry %q", ids[0])
	}
	for _, id := range ids {
		ackPush(t, y.link, id, "")
	}
	footGone(t, y.st)
}
