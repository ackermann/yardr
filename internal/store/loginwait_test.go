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
	t.Parallel()
	y, _ := secretYard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- y.LoginWaitReady(ctx, "claude-ready") }()
	waiting := make(chan error, 1)
	go func() { _, err := y.LoginWait(ctx, "claude-ready", ""); waiting <- err }()
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
	t.Parallel()
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
	if _, err := y.LoginWait(refused, "claude-phish", ""); err == nil || !strings.Contains(err.Error(), "this is the crew member scout") {
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
	if _, err := y.LoginWait(ctx, "claude-led", ""); !errors.Is(err, context.DeadlineExceeded) {
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
	t.Parallel()
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
	if !rs[0].Ask.Secret.OperatorLoginInput() {
		t.Errorf("the stored ask %+v is not the operator's login input", rs[0].Ask.Secret)
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

	// Only the operator's own login input is the daemon's ask, which the
	// listener takes down the link from the edge (OperatorLoginInput): the
	// whole of the struct is held to its form, a field at a time and any
	// field added later.
	no := false
	input := AskSecret{Env: []string{"INPUT"}, Login: "claude-a1b2c3d4"}
	if !input.OperatorLoginInput() {
		t.Errorf("%+v is not the operator's login input", input)
	}
	for name, mutate := range map[string]func(*AskSecret){
		"a user's own login":       func(s *AskSecret) { s.As = "ada" },
		"a source":                 func(s *AskSecret) { s.Source = "crm" },
		"a hook":                   func(s *AskSecret) { s.Hook = "bot" },
		"a harness":                func(s *AskSecret) { s.Harness = "claude" },
		"a crew member":            func(s *AskSecret) { s.Crew = "yardmaster" },
		"a user's address":         func(s *AskSecret) { s.User = "ada" },
		"a host":                   func(s *AskSecret) { s.Host = "https://crm.example.com" },
		"a concealment":            func(s *AskSecret) { s.Concealed = &no },
		"no login":                 func(s *AskSecret) { s.Login = "" },
		"another name":             func(s *AskSecret) { s.Env = []string{"TOKEN"} },
		"a name more":              func(s *AskSecret) { s.Env = []string{"INPUT", "TOKEN"} },
		"no name":                  func(s *AskSecret) { s.Env = nil },
		"the name twice":           func(s *AskSecret) { s.Env = []string{"INPUT", "INPUT"} },
		"a harness key's variable": func(s *AskSecret) { s.Env = []string{"ANTHROPIC_API_KEY"} },
	} {
		s := AskSecret{Env: []string{"INPUT"}, Login: "claude-a1b2c3d4"}
		mutate(&s)
		if s.OperatorLoginInput() {
			t.Errorf("%s: %+v is taken for the operator's login input", name, s)
		}
	}
}

// A login-input ask is refused, and nothing is stored, beside a source or a
// user, for a name beside the one, for anyone but the operator, for another
// yard, and with options or about-beads.
func TestLoginAskRefusals(t *testing.T) {
	t.Parallel()
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

// waitFor starts a wait for ref on y, as a leader led for group registers it
// ("" for the machine's own login), and answers what it is handed: the
// test's stand-in for a leader. A wait refused as a duplicate tries again:
// a probe below may hold the reference for the moment it checks.
func waitFor(t *testing.T, y *askYard, ref, group string) <-chan string {
	t.Helper()
	waited := make(chan string, 1)
	go func() {
		for {
			value, err := y.LoginWait(context.Background(), ref, group)
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
	t.Parallel()
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-a1b2c3d4"), "yardmaster")
	const value = "code-handed-to-the-login"
	waited := waitFor(t, y, "claude-a1b2c3d4", "")
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
	q, err := y.UserAsks(y.ctx, "mckean", "mckean")
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
	t.Parallel()
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-never"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "code-1"}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "no login waits under it") || strings.Contains(err.Error(), "code-1") {
		t.Fatalf("with no wait: %v", err)
	}
	// A wait that ended: its context ran out.
	ended, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := y.LoginWait(ended, "claude-ended", ""); done <- err }()
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
	waited := waitFor(t, y, "claude-once", "")
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
// nothing at its ends, the operator, and a wait that is the login of the user
// it is asked of: each refusal repeats no value, and a refused value leaves
// the wait for the good one.
func TestGiveLoginRefusals(t *testing.T) {
	t.Parallel()
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
	waited := waitFor(t, y, "claude-picky", "")
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
	// A wait is its user's, whoever gives: the reference ties it to nobody,
	// so an ask of another user that names it is refused at the giving and
	// leaves it waiting for the ask that is its own. ada's login is led for
	// her group; claude-op is the machine's, the operator's.
	loginGroup(t, y, "ada-builders", "ada")
	waited = waitFor(t, y, "claude-ada", "ada-builders")
	forOp := waitFor(t, y, "claude-op", "")
	forAdaViaOp := waitFor(t, y, "claude-ada-via-op", "ada-builders")
	for _, ref := range []string{"claude-ada", "claude-op", "claude-ada-via-op"} {
		waitingOwner(t, y, ref)
	}
	answered := len(answerNotes(t, y))
	for _, c := range []struct {
		name   string
		to, as string
		ref    string
		actor  string
		want   string
	}{
		// The finding's reproduction: an ask of the victim names a wait that
		// is the operator's.
		{"ada, asked for the operator's login", "ada", "ada", "claude-op", "ada",
			"is for ada's login, and the login waiting under claude-op is mckean's"},
		{"the operator, asked for ada's login", "mckean", "", "claude-ada", "mckean",
			"is for mckean's login, and the login waiting under claude-ada is ada's"},
		// Whose login it is is the As, not who the ask is addressed to.
		{"the operator, asked for ada's code, naming the operator's wait", "mckean", "ada", "claude-op", "mckean",
			"is for ada's login, and the login waiting under claude-op is mckean's"},
	} {
		a := loginAsk(c.ref)
		a.To, a.Secret.As = c.to, c.as
		bad := y.ask(t, a, "yardmaster")
		_, err := y.GiveSecret(y.ctx, y.bead, bad, map[string]string{"INPUT": "s3cret-for-the-other"}, c.actor, true)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "s3cret-for-the-other") {
			t.Errorf("%s: err = %v, want %q and no value", c.name, err, c.want)
		}
	}
	// Nothing was handed or written, and each wait is still there: the ask
	// that is its user's finds it.
	if n := len(answerNotes(t, y)); n != answered {
		t.Fatalf("the refusals wrote %d answer notes", n-answered)
	}
	for name, ch := range map[string]<-chan string{"ada's": waited, "the operator's": forOp, "ada's, for the operator": forAdaViaOp} {
		select {
		case got := <-ch:
			t.Fatalf("%s wait was handed %q by a refused giving", name, got)
		default:
		}
	}
	// ada gives her own code, and the operator gives the machine's, and
	// ada's too for her: the ask is addressed to the operator, and it is
	// ada's login it is for.
	viaOp := loginAsk("claude-ada-via-op")
	viaOp.To, viaOp.Secret.As = "mckean", "ada"
	for _, c := range []struct {
		ask   uint64
		ref   string
		actor string
		ch    <-chan string
		code  string
	}{
		{seq, "claude-ada", "ada", waited, "ada-code"},
		{y.ask(t, loginAsk("claude-op"), "yardmaster"), "claude-op", "mckean", forOp, "op-code"},
		{y.ask(t, viaOp, "yardmaster"), "claude-ada-via-op", "mckean", forAdaViaOp, "via-op-code"},
	} {
		if keyed := giveLogin(t, y, c.ask, c.code, c.actor); keyed.Login != c.ref {
			t.Fatalf("keyed = %+v", keyed)
		}
		if got := <-c.ch; got != c.code {
			t.Fatalf("the waiter of %s was handed %q", c.ref, got)
		}
	}
}

// loginGroup puts a group of the herdr runner that carries the login of the
// user loginFor, as yardr user add --login makes one.
func loginGroup(t *testing.T, y *askYard, name, loginFor string) {
	t.Helper()
	g := Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": "claude"}}
	if loginFor != "" {
		g.Config["login_for"] = loginFor
	}
	if err := y.PutGroup(y.ctx, g, "mckean"); err != nil {
		t.Fatal(err)
	}
}

// waitingOwner waits until the wait under ref has registered, and answers
// the user it recorded as the one whose login it is.
func waitingOwner(t *testing.T, y *askYard, ref string) string {
	t.Helper()
	ready, stop := context.WithTimeout(y.ctx, 10*time.Second)
	defer stop()
	if err := y.LoginWaitReady(ready, ref); err != nil {
		t.Fatalf("the wait under %s never registered: %v", ref, err)
	}
	y.logins.mu.Lock()
	defer y.logins.mu.Unlock()
	return y.logins.waits[ref].owner
}

// A wait is registered for the user whose login it is, as the server
// resolves it from the group the leader names: the one a session on the
// group burns, which is the operator's for a login led for no group and for
// a group that declares none. A group the yard does not have is refused and
// registers nothing, where it would otherwise read as the operator's. Where
// the yard names nobody the wait is still registered, so that a login led on
// stdin alone is not stopped, and it is handed nothing.
func TestLoginWaitRecordsItsOwner(t *testing.T) {
	t.Parallel()
	y, _ := secretYard(t)
	loginGroup(t, y, "plain-builders", "")
	loginGroup(t, y, "mckean-builders", "mckean")
	loginGroup(t, y, "ada-builders", "ada")
	wait := func(ref, group string) (owner string) {
		t.Helper()
		ctx, stop := context.WithCancel(y.ctx)
		done := make(chan error, 1)
		go func() { _, err := y.LoginWait(ctx, ref, group); done <- err }()
		owner = waitingOwner(t, y, ref)
		stop()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("the wait under %s = %v", ref, err)
		}
		return owner
	}
	for i, c := range []struct{ group, owner string }{
		{"", "mckean"},
		{"plain-builders", "mckean"},
		{"mckean-builders", "mckean"},
		{"ada-builders", "ada"},
	} {
		if got := wait("claude-owner-"+itoa(uint64(i)), c.group); got != c.owner {
			t.Errorf("a login led for group %q is %q's, want %q's", c.group, got, c.owner)
		}
	}

	// Refused at once: a wait that registered would wait until its context
	// ended, which the bound makes a failure and not a hang.
	ghost, stopGhost := context.WithTimeout(y.ctx, 10*time.Second)
	defer stopGhost()
	_, err := y.LoginWait(ghost, "claude-ghost", "ghost-builders")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "group ghost-builders") {
		t.Fatalf("a wait for a group there is none of = %v", err)
	}
	y.logins.mu.Lock()
	registered := len(y.logins.waits)
	y.logins.mu.Unlock()
	if registered != 0 {
		t.Fatalf("%d waits registered after the refusal and the ended waits, want none", registered)
	}

	// No operator named: nobody's wait, which no giving reaches. mckean still
	// holds the role, so they may be asked, and the refusal says why.
	if err := y.SetSetting(y.ctx, OperatorSetting, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(y.ctx)
	defer stop()
	go func() { _, _ = y.LoginWait(ctx, "claude-nobody", "") }()
	if got := waitingOwner(t, y, "claude-nobody"); got != "" {
		t.Fatalf("with no operator named the wait is %q's", got)
	}
	seq := y.ask(t, loginAsk("claude-nobody"), "yardmaster")
	_, err = y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"INPUT": "s3cret-for-nobody"}, "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "is for mckean's login, and the login waiting under claude-nobody is nobody's, because this yard names no operator") ||
		strings.Contains(err.Error(), "s3cret-for-nobody") {
		t.Fatalf("a giving for a wait of nobody's: %v", err)
	}
	for _, asked := range []string{"", "mckean", "ada"} {
		if _, waits, handed := y.handLogin("claude-nobody", asked, "s3cret"); !waits || handed {
			t.Errorf("a wait of nobody's, asked of %q: waits %v, handed %v", asked, waits, handed)
		}
	}
}

// A second wait under a reference that waits is refused: one wait owns its
// reference until it is handed over or ends.
func TestLoginWaitRefusesADuplicate(t *testing.T) {
	t.Parallel()
	y, _ := secretYard(t)
	ended, stop := context.WithCancel(context.Background())
	defer stop()
	first := make(chan error, 1)
	go func() {
		for {
			_, err := y.LoginWait(ended, "claude-twice", "")
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
		_, err := y.LoginWait(probe, "claude-twice", "")
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
	t.Parallel()
	y, _ := secretYard(t)
	seq := y.ask(t, loginAsk("claude-twice-run"), "yardmaster")
	waited := waitFor(t, y, "claude-twice-run", "")
	// The wait registers in its own goroutine: the runs below start once
	// a probe is refused as a duplicate.
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, leave := context.WithCancel(context.Background())
		leave()
		_, err := y.LoginWait(probe, "claude-twice-run", "")
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
