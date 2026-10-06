package store_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// resident adds a resident to the yard, with the peers it may mail.
func (y *peerYard) resident(t *testing.T, name, peers string) {
	t.Helper()
	cfg := map[string]any{}
	if peers != "" {
		cfg[store.MailPeers] = peers
	}
	if _, err := y.api.PutResident(y.ctx, store.Resident{Name: name, Runner: "herdr", Config: cfg}, "jo"); err != nil {
		t.Fatal(err)
	}
}

// inbox is the resident's unread mail.
func (y *peerYard) inbox(t *testing.T, name string) []store.Event {
	t.Helper()
	page, err := y.api.Inbox(y.ctx, name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(page.Events, func(e store.Event) bool { return e.Kind != "mail" })
}

func mail(id, to, from, text string) store.PeerMessage {
	body, _ := json.Marshal(map[string]string{"to": to, "from": from, "text": text})
	return store.PeerMessage{ID: id, Kind: store.PeerMail, Body: body}
}

// Mail to a peer's resident is queued for the peer as a message of kind
// mail when the link carries mail out and the resident who writes has the
// peer in its peers setting. The operator, who is no resident, needs the
// link's permission only.
func TestMailPeer(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.resident(t, "mayor", "north")
	y.resident(t, "clerk", "")

	// The link carries nothing yet: refused, and logged.
	if _, err := y.api.MailPeer(y.ctx, "mayor", "north", "hello", "mayor"); err == nil || !strings.Contains(err.Error(), "does not carry mail out") ||
		!strings.Contains(err.Error(), "yardr peer allow north --send mail") {
		t.Fatalf("mail over a link that carries none = %v", err)
	}
	if ref := peerEvents(t, y.api, "peer_message_refused"); len(ref) != 1 || ref[0].Data["direction"] != "out" || ref[0].Data["kind"] != "mail" || ref[0].Actor != "mayor" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	y.allow(t, store.PeerAllow{Send: []string{"mail"}})

	// A resident without the peer in its setting is refused, the link's
	// permission or not.
	if _, err := y.api.MailPeer(y.ctx, "mayor", "north", "hello", "clerk"); err == nil || !strings.Contains(err.Error(), "resident clerk may not mail residents of north") ||
		!strings.Contains(err.Error(), "yardr resident set clerk peers=north") {
		t.Fatalf("mail from a resident without peers = %v", err)
	}
	if _, err := y.api.MailPeer(y.ctx, "mayor", "nobody", "hello", "mayor"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mail to no peer = %v", err)
	}
	for name, args := range map[string][2]string{
		"must be 1..16384 bytes": {"mayor", strings.Repeat("x", store.MaxPeerMailBytes+1)},
		"must be 1..":            {"mayor", "  "},
		"no resident's name":     {"may or", "hello"},
		"that is no resident":    {"mayor@north", "hello"},
	} {
		if _, err := y.api.MailPeer(y.ctx, args[0], "north", args[1], "mayor"); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("MailPeer(%q, %d bytes) = %v, want %q", args[0], len(args[1]), err, name)
		}
	}
	// PeerSend is no way around the resident's setting.
	if _, err := y.api.PeerSend(y.ctx, "north", "mail", json.RawMessage(`{"to":"mayor","from":"clerk","text":"x"}`), "clerk"); err == nil || !strings.Contains(err.Error(), "yardr mail <resident>@north") {
		t.Fatalf("PeerSend of kind mail = %v", err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 {
		t.Fatalf("refused mail waits: %+v", p)
	}

	// The resident, with text of exactly the limit, and the operator.
	big := strings.Repeat("é", store.MaxPeerMailBytes/2)
	m, err := y.api.MailPeer(y.ctx, "mayor", "north", big, "mayor")
	if err != nil || m.Kind != "mail" || m.Peer != "north" {
		t.Fatalf("MailPeer = %+v, %v", m, err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "north", "from the operator", "jo"); err != nil {
		t.Fatalf("the operator's mail = %v", err)
	}
	out, _ := y.api.PeerOutbox(y.ctx, "north", 0)
	var body struct{ To, From, Text string }
	if len(out) != 2 || json.Unmarshal(out[0].Body, &body) != nil || body.To != "mayor" || body.From != "mayor" || body.Text != big {
		t.Fatalf("the outbox = %+v", out)
	}
	sent := peerEvents(t, y.api, "mail")
	if len(sent) != 2 || sent[0].Actor != "mayor" || sent[0].Data["to"] != "mayor" || sent[0].Data["to_peer"] != "north" ||
		sent[0].Data["text"] != big || sent[0].Data["id"] != m.ID || sent[0].Data["from_peer"] != nil || sent[1].Actor != "jo" {
		t.Fatalf("mail events = %+v", sent)
	}
	// Mail for north's mayor and north's clerk is in no inbox here, though
	// this yard has residents of both names.
	if got := append(y.inbox(t, "mayor"), y.inbox(t, "clerk")...); len(got) != 0 {
		t.Fatalf("mail that left is in an inbox here: %+v", got)
	}
}

// Mail a peer sends lands in the inbox of the resident it names, from
// <name>@<peer>, when the link carries mail in. Everything else is refused
// with a reason and logged.
func TestPeerMailReceive(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.resident(t, "mayor", "")
	receive := func(m store.PeerMessage) store.PeerTaken {
		t.Helper()
		got, err := y.own.PeerReceive(y.ctx, "north", []store.PeerMessage{m})
		if err != nil || len(got) != 1 {
			t.Fatalf("PeerReceive = %+v, %v", got, err)
		}
		return got[0]
	}
	if got := receive(mail("m0", "mayor", "mayor", "hi")); got.Refused != store.RefusedKind {
		t.Fatalf("mail over a link that takes none = %+v", got)
	}
	if got := y.inbox(t, "mayor"); len(got) != 0 {
		t.Fatalf("refused mail is in the inbox: %+v", got)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}})

	// The sender's mayor writes to this yard's mayor: the same name, and
	// not the resident's own doing.
	for i := 0; i < 2; i++ {
		if got := receive(mail("m1", "mayor", "mayor", "the build is green")); got.Refused != "" || got.Duplicate != (i == 1) {
			t.Fatalf("mail, time %d = %+v", i, got)
		}
	}
	got := y.inbox(t, "mayor")
	if len(got) != 1 || got[0].Actor != "mayor@north" || got[0].Data["from"] != "mayor" || got[0].Data["from_peer"] != "north" ||
		got[0].Data["to"] != "mayor" || got[0].Data["text"] != "the build is green" || got[0].Data["id"] != "m1" || got[0].Data["to_peer"] != nil {
		t.Fatalf("the inbox = %+v", got)
	}
	if rec := peerEvents(t, y.api, "peer_message_received"); len(rec) != 1 || rec[0].Data["kind"] != "mail" || rec[0].Data["text"] != nil {
		t.Fatalf("peer_message_received = %+v", rec)
	}

	before := len(peerEvents(t, y.api, "peer_message_refused"))
	raw := func(id, body string) store.PeerMessage {
		return store.PeerMessage{ID: id, Kind: store.PeerMail, Body: json.RawMessage(body)}
	}
	refusals := []struct {
		m    store.PeerMessage
		want string
	}{
		{mail("r1", "nobody", "mayor", "hi"), store.RefusedNoResident},
		{mail("r2", "../mayor", "mayor", "hi"), store.RefusedNoResident},
		{mail("r3", "mayor", "mayor", strings.Repeat("x", store.MaxPeerMailBytes+1)), store.RefusedTooLarge},
		{mail("r4", "mayor", "mayor", " "), store.RefusedNotMail},
		// A name with an @ would read as another yard's.
		{mail("r5", "mayor", "root@south", "hi"), store.RefusedNotMail},
		{mail("r6", "mayor", "", "hi"), store.RefusedNotMail},
		{raw("r7", `["mayor"]`), store.RefusedNotMail},
	}
	for _, r := range refusals {
		if got := receive(r.m); got.Refused != r.want {
			t.Errorf("%s = %+v, want %s", r.m.ID, got, r.want)
		}
	}
	ref := peerEvents(t, y.api, "peer_message_refused")[before:]
	if len(ref) != len(refusals) || ref[0].Data["reason"] != store.RefusedNoResident || ref[0].Data["to"] != "nobody" || ref[1].Data["to"] != nil ||
		ref[0].Actor != "peer:north" || ref[0].Data["direction"] != "in" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	if got := y.inbox(t, "mayor"); len(got) != 1 {
		t.Fatalf("refused mail reached the inbox: %+v", got)
	}
	// What the sender is told names no resident of this yard.
	for _, reason := range []string{store.RefusedKind, store.RefusedNoResident, store.RefusedTooLarge, store.RefusedRate, store.RefusedNotMail, "mayor, clerk"} {
		if said := store.PeerRefusal("mail", reason); strings.Contains(said, "mayor") || said == "" {
			t.Errorf("PeerRefusal(%q) = %q", reason, said)
		}
	}
}

// Mail a yard took by asking and refused is told to the peer with the next
// question, and forgotten once it has been.
func TestPeerMailPulledRefusal(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.resident(t, "mayor", "")
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}})
	batch := func(told []uint64, ms ...store.PeerMessage) store.PeerBatch {
		body, err := json.Marshal(store.PeerPage{Messages: ms})
		if err != nil {
			t.Fatal(err)
		}
		return store.PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(y.north, 200, "req-1", body), Body: body, Told: told}
	}
	good, bad := mail("p1", "mayor", "mayor", "hi"), mail("p2", "nobody", "mayor", "hi")
	good.Seq, bad.Seq = 3, 4
	res, err := y.api.PeerPulled(y.ctx, "north", batch(nil, good, bad))
	if err != nil || res.Taken[0].Refused != "" || res.Taken[1].Refused != store.RefusedNoResident {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	p, _ := y.api.GetPeer(y.ctx, "north")
	if p.Pulled != 4 || len(p.Refused) != 1 || p.Refused[4] != store.RefusedNoResident {
		t.Fatalf("after the pull: %+v", p)
	}
	if got := y.inbox(t, "mayor"); len(got) != 1 || got[0].Actor != "mayor@north" {
		t.Fatalf("the inbox = %+v", got)
	}
	// An empty answer to the question that told of it.
	if _, err := y.api.PeerPulled(y.ctx, "north", batch([]uint64{4})); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); len(p.Refused) != 0 || p.Pulled != 4 {
		t.Fatalf("after it was told: %+v", p)
	}
	quiet(t, y.own, "an empty page with nothing left to forget", func() {
		if _, err := y.api.PeerPulled(y.ctx, "north", batch([]uint64{4})); err != nil {
			t.Fatal(err)
		}
	})
}

