package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/peer"
)

func loginAsk(ref string) Ask {
	return Ask{Question: "The code the sign-in page showed", To: "mckean",
		Context: []string{"The login waits for what the sign-in page showed."},
		Secret:  &AskSecret{Login: ref}}
}

func TestLoginWaitReadyWaitsForRegistration(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- y.LoginWaitReady(ctx, "claude-ready") }()
	waiting := make(chan error, 1)
	go func() { _, err := y.LoginWait(ctx, "claude-ready"); waiting <- err }()
	if err := <-ready; err != nil {
		t.Fatalf("the registration: %v", err)
	}
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatalf("the wait after cancel: %v", err)
	}
}

// A wait registered from another crew member's pane is refused, so no agent
// but the yardmaster can be handed what the operator types into a login
// ask's field; the yardmaster's pane registers one.
func TestLoginWaitOnlyFromTheYardmastersPane(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if _, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "scout", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.CrewStarted(y.ctx, "scout", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := WithCaller(y.ctx, me)
	refused, stop := context.WithTimeout(pane, 2*time.Second)
	defer stop()
	if _, err := y.LoginWait(refused, "claude-phish"); err == nil || !strings.Contains(err.Error(), "this is the crew member scout") {
		t.Fatalf("LoginWait from scout's pane = %v", err)
	}
	if err := y.CrewStopped(y.ctx, "scout", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.CrewStarted(y.ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(pane, 50*time.Millisecond)
	defer cancel()
	if _, err := y.LoginWait(ctx, "claude-led"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LoginWait from the yardmaster's pane = %v, want it to wait", err)
	}
}

func TestOldLoginWaitCleanupKeepsAReplacement(t *testing.T) {
	t.Parallel()
	s := &Store{}
	old, replacement := &loginWait{}, &loginWait{}
	s.logins.waits = map[string]*loginWait{"claude-reused": replacement}
	s.forgetLoginWait("claude-reused", old)
	if s.logins.waits["claude-reused"] != replacement {
		t.Fatal("finishing an earlier wait removed its replacement")
	}
}

// A login-input ask is stored with the wait it names and the one name the
// input is asked for, whatever the asker sent for the names.
func TestLoginAskIsStoredWithTheWait(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	ask := loginAsk("claude-a1b2c3d4")
	ask.Secret.Env = []string{}
	seq := y.ask(t, ask, "yardmaster")
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"env":["INPUT"],"login":"claude-a1b2c3d4","source":""}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}
	rs := y.resolved(t)
	if got := rs[0].Ask.Secret.Line(); got != "a secret: INPUT, for the waiting login claude-a1b2c3d4" {
		t.Fatalf("the line = %q", got)
	}
	lines := strings.Join(rs[0].Lines(), "\n")
	for _, want := range []string{
		"  a secret: INPUT, for the waiting login claude-a1b2c3d4",
		"  it goes to the waiting login only; it is kept nowhere, and no agent is shown it",
		"  answer with: yardr bead answer " + y.bead + " " + itoa(seq) + " --secret",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the card lacks %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "for the source") {
		t.Errorf("the card names a source:\n%s", lines)
	}
	// A login that is a user's own is asked of them, and its card says whose.
	own := loginAsk("claude-ada")
	own.To, own.Secret.As = "ada", "ada"
	y.ask(t, own, "yardmaster")
	rs = y.resolved(t)
	ownJSON, _ := json.Marshal(rs[1].Ask.Secret)
	if want := `{"source":"","env":["INPUT"],"login":"claude-ada","as":"ada"}`; string(ownJSON) != want {
		t.Fatalf("the own login's secret = %s\nwant %s", ownJSON, want)
	}
	if got := rs[1].Ask.Secret.Line(); got != "a secret: INPUT, for ada's waiting login claude-ada" {
		t.Fatalf("the own login's line = %q", got)
	}
	// Asked of the operator for them it stands as well.
	own.To = "mckean"
	y.ask(t, own, "yardmaster")
}

// A login-input ask is refused, and nothing is stored, beside a source or a
// user, for a name beside the one, for anyone but the operator, for another
// yard, and with options or about-beads.
func TestLoginAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	other, err := y.CreateBead(y.ctx, NewBead{Title: "another"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	with := func(change func(*Ask)) Ask {
		a := loginAsk("claude-a1b2c3d4")
		if change != nil {
			change(&a)
		}
		return a
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"a source beside it", with(func(a *Ask) { a.Secret.Source = "crm" }), "a waiting login or a source, not both"},
		{"a user beside it", with(func(a *Ask) { a.Secret.User = "ada" }), "takes no --for-user"},
		{"a name beside the one", with(func(a *Ask) { a.Secret.Env = []string{"CODE"} }), "CODE is no name of it"},
		{"not the operator", with(func(a *Ask) { a.To = "ada" }), "answering the login is theirs alone"},
		{"an own login of nobody", with(func(a *Ask) { a.To, a.Secret.As = "mckean", "eve" }), "a login input is asked for the login of a user of the yard (yardr user list): user eve: not found"},
		{"an own login asked of another user", with(func(a *Ask) { a.To, a.Secret.As = "bob", "ada" }),
			"the code that signs ada in is theirs to give, or the operator's (mckean) for them, so bob cannot be asked for it"},
		{"another yard", with(func(a *Ask) { a.To = "mckean@north" }), "nothing of a secret crosses a link"},
		{"options", with(func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
		{"about", with(func(a *Ask) { a.About = []string{other.ID} }), "takes no --about"},
	} {
		_, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if _, err := y.AskNew(y.ctx, NewBead{}, c.ask, "yardmaster"); err == nil {
			t.Errorf("%s: asked with a new bead", c.name)
		}
	}
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}
}

// waitFor starts a wait for ref on y and answers what it is handed: the
// test's stand-in for a leader. A wait refused as a duplicate tries again:
// a probe below may hold the reference for the moment it checks.
func waitFor(t *testing.T, y *askYard, ref string) <-chan string {
	t.Helper()
	waited := make(chan string, 1)
	go func() {
		for {
			value, err := y.LoginWait(context.Background(), ref)
			if err == nil {
				waited <- value
				return
			}
			if !strings.Contains(err.Error(), "already waits") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return waited
}

// giveLogin gives the login ask seq on y's bead until its wait has
// registered, and fails loudly when it never does.
func giveLogin(t *testing.T, y *askYard, seq uint64, value, actor string) *SourceKeyed {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": value}, actor, true)
		if err == nil {
			return keyed
		}
		if !strings.Contains(err.Error(), "no login waits") || time.Now().After(deadline) {
			t.Fatalf("GiveSecret: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// GiveSecret hands a login input to the login that waits for it: the waiter
// takes the value, the answer note holds the names, and the value is in no
// note, file, event or mail of the yard, only "given".
func TestGiveLoginHandsToTheWaiter(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-a1b2c3d4"), "yardmaster")
	const value = "code-handed-to-the-login"
	waited := waitFor(t, y, "claude-a1b2c3d4")
	keyed := giveLogin(t, y, seq, value, "mckean")
	if keyed.Login != "claude-a1b2c3d4" || keyed.File != "" || strings.Join(keyed.Given, ",") != "INPUT" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if got := <-waited; got != value {
		t.Fatalf("the waiter was handed %q", got)
	}
	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "mckean" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["INPUT"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	if r := y.resolved(t)[0]; r.State != AskAnswered {
		t.Fatalf("the ask is %s", r.State)
	}
	lines := strings.Join(y.resolved(t)[0].Lines(), "\n")
	if !strings.Contains(lines, "  mckean gave INPUT") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	// Bead and UserAsks, as footplate and the web view read them.
	v, err := y.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	a := v.Asks[0]
	if a.State != "answered" || a.Secret == nil || a.Secret.Login != "claude-a1b2c3d4" ||
		strings.Join(a.Secret.Env, ",") != "INPUT" || strings.Join(a.Given, ",") != "INPUT" {
		t.Fatalf("Bead's ask = %+v (secret %+v)", a, a.Secret)
	}
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Secret == nil || q.Decided[0].Secret.Login != "claude-a1b2c3d4" {
		t.Fatalf("UserAsks = %+v", q)
	}
	// The asker hears it, naming the login.
	e := lastAnswer(t, y)
	if line, _ := e.Data["answer"].(string); line != "mckean gave INPUT to the waiting login claude-a1b2c3d4 (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}
	// The value is in no note, event, file or mail of the yard: the notes
	// and the events as JSON, every file under the home, and the store's
	// file, which every committed write is in.
	notes, _ := json.Marshal(mustNotes(t, y))
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := json.Marshal(evs)
	db, err := os.ReadFile(y.db.Path())
	if err != nil {
		t.Fatal(err)
	}
	for where, kept := range map[string][]byte{"the notes": notes, "the events": events, "the store's file": db} {
		if bytes.Contains(kept, []byte(value)) {
			t.Errorf("%s hold the value", where)
		}
	}
	err = filepath.Walk(y.home, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || path == y.db.Path() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(value)) {
			t.Errorf("%s holds the value", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A giving with no login waiting is refused naming that: no wait was ever
// registered, the wait ended, or another giving took it.
func TestGiveLoginRefusesWhenNoneWaits(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-never"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "code-1"}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "no login waits under it") || strings.Contains(err.Error(), "code-1") {
		t.Fatalf("with no wait: %v", err)
	}
	// A wait that ended: its context ran out.
	ended, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := y.LoginWait(ended, "claude-ended"); done <- err }()
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the ended wait = %v", err)
	}
	seq = y.ask(t, loginAsk("claude-ended"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "code-2"}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "no login waits under it") {
		t.Fatalf("after the wait ended: %v", err)
	}
	// A wait another giving took: the second giving finds none.
	seq = y.ask(t, loginAsk("claude-once"), "yardmaster")
	waited := waitFor(t, y, "claude-once")
	giveLogin(t, y, seq, "code-3", "mckean")
	if got := <-waited; got != "code-3" {
		t.Fatalf("the waiter was handed %q", got)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "code-4"}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "no login waits under it") {
		t.Fatalf("the second giving: %v", err)
	}
	if n := len(answerNotes(t, y)); n != 1 {
		t.Fatalf("%d answer notes, want the one's", n)
	}
}

// A login input takes exactly the name asked, a value that is one line with
// nothing at its ends, and the operator: each refusal repeats no value, and
// a refused value leaves the wait for the good one.
func TestGiveLoginRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-picky"), "yardmaster")
	for _, c := range []struct {
		name   string
		values map[string]string
		actor  string
		want   string
	}{
		{"another name", map[string]string{"CODE": "s3cret-for-code"}, "mckean", "was given a name it did not ask for"},
		{"no name", map[string]string{}, "mckean", "INPUT was not given"},
		{"empty", map[string]string{"INPUT": ""}, "mckean", "the value for INPUT is empty"},
		{"several lines", map[string]string{"INPUT": "s3cret-one\ns3cret-two"}, "mckean", "has a line break in it"},
		{"white space at its ends", map[string]string{"INPUT": " s3cret-padded "}, "mckean", "begins or ends with white space"},
		{"another giver", map[string]string{"INPUT": "s3cret-for-ada"}, "ada", "answering the login"},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, c.values, c.actor, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		for _, v := range c.values {
			if v != "" && strings.Contains(err.Error(), v) {
				t.Errorf("%s: the refusal repeats the value: %v", c.name, err)
			}
		}
	}
	// Nothing refused wrote a note, and the wait still waits for the good one.
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes, want none", n)
	}
	waited := waitFor(t, y, "claude-picky")
	giveLogin(t, y, seq, "good-code", "mckean")
	if got := <-waited; got != "good-code" {
		t.Fatalf("the waiter was handed %q", got)
	}
	// A login that is a user's own is theirs to give, or the operator's for
	// them: another user's word is refused, and the wait is left for theirs.
	own := loginAsk("claude-ada")
	own.To, own.Secret.As = "ada", "ada"
	seq = y.ask(t, own, "yardmaster")
	_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "s3cret-for-bob"}, "bob", true)
	if err == nil || !strings.Contains(err.Error(), "the code that signs ada in is ada's to give, or the operator's (mckean) for them") ||
		!strings.Contains(err.Error(), "this is bob") || strings.Contains(err.Error(), "s3cret-for-bob") {
		t.Fatalf("another user giving ada's code: %v", err)
	}
	waited = waitFor(t, y, "claude-ada")
	if keyed := giveLogin(t, y, seq, "ada-code", "ada"); keyed.Login != "claude-ada" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if got := <-waited; got != "ada-code" {
		t.Fatalf("the waiter was handed %q", got)
	}
}

// A second wait under a reference that waits is refused: one wait owns its
// reference until it is handed over or ends.
func TestLoginWaitRefusesADuplicate(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	ended, stop := context.WithCancel(context.Background())
	defer stop()
	first := make(chan error, 1)
	go func() {
		for {
			_, err := y.LoginWait(ended, "claude-twice")
			// A probe may hold the reference for the moment it checks.
			if err == nil || !strings.Contains(err.Error(), "already waits") {
				first <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	// Probes with their context already ended register and leave at once
	// where nothing waits, and are refused where the first one does: the
	// refusal proves it registered.
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, leave := context.WithCancel(context.Background())
		leave()
		_, err := y.LoginWait(probe, "claude-twice")
		if err != nil && strings.Contains(err.Error(), "already waits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a duplicate wait under a waiting reference: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the first wait = %v", err)
	}
}

// write's batch may run a function twice for one call: the handing runs
// once, and the giving ends the same then as when it ran once: one answer
// note, and the waiter handed the value once.
func TestGiveLoginRunTwice(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-twice-run"), "yardmaster")
	waited := waitFor(t, y, "claude-twice-run")
	// The wait registers in its own goroutine: the runs below start once
	// a probe is refused as a duplicate.
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, leave := context.WithCancel(context.Background())
		leave()
		_, err := y.LoginWait(probe, "claude-twice-run")
		if err != nil && strings.Contains(err.Error(), "already waits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wait never registered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var k sourceKey
	var lg loginSent
	give := func(w *wtx) error {
		return w.giveSecret(y.ctx, y.Store, y.home, y.bead, seq, map[string]string{"INPUT": "s3cret"}, "mckean", true, &k, &lg)
	}
	others := errors.New("another caller's function failed")
	err := y.db.Update(func(tx *bolt.Tx) error {
		if err := give(&wtx{Tx: tx, mentions: &y.mentions}); err != nil {
			return err
		}
		return others
	})
	if !errors.Is(err, others) {
		t.Fatal(err)
	}
	// The wait was handed in the run that was rolled back: the value is
	// with the waiter, and the ask is open still.
	if got := <-waited; got != "s3cret" {
		t.Fatalf("the waiter was handed %q", got)
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes, want none", n)
	}
	// The run by itself writes the note and hands nothing more: no wait,
	// but lg set, so no refusal.
	if err := y.db.Update(func(tx *bolt.Tx) error {
		return give(&wtx{Tx: tx, mentions: &y.mentions})
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(answerNotes(t, y)); n != 1 {
		t.Fatalf("%d answer notes, want 1", n)
	}
	select {
	case got := <-waited:
		t.Fatalf("the waiter was handed %q twice", got)
	default:
	}
}
