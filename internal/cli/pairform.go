package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// The pairing form is yardr peer pair for a person who has never seen the
// peer commands: bare, in a terminal, it asks what the command takes as
// arguments and flags, and then runs the same pairing (pairHost, pairJoin)
// with its own words in place of the command's. Every question but the last
// comes before this yard's yes to the check, so nothing is saved on either
// yard until the form's operator has answered them. The last, whom of the
// other yard to trust as a person of this one (askTrust), is about names
// this yard learns only over the saved link.

// pairChoice is what the operator answers in the form beyond the check.
type pairChoice struct {
	// mail is whether the link carries mail between crew members, both ways.
	mail bool
	// file is whether the mail may carry images, both ways (store.PeerFile).
	file bool
	// expose are the crew members and users of this yard the other one may
	// see and write to (store.Peer.Expose).
	expose []string
}

// The two ways through the form.
const (
	pairShow  = "show"
	pairEnter = "enter"
)

// What a yard that cannot be called is offered.
const (
	listenSet   = "set"
	listenOther = "other"
	listenStop  = "stop"
)

// pairFormPing is how long the form waits for the pong that proves the
// link: the other operator may still be at their last question.
var pairFormPing = time.Minute

// listenWait is how long a restarted server gets to say what it listens on.
var listenWait = 10 * time.Second

// restartServer stops the yard's server and starts it again, which is when
// it takes a new hook_addr. Tests replace it.
var restartServer = func(ctx context.Context, cfg *config.Config) error {
	if err := server.Stop(cfg); err != nil {
		return err
	}
	_, err := connect(ctx, cfg)
	return err
}

// pairForm links this yard with another as a form. An operator who leaves
// it, or says no to the check, has saved nothing, and is told so: that is
// no failure of the command.
func (a *app) pairForm(ctx context.Context, cfg *config.Config, st store.API) error {
	// Ctrl+C while the form waits for the other yard ends the pairing, as
	// it does for the command.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	yardr := yardrAt(cfg.Home)
	fmt.Print("\nLinking this yard with another\n\n" +
		"Two linked yards send each other messages: a ping, which shows the other is there, and mail\n" +
		"between their crew (the agents that live in a yard, such as the yardmaster). They share nothing\n" +
		"else, and each operator decides what their own yard takes.\n\n" +
		"You and the other yard's operator each run this form. One of you shows a code and reads it to\n" +
		"the other, who enters it.\n\n")
	way := pairShow
	err := ask(huh.NewSelect[string]().Key("pair_way").Title("Show a code, or enter one?").
		Description("The yard that shows the code is the one that is called, so the other yard has to be able to reach\n"+
			"it. The yard that enters the code only calls: it needs nothing set up.").
		Options(
			huh.NewOption("Show a code: the other yard calls this one", pairShow),
			huh.NewOption("Enter a code: the other yard shows it, and this one calls", pairEnter),
		).Value(&way))
	for err == nil && way != "" {
		opt := pairOpts{timeout: pairFormPing, form: &pairChoice{}}
		if way == pairShow {
			way, err = a.formShow(ctx, cfg, st, opt)
		} else {
			way, err = "", a.formEnter(ctx, cfg, st, opt)
		}
	}
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		fmt.Printf("\nLeft before the yards were linked: nothing was saved. `%s peer pair` starts again.\n", yardr)
		return nil
	case errors.Is(err, errSaidNo):
		fmt.Printf("\nYou said no, so nothing was saved, on this yard or on the other. `%s peer pair` starts again.\n", yardr)
		return nil
	}
	return err
}

// formShow is the way of the yard that shows the code: it is called, so it
// is first made sure that it can be. It returns the way to go on with when
// the operator chose the other one.
func (a *app) formShow(ctx context.Context, cfg *config.Config, st store.API, opt pairOpts) (string, error) {
	for {
		can, why := callable(ctx, cfg, st)
		if can {
			break
		}
		next, err := a.askListener(ctx, cfg, st, why)
		if err != nil || next != pairShow {
			return next, err
		}
	}
	var err error
	if opt.url, err = formURL(cfg); err != nil {
		return "", err
	}
	return "", a.pairHost(ctx, cfg, st, opt)
}

