package store_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// away is the flow of a yard whose stage build another yard works.
const away = "build -> review on done | decide on refused | decide on undelivered\nreview -> merged\ndecide -> build\nmerged terminal\n"

// newSendingYard is a yard linked with north that has its stage build, in
// the depot web, worked there: the link carries bead out, the group at-north
// names the yard and a route sends the stage to it. It returns the yard and
// its own public key.
func newSendingYard(t *testing.T) (*peerYard, string) {
	t.Helper()
	y := newPeerYard(t, "http://north.test")
	keyFile := filepath.Join(t.TempDir(), "peer.key")
	k, _, err := yardkey.Ensure(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	y.own.SetYard(keyFile, "v0.9.2")
	if err := y.api.PutDepot(y.ctx, store.Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "away", away)
	if err := y.api.SetFlowPointer(y.ctx, "web", "", "away", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "at-north", Runner: store.PeerRunner, Limit: 2, Config: map[string]any{"peer": "north"}}, "me"); err != nil {
		t.Fatal(err)
	}
	return y, yardkey.Public(k)
}

// send creates a bead in build, claims it for at-north as the dispatcher
// does and has it sent; it returns the bead and its session.
func (y *peerYard) send(t *testing.T, nb store.NewBead) (*store.Bead, *store.Session) {
	t.Helper()
	nb.Depot, nb.Stage = "web", "build"
	b, err := y.api.CreateBead(y.ctx, nb, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return b, y.claim(t, b.ID)
}

func (y *peerYard) claim(t *testing.T, id string) *store.Session {
	t.Helper()
	sess, err := y.api.Claim(y.ctx, id, "at-north", "build", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.PeerBeadSend(y.ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := y.api.MarkRunning(y.ctx, sess.ID, map[string]any{"peer": "north"}); err != nil {
		t.Fatal(err)
	}
	return sess
}

func (y *peerYard) outbox(t *testing.T) []store.PeerOut {
	t.Helper()
	out, err := y.api.PeerOutbox(y.ctx, "north", 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// settled is the bead once the store's settler has applied the outcome of
// its stay at a peer: the write that ended the stay returns before that
// move, so a test that asserts it waits on the effect.
func (y *peerYard) settled(t *testing.T, id, stage string) *store.Bead {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := y.api.GetBead(y.ctx, id)
		if err == nil && got.Stage == stage {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("bead %s not in %s: %+v, %v", id, stage, got, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A peer group names a yard this one is linked with, whose link carries
// beads out, and has no members.
func TestPeerGroup(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	put := func(g store.Group) error {
		g.Name, g.Runner = "at-north", store.PeerRunner
		return y.api.PutGroup(y.ctx, g, "me")
	}
	for want, g := range map[string]store.Group{
		"--set peer=<peer>":             {},
		"peer south: not found":         {Config: map[string]any{"peer": "south"}},
		"does not carry bead out":       {Config: map[string]any{"peer": "north"}},
		"members are only for manual":   {Config: map[string]any{"peer": "north"}, Members: []string{"jo"}},
		"yardr peer allow north --send": {Config: map[string]any{"peer": "north"}},
	} {
		if err := put(g); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a peer group %+v: %v, want %q", g, err, want)
		}
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if err := put(store.Group{Config: map[string]any{"peer": "north", "tries": "none"}}); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("tries=none: %v", err)
	}
	if err := put(store.Group{Limit: 3, Config: map[string]any{"peer": "north", "tries": float64(2)}}); err != nil {
		t.Fatal(err)
	}
	g, err := y.api.GetGroup(y.ctx, "at-north")
	if err != nil || g.AtPeer() != "north" || g.Limit != 3 {
		t.Fatalf("the group = %+v, %v", g, err)
	}
	if other := (store.Group{Runner: "exec", Config: map[string]any{"peer": "north"}}); other.AtPeer() != "" {
		t.Fatal("a group of another runner sends to a peer")
	}
}

// A train crosses a link as a train: the message says the type in the word
// the yard has for it, and the other yard takes it for a task either way.
func TestPeerBeadSendSaysTheType(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "The export", Type: store.TypeTrain, Depot: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	y.claim(t, b.ID)
	out := y.outbox(t)
	if len(out) != 1 {
		t.Fatalf("the outbox = %+v", out)
	}
	var m store.PeerBeadMessage
	if err := json.Unmarshal(out[0].Body, &m); err != nil || m.Type != store.TypeTrain {
		t.Fatalf("the bead message = %+v, %v; want type train", m, err)
	}
}

// What is sent is the bead as the other yard is to work it: the message of
// docs/peers.md, under an id made of the bead and the event that put it in
// the stage, so a second claim in the same stay sends nothing new.
func TestPeerBeadSend(t *testing.T) {
	t.Parallel()
	y, own := newSendingYard(t)
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Add the export", Body: "As a CSV.", Type: "feature", Priority: 1, Depot: "web", Stage: "build",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: "https://linear.app/x/issue/YRD-1"}}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := y.api.AddNote(y.ctx, b.ID, text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	sess := y.claim(t, b.ID)
	out := y.outbox(t)
	if len(out) != 1 || out[0].Kind != store.PeerBead || out[0].Re != "" || out[0].Tries != store.PeerBeadTries || !strings.HasPrefix(out[0].ID, b.UUID+"-") {
		t.Fatalf("the outbox after the claim = %+v", out)
	}
	var m store.PeerBeadMessage
	if err := json.Unmarshal(out[0].Body, &m); err != nil {
		t.Fatal(err)
	}
	if m.V != 1 || m.Yardr != "v0.9.2" || m.Bead != b.ID || m.Stage != "build" || m.Title != "Add the export" || m.Body != "As a CSV." || m.Type != "feature" ||
		m.Priority != 1 || !slices.Equal(m.Outcomes, []string{"done", "refused", "undelivered"}) || !slices.Equal(m.Path, []string{own}) ||
		len(m.Notes) != 2 || m.Notes[0].Text != "first" || m.Notes[0].Author != "yardmaster" || m.Notes[1].Text != "second" || m.NotesLeftOut != 0 ||
		len(m.Links) != 1 || m.Links[0].URL != "https://linear.app/x/issue/YRD-1" || m.Links[0].Kind != "tracks" {
		t.Fatalf("the bead message = %+v", m)
	}
	if strings.Contains(string(out[0].Body), b.UUID) {
		t.Fatal("the message's body names the bead by its UUID")
	}
	sent := peerEvents(t, y.api, "peer_bead_sent")
	if len(sent) != 1 || sent[0].Data["id"] != out[0].ID || sent[0].Data["session"] != sess.ID || *sent[0].Bead != b.ID {
		t.Fatalf("peer_bead_sent = %+v", sent)
	}
	for _, e := range sent {
		if raw, _ := json.Marshal(e.Data); strings.Contains(string(raw), "As a CSV") {
			t.Fatalf("an event carries the bead's body: %s", raw)
		}
	}

	// The daemon died while it started the session: the claim is made
	// again, and the message is the one that waits already.
	if err := y.api.EndSession(y.ctx, sess.ID, "failed", "start interrupted"); err != nil {
		t.Fatal(err)
	}
	y.claim(t, b.ID)
	if again := y.outbox(t); len(again) != 1 || again[0].ID != out[0].ID || len(peerEvents(t, y.api, "peer_bead_sent")) != 1 {
		t.Fatalf("the outbox after a second claim in the stay = %+v", again)
	}
	// And once north has it, nothing is queued for a third.
	if err := y.api.PeerDelivered(y.ctx, "north", out[0].Seq); err != nil {
		t.Fatal(err)
	}
	live, _ := y.api.Sessions(y.ctx, false)
	if err := y.api.EndSession(y.ctx, live[0].ID, "failed", "start interrupted"); err != nil {
		t.Fatal(err)
	}
	y.claim(t, b.ID)
	if len(y.outbox(t)) != 0 || len(peerEvents(t, y.api, "peer_bead_sent")) != 1 {
		t.Fatal("a bead north has was queued again")
	}
	// A bead that is no session's is sent by nobody.
	if _, err := y.api.PeerBeadSend(y.ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "does not work bead") {
		t.Fatalf("sending for a session that ended: %v", err)
	}
	// It is the yard's own to send: not by hand.
	if _, err := y.api.PeerSend(y.ctx, "north", store.PeerBead, nil, "jo"); err == nil {
		t.Fatal("a bead message was sent by hand")
	}
}

// A bead too large to cross a link is not sent: its stage ends as refused,
// and the note says why. Older notes that do not fit are left out, and
// counted, as a return's are.
func TestPeerBeadTooLargeToSend(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	big, _ := y.send(t, store.NewBead{Title: "Too much", Body: strings.Repeat("x", store.MaxPeerBeadBody+1)})
	got := y.settled(t, big.ID, "decide")
	if len(y.outbox(t)) != 0 {
		t.Fatalf("a bead too large to send = %+v; outbox %d", got, len(y.outbox(t)))
	}
	notes, _ := y.api.Notes(y.ctx, big.ID)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "This yard did not send this bead to north: it is too large: ") {
		t.Fatalf("its note = %+v", notes)
	}
	// It was not sent, so nothing says it was at north or that north
	// refused it, and it has no row for a bead there.
	away := got.Away()
	if away == nil || away.Outcome != store.OutcomeRefused || !away.Unsent || !strings.HasPrefix(away.Line(), "not sent to north "+away.At.Local().Format("01-02 15:04")+": it is too large: ") {
		t.Fatalf("what it says = %+v", away)
	}
	if strings.Contains(away.Line(), "was at") || strings.Contains(away.Line(), "refused") || len(got.PeerLinks()) != 0 {
		t.Fatalf("a bead not sent reads as sent: %q, rows %+v", away.Line(), got.PeerLinks())
	}

	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Many notes", Depot: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := y.api.AddNote(y.ctx, b.ID, fmt.Sprintf("%d %s", i, strings.Repeat("n", store.MaxPeerNoteBytes)), "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	y.claim(t, b.ID)
	out := y.outbox(t)
	var m store.PeerBeadMessage
	if len(out) != 1 || json.Unmarshal(out[0].Body, &m) != nil {
		t.Fatalf("the outbox = %+v", out)
	}
	if len(m.Notes)+m.NotesLeftOut != 10 || m.NotesLeftOut == 0 || !strings.HasPrefix(m.Notes[len(m.Notes)-1].Text, "9 ") ||
		!strings.Contains(m.Notes[0].Text, "[cut: ") || len(out[0].Body) > store.MaxPeerBody {
		t.Fatalf("the notes sent: %d, %d left out, body %d bytes", len(m.Notes), m.NotesLeftOut, len(out[0].Body))
	}
}

// A bead can pass its field limits but still exceed the peer message limit
// after its links are included. That refusal also happens before queueing.
func TestPeerBeadMessageTooLargeToSend(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Large link", Depot: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		url := fmt.Sprintf("https://tracker.test/%d/", i) + strings.Repeat("x", 30_000)
		if _, err := y.api.AddLink(y.ctx, b.ID, store.NewLink{Kind: store.LinkRelates, Source: "tracker", URL: url}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	y.claim(t, b.ID)
	got := y.settled(t, b.ID, "decide")
	if got.Away() == nil || !got.Away().Unsent || !strings.HasPrefix(got.Away().Detail, "it is too large: peer message ") || len(y.outbox(t)) != 0 {
		t.Fatalf("oversized peer message = %+v; outbox %+v", got, y.outbox(t))
	}
	if len(got.PeerLinks()) != 0 {
		t.Fatalf("a message not queued made a peer link: %+v", got.PeerLinks())
	}
	ref := peerEvents(t, y.api, "peer_bead_refused")
	if len(ref) != 1 || ref[0].Data["sent"] != false {
		t.Fatalf("refusal event = %+v", ref)
	}
	notes, _ := y.api.Notes(y.ctx, b.ID)
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Text, "This yard did not send this bead to north: it is too large: peer message ") {
		t.Fatalf("refusal note = %+v", notes)
	}
}

// A bead sent where nothing can carry it is not sent: its stage ends as
// undelivered when the link carries no bead out, or the peer is linked no
// more, and the line says not sent, with no row for a bead there. A later
// sending this yard refuses keeps no row either: there was no stay.
func TestPeerBeadNotSentWithoutALink(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	y.allow(t, store.PeerAllow{NoSend: []string{"bead"}})
	big, _ := y.send(t, store.NewBead{Title: "Too much", Body: strings.Repeat("x", store.MaxPeerBeadBody+1)})
	got := y.settled(t, big.ID, "decide")
	if len(y.outbox(t)) != 0 {
		t.Fatalf("a bead whose link carries nothing out = %+v; outbox %d", got, len(y.outbox(t)))
	}
	away := got.Away()
	if away == nil || away.Outcome != store.OutcomeUndelivered || !away.Unsent ||
		!strings.HasPrefix(away.Line(), "not sent to north "+away.At.Local().Format("01-02 15:04")+": the link with north does not carry bead out") {
		t.Fatalf("what it says = %+v", away)
	}
	if strings.Contains(away.Line(), "was at") || len(got.PeerLinks()) != 0 {
		t.Fatalf("a bead not sent reads as sent: %q, rows %+v", away.Line(), got.PeerLinks())
	}
	und := peerEvents(t, y.api, "peer_bead_undelivered")
	if len(und) != 1 || und[0].Data["sent"] != false {
		t.Fatalf("undelivered event = %+v", und)
	}
	notes, _ := y.api.Notes(y.ctx, big.ID)
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Text, "This yard did not send this bead to north: the link with north does not carry bead out") {
		t.Fatalf("its note = %+v", notes)
	}
	// The word allowed again, the bead goes back and this yard refuses
	// it for its size: still no stay to name, so still no row.
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if _, err := y.api.Advance(y.ctx, big.ID, "build", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.claim(t, big.ID)
	got = y.settled(t, big.ID, "decide")
	if away := got.Away(); away == nil || away.Outcome != store.OutcomeRefused || !away.Unsent || away.Stayed != nil || len(got.PeerLinks()) != 0 {
		t.Fatalf("a bead refused after one not sent = %+v, rows %+v", away, got.PeerLinks())
	}

	// No link at all: the peer is gone.
	y2, _ := newSendingYard(t)
	if err := y2.api.DeletePeer(y2.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	lost, _ := y2.send(t, store.NewBead{Title: "Nowhere to go"})
	got = y2.settled(t, lost.ID, "decide")
	away = got.Away()
	if away == nil || away.Outcome != store.OutcomeUndelivered || !away.Unsent ||
		!strings.HasPrefix(away.Line(), "not sent to the yard that was north (link removed) ") ||
		!strings.Contains(away.Line(), "this yard is not linked with a yard north any more") {
		t.Fatalf("what it says = %+v", away)
	}
	if strings.Contains(away.Line(), "was at") || len(got.PeerLinks()) != 0 {
		t.Fatalf("a bead not sent reads as sent: %q, rows %+v", away.Line(), got.PeerLinks())
	}
	und = peerEvents(t, y2.api, "peer_bead_undelivered")
	if len(und) != 1 || und[0].Data["sent"] != false {
		t.Fatalf("undelivered event = %+v", und)
	}
	notes, _ = y2.api.Notes(y2.ctx, lost.ID)
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Text, "This yard did not send this bead to north: this yard is not linked with a yard north any more") {
		t.Fatalf("its note = %+v", notes)
	}
}

// hangingGit is a stand-in git before the real one that waits until
// release is called for calls in the checkout repo, and hands on
// everything else: a depot's repository that answers slowly. asked is
// its calls in that checkout, as the stand-in saw them. A call also ends
// when the test binary is gone, so none is left waiting by a test that
// failed with git held.
type hangingGit struct {
	t   *testing.T
	dir string
}

func hangGit(t *testing.T, repo string) *hangingGit {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	h := &hangingGit{t: t, dir: t.TempDir()}
	scripttest.Write(t, filepath.Join(h.dir, "git"), "#!/bin/sh\ndir=.\n[ \"${1:-}\" = -C ] && dir=$2 && shift 2\n"+
		"if [ \"$dir\" = \""+repo+"\" ]; then echo \"$*\" >>"+h.dir+"/asked\nwhile [ ! -f "+h.dir+"/release ]; do kill -0 \"$YARDR_TEST_PID\" 2>/dev/null || exit 0; sleep 0.05; done; fi\n"+
		"exec \"$YARDR_TEST_GIT\" -C \"$dir\" \"$@\"\n")
	t.Setenv("YARDR_TEST_GIT", real)
	// YARDR_TEST_PID is the test binary's own pid: the stand-in's wait ends
	// when it is gone, since a killed binary runs no cleanup.
	t.Setenv("YARDR_TEST_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("PATH", h.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A test that failed with git held lets what waits on it end before its
	// directories go.
	t.Cleanup(h.release)
	return h
}

func (h *hangingGit) release() {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, "release"), nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// waitAsked waits until git has been called in the held checkout: the
// effect a test waits on, never the wall time a call takes.
func (h *hangingGit) waitAsked() {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(h.dir, "asked")); err == nil && len(b) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("git was never called in the held checkout")
}

// holdSettle sends a bead of the yard y to north, which works its stage
// build, and takes north's return of it with git held in the depot's
// repository, where the bead's flow is. It returns the bead and the shim
// once the write is back and the settler is in git, waiting on it.
func (y *peerYard) holdSettle(t *testing.T) (*store.Bead, *hangingGit) {
	t.Helper()
	dir := gitRepo(t, map[string]string{".yardr/flows/default.flow": away})
	keyFile := filepath.Join(t.TempDir(), "peer.key")
	if _, _, err := yardkey.Ensure(keyFile); err != nil {
		t.Fatal(err)
	}
	y.own.SetYard(keyFile, "v0.9.2")
	if err := y.api.PutDepot(y.ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "at-north", Runner: store.PeerRunner, Limit: 2, Config: map[string]any{"peer": "north"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, _ := y.send(t, store.NewBead{Title: "Add the export"})
	// A commit the store's cache has not read: the next resolve of the
	// depot's flows runs git, which the shim below holds.
	commitFiles(t, dir, map[string]string{".yardr/flows/alt.flow": "build -> review\nreview terminal\n"})
	git := hangGit(t, dir)
	re := y.outbox(t)[0].ID
	body, _ := json.Marshal(map[string]any{"v": 1, "bead": "web-0wzb", "outcome": "done"})
	if got := y.take(t, store.PeerMessage{ID: "r1", Kind: store.PeerBeadReturn, Re: re, Body: body}); got[0].Refused != "" {
		t.Fatalf("the return = %+v", got)
	}
	// The write is back and did its part: the return is logged, and the
	// settler is now in git, which hangs until released.
	if evs := y.kindEvents(t, b.ID, "peer_bead_returned"); len(evs) != 1 || evs[0].Data["outcome"] != "done" {
		t.Fatalf("peer_bead_returned = %+v", evs)
	}
	git.waitAsked()
	if got := mustBead(t, y, b.ID); got.Stage != "build" {
		t.Fatalf("the bead while its settle waits on git = %+v", got)
	}
	return b, git
}

// A peer return is settled outside its write, and a close cuts a settle in
// its repository read while leaving the stay owed for the next open.
func TestPeerBeadReturnSettlesOffTheWrite(t *testing.T) {
	t.Run("settles after the repository read", func(t *testing.T) {
		// Serial: it shims git on the process PATH, which parallel tests cannot share.
		y := newPeerYard(t, "http://north.test")
		b, git := y.holdSettle(t)
		git.release()
		// Moved on with no further write: this test's reads are its only
		// calls since the return.
		got := y.settled(t, b.ID, "review")
		adv := y.kindEvents(t, b.ID, "advanced")
		if len(adv) != 1 || adv[0].Actor != "peer:north" || adv[0].Data["outcome"] != "done" || adv[0].Data["from"] != "build" || adv[0].Data["to"] != "review" {
			t.Fatalf("advanced = %+v", adv)
		}
		if away := got.Away(); away.Outcome != "done" || !strings.HasPrefix(away.Line(), "was at north") {
			t.Fatalf("the bead after its return = %+v, %q", got, away.Line())
		}
	})

	t.Run("close cuts the repository read", func(t *testing.T) {
		// Serial: it shims git on the process PATH, which parallel tests cannot share.
		path := filepath.Join(t.TempDir(), "yard.db")
		// Closed below, not by a cleanup that would close it a second time.
		first, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		y := newPeerYardOn(t, "http://north.test", first, first)
		b, git := y.holdSettle(t)
		closed := make(chan error, 1)
		go func() { closed <- first.Close(y.ctx) }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second): // a fuse: git is held until released
			t.Fatal("Close waited for the settler's read of the depot's repository")
		}
		// What the close cut is owed still: the next open takes it up.
		again := storetest.Open(t, path)
		y.api, y.own = again, again
		git.release()
		y.settled(t, b.ID, "review")
		if adv := y.kindEvents(t, b.ID, "advanced"); len(adv) != 1 || adv[0].Actor != "peer:north" || adv[0].Data["to"] != "review" {
			t.Fatalf("advanced = %+v", adv)
		}
	})
}

// Where a bead has been comes with it and goes on with it: a bead that
// arrives with a path is sent on with that path and this yard's key, one
// from a yardr that said none with its sender's. A yard that finds itself
// on the path refuses the bead, and so does one it would reach as the
// ninth.
func TestPeerBeadPath(t *testing.T) {
	t.Parallel()
	y, own := newSendingYard(t)
	if _, err := y.api.AddUser(y.ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "incoming", "build -> returned on done\nreturned terminal\n")
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 50}, "jo"); err != nil {
		t.Fatal(err)
	}
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("yard-%02d-%s", i, strings.Repeat("k", 32))
		}
		return out
	}
	with := func(path any) func(map[string]any) {
		return func(b map[string]any) { b["path"] = path }
	}
	for name, tc := range map[string]struct {
		path any
		want string
	}{
		"a loop":                   {[]string{own, y.key}, store.RefusedLoop},
		"a loop the sender hid":    {[]string{keys(1)[0], own}, store.RefusedLoop},
		"nine yards":               {append(keys(8), y.key), store.RefusedTooFar},
		"eight and the sender":     {keys(8), store.RefusedTooFar},
		"what is no key":           {[]string{"not a key"}, store.RefusedNotBead},
		"eight yards":              {append(keys(7), y.key), ""},
		"seven without the sender": {keys(7), ""},
		"none, an older yardr":     {nil, ""},
	} {
		id := "p-" + strings.ReplaceAll(name, " ", "-")[:min(len(name), 20)]
		id = strings.NewReplacer(",", "", ".", "").Replace(id)
		theirs := "web-" + id[2:8]
		m := beadMsg(id, theirs, nil)
		if tc.path != nil {
			m = beadMsg(id, theirs, with(tc.path))
		}
		if got := y.take(t, m); got[0].Refused != tc.want {
			t.Errorf("%s: taken as %+v, want refused %q", name, got[0], tc.want)
		}
	}
	if store.PeerRefusal("north", "bead", store.RefusedLoop) == store.PeerRefusal("north", "bead", "unheard-of") ||
		!strings.Contains(store.PeerRefusal("north", "bead", store.RefusedTooFar), "8") {
		t.Fatal("the sender has no words for a loop or a path too long")
	}

	// Sent on, each says where it has been: its path, with the sender it
	// came from last whatever it wrote, and this yard.
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{Depot: "web", Stage: "build"})
	if err != nil || len(bs) != 3 {
		t.Fatalf("the beads that were taken = %d, %v", len(bs), err)
	}
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "at-north", Runner: store.PeerRunner, Limit: 5, Config: map[string]any{"peer": "north"}}, "me"); err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{9: true, 2: true} // eight and this yard twice over, the sender alone and this yard
	lens := []int{}
	for _, b := range bs {
		y.claim(t, b.ID)
	}
	for _, o := range y.outbox(t) {
		var m store.PeerBeadMessage
		if json.Unmarshal(o.Body, &m) != nil || m.Path[len(m.Path)-1] != own || m.Path[len(m.Path)-2] != y.key {
			t.Fatalf("a bead sent on says it has been at %v", m.Path)
		}
		lens = append(lens, len(m.Path))
		if !want[len(m.Path)] {
			t.Fatalf("a bead sent on has a path of %d yards", len(m.Path))
		}
	}
	slices.Sort(lens)
	if !slices.Equal(lens, []int{2, 9, 9}) {
		t.Fatalf("the paths sent on are %v yards long", lens)
	}
}

