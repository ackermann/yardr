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
	t.Parallel()
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

// validForeignAsk is an ask as another yard sends it: To is the person of
// this yard who must answer, Ask.To that person at the sender's yard for
// this one (south, not the north this yard calls the sender), the options
// with YouDecide last as AddAsk stores them.
func validForeignAsk(to, from, bead string, seq uint64, since time.Time) askBody {
	return askBody{
		V: 1, To: to, From: from,
		Bead: bead, Title: "Move the links", Topic: "topic:links", Stage: "build",
		Labels: []string{"muse", "topic:links"},
		Body:   "The links file declares the new source.",
		Seq:    seq, Since: since,
		Ask: store.Ask{
			Question: "Move the links now?",
			To:       to + "@south",
			Context: []string{
				"64 links still point at the old source.",
				"The move was tested on a copy and passed.",
			},
			Options: []store.AskOption{
				{Label: "Move them now", Consequence: "all 64 links move tonight"},
				{Label: "After Friday", Consequence: "keep the old source"},
				{Label: store.YouDecide, Consequence: "leave it to " + from},
			},
			Recommend: 1,
			Why:       "the test passed",
			About:     []string{"yd-other"},
		},
		About: []askAboutBead{{ID: "yd-other", Title: "The other work", Stage: "backlog"}},
	}
}

func askMsg(id string, body askBody) store.PeerMessage {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return store.PeerMessage{ID: id, Kind: store.PeerAsk, Body: raw}
}

