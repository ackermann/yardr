package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// pairOpts are the flags of yardr peer pair.
type pairOpts struct {
	name, url string
	yes, end  bool
	expose    []string
	timeout   time.Duration
	// form is what the operator answers in the guided form (pairForm), nil
	// for the command: the form asks more, and says it in other words.
	form *pairChoice
}

// pairPoll is how often each side looks how the pairing stands: the host
// in its own server, the caller at the host. Tests shorten it.
var pairPoll = struct{ host, caller time.Duration }{250 * time.Millisecond, time.Second}

func (a *app) pairCmd() *cobra.Command {
	var opt pairOpts
	cmd := &cobra.Command{
		Use: "pair [<address> <code>]", Short: "Link this yard with another from a short code, with nothing to copy",
		Long: `Links two yards without an invite line carried by hand. The operator of the
yard that can be reached opens the pairing and reads its code to the other:

  yardr peer pair                        on the yard that can be reached: shows a code
  yardr peer pair <address> <code>       on the other: calls it with the code

With nothing after it, in a terminal, yardr peer pair is a form on either
yard: it asks whether to show a code or enter one, sees to it that a yard
that shows one can be called, and after the check asks what the link may
carry (ping, and mail between crew members), whom of this yard the other one
may write to (its crew members and users, the yardmaster ticked) and the
other yard's name. With an argument or a flag, without a terminal, with
--json and in an agent's session it asks only for the check, as below.

The yards then give each other their names and public keys, and each shows
its operator a check of six digits. Compare it with the other operator's,
over the way you passed the code: it is the same on both only when nothing
between the yards put a key of its own in. Each operator confirms it, each
yard saves the other as a peer, and a ping proves the link. Declined, or not
answered in time, on either side: nothing is saved on either.

The code is good for 10 minutes and for one pairing, and 5 wrong codes end
the pairing. It keeps a yard that was not given it from calling at all. The
check is what the link rests on: do not confirm one you have not compared.

The link carries ping both ways and nothing else, and shows the other yard
nobody of this one. In a terminal the form asks whom this yard lets it write
to; in a pane or script, pass --expose <crew member or user> (repeatable).
Mail between crew members is allowed separately with peer allow.

The yard that opens the pairing needs a listener (yardr settings set
hook_addr <host:port>) and says where the other reaches it with --url; the
default is the listener's own address. The other need not be reachable at
all: the link then pulls. One of the two has to be.

Without a terminal there is no one to ask: --yes confirms the check without
asking, and prints it. That is for scripts and tests, on a network you trust:
a check nobody compared has proved nothing. With --json the opening yard
prints two documents, the code at once and the result at the end.`,
		Example: `  yardr peer pair --url http://10.0.0.5:8790     # north, which can be reached
  yardr peer pair http://10.0.0.5:8790 K7QM-2XPD # laptop, with the code north shows
  yardr peer pair --end                          # end the pairing this yard has open`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return errors.New("give nothing, to open a pairing here, or the address and the code of the yard that opened one")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if err := a.operatorOnly(ctx, st, "pair this yard with another"); err != nil {
				return err
			}
			if len(opt.expose) > 0 {
				if err := validatePairExpose(ctx, st, opt.expose); err != nil {
					return err
				}
			}
			if opt.end {
				if len(args) != 0 {
					return errors.New("--end ends the pairing this yard opened: it takes no address")
				}
				p, err := st.GetPairing(ctx, "")
				if errors.Is(err, store.ErrNotFound) || (err == nil && p.State != store.PairOpened && p.State != store.PairCalled) {
					return errors.New("this yard has no pairing open")
				} else if err != nil {
					return err
				}
				if err := st.PairClose(ctx, p.ID, a.who()); err != nil {
					return err
				}
				return a.out(c.OutOrStdout(), map[string]string{"pairing": "ended"}, func(w io.Writer) { fmt.Println("the pairing is ended: its code is good for nothing") })
			}
			if len(args) == 0 && !pairFlagged(c) && !a.json && interactive() && !agentSession() {
				return a.pairForm(c.OutOrStdout(), ctx, cfg, st)
			}
			if !opt.yes && (a.json || !interactive()) {
				return errors.New("a pairing asks its operator to compare a check, and there is no terminal to ask in (or --json was given): --yes confirms the check without asking, and prints it")
			}
			// Ctrl+C ends the pairing rather than the process: the other
			// yard is told, and nothing is left open.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			if len(args) == 0 {
				return a.pairHost(c.OutOrStdout(), ctx, cfg, st, opt)
			}
			if opt.url != "" {
				return errors.New("--url is where the yard that opens the pairing is reached; the one that calls gives the address it calls")
			}
			return a.pairJoin(c.OutOrStdout(), ctx, cfg, st, args[0], args[1], opt)
		},
	}
	cmd.Flags().StringVar(&opt.name, "name", "", "the name to save the other yard under (default: the name it gives itself)")
	cmd.Flags().StringVar(&opt.url, "url", "", "where the other yard reaches this one's listener, such as http://10.0.0.5:8790 (default: the listener's address)")
	cmd.Flags().BoolVar(&opt.yes, "yes", false, "confirm the check without asking, and print it (scripts and tests)")
	cmd.Flags().StringSliceVar(&opt.expose, "expose", nil, "crew member or user of this yard the other may see and write to (repeatable)")
	cmd.Flags().BoolVar(&opt.end, "end", false, "end the pairing this yard has open")
	cmd.Flags().DurationVar(&opt.timeout, "timeout", 30*time.Second, "how long to wait for the pong that proves the link")
	return cmd
}

