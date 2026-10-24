package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// signInWatch is a store that watches the command which signs user in: whether
// their sign-in was open when the crew member that serves them was put (the
// daemon starts the conversation at that write), whether their conversation
// had been asked to start again when the sign-in's bead was closed, and, when
// it comes to asking for their code, what atAsk does first, which plants the
// sessions the daemon would have started by then.
type signInWatch struct {
	store.API
	user  string
	atAsk func()

	mu     sync.Mutex
	served []bool
	closed []bool
}

func (w *signInWatch) PutCrew(ctx context.Context, def store.CrewMember, actor string) (*store.CrewMember, error) {
	if def.EnvFor(w.user) != "" {
		beads, err := w.API.ListBeads(ctx, store.BeadFilter{Type: store.TypeQuestion})
		if err != nil {
			return nil, err
		}
		w.mu.Lock()
		w.served = append(w.served, store.SigningIn(beads, w.user, "claude", time.Now()))
		w.mu.Unlock()
	}
	return w.API.PutCrew(ctx, def, actor)
}

func (w *signInWatch) CloseBead(ctx context.Context, id, reason string, exp store.Expect, actor string) (*store.Bead, error) {
	r, err := w.API.GetCrew(ctx, "yardmaster")
	w.mu.Lock()
	w.closed = append(w.closed, err == nil && r.Session(w.user).RestartRequested != "")
	w.mu.Unlock()
	return w.API.CloseBead(ctx, id, reason, exp, actor)
}

func (w *signInWatch) AddAsk(ctx context.Context, id string, ask store.Ask, actor string) (uint64, error) {
	if w.atAsk != nil {
		w.atAsk()
	}
	return w.API.AddAsk(ctx, id, ask, actor)
}

// sitAt starts the yardmaster's session for user ("" for the line session) of
// the harness kind, as the daemon records one, at its login screen unless
// screen is "".
func sitAt(t *testing.T, st store.API, user, kind, screen string) {
	t.Helper()
	handle := map[string]any{"agent": "y-" + user, "kind": kind}
	if screen != "" {
		handle[store.HarnessLoginKey] = screen
	}
	// Not Fatal: the relay calls this from a goroutine of its own.
	if err := st.CrewStartedFor(context.Background(), "yardmaster", user, handle); err != nil {
		t.Error(err)
	}
}

const atLoginScreen = "Select login method:"

// signedIn runs args, a command that leads user's sign-in, and plays the
// person: it waits for the ask for their code, checks that it is theirs and
// for a waiting login, gives the stand-in's code as them, and answers what
// the command printed and how it ended.
func signedIn(t *testing.T, st store.API, user string, args ...string) (string, error) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := as(t, st, "jo", args...)
		done <- result{out, err}
	}()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for {
		// The command adds the user first: until it has, there is no queue.
		q, err := st.UserAsks(ctx, user)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if err == nil && len(q.Open) > 0 {
			ask := q.Open[0]
			if ask.Question != "The code the sign-in page showed" || ask.Secret == nil || ask.Secret.Login == "" || ask.Secret.As != user ||
				strings.Join(ask.Secret.Env, ",") != "INPUT" {
				t.Fatalf("the ask for %s's code: %+v (secret %+v)", user, ask, ask.Secret)
			}
			if _, err := st.GiveSecret(ctx, ask.Bead, ask.Ask, map[string]string{"INPUT": "good#s1"}, user, true); err != nil {
				t.Fatalf("%s giving their code: %v", user, err)
			}
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the command ended before it asked for %s's code: %v\n%s", user, r.err, r.out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ask for %s's code within 30 s", user)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not end after the code was given")
	}
	return "", nil
}

// routesFor are the routes that take user's beads, by stage, with their bead
// priority.
func routesFor(t *testing.T, st store.API, user string) []string {
	t.Helper()
	rs, err := st.Routes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		if r.Originator != nil && *r.Originator == user {
			p := "any"
			if r.BeadPriority != nil {
				p = "P" + strconv.Itoa(*r.BeadPriority)
			}
			out = append(out, r.Stage+" "+p+" -> "+r.Group)
		}
	}
	slices.Sort(out)
	return out
}

