package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// askYards are two yards for a question across them: mac, where the work
// is and the yardmaster asks, and airy, where mckean sits and decides.
// airyCalled says whether mac can call airy; a laptop behind a router
// cannot be, and asks mac for what waits.
func askYards(t *testing.T, clock *peerClock, airyCalled bool) (mac, airy *peerYard) {
	t.Helper()
	ctx := context.Background()
	mac, airy = newPeerYard(t, "mac", clock), newPeerYard(t, "airy", clock)
	link(t, mac, airy, airyCalled, true)
	_, err := mac.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "none", Config: map[string]any{store.MailPeers: "airy"}}, "jo")
	must(t, err)
	_, err = mac.api.AllowPeer(ctx, "airy", store.PeerAllow{Send: []string{"ask"}, Receive: []string{"ask"}}, "jo")
	must(t, err)
	_, err = airy.api.AddUser(ctx, "mckean", "jo")
	must(t, err)
	_, err = airy.api.AllowPeer(ctx, "mac", store.PeerAllow{Send: []string{"ask"}, Receive: []string{"ask"}, Expose: []string{"mckean"}}, "jo")
	must(t, err)
	return mac, airy
}

// question asks airy's mckean a question on a new bead of mac's, and carries it
// over: it stands in mckean's queue at airy when it returns.
func (y *peerYard) question(t *testing.T, airy *peerYard, options ...string) (bead string, seq uint64) {
	t.Helper()
	ctx := context.Background()
	b, err := y.api.CreateBead(ctx, store.NewBead{Title: "Move the links"}, "yardmaster")
	must(t, err)
	ask := store.Ask{Question: "Move them now?", To: "mckean@airy", Context: []string{"The links file declares the new source."}}
	for _, o := range options {
		ask.Options = append(ask.Options, store.AskOption{Label: o, Consequence: "then " + o})
	}
	seq, err = y.api.AddAsk(ctx, b.ID, ask, "yardmaster")
	must(t, err)
	// Posted by mac, or asked for by airy, whichever the link is.
	y.pass(t)
	airy.pass(t)
	for _, o := range airy.queue(t).Open {
		if o.Bead == b.ID+"@mac" && o.Ask == seq && o.Asker == "yardmaster@mac" {
			return b.ID, seq
		}
	}
	t.Fatalf("the ask on %s does not stand in mckean's queue at airy: %+v", b.ID, airy.queue(t))
	return "", 0
}

func (y *peerYard) queue(t *testing.T) *store.AskQueue {
	t.Helper()
	q, err := y.api.UserAsks(context.Background(), "mckean", "mckean")
	must(t, err)
	return q
}

// decided is the row of mckean's decided asks for the bead, nil when the
// question is open or not there.
func (y *peerYard) decided(t *testing.T, bead string) *store.DecidedAsk {
	t.Helper()
	for _, d := range y.queue(t).Decided {
		if d.Bead == bead {
			return &d
		}
	}
	return nil
}

func (y *peerYard) open(t *testing.T, bead string) bool {
	t.Helper()
	for _, o := range y.queue(t).Open {
		if o.Bead == bead {
			return true
		}
	}
	return false
}

// resolved is the ask on the bead as its yard has it.
func (y *peerYard) resolved(t *testing.T, bead string, seq uint64) store.ResolvedAsk {
	t.Helper()
	notes, err := y.api.Notes(context.Background(), bead)
	must(t, err)
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			return r
		}
	}
	t.Fatalf("bead %s has no ask %d", bead, seq)
	return store.ResolvedAsk{}
}

// yardMail is the text of the mail the yard wrote to name, oldest first.
func (y *peerYard) yardMail(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	for _, e := range y.events(t, "mail") {
		if e.Actor == "yard" && e.Data["to"] == name {
			out = append(out, e.Data["text"].(string))
		}
	}
	return out
}

