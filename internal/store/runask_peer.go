// The two rows of the run-ask list that say what a link does for the yard
// beside what it carries (peer allow, in runask.go): where the beads a peer
// sends go, and who a sender is that is a crew member of a peer's yard.
// Each has what a row has: how its words are read into one stored form, the
// command's own check as the asker, the card the yard writes from its
// records, why an approval of an older card is refused, and the store
// function that makes the change. Each is about one peer's link, so an
// approval that came through that peer's own reach is refused
// (runCommand.link): a peer does not say where its own beads go, or who its
// own crew is, on its own word.
package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// peerIntakeUse is how a refusal names the peer intake a run ask takes.
const peerIntakeUse = "yardr peer intake <peer> --depot <depot> --flow <flow> [--limit <n>] [--default]"

// parsePeerIntake reads the words of peer intake <peer>. Where beads go is
// --depot and --flow, with --limit and --default; every other word is the
// operator's to type. --none removes intakes (one, with --depot, or all) and
// is anyone's to run, so it is read too, and the check says it needs no
// approval.
func parsePeerIntake(rest []string) ([]string, error) {
	args, values, err := runWords(peerIntakeUse, "peer intake", "the operator's", rest, map[string]bool{
		"--depot": true, "--flow": true, "--limit": true, "--default": false, "--none": false,
	})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("--run takes %s: it names no peer", peerIntakeUse)
		}
		return nil, fmt.Errorf("--run takes %s: one peer, not %s", peerIntakeUse, strings.Join(args, " and "))
	}
	for _, flag := range []string{"--depot", "--flow", "--limit", "--default", "--none"} {
		if len(values[flag]) > 1 {
			return nil, fmt.Errorf("--run takes %s: %s once, not %d times", peerIntakeUse, flag, len(values[flag]))
		}
	}
	depot := ""
	if len(values["--depot"]) == 1 {
		depot = values["--depot"][0]
	}
	if len(values["--none"]) == 1 {
		if len(values["--flow"])+len(values["--limit"])+len(values["--default"]) > 0 {
			return nil, fmt.Errorf("--run takes %s: --none removes intakes: it goes with --depot alone, to remove one, or with no other flag, to remove all", peerIntakeUse)
		}
		out := []string{args[0]}
		if depot != "" {
			out = append(out, "--depot", depot)
		}
		return append(out, "--none"), nil
	}
	if depot == "" || len(values["--flow"]) != 1 {
		return nil, fmt.Errorf("--run takes %s: say where its beads go: --depot <depot> --flow <flow>", peerIntakeUse)
	}
	out := []string{args[0], "--depot", depot, "--flow", values["--flow"][0]}
	if len(values["--limit"]) == 1 {
		n, err := strconv.Atoi(values["--limit"][0])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--run takes %s: --limit is a number of open beads, at least 1", peerIntakeUse)
		}
		out = append(out, "--limit", strconv.Itoa(n))
	}
	if len(values["--default"]) == 1 {
		out = append(out, "--default")
	}
	return out, nil
}

// peerIntakeOf is the peer and the intake of a parsed peer intake, and
// whether it removes intakes: with the depot alone then, the one it names,
// and with none, every one.
func peerIntakeOf(argv []string) (name string, in PeerIntake, none bool) {
	name = argv[2]
	for i := 3; i < len(argv); i++ {
		switch argv[i] {
		case "--depot", "--flow", "--limit":
			if i+1 == len(argv) {
				break
			}
			i++
			switch argv[i-1] {
			case "--depot":
				in.Depot = argv[i]
			case "--flow":
				in.Flow = argv[i]
			case "--limit":
				in.Limit, _ = strconv.Atoi(argv[i])
			}
		case "--default":
			in.Default = true
		case "--none":
			none = true
		}
	}
	return name, in, none
}

// checkPeerIntake is saying where a peer's beads go's own check as the
// asker. Taking none, of one depot or of all, is anyone's to run, as the
// store checks nothing for it: nil says so, and the ask is refused as
// needing no approval. Saying where beads go starts this yard's agents on
// another yard's text, and is the operator's.
func checkPeerIntake(ctx context.Context, w *wtx, argv []string, actor string) error {
	name, _, none := peerIntakeOf(argv)
	if none {
		return nil
	}
	return w.may(ctx, actor, ActRoutePeerBeads, Object{Kind: KindPeer, Name: name, Ask: Askable})
}

