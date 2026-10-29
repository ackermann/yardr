// The peer allow row of the run-ask list (runList): what a peer may send
// and receive, who it shows itself to, and the mail it takes. It has what
// a row has: how its words are read into one stored form, the command's
// own check as the asker, the card the yard writes from its records, why
// an approval of an older card is refused, and the store function that
// makes the change.
package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// peerAllowChange is a parsed peer allow: the kinds it carries each way
// and stops, who it shows and stops showing, and the limit it sets.
type peerAllowChange struct {
	name                              string
	send, receive, nosend, noreceive  []string
	expose, noexpose, trust, notreach []string
	notrust, reach                    []string
	limit                             int
	hasLimit                          bool
}

// peerAllowOf reads a peer allow in its stored form: the peer, then each
// flag with its value.
func peerAllowOf(argv []string) peerAllowChange {
	al := peerAllowChange{name: argv[2]}
	for i := 3; i+1 < len(argv); i += 2 {
		switch value := argv[i+1]; argv[i] {
		case "--send":
			al.send = append(al.send, value)
		case "--receive":
			al.receive = append(al.receive, value)
		case "--no-send":
			al.nosend = append(al.nosend, value)
		case "--no-receive":
			al.noreceive = append(al.noreceive, value)
		case "--expose":
			al.expose = append(al.expose, value)
		case "--no-expose":
			al.noexpose = append(al.noexpose, value)
		case "--trust":
			al.trust = append(al.trust, value)
		case "--no-trust":
			al.notrust = append(al.notrust, value)
		case "--reach":
			al.reach = append(al.reach, value)
		case "--no-reach":
			al.notreach = append(al.notreach, value)
		case "--mail-limit":
			al.limit, _ = strconv.Atoi(value)
			al.hasLimit = true
		}
	}
	return al
}

// peerAllowUse is how a refusal names the peer allow a run ask takes.
const peerAllowUse = "--run takes yardr peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>"

