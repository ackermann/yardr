package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// A person with a login of their own is made whole by one command
// (docs/groups.md, "A person with a login of their own"): yardr user add
// <name> --login claude makes, in this order, the user; the folder their
// login lands in and the env file that names it; the group that runs on that
// login; the routes that send their beads to it; the yardmaster's
// conversation with them; and opens the sign-in, whose link goes to them and
// whose code they send back. Each step is a command that stands on its own
// (user add, group add, route add --originator, crew set, harness login
// --group), in the order the docs gave them by hand; a slip in any of them
// bills one person's work to another's login, which is why one command makes
// them. Which group runs on the login and which stages it takes are not
// said by the design: the group is a copy of the yard's builders, and the
// routes are a copy of the routes that take them.

// loginUserName is a user's name as a folder takes it: one path element.
var loginUserName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// loginFolderEnvs are the harnesses --login leads, each with the variable
// that names the folder its login lands in. Only claude is wired: the others
// either have no login a program leads or are logged in on their own screen.
var loginFolderEnvs = map[string]string{"claude": claudeFolderEnv}

// userLoginPlan is what user add --login will make, worked out from the yard
// as it stands before anything is written, so that a refusal leaves no
// person half made.
type userLoginPlan struct {
	user, harness string
	// like is the group the person's is a copy of, and group the person's.
	like  store.Group
	group string
	// folder is where their login lands, and env the file that names it,
	// both under the home.
	folder, env string
	// routes are the routes to add: a twin of each route that sends work to
	// like first, for the person's beads.
	routes []store.Route
	// crew is the crew member that talks with them, and serving the names it
	// serves with them added, "" where it serves everyone already.
	crew    string
	serving string
}

