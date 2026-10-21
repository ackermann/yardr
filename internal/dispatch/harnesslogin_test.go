package dispatch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	r, err := s.GetCrew(context.Background(), "yardmaster")
	must(t, err)
	return openAsksOn(t, s, r.Journal)
}

func openAsksOn(t *testing.T, s store.API, journal string) []store.ResolvedAsk {
	t.Helper()
	notes, err := s.Notes(context.Background(), journal)
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
	// Crew says the session sits at its screen and the yard leads the
	// login, and a client was told of each as it came: the session sat
	// down, then the yard asked how to log it in.
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil || !r.LoginScreen || !r.LoginLed {
		t.Fatalf("while the yard leads the login: Crew says login_screen %v and login_led %v (%v), want both", r.LoginScreen, r.LoginLed, err)
	}
	if evs := crewLogins(t, s); len(evs) != 2 || evs[0].Data["login_screen"] != true || evs[0].Data["login_led"] != false ||
		evs[1].Data["login_screen"] != true || evs[1].Data["login_led"] != true {
		t.Fatalf("crew_login events while the yard asks how to log in = %+v, want sitting down, then leading", evs)
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
	} else if r.LoginScreen || r.LoginLed {
		t.Errorf("after the login Crew says login_screen %v and login_led %v, want neither", r.LoginScreen, r.LoginLed)
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

// Picking "API key" on the first ask leads no login program: one secret
// ask brings the key, GiveSecret writes it into the operator's key file
// for the kind, and the crew member starts again with its first prompt. No
// leader runs, and the value is in the file and nowhere else in the yard.
func TestYardLeadsAPIKeyLoginThroughAsks(t *testing.T) {
	t.Parallel()
	s, f, d, l := loginSetup(t)
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	pick := openAsks(t, s)[0]
	var labels []string
	for _, o := range pick.Ask.Options {
		labels = append(labels, o.Label)
	}
	if len(labels) < 3 || labels[0] != "Claude Code: Claude subscription" || labels[2] != "Claude Code: API key" {
		t.Fatalf("first ask options: %q", labels)
	}
	must(t, s.AnswerAsk(ctx, r.Journal, pick.Note.Seq, 3, "", "op"))
	passUntil(t, d, "a secret ask for the key", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret != nil
	})
	key := openAsks(t, s)[0]
	if key.Ask.Secret.Harness != "claude" || key.Ask.Secret.Crew != "yardmaster" ||
		strings.Join(key.Ask.Secret.Env, ",") != "ANTHROPIC_API_KEY" || key.Ask.Secret.Source != "" {
		t.Fatalf("the secret ask asks for %+v", key.Ask.Secret)
	}
	if l.count() != 0 {
		t.Fatalf("a leader ran for an API key pick")
	}
	const value = "sk-ant-k3y"
	keyed, err := s.GiveSecret(ctx, r.Journal, key.Note.Seq, map[string]string{"ANTHROPIC_API_KEY": value}, "op")
	if err != nil {
		t.Fatal(err)
	}
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
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginAskKey] != nil {
		t.Errorf("the login's mark is left on the handle: %v", r.Handle[dispatch.HarnessLoginAskKey])
	}
	// The key is in the operator's key file of the kind, mode 0600, the
	// crew member's env_file is left as it was, and the answer holds the
	// name, never the value.
	file := keyed.File
	if !strings.HasSuffix(file, "/"+store.HarnessDir+"/claude/op.env") {
		t.Fatalf("the key went to %s, want the operator's key file of claude", file)
	}
	if r.Config["env_file"] != nil {
		t.Fatalf("the yardmaster's env_file was set: %v", r.Config["env_file"])
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "ANTHROPIC_API_KEY="+value+"\n" {
		t.Fatalf("the env file holds %q, %v", b, err)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the env file's mode = %v, %v", st.Mode(), err)
	}
	notes, err := s.Notes(ctx, r.Journal)
	must(t, err)
	for _, n := range notes {
		if strings.Contains(n.Text, value) {
			t.Errorf("note %d holds the key", n.Seq)
		}
	}
	evs, err := s.EventsSince(ctx, 0, 10000)
	must(t, err)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), value) {
			t.Errorf("event %s holds the key: %s", e.Kind, b)
		}
	}
}