// A pick, words, Later and Decline given at airy each land on mac's bead
// as the right note, written by mckean@airy, once, whichever yard calls
// the other.
func TestPeerAskAnswersComeHome(t *testing.T) {
	t.Parallel()
	for name, airyCalled := range map[string]bool{"both yards are called": true, "airy asks for what waits": false} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mac, airy := askYards(t, newClock(), airyCalled)

			bead, seq := mac.question(t, airy, "Move them now", "After Friday")
			must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 1, "tonight", "mckean", false))
			if d := airy.decided(t, bead+"@mac"); d == nil || d.State != "answered" || d.Option != 1 || d.OptionLabel != "Move them now" {
				t.Fatalf("airy's queue after the pick = %+v", airy.queue(t))
			}
			airy.pass(t)
			r := mac.resolved(t, bead, seq)
			if r.State != store.AskAnswered || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Option != 1 || r.Current.Answer.Words != "tonight" {
				t.Fatalf("mac's ask after the pick = %+v", r)
			}
			if lines := strings.Join(r.Lines(), "\n"); !strings.Contains(lines, "mckean@airy picked 1 (Move them now): tonight") {
				t.Fatalf("mac shows:\n%s", lines)
			}

			bead, seq = mac.question(t, airy, "Move them now")
			must(t, airy.api.DeferAsk(ctx, bead+"@mac", seq, "mckean"))
			airy.pass(t)
			if r := mac.resolved(t, bead, seq); r.State != store.AskAside || r.Current.Note.Author != "mckean@airy" {
				t.Fatalf("mac's ask after Later = %+v", r)
			}
			if d := airy.decided(t, bead+"@mac"); d == nil || d.State != "put aside" || d.ReturnAt == nil {
				t.Fatalf("airy's queue after Later = %+v", airy.queue(t))
			}
			// And answered early, as a pick like any other.
			must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 1, "", "mckean", false))
			airy.pass(t)
			if r := mac.resolved(t, bead, seq); r.State != store.AskAnswered || len(r.Answers) != 2 {
				t.Fatalf("mac's ask after the pick that followed Later = %+v", r)
			}

			bead, seq = mac.question(t, airy, "Move them now")
			must(t, airy.api.DeclineAsk(ctx, bead+"@mac", seq, "not mine", "mckean"))
			airy.pass(t)
			if r := mac.resolved(t, bead, seq); r.State != store.AskDeclined || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Words != "not mine" {
				t.Fatalf("mac's ask after Decline = %+v", r)
			}

			bead, seq = mac.question(t, airy)
			must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 0, "warmly", "mckean", false))
			airy.pass(t)
			if r := mac.resolved(t, bead, seq); r.State != store.AskAnswered || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Words != "warmly" {
				t.Fatalf("mac's ask after words = %+v", r)
			}

			// Further passes carry nothing twice, and nothing was refused,
			// given up or told.
			for i := 0; i < 2; i++ {
				mac.pass(t)
				airy.pass(t)
			}
			if r := mac.resolved(t, bead, seq); len(r.Answers) != 1 {
				t.Fatalf("an answer landed twice: %+v", r.Answers)
			}
			if n := len(mac.events(t, "peer_message_refused")) + len(airy.events(t, "peer_message_refused")) +
				len(mac.events(t, "peer_message_failed")) + len(airy.events(t, "peer_message_failed")); n != 0 {
				t.Fatalf("%d messages refused or given up", n)
			}
			if len(mac.waiting(t, "airy"))+len(airy.waiting(t, "mac")) != 0 {
				t.Fatal("messages still wait")
			}
			if len(airy.queue(t).Open) != 0 || len(airy.yardMail(t, "mckean"))+len(mac.yardMail(t, "yardmaster")) != 0 {
				t.Fatalf("airy's queue = %+v", airy.queue(t))
			}
		})
	}
}

