package dispatch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// standInLeader plays `yardr harness login claude --json` as the yard runs
// it: its needs on one pipe, its answers on the other, its input waited
// for in the store as the real leader waits (LoginWait), so the value of a
// secret ask reaches it the way it reaches the real one. The code good#s1
// logs it in; fail ends the next login with an error line, as a login
// whose wait ran out does.
type standInLeader struct {
	s      store.API
	mu     sync.Mutex
	starts int
	chosen []string
	given  []string
	fail   bool
	// gone is closed by a leader that was stopped (its stdin closed).
	gone chan struct{}
}

func (l *standInLeader) lead(ctx context.Context, kind string) (*dispatch.LoginLeader, error) {
	l.mu.Lock()
	l.starts++
	fail := l.fail
	l.gone = make(chan struct{})
	gone := l.gone
	l.mu.Unlock()
	needsR, needsW := io.Pipe()
	ansR, ansW := io.Pipe()
	ended := make(chan error, 1)
	go func() {
		err := l.play(ctx, kind, fail, needsW, bufio.NewScanner(ansR))
		_ = needsW.Close()
		_ = ansR.Close()
		close(gone)
		ended <- err
	}()
	return &dispatch.LoginLeader{Needs: needsR, Answers: ansW, Wait: func() error { return <-ended }}, nil
}

func (l *standInLeader) play(ctx context.Context, kind string, fail bool, out io.Writer, in *bufio.Scanner) error {
	say := func(v any) { b, _ := json.Marshal(v); _, _ = out.Write(append(b, '\n')) }
	answers := make(chan map[string]any)
	go func() {
		defer close(answers)
		for in.Scan() {
			var a map[string]any
			_ = json.Unmarshal(in.Bytes(), &a)
			answers <- a
		}
	}()
	say(map[string]any{"need": "choose", "text": "How do you log in to Claude Code?", "options": []string{"Claude subscription", "Anthropic Console (API usage billing)"}})
	a, ok := <-answers
	if !ok {
		return errors.New("stdin closed")
	}
	l.mu.Lock()
	l.chosen = append(l.chosen, fmt.Sprint(a["choose"]))
	l.mu.Unlock()
	say(map[string]any{"need": "open_url", "url": "https://claude.com/cai/oauth/authorize?code=true&state=s1"})
	if fail {
		say(map[string]any{"error": "no input was given within 10m0s: the login was stopped"})
		return errors.New("exit status 1")
	}
	id := kind + "-wait1"
	waitCtx, stop := context.WithCancel(ctx)
	defer stop()
	value := make(chan string, 1)
	go func() {
		if v, err := l.s.LoginWait(waitCtx, id); err == nil {
			value <- v
		}
	}()
	if err := l.s.LoginWaitReady(waitCtx, id); err != nil {
		return err
	}
	say(map[string]any{"need": "input", "text": "Paste code here if prompted >", "secret": true, "login": id})
	var v string
	select {
	case v = <-value:
	case _, ok := <-answers:
		if !ok {
			return errors.New("stdin closed")
		}
		return errors.New("an answer on stdin: the yard writes no value there")
	}
	l.mu.Lock()
	l.given = append(l.given, v)
	l.mu.Unlock()
	if v != "good#s1" {
		say(map[string]any{"error": "claude auth login: exit status 1: Login failed: Request failed with status code 400"})
		return errors.New("exit status 1")
	}
	say(map[string]any{"need": "confirm", "text": "Check that Claude Code answers?"})
	if a, ok = <-answers; !ok || a["confirm"] == nil {
		return errors.New("the check was not answered")
	}
	say(map[string]any{"done": true, "kind": kind, "proved": false, "try": map[string]any{"state": "not tried"}})
	return nil
}

func (l *standInLeader) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

// loginSetup is a yard whose yardmaster sits at Claude Code's login
// screen, with an operator, op, and a stand-in leader.
func loginSetup(t *testing.T) (store.API, *crewRunner, *dispatch.Dispatcher, *standInLeader) {
	t.Helper()
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "claude"}})
	ctx := context.Background()
	must(t, s.SetSetting(ctx, store.OperatorSetting, "op", "op"))
	l := &standInLeader{s: s}
	d.LeadLogin = l.lead
	d.HarnessInstalled = func(kind string) bool { return kind == "claude" }
	f.login = "Select login method:"
	f.input = "1. Claude account with subscription · Pro, Max, Team, or Enterprise"
	return s, f, d, l
}

// openAsks are op's open asks on the yardmaster's journal.
func openAsks(t *testing.T, s store.API) []store.ResolvedAsk {
	t.Helper()
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	notes, err := s.Notes(ctx, r.Journal)
	must(t, err)
	var out []store.ResolvedAsk
	for _, a := range store.ResolveAsks(notes, time.Now()) {
		if a.State == store.AskOpen {
			out = append(out, a)
		}
	}
	return out
}

// passUntil runs passes until cond holds, as the daemon's loop runs one
// on each write and on each line the leader prints.
func passUntil(t *testing.T, d *dispatch.Dispatcher, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		must(t, d.Reconcile(context.Background()))
	}
}