// callable reports whether another yard can call this one: the server says
// it listens, and something answers on the address. Else it says why not.
func callable(ctx context.Context, cfg *config.Config, st store.API) (bool, string) {
	listening, text := receiver(ctx, cfg, st)
	if !listening {
		return false, text
	}
	l, _ := hooks.ReadListener(cfg)
	if !answers(dialable(l.Addr)) {
		return false, fmt.Sprintf("the server says it takes calls on %s, and nothing answers there", l.Addr)
	}
	return true, ""
}

// dialable is a listener's address as this machine calls it: one on every
// address is tried on the machine's own.
func dialable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// askListener says that this yard cannot be called and what that takes, and
// asks how to go on: set the address and restart the server, go the other
// way round, or stop. It returns the way to go on with, "" for none.
func (a *app) askListener(ctx context.Context, cfg *config.Config, st store.API, why string) (string, error) {
	yardr := yardrAt(cfg.Home)
	fmt.Printf("\nThis yard cannot be called yet. To show a code it has to take the other yard's call, on an\n"+
		"address of this machine that the other yard can reach. As it is now:\n\n  %s\n\n", why)
	l, known := hooks.ReadListener(cfg)
	var opts []huh.Option[string]
	pick := listenOther
	if !known || l.From != hooks.FromVariable {
		// An address from the variable is not the setting's to change.
		opts = append(opts, huh.NewOption("Set the address now: yardr saves it and restarts its server", listenSet))
		pick = listenSet
	}
	opts = append(opts,
		huh.NewOption("Go the other way round: the other yard shows the code, and you enter it here", listenOther),
		huh.NewOption("Stop here", listenStop))
	if err := ask(huh.NewSelect[string]().Key("pair_listen").Title("How to go on?").
		Description("Only one of the two yards has to be reachable. If the other one is, go the other way round:\n" +
			"this yard then needs no address at all.").
		Options(opts...).Value(&pick)); err != nil {
		return "", err
	}
	switch pick {
	case listenOther:
		return pairEnter, nil
	case listenStop:
		fmt.Printf("\nStopped: nothing was changed. `%s peer pair` starts again.\n", yardr)
		return "", nil
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return "", err
	}
	addr := all[hooks.FromSetting]
	if err := ask(huh.NewInput().Key("pair_listen_addr").Title("The address this yard takes calls on").
		Description("This machine's name or number on a network the other yard is on, a colon, and a free port:\n" +
			"10.0.0.5:8790. For a yard on this same machine, 127.0.0.1 and a port. The address is saved as\n" +
			"the yard's hook_addr setting; webhooks come in on it too.").
		Placeholder("10.0.0.5:8790").Value(&addr).Validate(func(s string) error {
		return hooks.CheckAddr(strings.TrimSpace(s))
	})); err != nil {
		return "", err
	}
	addr = strings.TrimSpace(addr)
	restart := true
	if err := ask(huh.NewConfirm().Key("pair_restart").Title(fmt.Sprintf("Save %s and restart the yard's server?", addr)).
		Description("The server takes a new address only when it starts. Restarting it takes a moment; the daemon\n" +
			"and the agents go on, and wait while it is away.").
		Value(&restart)); err != nil {
		return "", err
	}
	if !restart {
		fmt.Printf("\nNothing was changed. `%s peer pair` starts again.\n", yardr)
		return "", nil
	}
	if err := st.SetSetting(ctx, hooks.FromSetting, addr, a.who()); err != nil {
		return "", err
	}
	fmt.Printf("\nThe address is saved (hook_addr = %s). Restarting the server.\n", addr)
	if err := restartServer(ctx, cfg); err != nil {
		return "", fmt.Errorf("restarting the server: %w", err)
	}
	// The server says what it listens on as it starts; the check that
	// follows reads that.
	for deadline := time.Now().Add(listenWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if can, _ := callable(ctx, cfg, st); can {
			fmt.Println("The server is back, and takes calls there.")
			break
		}
	}
	return pairShow, nil
}

