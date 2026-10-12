package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// askBody is the body of a message of kind ask, as the yard queues it.
type askBody struct {
	V      int            `json:"v"`
	To     string         `json:"to"`
	From   string         `json:"from"`
	Bead   string         `json:"bead"`
	Title  string         `json:"title"`
	Topic  string         `json:"topic"`
	Stage  string         `json:"stage"`
	Labels []string       `json:"labels"`
	Body   string         `json:"body"`
	Seq    uint64         `json:"seq"`
	Since  time.Time      `json:"since"`
	Ask    store.Ask      `json:"ask"`
	About  []askAboutBead `json:"about"`
}

type askAboutBead struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

func crossAsk() store.Ask {
	return store.Ask{
		Question: "Move the links now?",
		To:       "mckean@north",
		Context: []string{
			"64 links still point at the old source.",
			"The move was tested on a copy and passed.",
		},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all 64 links move tonight"},
			{Label: "After Friday", Consequence: "keep the old source"},
		},
		Recommend: 1,
		Why:       "the test passed",
	}
}

// An ask addressed to a person of a linked yard is stored on the bead and
// queued for the peer as one message of kind ask, in the same write: the
// bead's handle, title, topic and stage, its body cut at 16 KB saying so,
// the ask's seq, asker and since, the ask's JSON whole, and each
// about-bead as id, title and stage.
func TestAskPeer(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "north")
	y.member(t, "clerk", "")
	if err := y.api.SetSetting(y.ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "the work", Body: strings.Repeat("body ", 4<<10),
		Stage: "build", Labels: []string{"topic:links", "muse"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	other, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "the other work", Stage: "backlog"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	notes := func() int {
		t.Helper()
		ns, err := y.api.Notes(y.ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(ns)
	}

	// The link carries nothing yet: refused, naming the allow, before
	// anything is stored: no note, and no refusal logged either.
	ask := crossAsk()
	ask.About = []string{other.ID}
	if _, err := y.api.AddAsk(y.ctx, b.ID, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "does not carry ask out") ||
		!strings.Contains(err.Error(), "yardr peer allow north --send ask") {
		t.Fatalf("an ask over a link that carries none = %v", err)
	}
	if n := notes(); n != 0 {
		t.Fatalf("a refused ask stored %d notes", n)
	}
	if ref := peerEvents(t, y.api, "peer_message_refused"); len(ref) != 0 {
		t.Fatalf("a refused ask logged %+v", ref)
	}
	y.allow(t, store.PeerAllow{Send: []string{"ask"}})

	// A crew member without the peer in its setting is refused, the link's
	// permission or not, and stores nothing.
	if _, err := y.api.AddAsk(y.ctx, b.ID, ask, "clerk"); err == nil || !strings.Contains(err.Error(), "crew member clerk may not ask mckean@north") ||
		!strings.Contains(err.Error(), "yardr crew set clerk peers=north") {
		t.Fatalf("an ask from a crew member without peers = %v", err)
	}
	if n := notes(); n != 0 {
		t.Fatalf("a refused ask stored %d notes", n)
	}
	// No such peer, and a bead's agent, are refused before storing too.
	askNobody := crossAsk()
	askNobody.To = "mckean@nobody"
	if _, err := y.api.AddAsk(y.ctx, b.ID, askNobody, "yardmaster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an ask to no peer = %v", err)
	}
	if _, err := y.api.AddAsk(y.ctx, b.ID, ask, "yard-10-new"); err == nil ||
		!strings.Contains(err.Error(), "or a user of the yard (yardr user list) may ask another yard; this is yard-10-new") {
		t.Fatalf("an ask across as a bead's agent = %v", err)
	}
	if n := notes(); n != 0 {
		t.Fatalf("a refused ask stored %d notes", n)
	}
	for to, want := range map[string]string{
		"may or@north": "that is no person's name",
		"@north":       "that is no person's name",
	} {
		bad := crossAsk()
		bad.To = to
		if _, err := y.api.AddAsk(y.ctx, b.ID, bad, "yardmaster"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AddAsk to %q = %v, want %q", to, err, want)
		}
	}
	if _, err := y.api.AddAsk(y.ctx, b.ID, ask, "yard master"); err == nil || !strings.Contains(err.Error(), "is what another yard is told") {
		t.Errorf("an ask from %q = %v", "yard master", err)
	}
	// PeerSend is no way around the gates: an ask is the yard's own to
	// queue, from its own asks.
	if _, err := y.api.PeerSend(y.ctx, "north", "ask", json.RawMessage(`{}`), "yardmaster"); err == nil || !strings.Contains(err.Error(), "yardr bead ask") {
		t.Fatalf("PeerSend of kind ask = %v", err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 {
		t.Fatalf("refused asks wait: %+v", p)
	}

	// The crew member asks, and the operator, who needs the link's
	// permission only.
	seq, err := y.api.AddAsk(y.ctx, b.ID, ask, "yardmaster")
	if err != nil {
		t.Fatalf("an ask across = %v", err)
	}
	plain := crossAsk()
	plain.About = nil
	if _, err := y.api.AddAsk(y.ctx, b.ID, plain, "jo"); err != nil {
		t.Fatalf("the operator's ask across = %v", err)
	}
	out, _ := y.api.PeerOutbox(y.ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "ask" || out[1].Kind != "ask" {
		t.Fatalf("the outbox = %+v", out)
	}
	var body askBody
	if err := json.Unmarshal(out[0].Body, &body); err != nil {
		t.Fatalf("the queued body is no ask: %v", err)
	}
	ns, _ := y.api.Notes(y.ctx, b.ID)
	if len(ns) != 2 || ns[0].Kind != "ask" || ns[0].Seq != seq {
		t.Fatalf("the bead's notes = %+v", ns)
	}
	var stored store.Ask
	if err := json.Unmarshal([]byte(ns[0].Text), &stored); err != nil {
		t.Fatal(err)
	}
	switch {
	case body.V != store.PeerAskVersion:
		t.Errorf("body v = %d", body.V)
	case body.To != "mckean" || body.From != "yardmaster":
		t.Errorf("body to/from = %q/%q", body.To, body.From)
	case body.Bead != b.ID || body.Title != "the work" || body.Topic != "topic:links" || body.Stage != "build":
		t.Errorf("body bead = %+v", body)
	case len(body.Labels) != 2 || body.Labels[0] != "muse" || body.Labels[1] != "topic:links":
		t.Errorf("body labels = %q", body.Labels)
	case len(body.Body) > 16<<10 || !strings.HasSuffix(body.Body, "\n[cut: 20480 bytes in all]"):
		t.Errorf("body body of %d bytes is cut: %.40q...", len(body.Body), body.Body[len(body.Body)-40:])
	case body.Seq != seq || !body.Since.Equal(ns[0].At):
		t.Errorf("body seq/since = %d/%v, ask note %d/%v", body.Seq, body.Since, ns[0].Seq, ns[0].At)
	case body.Ask.Question != stored.Question || body.Ask.To != "mckean@north" || len(body.Ask.Options) != len(stored.Options) ||
		body.Ask.Options[2].Label != store.YouDecide || len(body.Ask.About) != 1 || body.Ask.About[0] != other.ID:
		t.Errorf("body ask = %+v", body.Ask)
	case len(body.About) != 1 || body.About[0].ID != other.ID || body.About[0].Title != "the other work" || body.About[0].Stage != "backlog":
		t.Errorf("body about = %+v", body.About)
	}
	if stored.To != "mckean@north" {
		t.Errorf("the stored ask is to %q", stored.To)
	}

	// An ask without a peer queues nothing.
	if _, err := y.api.AddAsk(y.ctx, b.ID, store.Ask{Question: "Local?", To: "mckean", Context: []string{"here."}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != 2 {
		t.Fatalf("a local ask queued: %+v", out)
	}
}

// Until yardr-gr10 lands, an ask that comes in is refused as a kind the
// link does not carry, and logged: it is never taken and dropped
// silently.
func TestPeerAskNotTakenYet(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}})
	got := y.take(t, store.PeerMessage{ID: "a1", Kind: store.PeerAsk, Body: json.RawMessage(`{"v":1}`)})
	if len(got) != 1 || got[0].Refused != store.RefusedKind {
		t.Fatalf("take of kind ask = %+v", got)
	}
	if ref := peerEvents(t, y.api, "peer_message_refused"); len(ref) != 1 || ref[0].Data["direction"] != "in" || ref[0].Data["kind"] != "ask" || ref[0].Data["reason"] != store.RefusedKind {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	if taken := peerEvents(t, y.api, "peer_message_received"); len(taken) != 0 {
		t.Fatalf("peer_message_received = %+v", taken)
	}
}