// paired is what a pairing ended with.
type paired struct {
	Role  string      `json:"role"`
	Peer  *store.Peer `json:"peer"`
	Check string      `json:"check"`
	// Ping is the ping that proves the link, and PingError why there was no
	// pong: the peer is saved all the same.
	Ping      *pinged `json:"ping,omitempty"`
	PingError string  `json:"ping_error,omitempty"`
}

// nothingSaved ends what is said of every pairing that ended without a link.
const nothingSaved = ": nothing was saved on either yard"

// errSaidNo is a pairing this yard's own operator said no to.
var errSaidNo = errors.New("declined" + nothingSaved)

// notCalled is a call with a code that got no pairing: the form asks for
// the address and the code again after one.
type notCalled struct{ error }

// pairFlagged reports whether any of yardr peer pair's own flags was given.
func pairFlagged(c *cobra.Command) bool {
	for _, f := range []string{"name", "url", "yes", "end", "timeout", "expose"} {
		if c.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// validatePairExpose refuses a misspelled or oversized grant before this
// yard opens or calls a pairing, so a bad flag cannot leave a saved link.
func validatePairExpose(ctx context.Context, st store.API, names []string) error {
	if len(names) > store.MaxPeerExpose {
		return fmt.Errorf("a link exposes at most %d names, and %d are given", store.MaxPeerExpose, len(names))
	}
	opts, _, err := exposables(ctx, st)
	if err != nil {
		return err
	}
	valid := make(map[string]bool, len(opts))
	for _, opt := range opts {
		valid[opt.Value] = true
	}
	for _, name := range names {
		if !valid[name] {
			return fmt.Errorf("--expose %s is not a crew member or user of this yard", name)
		}
	}
	return nil
}

// progress says how a pairing goes while it waits. With --json that is not
// part of what is printed, so it goes to stderr.
func (a *app) progress(format string, args ...any) {
	if a.json {
		fmt.Fprintf(stderr, format+"\n", args...)
		return
	}
	fmt.Printf(format+"\n", args...)
}

// confirmCheck shows the check and asks the operator whether the other
// yard's is the same. With --yes it is printed and not asked.
func (a *app) confirmCheck(opt pairOpts, peer, calls, check string) (bool, error) {
	if opt.form != nil {
		return askSame(calls, check)
	}
	a.progress("\n  check  %s\n", check)
	if opt.yes {
		a.progress("--yes: the check is confirmed without being compared with %s's", peer)
		return true, nil
	}
	return askCheck(peer, calls, check)
}

// askCheck asks the operator whether the yard saved as peer, which calls
// itself calls, shows the same check. Leaving the question is a no. Tests
// replace it.
var askCheck = func(peer, calls, check string) (bool, error) {
	yes := false
	err := ask(huh.NewConfirm().
		Title(fmt.Sprintf("Does the other operator see %s too?", check)).
		Description(fmt.Sprintf("Ask them, over the way you passed the code. The same check on both yards means the yard\nthat calls itself %s is theirs, and nothing between the two put a key of its own in.\nA different one, or one you cannot compare: say no.", calls)).
		Affirmative("Yes, the same").Negative("No").
		Value(&yes))
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	return yes, err
}

// listenerURL is the address of this yard's listener as a URL, for a yard
// whose operator gave no --url: good when the listener is on one address,
// and no answer when it is on every one.
func listenerURL(cfg *config.Config) (string, error) {
	l, _ := hooks.ReadListener(cfg)
	host, port, err := net.SplitHostPort(l.Addr)
	if err != nil {
		return "", fmt.Errorf("the listener's address %q is none to give: say where the other yard reaches this one with --url", l.Addr)
	}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		return "", fmt.Errorf("the listener is on every address of this machine (%s), which tells the other yard nothing: say where it reaches this one with --url http://<this machine>:%s", l.Addr, port)
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// listenerFix is how a yard that listens on its own machine only comes to
// listen where another yard reaches it, with yardr as it is typed there.
// What stands in front of a listener is the operator's own, so no tool for
// that is named.
func listenerFix(yardr string) string {
	return fmt.Sprintf("`%s doctor` says what the server listens on, and `%s settings set hook_addr <host:port>` then `%s server stop` changes it "+
		"(the form, bare `%s peer pair`, does both)", yardr, yardr, yardr, yardr)
}

// behindLoopback is what the yard that opens a pairing is told when it
// gives the other yard an address off this machine while its own server
// listens on this machine only: "" when that is not so. It is a warning and
// no refusal, since a proxy of the operator's own may pass the call on.
func behindLoopback(cfg *config.Config, addr string) string {
	l, known := hooks.ReadListener(cfg)
	u, err := url.Parse(addr)
	if !known || err != nil || !onLoopback(l.Addr) || u.Hostname() == "localhost" || isLoopback(u.Hostname()) {
		return ""
	}
	text := fmt.Sprintf("This yard's server listens on %s, which only this machine can reach, and the other yard is told to call %s. "+
		"Its call arrives only if something of your own (a tunnel or proxy) passes it on to %s; if nothing does, it reaches no yard. "+
		"Then end this (Ctrl+C) and let the server listen where the other yard reaches it: %s. After that, open the pairing again.",
		l.Addr, addr, l.Addr, listenerFix(yardrAt(cfg.Home)))
	if l.From == hooks.FromVariable {
		text += "\n" + variableWins(l) + " Change it there, or unset it."
	}
	return text
}

// pairHost opens a pairing on this yard, shows its code and waits: for the
// other yard to call, for the two operators, and for the ping.
func (a *app) pairHost(w io.Writer, ctx context.Context, cfg *config.Config, st store.API, opt pairOpts) error {
	yardr := yardrAt(cfg.Home)
	if listening, text := receiver(ctx, cfg, st); !listening {
		return fmt.Errorf("this yard has no listener, so the other yard cannot call it: %s\n"+
			"One of the two yards has to be reachable by the other (on an address the other can reach, directly or through a tunnel or proxy of your own). "+
			"If the other yard is, open the pairing there and call it from here: %s peer pair <address> <code> (docs/peers.md)", text, yardr)
	}
	addr := opt.url
	if addr == "" {
		var err error
		if addr, err = listenerURL(cfg); err != nil {
			return err
		}
	}
	inv, err := invite(ctx, cfg, st, addr)
	if err != nil {
		return err
	}
	who := a.who()
	opened, err := st.PairOpen(ctx, store.PairOffer{Name: inv.Name, Key: inv.Key, URL: inv.URL, As: opt.name}, who)
	if err != nil {
		return err
	}
	id := opened.ID
	// However this returns, the pairing is not left open. One that is done
	// or has ended is not changed by it.
	defer func() { _ = st.PairClose(context.WithoutCancel(ctx), id, who) }()

	command := fmt.Sprintf("yardr peer pair %s %s", inv.URL, opened.Code)
	// The form gives the listener's own address, so only the command can
	// name one the listener is not on.
	var warn string
	if opt.form == nil {
		warn = behindLoopback(cfg, inv.URL)
	}
	if opt.form != nil {
		showCode(inv.URL, opened.Code, opened.Expires)
	} else if a.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"pairing": id, "code": opened.Code, "address": inv.URL, "expires": opened.Expires, "command": command}); err != nil {
			return err
		}
		if warn != "" {
			a.progress("%s", warn)
		}
	} else {
		fmt.Printf("Pairing is open for %s, until %s, for one yard. Read the code to the other operator, who runs:\n\n  %s\n\n",
			short(pairing.TTL), opened.Expires.Local().Format("15:04:05"), command)
		if warn != "" {
			fmt.Printf("%s\n\n", warn)
		}
		fmt.Println("Waiting for the other yard to call. Ctrl+C ends the pairing.")
	}

	p, err := a.pairHosted(ctx, st, opened.Pairing, opt)
	if err != nil {
		return err
	}
	peer, err := st.GetPeer(ctx, p.Peer)
	if err != nil {
		return err
	}
	return a.pairDone(w, ctx, cfg, st, paired{Role: "host", Peer: peer, Check: p.Check}, opt)
}

