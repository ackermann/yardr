package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// A yard takes PeerMailLimit mails from one peer within an hour, counted
// from the first. What comes after is refused, with one event for all of
// it, until that hour has passed.
func TestPeerMailLimit(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Receive: []string{PeerMail}, Expose: []string{"yardmaster"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	receive := func(to string) string {
		t.Helper()
		n++
		body, _ := json.Marshal(peerMail{To: to, From: "yardmaster", Text: "hi"})
		got, err := s.PeerReceive(ctx, "north", yardkey.Public(k), []PeerMessage{{ID: fmt.Sprintf("m%d", n), Kind: PeerMail, Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Refused
	}
	count := func(kind, reason string) (n int) {
		t.Helper()
		evs, err := s.Events(ctx, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			// The yard's own line about the refused mail is none delivered.
			if e.Kind == kind && e.Actor != "yard" && (reason == "" || e.Data["reason"] == reason) {
				n++
			}
		}
		return n
	}
	// Mail for nobody counts too: a yard cannot go on asking who lives here.
	if got := receive("nobody"); got != RefusedNotExposed {
		t.Fatalf("mail for nobody = %q", got)
	}
	for i := 1; i < PeerMailLimit; i++ {
		at = at.Add(time.Minute)
		if got := receive("yardmaster"); got != "" {
			t.Fatalf("mail %d of the hour = %q", i+1, got)
		}
	}
	for i := 0; i < 5; i++ {
		at = at.Add(time.Minute)
		if got := receive("yardmaster"); got != RefusedRate {
			t.Fatalf("mail %d over the limit = %q", i+1, got)
		}
	}
	if got := count("peer_message_refused", RefusedRate); got != 1 {
		t.Fatalf("%d events for the mail over the limit, want 1", got)
	}
	if got := count("mail", ""); got != PeerMailLimit-1 {
		t.Fatalf("%d mails delivered, want %d", got, PeerMailLimit-1)
	}
	// One second short of the hour since the first, and then the hour.
	at = time.Date(2026, 10, 5, 12, 59, 59, 0, time.UTC)
	if got := receive("yardmaster"); got != RefusedRate {
		t.Fatalf("mail a second before the hour has passed = %q", got)
	}
	at = at.Add(time.Second)
	if got := receive("yardmaster"); got != "" {
		t.Fatalf("mail once the hour has passed = %q", got)
	}
	if got := count("mail", ""); got != PeerMailLimit {
		t.Fatalf("%d mails delivered, want %d", got, PeerMailLimit)
	}
	// The next hour's refusal is an event of its own.
	for i := 0; i < PeerMailLimit+1; i++ {
		receive("yardmaster")
	}
	if got := count("peer_message_refused", RefusedRate); got != 2 {
		t.Fatalf("%d events for two hours over the limit, want 2", got)
	}
}

// Mail over a peer's hour is told to whom it was for, by mail from the
// yard: once in the hour for each, however much is refused, with nothing of
// the refused text. The operator sets what a link takes, and the files of
// the hour follow it.
func TestPeerMailLimitIsToldAndSet(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, Peer{Name: "airy", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"yardmaster", "hidden"} {
		if _, err := s.PutCrew(ctx, CrewMember{Name: name, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "airy", PeerAllow{Receive: []string{PeerMail, PeerFile}, Expose: []string{"yardmaster", "ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	receive := func(to, text string) string {
		t.Helper()
		n++
		body, _ := json.Marshal(peerMail{To: to, From: "mckean", Text: text})
		got, err := s.PeerReceive(ctx, "airy", yardkey.Public(k), []PeerMessage{{ID: fmt.Sprintf("m%d", n), Kind: PeerMail, Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Refused
	}
	// told are the lines the yard wrote to: what it told of refused mail.
	told := func(to string) (lines []string) {
		t.Helper()
		evs, err := s.Events(ctx, "", 2000)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "mail" && e.Actor == "yard" && e.Data["to"] == to {
				lines = append(lines, e.Data["text"].(string))
			}
		}
		return lines
	}
	for i := 0; i < PeerMailLimit; i++ {
		if got := receive("yardmaster", "hi"); got != "" {
			t.Fatalf("mail %d of the hour = %q", i+1, got)
		}
	}
	if got := told("yardmaster"); len(got) != 0 {
		t.Fatalf("within the limit the yard told %q", got)
	}
	at = at.Add(19 * time.Minute)
	for i := 0; i < 7; i++ {
		if got := receive("yardmaster", "the secret text"); got != RefusedRate {
			t.Fatalf("mail %d over the limit = %q", i+1, got)
		}
	}
	until := time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC).Local().Format("15:04")
	want := "mail from mckean@airy was refused: this yard takes 30 mails and asks an hour from airy, until " + until + "; 31 so far. " +
		"Nothing of its text was kept: ask mckean to send it again after that. " +
		"You are told once for the hour, whatever else is refused (yardr peer show airy counts it). " +
		"The operator sets what the link takes with yardr peer allow airy --mail-limit <n>."
	if got := told("yardmaster"); len(got) != 1 || got[0] != want {
		t.Fatalf("the yardmaster was told %q, want once %q", got, want)
	}
	// It is in the inbox the crew member reads.
	if page, err := s.Inbox(ctx, "yardmaster", 0); err != nil || page.Total != PeerMailLimit+1 || page.Events[page.Total-1].Data["text"] != want {
		t.Fatalf("the yardmaster's inbox = %+v, %v", page, err)
	}
	// A user it was for is told too, once, and nobody the link does not
	// expose or the yard does not have: they would not have had the mail.
	for range 3 {
		receive("ada", "hi")
		receive("hidden", "hi")
		receive("nobody", "hi")
	}
	if box, err := s.UserInbox(ctx, "ada", 0); err != nil || len(box.Mail) != 1 || box.Mail[0].From != "yard" || !strings.Contains(box.Mail[0].Text, "; 38 so far.") {
		t.Fatalf("ada's inbox = %+v, %v", box, err)
	}
	if got := len(told("hidden")) + len(told("nobody")); got != 0 {
		t.Fatalf("%d lines for names the link does not expose", got)
	}
	// A body that is no mail, or names a sender that is none, fills no inbox.
	if got, err := s.PeerReceive(ctx, "airy", yardkey.Public(k), []PeerMessage{{ID: "x1", Kind: PeerMail, Body: json.RawMessage(`"no mail"`)}}); err != nil || got[0].Refused != RefusedRate {
		t.Fatalf("no mail over the limit = %+v, %v", got, err)
	}
	if got := len(told("yardmaster")) + len(told("ada")); got != 2 {
		t.Fatalf("%d lines in the hour, want one each for the two written to", got)
	}
	evs, err := s.Events(ctx, "", 2000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if raw, _ := json.Marshal(e.Data); strings.Contains(string(raw), "the secret text") {
			t.Fatalf("the refused text was kept: %s %s", e.Kind, raw)
		}
	}

	// To take more is the operator's, to take less anyone's.
	for limit, want := range map[int]string{
		-1:                   "peer airy: a link takes 1 to 10000 mails an hour, not -1",
		MaxPeerMailLimit + 1: "peer airy: a link takes 1 to 10000 mails an hour, not 10001",
	} {
		if _, err := s.AllowPeer(ctx, "airy", PeerAllow{MailLimit: limit}, "jo"); err == nil || err.Error() != want {
			t.Fatalf("a limit of %d = %v", limit, err)
		}
	}
	if _, err := s.AllowPeer(ctx, "airy", PeerAllow{MailLimit: 100}, "yardmaster"); err == nil ||
		err.Error() != "only the operator may take more mail from a peer; this is the crew member yardmaster" {
		t.Fatalf("a crew member raising the limit = %v", err)
	}
	p, err := s.AllowPeer(ctx, "airy", PeerAllow{MailLimit: 100}, "jo")
	if err != nil || p.MailsAnHour() != 100 || p.FilesAnHour() != 100*MaxMailFiles {
		t.Fatalf("the link set to 100 = %+v, %v", p, err)
	}
	if evs, err := s.Events(ctx, "", 1); err != nil || len(evs) != 1 || evs[0].Kind != "peer_allowed" || fmt.Sprint(evs[0].Data["mail_limit"]) != "100" {
		t.Fatalf("peer_allowed = %+v, %v", evs, err)
	}
	// The hour's count stands, and mail is taken again up to the new limit.
	left := 100 - p.Mails
	for i := 0; i < left; i++ {
		if got := receive("yardmaster", "hi"); got != "" {
			t.Fatalf("mail %d under the limit of 100 = %q", p.Mails+i+1, got)
		}
	}
	if got := receive("yardmaster", "hi"); got != RefusedRate {
		t.Fatalf("mail 101 of the hour = %q", got)
	}
	// A refusal by the new limit is told anew, and says that limit.
	if got := told("yardmaster"); len(got) != 2 || !strings.Contains(got[1], "takes 100 mails and asks an hour from airy") || !strings.Contains(got[1], "; 101 so far.") {
		t.Fatalf("the yardmaster was told %q", got)
	}
	if p, err = s.AllowPeer(ctx, "airy", PeerAllow{MailLimit: 1}, "yardmaster"); err != nil || p.MailsAnHour() != 1 || p.FilesAnHour() != MaxMailFiles {
		t.Fatalf("a crew member lowering the limit = %+v, %v", p, err)
	}

	// The files of the hour follow the mail: 8 for the one mail, and the
	// ninth is refused.
	at = at.Add(PeerMailWindow)
	file := func(i int) string {
		t.Helper()
		data := []byte("\x89PNG\r\n\x1a\n" + fmt.Sprintf("picture %d", i))
		sum := sha256.Sum256(data)
		body, _ := json.Marshal(peerFile{BlobRef: BlobRef{Hash: hex.EncodeToString(sum[:]), Type: "image/png", Bytes: int64(len(data))}, Data: data})
		got, err := s.PeerReceive(ctx, "airy", yardkey.Public(k), []PeerMessage{{ID: fmt.Sprintf("f%d", i), Kind: PeerFile, Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Refused
	}
	for i := 0; i < MaxMailFiles; i++ {
		if got := file(i); got != "" {
			t.Fatalf("file %d of the hour = %q", i+1, got)
		}
	}
	if got := file(MaxMailFiles); got != RefusedRate {
		t.Fatalf("file %d on a link that takes 1 mail an hour = %q", MaxMailFiles+1, got)
	}
	// Set back to the default it is no setting.
	if p, err = s.AllowPeer(ctx, "airy", PeerAllow{MailLimit: PeerMailLimit}, "jo"); err != nil || p.MailLimit != 0 || p.MailsAnHour() != PeerMailLimit {
		t.Fatalf("the link set back = %+v, %v", p, err)
	}
}

// mailLinked are two stores, a and b, linked both ways with mail allowed
// each way and each exposing its user mckean to the other. key holds each
// yard's private key, to sign as it does.
func mailLinked(t *testing.T) (a, b *linked, key map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	key = map[string][]byte{}
	open := func(name string) *linked {
		s, err := Open(filepath.Join(t.TempDir(), name+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(context.Background()) })
		k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		key[name] = k
		if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddUser(ctx, "mckean", "jo"); err != nil {
			t.Fatal(err)
		}
		return &linked{Store: s, name: name, key: yardkey.Public(k)}
	}
	a, b = open("a"), open("b")
	for _, l := range [][2]*linked{{a, b}, {b, a}} {
		if _, err := l[0].AddPeer(ctx, Peer{Name: l[1].name, Key: l[1].key}, "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := l[0].AllowPeer(ctx, l[1].name, PeerAllow{Send: []string{PeerMail}, Receive: []string{PeerMail}, Expose: []string{"mckean"}}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return a, b, key
}

// yardMail is the yard's mail to who in the yard's events: what it told of
// mail that was not delivered.
func yardMail(t *testing.T, s *Store, who string) []Event {
	t.Helper()
	evs, err := s.Events(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == "mail" && e.Actor == "yard" && e.Data["to"] == who {
			out = append(out, e)
		}
	}
	return out
}

// Mail over a peer's hour is refused with the hour's limit and when it
// ends, and the sender is told both: its yard gives the mail up with the
// refusal's hour, the writer's mail from the yard names the hour's end,
// and its event keeps them.
func TestPeerMailLimitReachesSender(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })
	a, b, _ := mailLinked(t)
	ctx := context.Background()
	until := at.Add(PeerMailWindow)
	for i := 0; i < PeerMailLimit+1; i++ {
		if _, err := a.MailPeer(ctx, "mckean", "b", fmt.Sprintf("letter %d", i+1), "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := a.PeerOutbox(ctx, "b", 0)
	if err != nil || len(out) != PeerMailLimit+1 {
		t.Fatalf("a's outbox = %d messages, %v", len(out), err)
	}
	for i, m := range out {
		got, err := b.PeerReceive(ctx, "a", a.key, []PeerMessage{m.PeerMessage})
		if err != nil {
			t.Fatal(err)
		}
		if i < PeerMailLimit {
			if got[0].Refused != "" {
				t.Fatalf("mail %d of the hour = %+v", i+1, got[0])
			}
			if err := a.PeerDelivered(ctx, "b", m.Seq); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got[0].Refused != RefusedRate || got[0].Limit != PeerMailLimit || got[0].Until == nil || !got[0].Until.Equal(until) {
			t.Fatalf("mail 31 of the hour = %+v, want rate_limited with %d until %s", got[0], PeerMailLimit, until)
		}
		if err := a.PeerRefusedRate(ctx, "b", m.Seq, got[0].Limit, *got[0].Until); err != nil {
			t.Fatal(err)
		}
	}
	end := until.Local().Format("15:04")
	evs, err := a.Events(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var failed []Event
	for _, e := range evs {
		if e.Kind == "peer_message_failed" {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 || failed[0].Data["reason"] != "peer b: refused by the peer: it takes 30 mails an hour from this yard; send again after "+end {
		t.Fatalf("the failed message = %+v", failed)
	}
	back := yardMail(t, a.Store, "mckean")
	if len(back) != 1 || back[0].Data["undelivered"] != out[PeerMailLimit].ID || back[0].Data["limit"] != float64(PeerMailLimit) {
		t.Fatalf("the yard's mail to the writer = %+v", back)
	}
	text, _ := back[0].Data["text"].(string)
	if !strings.HasSuffix(text, " was not delivered: refused by the peer: it takes 30 mails an hour from this yard; send again after "+end) {
		t.Fatalf("the writer is told %q", text)
	}
	kept, err := time.Parse(time.RFC3339Nano, back[0].Data["until"].(string))
	if err != nil || !kept.Equal(until) {
		t.Fatalf("the mail event keeps until = %v, %v; want %s", back[0].Data["until"], err, until)
	}
	if box, err := a.UserInbox(ctx, "mckean", 0); err != nil || len(box.Mail) != 1 || box.Mail[0].Text != text {
		t.Fatalf("mckean's inbox = %+v, %v", box, err)
	}
}

// Mail over a peer's hour where the peer asked for its mail: what it did
// not take is told with the next question, with the hour, and the sender's
// yard gives it up the same way.
func TestPeerMailLimitReachesSenderByPull(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })
	a, b, key := mailLinked(t)
	ctx := context.Background()
	until := at.Add(PeerMailWindow)
	for i := 0; i < PeerMailLimit+1; i++ {
		if _, err := a.MailPeer(ctx, "mckean", "b", fmt.Sprintf("letter %d", i+1), "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := a.PeerHandOver(ctx, "b", b.key, 0, nil)
	if err != nil || len(page.Messages) != PeerMailLimit+1 {
		t.Fatalf("the handover = %d messages, %v", len(page.Messages), err)
	}
	body, err := json.Marshal(PeerPage{Messages: page.Messages})
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.PeerPulled(ctx, "a", PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(key["a"], 200, "req-1", body), Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if taken := res.Taken[PeerMailLimit]; taken.Refused != RefusedRate || taken.Limit != PeerMailLimit || taken.Until == nil || !taken.Until.Equal(until) {
		t.Fatalf("mail 31 of the hour = %+v, want rate_limited with %d until %s", taken, PeerMailLimit, until)
	}
	last := page.Messages[PeerMailLimit].Seq
	p, err := b.GetPeer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if no := p.Refused[last]; no.Reason != RefusedRate || no.Limit != PeerMailLimit || no.Until == nil || !no.Until.Equal(until) {
		t.Fatalf("what b tells a = %+v, want rate_limited with %d until %s", no, PeerMailLimit, until)
	}
	// b asks again, telling a what it did not take.
	if _, err := a.PeerHandOver(ctx, "b", b.key, last, p.Refused); err != nil {
		t.Fatal(err)
	}
	end := until.Local().Format("15:04")
	back := yardMail(t, a.Store, "mckean")
	if len(back) != 1 || back[0].Data["undelivered"] != page.Messages[PeerMailLimit].ID || back[0].Data["limit"] != float64(PeerMailLimit) {
		t.Fatalf("the yard's mail to the writer = %+v", back)
	}
	text, _ := back[0].Data["text"].(string)
	if !strings.HasSuffix(text, " was not delivered: refused by the peer: it takes 30 mails an hour from this yard; send again after "+end) {
		t.Fatalf("the writer is told %q", text)
	}
	kept, err := time.Parse(time.RFC3339Nano, back[0].Data["until"].(string))
	if err != nil || !kept.Equal(until) {
		t.Fatalf("the mail event keeps until = %v, %v; want %s", back[0].Data["until"], err, until)
	}
}

// A yard upgraded with a refusal still untold reads the reason it stored,
// from before one over the hour carried its limit and when it ends, and
// telling it stores the refusal as now.
func TestPeerRefusedReadsABareReason(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	err = s.write(func(w *wtx) error {
		var p Peer
		if ok, err := get(w.Tx, bPeers, "north", &p); err != nil || !ok {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		m["refused"] = map[string]string{"4": RefusedRate}
		raw, err = json.Marshal(m)
		if err != nil {
			return err
		}
		return w.Bucket(bPeers).Put([]byte("north"), raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPeer(ctx, "north")
	if err != nil || len(p.Refused) != 1 || p.Refused[4].Reason != RefusedRate || p.Refused[4].Limit != 0 || p.Refused[4].Until != nil {
		t.Fatalf("the peer with a bare reason = %+v, %v", p.Refused, err)
	}
	body, err := json.Marshal(PeerPage{Messages: []PeerMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeerPulled(ctx, "north", PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(k, 200, "req-1", body), Body: body, Told: []uint64{4}}); err != nil {
		t.Fatal(err)
	}
	if p, err := s.GetPeer(ctx, "north"); err != nil || len(p.Refused) != 0 {
		t.Fatalf("after it was told: %+v, %v", p.Refused, err)
	}
}