// One ask picked in both yards: at airy, in mckean's queue, and by hand
// on mac's bead. Both picks land as notes and the newest on the bead
// stands, whichever came first; nothing is lost and nothing is doubled.
func TestPeerAskPickedInBothYards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mac, airy := askYards(t, newClock(), true)
	// The picks by hand below are the operator's: the asks are airy's
	// person's to answer, and nobody else's on mac.
	must(t, mac.api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))

	// airy's pick is on its way while mac picks by hand: it lands after,
	// so it stands.
	bead, seq := mac.question(t, airy, "Move them now", "After Friday")
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 2, "", "mckean", false))
	must(t, mac.api.AddAnswer(ctx, bead, store.Answer{Ask: seq, Option: 1}, "jo"))
	airy.pass(t)
	r := mac.resolved(t, bead, seq)
	if len(r.Answers) != 2 || r.Answers[0].Note.Author != "jo" || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Option != 2 {
		t.Fatalf("mac's ask after both picked, airy's last = %+v", r)
	}
	if d := airy.decided(t, bead+"@mac"); d == nil || d.Option != 2 {
		t.Fatalf("airy's queue = %+v", airy.queue(t))
	}

	// airy's pick lands first, then mac picks by hand: mac's stands. What
	// airy shows is its own pick; nothing of mac's crosses.
	bead, seq = mac.question(t, airy, "Move them now", "After Friday")
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 2, "", "mckean", false))
	airy.pass(t)
	must(t, mac.api.AddAnswer(ctx, bead, store.Answer{Ask: seq, Option: 1}, "jo"))
	r = mac.resolved(t, bead, seq)
	if len(r.Answers) != 2 || r.Current.Note.Author != "jo" || r.Current.Answer.Option != 1 {
		t.Fatalf("mac's ask after both picked, mac's last = %+v", r)
	}
	// A newer pick at airy lands still, and stands.
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 3, "", "mckean", false))
	airy.pass(t)
	mac.pass(t)
	airy.pass(t)
	r = mac.resolved(t, bead, seq)
	if len(r.Answers) != 3 || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Option != 3 {
		t.Fatalf("mac's ask after airy picked again = %+v", r)
	}
	if n := len(mac.events(t, "peer_message_refused")) + len(airy.events(t, "peer_message_failed")); n != 0 {
		t.Fatalf("%d picks refused", n)
	}

	// mac declines by hand while airy's pick is on its way: the pick is
	// refused, naming the state, and never lands. airy marks the question
	// declined, says why by mail, and takes no pick for it any more.
	bead, seq = mac.question(t, airy, "Move them now", "After Friday")
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 1, "", "mckean", false))
	must(t, mac.api.AddAnswer(ctx, bead, store.Answer{Ask: seq, Decline: true, Words: "overtaken"}, "jo"))
	airy.pass(t)
	if r := mac.resolved(t, bead, seq); len(r.Answers) != 1 || r.State != store.AskDeclined {
		t.Fatalf("mac's ask after a late pick = %+v", r)
	}
	if ref := mac.events(t, "peer_message_refused"); len(ref) != 1 || ref[0].Data["reason"] != store.RefusedAskDeclined || ref[0].Data["bead"] != bead {
		t.Fatalf("mac's refusal = %+v", ref)
	}
	d := airy.decided(t, bead+"@mac")
	if d == nil || d.State != "declined" || d.Option != 0 || d.Words != "it was declined in mac's yard" {
		t.Fatalf("airy's queue after the refusal = %+v", airy.queue(t))
	}
	mail := airy.yardMail(t, "mckean")
	if len(mail) != 1 || !strings.HasPrefix(mail[0], `your pick of 1 (Move them now) on "Move them now?" (ask `) ||
		!strings.HasSuffix(mail[0], ", from yardmaster@mac) did not land: it was declined in mac's yard. It takes no further answer.") {
		t.Fatalf("airy's mail = %q", mail)
	}
	if err := airy.api.AnswerAsk(ctx, bead+"@mac", seq, 2, "", "mckean", false); err == nil || !strings.Contains(err.Error(), "takes no further answer") {
		t.Fatalf("a pick at airy on the marked ask = %v", err)
	}
	if len(airy.waiting(t, "mac")) != 0 {
		t.Fatal("airy still has an answer waiting")
	}
}