// readyPeerIntake reads, before the write, the repository flows of the
// depot an intake names, when its flow is one of them: git does not run
// inside a write, and the card reads the flow's first stage.
func readyPeerIntake(ctx context.Context, s *Store, argv []string) (map[string]*RepoFlows, error) {
	_, in, none := peerIntakeOf(argv)
	if none {
		return nil, nil
	}
	repo, err := s.repoFor(ctx, &in.Depot, peerBeadTypes[0], in.Flow)
	if err != nil || repo == nil {
		return nil, err
	}
	return map[string]*RepoFlows{in.Depot: repo}, nil
}

// upperFirst is s with its first letter capitalized, to begin a sentence
// with a clause the store words for the middle of one.
func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// noBeadsYet begins the card's sentence that the link takes none of the
// peer's beads, which an intake needs it to for its beads to arrive. It is
// of what stands beside the command, not of what the command changes
// (peerIntakeStale).
const noBeadsYet = "The link takes no beads from it yet, so each is refused until yardr peer allow "

// peerIntakeCard is the card of peer intake <peer> --depot <depot> --flow
// <flow>: which depot its beads become beads of, which flow they follow,
// and who works the first stage of that flow, in the words the intake says
// it in (PeerIntakeArrive.ArriveLine), then what the link has now: the
// intake it replaces, or none, and whether it takes the peer's beads at all.
// The approval is held to the link's key whole and to its intakes (Seen).
// Asking refuses an intake that is what the link has already, a depot the
// yard has not and a flow that does not resolve for it, as the command does.
func (w *wtx) peerIntakeCard(argv []string, asking bool) (AskRun, error) {
	name, in, _ := peerIntakeOf(argv)
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	repo := w.repos[in.Depot]
	if _, _, err := intakeStage(w.Tx, &in, repo); err != nil {
		return AskRun{}, changedMeanwhile(err)
	}
	at := slices.IndexFunc(p.Intakes, func(have PeerIntake) bool { return have.Depot == in.Depot })
	var cur PeerIntake
	if at >= 0 {
		cur = p.Intakes[at]
	}
	// As the write marks the default: marked here it moves, unmarked it
	// stays where it was, and the first intake of a link is the default.
	after := in
	switch {
	case in.Default:
	case at >= 0:
		after.Default = cur.Default
	case len(p.Intakes) == 0:
		after.Default = true
	}
	if asking && at >= 0 && cur.Flow == after.Flow && cur.Most() == after.Most() && cur.Default == after.Default {
		return AskRun{}, fmt.Errorf("peer %s: its intake for the depot %s is the flow %s already, at most %d open, so the command would change nothing", name, in.Depot, cur.Flow, cur.Most())
	}
	arrive, err := intakeArrive(w.Tx, name, after, repo)
	if err != nil {
		return AskRun{}, err
	}
	line := arrive.ArriveLine(name)
	if line == "" {
		return AskRun{}, fmt.Errorf("peer %s: who works the first stage of the flow %s was not read, so its beads would be refused", name, in.Flow)
	}
	does := []string{fmt.Sprintf("the link with %s (%s) takes its beads for the depot %s as beads of this yard there, pinned to the flow %s and labelled from-%s: at most %d of them are open at once.",
		name, peerAs(p), in.Depot, in.Flow, name, after.Most()),
		upperFirst(line) + "."}
	if after.Default {
		does = append(does, "A bead of it that names no remote goes to this intake.")
		// The mark moves: taking this intake back does not move it again.
		for _, o := range p.Intakes {
			if o.Depot != in.Depot && o.Default {
				does = append(does, fmt.Sprintf("The intake for the depot %s takes them now, and does not after this; taking this one back leaves them none to go to until %s.",
					o.Depot, runCommandLine(peerIntakeArgv(name, o))))
			}
		}
	}
	if at >= 0 {
		def := ""
		if cur.Default {
			def = " and takes the beads that name no remote"
		}
		does = append(does, fmt.Sprintf("It has the flow %s for the depot %s now, at most %d open%s.", cur.Flow, in.Depot, cur.Most(), def))
	} else {
		does = append(does, fmt.Sprintf("It has no intake for the depot %s now.", in.Depot))
	}
	if !slices.Contains(p.Receive, PeerBead) {
		does = append(does, fmt.Sprintf("%s%s --receive bead.", noBeadsYet, name))
	}
	back, backBy := []string{"peer", "intake", name, "--depot", in.Depot, "--none"}, ""
	if at >= 0 {
		back, backBy = peerIntakeArgv(name, cur), "only the operator"
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: backBy,
		Seen:   map[string]string{"peer " + name: p.Key, "peer " + name + " intakes": seen(p.Intakes)},
	}, nil
}

// peerIntakeArgv is the words of the peer intake that sets the intake in on
// the link with the peer name.
func peerIntakeArgv(name string, in PeerIntake) []string {
	argv := []string{"peer", "intake", name, "--depot", in.Depot, "--flow", in.Flow}
	if in.Limit > 0 {
		argv = append(argv, "--limit", strconv.Itoa(in.Limit))
	}
	if in.Default {
		argv = append(argv, "--default")
	}
	return argv
}