// formURL is where the other yard reaches this one: the listener's own
// address, or, for a listener on every address of the machine, the one the
// operator names.
func formURL(cfg *config.Config) (string, error) {
	l, _ := hooks.ReadListener(cfg)
	if u, err := listenerURL(cfg); err == nil {
		if onLoopback(l.Addr) {
			fmt.Printf("\nThis yard takes calls on %s, which only a yard on this same machine can reach.\n", l.Addr)
		}
		return u, nil
	}
	_, port, _ := net.SplitHostPort(l.Addr)
	u := ""
	err := ask(huh.NewInput().Key("pair_url").Title("Where does the other yard reach this one?").
		Description(fmt.Sprintf("This yard takes calls on every address of this machine, port %s. Give the one the other yard\n"+
			"can reach: this machine's name or number on their network, and the port.", port)).
		Placeholder("http://10.0.0.5:" + port).Value(&u).Validate(func(s string) error {
		_, err := pairAddress(strings.TrimSpace(s))
		return err
	}))
	if err != nil {
		return "", err
	}
	return pairAddress(strings.TrimSpace(u))
}

// onLoopback reports whether a listener's address is on this machine only,
// where no yard on another machine reaches it.
func onLoopback(addr string) bool {
	host, _, _ := net.SplitHostPort(addr)
	return host == "localhost" || isLoopback(host)
}

func isLoopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// showCode shows the open pairing's code and the address to give with it.
func showCode(addr, code string, expires time.Time) {
	fmt.Printf("\n  The code      %s\n  The address   %s\n\n"+
		"Read both to the other yard's operator. There, `yardr peer pair` asks for them under\n"+
		"\"Enter a code\". The code is good for %s, until %s, and for one yard.\n\n"+
		"Waiting for the other yard to call. Ctrl+C ends this, and the code with it.\n",
		code, addr, short(pairing.TTL), expires.Local().Format("15:04:05"))
}

// formEnter is the way of the yard that calls: it asks for the address and
// the code the other screen shows, and asks again after a call that got no
// pairing (a wrong code, no yard at the address).
func (a *app) formEnter(ctx context.Context, cfg *config.Config, st store.API, opt pairOpts) error {
	fmt.Print("\nOn the other yard, its operator runs `yardr peer pair` and picks \"Show a code\". Their screen\n" +
		"then shows a code and an address.\n\n")
	var addr, code string
	for {
		if err := ask(
			huh.NewInput().Key("pair_address").Title("The other yard's address").
				Description("As the other screen shows it, such as http://10.0.0.5:8790.").
				Value(&addr).Validate(func(s string) error {
				_, err := pairAddress(strings.TrimSpace(s))
				return err
			}),
			huh.NewInput().Key("pair_code").Title("The code").
				Description("Eight letters and digits, as the other operator reads them out: K7QM-2XPD.\n"+
					"Capitals and the dash do not matter.").
				Value(&code).Validate(func(s string) error {
				_, err := pairing.CleanCode(s)
				return err
			}),
		); err != nil {
			return err
		}
		fmt.Println("\nCalling the other yard.")
		err := a.pairJoin(ctx, cfg, st, strings.TrimSpace(addr), code, opt)
		var no notCalled
		if !errors.As(err, &no) {
			return err
		}
		fmt.Printf("\nThe call did not go through: %v\n\n", err)
		again := true
		if err := ask(huh.NewConfirm().Key("pair_again").Title("Try again?").
			Description("With the address and the code as the other screen shows them now.").
			Value(&again)); err != nil {
			return err
		}
		if !again {
			return nil
		}
		code = ""
	}
}

// digits are the figures of the check, five rows high.
var digits = map[rune][5]string{
	'0': {"███", "█ █", "█ █", "█ █", "███"},
	'1': {"  █", "  █", "  █", "  █", "  █"},
	'2': {"███", "  █", "███", "█  ", "███"},
	'3': {"███", "  █", "███", "  █", "███"},
	'4': {"█ █", "█ █", "███", "  █", "  █"},
	'5': {"███", "█  ", "███", "  █", "███"},
	'6': {"███", "█  ", "███", "█ █", "███"},
	'7': {"███", "  █", "  █", "  █", "  █"},
	'8': {"███", "█ █", "███", "█ █", "███"},
	'9': {"███", "█ █", "███", "  █", "███"},
	' ': {"  ", "  ", "  ", "  ", "  "},
}

// large is a check in figures five rows high, to be read across a room or
// a video call. A character it has no figure for is left out: the check is
// printed small beside it.
func large(check string) string {
	var rows [5]strings.Builder
	for _, r := range check {
		d, ok := digits[r]
		if !ok {
			continue
		}
		for i := range rows {
			rows[i].WriteString(d[i] + "  ")
		}
	}
	var b strings.Builder
	for i := range rows {
		b.WriteString("    " + strings.TrimRight(rows[i].String(), " ") + "\n")
	}
	return b.String()
}