// The link is down past the tries. An answer given up opens the question
// again at airy, with mail that says the pick did not land; picked again
// once mac is back, it lands. A question given up is told to its asker at
// mac, and stays open on the bead, answerable by hand.
func TestPeerAskLinkDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newClock()
	mac, airy := askYards(t, clock, true)
	mac.d.PeerTries, airy.d.PeerTries = 3, 3
	// The answer by hand below is the operator's: the ask is airy's
	// person's to answer, and nobody else's on mac.
	must(t, mac.api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))

	bead, seq := mac.question(t, airy, "Move them now", "After Friday")
	mac.down.Store(true)
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 1, "", "mckean", false))
	for i := 0; i < 3; i++ {
		if airy.open(t, bead+"@mac") {
			t.Fatalf("the question is open again after %d tries", i)
		}
		airy.pass(t)
		clock.now = clock.now.Add(time.Hour)
	}
	if !airy.open(t, bead+"@mac") || airy.decided(t, bead+"@mac") != nil {
		t.Fatalf("airy's queue after the answer was given up = %+v", airy.queue(t))
	}
	mail := airy.yardMail(t, "mckean")
	if len(mail) != 1 || !strings.Contains(mail[0], "did not land: mac was not reached in 3 tries since ") ||
		!strings.HasSuffix(mail[0], " The question is open again.") {
		t.Fatalf("airy's mail = %q", mail)
	}
	mac.down.Store(false)
	airy.pass(t)
	if r := mac.resolved(t, bead, seq); r.State != store.AskOpen {
		t.Fatalf("an answer given up was posted again: %+v", r)
	}
	must(t, airy.api.AnswerAsk(ctx, bead+"@mac", seq, 2, "", "mckean", false))
	airy.pass(t)
	if r := mac.resolved(t, bead, seq); len(r.Answers) != 1 || r.Current.Note.Author != "mckean@airy" || r.Current.Answer.Option != 2 {
		t.Fatalf("mac's ask after the pick was given again = %+v", r)
	}
	if d := airy.decided(t, bead+"@mac"); d == nil || d.Option != 2 || len(airy.yardMail(t, "mckean")) != 1 {
		t.Fatalf("airy's queue = %+v", airy.queue(t))
	}

	// The other way: the question never reaches airy.
	airy.down.Store(true)
	b, err := mac.api.CreateBead(ctx, store.NewBead{Title: "Name the release"}, "yardmaster")
	must(t, err)
	seq, err = mac.api.AddAsk(ctx, b.ID, store.Ask{Question: "What name?", To: "mckean@airy", Context: []string{"the release."}}, "yardmaster")
	must(t, err)
	for i := 0; i < 3; i++ {
		mac.pass(t)
		clock.now = clock.now.Add(time.Hour)
	}
	told := mac.yardMail(t, "yardmaster")
	if len(told) != 1 || !strings.HasPrefix(told[0], "your question for mckean@airy of ") || !strings.Contains(told[0], "was not delivered: ") ||
		!strings.Contains(told[0], "It stays open on the bead") {
		t.Fatalf("mac's mail to the asker = %q", told)
	}
	airy.down.Store(false)
	mac.pass(t)
	airy.pass(t)
	if airy.open(t, b.ID+"@mac") {
		t.Fatal("a question given up was posted again")
	}
	if r := mac.resolved(t, b.ID, seq); r.State != store.AskOpen {
		t.Fatalf("mac's ask after it was given up = %+v", r)
	}
	must(t, mac.api.AddAnswer(ctx, b.ID, store.Answer{Ask: seq, Words: "Aurora"}, "jo"))
}