// The yardmaster of a hosted yard sat at Claude Code's login screen, and
// nobody led the login: the yardmaster that was to relay it is that
// harness's agent (yardr-302g). The yard leads it: one ask how, then the
// page and a secret ask for the code, which reaches the leader alone, and
// the yardmaster starts again with its first prompt. Mail while the login
// is open makes no second login.
func TestYardLeadsHarnessLoginThroughAsks(t *testing.T) {
	t.Parallel()
	s, f, d, l := loginSetup(t)
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	pick := openAsks(t, s)[0]
	if pick.Ask.To != "op" || len(pick.Ask.Options) < 2 || pick.Ask.Options[0].Label != "Claude Code: Claude subscription" {
		t.Fatalf("first ask: %+v", pick.Ask)
	}
	if l.count() != 0 {
		t.Fatalf("a leader ran before the ask was answered")
	}
	// Held nudges while the ask is open: no second ask, no leader.
	must(t, s.Mail(ctx, "yardmaster", "hi", "op"))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n := len(openAsks(t, s)); n != 1 || l.count() != 0 || len(f.nudges) != 0 {
		t.Fatalf("after mail: %d open asks, %d leaders, nudges %v", n, l.count(), f.nudges)
	}

	must(t, s.AnswerAsk(ctx, r.Journal, pick.Note.Seq, 1, "", "op"))
	passUntil(t, d, "a secret ask for the code", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret != nil
	})
	input := openAsks(t, s)[0]
	if input.Ask.Secret.Login != "claude-wait1" {
		t.Fatalf("the secret ask names wait %q", input.Ask.Secret.Login)
	}
	if !strings.Contains(strings.Join(input.Ask.Context, "\n"), "https://claude.com/cai/oauth/authorize?code=true&state=s1") {
		t.Errorf("the page is not in the ask: %q", input.Ask.Context)
	}
	if _, err := s.GiveSecret(ctx, r.Journal, input.Note.Seq, map[string]string{"INPUT": "good#s1"}, "op"); err != nil {
		t.Fatal(err)
	}
	passUntil(t, d, "an ask whether to check", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret == nil && a[0].Note.Seq != pick.Note.Seq
	})
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 2, "", "op"))
	// The pane stays at the login screen, as Claude Code's does when it was
	// logged in elsewhere, until the yard starts it again: logged in, and
	// given its first prompt.
	passUntil(t, d, "the login told as made", func() bool {
		_, ev := kindCount(t, s, r.Journal, "login_led")
		return ev.Data["step"] == "logged_in"
	})
	f.mu.Lock()
	f.login, f.input, f.sent = "", "", true
	f.mu.Unlock()
	passUntil(t, d, "the yardmaster started again with its first prompt", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, n := range f.nudges {
			if strings.HasSuffix(n, ": prime yourself") {
				return true
			}
		}
		return false
	})
	if l.count() != 1 {
		t.Errorf("leaders started: %d, want 1", l.count())
	}
	if l.chosen[0] != "Claude subscription" || len(l.given) != 1 || l.given[0] != "good#s1" {
		t.Errorf("the leader was answered %v and given %d values", l.chosen, len(l.given))
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginAskKey] != nil {
		t.Errorf("the login's mark is left on the handle: %v", r.Handle[dispatch.HarnessLoginAskKey])
	}

	// Nothing of the code stands anywhere in the yard.
	evs, err := s.EventsSince(ctx, 0, 10000)
	must(t, err)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "good#s1") {
			t.Errorf("event %s holds the code: %s", e.Kind, b)
		}
	}
	notes, err := s.Notes(ctx, r.Journal)
	must(t, err)
	for _, n := range notes {
		if strings.Contains(n.Text, "good#s1") {
			t.Errorf("note %d holds the code", n.Seq)
		}
	}
	if _, ev := kindCount(t, s, r.Journal, "login_led"); fmt.Sprint(ev.Data["restarted"]) != "[crew:yardmaster]" {
		t.Errorf("restarted %v, want the yardmaster", ev.Data["restarted"])
	}
}

// A login nobody finishes ends with its open ask taken back, and is not
// offered again on every pass: only once mail comes after it ended.
func TestLedLoginThatEndsIsOfferedOnNextMail(t *testing.T) {
	t.Parallel()
	s, _, d, l := loginSetup(t)
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	l.fail = true

	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 1, "", "op"))
	passUntil(t, d, "the login's end told", func() bool {
		_, ev := kindCount(t, s, r.Journal, "login_led")
		return ev.Data["step"] == "failed"
	})
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n := len(openAsks(t, s)); n != 0 {
		t.Fatalf("%d asks open after the login ended, want none until mail comes", n)
	}
	must(t, s.Mail(ctx, "yardmaster", "hello?", "op"))
	passUntil(t, d, "the ask again", func() bool { return len(openAsks(t, s)) == 1 })
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n := len(openAsks(t, s)); n != 1 {
		t.Fatalf("%d asks open, want the one", n)
	}
}

// A daemon that starts again finds the first ask on the handle and waits
// on it: no second ask. One answered while no daemon ran is acted on.
func TestLedLoginAskOutlivesTheDaemon(t *testing.T) {
	t.Parallel()
	s, f, d, l := loginSetup(t)
	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	again := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1,
		LeadLogin: l.lead, HarnessInstalled: d.HarnessInstalled}
	for range 3 {
		must(t, again.Reconcile(context.Background()))
	}
	if n := len(openAsks(t, s)); n != 1 {
		t.Fatalf("%d asks open after the daemon started again, want the one", n)
	}
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 1, "", "op"))
	third := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1,
		LeadLogin: l.lead, HarnessInstalled: d.HarnessInstalled}
	passUntil(t, third, "the leader started for the pick made while no daemon ran", func() bool { return l.count() == 1 })
	if _, ev := kindCount(t, s, r.Journal, "login_led"); ev.Data["step"] != "started" {
		t.Errorf("last login_led %v, want the start", ev.Data)
	}
}