// askSame shows the check large, with what the other yard calls itself,
// and asks the one question both operators answer. Leaving it is a no.
func askSame(calls, check string) (bool, error) {
	fmt.Printf("\nThe two yards have reached each other. The other one calls itself %s.\n"+
		"Both screens now show a number. This one's:\n\n%s\n    %s\n\n", calls, large(check), check)
	same := false
	err := ask(huh.NewConfirm().Key("pair_same").Title("Does the other screen show the same?").
		Description(fmt.Sprintf("Ask the other operator to read theirs out. The same number on both means this yard has\n"+
			"reached theirs, the one that calls itself %s, and nobody sits in between. A different one,\n"+
			"or one you cannot compare: No, and nothing is saved.", calls)).
		Affirmative("Yes").Negative("No").Value(&same))
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	return same, err
}

// exposables are those of this yard a peer can be let write to, as the
// form lists them: its crew members, then its users. ticked is the
// yardmaster, when the yard has one: it is whom the other yard writes to
// first. It is listed first too: huh opens a list at its first ticked
// option, with those above it scrolled out of sight.
func exposables(ctx context.Context, st store.API) (opts []huh.Option[string], ticked []string, err error) {
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, nil, err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return nil, nil, err
	}
	yardmaster, operator := yardmasterOf(ctx, st), yardOperator(ctx, st)
	var names []string
	for _, r := range crew {
		names = append(names, r.Name)
		if r.Name == yardmaster {
			ticked = []string{r.Name}
			opts = slices.Insert(opts, 0, huh.NewOption(r.Name+"  (crew member, the yardmaster)", r.Name))
			continue
		}
		opts = append(opts, huh.NewOption(r.Name+"  (crew member)", r.Name))
	}
	for _, u := range users {
		if slices.Contains(names, u.Name) {
			continue
		}
		is := "user"
		if u.Name == operator {
			is = "user, the operator"
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s)", u.Name, is), u.Name))
	}
	return opts, ticked, nil
}

// ask asks what the link may carry, whom of this yard the other one may
// write to, and the name the other yard is saved under, which starts at the
// one it gives itself. who are those it may be let write to, with ticked
// of them ticked; a yard with nobody is not asked. free says whether a name
// can be a peer's here. ping is ticked and stays: the pong that proves the
// link is a ping too. file starts unticked, since it lets another yard's
// bytes land on this one's disk, and goes only with mail, which carries it.
func (f *pairChoice) ask(calls string, who []huh.Option[string], ticked []string, free func(name string) error) (string, error) {
	carry, name, expose := []string{store.PeerPing, store.PeerMail}, calls, slices.Clone(ticked)
	fields := []huh.Field{
		// The value before the options: they are ticked from it.
		huh.NewMultiSelect[string]().Key("pair_carry").Title("What may this link carry?").
			Description(fmt.Sprintf("Each in both directions: what this yard sends %s, and what it takes from it. The other\n"+
				"operator is asked the same, and mail or an image crosses only when both of you tick it.", calls)).
			Value(&carry).Options(
			huh.NewOption("ping: each yard may ask the other whether it is there, and is answered. Always on: it proves the link", store.PeerPing),
			huh.NewOption(fmt.Sprintf("mail between crew members: this yard's may write to %s's, and %s's to this yard's", calls, calls), store.PeerMail),
			huh.NewOption(fmt.Sprintf("file: images in mail cross the link, kept within %s", store.PeerFileMaxSetting), store.PeerFile),
		).Validate(func(picked []string) error {
			if !slices.Contains(picked, store.PeerPing) {
				return errors.New("ping stays ticked: without it the link cannot be proved")
			}
			if slices.Contains(picked, store.PeerFile) && !slices.Contains(picked, store.PeerMail) {
				return errors.New("file goes with mail, which carries the images: tick mail too, or untick file")
			}
			return nil
		}),
	}
	if len(who) > 0 {
		fields = append(fields, huh.NewMultiSelect[string]().Key("pair_expose").Title(fmt.Sprintf("Who may %s write to?", calls)).
			Description(fmt.Sprintf("Of this yard. %s is shown those you tick, by name and with one line of what they do, and its\n"+
				"mail reaches them and nobody else. With nobody ticked it sees no one here and can write to no\n"+
				"one. The other operator is asked the same of their yard. To change it later:\n"+
				"`yardr peer allow <name> --expose <who>`, and `--no-expose <who>`.", calls)).
			Value(&expose).Options(who...).Validate(func(picked []string) error {
			if len(picked) > store.MaxPeerExpose {
				return fmt.Errorf("a link exposes at most %d names, and %d are ticked", store.MaxPeerExpose, len(picked))
			}
			return nil
		}))
	}
	fields = append(fields, huh.NewInput().Key("pair_name").Title("This yard's name for the other one").
		Description(fmt.Sprintf("How you name it here, as in `yardr mail yardmaster@<name>`. It calls itself %s.", calls)).
		Value(&name).Validate(func(s string) error { return free(strings.TrimSpace(s)) }))
	if err := ask(fields...); err != nil {
		return "", err
	}
	f.mail = slices.Contains(carry, store.PeerMail)
	f.file = f.mail && slices.Contains(carry, store.PeerFile)
	f.expose = nil
	if len(who) > 0 {
		f.expose = slices.Sorted(slices.Values(expose))
	}
	if !f.mail && len(f.expose) > 0 {
		// The two grants are apart (exposeNote): said before anything is
		// saved, in the form's words.
		fmt.Printf("\nMail is not ticked, so %s will be listed to %s as taking no mail: it sees them here, and cannot\n"+
			"write to them.\n", strings.Join(f.expose, ", "), calls)
	}
	return strings.TrimSpace(name), nil
}