// planUserLogin works out what user add --login makes for user, and refuses
// what cannot be made, writing nothing.
func (a *app) planUserLogin(ctx context.Context, st store.API, user, harness, likeName string) (*userLoginPlan, error) {
	if !agents.IsKind(harness) {
		return nil, fmt.Errorf("--login %s is no harness kind; herdr starts: %s", harness, strings.Join(agents.Kinds(), ", "))
	}
	if _, ok := loginFolderEnvs[harness]; !ok {
		return nil, fmt.Errorf("--login %s: only claude is wired (its login folder is one a program names with CLAUDE_CONFIG_DIR, and its sign-in one yardr leads)", harness)
	}
	// The folder and the env file are named by the user, and a user's name may
	// hold path separators: the yard chooses no path for one that does.
	if !loginUserName.MatchString(user) {
		return nil, fmt.Errorf("user %q: the login folder is named by the user, so --login takes letters, digits, '.', '_' and '-', starting with a letter or digit", user)
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	p := &userLoginPlan{user: user, harness: harness,
		folder: filepath.Join(cfg.Home, "logins", user), env: filepath.Join(cfg.Home, "groups", user+".env")}
	// A removed user's folder stays on disk, with their login and their
	// transcripts, and a name is reusable: the first's login must not be the
	// second's. Moving it away is the operator's to say.
	for _, path := range []string{p.folder, p.env} {
		if _, err := os.Lstat(path); err == nil {
			return nil, fmt.Errorf("%s exists, and a login that is there already is not taken over for a user added now (a removed user's stays on disk): move it away, then add %s again", path, user)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	if err := userLoginEnvAvailable(ctx, st, cfg.Home, p.env, groups); err != nil {
		return nil, err
	}
	if p.like, err = likeGroup(groups, likeName, settings[store.DefaultKindSetting], settings[store.OperatorSetting]); err != nil {
		return nil, err
	}
	p.group = user + "-" + p.like.Name
	if slices.ContainsFunc(groups, func(g store.Group) bool { return g.Name == p.group }) {
		return nil, fmt.Errorf("group %s exists: a person's group is made new, so that no login of another's is run under their name", p.group)
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		// The operator's routes to the group: one with an originator is
		// already for someone, and a copy of it would be for no one.
		if r.Worker() != p.like.Name || r.Originator != nil {
			continue
		}
		twin := r
		twin.ID, twin.Group, twin.When, twin.Else, twin.Originator = "", p.group, "", nil, &user
		twin.Note = user + "'s own login (yardr user add --login)"
		p.routes = append(p.routes, twin)
	}
	if len(p.routes) == 0 {
		return nil, fmt.Errorf("no route of the operator's sends work to group %s, so there is no stage to route %s's beads to: route it first (yardr route add), or copy another group with --like", p.like.Name, user)
	}
	p.crew = yardmasterOf(ctx, st)
	ym, err := st.GetCrew(ctx, p.crew)
	if err != nil {
		return nil, fmt.Errorf("the yardmaster talks with %s on their own login, and %w", user, err)
	}
	// Serving everyone already serves them; a list gains the name.
	if served := ym.Served(); len(served) > 0 {
		p.serving = strings.Join(append(served, user), ",")
	}
	// Found before anything is made: a person made and no sign-in to lead is
	// the half-made person this plan is for.
	if _, err := lookPath(agents.Executable(harness)); err != nil {
		return nil, fmt.Errorf("%s is not on PATH, so there is no sign-in to lead: %s", agents.Executable(harness), agents.InstallHint(harness))
	}
	return p, nil
}

// userLoginEnvAvailable refuses to create the login's env file where a
// group or another user's credential already names that path. The file
// need not exist yet for its path to be in use: creating it would change
// what that group or user reads.
func userLoginEnvAvailable(ctx context.Context, st store.API, home, path string, groups []store.Group) error {
	want, err := loginPathIdentity(path)
	if err != nil {
		return fmt.Errorf("login env file %s: %w", path, err)
	}
	for _, g := range groups {
		file, _ := g.Config["env_file"].(string)
		if file == "" {
			continue
		}
		have, err := loginPathIdentity(file)
		if err != nil {
			return fmt.Errorf("group %s env_file %s: %w", g.Name, file, err)
		}
		if have == want {
			return fmt.Errorf("login env file %s is already named by group %s; move that group's env_file first", path, g.Name)
		}
	}
	users, err := st.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		file := u.EnvFile
		if file == "" && !strings.ContainsAny(u.Name, `/\\`) {
			file = filepath.Join(home, "users", u.Name+".env")
		}
		if file == "" {
			continue
		}
		have, err := loginPathIdentity(file)
		if err != nil {
			return fmt.Errorf("user %s env_file %s: %w", u.Name, file, err)
		}
		if have == want {
			return fmt.Errorf("login env file %s is already named by user %s; move that user's env_file first", path, u.Name)
		}
	}
	return nil
}

// loginPathIdentity resolves existing symlinks in the path and its parents,
// while still naming the not-yet-created env file by its resolved parent.
func loginPathIdentity(path string) (string, error) {
	p, err := config.ExpandPath(path)
	if err != nil {
		return "", err
	}
	p = filepath.Clean(p)
	var missing []string
	for current := p; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return p, nil
		}
		missing = append(missing, filepath.Base(current))
	}
}