// yardr user add <name> --login claude makes a person whole (design
// yardr-m06d 3.8): the user, the folder and the env file their login lands in,
// the group that runs on it, the routes that send their beads to it, the
// yardmaster's conversation with them, and the sign-in, whose page is mailed
// to them and whose code they give as an ask of their own. yardr user rm
// takes the group, the routes and the entries in one write, and leaves the
// folder. It stays sequential: the stand-in claude is found through lookPath.
func TestUserAddWithALogin(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	home := yardEnv(t, st)["YARDR_HOME"]
	dir := fakeTools(t, map[string]string{"claude": loginStandIn})
	stub(t, &tryHarnessCall, realTry)

	// The yard's builders, two routes that take them, and reviewers that no
	// person's group is a copy of.
	mustAs(t, st, "jo", "group", "add", "builders", "--runner", "herdr", "--limit", "4",
		"--set", "kind=claude", "--set", "model=claude-sonnet-5-5", "--set", "prompt=roles/builder.md")
	mustAs(t, st, "jo", "group", "add", "reviewers", "--runner", "herdr", "--set", "kind=claude")
	mustAs(t, st, "jo", "group", "add", "bench", "--runner", "manual")
	mustAs(t, st, "jo", "route", "add", "--stage", "new", "--group", "builders")
	mustAs(t, st, "jo", "route", "add", "--stage", "new", "--bead-priority", "1", "--group", "builders")
	mustAs(t, st, "jo", "route", "add", "--stage", "review", "--group", "reviewers")
	operatorRoutes := func() int {
		rs, err := st.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(rs)
	}
	before := operatorRoutes()
	claimedEnv := filepath.Join(home, "groups", "nina.env")
	mustAs(t, st, "jo", "group", "add", "stale", "--runner", "herdr", "--set", "kind=claude", "--set", "env_file="+claimedEnv)

	// What cannot be made is refused before anything is: a harness that is
	// not wired, a group that is not there or starts no agent, a login that
	// stands from a user removed before (a name is reusable), a name that is
	// no folder.
	if err := os.MkdirAll(filepath.Join(home, "logins", "eve"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"user", "add", "ada", "--login", "codex"}, "--login codex: only claude is wired"},
		{[]string{"user", "add", "ada", "--login", "nothing"}, "--login nothing is no harness kind"},
		{[]string{"user", "add", "ada", "--login", "claude", "--like", "ghost"}, "group ghost: not found"},
		{[]string{"user", "add", "ada", "--login", "claude", "--like", "bench"}, "group bench is manual, not herdr"},
		{[]string{"user", "add", "ada", "--like", "builders"}, "--like names the group a person's own is a copy of: it goes with --login"},
		{[]string{"user", "add", "nina", "--login", "claude"}, "login env file " + claimedEnv + " is already named by group stale"},
		{[]string{"user", "add", "eve", "--login", "claude"}, filepath.Join(home, "logins", "eve") + " exists"},
		{[]string{"user", "add", "a/b", "--login", "claude"}, "the login folder is named by the user"},
	} {
		if out, err := as(t, st, "jo", c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("yardr %s = %v, want %q\n%s", strings.Join(c.args, " "), err, c.want, out)
		}
	}
	for _, name := range []string{"ada", "eve", "nina"} {
		if _, err := st.GetUser(ctx, name); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a refused add made the user %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "groups", "ada.env")); err == nil {
		t.Fatal("a refused add wrote an env file")
	}
	if _, err := os.Stat(filepath.Join(home, "groups", "nina.env")); err == nil {
		t.Fatal("a refused add wrote an env file another group already names")
	}
	mustAs(t, st, "jo", "group", "rm", "stale")

	// The whole of it, with the yardmaster serving everyone. When her code is
	// asked for, the daemon has started her conversation on a folder with no
	// login in it, and it sits at the login screen; the line session of the
	// yardmaster sits there too, and her sign-in is not its own.
	watch := &signInWatch{API: st, user: "ada", atAsk: func() {
		sitAt(t, st, "ada", "claude", atLoginScreen)
		sitAt(t, st, "", "claude", atLoginScreen)
	}}
	rememberEnv(t, watch, yardEnv(t, st))
	out, err := signedIn(t, watch, "ada", "user", "add", "ada", "--login", "claude")
	if err != nil {
		t.Fatalf("user add ada --login claude: %v\n%s", err, out)
	}
	folder, env := filepath.Join(home, "logins", "ada"), filepath.Join(home, "groups", "ada.env")
	for _, want := range []string{
		"user ada added\n",
		"  login folder " + folder + " (0700), env file " + env + " (0600)\n",
		"  group ada-builders (a copy of builders: kind claude, login_for ada, limit 1)\n",
		"-> ada-builders\n",
		"  yardmaster serves ada (env_for_ada)\n",
		"  ada has no device yet",
		"  the page to sign in on is mailed to ada\n",
		"  the code ada pastes back is ask ",
		"  ada is signed in, and one call to Claude Code answered (the login is in " + env + ")\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the login folder: %v, %v", fi, err)
	}
	if fi, err := os.Stat(env); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the env file: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(env); string(b) != "CLAUDE_CONFIG_DIR="+folder+"\n" {
		t.Errorf("the env file holds %q", b)
	}
	g, err := st.GetGroup(ctx, "ada-builders")
	if err != nil {
		t.Fatal(err)
	}
	if g.Runner != "herdr" || g.Limit != 1 || g.LoginFor() != "ada" || g.Config["kind"] != "claude" || g.Config["env_file"] != env ||
		g.Config["model"] != "claude-sonnet-5-5" || g.Config["prompt"] != "roles/builder.md" {
		t.Errorf("ada's group = %+v", g)
	}
	if got, want := strings.Join(routesFor(t, st, "ada"), "; "), "new P1 -> ada-builders; new any -> ada-builders"; got != want {
		t.Errorf("ada's routes: %s\nwant %s", got, want)
	}
	if got := operatorRoutes(); got != before+2 {
		t.Errorf("%d routes, want the operator's %d and two of ada's", got, before)
	}
	ym, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if ym.EnvFor("ada") != env || len(ym.Served()) != 0 || !ym.HasConversation("ada") {
		t.Errorf("the yardmaster after: %+v", ym.Config)
	}
	// Her sign-in was open before the conversation was served, since the
	// daemon starts that session at the write and offers the operator no login
	// card for it while the bead stands; the session at the screen was asked to
	// start again on the login she made (hers alone, not the line session's)
	// before the bead was closed, since a daemon that saw the bead closed first
	// would offer the card for a session that is about to be gone.
	if len(watch.served) != 1 || !watch.served[0] {
		t.Errorf("the sign-in was open at each write that served ada: %v, want once, open", watch.served)
	}
	if len(watch.closed) != 1 || !watch.closed[0] {
		t.Errorf("ada's conversation had been asked to start again when the bead was closed: %v, want once, yes", watch.closed)
	}
	if beads, err := st.ListBeads(ctx, store.BeadFilter{Type: store.TypeQuestion}); err != nil || store.SigningIn(beads, "ada", "claude", time.Now()) {
		t.Errorf("ada's sign-in is open after it ended: %v", err)
	}
	if got := ym.Conversations["ada"].RestartRequested; got != "claude was logged in through the yard's asks" {
		t.Errorf("ada's conversation was asked to start again with %q", got)
	}
	if ym.RestartRequested != "" {
		t.Errorf("the line session was asked to start again (%q): the login is not its own", ym.RestartRequested)
	}
	if want := "  ada's conversation with yardmaster starts again on the login\n"; !strings.Contains(out, want) {
		t.Errorf("the output lacks %q:\n%s", want, out)
	}
	// The login was led under ada's folder with the subscription, the page
	// reached ada's inbox, and nothing is left asking: the ask is answered
	// and its bead closed.
	if got := readFile(t, filepath.Join(dir, "logins")); got != "auth login --claudeai\n" {
		t.Errorf("the login command was started as %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "calls")); got != "-p "+tryPrompt+"\n" {
		t.Errorf("the one real call that proves the login: %q", got)
	}
	box, err := st.UserInbox(ctx, "ada", 10, "ada")
	if err != nil || len(box.Mail) != 1 || !strings.Contains(box.Mail[0].Text, loginPage) || strings.Contains(box.Mail[0].Text, "good#s1") {
		t.Errorf("ada's mail: %+v, %v", box, err)
	}
	if q, err := st.UserAsks(ctx, "ada"); err != nil || len(q.Open) != 0 || len(q.Decided) != 1 {
		t.Errorf("ada's asks: %+v, %v", q, err)
	}
	if strings.Contains(out, "good#s1") {
		t.Errorf("the code is printed:\n%s", out)
	}

	// With the yardmaster serving a list, the user joins it; and the sign-in
	// opened again, as JSON, for one who has it.
	mustAs(t, st, "jo", "crew", "set", "yardmaster", "for=ada,jo")
	if out, err = signedIn(t, st, "bob", "user", "add", "bob", "--login", "claude"); err != nil {
		t.Fatalf("user add bob --login claude: %v\n%s", err, out)
	}
	bobEnv := filepath.Join(home, "groups", "bob.env")
	if ym, err = st.GetCrew(ctx, "yardmaster"); err != nil || strings.Join(ym.Served(), ",") != "ada,bob,jo" || ym.EnvFor("bob") != bobEnv {
		t.Errorf("the yardmaster after bob: %+v, %v", ym.Config, err)
	}
	// The sign-in opened again starts again only a conversation that sits at
	// the screen of the harness it signs in to: not hers at another harness's
	// screen, nor hers running on a login.
	for _, c := range []struct {
		name  string
		plant func()
	}{
		{"another harness's screen", func() { sitAt(t, st, "ada", "codex", atLoginScreen) }},
		{"no screen", func() { sitAt(t, st, "ada", "claude", "") }},
	} {
		again := &signInWatch{API: st, user: "ada", atAsk: c.plant}
		rememberEnv(t, again, yardEnv(t, st))
		if out, err = signedIn(t, again, "ada", "user", "login", "ada", "--json"); err != nil {
			t.Fatalf("user login ada: %v\n%s", err, out)
		}
		if ym, err = st.GetCrew(ctx, "yardmaster"); err != nil || ym.Conversations["ada"].RestartRequested != "" {
			t.Errorf("ada's conversation at %s was asked to start again: %+v, %v", c.name, ym.Conversations["ada"], err)
		}
	}
	var in userSignIn
	if err := json.Unmarshal([]byte(out), &in); err != nil || !in.Mailed || !in.SignedIn || in.Bead == "" || in.Ask == 0 || in.Error != "" {
		t.Errorf("user login ada --json = %s (%v)", out, err)
	}

	// Removing ada takes what carries her login in the one write, says so,
	// and leaves the folder and the env file.
	out = mustAs(t, st, "jo", "user", "rm", "ada")
	for _, want := range []string{
		"user ada removed\n",
		"  Groups that carry their login go with them: ada-builders.\n",
		"  Their routes go with them (2 routes).\n",
		"  They are taken out of for= and env_for_ada of yardmaster.\n",
		"  Their login stays on disk, in the folder the env file " + env + " names; no command erases it yet.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rm's output lacks %q:\n%s", want, out)
		}
	}
	if _, err := st.GetGroup(ctx, "ada-builders"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ada's group after rm: %v", err)
	}
	if got := operatorRoutes(); got != before+2 {
		t.Errorf("%d routes after rm, want the operator's %d and bob's two", got, before)
	}
	if ym, err = st.GetCrew(ctx, "yardmaster"); err != nil || ym.EnvFor("ada") != "" || strings.Join(ym.Served(), ",") != "bob,jo" {
		t.Errorf("the yardmaster after rm: %+v, %v", ym.Config, err)
	}
	for _, path := range []string{folder, env} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s after rm: %v", path, err)
		}
	}
	// A name is reusable and the first's login must not be the second's.
	if out, err := as(t, st, "jo", "user", "add", "ada", "--login", "claude"); err == nil || !strings.Contains(err.Error(), folder+" exists") {
		t.Errorf("ada added again over her old login: %v\n%s", err, out)
	}
}