// trustables are the users of this yard that the form offers to trust a
// name of the peer as: those whose own name the peer exposes as a person,
// the one case the form can tell is meant (someone who works in both
// yards). known is whether the peer has said whom it exposes at all.
func trustables(ctx context.Context, st store.API, peer string) (opts []huh.Option[string], known bool, err error) {
	p, err := st.GetPeer(ctx, peer)
	if err != nil || p.Said == nil {
		return nil, false, err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, u := range users {
		if slices.ContainsFunc(p.Said.Roster, func(e store.PeerRosterEntry) bool { return e.Kind == store.RosterUser && e.Name == u.Name }) {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s@%s speaks as %s)", u.Name, u.Name, peer, u.Name), u.Name))
		}
	}
	return opts, true, nil
}

// askTrust asks whether a name of the other yard is trusted as a person of
// this one (store.Peer.Trust), and grants what is ticked, nothing being
// ticked before. It is the form's question after the link is saved, with
// the one about reach behind it for whoever was ticked (askReach): a
// name is trusted only when the other yard exposes it, and whom it exposes
// this yard knows once the two have talked (proved). Until then, and when
// nobody it exposes has the name of a user here, the form does not ask and
// says what does it later. Leaving the question trusts nobody: the link is
// saved either way.
func (a *app) askTrust(ctx context.Context, cfg *config.Config, st store.API, peer string, proved bool) error {
	var who []huh.Option[string]
	why := fmt.Sprintf("Whom %s exposes is not known yet", peer)
	if proved {
		opts, known, err := trustables(ctx, st, peer)
		if err != nil {
			return err
		}
		if who = opts; known {
			why = fmt.Sprintf("%s exposes nobody who has the name of a person of this yard", peer)
		}
	}
	later := fmt.Sprintf("`%s peer allow %s --trust <name>@%s=<user>`", yardrAt(cfg.Home), peer, peer)
	if len(who) == 0 {
		fmt.Printf("\nMail from a name on %s can count as a person of this yard speaking, for someone who works in both.\n"+
			"%s, so the form does not ask: %s does it later\n"+
			"(docs/peers.md, \"Trusting a name as a person of this yard\").\n", peer, why, later)
		return nil
	}
	var trust []string
	err := ask(huh.NewMultiSelect[string]().Key("pair_trust").Title(fmt.Sprintf("Trust a name on %s as a person of this yard?", peer)).
		Description(fmt.Sprintf("%s exposes these names, and this yard has people of the same. Tick one who is the same person\n"+
			"there, and their mail from %s counts as that person of this yard speaking: the crew here takes it\n"+
			"as their word, not as a stranger's. It gives the name nothing else. With nobody ticked, all mail\n"+
			"from %s is another yard's. To change it later:\n"+
			"`yardr peer allow %s --trust <name>@%s=<user>`, and `--no-trust <name>@%s`.", peer, peer, peer, peer, peer, peer)).
		Value(&trust).Options(who...))
	if errors.Is(err, huh.ErrUserAborted) {
		trust = nil
	} else if err != nil {
		return err
	}
	if len(trust) == 0 {
		fmt.Printf("\nNo name on %s is trusted as a person of this yard: %s does it later.\n", peer, later)
		return nil
	}
	grant := map[string]string{}
	for _, u := range trust {
		grant[u] = u
	}
	now, err := st.AllowPeer(ctx, peer, store.PeerAllow{Trust: grant}, a.who())
	if err != nil {
		return fmt.Errorf("the yards are linked, and the names you ticked could not be trusted on the link: %w", err)
	}
	fmt.Println()
	for _, t := range now.Trusted() {
		fmt.Printf("Mail from %s counts as %s of this yard speaking.\n", t.Address(), t.As)
	}
	return a.askReach(ctx, cfg, st, peer, trust)
}

