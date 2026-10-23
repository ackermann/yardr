package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// The yard leads a harness's login itself when a crew member sits at its
// login screen (yardr-302g). Design yardr-bwm2 3.5 had the yardmaster relay
// `yardr harness login <kind> --json` to the person, but the yardmaster is
// an agent of that harness, and it cannot start while the harness is logged
// out: on a hosted yard nobody led the first login, and mail to the
// yardmaster sat unread with nothing said of why.
//
// So the daemon leads it, with no agent in the loop. Its steps are asks of
// the yard's operator on the crew member's journal:
//
//  1. Which harness, and which of its methods: one option per method of
//     each kind installed here whose file has a login recipe, and one
//     "API key" option per kind whose file names a login_env. Nothing
//     runs until it is answered, so a person who does nothing sees this
//     one ask.
//  2. The pick starts the login leader, a child of the daemon on its own
//     binary, and answers its choose need with the method. Its open_url is
//     the link of the next ask; its input is a secret ask naming the wait
//     the leader registered (store.LoginWait), which GiveSecret answers in
//     the server's memory: the daemon never holds the value, and no note,
//     event, file or mail does. An API key pick instead asks for the key
//     as a secret ask, which GiveSecret writes into the operator's key
//     file of the kind (store.HarnessKeyFile): no leader runs, and no
//     login screen is read. Every session of the kind on the operator's
//     login reads that file as it starts (the runner), so the key reaches
//     what the machine's login does: the crew, the groups without
//     login_for and the operator's conversations. A session on another
//     user's login reads that user's file, which no ask writes yet.
//  3. Its confirm, the one call that proves the login, is an ask of its
//     own: it costs a model call on the person's account.
//  4. Done: every session of the kind sitting at its login screen is
//     restarted: a crew session as yardr crew restart does, and a bead
//     session ended as aborted, so its bead is routed again and a new
//     session starts on the login. A key's done is the same restart, on
//     the secret ask's answer, of the sessions on the operator's login
//     alone: the others read no key and would sit at the screen again.
//
// One login is led per kind at a time: the machine's login of a kind is
// every crew member's of that kind. A login that ends without one (the
// leader's own timeouts, a decline) takes its open ask back and is offered
// again on the next held nudge, mail that came after it ended, never on
// every pass.

// HarnessLoginAskKey is a handle key of the crew session a led login was
// offered for: the journal its asks are on, the ask it waits on and what
// step that ask is. A daemon that starts again finds its first ask here
// and waits on it again; an ask of a later step, whose leader went with
// the daemon, it takes back. Crew says the mark as login_led, and the store
// raises crew_login when it is put on or taken off.
const HarnessLoginAskKey = store.HarnessLoginAskKey

// The steps of a led login: which ask it waits on.
const (
	leadPick    = "pick"
	leadInput   = "input"
	leadConfirm = "confirm"
	leadKey     = "key"
)

// apiKeyMethod is the method of an "API key" choice: the option's label
// after the kind's, as a recipe method's label is.
const apiKeyMethod = "API key"

// LoginLeader is a running `yardr harness login <kind> --json`: the JSON
// lines it prints, the JSON lines it is answered with, and its end.
type LoginLeader struct {
	Needs   io.Reader
	Answers io.WriteCloser
	// Wait waits for the leader to end, after Needs has been read to its
	// end, and says how it ended.
	Wait func() error
}