// pairHosted follows the host's pairing until it is done, asking the
// operator once the other yard has called.
func (a *app) pairHosted(ctx context.Context, st store.API, p store.Pairing, opt pairOpts) (*store.Pairing, error) {
	tries, asked := 0, false
	for {
		now, err := st.GetPairing(ctx, p.ID)
		if ctx.Err() != nil {
			return nil, errors.New("the pairing was ended" + nothingSaved)
		} else if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("the pairing is gone: the yard's server stopped or was replaced while it was open, and a pairing lives in its memory. Open another" + nothingSaved)
		} else if err != nil {
			return nil, err
		}
		if now.Tries > tries {
			tries = now.Tries
			a.progress("a wrong code was tried (%d of %d)", tries, pairing.MaxTries)
		}
		if now.State == store.PairCalled && !asked {
			asked = true
			if opt.form == nil {
				a.progress("%s called (it calls itself %s). Its key: %s", now.Peer, now.Calls, now.Key)
			}
			yes, err := a.confirmCheck(opt, now.Peer, now.Calls, now.Check)
			if err != nil {
				return nil, err
			}
			if yes && opt.form != nil {
				// The form's other questions come before this yard's yes, so
				// that nothing is saved on either yard until they are answered.
				if yes, err = a.hostChoice(ctx, st, now, opt.form); err != nil {
					return nil, err
				}
			}
			if now, err = st.PairAnswer(ctx, p.ID, yes, a.who()); err != nil {
				return nil, err
			}
			if now.State == store.PairCalled && opt.form != nil {
				fmt.Printf("\nWaiting for the other operator to answer theirs, until %s.\n", now.Expires.Local().Format("15:04:05"))
			} else if now.State == store.PairCalled {
				a.progress("waiting for %s's operator to confirm, until %s", now.Peer, now.Expires.Local().Format("15:04:05"))
			}
		}
		switch now.State {
		case store.PairDone:
			return now, nil
		case store.PairEnded:
			return nil, hostEnded(*now)
		}
		select {
		case <-ctx.Done():
		case <-time.After(pairPoll.host):
		}
	}
}