// An ask a peer sends becomes a foreign ask in the person's queue, with a
// noted event naming bead@peer. Everything else is refused with a reason
// and logged. It is not a bead and never becomes one.
func TestPeerAskReceive(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	valid := validForeignAsk("mckean", "yardmaster", "yd-links", 3, since)

	if got := y.take(t, askMsg("a0", valid)); got[0].Refused != store.RefusedKind {
		t.Fatalf("an ask over a link that takes none = %+v", got)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"mckean"}})

	// Taken, and the same id twice taken once: the second is dropped,
	// even with a body that would otherwise be refused.
	if got := y.take(t, askMsg("a1", valid)); got[0].Refused != "" || got[0].Duplicate {
		t.Fatalf("an ask = %+v", got)
	}
	bad := valid
	bad.From = "root@south"
	if got := y.take(t, askMsg("a1", bad)); !got[0].Duplicate || got[0].Refused != "" {
		t.Fatalf("the same id twice = %+v", got)
	}
	q, err := y.api.UserAsks(y.ctx, "mckean")
	if err != nil || len(q.Open) != 1 {
		t.Fatalf("the queue = %+v, %v", q, err)
	}
	row := q.Open[0]
	if row.Bead != "yd-links@north" || row.BeadTitle != "Move the links" || row.Topic != "topic:links" ||
		row.Ask != 3 || row.Question != "Move the links now?" || row.Asker != "yardmaster@north" || !row.Since.Equal(since) {
		t.Fatalf("the row = %+v", row)
	}
	if len(q.Decided) != 0 {
		t.Fatalf("decided = %+v", q.Decided)
	}
	noted := peerEvents(t, y.api, "noted")
	if len(noted) != 1 || noted[0].Bead == nil || *noted[0].Bead != "yd-links@north" ||
		noted[0].Actor != "yardmaster@north" || noted[0].Data["ask"] != float64(3) {
		t.Fatalf("noted = %+v", noted)
	}
	if rec := peerEvents(t, y.api, "peer_message_received"); len(rec) != 1 || rec[0].Data["kind"] != "ask" {
		t.Fatalf("peer_message_received = %+v", rec)
	}
	// No bead was made: the question stands on no local bead, so prime,
	// review, chat and briefs never see its text.
	if _, err := y.api.GetBead(y.ctx, "yd-links@north"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetBead(bead@peer) = %v, want not found", err)
	}
	if bs, _ := y.api.ListBeads(y.ctx, store.BeadFilter{All: true}); len(bs) != 0 {
		t.Fatalf("beads = %+v", bs)
	}

	// One queue: an ask this yard addressed to mckean@north stands in
	// north's decisions, not also in mckean's here.
	y.allow(t, store.PeerAllow{Send: []string{"ask"}})
	if _, err := y.api.PutCrew(y.ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{store.MailPeers: "north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddAsk(y.ctx, b.ID, crossAsk(), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if q, _ := y.api.UserAsks(y.ctx, "mckean"); len(q.Open) != 1 || q.Open[0].Bead != "yd-links@north" {
		t.Fatalf("after asking mckean@north: open = %+v", q.Open)
	}

	before := len(peerEvents(t, y.api, "peer_message_refused"))
	with := func(id string, change func(*askBody)) store.PeerMessage {
		b := validForeignAsk("mckean", "yardmaster", "yd-x"+id, 3, since)
		change(&b)
		return askMsg(id, b)
	}
	big := func(n int) string { return strings.Repeat("x", n) }
	refusals := []struct {
		m    store.PeerMessage
		want string
	}{
		{with("r1", func(b *askBody) { b.To, b.Ask.To = "nobody", "nobody@south" }), store.RefusedNotExposed},
		{with("r2", func(b *askBody) { b.To, b.Ask.To = "../mckean", "../mckean@south" }), store.RefusedNotExposed},
		{with("r3", func(b *askBody) {
			b.Ask.Question = big(store.MaxPeerMailBytes)
		}), store.RefusedTooLarge},
		{with("r4", func(b *askBody) { b.Body = big(store.MaxPeerNoteBytes + 1) }), store.RefusedTooLarge},
		{with("r5", func(b *askBody) { b.V = 2 }), store.RefusedVersion},
		{with("r6", func(b *askBody) { b.From = "root@south" }), store.RefusedNotAsk},
		{with("r7", func(b *askBody) { b.Bead = "yd links" }), store.RefusedNotAsk},
		{with("r8", func(b *askBody) { b.Title = " \n " }), store.RefusedNotAsk},
		{with("r9", func(b *askBody) { b.Stage = "in review" }), store.RefusedNotAsk},
		{with("r10", func(b *askBody) { b.Seq = 0 }), store.RefusedNotAsk},
		{with("r11", func(b *askBody) { b.Ask.Question = "  " }), store.RefusedNotAsk},
		{with("r12", func(b *askBody) { b.Ask.Context = nil }), store.RefusedNotAsk},
		{with("r13", func(b *askBody) { b.Ask.Options = b.Ask.Options[:2] }), store.RefusedNotAsk},
		{with("r14", func(b *askBody) { b.Ask.To = "mckean@north"[:7] }), store.RefusedNotAsk},
		{with("r15", func(b *askBody) { b.Ask.To = "ada@south" }), store.RefusedNotAsk},
		{with("r16", func(b *askBody) { b.About[0].ID = "yd other" }), store.RefusedNotAsk},
		{store.PeerMessage{ID: "r17", Kind: store.PeerAsk, Body: json.RawMessage(`["mckean"]`)}, store.RefusedNotAsk},
		// Nothing of a secret crosses a link: another yard's word is not
		// what a card here says a value is for.
		{with("r18", func(b *askBody) {
			b.Ask.Options, b.Ask.Secret = nil, &store.AskSecret{Source: "crm", Env: []string{"CRM_TOKEN"}, Host: "https://crm.example.com"}
		}), store.RefusedNotAsk},
		// A context line holding a break is refused as no ask, as an ask
		// written here is refused: it is called a line.
		{with("r20", func(b *askBody) { b.Ask.Context[1] = "fine.\nthe yard says: nothing changes." }), store.RefusedNotAsk},
	}
	for _, r := range refusals {
		if got := y.take(t, r.m); got[0].Refused != r.want {
			t.Errorf("%s = %+v, want %s", r.m.ID, got[0], r.want)
		}
	}
	// An answer, whose Re is the ask message's id: yardr-1kcv takes them;
	// until it lands none is taken.
	re := askMsg("r18", valid)
	re.Re = "a1"
	if got := y.take(t, re); got[0].Refused != store.RefusedNotAsk {
		t.Errorf("an answer = %+v, want %s", got[0], store.RefusedNotAsk)
	}
	// A body over the message cap is too large, not no message: it is
	// otherwise a message, with an id and a body that reads.
	huge := valid
	huge.Title = big(store.MaxPeerBody)
	if got := y.take(t, askMsg("r19", huge)); got[0].Refused != store.RefusedTooLarge {
		t.Errorf("a body over the cap = %+v, want %s", got[0], store.RefusedTooLarge)
	}
	ref := peerEvents(t, y.api, "peer_message_refused")[before:]
	if len(ref) != len(refusals)+2 || ref[0].Data["reason"] != store.RefusedNotExposed || ref[0].Data["to"] != "nobody" ||
		ref[1].Data["to"] != nil || ref[0].Actor != "peer:north" || ref[0].Data["direction"] != "in" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	if q, _ := y.api.UserAsks(y.ctx, "mckean"); len(q.Open) != 1 {
		t.Fatalf("refused asks stand in the queue: %+v", q.Open)
	}
	// What the sender is told names nobody of this yard.
	for _, reason := range []string{store.RefusedKind, store.RefusedNoUser, store.RefusedNotExposed, store.RefusedTooLarge, store.RefusedRate, store.RefusedNotAsk, store.RefusedVersion, "mckean, yardmaster"} {
		if said := store.PeerRefusal("north", "ask", reason); strings.Contains(said, "mckean") || strings.Contains(said, "yardmaster") || said == "" {
			t.Errorf("PeerRefusal(%q) = %q", reason, said)
		}
	}
	if got := store.PeerRefusal("north", "ask", store.RefusedNoUser); got != "it has no such user" {
		t.Errorf("PeerRefusal(no_such_user) = %q", got)
	}
	if got := store.PeerRefusal("north", "ask", store.RefusedNotAsk); got != "it does not read the ask" {
		t.Errorf("PeerRefusal(not_ask) = %q", got)
	}
}