// leadLine is one line a login leader prints: a need, the end, or why it
// failed (cli's loginNeed, loginDone and the --json error).
type leadLine struct {
	Need    string   `json:"need"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	URL     string   `json:"url"`
	Login   string   `json:"login"`
	Done    bool     `json:"done"`
	Proved  bool     `json:"proved"`
	Error   string   `json:"error"`
}

// harnessLead is one login the yard leads, for one kind.
type harnessLead struct {
	kind string
	// method is the picked method's label, "" for a kind with none.
	method string
	// seat is the session it was offered for: its journal holds the asks.
	seat loginSeat
	// ask is the ask it waits on, of step; 0 while it waits on the leader.
	ask  uint64
	step string
	// urls are the sign-in pages the leader printed, for the next ask.
	urls []string
	// switched names crew members whose kind the pick changed: they are
	// restarted on the new kind however their screens read.
	switched []string

	leader *LoginLeader
	lines  chan leadLine
	ended  chan error
	// stop ends the leader's context, which ends the leader, and the
	// reading of its lines with it.
	stop context.CancelFunc
	// done and failed are what the leader's lines said of its end.
	done, proved bool
	failed       string
}

// loginSeat is a crew session at its harness's login screen.
type loginSeat struct {
	member, user, journal, kind string
}

func (s loginSeat) key() string {
	if s.user == "" {
		return "crew:" + s.member
	}
	return "crew:" + s.member + ":" + s.user
}

// loginRest is a kind whose led login ended without one: it is offered
// again once a session at its screen has mail newer than head.
type loginRest struct{ head uint64 }

// seatsAtLogin are the live crew sessions of rs marked at their harness's
// login screen, in a stable order, each with its kind.
func seatsAtLogin(rs []store.CrewMember, def string) []loginSeat {
	var out []loginSeat
	for _, r := range rs {
		users := append([]string{""}, slices.Sorted(maps.Keys(r.Conversations))...)
		for _, u := range users {
			st := r.Session(u)
			if !st.Live() || st.Handle[HarnessLoginKey] == nil {
				continue
			}
			out = append(out, loginSeat{member: r.Name, user: u, journal: r.Journal, kind: seatKind(r, st.Handle, def)})
		}
	}
	return out
}

// seatKind is the harness kind of a crew session: the kind the runner
// started it as, the kind its member's config names, the yard's default,
// or the runner's.
func seatKind(r store.CrewMember, handle map[string]any, def string) string {
	if k, _ := handle["kind"].(string); k != "" {
		return k
	}
	if k, _ := r.Config["kind"].(string); k != "" {
		return k
	}
	if def != "" {
		return def
	}
	return agents.DefaultKind
}

// loginChoice is one option of the first ask: a kind, and one of its
// methods ("" for a kind with none), or its API key.
type loginChoice struct {
	kind, method string
	apiKey       bool
}

// loginChoices are the kinds the yard can lead a login of, each by method,
// and each by API key: installed here, with a login recipe or a login_env
// in their file. The key follows the kind's methods, so the recommended
// option stays the kind's own login.
func (d *Dispatcher) loginChoices() []loginChoice {
	var out []loginChoice
	for _, h := range agents.Harnesses {
		if !d.harnessInstalled(h.Kind) {
			continue
		}
		if h.LoginRecipe != nil {
			if len(h.LoginRecipe.Methods) == 0 {
				out = append(out, loginChoice{kind: h.Kind})
			}
			for _, m := range h.LoginRecipe.Methods {
				out = append(out, loginChoice{kind: h.Kind, method: m.Label})
			}
		}
		if len(h.LoginEnv) > 0 {
			out = append(out, loginChoice{kind: h.Kind, method: apiKeyMethod, apiKey: true})
		}
	}
	return out
}

func (d *Dispatcher) harnessInstalled(kind string) bool {
	if d.HarnessInstalled != nil {
		return d.HarnessInstalled(kind)
	}
	_, err := exec.LookPath(agents.Executable(kind))
	return err == nil
}

func (c loginChoice) label() string {
	h, _ := agents.Lookup(c.kind)
	if c.method == "" {
		return h.Label
	}
	return h.Label + ": " + c.method
}

// harnessLogins leads the logins of the kinds crew sessions of rs sit at
// the login screen of: it carries on the logins it leads, and offers one
// for a kind that has none. rs is the crew as the pass read it.
func (d *Dispatcher) harnessLogins(ctx context.Context, rs []store.CrewMember) {
	if d.leads == nil {
		d.leads, d.loginRests = map[string]*harnessLead{}, map[string]loginRest{}
	}
	// A kind carried on this pass is not offered on it: rs was read
	// before its login ended, and still shows its sessions at the screen.
	carried := map[string]bool{}
	for _, kind := range slices.Sorted(maps.Keys(d.leads)) {
		l := d.leads[kind]
		carried[kind], carried[l.seat.key()] = true, true
		carried["member:"+l.seat.member] = true
		for _, member := range l.switched {
			carried["member:"+member] = true
		}
		d.carryLead(ctx, l)
	}
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return
	}
	operator := settings[store.OperatorSetting]
	seats := seatsAtLogin(rs, settings[store.DefaultKindSetting])
	for _, seat := range seats {
		if l := d.leadOf(seat); l != nil || operator == "" || carried[seat.kind] || carried[seat.key()] || carried["member:"+seat.member] {
			continue
		}
		if mark := askMark(rs, seat); mark != nil {
			if d.adoptLead(ctx, seat, mark) {
				continue
			}
		}
		if rest, ok := d.loginRests[seat.kind]; ok {
			page, err := d.Store.InboxFor(ctx, seat.member, seat.user, 1)
			if err != nil || page.Last <= rest.head {
				continue
			}
		}
		if d.ownSignIn(ctx, rs, seat) {
			continue
		}
		d.offerLogin(ctx, seat, operator)
	}
}

// ownSignIn reports whether the seat is a person's conversation on a login of
// their own that the person is signing in to now (yardr user add --login,
// yardr user login). The card offerLogin makes leads a login with this
// daemon's env, which is the machine's login and not the folder that env_for_
// names (docs/crew.md), so for such a seat it cannot do what it says: the
// person's own sign-in is the way, and its command starts the session again
// when it ends well. The seat's env is the env_file of a group that carries
// that user's login, and the sign-in is the open bead store.SigningIn reads.
// A session already to start again needs no card either: the pass that carries
// the restart out still reads the old session in rs. A sign-in that ends
// without a login closes its bead, and the seat is offered like any other.
func (d *Dispatcher) ownSignIn(ctx context.Context, rs []store.CrewMember, seat loginSeat) bool {
	i := slices.IndexFunc(rs, func(r store.CrewMember) bool { return r.Name == seat.member })
	if i < 0 {
		return false
	}
	// The line session has no user, so no env_for_ either.
	env := rs[i].EnvFor(seat.user)
	if env == "" {
		return false
	}
	groups, err := d.Store.Groups(ctx)
	if err != nil || !slices.ContainsFunc(groups, func(g store.Group) bool {
		file, _ := g.Config["env_file"].(string)
		return g.LoginFor() == seat.user && sameEnvFile(env, file)
	}) {
		return false
	}
	if rs[i].Session(seat.user).RestartRequested != "" {
		return true
	}
	beads, err := d.Store.ListBeads(ctx, store.BeadFilter{Type: store.TypeQuestion})
	return err == nil && store.SigningIn(beads, seat.user, seat.kind, d.now())
}

// sameEnvFile reports whether two env_file settings name one file, as the
// runner reads either: with a leading ~/ expanded.
func sameEnvFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	pa, err := config.ExpandPath(a)
	if err != nil {
		return false
	}
	pb, err := config.ExpandPath(b)
	return err == nil && filepath.Clean(pa) == filepath.Clean(pb)
}

// leadOf is the login led for the seat's kind, or one whose pick switches
// the seat's crew member to another kind.
func (d *Dispatcher) leadOf(seat loginSeat) *harnessLead {
	if l := d.leads[seat.kind]; l != nil {
		return l
	}
	for _, l := range d.leads {
		if (l.seat.member == seat.member && l.seat.user == seat.user) || slices.Contains(l.switched, seat.member) {
			return l
		}
	}
	return nil
}

// askMark is the HarnessLoginAskKey mark of the seat's session in rs.
func askMark(rs []store.CrewMember, seat loginSeat) map[string]any {
	for _, r := range rs {
		if r.Name == seat.member {
			m, _ := r.Session(seat.user).Handle[HarnessLoginAskKey].(map[string]any)
			return m
		}
	}
	return nil
}

// adoptLead takes up the login whose mark a session carries, from a
// daemon before this one: its first ask is waited on again, or acted on
// where it was answered meanwhile, and so is an API key's secret ask,
// which has no leader to have gone with that daemon. An open ask of a
// later led step is taken back, since its leader went with that daemon.
// It reports whether a login is led for the seat again.
func (d *Dispatcher) adoptLead(ctx context.Context, seat loginSeat, mark map[string]any) bool {
	var seq float64
	switch n := mark["ask"].(type) {
	case float64:
		seq = n
	case uint64:
		seq = float64(n)
	}
	step, _ := mark["step"].(string)
	kind, _ := mark["kind"].(string)
	journal, _ := mark["journal"].(string)
	if journal == "" {
		journal = seat.journal
	}
	state, _ := d.askState(ctx, journal, uint64(seq))
	if (step == leadPick || step == leadKey) && (state == store.AskOpen || state == store.AskAside || state == store.AskAnswered) {
		l := &harnessLead{kind: kind, seat: seat, ask: uint64(seq), step: step}
		l.seat.journal = journal
		if l.kind == "" {
			l.kind = seat.kind
		}
		d.leads[l.kind] = l
		return true
	}
	if state == store.AskOpen && seq > 0 {
		_ = d.Store.TakeBackAsk(ctx, journal, uint64(seq),
			"the yard's daemon that led this login stopped, and the login with it: the yard asks again", actor)
	}
	d.markLeadAsk(ctx, seat, nil)
	return false
}

// askState is the state of the ask seq on bead, with its current answer.
func (d *Dispatcher) askState(ctx context.Context, bead string, seq uint64) (store.AskState, *store.ResolvedAnswer) {
	notes, err := d.Store.Notes(ctx, bead)
	if err != nil {
		return store.AskOpen, nil
	}
	for _, a := range store.ResolveAsks(notes, d.now()) {
		if a.Note.Seq == seq {
			return a.State, a.Current
		}
	}
	// No such ask: as good as taken back.
	return store.AskTakenBack, nil
}

// offerLogin asks the operator which harness logs in, and how: the first
// step of a led login for the seat.
func (d *Dispatcher) offerLogin(ctx context.Context, seat loginSeat, operator string) {
	choices := d.loginChoices()
	key := "login:" + seat.key()
	if len(choices) == 0 {
		if d.status[key] != "no recipe" {
			d.status[key] = "no recipe"
			d.Log.Warn("a crew member sits at its login screen, and no harness installed here has a login the yard can lead",
				"crew", seat.member, "kind", seat.kind)
		}
		return
	}
	delete(d.status, key)
	h, _ := agents.Lookup(seat.kind)
	who := seat.member
	if seat.user != "" {
		who += " (for " + seat.user + ")"
	}
	shown := h.Label
	if shown == "" {
		shown = seat.kind
	}
	lines := []string{
		fmt.Sprintf("%s shows %s's login screen, so it reads no mail until the harness is logged in.", who, shown),
		"The yard runs the harness's own login for you and asks you here for each step: a page to sign in on, then what the page gives you.",
	}
	if slices.ContainsFunc(choices, func(c loginChoice) bool { return c.apiKey }) {
		lines = append(lines,
			fmt.Sprintf("An API key choice instead asks only for the key, and writes it into %s's environment.", seat.member),
			"What you type at a login's asks goes to the login and is kept nowhere; a key is kept in that file alone.")
	} else {
		lines = append(lines, "What you type at those asks goes to the login and is kept nowhere.")
	}
	ask := store.Ask{
		Question: fmt.Sprintf("%s is not logged in: how should the yard log in its harness?", who),
		To:       operator,
		Context:  lines,
	}
	for i, c := range choices {
		ch, _ := agents.Lookup(c.kind)
		consequence := fmt.Sprintf("runs %s's own login, and every crew member on %s uses it", ch.Label, ch.Label)
		if c.apiKey {
			consequence = fmt.Sprintf("asks for the API key, writes it into %s's environment, and starts %s again on it", seat.member, seat.member)
		}
		if c.kind != seat.kind {
			consequence = fmt.Sprintf("makes %s and the yard's default run %s, then %s", seat.member, ch.Label, consequence)
		}
		ask.Options = append(ask.Options, store.AskOption{Label: c.label(), Consequence: consequence})
		if ask.Recommend == 0 && c.kind == seat.kind {
			ask.Recommend, ask.Why = i+1, fmt.Sprintf("%s already runs %s", who, shown)
		}
	}
	seq, err := d.Store.AddAsk(ctx, seat.journal, ask, actor)
	if err != nil {
		d.Log.Warn("ask for a harness's login", "crew", seat.member, "err", err)
		return
	}
	delete(d.loginRests, seat.kind)
	l := &harnessLead{kind: seat.kind, seat: seat, ask: seq, step: leadPick}
	d.leads[seat.kind] = l
	d.markLeadAsk(ctx, seat, l)
	_ = d.Store.LogDispatch(ctx, seat.journal, "login_led", actor, map[string]any{
		"crew": seat.member, "kind": seat.kind, "step": "asked", "ask": seq,
		"reason": fmt.Sprintf("%s sits at its harness's login screen: the yard asked %s how to log it in (ask %d)", who, operator, seq)})
	d.Log.Info("asked how to log a harness in", "crew", seat.member, "kind", seat.kind, "ask", seq)
}

// markLeadAsk keeps the ask l waits on on the handle of its seat's
// session, or takes the mark off for nil.
func (d *Dispatcher) markLeadAsk(ctx context.Context, seat loginSeat, l *harnessLead) {
	r, err := d.Store.GetCrew(ctx, seat.member)
	if err != nil {
		return
	}
	h := maps.Clone(r.Session(seat.user).Handle)
	if h == nil {
		h = map[string]any{}
	}
	if l == nil || l.ask == 0 {
		if h[HarnessLoginAskKey] == nil {
			return
		}
		delete(h, HarnessLoginAskKey)
	} else {
		h[HarnessLoginAskKey] = map[string]any{"journal": l.seat.journal, "ask": l.ask, "step": l.step, "kind": l.kind}
	}
	if err := d.Store.SetCrewHandleFor(ctx, seat.member, seat.user, h); err != nil {
		d.Log.Warn("mark the login's ask on the crew session", "crew", seat.member, "err", err)
	}
}

// carryLead takes l as far as it goes on this pass: an answer to the ask
// it waits on, what its leader printed, and its end.
func (d *Dispatcher) carryLead(ctx context.Context, l *harnessLead) {
	if l.ask != 0 {
		state, cur := d.askState(ctx, l.seat.journal, l.ask)
		switch {
		case state == store.AskAnswered && l.step == leadPick:
			if !d.picked(ctx, l, cur) {
				return
			}
		case state == store.AskAnswered && l.step == leadConfirm:
			yes := cur != nil && cur.Answer.Option == 1
			l.ask = 0
			d.answerLeader(l, map[string]any{"confirm": yes})
		case state == store.AskAnswered && l.step == leadInput:
			// Given: the value went to the leader in the server's memory.
			l.ask = 0
		case state == store.AskAnswered && l.step == leadKey:
			// Given: the key is in the operator's key file of the kind,
			// and no leader runs, so the login is made with the restart.
			l.ask = 0
			d.loggedIn(ctx, l)
		case state == store.AskDeclined || state == store.AskTakenBack:
			l.ask = 0
			if l.leader == nil {
				d.endLead(ctx, l, "the ask was "+state.String())
				return
			}
			if l.step == leadConfirm {
				d.answerLeader(l, map[string]any{"confirm": false})
			} else {
				// Nothing else answers it: the leader stops when its
				// stdin closes.
				_ = l.leader.Answers.Close()
			}
		}
	}
	if l.leader == nil {
		return
	}
	d.drainLead(ctx, l)
	select {
	case err := <-l.ended:
		// What it printed before its end is read first.
		d.drainLead(ctx, l)
		switch {
		case l.done && err == nil:
			d.loggedIn(ctx, l)
		case l.failed != "":
			d.endLead(ctx, l, l.failed)
		case err != nil:
			d.endLead(ctx, l, err.Error())
		default:
			d.endLead(ctx, l, "the login ended without saying it was made")
		}
	default:
	}
}

// drainLead acts on every line l's leader printed that was not read yet.
func (d *Dispatcher) drainLead(ctx context.Context, l *harnessLead) {
	for {
		select {
		case line := <-l.lines:
			d.leadSaid(ctx, l, line)
		default:
			return
		}
	}
}

// picked acts on the answer to the first ask: the kind and method it
// names, the crew member's kind where that changes, and the leader
// started. It reports whether the lead goes on.
func (d *Dispatcher) picked(ctx context.Context, l *harnessLead, cur *store.ResolvedAnswer) bool {
	choices := d.loginChoices()
	n := 0
	if cur != nil {
		n = cur.Answer.Option
	}
	// The option yardr adds, You decide, is the recommendation, or the
	// first.
	if n < 1 || n > len(choices) {
		n = 1
		for i, c := range choices {
			if c.kind == l.seat.kind {
				n = i + 1
				break
			}
		}
	}
	if len(choices) == 0 {
		d.endLead(ctx, l, "no harness installed here has a login the yard can lead")
		return false
	}
	c := choices[n-1]
	if c.kind != l.kind {
		answeredBy := actor
		if cur != nil && cur.Note.Author != "" {
			answeredBy = cur.Note.Author
		}
		if err := d.switchKind(ctx, l.seat, c.kind, answeredBy); err != nil {
			d.endLead(ctx, l, err.Error())
			return false
		}
		delete(d.leads, l.kind)
		if other := d.leads[c.kind]; other != nil {
			// That kind's login is led already: this crew member waits on
			// it, and is restarted with the rest of its kind.
			if !slices.Contains(other.switched, l.seat.member) {
				other.switched = append(other.switched, l.seat.member)
			}
			d.markLeadAsk(ctx, l.seat, nil)
			return false
		}
		l.kind = c.kind
		if !slices.Contains(l.switched, l.seat.member) {
			l.switched = append(l.switched, l.seat.member)
		}
		d.leads[c.kind] = l
	}
	l.method, l.ask, l.step = c.method, 0, ""
	if c.apiKey {
		if err := d.askKey(ctx, l); err != nil {
			d.endLead(ctx, l, err.Error())
			return false
		}
	} else if err := d.startLeader(ctx, l); err != nil {
		d.endLead(ctx, l, err.Error())
		return false
	}
	reason := fmt.Sprintf("%s was picked: the yard runs its login", c.label())
	if c.apiKey {
		reason = fmt.Sprintf("%s was picked: the yard asks for the API key", c.label())
	}
	_ = d.Store.LogDispatch(ctx, l.seat.journal, "login_led", actor, map[string]any{
		"crew": l.seat.member, "kind": l.kind, "step": "started", "method": c.method,
		"reason": reason})
	return true
}

// switchKind makes the seat's crew member, and the yard's default, run
// kind: the operator's pick of another harness than it runs.
func (d *Dispatcher) switchKind(ctx context.Context, seat loginSeat, kind, answeredBy string) error {
	r, err := d.Store.GetCrew(ctx, seat.member)
	if err != nil {
		return err
	}
	cfg := maps.Clone(r.Config)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["kind"] = kind
	r.Config = cfg
	if _, err := d.Store.PutCrew(ctx, *r, answeredBy); err != nil {
		return fmt.Errorf("making %s run %s: %w", seat.member, kind, err)
	}
	// The answer to this operator-only ask authorizes the setting change.
	if err := d.Store.SetSetting(ctx, store.DefaultKindSetting, kind, answeredBy); err != nil {
		return fmt.Errorf("making %s the yard's default kind: %w", kind, err)
	}
	return nil
}

// startLeader starts the login leader of l's kind and reads what it prints
// into l.lines, waking the loop for each line and for its end.
func (d *Dispatcher) startLeader(ctx context.Context, l *harnessLead) error {
	lead := d.LeadLogin
	if lead == nil {
		lead = leadLoginProcess
	}
	ctx, stop := context.WithCancel(ctx)
	leader, err := lead(ctx, l.kind)
	if err != nil {
		stop()
		return fmt.Errorf("starting the login of %s: %w", l.kind, err)
	}
	l.leader, l.stop = leader, stop
	l.lines, l.ended = make(chan leadLine, 16), make(chan error, 1)
	go func() {
		lines := bufio.NewScanner(leader.Needs)
		for lines.Scan() {
			var line leadLine
			if json.Unmarshal(lines.Bytes(), &line) != nil {
				continue
			}
			select {
			case l.lines <- line:
				d.wake()
			case <-ctx.Done():
				// Over: nobody reads what is left.
			}
		}
		l.ended <- leader.Wait()
		d.wake()
	}()
	return nil
}

// leadLoginProcess runs this yardr's `harness login <kind> --json` as a
// child of the daemon, with the daemon's environment: it finds the yard's
// store as the daemon does, and registers its inputs' waits there.
func leadLoginProcess(ctx context.Context, kind string) (*LoginLeader, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, "harness", "login", kind, "--json")
	answers, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Its stdin closing is how a leader is stopped: it stops the harness's
	// login command, its whole group, before it ends. Killed, it would
	// leave that command listening.
	cmd.Cancel = answers.Close
	cmd.WaitDelay = leaderStopWait
	needs, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &LoginLeader{Needs: needs, Answers: answers, Wait: cmd.Wait}, nil
}

// leaderStopWait is how long a login leader whose stdin was closed has to
// stop its login command before it is killed.
const leaderStopWait = 10 * time.Second

// wake runs the loop's next pass soon, when Run runs it.
func (d *Dispatcher) wake() {
	if d.poke != nil {
		d.poke()
	}
}

// answerLeader writes one answer line to l's leader.
func (d *Dispatcher) answerLeader(l *harnessLead, ans map[string]any) {
	if l.leader == nil {
		return
	}
	b, _ := json.Marshal(ans)
	_, _ = l.leader.Answers.Write(append(b, '\n'))
}

// leadSaid acts on one line l's leader printed.
func (d *Dispatcher) leadSaid(ctx context.Context, l *harnessLead, line leadLine) {
	switch {
	case line.Error != "":
		l.failed = line.Error
	case line.Done:
		l.done, l.proved = true, line.Proved
	case line.Need == "choose":
		d.answerLeader(l, map[string]any{"choose": l.method})
	case line.Need == "open_url":
		if strings.HasPrefix(line.URL, "https://") && !slices.Contains(l.urls, line.URL) {
			l.urls = append(l.urls, line.URL)
		}
	case line.Need == "input":
		d.askInput(ctx, l, line)
	case line.Need == "confirm":
		d.askConfirm(ctx, l, line)
	}
}

// signIn is the context lines that send the person to the pages l's
// leader printed, once each.
func (l *harnessLead) signIn() []string {
	var out []string
	for _, u := range l.urls {
		out = append(out, "Sign in on this page: "+u)
	}
	l.urls = nil
	return out
}

// askInput asks the operator for what the sign-in page gave them, as a
// secret ask naming the leader's wait: the value goes to the leader alone.
func (d *Dispatcher) askInput(ctx context.Context, l *harnessLead, line leadLine) {
	if line.Login == "" {
		// A leader with no yard to wait in takes its input on stdin, which
		// the yard never writes a value to: it ends on its own timeout.
		d.Log.Warn("the login leader waits for an input on stdin alone", "kind", l.kind)
		return
	}
	h, _ := agents.Lookup(l.kind)
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return
	}
	lines := append(l.signIn(),
		fmt.Sprintf("%s's login waits for what the page gives you (%s).", h.Label, strings.TrimSpace(line.Text)),
		"What you type goes to the login in the yard's memory and is kept nowhere.")
	seq, err := d.Store.AddAsk(ctx, l.seat.journal, store.Ask{
		Question: fmt.Sprintf("Paste what %s's sign-in page gave you", h.Label),
		To:       settings[store.OperatorSetting],
		Context:  lines,
		Secret:   &store.AskSecret{Login: line.Login},
	}, actor)
	if err != nil {
		d.Log.Warn("ask for the login's input", "kind", l.kind, "err", err)
		_ = l.leader.Answers.Close()
		return
	}
	l.ask, l.step = seq, leadInput
	d.markLeadAsk(ctx, l.seat, l)
}

// askConfirm asks the operator whether the login is proved with one real
// call, which costs a model call on their account.
func (d *Dispatcher) askConfirm(ctx context.Context, l *harnessLead, line leadLine) {
	h, _ := agents.Lookup(l.kind)
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return
	}
	lines := append(l.signIn(),
		fmt.Sprintf("%s's login command ended well.", h.Label),
		strings.TrimSpace(line.Text))
	seq, err := d.Store.AddAsk(ctx, l.seat.journal, store.Ask{
		Question: fmt.Sprintf("%s is logged in: check that it answers?", h.Label),
		To:       settings[store.OperatorSetting],
		Context:  lines,
		Options: []store.AskOption{
			{Label: "Check it", Consequence: "makes one real call, which costs a model call; then the crew starts"},
			{Label: "Skip the check", Consequence: "the crew starts at once, and its first turn is the check"},
		},
	}, actor)
	if err != nil {
		d.Log.Warn("ask whether to check the login", "kind", l.kind, "err", err)
		d.answerLeader(l, map[string]any{"confirm": false})
		return
	}
	l.ask, l.step = seq, leadConfirm
	d.markLeadAsk(ctx, l.seat, l)
}

// askKey asks the operator for the API key of l's kind, as a secret ask:
// GiveSecret writes it into the operator's key file of the kind, and the
// lead ends on its answer with the sessions started again. No leader runs.
// Its context says the reach the person consents to: every session of the
// kind on the operator's login reads the key.
func (d *Dispatcher) askKey(ctx context.Context, l *harnessLead) error {
	h, _ := agents.Lookup(l.kind)
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return err
	}
	seq, err := d.Store.AddAsk(ctx, l.seat.journal, store.Ask{
		Question: fmt.Sprintf("Paste the API key %s runs with", h.Label),
		To:       settings[store.OperatorSetting],
		Context: []string{
			fmt.Sprintf("It is written into one file the yard keeps, and nowhere else: every %s session that burns your login reads it, the crew's, the groups' without login_for and your conversations.", h.Label),
			"A session on another person's login (login_for) never reads it.",
			fmt.Sprintf("The yard starts %s, and every other session at the login screen on your login, again on it at once.", l.seat.member),
		},
		Secret: &store.AskSecret{Harness: l.kind, Crew: l.seat.member, Env: h.LoginEnv},
	}, actor)
	if err != nil {
		return fmt.Errorf("ask for the API key: %w", err)
	}
	l.ask, l.step = seq, leadKey
	d.markLeadAsk(ctx, l.seat, l)
	return nil
}

// loggedIn ends l with its login made: every live session of its kind at
// the login screen, crew and bead sessions both, and the crew member its
// pick switched, starts again. A key's lead runs no leader, so there is
// nothing to stop; its key is read by the sessions on the operator's login
// alone (onLedLogin), so only those start again.
func (d *Dispatcher) loggedIn(ctx context.Context, l *harnessLead) {
	delete(d.leads, l.kind)
	if l.stop != nil {
		l.stop()
	}
	d.markLeadAsk(ctx, l.seat, nil)
	var restarted []string
	var seats []loginSeat
	settings, _ := d.Store.Settings(ctx)
	operator := settings[store.OperatorSetting]
	if rs, err := d.Store.Crew(ctx); err == nil {
		seats = seatsAtLogin(rs, settings[store.DefaultKindSetting])
		sort.SliceStable(seats, func(i, j int) bool { return seats[i].key() < seats[j].key() })
		for _, seat := range seats {
			if seat.kind != l.kind && !slices.Contains(l.switched, seat.member) {
				continue
			}
			// A conversation burns its user's login: the line session
			// burns the operator's.
			if seat.user != "" && !l.onLedLogin(seat.user, operator) {
				continue
			}
			if err := d.Store.RequestRestartFor(ctx, seat.member, seat.user, l.kind+" was logged in through the yard's asks", "", actor); err != nil {
				d.Log.Warn("restart a crew session after its login", "crew", seat.member, "err", err)
				continue
			}
			restarted = append(restarted, seat.key())
		}
	}
	restarted = append(restarted, d.restartBeadsAtLogin(ctx, l, operator)...)
	// A session that comes back at the screen all the same (its harness
	// reads its login from elsewhere) is offered one on its next mail.
	head, _ := d.Store.Head(ctx)
	d.loginRests[l.kind] = loginRest{head: head}
	// A switched session still carries its old kind until its requested
	// restart runs. Do not offer that old login from a pass's earlier read.
	for _, seat := range seats {
		if slices.Contains(l.switched, seat.member) && seat.kind != l.kind {
			d.loginRests[seat.kind] = loginRest{head: head}
		}
	}
	_ = d.Store.LogDispatch(ctx, l.seat.journal, "login_led", actor, map[string]any{
		"crew": l.seat.member, "kind": l.kind, "step": "logged_in", "proved": l.proved, "restarted": restarted,
		"reason": fmt.Sprintf("%s is logged in: the sessions at its login screen start again", l.kind)})
	d.Log.Info("a harness was logged in through the yard's asks", "kind", l.kind, "restarted", restarted)
}

// onLedLogin reports whether a session on login's login has l's login: any
// session for a login its own program made, which is the machine's, and
// for a key only one on the operator's login, whose key file the key went
// to (store.HarnessKeyFile).
func (l *harnessLead) onLedLogin(login, operator string) bool {
	return l.step != leadKey || (login != "" && login == operator)
}

// restartBeadsAtLogin starts again every live bead session of l's kind at
// its login screen on l's login: it is ended as aborted, which is no
// failure of the bead, the teardown stops its pane, and the bead is routed
// again on a later pass, so a new session starts on the login. It answers
// what it ended, as session:<id>.
func (d *Dispatcher) restartBeadsAtLogin(ctx context.Context, l *harnessLead, operator string) []string {
	ss, err := d.Store.Sessions(ctx, false)
	if err != nil {
		d.Log.Warn("read the sessions to start again after a login", "kind", l.kind, "err", err)
		return nil
	}
	var groups map[string]store.Group
	var out []string
	for _, s := range ss {
		if s.Handle[HarnessLoginKey] == nil || !l.onLedLogin(s.Login, operator) {
			continue
		}
		kind := s.Kind
		if kind == "" {
			// A session from before the claim recorded its kind: its
			// group's, as the runner started it.
			if groups == nil {
				groups = map[string]store.Group{}
				if gs, err := d.Store.Groups(ctx); err == nil {
					for _, g := range gs {
						groups[g.Name] = g
					}
				}
			}
			kind = agents.Kind(groups[s.Group].Config)
		}
		if kind != l.kind {
			continue
		}
		if err := d.Store.EndSession(ctx, s.ID, "aborted", l.kind+" was logged in through the yard's asks: the session starts again on it"); err != nil {
			d.Log.Warn("end a bead session after its login", "session", s.ID, "err", err)
			continue
		}
		d.forget(s.ID)
		out = append(out, "session:"+s.ID)
	}
	return out
}

// endLead ends l without a login: its open ask is taken back with why, and
// its kind is offered again on the next held nudge.
func (d *Dispatcher) endLead(ctx context.Context, l *harnessLead, why string) {
	delete(d.leads, l.kind)
	if l.stop != nil {
		l.stop()
	}
	if l.ask != 0 {
		if state, _ := d.askState(ctx, l.seat.journal, l.ask); state == store.AskOpen || state == store.AskAside {
			_ = d.Store.TakeBackAsk(ctx, l.seat.journal, l.ask, "the login ended: "+why, actor)
		}
	}
	d.markLeadAsk(ctx, l.seat, nil)
	head, _ := d.Store.Head(ctx)
	d.loginRests[l.kind] = loginRest{head: head}
	_ = d.Store.LogDispatch(ctx, l.seat.journal, "login_led", actor, map[string]any{
		"crew": l.seat.member, "kind": l.kind, "step": "failed",
		"reason": fmt.Sprintf("the login of %s ended without one: %s. The yard asks again when %s is next sent mail", l.kind, why, l.seat.member)})
	d.Log.Warn("a led login ended without one", "kind", l.kind, "why", why)
}