// askReach asks, of the names just trusted, to which of them the asks for
// that person are also sent (store.Peer.Reach), and grants what is ticked,
// nothing being ticked before. It is a second grant and its own question:
// trust is about whose word comes in, and this sends every question put to
// the person here out to the other yard. Leaving it sends nothing.
func (a *app) askReach(ctx context.Context, cfg *config.Config, st store.API, peer string, trusted []string) error {
	var who []huh.Option[string]
	for _, u := range trusted {
		who = append(who, huh.NewOption(fmt.Sprintf("%s  (asks for %s are also sent to %s@%s)", u, u, u, peer), u))
	}
	later := fmt.Sprintf("`%s peer allow %s --reach <name>@%s=<user>`", yardrAt(cfg.Home), peer, peer)
	var reach []string
	err := ask(huh.NewMultiSelect[string]().Key("pair_reach").Title(fmt.Sprintf("Send their questions to %s as well?", peer)).
		Description(fmt.Sprintf("Tick a name, and every ask for that person in this yard is also sent to them on %s, where it\n"+
			"stands among their decisions, and the answer they give there is taken as theirs. What is sent is\n"+
			"the ask whole, with its bead's handle, title, topic, stage, labels and cut body: whoever runs\n"+
			"%s can read it, and can answer as them. With nobody ticked, nothing is sent. Nothing is\n"+
			"sent either until the link carries asks: `yardr peer allow %s --send ask --receive ask`.\n"+
			"To change it later: `yardr peer allow %s --reach <name>@%s=<user>`, and `--no-reach <name>@%s`.", peer, peer, peer, peer, peer, peer)).
		Value(&reach).Options(who...))
	if errors.Is(err, huh.ErrUserAborted) {
		reach = nil
	} else if err != nil {
		return err
	}
	if len(reach) == 0 {
		fmt.Printf("No ask for a person of this yard is sent to %s: %s does it later.\n", peer, later)
		return nil
	}
	grant := map[string]string{}
	for _, u := range reach {
		grant[u] = u
	}
	now, err := st.AllowPeer(ctx, peer, store.PeerAllow{Reach: grant}, a.who())
	if err != nil {
		return fmt.Errorf("the yards are linked and the names trusted, and the asks for those you ticked could not be sent on the link: %w", err)
	}
	for _, line := range reachLines(*now) {
		fmt.Println("Reach: " + line)
	}
	return nil
}

// hostChoice asks the form's questions on the yard that is called, once
// its operator has said yes to the check, and gives the pairing the name
// they chose. It reports false when the operator left the questions, which
// is a no to the pairing.
func (a *app) hostChoice(ctx context.Context, st store.API, p *store.Pairing, f *pairChoice) (bool, error) {
	who, ticked, err := exposables(ctx, st)
	if err != nil {
		return false, err
	}
	name, err := f.ask(p.Calls, who, ticked, func(name string) error {
		if name == p.Peer {
			return nil // the pairing holds it already
		}
		return st.CheckPeer(ctx, store.Peer{Name: name, Key: p.Key})
	})
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	} else if err != nil || name == p.Peer {
		return err == nil, err
	}
	// The other operator may have said no meanwhile, or the time run out:
	// then the answer that follows finds the pairing ended, and says why.
	if now, err := st.GetPairing(ctx, p.ID); err != nil || now.State != store.PairCalled {
		return true, nil
	}
	if _, err := st.PairName(ctx, p.ID, name, a.who()); err != nil {
		return false, err
	}
	return true, nil
}