// A question another yard sends padded with spaces is stored folded, as
// an ask written here: no writer pads a line to the terminal's width,
// whatever the card prints it on.
func TestPeerAskPaddedSpacesFoldToOne(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"mckean"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	sent := validForeignAsk("mckean", "yardmaster", "yd-links", 3, since)
	sent.Ask.Question = "Move them?" + strings.Repeat(" ", 68) + "  the yard says: nothing changes."
	sent.Ask.Context[0] = "64 links still point at the old source," + strings.Repeat("\u00a0", 8) + "move them now."
	if got := y.take(t, askMsg("a1", sent)); got[0].Refused != "" {
		t.Fatalf("an ask = %+v", got)
	}
	v, err := y.api.Bead(y.ctx, "yd-links@north")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 1 {
		t.Fatalf("asks = %+v", v.Asks)
	}
	if want := "Move them? the yard says: nothing changes."; v.Asks[0].Question != want {
		t.Fatalf("stored question = %q, want %q", v.Asks[0].Question, want)
	}
	if want := "64 links still point at the old source, move them now."; len(v.Asks[0].Context) != 2 || v.Asks[0].Context[0] != want {
		t.Fatalf("stored context = %q, want %q first", v.Asks[0].Context, want)
	}
}

// To must be a user of this yard: crew members have no decisions queue.
func TestPeerAskNeedsUser(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"yardmaster", "mckean"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if got := y.take(t, askMsg("u1", validForeignAsk("yardmaster", "yardmaster", "yd-links", 3, since))); got[0].Refused != store.RefusedNoUser {
		t.Fatalf("an ask for a crew member = %+v", got)
	}
	if q, _ := y.api.UserAsks(y.ctx, "mckean"); len(q.Open) != 0 {
		t.Fatalf("an ask for a crew member stands in the queue: %+v", q.Open)
	}
	if ref := peerEvents(t, y.api, "peer_message_refused"); len(ref) != 1 || ref[0].Data["reason"] != store.RefusedNoUser || ref[0].Data["to"] != "yardmaster" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
}