// A sign-in that nobody finishes ends within the login's own timeout: the
// person is made, the page was mailed, the ask for the code is taken back and
// its bead closed, and the command says how to open it again.
func TestUserAddWithALoginNobodyFinishes(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	fakeTools(t, map[string]string{"claude": loginStandIn})
	stub(t, &tryHarnessCall, realTry)
	stub(t, &loginInputTimeout, 300*time.Millisecond)
	mustAs(t, st, "jo", "group", "add", "builders", "--runner", "herdr", "--set", "kind=claude")
	mustAs(t, st, "jo", "route", "add", "--stage", "new", "--group", "builders")
	// Her conversation sits at the login screen all the while, as the daemon
	// starts it by then.
	watch := &signInWatch{API: st, user: "cy", atAsk: func() { sitAt(t, st, "cy", "claude", atLoginScreen) }}
	rememberEnv(t, watch, yardEnv(t, st))
	out, err := as(t, watch, "jo", "user", "add", "cy", "--login", "claude")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("user add cy --login claude = %v, want the failed sign-in\n%s", err, out)
	}
	for _, want := range []string{"user cy added\n", "  the page to sign in on is mailed to cy\n", "no input was given within 300ms", "  yardr user login cy opens it again\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if _, err := st.GetGroup(ctx, "cy-builders"); err != nil {
		t.Errorf("cy's group: %v", err)
	}
	if q, err := st.UserAsks(ctx, "cy"); err != nil || len(q.Open) != 0 {
		t.Errorf("cy's asks after the sign-in ended: %+v, %v", q, err)
	}
	// The login was not made: her session at the screen is left as it is, to
	// be offered the operator's attention now that her sign-in is not open.
	if beads, err := st.ListBeads(ctx, store.BeadFilter{Type: store.TypeQuestion}); err != nil || store.SigningIn(beads, "cy", "claude", time.Now()) {
		t.Errorf("cy's sign-in is open after it ended: %v", err)
	}
	if ym, err := st.GetCrew(ctx, "yardmaster"); err != nil || !ym.Session("cy").Live() || ym.Session("cy").RestartRequested != "" {
		t.Errorf("cy's conversation after a sign-in that made no login: %+v, %v", ym.Session("cy"), err)
	}
	if strings.Contains(out, "starts again") {
		t.Errorf("the output says a session starts again:\n%s", out)
	}
}