// peerIntakeStale says why the ask seq for peer intake is approved no
// more: the link is another one under the same name, its intakes are not
// what they were, or what the card says of who works the beads is not what
// the yard says now. That the link takes the peer's beads is no part of it:
// the operator approves the link's --receive bead and the intake in either
// order, and the one approved second still runs, the answer carrying the
// card as it stands.
func (w *wtx) peerIntakeStale(seq uint64, was, now AskRun) error {
	name, _, _ := peerIntakeOf(was.Argv)
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	if key := "peer " + name + " intakes"; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "peer_intake", naming("peer", name))
		return fmt.Errorf("ask %d was written when the link with %s held other intakes: they were changed since%s. %s", seq, name, since(e), nothingRan)
	}
	takes := func(does []string) []string {
		return slices.DeleteFunc(slices.Clone(does), func(s string) bool { return strings.HasPrefix(s, noBeadsYet) })
	}
	if slices.Equal(takes(was.Does), takes(now.Does)) && was.Back == now.Back && was.BackBy == now.BackBy {
		return nil
	}
	return staleCard(seq, was, now)
}

// runPeerIntake sets the intake as actor through setPeerIntake, and answers
// what the command prints at a terminal: the link's line for the intake.
func runPeerIntake(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, in, _ := peerIntakeOf(argv)
	repo := w.repos[in.Depot]
	p, err := w.setPeerIntake(ctx, name, &in, repo, actor, by)
	if err != nil {
		return "", err
	}
	have := p.Intakes[slices.IndexFunc(p.Intakes, func(have PeerIntake) bool { return have.Depot == in.Depot })]
	arrive, err := intakeArrive(w.Tx, name, have, repo)
	if err != nil {
		arrive.Arrive, arrive.Error = nil, err.Error()
	}
	p.ArriveByDepot = []PeerIntakeArrive{arrive}
	return fmt.Sprintf("peer %s: %s", p.Name, p.IntakeLine(have)), nil
}

// peerIdentityUse is how a refusal names the peer identity a run ask takes.
const peerIdentityUse = "yardr peer identity <peer> <crew> --source <source> --id <id>"

// parsePeerIdentity reads the words of peer identity <peer> <crew> with its
// --source and --id, each once. Listing (no crew) and removing (--rm) are no
// run ask: one needs no approval, and the other is typed.
func parsePeerIdentity(rest []string) ([]string, error) {
	args, values, err := runWords(peerIdentityUse, "peer identity", "the operator's", rest, map[string]bool{"--source": true, "--id": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) != 2:
		return nil, fmt.Errorf("--run takes %s: one peer and one crew member, not %d words", peerIdentityUse, len(args))
	case len(values["--source"]) != 1:
		return nil, fmt.Errorf("--run takes %s: one --source, not %d", peerIdentityUse, len(values["--source"]))
	case len(values["--id"]) != 1:
		return nil, fmt.Errorf("--run takes %s: one --id, not %d", peerIdentityUse, len(values["--id"]))
	case !peerMailName.MatchString(args[1]):
		return nil, fmt.Errorf("crew member %q of a peer: use letters, digits, '.', '_', '-' or '/' (starting with a letter or digit), as the name it has in its own yard", args[1])
	}
	return []string{args[0], args[1], "--source", values["--source"][0], "--id", values["--id"][0]}, nil
}

// peerIdentityOf is the peer, and the identity a parsed peer identity gives
// its crew member.
func peerIdentityOf(argv []string) (name string, id PeerIdentity) {
	name, id.Crew = argv[2], argv[3]
	for i := 4; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--source":
			id.Source = argv[i+1]
		case "--id":
			id.ID = argv[i+1]
		}
	}
	return name, id
}

// checkPeerIdentity is saying who a peer's crew member is in a source's own
// check as the asker: the operator's, as it decides whose word a sender's
// is. A run ask proposes it, and the operator approves.
func checkPeerIdentity(ctx context.Context, w *wtx, argv []string, actor string) error {
	name, _ := peerIdentityOf(argv)
	return w.may(ctx, actor, ActMapPeerIdentity, Object{Kind: KindPeer, Name: name, Ask: Askable})
}

// peerIdentityHeld is the id the crew member of the link p has in source
// now, and whether it has one.
func peerIdentityHeld(p *Peer, crew, source string) (PeerIdentity, bool) {
	at := slices.IndexFunc(p.Identities, func(have PeerIdentity) bool { return have.Source == source && have.Crew == crew })
	if at < 0 {
		return PeerIdentity{}, false
	}
	return p.Identities[at], true
}