// formDone is how the form ends once both yards have saved each other: it
// allows what the operator ticked, in one write, proves the link with a
// ping, asks whom of the other yard to trust as a person of this one
// (askTrust), and says how to use, see and remove it. No pong is said, with what is known of
// why, and is no failure: the link is saved.
func (a *app) formDone(ctx context.Context, cfg *config.Config, st store.API, p *store.Peer, opt pairOpts) error {
	yardr, carries := yardrAt(cfg.Home), "ping"
	ch := store.PeerAllow{Expose: opt.form.expose}
	if opt.form.mail {
		ch.Send, ch.Receive = []string{store.PeerMail}, []string{store.PeerMail}
		carries = "ping and mail"
	}
	if opt.form.file {
		ch.Send, ch.Receive = append(ch.Send, store.PeerFile), append(ch.Receive, store.PeerFile)
		carries = "ping, mail and file"
	}
	if len(ch.Send)+len(ch.Expose) > 0 {
		if _, err := st.AllowPeer(ctx, p.Name, ch, a.who()); err != nil {
			return fmt.Errorf("the yards are linked, and what you ticked could not be allowed on the link: %w", err)
		}
	}
	fmt.Printf("\nThe two yards are linked. This one knows the other as %s, and the link carries %s, each way.\n", p.Name, carries)
	switch who := strings.Join(opt.form.expose, ", "); {
	case who == "":
		fmt.Printf("%s sees nobody of this yard and can write to no one here: `%s peer allow %s --expose <crew member or user>`.\n", p.Name, yardr, p.Name)
	case opt.form.mail:
		fmt.Printf("%s may write to: %s.\n", p.Name, who)
	default:
		fmt.Printf("%s sees %s, and cannot write to them: the link carries no mail (`%s peer allow %s --send mail --receive mail`).\n", p.Name, who, yardr, p.Name)
	}
	var noPong string
	proved := false
	if p.URL != "" && !daemonRunning(cfg.DaemonLock()) {
		// Known now: not worth the wait for a pong that cannot come.
		noPong = fmt.Sprintf("no daemon runs on this yard, and the daemon is what carries messages: `%s init --start` starts it", yardr)
	} else {
		fmt.Printf("Asking %s whether it is there (up to %s: its operator may still be answering).\n", p.Name, strings.TrimSuffix(opt.timeout.String(), "0s"))
		if got, err := a.ping(ctx, cfg, st, p.Name, opt.timeout); err != nil {
			noPong = err.Error()
		} else {
			fmt.Printf("%s answered in %s: the link works.\n", p.Name, got.Took.Round(time.Millisecond))
			proved = true
		}
	}
	if noPong != "" {
		fmt.Printf("The link is saved, and not proved yet: %s\n`%s peer ping %s` asks again.\n", noPong, yardr, p.Name)
	}
	if opt.form.mail {
		// Trust is about mail that comes: a link without mail has none.
		if err := a.askTrust(ctx, cfg, st, p.Name, proved); err != nil {
			return err
		}
	}
	if opt.form.mail {
		fmt.Printf("\nTo try the mail, write to %s's yardmaster:\n\n  %s mail yardmaster@%s \"hello from the other yard\"\n\n"+
			"It arrives if the other operator ticked mail too, and their yardmaster as one this yard may write\n"+
			"to. A crew member of this yard may write there once you have allowed it that yard by name:\n"+
			"docs/peers.md, \"Mail between crew members\".\n", p.Name, yardr, p.Name)
	}
	if opt.form.file {
		fmt.Printf("\nAn image goes with the mail (`--file <png or jpeg>`), once the other operator ticked file too.\n")
	}
	fmt.Printf("\nThe link, and what waits on it:   %s peer show %s\nTo remove it:                     %s peer rm %s\n", yardr, p.Name, yardr, p.Name)
	return nil
}