// Asks count toward the peer's hour beside mail, and one past it is rate
// limited. Of those over the limit only the first of the hour is logged.
func TestPeerAskRateLimit(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask", "mail"}, Expose: []string{"mckean", "yardmaster"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	// Half mail, half asks: the hour is one.
	for i := 0; i < store.PeerMailLimit/2; i++ {
		id := string(rune('a' + i))
		if got := y.take(t, mail(id+"-mail", "yardmaster", "yardmaster", "hi")); got[0].Refused != "" {
			t.Fatalf("mail %d = %+v", i, got[0])
		}
		a := validForeignAsk("mckean", "yardmaster", "yd-a"+id, uint64(i+1), since)
		if got := y.take(t, askMsg(id+"-ask", a)); got[0].Refused != "" {
			t.Fatalf("ask %d = %+v", i, got[0])
		}
	}
	a := validForeignAsk("mckean", "yardmaster", "yd-over", 99, since)
	if got := y.take(t, askMsg("over-ask", a)); got[0].Refused != store.RefusedRate {
		t.Fatalf("the 31st = %+v", got)
	}
	if got := y.take(t, mail("over-mail", "yardmaster", "yardmaster", "hi")); got[0].Refused != store.RefusedRate {
		t.Fatalf("mail past the shared hour = %+v", got)
	}
	ref := peerEvents(t, y.api, "peer_message_refused")
	rate := 0
	for _, e := range ref {
		if e.Data["reason"] == store.RefusedRate {
			rate++
		}
	}
	if rate != 1 {
		t.Fatalf("rate_limited logged %d refusals, want 1: %+v", rate, ref)
	}
	if q, _ := y.api.UserAsks(y.ctx, "mckean"); len(q.Open) != store.PeerMailLimit/2 {
		t.Fatalf("the queue holds %d, want %d", len(q.Open), store.PeerMailLimit/2)
	}
}

