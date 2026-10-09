package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	peerscripts "yardr.dev/yardr/contrib/peer"
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

The short way, when one operator can read a code to the other:

  yardr peer pair                      in a terminal: a form, on both yards. Else,
                                       on the yard that can be reached: shows a code
  yardr peer pair <address> <code>     on the other: the yards exchange their keys,
                                       each operator confirms a check, a ping proves it

The way by hand, when no code can be passed, or for scripts:

  yardr peer key                       this yard's public key (made on first use)
  yardr peer invite --url <url>        one line for the other operator to paste
  yardr peer add <name> <invite>       add the other yard, from its invite
  yardr peer allow <name> --send ping --receive ping
  yardr peer ping <name>               prove the link, both ways
  yardr peer rename <old> <new>        change the name this yard has for a link
  yardr peer set <name> --url <url>    change where this yard calls it; --url "" for
                                       a yard that is never called, and calls
  yardr peer intake <name> --depot <depot> --flow <flow>
                                       where the beads it sends go (with
                                       yardr peer allow <name> --receive bead)

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
				ID   string `json:"id"`
				Kind string `json:"kind"`
				Seq  uint64 `json:"seq"`
				// To and From are a mail's, the one a crew member of the peer.
				To       string     `json:"to,omitempty"`
				From     string     `json:"from,omitempty"`
				Attempts int        `json:"attempts,omitempty"`
				Error    string     `json:"error,omitempty"`
				RetryAt  *time.Time `json:"retry_at,omitempty"`
			}
			rows := []row{}
			for _, m := range waiting {
				r := row{ID: m.ID, Kind: m.Kind, Seq: m.Seq, Attempts: m.Attempts, Error: m.Error, RetryAt: m.RetryAt}
				if m.Kind == store.PeerMail {
					var mail struct{ To, From string }
					_ = json.Unmarshal(m.Body, &mail)
					r.To, r.From = mail.To, mail.From
				}
				rows = append(rows, r)
			}
			return a.out(map[string]any{"peer": p, "listening": listening, "incoming": in, "waiting": rows}, func() {
				fmt.Printf("%s  (it calls itself %s)\n", p.Name, or(p.Calls, "-"))
				for _, w := range p.Was {
					fmt.Printf("  was      %s until %s\n", w.Name, w.At.Local().Format("2006-01-02 15:04"))
				}
				fmt.Printf("  key      %s\n", p.Key)
				if p.URL != "" {
					fmt.Printf("  url      %s: this yard posts to it and asks it for what waits there\n", p.URL)
				} else {
					fmt.Println("  url      none: this yard never calls it; it calls this one")
				}
				fmt.Printf("  send     %s\n", or(strings.Join(p.Send, ", "), "nothing"))
				fmt.Printf("  receive  %s\n", or(strings.Join(p.Receive, ", "), "nothing"))
				fmt.Printf("  expose   %s\n", exposeLine(*p))
				if line := intakeLine(*p); line != "" {
					fmt.Printf("  intake   %s\n", line)
				}
				if p.Build != "" {
					fmt.Printf("  build    yardr %s, as its last bead said\n", p.Build)
				}
				fmt.Println("  " + understandsLine(*p))
				if line := exposesLine(*p); line != "" {
					fmt.Println("  " + line)
				}
				if line := trustLine(*p); line != "" {
					fmt.Println("  trusted as: " + line)
				}
				fmt.Printf("  heard    %s\n", store.HeardLine(p.HeardAt))
				if p.Away > 0 {
					fmt.Printf("  away     %d of this yard's beads are there (yardr bead show <bead> says since when)\n", p.Away)
				}
				fmt.Printf("  incoming %s\n", in)
				if p.Failed > 0 {
					fmt.Printf("  %d messages for it were given up (peer_message_failed in yardr events)\n", p.Failed)
				}
				for _, m := range rows {
					line := fmt.Sprintf("  waiting: %s %s", m.Kind, m.ID)
					if m.To != "" {
						line += fmt.Sprintf(" from %s to %s@%s", m.From, m.To, p.Name)
					}
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
requests are refused here from now on.

The beads it sent stay as they are, and say that their link is removed: an
open one is still worked or closed here, and nothing is returned for it.`,
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

	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Change the name this yard has for a peer", Args: cobra.ExactArgs(2),
		Long: `Changes the name this yard knows a linked yard by. The name is this yard's
own: the other yard hears nothing of it and changes nothing. The link stays
what it is: its key, its URL and what it carries (yardr peer allow).

Everything that names the peer follows, in the same write:
  - mail that waits for it is delivered, and what was taken from it is still
    known as taken;
  - a crew member that may mail it (its peers setting) may still, under the new
    name, and is told of it by mail from the yard; one that may release its
    beads (its release setting) may still;
  - a bead it sent says it came from <new> (yardr bead show), and an open
    one is labelled from-<new>;
  - yardr inbox shows mail it sent before as from <name>@<new>, and yardr mail
    --with <name>@<new> prints the mail from before and after as one
    conversation.

The old name is free afterwards and reaches nothing: a yard added under it
later is another yard, with no mail, no grant and no history of this one's.
The event log is not rewritten: its events keep the name the peer had when
they were logged, and peer_renamed says when it changed.

Only the operator may: the name is what crew members' addresses reach a yard by.
Refused when <old> is no peer, <new> is no name for one, or <new> is a peer
already.`,
		Example: `  yardr peer rename nroth north`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			var may []string
			rs, err := st.Crew(ctx)
			if err != nil {
				return err
			}
			for _, r := range rs {
				if slices.Contains(r.MayMail(), args[0]) {
					may = append(may, r.Name)
				}
			}
			p, err := st.RenamePeer(ctx, args[0], args[1], a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"peer": p, "from": args[0], "crew": may}, func() {
				fmt.Printf("peer %s is %s now: its crew are <name>@%s, and nothing is reached as <name>@%s\n", args[0], p.Name, p.Name, args[0])
				if p.Waiting > 0 {
					fmt.Printf("%d messages that waited for it still wait, and are delivered\n", p.Waiting)
				}
				if len(may) > 0 {
					fmt.Printf("%s may mail it as before, and got mail from the yard that says so\n", strings.Join(may, ", "))
				}
			})
		},
	}

	var to string
	set := &cobra.Command{
		Use: "set <name> --url <url>", Short: "Change where this yard calls a peer, or make it one that is never called", Args: cobra.ExactArgs(1),
		Long: `Changes the URL this yard calls a linked yard at: the yard moved to another
host or port, or TLS was put in front of it. --url "" takes the URL away:
this yard never calls the peer then, as it does not call a laptop behind a
router, and the peer calls this one and asks for what waits here.

The link stays what it is: its name, its key and what it carries (yardr peer
allow). It is one write, and what waits for the peer still waits: each
message is posted to the new address at its next try, or, for a peer no
longer called, handed over when it asks. Removing the peer and adding it
again would drop them.

The other yard hears nothing of it: it knows this yard by its key. Where it
calls this yard is its own to set, there.

Only the operator may: the URL is where this yard's messages go. Refused
when <name> is no peer, when the URL is none yardr peer add would take
(http:// or https:// and a host, no credential in it), or when it is the URL
the peer has already.`,
		Example: `  yardr peer set north --url https://north.example.com
  yardr peer set laptop --url ""         # never called: it calls this yard`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			if !c.Flags().Changed("url") {
				return errors.New(`say what: --url <url> for where this yard calls the peer, or --url "" for a peer it never calls`)
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			p, err := st.SetPeerURL(ctx, args[0], to, a.who())
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			return a.out(p, func() {
				if p.URL == "" {
					fmt.Printf("peer %s is never called now: it calls this yard and asks for what waits here\n", p.Name)
					if p.Waiting > 0 {
						fmt.Printf("%d messages that waited for it still wait, and are handed over when it asks\n", p.Waiting)
					}
					// What only the peer can do is now all that brings its
					// messages in and takes this yard's out.
					listening, text := receiver(ctx, cfg, st)
					fmt.Printf("incoming %s\n", peerIncoming(*p, listening, text))
					return
				}
				fmt.Printf("peer %s is called at %s now\n", p.Name, p.URL)
				if p.Waiting > 0 {
					fmt.Printf("%d messages that waited for it still wait, and are posted there, each at its next try\n", p.Waiting)
				}
				if !daemonRunning(cfg.DaemonLock()) {
					fmt.Printf("no daemon runs on this yard, and the daemon is what calls it: %s daemon\n", yardrAt(cfg.Home))
				}
			})
		},
	}
	set.Flags().StringVar(&to, "url", "", `where this yard reaches the peer's hook listener, such as https://north.example.com ("": it is never called)`)

	var ch store.PeerAllow
	var trust, noTrust []string
	allow := &cobra.Command{
		Use: "allow <name>", Short: "Say which kinds of messages the link with a peer carries", Args: cobra.ExactArgs(1),
		Long: `A link carries only the kinds of messages allowed, each direction on its own:
--send for what this yard may send the peer, --receive for what it takes from
it. Nothing is allowed until this says so, and only the operator may allow.
--no-send and --no-receive stop a kind again, which anyone may.

Each yard tells the other which kinds its yardr understands (yardr peer show).
--send for a kind the peer has said it does not understand, or for a peer
that has not said, is allowed and warns: what a peer said may be stale, and a
message its yardr does not know is refused there and given up here with that
reason.

A message of a kind not allowed is refused before it leaves, and one that
comes in is answered 403 and logged (peer_message_refused). The kinds:
  ping   yardr peer ping: a ping, and the pong that answers it. A ping needs
         the kind allowed both ways on both sides, since the answer is a
         message too.
  mail   yardr mail <crew member>@<peer>: mail between crew members of the two
         yards. --send lets this yard's crew write (each needs the peer
         in its peers setting too), --receive puts the peer's mail in the
         inbox of the crew member it names.
  file   yardr mail <crew member>@<peer> --file: the images mail carries, each
         a message of its own that crosses before its mail. --send lets
         this yard's mail to the peer carry images, --receive lets the
         peer's bytes land in this yard's blobs. Without --receive the
         peer's mail still arrives, and says "not taken" where the image
         would be. One file may have up to yardr settings get
         ` + store.PeerFileMaxSetting + ` bytes, on each side by its own setting.
  bead   a bead of one yard worked by the other. --receive lets this yard
         take the peer's beads, each as a bead of its own, and send back how
         it ended; where they go is yardr peer intake, and without one each
         is refused. --send lets this yard send the peer beads and take
         what comes back. One word covers the bead and its answer, so a yard
         that only receives can be sent work and can send none.

Whom of this yard the peer may see and write to is said apart, by name:
--expose <name>,... for crew members and users of this yard, which only the
operator may, and --no-expose to stop, which anyone may. Nobody is exposed
until this says so. The peer is told the names, each with one line of what
they do (yardr peer show there lists them), and mail from it reaches those
names and no other: mail to anyone else is refused (not_exposed). --receive
mail is still what lets the link carry mail at all; --expose alone shows the
peer who is there.

--trust <name>@<peer>=<user> says that mail from that name of the peer's
yard counts as that user of this yard speaking: it arrives marked "(as
<user>)", and a crew member takes it as the user's word. <name> must be on
the roster the peer last said (yardr peer show lists whom it exposes) and
<user> a user of this yard (yardr user list). Only the operator may;
--no-trust <name>@<peer> stops it, which anyone may. It is about mail and
nothing else: the name gets no powers of the operator's and runs no command.
The grant goes when the user is removed or the peer exposes the name no
more (docs/peers.md).`,
		Example: `  yardr peer allow north --send ping --receive ping
  yardr peer allow north --send mail --receive mail
  yardr peer allow north --send file --receive file
  yardr peer allow north --expose yardmaster,mckean
  yardr peer allow north --no-expose mckean
  yardr peer allow north --trust mckean@north=mckean
  yardr peer allow north --no-trust mckean@north
  yardr peer allow north --receive bead
  yardr peer allow north --no-receive ping`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			var err error
			if ch.Trust, ch.NoTrust, err = peerTrust(args[0], trust, noTrust); err != nil {
				return err
			}
			if len(ch.Send)+len(ch.Receive)+len(ch.NoSend)+len(ch.NoReceive)+len(ch.Expose)+len(ch.NoExpose)+len(ch.Trust)+len(ch.NoTrust) == 0 {
				return fmt.Errorf("say what: --send or --receive <kind> (%s), --no-send or --no-receive to stop one, --expose or --no-expose <name> for whom of this yard the peer may write to, --trust <name>@<peer>=<user> or --no-trust <name>@<peer> for whose mail counts as a user's", strings.Join(store.PeerKinds, ", "))
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			p, err := st.AllowPeer(ctx, args[0], ch, a.who())
			if err != nil {
				return err
			}
			// To stderr, so --json prints the peer and nothing else.
			for _, warning := range sendWarnings(*p, ch.Send) {
				fmt.Fprintln(stderr, "warning: "+warning)
			}
			return a.out(p, func() {
				fmt.Printf("peer %s: send %s; receive %s; expose %s\n", p.Name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"), or(strings.Join(p.Expose, ", "), "nobody"))
				if note := exposeNote(*p); note != "" {
					fmt.Println(note)
				}
				if line := trustLine(*p); line != "" {
					fmt.Println("trusted as: " + line)
				}
			})
		},
	}
	allow.Flags().StringSliceVar(&ch.Send, "send", nil, "kind this yard may send the peer (repeatable): "+strings.Join(store.PeerKinds, ", "))
	allow.Flags().StringSliceVar(&ch.Receive, "receive", nil, "kind this yard takes from the peer (repeatable)")
	allow.Flags().StringSliceVar(&ch.NoSend, "no-send", nil, "kind this yard no longer sends the peer")
	allow.Flags().StringSliceVar(&ch.NoReceive, "no-receive", nil, "kind this yard no longer takes from the peer")
	allow.Flags().StringSliceVar(&ch.Expose, "expose", nil, "crew member or user of this yard the peer may see and write to (repeatable)")
	allow.Flags().StringSliceVar(&ch.NoExpose, "no-expose", nil, "name the peer no longer sees or writes to")
	allow.Flags().StringSliceVar(&trust, "trust", nil, "<name>@<peer>=<user>: mail from that name of the peer counts as that user of this yard speaking (repeatable)")
	allow.Flags().StringSliceVar(&noTrust, "no-trust", nil, "<name>@<peer> whose mail no longer counts as a user's")

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

	cmd.AddCommand(a.pairCmd(), key, invite, add, list, show, rm, rename, set, allow, a.peerIntakeCmd(), ping)
	return cmd
}

func (a *app) peerIntakeCmd() *cobra.Command {
	var in store.PeerIntake
	var none bool
	cmd := &cobra.Command{
		Use: "intake <name>", Short: "Say where the beads a peer sends go: the depot and the flow", Args: cobra.ExactArgs(1),
		Long: `A linked yard can send this one a bead to work (yardr peer allow <name>
--receive bead). The intake says where such a bead goes: it becomes a bead of
this yard in --depot, pinned to --flow, in that flow's first stage, labelled
from-<name>. When the flow ends it (a terminal stage), the outcome and the
notes written here go back. Without an intake every bead the peer sends is
refused (no_intake).

The flow is this yard's choice, never the sender's: a flow of the store
(yardr flow add) or of the depot's repository. What stands between arrival and
the end is its shape: a first stage a person works, or one routed to builders.

A bead arrives held: its text is another yard's, and it becomes the brief of
an agent that runs with this yard's permissions. Three levels, per peer:
  - held, and a user releases it with yardr bead unhold (the default);
  - held, and a crew member the operator named may release it too
    (yardr crew set <crew member> release=<name>);
  - --auto: it arrives ready, and the flow's routes start on it unseen. That
    lets the operator of the other yard start agents here.

--limit is how many open beads from the peer this yard keeps at once (10
unless set): the next is refused (busy) until one ends.

--hold-for is how long a held bead waits to be released (3d unless set, as
40m, 12h or 7d). One that nobody released by then is closed by the yard and
goes back as refused, with a note that says so, and the sender's flow routes
on it. A day before, or at half the time when it is shorter than two days,
peer_bead_expiring is logged on the bead (yardr subscribe <name> yardr
--where kind=peer_bead_expiring). --hold-for 0 keeps a bead for as long as
nobody releases it. It holds for the beads that wait already.

Only the operator may set an intake, as only the operator allows what a link
carries. --none removes it, which anyone may. Refused when the depot is none
of the yard's or the flow does not resolve for it.`,
		Example: `  yardr peer allow north --receive bead
  yardr peer intake north --depot web --flow incoming
  yardr peer intake north --depot web --flow incoming --auto
  yardr peer intake north --depot web --flow incoming --hold-for 7d
  yardr peer intake north --none`,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			set := in.Depot != "" || in.Flow != "" || in.Auto || c.Flags().Changed("limit") || c.Flags().Changed("hold-for")
			switch {
			case none && set:
				return errors.New("--none removes the intake: it goes with no other flag")
			case !none && (in.Depot == "" || in.Flow == ""):
				return errors.New("say where its beads go: --depot <depot> --flow <flow> (--auto to let them arrive ready), or --none")
			case c.Flags().Changed("limit") && in.Limit < 1:
				return errors.New("--limit: a number of open beads, at least 1")
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			var want *store.PeerIntake
			if !none {
				want = &in
			}
			p, err := st.SetPeerIntake(ctx, args[0], want, a.who())
			if err != nil {
				return err
			}
			return a.out(p, func() {
				if p.Intake == nil {
					fmt.Printf("peer %s: no intake; a bead it sends is refused\n", p.Name)
					return
				}
				fmt.Printf("peer %s: %s\n", p.Name, intakeLine(*p))
				// The code of a bead worked in a repository goes back by a
				// push, which is a stage of the flow and a script's: said
				// here, where the operator decides what the peer's beads
				// become.
				if depot, err := st.GetDepot(ctx, p.Intake.Depot); err == nil && !depot.IsDir() {
					a.returnBranchHint(p.Intake.Flow, depot.Name)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.Depot, "depot", "", "the depot its beads become beads of")
	f.StringVar(&in.Flow, "flow", "", "the flow each is pinned to, and starts in the first stage of")
	f.BoolVar(&in.Auto, "auto", false, "let its beads arrive ready, not held: the other yard's operator can then start agents here")
	f.IntVar(&in.Limit, "limit", 0, fmt.Sprintf("open beads from it this yard keeps at once (default %d)", store.PeerIntakeLimit))
	f.StringVar(&in.HoldFor, "hold-for", "", fmt.Sprintf("how long a held bead waits to be released before it goes back as refused, such as 12h or 7d (default %s; 0 for never)", store.Span(store.PeerHoldFor)))
	f.BoolVar(&none, "none", false, "remove the intake: every bead it sends is refused")
	return cmd
}

// returnBranchHint installs this build's return-branch in the home's
// scripts, as yardr setup installs merge-local, and says how a stage of
// the intake's flow runs it. One already there is kept as it is: it may be
// the yard's own. A script that cannot be written is said and stops
// nothing: the intake is set.
func (a *app) returnBranchHint(flow, depot string) {
	cfg, err := a.config()
	if err != nil {
		return
	}
	path := filepath.Join(cfg.Home, "scripts", "return-branch")
	have, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, err := writeNew(path, string(peerscripts.ReturnBranch), 0o755); err != nil {
			fmt.Printf("return-branch could not be installed (%v): it is contrib/peer/return-branch of yardr\n", err)
			return
		}
		fmt.Printf("return-branch installed: %s (this build's contrib/peer/return-branch)\n", path)
	case err != nil:
		fmt.Printf("return-branch could not be read (%v)\n", err)
		return
	case !bytes.Equal(have, peerscripts.ReturnBranch):
		fmt.Printf("return-branch is there already and differs from this build's: %s is kept as it is\n", path)
	}
	fmt.Printf("Its beads' code goes back when a stage of flow %s pushes their branch (docs/peers.md, \"Code that comes back\"):\n"+
		"  yardr group add %s/return --runner exec --limit 1 --set cmd=%s\n"+
		"  yardr route add --stage push --depot %s --group %s/return\n"+
		"Without such a stage the outcome and the notes go back, and no code.\n",
		flow, depot, shellQuote("YARDR_PUSH=origin "+shellQuote(path)), depot, depot)
}

// intakeLine says where a peer's beads go and how they arrive, or that
// they are refused when the link takes them and no intake says where; ""
// when the link takes none and nothing is set.
func intakeLine(p store.Peer) string {
	takes := slices.Contains(p.Receive, store.PeerBead)
	if p.Intake == nil {
		if !takes {
			return ""
		}
		return fmt.Sprintf("none: a bead it sends is refused (yardr peer intake %s --depot <depot> --flow <flow>)", p.Name)
	}
	how := fmt.Sprintf("its beads arrive held, for a user to release (yardr bead unhold), or a crew member with release=%s, ", p.Name)
	if hold := p.Intake.Hold(); hold > 0 {
		how += fmt.Sprintf("and go back as refused when nobody has within %s", store.Span(hold))
	} else {
		how += "and wait for as long as nobody has (--hold-for 0)"
	}
	if p.Intake.Auto {
		how = "its beads arrive ready (--auto): the flow's routes start on them unseen"
	}
	line := fmt.Sprintf("depot %s, flow %s: %s; at most %d open, %d now", p.Intake.Depot, p.Intake.Flow, how, p.Intake.Most(), p.Beads)
	if !takes {
		line += fmt.Sprintf(". The link does not take its beads yet: yardr peer allow %s --receive bead", p.Name)
	}
	return line
}

// understandsLine is what yardr peer show says of the peer's build: the
// kinds its yardr last said it understands, or that it has not said. A yard
// that has not said is one on a yardr from before yards said it, or one
// nothing has come from since the link was made.
func understandsLine(p store.Peer) string {
	if p.Said == nil {
		return "has not said what it understands (an older yardr, or nothing has come from it yet)"
	}
	return fmt.Sprintf("understands: %s (yardr %s, said %s)", or(strings.Join(p.Said.Understands, ", "), "nothing"),
		or(p.Said.Yardr, "of a build it does not name"), p.Said.At.Local().Format("2006-01-02 15:04"))
}

// exposeLine is whom of this yard the peer may see and write to, and what
// is amiss between that and the mail the link takes (exposeNote).
func exposeLine(p store.Peer) string {
	line := or(strings.Join(p.Expose, ", "), "nobody")
	if note := exposeNote(p); note != "" {
		line += ": " + note
	}
	return line
}

// exposeNote says where the two grants of mail in do not meet: a link that
// takes mail and exposes nobody takes none, and one that exposes someone
// and takes no mail shows the peer names it cannot write to. "" when they
// meet. It is what a link from before there was a roster needs said once:
// every crew member took a peer's mail then.
func exposeNote(p store.Peer) string {
	takes := slices.Contains(p.Receive, store.PeerMail)
	switch {
	case takes && len(p.Expose) == 0:
		return fmt.Sprintf("the link takes mail and exposes nobody, so all mail from %s is refused (yardr peer allow %s --expose <crew member or user>,...)", p.Name, p.Name)
	case !takes && len(p.Expose) > 0:
		return fmt.Sprintf("%s sees them and cannot write to them: the link takes no mail (yardr peer allow %s --receive mail)", p.Name, p.Name)
	}
	return ""
}

// exposesLine is the roster the peer last said: whom of its yard this one
// may see and write to, each with what they are and the line the peer
// gives of what they do. "" for a peer that exposes nobody, or has not
// said (understandsLine says which).
func exposesLine(p store.Peer) string {
	if p.Said == nil || len(p.Said.Roster) == 0 {
		return ""
	}
	var who []string
	mail := true
	for _, e := range p.Said.Roster {
		is := map[string]string{store.RosterCrew: "crew", store.RosterUser: "person"}[e.Kind]
		if e.Role != "" {
			is += ", " + e.Role
		}
		who = append(who, fmt.Sprintf("%s (%s)", e.Name, is))
		mail = mail && e.Mail
	}
	line := "exposes: " + strings.Join(who, ", ")
	if !mail {
		line += ". Its link takes no mail from this yard, so none of them can be written to yet (there: yardr peer allow <this yard> --receive mail)"
	}
	return line
}

// trustLine is whose mail from the peer counts as a user of this yard
// speaking (store.Peer.Trust): "mckean@north -> mckean", "" when nobody's.
func trustLine(p store.Peer) string {
	var out []string
	for _, t := range p.Trusted() {
		out = append(out, t.String())
	}
	return strings.Join(out, ", ")
}

// peerTrust reads what --trust and --no-trust were given for peer:
// <name>@<peer>=<user> and <name>@<peer>. The address is written whole, as
// mail names its sender, so one meant for another link is caught here.
func peerTrust(peer string, trust, noTrust []string) (add map[string]string, drop []string, err error) {
	name := func(flag, addr string) (string, error) {
		n, at, ok := strings.Cut(addr, "@")
		switch {
		case !ok || n == "" || at == "":
			return "", fmt.Errorf("--%s %s: name the sender as mail does, <name>@%s", flag, addr, peer)
		case at != peer:
			return "", fmt.Errorf("--%s %s: that is a name of %s, and this changes the link with %s", flag, addr, at, peer)
		}
		return n, nil
	}
	for _, t := range trust {
		addr, user, ok := strings.Cut(t, "=")
		if !ok || user == "" {
			return nil, nil, fmt.Errorf("--trust %s: say whom the name is trusted as, <name>@%s=<user of this yard>", t, peer)
		}
		n, err := name("trust", addr)
		if err != nil {
			return nil, nil, err
		}
		if add == nil {
			add = map[string]string{}
		}
		add[n] = user
	}
	for _, addr := range noTrust {
		n, err := name("no-trust", addr)
		if err != nil {
			return nil, nil, err
		}
		drop = append(drop, n)
	}
	return add, drop, nil
}

// trustNotes says which of the link's grants name someone who is gone: a
// user this yard has no more, or a name the peer exposes no more. The store
// drops a grant when it sees either go (docs/peers.md), so one that is left
// came another way, and speaks as nobody until it is taken off.
func trustNotes(p store.Peer, users []string, yardr string) []string {
	var out []string
	for _, t := range p.Trusted() {
		exposed := p.Said != nil && slices.ContainsFunc(p.Said.Roster, func(e store.PeerRosterEntry) bool { return e.Name == t.Name })
		var gone string
		switch {
		case !slices.Contains(users, t.As):
			gone = fmt.Sprintf("%s is no user of this yard", t.As)
		case !exposed:
			gone = fmt.Sprintf("%s exposes nobody called %s", p.Name, t.Name)
		default:
			continue
		}
		out = append(out, fmt.Sprintf("the link with peer %s trusts %s as %s, and %s: %s peer allow %s --no-trust %s", p.Name, t.Address(), t.As, gone, yardr, p.Name, t.Address()))
	}
	return out
}

// sendWarnings says, for each of the kinds just allowed out to p, that p's
// yardr may not take it: p said it does not understand the kind, or has not
// said what it understands. They warn and stop nothing: what a peer said
// may be stale.
func sendWarnings(p store.Peer, kinds []string) []string {
	var out []string
	for _, k := range kinds {
		switch {
		case p.Said == nil:
			out = append(out, fmt.Sprintf("%s has not said what its yardr understands (an older yardr, or nothing has come from it yet): a %s sent to it may be refused there", p.Name, k))
		case !p.Said.Knows(k):
			out = append(out, fmt.Sprintf("%s's yardr (%s) said at %s that it understands %s, and not %s: a %s sent to it is refused there until it is upgraded",
				p.Name, or(p.Said.Yardr, "a build it does not name"), p.Said.At.Local().Format("2006-01-02 15:04"), or(strings.Join(p.Said.Understands, ", "), "nothing"), k, k))
		}
	}
	return out
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
// as anyone else, where no write of the store's refuses it in time: a read
// (everyone's timers), and a pairing, whose first write comes after the
// other yard was called. Every other such rule is the store's
// (store.operatorOnly), in the write itself. Like it, this is a line, not a
// wall.
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
	// A yardr server that restarts while the pong is waited for does not end
	// the wait: the pong is in the log when it is back.
	for e := range store.Following(ctx, st, head, streamRetry) {
		if peer, _ := e.Data["peer"].(string); peer != name && !(e.Kind == "peer_renamed" && e.Data["from"] == name) {
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
		case e.Kind == "peer_renamed":
			// The pong comes under the name the peer has now.
			name, _ = e.Data["to"].(string)
			res.Peer, p.Name = name, name
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

// peerPart is what doctor says of the yard's peers: who they are, whom of
// this yard the mail of each reaches, and what keeps their messages from
// crossing: no key, no listener for a peer that must call, messages given
// up, mail taken for nobody.
func peerPart(ctx context.Context, cfg *config.Config, st store.API) (optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	part := optionalPart{Key: "peers", Title: "peers",
		Detail: fmt.Sprintf("this yard is linked with no other; %s peer pair in a terminal links it with one, as a form (docs/peers.md)", yardr)}
	ps, err := st.Peers(ctx)
	if err != nil || len(ps) == 0 {
		return part, err
	}
	us, err := st.Users(ctx)
	if err != nil {
		return part, err
	}
	var users []string
	for _, u := range us {
		users = append(users, u.Name)
	}
	var names, callers []string
	for _, p := range ps {
		var marks []string
		if p.URL == "" {
			marks = append(marks, "it calls")
			callers = append(callers, p.Name)
		}
		// Per link that takes mail: who is exposed on it is who gets it.
		if slices.Contains(p.Receive, store.PeerMail) {
			marks = append(marks, "its mail reaches "+or(strings.Join(p.Expose, ", "), "nobody"))
		}
		// And whose mail from it is a user's word.
		if line := trustLine(p); line != "" {
			marks = append(marks, "trusted as: "+line)
		}
		part.Notes = append(part.Notes, trustNotes(p, users, yardr)...)
		name := p.Name
		if len(marks) > 0 {
			name += " (" + strings.Join(marks, "; ") + ")"
		}
		names = append(names, name)
		if p.Failed > 0 {
			part.Notes = append(part.Notes, fmt.Sprintf("%d messages for peer %s were given up (%s peer show %s)", p.Failed, p.Name, yardr, p.Name))
		}
		// Mail with an image to it is refused here while that holds.
		if slices.Contains(p.Send, store.PeerFile) {
			if err := p.SendsFiles(); err != nil {
				part.Notes = append(part.Notes, fmt.Sprintf("the link sends files, and %s", strings.TrimPrefix(err.Error(), "peer ")))
			}
		}
		// A link from before there was a roster took mail for every crew
		// member, and takes none until someone is exposed on it.
		if slices.Contains(p.Receive, store.PeerMail) && len(p.Expose) == 0 {
			part.Notes = append(part.Notes, fmt.Sprintf("the link with peer %s takes mail and exposes nobody, so all its mail is refused: %s peer allow %s --expose <crew member or user>,...", p.Name, yardr, p.Name))
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