// hostEnded says why the host's pairing ended without a link.
func hostEnded(p store.Pairing) error {
	text := "the pairing ended (" + p.Reason + ")"
	switch p.Reason {
	case pairing.WrongCode:
		text = fmt.Sprintf("the pairing ended after %d wrong codes. Open another, and read out its code", pairing.MaxTries)
	case pairing.Declined:
		return errSaidNo
	case pairing.PeerDeclined:
		text = "the other yard's operator declined, or that yard could not add this one"
	case pairing.TimedOut:
		text = fmt.Sprintf("no yard called with the code within %s", short(pairing.TTL))
		if p.Calls != "" {
			text = fmt.Sprintf("the two operators did not both confirm within %s of the call", short(pairing.ConfirmTTL))
		}
	case pairing.Closed:
		text = "the pairing was ended"
	case pairing.AlreadyPeer, pairing.BadName, pairing.Own:
		text = p.Detail
	}
	return errors.New(text + nothingSaved)
}

// pairAddress is the address the calling operator typed, as a URL:
// host:port is taken as http.
func pairAddress(s string) (string, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := yardkey.CleanURL(s)
	if err != nil {
		return "", fmt.Errorf("%w: the address yardr peer pair prints on the other yard", err)
	}
	return u, nil
}

// pairHTTP is what a calling yard reaches the other with. The other yard
// stretches the code before it answers, so a call takes a moment.
var pairHTTP = &http.Client{Timeout: 30 * time.Second}