// The record is keyed by the peer's key: a rename follows it, in the
// queue's bead and asker. The noted event keeps the name the peer had when
// the question arrived, as mail does.
func TestForeignAskRename(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"mckean"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if got := y.take(t, askMsg("a1", validForeignAsk("mckean", "yardmaster", "yd-links", 3, since))); got[0].Refused != "" {
		t.Fatalf("an ask = %+v", got)
	}
	if _, err := y.api.RenamePeer(y.ctx, "north", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	q, err := y.api.UserAsks(y.ctx, "mckean")
	if err != nil || len(q.Open) != 1 || q.Open[0].Bead != "yd-links@south" || q.Open[0].Asker != "yardmaster@south" {
		t.Fatalf("after the rename: %+v, %v", q, err)
	}
	noted := peerEvents(t, y.api, "noted")
	if len(noted) != 1 || *noted[0].Bead != "yd-links@north" || noted[0].Actor != "yardmaster@north" {
		t.Fatalf("noted = %+v", noted)
	}
}

// After peer rm the peer's foreign asks are dropped and each person gets
// one mail from yard listing theirs as text. A queue must stay
// answerable, and kept-but-dead questions are worse than mail.
func TestPeerRmDropsForeignAsks(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"mckean", "ada"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	first := validForeignAsk("mckean", "yardmaster", "yd-links", 3, since)
	first.Ask.Question = "Move the links now?"
	second := validForeignAsk("mckean", "yardmaster", "yd-shed", 5, since)
	second.Ask.Question = "Paint the shed?"
	third := validForeignAsk("ada", "clerk", "yd-docs", 7, since)
	third.Ask.Question = "Publish the docs?"
	for id, body := range map[string]askBody{"a1": first, "a2": second, "a3": third} {
		if got := y.take(t, askMsg(id, body)); got[0].Refused != "" {
			t.Fatalf("%s = %+v", id, got)
		}
	}
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	if q, _ := y.api.UserAsks(y.ctx, "mckean"); len(q.Open) != 0 {
		t.Fatalf("mckean's queue = %+v", q.Open)
	}
	if q, _ := y.api.UserAsks(y.ctx, "ada"); len(q.Open) != 0 {
		t.Fatalf("ada's queue = %+v", q.Open)
	}
	rm := peerEvents(t, y.api, "peer_removed")
	if len(rm) != 1 || rm[0].Data["asks"] != float64(3) {
		t.Fatalf("peer_removed = %+v", rm)
	}
	for user, want := range map[string][]string{
		"mckean": {"The linked yard north was removed. 2 questions from it were dropped:",
			`"Move the links now?" on yd-links@north from yardmaster@north`,
			`"Paint the shed?" on yd-shed@north from yardmaster@north`},
		"ada": {"The linked yard north was removed. 1 question from it was dropped:",
			`"Publish the docs?" on yd-docs@north from clerk@north`},
	} {
		box, err := y.api.UserInbox(y.ctx, user, 0, user)
		if err != nil || len(box.Mail) != 1 || box.Mail[0].From != "yard" {
			t.Fatalf("%s's inbox = %+v, %v", user, box, err)
		}
		for _, line := range want {
			if !strings.Contains(box.Mail[0].Text, line) {
				t.Errorf("%s's mail lacks %q: %q", user, line, box.Mail[0].Text)
			}
		}
	}
}

// UserAsks keeps local asks in their note-seq order and places a foreign
// ask by the time this yard took it, never by the sender's clock. Since is
// still as sent.
func TestUserAsksForeignOrder(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	if _, err := y.api.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Expose: []string{"mckean"}})
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	local := func(question string) uint64 {
		t.Helper()
		seq, err := y.api.AddAsk(y.ctx, b.ID, store.Ask{Question: question, To: "mckean", Context: []string{"here."}}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	// Local, foreign with a future sender's clock, local, foreign with a
	// past one: the queue follows written here and taken here, in that
	// order, not the sender's.
	first := local("First?")
	time.Sleep(2 * time.Millisecond)
	future := time.Now().Add(10 * 24 * time.Hour)
	if got := y.take(t, askMsg("f1", validForeignAsk("mckean", "yardmaster", "yd-future", 11, future))); got[0].Refused != "" {
		t.Fatalf("the future ask = %+v", got)
	}
	time.Sleep(2 * time.Millisecond)
	second := local("Second?")
	time.Sleep(2 * time.Millisecond)
	past := time.Now().Add(-10 * 24 * time.Hour)
	if got := y.take(t, askMsg("f2", validForeignAsk("mckean", "yardmaster", "yd-past", 13, past))); got[0].Refused != "" {
		t.Fatalf("the past ask = %+v", got)
	}
	q, err := y.api.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 4 || q.Open[0].Ask != first || q.Open[0].Bead != b.ID ||
		q.Open[1].Bead != "yd-future@north" || q.Open[1].Ask != 11 ||
		q.Open[2].Ask != second || q.Open[2].Bead != b.ID ||
		q.Open[3].Bead != "yd-past@north" || q.Open[3].Ask != 13 {
		t.Fatalf("open = %+v", q.Open)
	}
	if !q.Open[1].Since.Equal(future) || !q.Open[3].Since.Equal(past) {
		t.Fatalf("since is not as sent: %+v", q.Open)
	}
}