// likeGroup is the group a person's is a copy of: the one --like names, else
// the yard's builders, which is the group named builders, or the one group
// named <depot>-builders as a pack names a depot's. It must be a herdr group
// of the operator's that runs Claude, since a copy of anything else would
// run under a login of another kind or of another person.
func likeGroup(groups []store.Group, named, defaultKind, operator string) (store.Group, error) {
	var herdr []string
	for _, g := range groups {
		if g.Runner == "herdr" {
			herdr = append(herdr, g.Name)
		}
	}
	pick := func(name string) (store.Group, bool) {
		for _, g := range groups {
			if g.Name == name {
				return g, true
			}
		}
		return store.Group{}, false
	}
	var like store.Group
	switch {
	case named != "":
		g, ok := pick(named)
		if !ok {
			return like, fmt.Errorf("--like %s: group %s: %w", named, named, store.ErrNotFound)
		}
		like = g
	default:
		if g, ok := pick("builders"); ok {
			like = g
			break
		}
		var builders []string
		for _, g := range groups {
			if g.Runner == "herdr" && g.LoginFor() == "" && strings.HasSuffix(g.Name, "-builders") {
				builders = append(builders, g.Name)
			}
		}
		switch len(builders) {
		case 1:
			like, _ = pick(builders[0])
		case 0:
			return like, fmt.Errorf("the yard has no builders group to copy for a person: name the group with --like (the herdr groups are %s)", or(strings.Join(herdr, ", "), "none"))
		default:
			return like, fmt.Errorf("several groups are builders (%s): name the one to copy with --like", strings.Join(builders, ", "))
		}
	}
	if like.Runner != "herdr" {
		return like, fmt.Errorf("group %s is %s, not herdr, and starts no agent a login could carry: copy a herdr group with --like", like.Name, like.Runner)
	}
	if who := like.LoginFor(); who != "" && who != operator {
		return like, fmt.Errorf("group %s runs on %s's login: a person's group is a copy of the operator's, so that no login of another's is burned", like.Name, who)
	}
	if kind := like.Kind(defaultKind); kind != "claude" {
		return like, fmt.Errorf("group %s runs %s, and --login claude signs the person in to Claude: copy a claude group with --like", like.Name, kind)
	}
	return like, nil
}

// userLoginGroup is the group a person's login runs under: a copy of like
// (its prompt, arguments, model and the rest) with kind claude, the person's
// login and their env file. Its limit is 1, as a new group's is: twelve
// people at the limit of a shared builders would overrun the machine, and the
// operator raises one with group set.
func (p *userLoginPlan) userLoginGroup() store.Group {
	cfg := maps.Clone(p.like.Config)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["kind"], cfg["login_for"], cfg["env_file"] = p.harness, p.user, p.env
	return store.Group{Name: p.group, Runner: "herdr", Config: cfg, Limit: 1}
}

// userLoginOf is what carries user's own login in the yard, read from its
// groups, routes and crew as the store reads them when it removes the user.
func userLoginOf(ctx context.Context, st store.API, user string) (store.UserLogin, error) {
	groups, err := st.Groups(ctx)
	if err != nil {
		return store.UserLogin{}, err
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return store.UserLogin{}, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return store.UserLogin{}, err
	}
	return store.LoginOf(user, groups, routes, crew), nil
}

// userLoginMade is what yardr user add --login made, as --json prints it.
type userLoginMade struct {
	User   string      `json:"user"`
	Folder string      `json:"folder,omitempty"`
	Env    string      `json:"env_file,omitempty"`
	Group  string      `json:"group,omitempty"`
	Routes []string    `json:"routes,omitempty"`
	Crew   string      `json:"crew,omitempty"`
	SignIn *userSignIn `json:"sign_in,omitempty"`
}