// pairJoin calls the yard at address with the code its operator read out.
func (a *app) pairJoin(w io.Writer, ctx context.Context, cfg *config.Config, st store.API, address, code string, opt pairOpts) error {
	addr, err := pairAddress(address)
	if err != nil {
		return err
	}
	clean, err := pairing.CleanCode(code)
	if err != nil {
		return err
	}
	inv, err := invite(ctx, cfg, st, "")
	if err != nil {
		return err
	}
	key, err := yardkey.Load(cfg.PeerKey())
	if err != nil {
		return err
	}
	who := a.who()
	if opt.name != "" {
		// Said now, not after the other operator has been kept waiting. The
		// key is this yard's own, which is no peer's: only the name is asked.
		if err := st.CheckPeer(ctx, store.Peer{Name: opt.name, Key: inv.Key}); err != nil {
			return err
		}
	}
	caller := &yards.Caller{Key: key, HTTP: pairHTTP}
	// Whether the yard that answers can be a peer here is asked before this
	// yard has revealed itself: the other operator is then asked nothing,
	// and the code is still good for a call with --name.
	var peer store.Peer
	errNotAdded := errors.New("not added")
	called, err := caller.PairCall(ctx, addr, clean, inv.Name, func(host pairing.Party) error {
		peer = store.Peer{Name: or(opt.name, host.Name), Key: host.Key, URL: addr, Calls: host.Name}
		if host.Key == inv.Key {
			_ = st.PairRefused(ctx, pairing.Own, host.Name, who)
			return fmt.Errorf("%w: the yard that answered has this yard's own key, and a yard is not paired with itself", errNotAdded)
		}
		if err := st.CheckPeer(ctx, peer); err != nil {
			_ = st.PairRefused(ctx, pairing.AlreadyPeer, host.Name, who)
			if opt.form != nil {
				return fmt.Errorf("%w: %w. `%s peer list` shows this yard's peers; the code is still good for `%s peer pair <address> <code> --name <another name>`",
					errNotAdded, err, yardrAt(cfg.Home), yardrAt(cfg.Home))
			}
			if opt.name == "" {
				return fmt.Errorf("%w: %w (--name saves the other yard under a name of your choosing; the code is still good)", errNotAdded, err)
			}
			return fmt.Errorf("%w: %w", errNotAdded, err)
		}
		return nil
	})
	if errors.Is(err, errNotAdded) {
		return errors.New(strings.TrimPrefix(err.Error(), errNotAdded.Error()+": ") + nothingSaved)
	} else if err != nil {
		return notCalled{a.callFailed(ctx, cfg, st, addr, err)}
	}
	host := called.Host
	// refuse ends the pairing from this side: the other yard is told, so
	// its operator is not left to wait, and this yard logs why.
	refuse := func(reason string) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = caller.PairAnswer(ctx, called, false)
		_ = st.PairRefused(ctx, reason, host.Name, who)
	}

	if opt.form == nil {
		a.progress("%s answered at %s (it calls itself %s). Its key: %s", peer.Name, addr, host.Name, host.Key)
		if host.URL != addr {
			a.progress("it gives its address as %s; this yard keeps the one it answered at", host.URL)
		}
	}
	yes, err := a.confirmCheck(opt, peer.Name, host.Name, called.Check())
	if err == nil && yes && opt.form != nil {
		// As on the yard that is called: every question before this yard's yes.
		var name string
		who, ticked, lerr := exposables(ctx, st)
		if err = lerr; err == nil {
			name, err = opt.form.ask(host.Name, who, ticked, func(name string) error {
				return st.CheckPeer(ctx, store.Peer{Name: name, Key: host.Key})
			})
		}
		if yes = !errors.Is(err, huh.ErrUserAborted); !yes {
			err = nil
		}
		peer.Name = or(name, peer.Name)
	}
	if err != nil || !yes || ctx.Err() != nil {
		refuse(pairing.Declined)
		if err != nil {
			return err
		}
		return errSaidNo
	}
	if opt.form != nil {
		fmt.Println("\nWaiting for the other operator to answer theirs.")
	} else {
		a.progress("waiting for %s's operator to confirm", peer.Name)
	}
	wait, cancel := context.WithTimeout(ctx, pairing.ConfirmTTL)
	defer cancel()
	out, err := caller.PairWait(wait, called, pairPoll.caller)
	switch {
	case ctx.Err() != nil:
		refuse(pairing.Closed)
		return errors.New("the pairing was ended" + nothingSaved)
	case wait.Err() != nil:
		refuse(pairing.TimedOut)
		return fmt.Errorf("%s's operator did not confirm within %s%s", peer.Name, short(pairing.ConfirmTTL), nothingSaved)
	case err != nil:
		// The other yard may have saved this one: its answer did not arrive.
		return fmt.Errorf("asking %s how the pairing stands: %w\nThis yard saved nothing. If %s shows this yard as a peer, remove it there (yardr peer rm) and pair again", peer.Name, err, peer.Name)
	case out.State != pairing.Confirmed:
		return a.hostSaidNo(ctx, st, peer.Name, host.Name, out.Reason)
	}
	saved, err := st.PairPeer(ctx, peer, who)
	if err != nil {
		return fmt.Errorf("%s has saved this yard, and this yard could not save it: %w\nRemove this yard there (yardr peer rm) and pair again", peer.Name, err)
	}
	return a.pairDone(w, ctx, cfg, st, paired{Role: "joiner", Peer: saved, Check: called.Check()}, opt)
}

