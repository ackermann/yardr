// The peer add row of the run-ask list (runList): linking another yard as
// a peer from its invite. It has what a row has: how its words are read
// into one stored form, the command's own check as the asker, the card the
// yard writes from the invite, why an approval of an older card is refused,
// and the store function that makes the change. It is about one peer's
// link, so an approval that came through that peer's own reach is refused
// (runCommand.link): a peer does not add its own link on its own word.
package store

import (
	"context"
	"fmt"

	"yardr.dev/yardr/internal/yardkey"
)

// parsePeerAdd reads the words of peer add <name> <invite>.
func parsePeerAdd(rest []string) ([]string, error) {
	const use = "yardr peer add <name> <invite>"
	if len(rest) != 2 {
		return nil, fmt.Errorf("--run takes %s: one peer and one invite, not %d words", use, len(rest))
	}
	return []string{rest[0], rest[1]}, nil
}

// checkPeerAdd is adding a peer's own check as the asker.
func checkPeerAdd(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.may(ctx, actor, ActAddPeer, Object{Kind: KindPeer, Name: argv[2], Ask: Askable})
}

// peerAddCard is the card of peer add <name> <invite>: the yard the invite
// names linked as the peer, with its key whole and where this yard calls
// it, or that this yard never calls it, each verbatim, and that the link
// carries nothing until peer allow says what. The approval is held to the
// name standing free (Seen). Asking refuses an invite that is none, a name
// or a key a peer has already, as the command does.
func (w *wtx) peerAddCard(argv []string, asking bool) (AskRun, error) {
	name := argv[2]
	inv, err := yardkey.ParseInvite(argv[3])
	if err != nil {
		return AskRun{}, err
	}
	if err := w.checkPeerInvite(inv.Key); err != nil {
		return AskRun{}, err
	}
	p := Peer{Name: name, Key: inv.Key, URL: inv.URL, Calls: inv.Name}
	if err := peerFree(w.Tx, &p); err != nil {
		if asking {
			return AskRun{}, err
		}
		// The approval reads the card again without refusing what
		// changed: a peer of the name added since is stale's to
		// refuse, in its own words. Anything else, a key another
		// peer holds since among it, refuses the approval here, and
		// nothing runs.
		if _, lerr := loadPeer(w.Tx, name); lerr != nil {
			return AskRun{}, err
		}
	}
	called := "called at " + inv.URL
	if inv.URL == "" {
		called = "never called: it calls this one and asks for what waits here"
	}
	key := ""
	if have, err := loadPeer(w.Tx, name); err == nil {
		key = have.Key
	}
	return AskRun{
		Argv: argv,
		Does: []string{
			fmt.Sprintf("links the yard %s as the peer %s with the key %s, %s.", inv.Name, name, inv.Key, called),
			fmt.Sprintf("The link carries nothing until yardr peer allow %s says what.", name),
		},
		Back:   runCommandLine([]string{"peer", "rm", name}),
		BackBy: "only the operator",
		Seen:   map[string]string{"peer " + name: key},
	}, nil
}

// peerAddStale says why the ask seq for peer add <name> <invite> is
// approved no more: a peer was added under the name since the ask was
// written, so the name the link would take is taken.
func (w *wtx) peerAddStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if _, err := loadPeer(w.Tx, name); err == nil {
		e := lastEvent(w.Tx, "peer_added", naming("peer", name))
		return fmt.Errorf("ask %d was written when no peer was added under %s: one was added since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runPeerAdd links the peer as actor through addPeer, and answers what the
// command prints at a terminal: the link with what it calls itself.
func runPeerAdd(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	inv, err := yardkey.ParseInvite(argv[3])
	if err != nil {
		return "", err
	}
	p, err := w.addPeer(ctx, Peer{Name: argv[2], Key: inv.Key, URL: inv.URL, Calls: inv.Name}, actor, by)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("peer %s added (it calls itself %s)", p.Name, p.Calls), nil
}
