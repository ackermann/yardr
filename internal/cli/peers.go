package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

func (a *app) peerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "peer", Short: "Other yards this one is linked with, and the messages between them",
		Long: `Two yards are linked when the operator of each has added the other, by its
public key. They share no store: each sends the other signed messages, and
takes only the kinds its operator allowed, each direction on its own. A new
link carries nothing.

  yardr peer key                       this yard's public key (made on first use)
  yardr peer invite --url <url>        one line for the other operator to paste
  yardr peer add <name> <invite>       add the other yard, from its invite
  yardr peer allow <name> --send ping --receive ping
  yardr peer ping <name>               prove the link, both ways

The other yard's requests come in on the server's hook listener, under
/peers/, beside /hooks/ (yardr settings set hook_addr <host:port>): a yard
without a listener takes nothing in. A yard that cannot be reached at all (a
laptop behind a router) gives no --url in its invite: it is never called, it
calls the other and asks for what waits there. One of two linked yards must be
reachable. The daemon carries the messages (yardr daemon).

Every request and every answer is signed with the sender's key. That proves
who sent it and that it is whole. It does not hide it: over plain http the body
is readable by whoever carries it, so put TLS in front of the listener for
anything that is not on one machine or a private network. See docs/peers.md.`}

	key := &cobra.Command{
		Use: "key", Short: "Print this yard's public key, making the key pair on first use", Args: cobra.NoArgs,
		Long: `Prints the public key other yards know this one by. The key pair is made once:
the private key stays in the home (peer.key, mode 0600; a file open to others
is refused, as a hook's secret file is) and the same public key is printed
every time.`,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			k, made, err := yardkey.Ensure(cfg.PeerKey())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"key": yardkey.Public(k), "file": cfg.PeerKey(), "made": made}, func() {
				fmt.Println(yardkey.Public(k))
			})
		},
	}

	var url string
	invite := &cobra.Command{
		Use: "invite", Short: "Print the line another yard's operator adds this yard with", Args: cobra.NoArgs,
		Long: `Prints one line to give the other yard's operator, who runs yardr peer add
<name> <line> there: this yard's name, its public key and, with --url, where
it can be reached: the address of its hook listener as the other yard sees
it. Without --url the other yard never calls this one: this one calls it, so
it is the other that must be reachable.

The line holds nothing secret. Give it over a way the other operator can
trust to be yours: whoever swaps the key in it is who they link with.`,
		Example: `  yardr peer invite --url http://10.0.0.5:8790
  yardr peer invite                      # this yard cannot be reached: it calls`,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			inv, err := invite(ctx, cfg, st, url)
			if err != nil {
				return err
			}
			return a.out(map[string]any{"invite": inv.String(), "name": inv.Name, "key": inv.Key, "url": inv.URL}, func() {
				fmt.Println(inv.String())
				if listening, text := receiver(ctx, cfg, st); inv.URL != "" && !listening {
					fmt.Fprintln(stderr, "note: nothing reaches this yard at that URL yet: "+text)
				}
			})
		},
	}
	invite.Flags().StringVar(&url, "url", "", "where the other yard reaches this one's hook listener, such as http://10.0.0.5:8790")

	add := &cobra.Command{
		Use: "add <name> <invite>", Short: "Add another yard as a peer, from its invite", Args: cobra.ExactArgs(2),
		Long: `Records the yard whose operator gave you <invite> (yardr peer invite, there)
under <name>, which is yours to choose. Only the operator may: who is added
here is who this yard takes messages from.

The link works once each side has added the other, and carries nothing until
yardr peer allow says what. A name or key that is a peer already is refused:
remove the peer first to replace it.`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			inv, err := yardkey.ParseInvite(args[1])
			if err != nil {
				return err
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if err := a.operatorOnly(ctx, st, "add a peer"); err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if own, err := yardkey.Load(cfg.PeerKey()); err == nil && yardkey.Public(own) == inv.Key {
				return errors.New("that invite is this yard's own: give it to the other yard's operator, and add theirs here")
			}
			p, err := st.AddPeer(ctx, store.Peer{Name: args[0], Key: inv.Key, URL: inv.URL, Calls: inv.Name}, a.who())
			if err != nil {
				return err
			}
			return a.out(p, func() {
				yardr := yardrAt(cfg.Home)
				fmt.Printf("peer %s added (it calls itself %s)\n", p.Name, p.Calls)
				if p.URL == "" {
					fmt.Println("it gave no URL, so this yard never calls it: it calls this one and asks for what waits here")
				}
				fmt.Printf("the link carries nothing yet: %s peer allow %s --send ping --receive ping\n", yardr, p.Name)
				fmt.Printf("%s adds this yard with the line of: %s peer invite%s\n", p.Name, yardr, map[bool]string{true: " --url <where it reaches this yard>"}[p.URL == ""])
			})
		},
	}

	list := &cobra.Command{
		Use: "list", Short: "List peers", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ps, err := st.Peers(c.Context())
			if err != nil {
				return err
			}
			return a.out(ps, func() {
				rows := [][]string{}
				for _, p := range ps {
					out := fmt.Sprint(p.Waiting)
					if p.Failed > 0 {
						out += fmt.Sprintf(" (%d failed)", p.Failed)
					}
					rows = append(rows, []string{p.Name, or(p.URL, "- (it calls)"), or(strings.Join(p.Send, ","), "-"), or(strings.Join(p.Receive, ","), "-"), out})
				}
				table("NAME\tURL\tSEND\tRECEIVE\tWAITING", rows)
			})
		},
	}

	show := &cobra.Command{
		Use: "show <name>", Short: "Print a peer: its key, what the link carries, what waits for it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			p, err := st.GetPeer(ctx, args[0])
			if err != nil {
				return err
			}
			waiting, err := st.PeerOutbox(ctx, p.Name, 0)
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			listening, text := receiver(ctx, cfg, st)
			in := peerIncoming(*p, listening, text)
			type row struct {
				ID       string     `json:"id"`
				Kind     string     `json:"kind"`
				Seq      uint64     `json:"seq"`
				Attempts int        `json:"attempts,omitempty"`
				Error    string     `json:"error,omitempty"`
				RetryAt  *time.Time `json:"retry_at,omitempty"`
			}
			rows := []row{}
			for _, m := range waiting {
				rows = append(rows, row{m.ID, m.Kind, m.Seq, m.Attempts, m.Error, m.RetryAt})
			}
			return a.out(map[string]any{"peer": p, "listening": listening, "incoming": in, "waiting": rows}, func() {
				fmt.Printf("%s  (it calls itself %s)\n", p.Name, or(p.Calls, "-"))
				fmt.Printf("  key      %s\n", p.Key)
				if p.URL != "" {
					fmt.Printf("  url      %s: this yard posts to it and asks it for what waits there\n", p.URL)
				} else {
					fmt.Println("  url      none: this yard never calls it; it calls this one")
				}
				fmt.Printf("  send     %s\n", or(strings.Join(p.Send, ", "), "nothing"))
				fmt.Printf("  receive  %s\n", or(strings.Join(p.Receive, ", "), "nothing"))
				fmt.Printf("  incoming %s\n", in)
				if p.Failed > 0 {
					fmt.Printf("  %d messages for it were given up (peer_message_failed in yardr events)\n", p.Failed)
				}
				for _, m := range rows {
					line := fmt.Sprintf("  waiting: %s %s", m.Kind, m.ID)
					if m.Attempts > 0 {
						line += fmt.Sprintf("; tried %d times, next at %s: %s", m.Attempts, m.RetryAt.Local().Format("15:04:05"), m.Error)
					}
					fmt.Println(line)
				}
			})
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Short: "Remove a peer: nothing more is taken from it or sent to it", Args: cobra.ExactArgs(1),
		Long: `Removes the link on this side. What waited for the peer is dropped. The other
yard still has this one as a peer until its operator removes it there; its
requests are refused here from now on.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.DeletePeer(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"peer": args[0], "rm": "ok"}, func() { fmt.Printf("%s: removed\n", args[0]) })
		},
	}

	var ch store.PeerAllow
	allow := &cobra.Command{
		Use: "allow <name>", Short: "Say which kinds of messages the link with a peer carries", Args: cobra.ExactArgs(1),
		Long: `A link carries only the kinds of messages allowed, each direction on its own:
--send for what this yard may send the peer, --receive for what it takes from
it. Nothing is allowed until this says so, and only the operator may allow.
--no-send and --no-receive stop a kind again, which anyone may.

A message of a kind not allowed is refused before it leaves, and one that
comes in is answered 403 and logged (peer_message_refused). The kinds:
  ping   yardr peer ping: a ping, and the pong that answers it. A ping needs
         the kind allowed both ways on both sides, since the answer is a
         message too.`,
		Example: `  yardr peer allow north --send ping --receive ping
  yardr peer allow north --no-receive ping`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			if len(ch.Send)+len(ch.Receive)+len(ch.NoSend)+len(ch.NoReceive) == 0 {
				return fmt.Errorf("say what: --send or --receive <kind> (%s), --no-send or --no-receive to stop one", strings.Join(store.PeerKinds, ", "))
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if len(ch.Send)+len(ch.Receive) > 0 {
				if err := a.operatorOnly(ctx, st, "allow a peer's messages"); err != nil {
					return err
				}
			}
			p, err := st.AllowPeer(ctx, args[0], ch, a.who())
			if err != nil {
				return err
			}
			return a.out(p, func() {
				fmt.Printf("peer %s: send %s; receive %s\n", p.Name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"))
			})
		},
	}
	allow.Flags().StringSliceVar(&ch.Send, "send", nil, "kind this yard may send the peer (repeatable): "+strings.Join(store.PeerKinds, ", "))
	allow.Flags().StringSliceVar(&ch.Receive, "receive", nil, "kind this yard takes from the peer (repeatable)")
	allow.Flags().StringSliceVar(&ch.NoSend, "no-send", nil, "kind this yard no longer sends the peer")
	allow.Flags().StringSliceVar(&ch.NoReceive, "no-receive", nil, "kind this yard no longer takes from the peer")

	var timeout time.Duration
	ping := &cobra.Command{
		Use: "ping <name>", Short: "Send a peer a ping and wait for its pong", Args: cobra.ExactArgs(1),
		Long: `Sends the peer a ping as any message is sent, and waits for the pong the other
yard answers with, which comes back as any message does. It says how long
that took and which way each of the two went: push (posted by the sender) or
pull (asked for by the receiver). The daemons of both yards carry them, and
both yards must allow ping both ways.`,
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
			res, err := a.ping(ctx, cfg, st, args[0], timeout)
			if err != nil {
				return err
			}
			return a.out(res, func() {
				fmt.Printf("pong from %s in %s: the ping went by %s, the pong came by %s\n", res.Peer, res.Took.Round(time.Millisecond), res.Out, res.Back)
			})
		},
	}
	ping.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "how long to wait for the pong")

	cmd.AddCommand(key, invite, add, list, show, rm, allow, ping)
	return cmd
}

// invite is this yard's invite: its name, its public key (made now if it
// has none) and url, where it can be reached, when given.
func invite(ctx context.Context, cfg *config.Config, st store.API, url string) (yardkey.Invite, error) {
	k, _, err := yardkey.Ensure(cfg.PeerKey())
	if err != nil {
		return yardkey.Invite{}, err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return yardkey.Invite{}, err
	}
	inv := yardkey.Invite{Name: runner.YardName(cfg.Home, all), Key: yardkey.Public(k)}
	if url != "" {
		if inv.URL, err = yardkey.CleanURL(url); err != nil {
			return yardkey.Invite{}, fmt.Errorf("--url: %w", err)
		}
	}
	// Read back as the other side will read it: a yard name the invite
	// cannot carry is said here, not there.
	if _, err := yardkey.ParseInvite(inv.String()); err != nil {
		return yardkey.Invite{}, fmt.Errorf("%w; give the yard a name an invite can carry: %s settings set name <name>", err, yardrAt(cfg.Home))
	}
	return inv, nil
}

// config is the yard's home as this run knows it: read now when no command
// before has (a store handed in by a test connects to nothing).
func (a *app) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	return config.Load()
}

// operatorOnly refuses what only the operator may do when this command acts
// as anyone else, with the check a resident's definition is saved under
// (mayAllow): who a yard is linked with, and what it takes from them, is
// not for a resident's session to grant. Like that check it is a line, not
// a wall.
func (a *app) operatorOnly(ctx context.Context, st store.API, what string) error {
	operator, is, err := a.operator(ctx, st)
	if err != nil || is {
		return err
	}
	return fmt.Errorf("only the operator (%s) may %s; this is %s", operator, what, a.who())
}

// peerIncoming says how the peer's messages reach this yard, and that they
// do not when they cannot: a peer that is never called must call, and with
// no listener there is nothing for it to call.
func peerIncoming(p store.Peer, listening bool, text string) string {
	switch {
	case listening:
		return fmt.Sprintf("it posts to this yard under %s: %s", yards.Path, text)
	case p.URL != "":
		return "nothing can be posted to this yard, so it gets this peer's messages by asking it: " + text
	}
	return "none. No listener, no incoming: this peer is never called and cannot call: " + text
}

// pinged is what a ping found.
type pinged struct {
	Peer string        `json:"peer"`
	ID   string        `json:"id"`
	Took time.Duration `json:"took"`
	// Out and Back are the ways the ping and the pong went.
	Out  string `json:"out"`
	Back string `json:"back"`
}

// ping sends the peer a ping and waits for the pong: the message that
// names the ping as what it answers.
func (a *app) ping(ctx context.Context, cfg *config.Config, st store.API, name string, timeout time.Duration) (*pinged, error) {
	p, err := st.GetPeer(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, err := yardkey.Load(cfg.PeerKey()); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("this yard has no key, so it can sign nothing: %s peer key", yardrAt(cfg.Home))
	}
	// From before the ping is queued, so a pong that is quick is not missed.
	head, err := st.Head(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	m, err := st.PeerSend(ctx, name, store.PeerPing, nil, a.who())
	if err != nil {
		return nil, err
	}
	res := &pinged{Peer: name, ID: m.ID, Out: store.PeerPush}
	if p.URL == "" {
		res.Out = store.PeerPull
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for ctx.Err() == nil {
		ch, err := st.Subscribe(ctx, head)
		if err != nil {
			return nil, err
		}
		for e := range ch {
			head = e.Seq
			if peer, _ := e.Data["peer"].(string); peer != name {
				continue
			}
			id, _ := e.Data["id"].(string)
			re, _ := e.Data["re"].(string)
			switch {
			case e.Kind == "peer_message_received" && re == m.ID:
				res.Took = time.Since(start)
				res.Back, _ = e.Data["way"].(string)
				return res, nil
			case e.Kind == "peer_message_failed" && id == m.ID:
				reason, _ := e.Data["reason"].(string)
				return nil, fmt.Errorf("the ping to %s was given up: %s", name, reason)
			case e.Kind == "peer_removed":
				return nil, fmt.Errorf("peer %s was removed while its pong was waited for", name)
			}
		}
	}
	return nil, errors.New(noPong(ctx, st, cfg, *p, timeout, m.ID))
}

// noPong says that no pong came, and what is known of why.
func noPong(ctx context.Context, st store.API, cfg *config.Config, p store.Peer, timeout time.Duration, id string) string {
	ctx = context.WithoutCancel(ctx)
	text := fmt.Sprintf("no pong from %s within %s", p.Name, timeout)
	if !daemonRunning(cfg.DaemonLock()) {
		return text + fmt.Sprintf(": no daemon runs on this yard, and the daemon is what carries messages (%s daemon)", yardrAt(cfg.Home))
	}
	if !slices.Contains(p.Receive, store.PeerPing) {
		return text + fmt.Sprintf(": this yard does not take ping from %s, so its pong is refused here (%s peer allow %s --receive ping)", p.Name, yardrAt(cfg.Home), p.Name)
	}
	waiting, err := st.PeerOutbox(ctx, p.Name, 0)
	if err != nil {
		return text
	}
	for _, m := range waiting {
		switch {
		case m.ID != id:
		case m.Error != "":
			return text + ": the ping has not arrived there: " + m.Error
		case p.URL == "":
			return text + fmt.Sprintf(": the ping waits here for %s to ask for it; its daemon must run, and it must have this yard's URL", p.Name)
		default:
			return text + ": the ping still waits here to be posted"
		}
	}
	return text + fmt.Sprintf(": the ping arrived there, and no pong came back. %s must allow ping both ways too, and its daemon must run", p.Name)
}

// peerPart is what doctor says of the yard's peers: who they are, and what
// keeps their messages from crossing: no key, no listener for a peer that
// must call, messages given up.
func peerPart(ctx context.Context, cfg *config.Config, st store.API) (optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	part := optionalPart{Key: "peers", Title: "peers",
		Detail: fmt.Sprintf("this yard is linked with no other; %s peer --help (docs/peers.md)", yardr)}
	ps, err := st.Peers(ctx)
	if err != nil || len(ps) == 0 {
		return part, err
	}
	var names, callers []string
	for _, p := range ps {
		if p.URL == "" {
			names = append(names, p.Name+" (it calls)")
			callers = append(callers, p.Name)
		} else {
			names = append(names, p.Name)
		}
		if p.Failed > 0 {
			part.Notes = append(part.Notes, fmt.Sprintf("%d messages for peer %s were given up (%s peer show %s)", p.Failed, p.Name, yardr, p.Name))
		}
	}
	part.Configured, part.Detail = true, strings.Join(names, ", ")
	if _, err := yardkey.Load(cfg.PeerKey()); errors.Is(err, fs.ErrNotExist) {
		part.Notes = append(part.Notes, fmt.Sprintf("this yard has no key, so it signs nothing and its peers cannot know it: %s peer key", yardr))
	} else if err != nil {
		part.Notes = append(part.Notes, "this yard's key cannot be used, so nothing is sent to or taken from its peers: "+err.Error())
	}
	if listening, text := receiver(ctx, cfg, st); !listening {
		note := "no listener, no incoming: nothing can be posted to this yard, so it gets its peers' messages only by asking those it can call"
		if len(callers) > 0 {
			note += "; " + strings.Join(callers, ", ") + " cannot be called and so cannot be heard at all"
		}
		part.Notes = append(part.Notes, note+": "+text)
	}
	return part, nil
}