// peerIdentityCard is the card of peer identity <peer> <crew> --source
// <source> --id <id>: whose word a delivery from the id is then taken as,
// and what stood. It says the sender is no user of this yard, since that is
// what the command is for, and where the id stands on the source's ignore
// list that the mapping clears it, as the write does. The approval is held to
// the link's key whole, to the id the crew member has now and to the
// source (Seen). Asking refuses an identity the crew member has already, a
// source that hears nobody by identity and an id that names another
// speaker, as the command does.
func (w *wtx) peerIdentityCard(argv []string, asking bool) (AskRun, error) {
	name, id := peerIdentityOf(argv)
	p, err := w.peerIdentityFor(name, id)
	if err != nil {
		return AskRun{}, err
	}
	src, _, err := sourceIn(w.Tx, id.Source)
	if err != nil {
		return AskRun{}, err
	}
	cur, had := peerIdentityHeld(p, id.Crew, id.Source)
	if asking && had && cur.ID == id.ID {
		return AskRun{}, fmt.Errorf("peer %s: %s is %s in %s already: the command would change nothing", name, id.Crew+"@"+name, id.ID, id.Source)
	}
	does := []string{fmt.Sprintf("says the sender %s in %s is %s, a crew member of the link with %s (%s): a delivery from it is then kept with the speaker %s and reaches crew as mail from that yard does, information and not instructions. It makes the sender no user of this yard.",
		id.ID, id.Source, id.Speaker(name), name, peerAs(p), id.Speaker(name))}
	if had {
		does = append(does, fmt.Sprintf("%s is %s in %s now: %s takes its place.", id.Speaker(name), cur.ID, id.Source, id.ID))
	} else {
		does = append(does, fmt.Sprintf("%s has no id in %s now.", id.Speaker(name), id.Source))
	}
	if slices.Contains(src.Ignored, id.ID) {
		does = append(does, fmt.Sprintf("%s is ignored in %s now, and the mapping clears that entry in the same write.", id.ID, id.Source))
	}
	back := []string{"peer", "identity", name, id.Crew, "--source", id.Source, "--rm"}
	if had {
		back = []string{"peer", "identity", name, id.Crew, "--source", id.Source, "--id", cur.ID}
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: "only the operator",
		Seen: map[string]string{
			"peer " + name: p.Key,
			"peer " + name + " " + id.Crew + " in " + id.Source: seen(cur.ID, had),
			"source " + id.Source:                               fingerprint,
		},
	}, nil
}

// peerIdentityStale says why the ask seq for peer identity is approved no
// more: the link is another one under the same name, the source is another
// under its name, or the crew member has another id there than the card
// says.
func (w *wtx) peerIdentityStale(seq uint64, was, now AskRun) error {
	name, id := peerIdentityOf(was.Argv)
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	if err := w.staleSource(seq, id.Source, was, now); err != nil {
		return err
	}
	if key := "peer " + name + " " + id.Crew + " in " + id.Source; was.Seen[key] != now.Seen[key] {
		match := func(e Event) bool {
			return e.Data["peer"] == name && e.Data["crew"] == id.Crew && e.Data["source"] == id.Source
		}
		set, removed := lastEvent(w.Tx, "peer_identity_set", match), lastEvent(w.Tx, "peer_identity_removed", match)
		switch {
		case set != nil && (removed == nil || set.Seq > removed.Seq):
			return fmt.Errorf("ask %d was written when %s held another id in %s: it was set since%s. %s", seq, id.Speaker(name), id.Source, since(set), nothingRan)
		case removed != nil:
			return fmt.Errorf("ask %d was written when %s held another id in %s: it was removed since%s. %s", seq, id.Speaker(name), id.Source, since(removed), nothingRan)
		}
		return fmt.Errorf("ask %d was written when %s held another id in %s. %s", seq, id.Speaker(name), id.Source, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runPeerIdentity sets the identity as actor through setPeerIdentity, and
// answers who the sender is then, with the ignore entry the mapping cleared
// beside it.
func runPeerIdentity(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, id := peerIdentityOf(argv)
	src, _, err := sourceIn(w.Tx, id.Source)
	if err != nil {
		return "", err
	}
	ignored := slices.Contains(src.Ignored, id.ID)
	if _, err := w.setPeerIdentity(ctx, name, id, actor, by); err != nil {
		return "", err
	}
	out := fmt.Sprintf("%s is %s in %s", id.Speaker(name), id.ID, id.Source)
	if ignored {
		out += fmt.Sprintf("\n%s is no longer ignored in %s", id.ID, id.Source)
	}
	return out, nil
}