// parsePeerAllow reads the words of peer allow <peer> with the flags a
// run ask takes: the kinds it carries each way and stops, and the limit.
// Who the peer sees and is trusted and reached as is read too, and
// refused as the operator's alone by the check, not here. Narrowing one
// of those is anyone's, and is run directly. The limit and the kinds are
// two asks: what each changes is carded apart.
func parsePeerAllow(rest []string) ([]string, error) {
	args, values, err := runWords(peerAllowUse, "peer allow", "the operator's", rest, map[string]bool{
		"--send": true, "--receive": true, "--no-send": true, "--no-receive": true,
		"--expose": true, "--no-expose": true, "--trust": true, "--no-trust": true,
		"--reach": true, "--no-reach": true, "--mail-limit": true,
	})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("%s: it names no peer", peerAllowUse)
		}
		return nil, fmt.Errorf("%s: one peer, not %s", peerAllowUse, strings.Join(args, " and "))
	}
	for _, flag := range []string{"--no-expose", "--no-trust", "--no-reach"} {
		if len(values[flag]) > 0 {
			return nil, fmt.Errorf("%s: %s narrows one, and is run directly", peerAllowUse, flag)
		}
	}
	limit := ""
	if len(values["--mail-limit"]) > 1 {
		return nil, fmt.Errorf("%s: --mail-limit once", peerAllowUse)
	} else if len(values["--mail-limit"]) == 1 {
		limit = values["--mail-limit"][0]
	}
	for _, flag := range []string{"--send", "--receive", "--no-send", "--no-receive", "--expose", "--trust", "--reach"} {
		if limit != "" && len(values[flag]) > 0 {
			return nil, fmt.Errorf("%s, and peer allow with --mail-limit and %s is two asks: propose the limit and the rest apart", peerAllowUse, flag)
		}
	}
	changed := limit != ""
	for flag, vs := range values {
		if flag != "--mail-limit" && len(vs) > 0 {
			changed = true
		}
	}
	if !changed {
		return nil, fmt.Errorf("%s: it says nothing to change", peerAllowUse)
	}
	n := 0
	if limit != "" {
		var err error
		if n, err = strconv.Atoi(limit); err != nil {
			return nil, fmt.Errorf("%s: %q is no number", peerAllowUse, limit)
		}
		if n < 1 || n > MaxPeerMailLimit {
			return nil, fmt.Errorf("peer %s: a link takes 1 to %d mails an hour, not %d", args[0], MaxPeerMailLimit, n)
		}
	}
	split := func(vs []string) []string {
		var out []string
		for _, v := range vs {
			out = append(out, strings.Split(v, ",")...)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	send, receive := split(values["--send"]), split(values["--receive"])
	nosend, noreceive := split(values["--no-send"]), split(values["--no-receive"])
	if err := checkKinds(send, receive, nosend, noreceive); err != nil {
		return nil, err
	}
	out := []string{args[0]}
	for _, fv := range []struct {
		flag string
		vs   []string
	}{
		{"--send", send}, {"--receive", receive}, {"--no-send", nosend}, {"--no-receive", noreceive},
		{"--expose", split(values["--expose"])}, {"--trust", split(values["--trust"])}, {"--reach", split(values["--reach"])},
	} {
		for _, v := range fv.vs {
			out = append(out, fv.flag, v)
		}
	}
	if limit != "" {
		out = append(out, "--mail-limit", strconv.Itoa(n))
	}
	return out, nil
}

// checkPeerAllow is the peer allow's own checks as the asker, shared with
// AllowPeer so the same command has the same operator-only refusal.
func checkPeerAllow(ctx context.Context, w *wtx, argv []string, actor string) error {
	al := peerAllowOf(argv)
	_, err := w.checkAllowPeer(ctx, al.name, peerAllowPermission{
		send: al.send, receive: al.receive,
		expose: len(al.expose) > 0, trust: len(al.trust) > 0, reach: len(al.reach) > 0,
		mailLimit: al.limit,
	}, actor)
	return err
}

// peerAs names the peer as the yard knows it, with what it calls itself
// and the start of its key, since two peers may have names that look
// alike.
func peerAs(p *Peer) string {
	as := "key " + keyStart(p.Key)
	if p.Calls != "" {
		as = "it calls itself " + p.Calls + ", " + as
	}
	return as
}

// kindsSay is the kinds a link carries one way as a card names them:
// "nothing" for none.
func kindsSay(kinds []string) string { return or(strings.Join(kinds, ", "), "nothing") }

// peerAllowCard is the card of peer allow: the limit it sets, and the
// kinds it carries each way and stops, with what the link carries in and
// out after the command beside what it carries now. The peer is named with
// what it calls itself and the start of its key, and the approval is held
// to the key whole and to what the link carries (Seen). Asking refuses a
// limit the link has already, and one below it: to take less mail is
// anyone's, and needs no approval. It refuses kinds that change nothing the
// same way.
func (w *wtx) peerAllowCard(argv []string, asking bool) (AskRun, error) {
	al := peerAllowOf(argv)
	p, err := loadPeer(w.Tx, al.name)
	if err != nil {
		return AskRun{}, err
	}
	var does []string
	back := []string{"peer", "allow", al.name}
	backBy := ""
	if al.hasLimit {
		has := p.MailsAnHour()
		switch {
		case !asking:
		case al.limit == has:
			return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour already, so the command would change nothing", al.name, has)
		case al.limit < has:
			return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour, and to take less needs no approval: anyone runs yardr peer allow %s --mail-limit %d", al.name, has, al.name, al.limit)
		}
		does = append(does, fmt.Sprintf("the link with %s (%s) takes %d mails and asks an hour from it. It takes %d now.", al.name, peerAs(p), al.limit, has))
		back = append(back, "--mail-limit", strconv.Itoa(has))
	}
	afterSend := changeList(p.Send, al.send, al.nosend)
	afterReceive := changeList(p.Receive, al.receive, al.noreceive)
	addedSend := slices.DeleteFunc(slices.Clone(afterSend), func(k string) bool { return slices.Contains(p.Send, k) })
	addedReceive := slices.DeleteFunc(slices.Clone(afterReceive), func(k string) bool { return slices.Contains(p.Receive, k) })
	removedSend := slices.DeleteFunc(slices.Clone(p.Send), func(k string) bool { return slices.Contains(afterSend, k) })
	removedReceive := slices.DeleteFunc(slices.Clone(p.Receive), func(k string) bool { return slices.Contains(afterReceive, k) })
	kinds := len(al.send)+len(al.receive)+len(al.nosend)+len(al.noreceive) > 0
	if asking && kinds && len(addedSend)+len(addedReceive)+len(removedSend)+len(removedReceive) == 0 {
		return AskRun{}, fmt.Errorf("peer %s: the link carries %s to it and takes %s from it already, so the command would change nothing", al.name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"))
	}
	if kinds {
		// What the link carries each way after the command and before it,
		// whole, so the person reads the link as it will be beside the
		// link as it is, then which kinds the command adds and stops.
		does = append(does, fmt.Sprintf("the link with %s (%s) will carry %s to it and take %s from it. It carries %s to it and takes %s from it now.",
			al.name, peerAs(p),
			kindsSay(afterSend), kindsSay(afterReceive),
			kindsSay(p.Send), kindsSay(p.Receive)))
		var delta []string
		if len(addedSend) > 0 {
			delta = append(delta, "adds "+strings.Join(addedSend, ", ")+" to what it carries to it")
		}
		if len(addedReceive) > 0 {
			delta = append(delta, "adds "+strings.Join(addedReceive, ", ")+" to what it takes from it")
		}
		if len(removedSend) > 0 {
			delta = append(delta, "stops carrying "+strings.Join(removedSend, ", ")+" to it")
		}
		if len(removedReceive) > 0 {
			delta = append(delta, "stops taking "+strings.Join(removedReceive, ", ")+" from it")
		}
		// None left when the link changed since the card was written: the
		// approval is then refused as stale, in the card's first sentence.
		if len(delta) > 0 {
			does = append(does, "That "+andList(delta)+".")
		}
	}
	for _, k := range addedSend {
		back = append(back, "--no-send", k)
	}
	for _, k := range addedReceive {
		back = append(back, "--no-receive", k)
	}
	for _, k := range removedSend {
		back = append(back, "--send", k)
		backBy = "only the operator"
	}
	for _, k := range removedReceive {
		back = append(back, "--receive", k)
		backBy = "only the operator"
	}
	finger := map[string]string{"peer " + al.name: p.Key}
	if kinds {
		finger["peer "+al.name+" carries"] = seen(p.Send, p.Receive)
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: backBy,
		Seen:   finger,
	}, nil
}

// keyStart is the start of a key as a card shows it. It names the key to a
// person and proves nothing: what is compared is the key whole.
func keyStart(key string) string {
	if len(key) > 8 {
		return key[:8] + "…"
	}
	return key
}

// peerKeyStale says why the ask seq about the peer name is approved no
// more when the link is another one under the same name, and nil when it
// is the one the card was written from.
func (w *wtx) peerKeyStale(seq uint64, name string, was, now AskRun) error {
	const nothing = "Nothing ran. Decline it; it is asked again if it is still wanted."
	seen := "peer " + name
	if was.Seen[seen] == now.Seen[seen] {
		return nil
	}
	added := ""
	if p, err := loadPeer(w.Tx, name); err == nil {
		added = " (the link there now was added " + p.AddedAt.Local().Format("2 Jan 15:04") + ")"
	}
	return fmt.Errorf("ask %d was written when the link with %s was another: it was removed and made again since%s. %s", seq, name, added, nothing)
}

// mailLimitStale says why the ask seq for peer allow <peer> --mail-limit
// <n> is approved no more: the link is another one under the same name, or
// it takes another number of mails than the card says.
func (w *wtx) mailLimitStale(seq uint64, was, now AskRun) error {
	al := peerAllowOf(was.Argv)
	name, n := al.name, al.limit
	const nothing = "Nothing ran. Decline it; it is asked again if it is still wanted."
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	// The limit the card was written at is in the command that takes it
	// back, which is the same command with that number.
	had := peerAllowOf(strings.Fields(was.Back)[1:]).limit
	has := peerAllowOf(strings.Fields(now.Back)[1:]).limit
	switch {
	case had == has:
		return fmt.Errorf("ask %d says %q, and the yard would say %q now: the card is from before that changed. %s", seq, strings.Join(was.Does, " "), strings.Join(now.Does, " "), nothing)
	case has == n:
		return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d already: nothing is left to change. %s", seq, name, had, has, nothing)
	}
	return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d: the card is from before that changed. %s", seq, name, had, has, nothing)
}

// peerAllowStale says why the ask seq for peer allow is approved no more:
// the limit's own reasons when it sets one, and the key and the sentences
// when it carries kinds.
func (w *wtx) peerAllowStale(seq uint64, was, now AskRun) error {
	al := peerAllowOf(was.Argv)
	if al.hasLimit {
		return w.mailLimitStale(seq, was, now)
	}
	if err := w.peerKeyStale(seq, al.name, was, now); err != nil {
		return err
	}
	return staleCard(seq, was, now)
}

// runPeerAllow makes a peer allow as actor through allowPeer, and answers
// what the command prints at a terminal.
func runPeerAllow(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	al := peerAllowOf(argv)
	ch := PeerAllow{Send: al.send, Receive: al.receive, NoSend: al.nosend, NoReceive: al.noreceive}
	if al.hasLimit {
		ch.MailLimit = al.limit
	}
	p, err := w.allowPeer(ctx, al.name, ch, actor, by)
	if err != nil {
		return "", err
	}
	if al.hasLimit && len(al.send)+len(al.receive)+len(al.nosend)+len(al.noreceive) == 0 {
		return "peer " + p.Name + ": " + p.MailTakes(), nil
	}
	return fmt.Sprintf("peer %s: send %s; receive %s; expose %s", p.Name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"), or(strings.Join(p.Expose, ", "), "nobody")), nil
}