// A login that ends well and answers no call is no login the person can work
// on: the sign-in says so, with what the call printed, and opens again the
// same way as one nobody finished.
func TestUserAddWithALoginThatAnswersNothing(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	mute := strings.Replace(loginStandIn, `{ echo ok; exit 0; }`, `{ echo "this plan has no Claude Code" >&2; exit 1; }`, 1)
	if mute == loginStandIn {
		t.Fatal("the stand-in has no call to silence")
	}
	fakeTools(t, map[string]string{"claude": mute})
	stub(t, &tryHarnessCall, realTry)
	mustAs(t, st, "jo", "group", "add", "builders", "--runner", "herdr", "--set", "kind=claude")
	mustAs(t, st, "jo", "route", "add", "--stage", "new", "--group", "builders")
	out, err := signedIn(t, st, "dee", "user", "add", "dee", "--login", "claude")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("user add dee --login claude = %v, want the failed sign-in\n%s", err, out)
	}
	for _, want := range []string{"  dee's sign-in did not finish: the login ended, and one real call to Claude Code did not answer (fails): ", "this plan has no Claude Code", "  yardr user login dee opens it again\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if q, err := st.UserAsks(ctx, "dee"); err != nil || len(q.Open) != 0 {
		t.Errorf("dee's asks after the sign-in ended: %+v, %v", q, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