// hostSaidNo says why the yard that was called ended the pairing, and logs
// it here. The reason is that yard's: its "declined" is its own operator's.
func (a *app) hostSaidNo(ctx context.Context, st store.API, peer, calls, reason string) error {
	mine, text := pairing.PeerDeclined, fmt.Sprintf("%s ended the pairing (%s)", peer, clipText(reason))
	switch reason {
	case pairing.Declined:
		text = fmt.Sprintf("%s's operator declined", peer)
	case pairing.PeerDeclined:
		mine, text = pairing.Declined, "declined"
	case pairing.TimedOut:
		mine, text = pairing.TimedOut, fmt.Sprintf("the two operators did not both confirm within %s of the call", short(pairing.ConfirmTTL))
	case pairing.Closed:
		mine, text = pairing.Closed, fmt.Sprintf("%s's operator ended the pairing", peer)
	case pairing.AlreadyPeer:
		mine, text = pairing.AlreadyPeer, fmt.Sprintf("%s could not save this yard: its name or its key became a peer's there meanwhile", peer)
	}
	_ = st.PairRefused(context.WithoutCancel(ctx), mine, calls, a.who())
	return errors.New(text + nothingSaved)
}

// callFailed says why a call with a code got no pairing: in this yard's
// words, for the reasons it knows, since a reason is the other yard's text.
func (a *app) callFailed(ctx context.Context, cfg *config.Config, st store.API, addr string, err error) error {
	yardr := yardrAt(cfg.Home)
	var no *pairing.Refused
	var unreached *url.Error
	switch {
	case errors.As(err, &no):
		text := "the yard at " + addr + " refused the call (" + clipText(no.Reason) + ")"
		switch no.Reason {
		case pairing.WrongCode:
			_ = st.PairRefused(context.WithoutCancel(ctx), pairing.WrongCode, "", a.who())
			text = fmt.Sprintf("that is not the code the yard at %s shows. Its pairing ends after %d wrong codes", addr, pairing.MaxTries)
		case pairing.NoPairing:
			text = fmt.Sprintf("the yard at %s has no pairing open: its operator runs yardr peer pair there, and reads you the code", addr)
		case pairing.TimedOut:
			text = fmt.Sprintf("that code has run out: a code is good for %s. The other operator opens another pairing", short(pairing.TTL))
		case pairing.Used:
			text = "that code was used by another call, and a code is good for one. If that call was not yours, the other operator should say no to the check they are shown, and open another pairing"
		case pairing.Superseded:
			text = "another call with the code took this one's place: run the command again"
		case pairing.AlreadyPeer:
			text = fmt.Sprintf("the yard at %s cannot add this one: this yard's name or key is a peer's there already. Its operator sees which, and can remove that peer or open the pairing again with --name", addr)
		case pairing.BadName:
			text = fmt.Sprintf("the yard at %s cannot save this yard under the name it gives itself: its operator opens the pairing again with --name, or this yard takes another (%s settings set name <name>)", addr, yardr)
		case pairing.Own:
			text = "the yard at " + addr + " is this yard itself: a yard is not paired with itself"
		}
		return errors.New(text + nothingSaved)
	case errors.As(err, &unreached):
		// A call that is refused or gets no answer is most often one to a
		// yard that listens on its own machine only: the fix is that yard's
		// operator's, so the commands are as they are typed there.
		var there string
		if unanswered(unreached) {
			there = fmt.Sprintf("\nIf a yard runs there, its listener may be on that machine only (127.0.0.1), where no other machine reaches it. On that yard: %s. "+
				"Where %s is set in the environment its server starts in, the variable is used instead of the setting: it is changed or unset there.",
				listenerFix("yardr"), hooks.FromVariable)
		}
		return fmt.Errorf("no yard was reached at %s: %w%s\n"+
			"One of the two yards has to be reachable by the other (on an address the other can reach, directly or through a tunnel or proxy of your own). "+
			"If this yard is the one that can be reached, open the pairing here (%s peer pair) and call it from there: docs/peers.md", addr, unreached.Err, there, yardr)
	}
	return fmt.Errorf("pairing with the yard at %s: %w%s", addr, err, nothingSaved)
}