// userSignIn is how far the sign-in got: the link mailed to the person, the
// ask for their code, whether it ended in a login, and whether the login
// answers a call.
type userSignIn struct {
	Mailed   bool   `json:"mailed"`
	Bead     string `json:"bead,omitempty"`
	Ask      uint64 `json:"ask,omitempty"`
	SignedIn bool   `json:"signed_in"`
	// Proved is whether one real call to the harness answered on the login.
	Proved  bool     `json:"proved"`
	Try     tryCheck `json:"-"`
	Account string   `json:"account,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// addUserLogin is yardr user add <name> --login <harness>: the user, then
// what makes the person whole, then their sign-in. It says each step as it
// is made, so that one that fails shows what stands and how to take it back.
func (a *app) addUserLogin(c *cobra.Command, st store.API, user, harness, like string) error {
	ctx := c.Context()
	p, err := a.planUserLogin(ctx, st, user, harness, like)
	if err != nil {
		return err
	}
	w := c.OutOrStdout()
	made := &userLoginMade{User: user}
	say := func(format string, args ...any) {
		if !a.json {
			fmt.Fprintf(w, format+"\n", args...)
		}
	}
	// The yard holds what was made when a step fails, and takes it back
	// with user rm; the folder stays, as it does there.
	failed := func(step string, err error) error {
		return fmt.Errorf("%s: %w; user %s stands as far as it went, and yardr user rm %s takes back what the yard holds of it (the folder stays on disk)", step, err, user, user)
	}
	if _, err := st.AddUser(ctx, user, a.who()); err != nil {
		return err
	}
	say("user %s added", user)

	if err := os.MkdirAll(filepath.Dir(p.folder), 0o700); err != nil {
		return failed("login folder", err)
	}
	if err := os.Mkdir(p.folder, 0o700); err != nil {
		return failed("login folder", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.env), 0o700); err != nil {
		return failed("env file", err)
	}
	if err := writeNewFile(p.env, []byte(loginFolderEnvs[harness]+"="+p.folder+"\n"), 0o600); err != nil {
		return failed("env file", err)
	}
	made.Folder, made.Env = p.folder, p.env
	say("  login folder %s (0700), env file %s (0600)", p.folder, p.env)

	g := p.userLoginGroup()
	if err := st.PutGroup(ctx, g, a.who()); err != nil {
		return failed("group "+g.Name, err)
	}
	made.Group = g.Name
	say("  group %s (a copy of %s: kind %s, login_for %s, limit %d)", g.Name, p.like.Name, harness, user, g.Limit)

	for _, r := range p.routes {
		saved, err := st.AddRoute(ctx, r, a.who())
		if err != nil {
			return failed("route", err)
		}
		made.Routes = append(made.Routes, saved.ID)
		scope := ""
		if saved.Depot != nil {
			scope = " in " + *saved.Depot
		}
		say("  route %s: %s%s, for %s's beads -> %s", saved.ID, saved.Stage, scope, user, g.Name)
	}

	// The sign-in's bead is made before the conversation is served, since the
	// daemon starts that session as soon as it is, on a login folder that holds
	// no login yet, and reads this bead open to offer the operator no login
	// card for it while the person signs in (store.SigningIn).
	bead, err := a.openSignIn(ctx, st, user, harness)
	if err != nil {
		return failed("the sign-in", err)
	}
	if err := a.serveUser(ctx, st, p); err != nil {
		a.closeSignIn(ctx, st, bead, user)
		return failed("the yardmaster's conversation", err)
	}
	made.Crew = p.crew
	say("  %s serves %s (%s%s)", p.crew, user, store.EnvForPrefix, user)

	// They read the page and give the code in a device or where a source
	// tells them, and a new user has neither: say so while the sign-in waits,
	// which the operator's other terminal can set right.
	say("  %s has no device yet, and reads the page and gives the code at one: yardr user device pair %s <device> --url <the address the yard is reached at>", user, user)
	in, err := a.signIn(c, st, user, g.Name, bead)
	made.SignIn = in
	if a.json {
		if oerr := a.out(w, made, nil); oerr != nil {
			return oerr
		}
	}
	return err
}

// serveUser sets the yardmaster's env_for_<user>, and the user in its for=
// where that names some: the line session keeps the yard, and this one is
// the conversation with the person, on their login.
func (a *app) serveUser(ctx context.Context, st store.API, p *userLoginPlan) error {
	sets := map[string]any{store.EnvForPrefix + p.user: p.env}
	if p.serving != "" {
		sets[store.CrewForKey] = p.serving
	}
	// The daemon re-mints a running crew member's revision between the read
	// and the write now and then: read again and apply the same change.
	for try := 0; ; try++ {
		have, err := st.GetCrew(ctx, p.crew)
		if err != nil {
			return err
		}
		def := *have
		if def.Config, err = store.MergeConfig(p.crew, have.Config, sets, nil); err != nil {
			return err
		}
		if _, err = st.PutCrew(ctx, def, a.who()); err == nil {
			return nil
		}
		if try == 4 || !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
}

// writeNewFile writes data to a file that is not there: a login's env file is
// never written over.
func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// signInQuestion is the question of the ask for the person's code, and the
// title of the bead the ask stands on.
const signInQuestion = "The code the sign-in page showed"

// openSignIn makes the bead user's sign-in stands on, which the code it asks
// for goes on and which stays open for as long as the sign-in does
// (store.SignInMeta). signIn closes it, whatever its end; a caller that
// fails before it gets there closes it itself.
func (a *app) openSignIn(ctx context.Context, st store.API, user, harness string) (string, error) {
	b, err := st.CreateBead(ctx, store.NewBead{For: user, Title: signInQuestion, Type: store.TypeQuestion,
		Body:     "The sign-in of " + user + " to Claude waits for the code its page shows.",
		Metadata: map[string]any{store.SignInMeta: harness}}, a.who())
	if err != nil {
		return "", err
	}
	return b.ID, nil
}

// closeSignIn closes the bead of a sign-in that has ended, in a login or not.
// It is not stopped by the signal that ended the command.
func (a *app) closeSignIn(ctx context.Context, st store.API, bead, user string) {
	end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = st.CloseBead(end, bead, "the sign-in of "+user+" ended", store.Expect{}, a.who())
}

// signIn leads user's sign-in under group's login and relays it to them:
// the page to sign in on is mailed to them, and the code it shows is asked
// of them (a login-input secret ask, on bead) and handed to the waiting
// login. It waits for the login to end, within the login's own timeouts, and
// says how it went; a login that did not end in one is an error, after the
// line that says why, with the command that opens it again. A login that
// ended well starts the person's conversation sessions that sat at the login
// screen again, before the bead is closed.
func (a *app) signIn(c *cobra.Command, st store.API, user, group, bead string) (*userSignIn, error) {
	// A closed terminal ends the command as a stop does: the bead must not
	// outlive it, since the daemon reads it open as a sign-in in progress. Not
	// when it runs under nohup: asking for the signal would undo the ignoring.
	stops := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		stops = append(stops, syscall.SIGHUP)
	}
	ctx, stop := signal.NotifyContext(c.Context(), stops...)
	defer stop()
	defer a.closeSignIn(ctx, st, bead, user)
	// What is said while the sign-in waits goes to the screen, and in JSON
	// mode to stderr, where the one document of the run is not.
	w := c.OutOrStdout()
	if a.json {
		w = c.ErrOrStderr()
	}
	say := func(format string, args ...any) { fmt.Fprintf(w, "  "+format+"\n", args...) }
	in := &userSignIn{}
	fail := func(err error) (*userSignIn, error) {
		in.Error = err.Error()
		say("%s's sign-in did not finish: %v", user, err)
		say("yardr user login %s opens it again", user)
		return in, errCheckFailed
	}
	h, ok := agents.Lookup("claude")
	if !ok {
		return fail(errors.New("yardr has no harness file for claude"))
	}
	env, file, err := groupLoginEnv(ctx, st, group)
	if err != nil {
		return fail(err)
	}
	path, err := lookPath(agents.Executable(h.Kind))
	if err != nil {
		return fail(fmt.Errorf("%s is not on PATH, so there is nothing to log in: %s", agents.Executable(h.Kind), agents.InstallHint(h.Kind)))
	}

	needs, needsOut := io.Pipe()
	answersIn, answers := io.Pipe()
	leadCtx, endLead := context.WithCancel(ctx)
	defer endLead()
	led := make(chan error, 1)
	go func() {
		err := leadLogin(leadCtx, h, path, answersIn, needsOut, env, group, st)
		// Nothing reads the answers of a login that has ended: a write still
		// waiting for the leader to take it is let go.
		answersIn.Close()
		needsOut.CloseWithError(err)
		led <- err
	}()
	open, relayed := a.relayNeeds(leadCtx, st, user, bead, needs, answers, in, say)
	if relayed != nil {
		// Whatever stopped the relaying, the login must end: it is stopped,
		// and what it still prints is read to its end so it is not left
		// blocked on a pipe nobody reads.
		endLead()
	}
	_ = answers.Close()
	_, _ = io.Copy(io.Discard, needs)
	err = <-led
	if relayed != nil {
		err = relayed
	}
	// What was asked of them and not answered is taken back, and the bead the
	// asks stand on is closed on the way out: a sign-in that ended leaves
	// nothing waiting.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, q := range open {
		_ = st.TakeBackAsk(cleanup, q.bead, q.seq, "the sign-in ended", a.who())
	}
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			err = errors.New("stopped before it ended")
		case errors.Is(err, errCheckFailed):
			// The login command ended well and the call did not answer.
			err = fmt.Errorf("the login ended, and one real call to %s did not answer (%s): %s", h.Label, in.Try.State, in.Try.Output)
		}
		return fail(err)
	}
	in.SignedIn = true
	in.Account = loginFolderAccount(env)
	said := user + " is signed in"
	if in.Account != "" {
		said += " as " + in.Account
	}
	if in.Proved {
		said += ", and one call to " + h.Label + " answered"
	}
	say("%s (the login is in %s)", said, file)
	a.restartSeats(cleanup, st, user, h.Kind, say)
	return in, nil
}

// restartSeats starts user's conversation sessions that sit at the harness's
// login screen again: they started on a folder that held no login, and the
// harness reads its login as it starts. While the sign-in is open the
// daemon offers the operator no card for them (store.SigningIn), so nothing
// else would start them again, as the daemon does for a login it leads
// itself (docs/crew.md). The bead is still open: the daemon must not see the
// sign-in ended while a session at the screen is still to be restarted.
func (a *app) restartSeats(ctx context.Context, st store.API, user, kind string, say func(string, ...any)) {
	crew, err := st.Crew(ctx)
	if err != nil {
		say("%s's conversation could not be looked up to start it again: %v", user, err)
		return
	}
	def := defaultKind(ctx, st)
	for _, r := range crew {
		cs := r.Session(user)
		// loginKindOf reads the handle of the member's line session: this is
		// the user's session's.
		r.Handle = cs.Handle
		if !cs.LoginScreen || loginKindOf(def, r) != kind {
			continue
		}
		reason := kind + " was logged in through the yard's asks"
		if err := st.RequestRestartFor(ctx, r.Name, user, reason, "", a.who()); err != nil {
			say("%s's conversation with %s sits at the login screen and could not be started again (%v): yardr crew restart %s --for %s does", user, r.Name, err, r.Name, user)
			continue
		}
		say("%s's conversation with %s starts again on the login", user, r.Name)
	}
}

// askedCode is a code asked of the person: the bead it stands on and its seq.
type askedCode struct {
	bead string
	seq  uint64
}

// relayNeeds reads the needs the leader prints, a JSON line each, and relays
// them: it chooses the harness's first way to log in, mails the page to sign
// in on to the person, asks them for the code the page shows (a login-input
// secret ask, which the waiting login takes), and agrees to the one real call
// that proves the login: it costs a model call on their seat, and a login
// that ended well but answers nothing (a plan without Claude Code, say) is
// better found now than at their first bead. It answers the asks it made, and
// why it stopped early when it did.
func (a *app) relayNeeds(ctx context.Context, st store.API, user, bead string, needs io.Reader, answers io.Writer, in *userSignIn, say func(string, ...any)) ([]askedCode, error) {
	var open []askedCode
	scan := bufio.NewScanner(needs)
	scan.Buffer(nil, loginLineMax)
	for scan.Scan() {
		var line struct {
			loginNeed
			Done   bool     `json:"done"`
			Proved bool     `json:"proved"`
			Try    tryCheck `json:"try"`
		}
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			continue
		}
		switch {
		case line.Need == needChoose && len(line.Options) > 0:
			// Claude's own subscription is the first way the harness lists,
			// and the one a Team or Enterprise seat is signed in with.
			b, _ := json.Marshal(map[string]string{"choose": line.Options[0]})
			_, _ = answers.Write(append(b, '\n'))
		case line.Need == needOpenURL:
			if err := st.Mail(ctx, user, signInMail(user, line.URL), a.who()); err != nil {
				return open, fmt.Errorf("mailing the sign-in page to %s: %w", user, err)
			}
			in.Mailed = true
			say("the page to sign in on is mailed to %s", user)
		case line.Need == needInput && line.Login != "":
			seq, err := st.AddAsk(ctx, bead, store.Ask{
				Question: signInQuestion,
				To:       user,
				Context: []string{"It is the code the page shows once you have signed in to Claude with your own account.",
					"It goes to the sign-in that waits for it, and is kept nowhere."},
				Secret: &store.AskSecret{Login: line.Login, As: user},
			}, a.who())
			if err != nil {
				return open, fmt.Errorf("asking %s for the code: %w", user, err)
			}
			open = append(open, askedCode{bead, seq})
			in.Bead, in.Ask = bead, seq
			say("the code %s pastes back is ask %d on %s; the sign-in waits for it up to %s", user, seq, bead, loginInputTimeout)
		case line.Need == needConfirm:
			_, _ = answers.Write([]byte(`{"confirm":true}` + "\n"))
		case line.Done:
			in.Proved, in.Try = line.Proved, line.Try
		}
	}
	return open, nil
}

// signInMail is the mail that carries the sign-in page to the person.
func signInMail(user, page string) string {
	return fmt.Sprintf("%s, your sign-in to Claude for this yard is waiting.\n"+
		"Open %s and sign in to Claude with your own account. The page then shows a code.\n"+
		"Paste the code into the ask %q, which stands among your questions in footplate and the web view. The sign-in waits for it up to %s.",
		user, page, signInQuestion, loginInputTimeout)
}

// userLoginCmd is yardr user login <name>: the sign-in of user add --login
// opened again, for a person whose group carries their login.
func (a *app) userLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use: "login <name>", Short: "Open a user's sign-in again: the page mailed to them, the code asked of them", Args: cobra.ExactArgs(1),
		Long: `Leads the sign-in of a user whose login a group carries (the group whose
login_for is the user, which yardr user add --login made), as that command
does at its end: the harness's own login runs into the group's folder, the
page to sign in on is mailed to the user, and the code it shows is asked of
them, a secret that goes to the waiting login and is kept nowhere. It waits
for the login to end, within the login's own timeouts (docs/doctor.md),
proves it with one short call on the user's seat, starts their conversation
again on it where it sits at the login screen, and says how it went. Use it
when the sign-in did not finish, or to move a person's seat: the folder it
signs in to is the one the group's env_file names.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if _, err := st.GetUser(c.Context(), args[0]); err != nil {
				return err
			}
			groups, err := st.Groups(c.Context())
			if err != nil {
				return err
			}
			var theirs []string
			for _, g := range groups {
				if g.LoginFor() == args[0] {
					theirs = append(theirs, g.Name)
				}
			}
			switch len(theirs) {
			case 0:
				return fmt.Errorf("no group carries %s's login (a group whose login_for is %s): yardr user add %s --login claude makes one", args[0], args[0], args[0])
			case 1:
			default:
				return fmt.Errorf("several groups carry %s's login (%s), and a person signs in once: yardr harness login claude --group <group> leads one of them", args[0], strings.Join(theirs, ", "))
			}
			bead, err := a.openSignIn(c.Context(), st, args[0], "claude")
			if err != nil {
				return err
			}
			in, err := a.signIn(c, st, args[0], theirs[0], bead)
			if a.json {
				if oerr := a.out(c.OutOrStdout(), in, nil); oerr != nil {
					return oerr
				}
			}
			return err
		},
	}
}