// Mail that is given up, by a post refused or by the word of a peer that
// asked for it, comes back to the resident who wrote it as mail from the
// yard. Mail the peer took does not.
func TestPeerMailUndelivered(t *testing.T) {
	y := newPeerYard(t, "") // north asks for its mail
	y.resident(t, "mayor", "north")
	y.allow(t, store.PeerAllow{Send: []string{"mail"}})
	var seqs []uint64
	for _, to := range []string{"mayor", "nobody", "clerk"} {
		m, err := y.api.MailPeer(y.ctx, to, "north", "hello "+to, "mayor")
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, m.Seq)
	}
	page, err := y.own.PeerHandOver(y.ctx, "north", 0, nil)
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("PeerHandOver = %+v, %v", page, err)
	}
	// North has the first two, and did not take the second. A reason this
	// yard does not know is not repeated.
	if _, err := y.own.PeerHandOver(y.ctx, "north", seqs[1], map[uint64]string{seqs[1]: store.RefusedNoResident, 99: "x"}); err != nil {
		t.Fatal(err)
	}
	p, _ := y.api.GetPeer(y.ctx, "north")
	if p.Waiting != 1 || p.Failed != 1 {
		t.Fatalf("after the question: %+v", p)
	}
	if sent := peerEvents(t, y.api, "peer_message_sent"); len(sent) != 1 || sent[0].Data["way"] != "pull" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	failed := peerEvents(t, y.api, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["reason"] != "refused by the peer: it has no such resident" || failed[0].Data["way"] != "pull" || failed[0].Data["text"] != nil {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	got := y.inbox(t, "mayor")
	text, _ := got[0].Data["text"].(string)
	if len(got) != 1 || got[0].Actor != "yard" || !strings.HasPrefix(text, "your mail to nobody@north of ") ||
		!strings.HasSuffix(text, " was not delivered: refused by the peer: it has no such resident") {
		t.Fatalf("the sender's inbox = %+v", got)
	}

	// The third is given up by the daemon.
	if err := y.api.PeerSendFailed(y.ctx, "north", seqs[2], "peer north: refused by the peer: it does not say why in words this yard knows", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if got := y.inbox(t, "mayor"); len(got) != 2 || !strings.Contains(got[1].Data["text"].(string), "your mail to clerk@north") {
		t.Fatalf("the sender's inbox = %+v", got)
	}
}

// Removing a peer takes it out of every resident's peers: the next yard
// added under the name is one nobody was allowed to mail.
func TestPeerRemovedLeavesNoResidentItsMail(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.resident(t, "mayor", "north,south")
	y.resident(t, "clerk", "north")
	y.resident(t, "scout", "south")
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"mayor": "south", "clerk": "", "scout": "south"} {
		r, err := y.api.GetResident(y.ctx, name)
		if err != nil || strings.Join(r.MayMail(), ",") != want {
			t.Errorf("%s may mail %v, %v; want %q", name, r.MayMail(), err, want)
		}
	}
	if rm := peerEvents(t, y.api, "peer_removed"); len(rm) != 1 || len(rm[0].Data["residents"].([]any)) != 2 {
		t.Fatalf("peer_removed = %+v", rm)
	}
}