// unanswered reports whether a call found nothing that took it: the
// connection was refused, or no answer came in time. A name that does not
// resolve, or a TLS failure, is another matter.
func unanswered(err error) bool {
	var timeout net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &timeout) && timeout.Timeout())
}

// clipText keeps another yard's text short and plain where it is shown.
func clipText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r == '_' {
			return r
		}
		return -1
	}, s)
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// pairDone says what a pairing ended with, and proves the link with a
// ping. The peer is saved whatever the ping does: no pong is said, with
// what is known of why, and is no failure of the pairing.
func (a *app) pairDone(w io.Writer, ctx context.Context, cfg *config.Config, st store.API, res paired, opt pairOpts) error {
	if opt.form != nil {
		return a.formDone(ctx, cfg, st, res.Peer, opt)
	}
	yardr, p := yardrAt(cfg.Home), res.Peer
	if len(opt.expose) > 0 {
		if _, err := st.AllowPeer(ctx, p.Name, store.PeerAllow{Expose: opt.expose}, a.who()); err != nil {
			return fmt.Errorf("peer %s is saved, and its exposure could not be allowed: %w", p.Name, err)
		}
	}
	a.progress("peer %s saved (it calls itself %s): the link carries ping both ways and nothing else", p.Name, p.Calls)
	if p.URL == "" {
		a.progress("it gave no URL, so this yard never calls it: it calls this one and asks for what waits here")
	}
	if p.URL != "" && !config.DaemonRunning(cfg.DaemonLock()) {
		// Known now: not worth the wait for a pong that cannot come.
		res.PingError = fmt.Sprintf("no daemon runs on this yard, and the daemon is what carries messages: %s daemon, then %s peer ping %s", yardr, yardr, p.Name)
	} else if got, err := a.ping(ctx, cfg, st, p.Name, opt.timeout); err != nil {
		res.PingError = err.Error()
	} else {
		res.Ping = got
	}
	return a.out(w, res, func(w io.Writer) {
		if res.Ping != nil {
			fmt.Printf("pong from %s in %s: the ping went by %s, the pong came by %s\n", p.Name, res.Ping.Took.Round(time.Millisecond), res.Ping.Out, res.Ping.Back)
		} else {
			fmt.Fprintf(stderr, "the link is saved, and not proved yet: %s\n", res.PingError)
		}
		if len(opt.expose) > 0 {
			fmt.Printf("%s may write to: %s.\n", p.Name, strings.Join(opt.expose, ", "))
		} else {
			fmt.Printf("%s may write to nobody here: %s peer allow %s --expose <crew member or user>,...\n", p.Name, yardr, p.Name)
		}
		fmt.Printf("mail between crew members is not allowed yet: %s peer allow %s --send mail --receive mail\n", yardr, p.Name)
	})
}

// short is a duration as it is said: "10 minutes".
func short(d time.Duration) string {
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}
