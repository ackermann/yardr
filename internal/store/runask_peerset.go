// The peer set row of the run-ask list (runList): moving where this yard
// calls a peer. It has what a row has: how its words are read into one
// stored form, the command's own check as the asker, the card the yard
// writes from its records, why an approval of an older card is refused, and
// the store function that makes the change. It is about one peer's link, so
// an approval that came through that peer's own reach is refused
// (runCommand.link): a peer does not say where its own link is called on its
// own word.
//
// The row is registered here, beside its functions, as the call row is
// (runask_call.go), and not in runList's literal: runask.go stands at the
// file-size ceiling the test gate holds (.yardr/test).
package store

import (
	"context"
	"fmt"
	"strings"
)

// peerSetUse is how a refusal names the peer set a run ask takes.
const peerSetUse = "yardr peer set <name> --url <url>"

func init() {
	runList = append(runList, runCommand{
		words:       []string{"peer", "set"},
		use:         peerSetUse,
		description: "Change where this yard calls a peer, or make it one that is never called",
		parse:       parsePeerSet,
		check:       checkPeerSet,
		card:        (*wtx).peerSetCard,
		stale:       (*wtx).peerSetStale,
		weigh:       peerSetWeight,
		run:         runPeerSet,
		link:        func(argv []string) ([]string, string) { return []string{argv[2]}, "say where its own link is called" },
	})
}

// parsePeerSet reads the words of peer set <name> --url <url>: the peer and
// the address it is called at. The address is kept in the one form the
// command keeps it in (cleanURL), and one that form refuses is refused here
// in its words. Taking the address away (--url "") is no word of a command
// that is split at spaces and nowhere else: it stays in a terminal.
func parsePeerSet(rest []string) ([]string, error) {
	args, values, err := runWords(peerSetUse, "peer set", "the operator's", rest, map[string]bool{"--url": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) == 0:
		return nil, fmt.Errorf("--run takes %s: it names no peer", peerSetUse)
	case len(args) > 1:
		return nil, fmt.Errorf("--run takes %s: one peer, not %s", peerSetUse, strings.Join(args, " and "))
	case len(values["--url"]) == 0:
		return nil, fmt.Errorf("--run takes %s: it names no address", peerSetUse)
	case len(values["--url"]) > 1:
		return nil, fmt.Errorf("--run takes %s: --url once", peerSetUse)
	}
	p := Peer{Name: args[0], URL: values["--url"][0]}
	if err := p.cleanURL(); err != nil {
		return nil, err
	}
	return []string{args[0], "--url", p.URL}, nil
}

// checkPeerSet is moving a peer's address's own check as the asker.
func checkPeerSet(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.may(ctx, actor, ActChangePeerAddress, Object{Kind: KindPeer, Name: argv[2], Ask: Askable})
}

// peerSetCard is the card of peer set <name> --url <url>: the address the
// link will be called at and the one it is called at now (or that it is
// never called), each whole, and that the link stays what it is while what
// waits for it is posted to the new address. What waits is not counted,
// since the count moves with every message and the card would go stale at
// once. The command that takes it back is the same command with the address
// the link has now. The approval is held to the link's key and to its
// address (Seen). Asking refuses the address the link has already.
func (w *wtx) peerSetCard(argv []string, asking bool) (AskRun, error) {
	name, to := argv[2], argv[4]
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	if asking && p.URL == to {
		return AskRun{}, fmt.Errorf("peer %s is called at %s already, so the command would change nothing", name, to)
	}
	// A link that is never called is taken back to that in a terminal alone:
	// no word of a run ask holds an empty address.
	now, back := "It is never called now: it calls this yard and asks for what waits here.", `""`
	if p.URL != "" {
		now, back = "It is called at "+p.URL+" now.", p.URL
	}
	return AskRun{
		Argv: argv,
		Does: []string{
			fmt.Sprintf("the link with %s (%s) will be called at %s. %s", name, peerAs(p), to, now),
			"The link stays what it is: its key, what it carries and what waits for it, which is posted to the new address at its next try. The other yard hears nothing of the move.",
		},
		Back:   runCommandLine([]string{"peer", "set", name, "--url", back}),
		BackBy: "only the operator",
		Seen: map[string]string{
			"peer " + name:              p.Key,
			"peer " + name + " address": seen(p.URL),
		},
	}, nil
}

// peerAddressStale says why the ask seq about the peer name is approved no
// more when the link is called at another address than when the card was
// written, and nil when it is called at the same.
func (w *wtx) peerAddressStale(seq uint64, name string, was, now AskRun) error {
	key := "peer " + name + " address"
	if was.Seen[key] == now.Seen[key] {
		return nil
	}
	e := lastEvent(w.Tx, "peer_url_set", naming("peer", name))
	return fmt.Errorf("ask %d was written when the link with %s was called at another address: it was set since%s. %s", seq, name, since(e), nothingRan)
}

// peerSetStale says why the ask seq for peer set <name> --url <url> is
// approved no more: the link is another one under the same name, or it is
// called at another address than when the card was written.
func (w *wtx) peerSetStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	if err := w.peerAddressStale(seq, name, was, now); err != nil {
		return err
	}
	return staleCard(seq, was, now)
}

// runPeerSet moves the peer as actor through setPeerURL, and answers what
// the command prints at a terminal, without the line about the daemon that
// only the terminal's yard can say.
func runPeerSet(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	p, err := w.setPeerURL(ctx, argv[2], argv[4], actor, by)
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("peer %s is called at %s now", p.Name, p.URL)
	if p.Waiting > 0 {
		out += fmt.Sprintf("\n%d messages that waited for it still wait, and are posted there, each at its next try", p.Waiting)
	}
	return out, nil
}