// A key's done starts again the bead sessions at the login screen on the
// operator's login, as it does the crew: a builder of a group without
// login_for sat there, and its session is ended as aborted, no failure, so
// its bead is routed again onto the key. A login_for group of another user
// reads no key of the operator's, so its session at the screen is left as
// it is: starting it again would only bring the screen back.
func TestLedKeyStartsBeadSessionsOnTheOperatorsLoginAgain(t *testing.T) {
	t.Parallel()
	s, f, d, _ := loginSetup(t)
	ctx := context.Background()
	d.Runners["herdr"] = f
	_, err := s.AddUser(ctx, "ada", "op")
	must(t, err)
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "op"))
	must(t, s.PutGroup(ctx, store.Group{Name: "ada-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "login_for": "ada"}}, "op"))
	atScreen := func(group, agent string) store.Session {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "work of " + group}, "op")
		must(t, err)
		sess, err := s.Claim(ctx, b.ID, group, b.Stage, "op")
		must(t, err)
		f.set(agent, "idle")
		must(t, s.MarkRunning(ctx, sess.ID, map[string]any{"agent": agent, dispatch.HarnessLoginKey: "Select login method:"}))
		return *sess
	}
	ours, theirs := atScreen("builders", "builder-1"), atScreen("ada-builders", "ada-builder-1")
	if ours.Login != "op" || theirs.Login != "ada" {
		t.Fatalf("the claims burn %q and %q, want op's and ada's", ours.Login, theirs.Login)
	}

	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 3, "", "op"))
	passUntil(t, d, "a secret ask for the key", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret != nil
	})
	key := openAsks(t, s)[0]
	for _, want := range []string{"every Claude Code session that burns your login reads it", "A session on another person's login (login_for) never reads it."} {
		if !strings.Contains(strings.Join(key.Ask.Context, "\n"), want) {
			t.Errorf("the key's ask says %q, want %q", key.Ask.Context, want)
		}
	}
	_, err = s.GiveSecret(ctx, r.Journal, key.Note.Seq, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-k3y"}, "op")
	must(t, err)
	passUntil(t, d, "the login told as made", func() bool {
		_, ev := kindCount(t, s, r.Journal, "login_led")
		return ev.Data["step"] == "logged_in"
	})
	_, ev := kindCount(t, s, r.Journal, "login_led")
	if got := fmt.Sprint(ev.Data["restarted"]); got != "[crew:yardmaster session:"+ours.ID+"]" {
		t.Errorf("restarted %s, want the yardmaster and op's builder", got)
	}
	if got, err := s.GetSession(ctx, ours.ID); err != nil || got.State != "aborted" {
		t.Errorf("op's builder = %+v, %v: want it aborted, to start again", got, err)
	}
	if n, err := s.FailureCount(ctx, ours.Bead, "new"); err != nil || n != 0 {
		t.Errorf("op's bead counts %d failures, %v: the restart is none", n, err)
	}
	if got, err := s.GetSession(ctx, theirs.ID); err != nil || got.State != "running" {
		t.Errorf("ada's builder = %+v, %v: want it left at its screen", got, err)
	}
}

// A kind whose harness file names no login_env offers no API-key option:
// with every kind installed, the first ask holds Claude's key alone.
func TestAPIKeyOptionNeedsLoginEnv(t *testing.T) {
	t.Parallel()
	s, _, d, _ := loginSetup(t)
	d.HarnessInstalled = func(kind string) bool { return true }
	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	var keys []string
	for _, o := range openAsks(t, s)[0].Ask.Options {
		if strings.HasSuffix(o.Label, "API key") {
			keys = append(keys, o.Label)
		}
	}
	if len(keys) != 1 || keys[0] != "Claude Code: API key" {
		t.Fatalf("API key options: %q", keys)
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

// A key's secret ask outlives the daemon, as the first ask does: it has
// no leader to have gone with it, so a daemon that starts again waits on
// it, and acts on a key given while none ran.
func TestLedKeyAskOutlivesTheDaemon(t *testing.T) {
	t.Parallel()
	s, f, d, l := loginSetup(t)
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 3, "", "op"))
	passUntil(t, d, "a secret ask for the key", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret != nil
	})
	seq := openAsks(t, s)[0].Note.Seq
	again := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1,
		LeadLogin: l.lead, HarnessInstalled: d.HarnessInstalled}
	for range 3 {
		must(t, again.Reconcile(context.Background()))
	}
	if a := openAsks(t, s); len(a) != 1 || a[0].Note.Seq != seq {
		t.Fatalf("after the daemon started again: %d open asks, want the key's", len(a))
	}
	if _, err := s.GiveSecret(ctx, r.Journal, seq, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-k3y"}, "op"); err != nil {
		t.Fatal(err)
	}
	third := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1,
		LeadLogin: l.lead, HarnessInstalled: d.HarnessInstalled}
	passUntil(t, third, "the login told as made", func() bool {
		_, ev := kindCount(t, s, r.Journal, "login_led")
		return ev.Data["step"] == "logged_in"
	})
	if l.count() != 0 {
		t.Errorf("a leader ran for an API key pick")
	}
}