// A return that is none is refused, and one for a bead this yard has not
// sent changes nothing.
func TestPeerBeadReturnsThatAreNone(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	b, _ := y.send(t, store.NewBead{Title: "Add the export"})
	id := y.outbox(t)[0].ID
	ret := func(mid, re string, body map[string]any) store.PeerMessage {
		raw, _ := json.Marshal(body)
		return store.PeerMessage{ID: mid, Kind: store.PeerBeadReturn, Re: re, Body: raw}
	}
	for name, tc := range map[string]struct {
		m    store.PeerMessage
		want string
	}{
		"another version": {ret("r1", id, map[string]any{"v": 2, "bead": "web-x1", "outcome": "done"}), store.RefusedVersion},
		"no outcome":      {ret("r2", id, map[string]any{"v": 1, "bead": "web-x1"}), store.RefusedNotBead},
		"an outcome that is no word": {ret("r3", id, map[string]any{"v": 1, "bead": "web-x1", "outcome": "done\nheld: no"}),
			store.RefusedNotBead},
	} {
		if got := y.take(t, tc.m); got[0].Refused != tc.want {
			t.Errorf("%s: %+v, want refused %q", name, got[0], tc.want)
		}
	}
	// For a bead that is not there, and for no bead at all: taken, logged
	// as stale, nothing moved.
	y.take(t, ret("r4", "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee-4", map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"}),
		ret("r5", "nothing", map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"}))
	stale := 0
	for _, e := range peerEvents(t, y.api, "peer_bead_returned") {
		if e.Data["stale"] == true {
			stale++
		}
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); stale != 2 || got.Stage != "build" || got.Group == nil {
		t.Fatalf("after returns for no bead: %d stale, bead %+v", stale, got)
	}
	// A link that sends no beads takes no return of one.
	y.allow(t, store.PeerAllow{NoSend: []string{"bead"}})
	if got := y.take(t, ret("r6", id, map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"})); got[0].Refused != store.RefusedKind {
		t.Fatalf("a return over a link that sends no beads = %+v", got)
	}
}

// The map of a flow says which stage another yard works, and which yard.
func TestFlowMapOfAPeerStage(t *testing.T) {
	t.Parallel()
	y, _ := newSendingYard(t)
	if _, err := y.api.AddRoute(y.ctx, store.Route{Stage: "build", Group: "at-north"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Add the export", Depot: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	m, err := y.api.FlowMap(y.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stages[0].Who != store.WhoPeer || m.Stages[0].Yard != "north" || !strings.Contains(m.Text(false), "> build    peer north") {
		t.Fatalf("the map = %+v\n%s", m.Stages[0], m.Text(false))
	}
}