// A declined key ends the lead without a login: its ask is taken back,
// and the first ask comes again only once mail comes after it ended.
func TestLedKeyDeclinedIsOfferedOnNextMail(t *testing.T) {
	t.Parallel()
	s, _, d, _ := loginSetup(t)
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	passUntil(t, d, "an ask how to log in", func() bool { return len(openAsks(t, s)) == 1 })
	must(t, s.AnswerAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, 3, "", "op"))
	passUntil(t, d, "a secret ask for the key", func() bool {
		a := openAsks(t, s)
		return len(a) == 1 && a[0].Ask.Secret != nil
	})
	must(t, s.DeclineAsk(ctx, r.Journal, openAsks(t, s)[0].Note.Seq, "", "op"))
	passUntil(t, d, "the login's end told", func() bool {
		_, ev := kindCount(t, s, r.Journal, "login_led")
		return ev.Data["step"] == "failed"
	})
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n := len(openAsks(t, s)); n != 0 {
		t.Fatalf("%d asks open after the key was declined, want none until mail comes", n)
	}
	must(t, s.Mail(ctx, "yardmaster", "hello?", "op"))
	passUntil(t, d, "the ask again", func() bool { return len(openAsks(t, s)) == 1 })
}

// When a member picks a kind whose login is already open, it joins that
// login: no second pick ask, and it restarts with the other members of the
// kind once the one login is made.
func TestLedLoginSwitchJoinsExistingKind(t *testing.T) {
	t.Parallel()
	s, f, d, l := loginSetup(t)
	ctx := context.Background()
	yardmaster, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	passUntil(t, d, "the yardmaster's login ask", func() bool { return len(openAsks(t, s)) == 1 })

	_, err = s.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "fake", Enabled: true,
		Config: map[string]any{"kind": "codex"}}, "op")
	must(t, err)
	passUntil(t, d, "scout at its login screen", func() bool {
		r, err := s.GetCrew(ctx, "scout")
		return err == nil && r.Live() && r.Handle[dispatch.HarnessLoginKey] != nil
	})
	scout, err := s.GetCrew(ctx, "scout")
	must(t, err)
	handle := map[string]any{}
	for k, v := range scout.Handle {
		handle[k] = v
	}
	handle["kind"] = "codex"
	must(t, s.SetCrewHandleFor(ctx, "scout", "", handle))
	scout, err = s.GetCrew(ctx, "scout")
	must(t, err)
	passUntil(t, d, "scout's kind choice", func() bool { return len(openAsksOn(t, s, scout.Journal)) == 1 })
	pick := openAsksOn(t, s, scout.Journal)[0]
	must(t, s.AnswerAsk(ctx, scout.Journal, pick.Note.Seq, 1, "", "op"))
	passUntil(t, d, "scout joined the existing Claude login", func() bool {
		r, err := s.GetCrew(ctx, "scout")
		return err == nil && r.Config["kind"] == "claude" && r.Handle[dispatch.HarnessLoginAskKey] == nil
	})
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if asks := openAsksOn(t, s, scout.Journal); len(asks) != 0 {
		t.Fatalf("scout has %d extra asks while Claude's login is open", len(asks))
	}
	if l.count() != 0 {
		t.Fatalf("the login started before its existing ask was answered")
	}

	pick = openAsks(t, s)[0]
	must(t, s.AnswerAsk(ctx, yardmaster.Journal, pick.Note.Seq, 1, "", "op"))
	passUntil(t, d, "the login's secret ask", func() bool {
		asks := openAsks(t, s)
		return len(asks) == 1 && asks[0].Ask.Secret != nil
	})
	secret := openAsks(t, s)[0]
	if _, err := s.GiveSecret(ctx, yardmaster.Journal, secret.Note.Seq, map[string]string{"INPUT": "good#s1"}, "op"); err != nil {
		t.Fatal(err)
	}
	passUntil(t, d, "the login's confirm ask", func() bool {
		asks := openAsks(t, s)
		return len(asks) == 1 && asks[0].Ask.Secret == nil && asks[0].Note.Seq != pick.Note.Seq
	})
	confirm := openAsks(t, s)[0]
	must(t, s.AnswerAsk(ctx, yardmaster.Journal, confirm.Note.Seq, 2, "", "op"))
	passUntil(t, d, "the shared login finished", func() bool {
		_, ev := kindCount(t, s, yardmaster.Journal, "login_led")
		return ev.Data["step"] == "logged_in"
	})
	f.mu.Lock()
	f.login = ""
	f.mu.Unlock()
	passUntil(t, d, "scout restarted with the selected kind", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.n >= 4
	})
	scout, err = s.GetCrew(ctx, "scout")
	must(t, err)
	if scout.Config["kind"] != "claude" || scout.Handle["agent"] != "scout-3" {
		t.Errorf("scout after restart: config kind %v, handle %v", scout.Config["kind"], scout.Handle)
	}
	if asks := openAsksOn(t, s, scout.Journal); len(asks) != 0 {
		t.Errorf("scout has %d extra asks after the shared login completed", len(asks))
	}
}
