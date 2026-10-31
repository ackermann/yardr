// Asks are questions as data: one question with enumerable options, asked
// of one person, living as a note of kind ask on any bead in any stage.
// The person answers by picking an option, puts the question aside, or
// declines it, each a note of kind answer; so is the asker taking the
// question back (TakeBackAsk). Both are JSON in Note.Text, one schema each,
// parsed the same everywhere; prime, review and chat render them as
// numbered text, never the raw form.
//
// An ask holds the question in one line, who must answer, two to five
// lines of context, one or more options each naming its consequence in a
// line, the one recommended and why, and the other beads it is about. An
// ask with no options wants words. yardr adds the option "You decide" to
// every ask that has options but a stage's card; the writer does not.
//
// An answer names its ask by the ask note's seq and holds either a pick
// (the option's number with any words), words alone (only on an ask with
// no options), Later (put aside: not open for 24 hours or until the
// asker's next note on the bead, whichever is first), or Decline (ends it
// with no option picked). A newer pick supersedes the older one for
// display; every pick stays as a note. TakeBack is not the person's: the
// asker, or the operator, ends an ask that is asked no more, with why.
//
// An ask may ask for a secret instead (secretask.go): its answer is the
// names that were given, written by GiveSecret alone, and the value is in
// no note. Or it may propose a command of the operator's (runask.go): its
// answer is the command the yard ran, written by ApproveAsk alone.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Note kinds for questions and their answers, beside note, journal and
// handoff.
const (
	NoteAsk    = "ask"
	NoteAnswer = "answer"
)

// YouDecide is the label of the option yardr adds to every ask that has
// options, a stage's card aside. The writer cannot write it: an option of
// that label, letter-case aside, is refused.
const YouDecide = "You decide"

// LaterFor is how long a put-aside ask stays out of the person's queue,
// unless the asker writes on the bead sooner.
const LaterFor = 24 * time.Hour

// AskOption is one option of an ask: its label and its consequence in a
// line each.
type AskOption struct {
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

// Ask is the JSON of a note of kind ask: one question as data.
type Ask struct {
	// Question is the question in one line, as a person would ask it.
	Question string `json:"question"`
	// To is who must answer, a user; empty is never stored (the command
	// defaults it to the yard's operator).
	To string `json:"to"`
	// Context is what the question is about, two to five lines, each one
	// line; at least one is stored.
	Context []string `json:"context"`
	// Options are the options in the order written, with YouDecide last
	// when the writer gave any, but on a stage's card. None wants words.
	Options []AskOption `json:"options,omitempty"`
	// Recommend is the recommended option's number, 1-based into Options,
	// 0 for no recommendation; Why says why in a line.
	Recommend int    `json:"recommend,omitempty"`
	Why       string `json:"why,omitempty"`
	// About names the other beads the question is about, by handle; the
	// ask lives on the one bead.
	About []string `json:"about,omitempty"`
	// Stage is the stage the ask was put for, when the yard put it on
	// entering a templated manual stage; empty on an ask a person asked.
	// Move is the outcome per stored option, frozen at ask time so a later
	// edit of the template cannot change what a standing ask does: the
	// entry for YouDecide is empty, and an input ask's single entry is its
	// move. Entry is the seq of the event that entered the bead into the
	// stage, the advanced event of the move that put it there, the created
	// event for a bead created in the stage, or the reopened event for a
	// bead reopened in it. A move entry is an outcome to report or, after
	// moveTo, a stage to advance to. Fallback marks an ask addressed to the
	// operator because no stage member could receive one; its recipient
	// remains an authorized answerer if the operator setting later changes.
	// Moves marks the card that offers the stage's moves (a moves template,
	// asktemplate.go): the yard gives it up for another ask put on the bead
	// (withdrawMoves), which no other stage ask does. All five are the
	// yard's own values: newAsk clears whatever a caller sends.
	Stage    string   `json:"stage,omitempty"`
	Move     []string `json:"move,omitempty"`
	Entry    uint64   `json:"entry,omitempty"`
	Fallback bool     `json:"fallback,omitempty"`
	Moves    bool     `json:"moves,omitempty"`
	// Secret makes the ask a secret ask: where its answer goes. Such an ask
	// has no options, no recommendation and no about-beads, is for the
	// operator (or, for a user's own address in a source, for that user),
	// and is answered by GiveSecret.
	Secret *AskSecret `json:"secret,omitempty"`
	// Run makes the ask a run ask: the one yardr command its approval runs.
	// Such an ask has no options, no recommendation and no about-beads, is
	// for the operator (or, for a command that is a user's own, for that
	// user), and is approved by ApproveAsk.
	Run *AskRun `json:"run,omitempty"`
	// Setup makes the ask a setup ask: the whole setup of a pack's
	// sources as one decision, the apply, the account and the grants.
	// Its options are the pack's presets, stored with the yard's
	// rendering of each, with YouDecide last; the writer sends the pack
	// and the grantees alone. It is for the operator, and is approved
	// by ApproveSetup with the preset pick and the account together.
	Setup *AskSetup `json:"setup,omitempty"`
	// DepotPush makes the ask a depot-push ask: turning one depot's
	// push to its origin on or off as one decision, carried out by the
	// server on approval. Such an ask has no options, no recommendation
	// and no about-beads, is for the operator, and is approved by
	// ApproveAsk as a run ask is (depotpush.go).
	DepotPush *AskDepotPush `json:"depot_push,omitempty"`
	// Identity makes the ask a mapping ask: who an unmapped sender in
	// a source hearing by identity is, asked of the operator when a
	// delivery from them is held under unknown. Its options are
	// the yard's users, each naming the identity its pick writes,
	// then someone new, whom the pick's words name, let in once, and
	// nobody, who is ignored, with YouDecide last; a pick writes in
	// the answering write (addAnswer) and delivers or deletes what
	// the card held. It is written by ClaimIdentity alone, from a
	// held delivery, never by AddAsk, and stays in the yard as a
	// run ask does.
	Identity *AskIdentity `json:"identity,omitempty"`
	// Weight is the card's weight (weight.go): the classes by which
	// approving it widens what reaches outside the yard, whose word or
	// credential counts as a person's, or what runs inside, each with the
	// yard's line. Empty on a routine card. It is the yard's own value,
	// weighed in the write that stores the ask from the card the yard
	// wrote: newAsk clears whatever a caller sends, as it clears Stage.
	Weight []AskWeight `json:"weight,omitempty"`
}

// AskIdentity is what a mapping ask maps: the source hearing by
// identity, the sender's id in it as the delivery carried it, and
// the channel it arrived in ("" where the envelope named none).
// Users are the yard's users as the card was written, in the order
// of the ask's options before someone new, nobody and YouDecide:
// picking option n writes Users[n-1]'s identity. The card holds the
// sender as unmapped when it was written; a sender mapped to anyone
// meanwhile makes its pick stale and nothing is written. Held is
// how many deliveries wait on the card (held.go), kept current by
// the write that holds or releases them; their bodies are never in
// the ask.
type AskIdentity struct {
	Source  string   `json:"source"`
	Sender  string   `json:"sender"`
	Channel string   `json:"channel,omitempty"`
	Users   []string `json:"users"`
	Held    int      `json:"held,omitempty"`
}

// IdentityAsked is what a mapping ask maps, as a client shows it:
// the source, the sender's id in it and the channel it arrived in,
// with how many held deliveries wait on the card.
type IdentityAsked struct {
	Source  string `json:"source"`
	Sender  string `json:"sender"`
	Channel string `json:"channel,omitempty"`
	Held    int    `json:"held,omitempty"`
}

// identityAsked is what the ask a maps, as a client shows it, nil
// for an ask that maps nobody.
func (a Ask) identityAsked() *IdentityAsked {
	if a.Identity == nil {
		return nil
	}
	return &IdentityAsked{Source: a.Identity.Source, Sender: a.Identity.Sender, Channel: a.Identity.Channel, Held: a.Identity.Held}
}

// Answer is the JSON of a note of kind answer: what one person did with
// one ask. Exactly one of a pick (Option), words alone (Words with no
// Option, Later, Decline or TakeBack), Later, Decline, Given, Ran and
// TakeBack is set, except a setup approval, which holds the picked preset
// (Option), the names given (Given) or the candidate taken (Account), and
// what the yard runs (Ran) together, written by ApproveSetup alone.
type Answer struct {
	// Ask is the ask note's seq.
	Ask uint64 `json:"ask"`
	// Option is the picked option's number, 1-based into the ask's
	// options, 0 for none.
	Option int `json:"option,omitempty"`
	// Words go with a pick, answer an ask with no options, or say why a
	// question is declined or taken back.
	Words string `json:"words,omitempty"`
	// Later puts the ask aside; Decline ends it with no option picked.
	Later   bool `json:"later,omitempty"`
	Decline bool `json:"decline,omitempty"`
	// Given are the names a secret ask was given values for: all the
	// answer says of them. Only GiveSecret writes it.
	Given []string `json:"given,omitempty"`
	// Account is the candidate a setup approval took instead of typing:
	// the picked source's name, "" for a typed account. Only ApproveSetup
	// writes it, with Ran.
	Account string `json:"account,omitempty"`
	// Ran is the command an approved run ask ran, with what it printed. Only
	// ApproveAsk writes it.
	Ran *AskRan `json:"ran,omitempty"`
	// TakeBack ends the ask as asked no more: the note's author is the asker
	// or the operator, never the person asked. Only TakeBackAsk writes it.
	TakeBack bool `json:"take_back,omitempty"`
}

// ParseAsk reads the ask a note of kind ask holds.
func ParseAsk(n Note) (Ask, error) {
	var a Ask
	if err := json.Unmarshal([]byte(n.Text), &a); err != nil {
		return Ask{}, fmt.Errorf("ask %d: %w", n.Seq, err)
	}
	return a, nil
}

// ParseAnswer reads the answer a note of kind answer holds.
func ParseAnswer(n Note) (Answer, error) {
	var a Answer
	if err := json.Unmarshal([]byte(n.Text), &a); err != nil {
		return Answer{}, fmt.Errorf("answer %d: %w", n.Seq, err)
	}
	return a, nil
}

// AddAsk stores ask on the bead id as a note of kind ask, written by
// actor, and answers the note's seq. It refuses an ask with no context, a
// context line holding a break, an option with no consequence, an option
// the writer called YouDecide, a recommendation that names no option or
// says no why, and an about-bead that does not exist. To must name who
// answers; the command defaults it.
//
// An ask with Secret asks the operator for a declared source's credential,
// or a user (or the operator for them) for that user's own address in a
// source that tells people. It is checked against the source's declaration
// in the write that stores it (askedSecret) and stored with the names and
// the host the declaration gives, whatever the writer sent for them. It
// takes no options, no recommendation and no about-beads, and nothing of it
// leaves the yard: a To of <person>@<peer> is refused, and no copy is sent
// to a name a link trusts as the one asked.
//
// An ask with Run proposes one command of the operator's, from a fixed
// list, for the operator to approve (ApproveAsk); one row of the list is a
// user's own, and is for that user or the operator. It is parsed and checked
// against the yard as it stands in the write that stores it (askedRun), and
// stored with the yard's own sentences for what the command changes. It
// takes no options, no recommendation and no about-beads, at most
// MaxRunContext lines of context, and is written by a crew member or a
// user, never by a bead's agent. Like a secret ask it stays in the yard: a
// To of <person>@<peer> is refused, and no copy is sent to a name with
// reach.
//
// An ask with Setup proposes the whole setup of a pack's sources as one
// decision, for the operator to approve with the preset pick and the
// account together (ApproveSetup). It is checked against the yard and the
// server's reading of the pack in the write that stores it (askedSetup),
// and stored with the yard's own sentences and its options, one per preset
// with YouDecide last. It takes a recommendation counting the presets, no
// options and no about-beads, at most MaxRunContext lines of context, and
// is written by a crew member or a user, never by a bead's agent. Like a
// run ask it stays in the yard: a To of <person>@<peer> is refused, and no
// copy is sent to a name with reach.
//
// A To of <person>@<peer> asks a person of a linked yard: in the same
// write that stores the ask, the yard queues one message of kind ask for
// the peer (peerAsk), where it stands among the person's decisions. The
// gates are mail's: the peer must be one, the link must carry ask out, a
// crew member asking must name the peer in its peers setting, and a bead's
// agent may not ask across. A refused ask stores nothing.
//
// An ask for a user of this yard is also sent, in the same write, to each
// name a link gives reach for that user (AskReach, peertrust.go): the same
// message, addressed to the name as its yard knows it, so it stands among
// the person's decisions there as well as here. The writer's gates are not
// asked, since the writer addressed a user of this yard and it is the
// operator's grant that sends the copy: a crew member without the peer and
// a bead's agent are copied too. A copy that cannot be queued is told to
// the person and refuses nothing. The copies stay in bAskCopies for
// late-answer checks, as does where an ask addressed across went.
// From a session's or a crew member's pane the ask is recorded as that
// session's actor or that crew member, whatever actor says; from anywhere
// else, as given (paneAuthor).
func (s *Store) AddAsk(ctx context.Context, id string, ask Ask, actor string) (uint64, error) {
	add, err := s.newAsk(ctx, ask, actor)
	if err != nil {
		return 0, err
	}
	var seq uint64
	err = s.write(func(w *wtx) error {
		seq, err = add(w, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// foldSpaces is a line with every run of spacing folded to one space, so
// no writer pads a line to the terminal's width: where the terminal
// breaks the card's line, the words after would continue at column 0, at
// the column of the yard's own lines. Fields splits on unicode.IsSpace,
// so the wide spaces fold too.
func foldSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// foldQuestion is the question with each of its lines folded, keeping its
// breaks: a question holds later lines, further in on the card.
func foldQuestion(q string) string {
	lines := strings.Split(q, "\n")
	for i := range lines {
		lines[i] = foldSpaces(lines[i])
	}
	return strings.Join(lines, "\n")
}

// newAsk checks ask as far as it can be checked outside a write and answers
// what stores it on the bead id inside one (AddAsk, AskNew).
func (s *Store) newAsk(ctx context.Context, ask Ask, actor string) (func(w *wtx, id string) (uint64, error), error) {
	// The yard's own values for a stage ask, whatever the writer sent, as
	// secret and run asks are stored with the yard's own values: a person
	// asking by hand puts no stage ask.
	ask.Stage, ask.Move, ask.Entry, ask.Fallback, ask.Moves = "", nil, 0, false, false
	ask.Weight = nil
	ask.Question = strings.TrimSpace(ask.Question)
	if ask.Question == "" {
		return nil, fmt.Errorf("an ask needs a question")
	}
	ask.Question = foldQuestion(ask.Question)
	ask.To = strings.TrimSpace(ask.To)
	if ask.To == "" {
		return nil, fmt.Errorf("an ask needs --to: who must answer")
	}
	var context []string
	for i, line := range ask.Context {
		// A context line is one line: a break in it would continue at
		// the column of the yard's own lines on the card. Check before
		// trimming so a leading or trailing break is refused too.
		if strings.Contains(line, "\n") {
			return nil, fmt.Errorf("--context line %d holds a line break: a context line is one line: give each line its own --context", i+1)
		}
		if line = strings.TrimSpace(line); line != "" {
			context = append(context, foldSpaces(line))
		}
	}
	if len(context) == 0 {
		return nil, fmt.Errorf("an ask needs --context: two to five lines of what it is about, at least one (e.g. --context \"<first line>\" --context \"<second line>\")")
	}
	ask.Context = context
	for i, o := range ask.Options {
		ask.Options[i].Label = strings.TrimSpace(o.Label)
		ask.Options[i].Consequence = strings.TrimSpace(o.Consequence)
		o = ask.Options[i]
		if o.Label == "" {
			return nil, fmt.Errorf("option %d needs a label: --option \"<label>: <consequence>\"", i+1)
		}
		if o.Consequence == "" {
			return nil, fmt.Errorf("option %d (%s) needs a consequence: --option %q", i+1, o.Label, o.Label+": <what picking it does>")
		}
		if strings.EqualFold(o.Label, YouDecide) {
			return nil, fmt.Errorf("option %d: yardr adds %q itself, to every ask with options: leave it out", i+1, YouDecide)
		}
	}
	// A setup ask's recommendation counts the pack's presets, which only
	// the write reads; askedSetup checks it there.
	if ask.Setup == nil {
		switch {
		case ask.Recommend != 0 && len(ask.Options) == 0:
			return nil, fmt.Errorf("--recommend %d: the ask has no options to recommend", ask.Recommend)
		case ask.Recommend < 0 || ask.Recommend > len(ask.Options):
			return nil, fmt.Errorf("--recommend %d: the ask has options 1..%d", ask.Recommend, len(ask.Options))
		case ask.Recommend != 0 && strings.TrimSpace(ask.Why) == "":
			return nil, fmt.Errorf("--recommend %d needs --why: why this option, in a line", ask.Recommend)
		case ask.Recommend == 0 && strings.TrimSpace(ask.Why) != "":
			return nil, fmt.Errorf("--why needs --recommend: which option it is for")
		}
	}
	ask.Why = strings.TrimSpace(ask.Why)
	// yardr adds YouDecide itself, in the write below, naming the actor
	// the pane bears out: the option says who is left to decide.
	var about []string
	for _, b := range ask.About {
		if b = strings.TrimSpace(b); b != "" {
			about = append(about, b)
		}
	}
	to, peer, outside := strings.Cut(ask.To, "@")
	var repos map[string]*RepoFlows
	if ask.Secret != nil {
		switch {
		case len(ask.Options) > 0 || ask.Recommend != 0:
			return nil, fmt.Errorf("a secret ask takes no --option and no --recommend: its answer is the value, typed where no agent is shown it")
		case len(about) > 0:
			return nil, fmt.Errorf("a secret ask takes no --about: it is about the source it names")
		case outside:
			return nil, fmt.Errorf("a secret is given in the yard that asks for it, by its operator or the user it is of: %s is of another yard, and nothing of a secret crosses a link", ask.To)
		}
	}
	if ask.Run != nil {
		switch {
		case ask.Secret != nil:
			return nil, fmt.Errorf("an ask asks for a secret or proposes a command to run, not both: --secret or --run")
		case len(ask.Options) > 0 || ask.Recommend != 0:
			return nil, fmt.Errorf("a run ask takes no --option and no --recommend: its answer is the operator's approval of the command, or a decline")
		case len(about) > 0:
			return nil, fmt.Errorf("a run ask takes no --about: it is about what its command names")
		case runContextLines(ask.Context) > MaxRunContext:
			return nil, fmt.Errorf("a run ask takes at most %d lines of --context, and this has %d: the command and what the yard says of it must stay in sight", MaxRunContext, runContextLines(ask.Context))
		case outside:
			return nil, fmt.Errorf("a command is approved in the yard that runs it, by its operator: %s is of another yard, and no approval crosses a link", ask.To)
		}
		// What can be refused without the yard's records is refused before
		// the write: a command no row reads. What names no row is told
		// apart inside, with the records: the asker's own to run, the
		// operator's alone, or none the yard runs from a card. A row whose
		// card reads git has it read here, before the write.
		c, argv, err := parseRow(ask.Run.Argv)
		if err != nil && err != errNotRow {
			return nil, err
		}
		if err == nil && c.ready != nil {
			if repos, err = c.ready(ctx, s, argv); err != nil {
				return nil, err
			}
		}
	}
	if ask.Setup != nil {
		switch {
		case ask.Secret != nil || ask.Run != nil:
			return nil, fmt.Errorf("an ask asks for a secret, proposes a command, or sets up a pack: --secret, --run or --setup, one of the three")
		case len(ask.Options) > 0:
			return nil, fmt.Errorf("a setup ask takes no --option: its options are the pack's presets, picked with the account")
		case len(about) > 0:
			return nil, fmt.Errorf("a setup ask takes no --about: it is about the pack it names")
		case runContextLines(ask.Context) > MaxRunContext:
			return nil, fmt.Errorf("a setup ask takes at most %d lines of --context, and this has %d: the pack and what the yard says of it must stay in sight", MaxRunContext, runContextLines(ask.Context))
		case outside:
			return nil, fmt.Errorf("a setup is approved in the yard that runs it, by its operator: %s is of another yard, and no approval crosses a link", ask.To)
		}
	}
	if ask.DepotPush != nil {
		switch {
		case ask.Secret != nil || ask.Run != nil || ask.Setup != nil:
			return nil, fmt.Errorf("an ask asks for a secret, proposes a command, sets up a pack, or turns a depot's push on or off: --secret, --run, --setup or --depot-push, one of the four")
		case len(ask.Options) > 0 || ask.Recommend != 0:
			return nil, fmt.Errorf("a depot-push ask takes no --option and no --recommend: its answer is the operator's approval, or a decline")
		case len(about) > 0:
			return nil, fmt.Errorf("a depot-push ask takes no --about: it is about the depot it names")
		case runContextLines(ask.Context) > MaxRunContext:
			return nil, fmt.Errorf("a depot-push ask takes at most %d lines of --context, and this has %d: the depot and what the yard says of it must stay in sight", MaxRunContext, runContextLines(ask.Context))
		case outside:
			return nil, fmt.Errorf("a depot-push card is approved in the yard that carries it out, by its operator: %s is of another yard, and no approval crosses a link", ask.To)
		}
	}
	if ask.Identity != nil {
		// A mapping ask is the yard's own, from a dropped delivery
		// (ClaimIdentity): no writer may invent the sender it maps.
		return nil, fmt.Errorf("a mapping ask is written by the yard from a dropped delivery, and proposed by nobody: map the sender with yardr user identity <user> --source %s --id %s", ask.Identity.Source, ask.Identity.Sender)
	}
	if outside {
		switch {
		case !peerMailName.MatchString(to):
			return nil, fmt.Errorf("ask to %q: that is no person's name", to)
		case !peerMailName.MatchString(actor):
			return nil, fmt.Errorf("ask from %q: a name of letters, digits, '.', '_', '-' and '/' is what another yard is told", actor)
		}
	}
	return func(w *wtx, id string) (uint64, error) {
		w.repos = repos
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return 0, err
		}
		actor = resolved
		row, err := w.loadBead(id)
		if err != nil {
			return 0, err
		}
		// Echo handles, whatever form of id was typed.
		var handles []string
		var aboutBeads []peerAskBead
		seen := map[string]bool{}
		for _, b := range about {
			other, err := w.loadBead(b)
			if err != nil {
				return 0, fmt.Errorf("about: %w", err)
			}
			if seen[other.UUID] {
				return 0, fmt.Errorf("about: %s is named twice", other.ID)
			}
			seen[other.UUID] = true
			handles = append(handles, other.ID)
			aboutBeads = append(aboutBeads, peerAskBead{ID: other.ID, Title: other.Title, Stage: other.Stage})
		}
		var p *Peer
		if outside {
			// Before anything is stored: a refused ask leaves no note.
			p, err = loadPeer(w.Tx, peer)
			if err != nil {
				return 0, err
			}
			if err := w.may(ctx, actor, ActAskPeer, Object{Kind: KindPeer, Name: peer, To: to}); err != nil {
				return 0, err
			}
			if !p.carries(PeerAsk, false) {
				return 0, fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
			}
		}
		// A copy of ask, so that a batch which runs this twice checks what
		// the writer sent both times.
		ask := ask
		if len(ask.Options) > 0 {
			options := make([]AskOption, 0, len(ask.Options)+1)
			ask.Options = append(append(options, ask.Options...),
				AskOption{Label: YouDecide, Consequence: "leave it to " + actor})
		}
		if ask.Secret != nil {
			if ask.Secret, err = w.askedSecret(*ask.Secret, ask.To); err != nil {
				return 0, err
			}
		}
		if ask.Run != nil {
			if ask.Run, err = w.askedRun(ctx, *ask.Run, ask.To, actor); err != nil {
				return 0, err
			}
		}
		if ask.Setup != nil {
			var setup *AskSetup
			var options []AskOption
			if setup, options, err = w.askedSetup(ctx, ask, ask.To, actor); err != nil {
				return 0, err
			}
			ask.Setup, ask.Options = setup, options
		}
		if ask.DepotPush != nil {
			if ask.DepotPush, err = w.askedDepotPush(ctx, *ask.DepotPush, row.ID, ask.To, actor); err != nil {
				return 0, err
			}
		}
		// Weighed from the card the yard has just written, never from
		// what the writer sent.
		if ask.Weight, err = w.weighAsk(ask); err != nil {
			return 0, err
		}
		ask.About = handles
		text, err := json.Marshal(ask)
		if err != nil {
			return 0, err
		}
		// The ask is tagged with the crew session that asked it, so its
		// answer is routed to that session's slice; an ask from no crew
		// pane is the line's, as every ask was before sessions.
		_, session, err := paneCrewSession(w.Tx, callerOf(ctx))
		if err != nil {
			return 0, err
		}
		n, err := w.addNotedSession(row, NoteAsk, string(text), actor, session)
		if err != nil {
			return 0, err
		}
		seq := n.Seq
		// The yard's card of moves gives way to this question, which is
		// what the person decides now (putStageAsks).
		if err := w.withdrawMoves(row, fmt.Sprintf("ask %d is on the bead now", seq)); err != nil {
			return 0, err
		}
		if !outside {
			// In the person's queue from this write on: told outside the
			// yard too, when a source tells them.
			if err := w.queueAskPush(ask.To, row.UUID, seq, ""); err != nil {
				return 0, err
			}
			if err := w.queueFootAsk(ask.To, row.UUID, seq, ""); err != nil {
				return 0, err
			}
		}
		// queue puts the ask in the outbox for name at p, as that yard
		// reads it: addressed name@peer.
		queue := func(p *Peer, name string) (*PeerOut, error) {
			sent := ask
			sent.To = name + "@" + p.Name
			body, err := json.Marshal(peerAsk{
				V: PeerAskVersion, To: name, From: actor,
				Bead: row.ID, Title: row.Title, Topic: topicOf(row.Bead), Stage: row.Stage,
				Labels: row.Labels, Body: cutNote(row.Body),
				Seq: seq, Since: n.At, Ask: sent, About: aboutBeads,
			})
			if err != nil {
				return nil, err
			}
			m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Body: body}
			if err := m.check(); err != nil {
				return nil, err
			}
			return w.queuePeer(p, m, actor)
		}
		if ask.Secret != nil || ask.Run != nil || ask.Setup != nil || ask.DepotPush != nil {
			// Nothing of a secret ask crosses a link, and a run ask is
			// approved in this yard alone, as a setup and a depot-push
			// card are: no copy is queued.
			return seq, nil
		}
		if !outside {
			var peers []Peer
			if err := each(w.Tx, bPeers, "", func(_ []byte, p *Peer) error {
				peers = append(peers, *p)
				return nil
			}); err != nil {
				return 0, err
			}
			var copies []askCopy
			for _, t := range AskReach(peers, ask.To, actor) {
				i := slices.IndexFunc(peers, func(p Peer) bool { return p.Name == t.Peer })
				out, err := queue(&peers[i], t.Name)
				if err != nil {
					// The ask is stored and stands here; only its copy
					// is not on its way.
					if err := w.copyUndelivered(ask.To, t.Address(), ask.Question, row.ID, seq, "", err.Error()); err != nil {
						return 0, err
					}
				} else if out != nil {
					copies = append(copies, askCopy{Key: peers[i].Key, Name: t.Name, As: ask.To, Message: out.ID})
				}
			}
			if len(copies) == 0 {
				return seq, nil
			}
			// Where it went, for the day it ends here (settleCopies).
			return seq, put(w.Tx, bAskCopies, join(row.UUID, seqKey(seq)), copies)
		}
		out, err := queue(p, to)
		if err != nil {
			return 0, err
		}
		if out == nil {
			// Checked above, in the same write: the link cannot have
			// stopped carrying ask since.
			return 0, fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
		}
		// The message that yard has it under, for the day it is taken back.
		return seq, put(w.Tx, bAskCopies, join(row.UUID, seqKey(seq)), []askCopy{{Key: p.Key, Name: to, Message: out.ID}})
	}, nil
}

// AskedBead is what AskNew made: the bead and the seq of the ask on it.
type AskedBead struct {
	Bead *Bead  `json:"bead"`
	Ask  uint64 `json:"ask"`
}

// AskNew makes a bead for a question that is about no bead yet, and the
// ask on it, in one write: nb as CreateBead takes it, titled with the
// question, and ask as AddAsk takes it. Whatever either would refuse
// refuses both, so a refused ask leaves no bead behind. Nothing more is
// stored than the two would store.
//
// A bead nb names no stage and no type for is a question (TypeQuestion):
// it stands where a bead of its depot starts (the stage new, under no
// flow, without one) and nothing routes it there, so no group builds a
// question and the dispatcher does not report it unrouted. Its asker
// closes it when the answer is on it. A stage or a type is the caller
// placing the bead, in a depot's decide stage for one: that bead is what
// CreateBead makes of nb, a task without a type, and routed as its is.
// From a session's or a crew member's pane the bead and the ask are
// recorded as that session's actor or that crew member, whatever actor
// says; from anywhere else, as given (paneAuthor).
func (s *Store) AskNew(ctx context.Context, nb NewBead, ask Ask, actor string) (*AskedBead, error) {
	nb.Title = strings.TrimSpace(ask.Question)
	if nb.Stage == "" && nb.Type == "" {
		nb.Type = TypeQuestion
	}
	add, err := s.newAsk(ctx, ask, actor)
	if err != nil {
		return nil, err
	}
	create, err := s.newBead(ctx, nb, actor)
	if err != nil {
		return nil, err
	}
	var made *AskedBead
	err = s.write(func(w *wtx) error {
		row, err := create(w)
		if err != nil {
			return err
		}
		seq, err := add(w, row.UUID)
		if err != nil {
			return err
		}
		// The ask re-minted the bead: answer it as stored.
		if row, err = w.loadBead(row.UUID); err != nil {
			return err
		}
		made = &AskedBead{Bead: &row.Bead, Ask: seq}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return made, nil
}

// AddAnswer records ans on the bead id as a note of kind answer, written
// by actor: a pick of one of the ask's options with any words, words
// alone on an ask with no options, Later, or Decline with why.
//
// A plain ask takes its answer only from whom it was put to or an
// operator; anyone else's is refused naming the addressee. A run, secret
// or setup ask takes Later and a decline from anyone here, and its settling
// answer through its own approval alone; a mapping ask takes Later from
// anyone and a decline from whom it was put to; a stage ask takes every
// answer from its members alone (below).
//
// A declined ask and an ask answered with words take no further answer; a
// picked ask takes only a newer pick, which supersedes for display. Words
// alone on an ask with options are refused: they go as a plain note and
// leave the ask open.
//
// An id of bead@peer names a question another yard asked (a foreign ask):
// the answer is queued home for the peer instead of written as a note
// (answerForeign), where it lands on the bead it was asked on. The socket
// writes and yardr bead answer both come through here, so both answer a
// foreign ask.
//
// An answer that ends an ask which was also sent to other yards (AddAsk)
// is told to each of them, in the same write (settleCopies): a pick, words
// or a decline, never Later.
//
// A secret ask takes Later, and Decline without words, and nothing else
// here: a pick, words and a decline's words are refused without being said
// back, so that no path but GiveSecret takes what a person typed for it.
//
// A run ask takes Later and Decline with its words, and nothing else here:
// a pick and words are refused, so that no path but ApproveAsk runs its
// command, from this yard or over a link.
//
// A setup ask takes Later and Decline with its words, and nothing else
// here: a pick and words are refused, so that no path but ApproveSetup
// settles its preset and its account together, from this yard or over a
// link.
//
// An answer on a stage ask is its members' to give: a pick, words, Later
// or Decline from anyone else is refused whole, with the membership
// reason. A pick or words that fire an outcome move the bead in the same
// write, through Done's path (stageAnswer). From a session's or a crew
// member's pane the answer is recorded as that session's actor or that
// crew member, whatever actor says; from anywhere else, as given
// (paneAuthor).
//
// A pick on a mapping ask that writes a user's identity is a weighty
// approval, which AnswerAsk makes with its person's word (weighed): here
// the answer comes with none, so such a pick is refused (weighedRefusal).
func (s *Store) AddAnswer(ctx context.Context, id string, ans Answer, actor string) error {
	return s.addAnswer(ctx, id, ans, actor, "", false)
}

// AddAnswerExpected is AddAnswer with the revision the caller read. The
// check and the answer share one write, so another tab cannot answer an
// older revision in the gap between a read and the write. The pane rule
// is AddAnswer's (paneAuthor).
func (s *Store) AddAnswerExpected(ctx context.Context, id string, ans Answer, actor, revision string) error {
	if revision == "" {
		return fmt.Errorf("an answer needs the bead revision it was drawn from")
	}
	return s.addAnswer(ctx, id, ans, actor, revision, false)
}

// addAnswer is AddAnswer's write. weighed is the caller's word that its
// person saw the weight of a mapping card whose pick writes an identity
// (weighedRefusal); no other answer reads it.
func (s *Store) addAnswer(ctx context.Context, id string, ans Answer, actor, revision string, weighed bool) error {
	unlock := s.askLocks.lock(askLockKey(id, ans.Ask))
	defer unlock()
	if err := answerShape(id, &ans); err != nil {
		return err
	}
	// A bead's handle never holds an "@" (validID).
	if bead, peer, foreign := strings.Cut(id, "@"); foreign {
		if revision != "" {
			return fmt.Errorf("a revision cannot guard an ask on another yard's bead")
		}
		return s.answerForeign(ctx, bead, peer, ans, actor)
	}
	// What an answer that moves the bead resolves its outcome and the next
	// stage's template through, read before the write: git and disk never
	// run inside one. An answer that moves nothing wants none of it, so a
	// failure to read refuses only the move, with its reason.
	pre, err := s.GetBead(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.seatLeftRefusal(ctx, id, ans); err != nil {
		return err
	}
	repo, repoErr := s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre))
	home, homeErr := s.stageHomeBefore(pre, repo)
	return s.writeVia(ctx, func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		if revision != "" && row.Revision != revision {
			return fmt.Errorf("%w: bead %s is at revision %s, not %s; read it again", ErrConflict, row.ID, row.Revision, revision)
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		mayAnswer := func(to string) error {
			return w.may(ctx, actor, ActAnswerAsk, Object{Kind: KindAsk, Name: row.ID, Seq: ans.Ask, To: to})
		}
		if _, err := answerable(row.ID, notes, ans, now(), actor, mayAnswer); err != nil {
			return err
		}
		move, _, err := w.stageAnswer(ctx, row, notes, ans, actor, repo, home)
		if err != nil {
			return err
		}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		note, err := w.addNoted(row, NoteAnswer, string(text), actor)
		if err != nil {
			return err
		}
		// Put aside: what is still queued for the ask goes, and the
		// aside's return is news too, told when its 24 hours are over
		// unless the asker writes on the bead first.
		if ans.Later && !ans.TakeBack && !ans.Decline {
			if err := w.queueFootNoteAside(row, notes, ans.Ask, note); err != nil {
				return err
			}
		}
		// A mapping ask's pick writes the identity it names, in this
		// write, and the pick that writes, the let-in pick or the decline
		// ends the card by closing its bead. It runs after the answer is recorded, for two
		// reasons: the write that settles the sender closes the cards for
		// it, and this card's ask must be answered by then, not taken back;
		// and addNoted wrote row, which a close made before it would lose
		// to. A refusal (stale, or not the operator's to give) rolls the
		// answer back with the write and leaves the card open. A declined
		// claim ends its card beside it the same way (answerClaim).
		for _, n := range notes {
			if n.Seq != ans.Ask || n.Kind != NoteAsk {
				continue
			}
			ask, err := ParseAsk(n)
			if err != nil {
				continue
			}
			if ask.Identity != nil {
				if err := w.answerMapping(ctx, row, ask, ans, actor, weighed); err != nil {
					return err
				}
			}
			if err := w.answerClaim(row, ask, ans, actor); err != nil {
				return err
			}
		}
		// A pick or words that fire an outcome move the bead in the same
		// write, through Done's path, and one that fires an advance through
		// Advance's; the answer's words stay on the answer note, so the move
		// carries no separate note. Nobody's to refuse: the answer's writer
		// was checked as a decider above.
		if move != "" {
			// The yard's own read failing under the move, not a refusal
			// of the answer: marked, so a device that is no operator's
			// is told that it failed and not on which file.
			if repoErr != nil {
				return &OperationalError{Err: repoErr}
			}
			if homeErr != nil {
				return &OperationalError{Err: homeErr}
			}
			if _, err := w.move(row.UUID, actor, repo, home, nil, stagePick(move)); err != nil {
				return err
			}
		}
		return w.settleCopies(row, ans, nil, "")
	})
}

// seatLeftRefusal refuses an answer to the yard's login card on a crew
// journal after its seat left the login screen: the answer made after the
// leave, before the next pass takes the card back, would lead a login
// nothing waits on. Dispatch learns of the leave at the next pass, which
// is the window itself, so only the answer path sees the answer in time,
// at write time here.
//
// The card is an ask the yard authored on a journal bead, and the answer
// a pick or words: declining the dead card ends it, and putting it aside
// leaves it to the sweep. The seat is any session of the crew member the
// journal is of — the line session and every conversation, as seatsAtLogin
// counts them — live and marked at the screen; a session seated while the
// card's own left still takes the answer, since no ask names its seat.
// Ordinary answers pay one string comparison; the notes and the crew are
// read only on the rare daemon-ask path. With the refusal it answers
// RefusedAskSeatLeft, as answerable answers its own; the local caller
// keeps the words alone.
func (s *Store) seatLeftRefusal(ctx context.Context, id string, ans Answer) (string, error) {
	if !strings.HasSuffix(id, "-journal") || ans.Decline || ans.Later {
		return "", nil
	}
	notes, err := s.Notes(ctx, id)
	if err != nil {
		return "", err
	}
	yard := false
	for _, n := range notes {
		if n.Seq == ans.Ask && n.Kind == NoteAsk && n.Author == "yard" {
			yard = true
			break
		}
	}
	if !yard {
		return "", nil
	}
	crew, err := s.Crew(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range crew {
		if r.Journal != id {
			continue
		}
		if screen, _ := r.CrewSession.login(); screen {
			return "", nil
		}
		for _, cs := range r.Conversations {
			if screen, _ := cs.login(); screen {
				return "", nil
			}
		}
		break
	}
	// The journal remains after its crew member is deleted. Its login card
	// still waits on a seat that no longer exists, so refuse it as well.
	return RefusedAskSeatLeft, fmt.Errorf("ask %d waits on a seat that left its login screen: the yard takes the card back", ans.Ask)
}

// answerShape refuses an answer that is not exactly one of a pick, words
// alone, Later and Decline, whatever ask it names; id is the bead as the
// refusal says it. Whether a decline needs words is the ask's to say
// (answerable). Given is GiveSecret's to write: an answer that came with
// it would say "gave" of a value no file holds. Ran is ApproveAsk's: an
// answer that came with it would say the yard ran what it did not.
// Account is ApproveSetup's, with Ran. TakeBack is TakeBackAsk's, which
// asks who takes it back: here it would be anyone's.
func answerShape(id string, ans *Answer) error {
	ans.Words = strings.TrimSpace(ans.Words)
	if ans.TakeBack {
		return fmt.Errorf("answer %d: an ask is taken back by whoever asked it, with yardr bead ask --take-back %s %d \"<why>\"", ans.Ask, id, ans.Ask)
	}
	if len(ans.Given) > 0 {
		return fmt.Errorf("answer %d: a secret is given with yardr bead answer %s %d --secret, and recorded by the yard alone", ans.Ask, id, ans.Ask)
	}
	if ans.Account != "" {
		return fmt.Errorf("answer %d: a setup's account is taken with yardr bead answer %s %d <n> --account <source>, and recorded by the yard alone", ans.Ask, id, ans.Ask)
	}
	if ans.Ran != nil {
		return fmt.Errorf("answer %d: a command is approved with yardr bead answer %s %d --approve, and what ran is recorded by the yard alone", ans.Ask, id, ans.Ask)
	}
	if ans.Ask == 0 {
		return fmt.Errorf("an answer needs the ask: yardr bead answer %s <ask> <n> [words]", id)
	}
	// Exactly one of a pick, words alone, Later and Decline.
	kinds := boolCount(ans.Option != 0) + boolCount(ans.Later) + boolCount(ans.Decline)
	if kinds == 0 && ans.Words == "" {
		return fmt.Errorf("answer %d: pick an option, answer with --words, put it aside with --later, or decline it", ans.Ask)
	}
	if kinds > 1 || (kinds == 1 && ans.Option == 0 && ans.Words != "" && !ans.Decline) {
		return fmt.Errorf("answer %d: pick an option, answer with --words, put it aside with --later, or decline it: one of the four", ans.Ask)
	}
	if ans.Later && (ans.Option != 0 || ans.Words != "") {
		return fmt.Errorf("--later puts ask %d aside with nothing else", ans.Ask)
	}
	return nil
}

// answerable decides whether ans, by actor, may be recorded on the ask it
// names among notes, the notes of the bead id, at the time at: the one set
// of rules for an answer written here, one queued home to another yard, and
// one another yard sent home. mayAnswer asks the policy whether the
// answerer may answer a plain ask put to to (ActAnswerAsk); nil where
// the answer is recorded as the addressee's, checked before. With the
// refusal it answers what the other yard is told of one that came from
// there: RefusedNoSuchAsk for an ask that is none, RefusedAskDeclined,
// RefusedAskAnswered and RefusedAskTakenBack for one that takes no further
// answer, RefusedNotAsk for a decline without its words, "" for an answer
// that does not fit the ask.
func answerable(id string, notes []Note, ans Answer, at time.Time, actor string, mayAnswer func(to string) error) (string, error) {
	var askNote *Note
	for i := range notes {
		if notes[i].Seq == ans.Ask {
			askNote = &notes[i]
		}
	}
	if askNote == nil {
		return RefusedNoSuchAsk, fmt.Errorf("bead %s has no ask %d", id, ans.Ask)
	}
	if askNote.Kind != NoteAsk {
		return RefusedNoSuchAsk, fmt.Errorf("note %d on bead %s is not an ask", ans.Ask, id)
	}
	ask, err := ParseAsk(*askNote)
	if err != nil {
		return RefusedNoSuchAsk, err
	}
	// Whose before what and what state it is in: a plain ask is its
	// addressee's or an operator's to answer, and nobody else is told
	// whether it would otherwise land. A stage ask answers to its
	// members (stageAnswer); a run, secret or setup ask to its own
	// approval, with Later and a decline anyone's here; a mapping ask
	// to whom it was put to (answerMapping), with Later anyone's.
	if ask.Stage == "" && ask.Secret == nil && ask.Run == nil && ask.Setup == nil && ask.DepotPush == nil && ask.Identity == nil && mayAnswer != nil {
		if err := mayAnswer(ask.To); err != nil {
			return "", err
		}
	}
	switch {
	// First, and without what was sent: the words may be the secret, typed
	// into a client that does not know the field.
	case ask.Secret != nil && ans.Decline && ans.Words != "":
		return "", fmt.Errorf("ask %d asks for a secret, and declining it takes no words: yardr bead answer %s %d --decline \"\"", ans.Ask, id, ans.Ask)
	case ask.Secret != nil && (ans.Option != 0 || ans.Words != ""):
		return "", fmt.Errorf("ask %d takes a secret, not words: yardr bead answer %s %d --secret", ans.Ask, id, ans.Ask)
	// A client that does not know the field shows a run ask as one that
	// wants words, and a link carries a pick or words home: neither approves.
	case ask.Run != nil && (ans.Option != 0 || ans.Words != "" && !ans.Decline):
		return "", fmt.Errorf("ask %d takes an approval, not words: yardr bead answer %s %d --approve", ans.Ask, id, ans.Ask)
	case ask.DepotPush != nil && (ans.Option != 0 || ans.Words != "" && !ans.Decline):
		return "", fmt.Errorf("ask %d takes an approval, not words: yardr bead answer %s %d --approve", ans.Ask, id, ans.Ask)
	// A setup ask has options, but a pick alone settles nothing: the
	// preset pick goes with the account, in one approval; a drift card
	// whose file holds every name is kept with a bare pick, through the
	// same approval.
	case ask.Setup != nil && (ans.Option != 0 || ans.Words != "" && !ans.Decline):
		if ask.Setup.Given && len(ask.Setup.Missing) == 0 {
			if len(ask.Setup.Candidates) > 0 {
				return "", fmt.Errorf("ask %d takes a preset pick with the account, not words alone: yardr bead answer %s %d <n>, <n> --secret, or <n> --account <source>", ans.Ask, id, ans.Ask)
			}
			return "", fmt.Errorf("ask %d takes a preset pick with the account, not words alone: yardr bead answer %s %d <n> or <n> --secret", ans.Ask, id, ans.Ask)
		}
		return "", fmt.Errorf("ask %d takes a preset pick with the account, not words alone: yardr bead answer %s %d <n> --secret (or --account <source> where the card lists one)", ans.Ask, id, ans.Ask)
	case ask.Secret == nil && ans.Decline && ans.Words == "":
		return RefusedNotAsk, fmt.Errorf("declining ask %d needs words: --decline \"<why you will not decide it>\"", ans.Ask)
	}
	resolved := ResolveAsks(notes, at)
	var current *ResolvedAsk
	for i := range resolved {
		if resolved[i].Note.Seq == ans.Ask {
			current = &resolved[i]
		}
	}
	if current == nil {
		return RefusedNoSuchAsk, fmt.Errorf("bead %s has no ask %d", id, ans.Ask)
	}
	switch current.State {
	case AskDeclined:
		return RefusedAskDeclined, fmt.Errorf("ask %d was declined: %s", ans.Ask, declinedLine(*current.Current))
	case AskTakenBack:
		return RefusedAskTakenBack, fmt.Errorf("ask %d is asked no more: %s", ans.Ask, takenBackLine(*current.Current))
	case AskAnswered:
		if ask.Run != nil {
			return RefusedAskAnswered, fmt.Errorf("ask %d was approved already, and the yard ran its command", ans.Ask)
		}
		if ask.Setup != nil {
			return RefusedAskAnswered, fmt.Errorf("ask %d was approved already: a setup runs once for one ask", ans.Ask)
		}
		if ask.DepotPush != nil {
			return RefusedAskAnswered, fmt.Errorf("ask %d was approved already, and the yard carries its push change out", ans.Ask)
		}
		if len(ask.Options) == 0 {
			return RefusedAskAnswered, fmt.Errorf("ask %d is answered already", ans.Ask)
		}
		if ans.Option == 0 {
			return RefusedAskAnswered, fmt.Errorf("ask %d is answered already: a newer pick replaces it, nothing else", ans.Ask)
		}
	}
	switch {
	case ans.Option != 0 && len(ask.Options) == 0:
		return "", fmt.Errorf("ask %d wants words: answer with yardr bead answer %s %d --words \"<text>\"", ans.Ask, id, ans.Ask)
	case ans.Option != 0 && (ans.Option < 1 || ans.Option > len(ask.Options)):
		return "", fmt.Errorf("ask %d has options 1..%d", ans.Ask, len(ask.Options))
	case ans.Option == 0 && !ans.Later && !ans.Decline && len(ask.Options) > 0:
		return "", fmt.Errorf("ask %d has options: words alone go as yardr bead note %s \"...\" and leave the ask open; pick one with yardr bead answer %s %d <n>",
			ans.Ask, id, id, ans.Ask)
	case ans.Option == 0 && !ans.Later && !ans.Decline && ans.Words == "":
		return "", fmt.Errorf("ask %d wants words: yardr bead answer %s %d --words \"<text>\"", ans.Ask, id, ans.Ask)
	}
	return "", nil
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// declinedLine is a declined ask's end as every view shows it. Only a
// secret ask is declined without words. Who and why are one line, as
// letters only, so neither starts a line of its own where it is shown.
func declinedLine(a ResolvedAnswer) string {
	if a.Answer.Words == "" {
		return fmt.Sprintf("%s declined to give it", OneLine(a.Note.Author))
	}
	return fmt.Sprintf("%s declined to decide this: %s", OneLine(a.Note.Author), OneLine(a.Answer.Words))
}

// takenBackLine is a taken-back ask's end as every view shows it: who took
// it back and why, never as an answer of the person asked. One line, as
// letters only, so neither starts a line of its own where it is shown.
func takenBackLine(a ResolvedAnswer) string {
	return fmt.Sprintf("taken back by %s: %s", OneLine(a.Note.Author), OneLine(a.Answer.Words))
}

// TakeBackAsk ends the ask seq on the bead id as asked no more, with the
// reason why: a note of kind answer written by actor, which is no answer of
// the person asked. The ask leaves their open decisions and reads wherever
// it is shown as "taken back by <actor>: <reason>". It takes no answer
// afterwards.
//
// Only whoever asked it and the operator may: a question is its asker's to
// withdraw, and nobody else's to end in the person's place. An ask that is
// answered, declined or taken back already is refused, naming how it ended;
// one put aside is taken back as an open one is. A bead@peer names a
// question another yard asked, which is taken back there.
//
// Where the ask also stands in other yards (a copy by reach, or an ask
// addressed to <person>@<peer>), each is told in the same write
// (settleCopies), and it leaves the person's decisions there too. From a
// session's or a crew member's pane the take-back is recorded as that
// session's actor or that crew member, whatever actor says, and theirs
// are the only asks it may take back; from anywhere else, as given
// (paneAuthor).
func (s *Store) TakeBackAsk(ctx context.Context, id string, seq uint64, reason, actor string) error {
	reason = strings.TrimSpace(reason)
	switch {
	case seq == 0:
		return fmt.Errorf("taking an ask back needs the ask: yardr bead ask --take-back %s <ask> \"<why>\"", id)
	case reason == "":
		return fmt.Errorf("taking ask %d back needs a reason, which the person reads in its place: yardr bead ask --take-back %s %d \"<why>\"", seq, id, seq)
	case strings.Contains(id, "@"):
		// A bead's handle never holds an "@" (validID).
		return fmt.Errorf("ask %d on %s was asked in another yard, and is taken back there by whoever asked it", seq, id)
	}
	unlock := s.askLocks.lock(askLockKey(id, seq))
	defer unlock()
	return s.writeVia(ctx, func(w *wtx) error {
		who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = who
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		var r *ResolvedAsk
		resolved := ResolveAsks(notes, now())
		for i := range resolved {
			if resolved[i].Note.Seq == seq {
				r = &resolved[i]
			}
		}
		switch {
		case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
			return fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
		case r == nil:
			return fmt.Errorf("bead %s has no ask %d", row.ID, seq)
		case r.AskErr != nil:
			return r.AskErr
		}
		if err := w.may(ctx, actor, ActTakeBackAsk, Object{Kind: KindAsk, Name: row.ID, Seq: seq, Whose: r.Note.Author}); err != nil {
			return err
		}
		switch r.State {
		case AskAnswered:
			return fmt.Errorf("ask %d is answered already: nothing is left to take back", seq)
		case AskDeclined:
			return fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
		case AskTakenBack:
			return fmt.Errorf("ask %d is taken back already: %s", seq, takenBackLine(*r.Current))
		}
		ans := Answer{Ask: seq, Words: reason, TakeBack: true}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		if _, err = w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
			return err
		}
		// A mapping card taken back ends without an answer: what it
		// held goes with it, as it does when its bead closes. A settle
		// takes its cards back beside its own delivery (takeBackStage
		// in retireMappingCards), never through here.
		if r.Ask.Identity != nil {
			if err := w.dropHeld(row.UUID); err != nil {
				return err
			}
		}
		return w.settleCopies(row, ans, nil, "")
	})
}

// AskState is what an ask waits for: nothing yet, a pick that stands, a
// return from being put aside, or nobody, declined or taken back.
type AskState int

const (
	// AskOpen waits for an answer.
	AskOpen AskState = iota
	// AskAnswered has a pick, words on an ask with no options, or the
	// names a secret ask was given, that stands.
	AskAnswered
	// AskAside was put aside and has not come back yet.
	AskAside
	// AskDeclined was declined: ended with no option picked.
	AskDeclined
	// AskTakenBack was taken back by its asker or the operator: asked no
	// more, and no answer of the person.
	AskTakenBack
)

// String is the state as words.
func (s AskState) String() string {
	switch s {
	case AskAnswered:
		return "answered"
	case AskAside:
		return "put aside"
	case AskDeclined:
		return "declined"
	case AskTakenBack:
		return "taken back"
	}
	return "open"
}

// ResolvedAnswer is one answer note parsed.
type ResolvedAnswer struct {
	Note   Note
	Answer Answer
}

// ResolvedAsk is one ask with its answers and its state.
type ResolvedAsk struct {
	Note Note
	Ask  Ask
	// AskErr says the note holds no ask; the views show the seq and why,
	// never the raw text.
	AskErr  error
	Answers []ResolvedAnswer
	State   AskState
	// Current is the answer the state comes from, nil when open.
	Current *ResolvedAnswer
	// ReturnAt is when a put-aside ask comes back, 24 hours after it was
	// put aside; the asker's next note on the bead brings it back sooner.
	ReturnAt time.Time
	now      time.Time
}

// ResolveAsks groups a bead's notes into its asks, oldest first, each with
// its answers in order and its state at now. A newer pick supersedes the
// older one; a put-aside that has run its 24 hours, or that the asker
// wrote over, is as if gone.
func ResolveAsks(notes []Note, now time.Time) []ResolvedAsk {
	var out []ResolvedAsk
	for _, n := range notes {
		if n.Kind != NoteAsk {
			continue
		}
		r := ResolvedAsk{Note: n, now: now}
		r.Ask, r.AskErr = ParseAsk(n)
		if r.AskErr == nil {
			current := -1
			for _, m := range notes {
				if m.Kind != NoteAnswer {
					continue
				}
				// An answer that does not parse names no ask: nothing
				// claims it, and the views say so where it stands.
				a, err := ParseAnswer(m)
				if err != nil || a.Ask != n.Seq {
					continue
				}
				r.Answers = append(r.Answers, ResolvedAnswer{Note: m, Answer: a})
				switch {
				case a.TakeBack:
					r.State, current = AskTakenBack, len(r.Answers)-1
				case a.Decline:
					r.State, current = AskDeclined, len(r.Answers)-1
				case a.Later:
					if laterBack(n.Author, notes, m, now) {
						continue
					}
					r.State, current = AskAside, len(r.Answers)-1
					r.ReturnAt = m.At.Add(LaterFor)
				default:
					r.State, current = AskAnswered, len(r.Answers)-1
				}
			}
			if current >= 0 {
				r.Current = &r.Answers[current]
			}
		}
		out = append(out, r)
	}
	return out
}

// laterBack reports whether a put-aside ask has come back: its 24 hours
// are over, or the asker wrote on the bead after it was put aside.
func laterBack(asker string, notes []Note, later Note, now time.Time) bool {
	if !now.Before(later.At.Add(LaterFor)) {
		return true
	}
	return slices.ContainsFunc(notes, func(n Note) bool {
		return n.Author == asker && n.Seq > later.Seq
	})
}

// beadNotes reads the bead's notes in seq order, mapping the bead to its
// handle, as Notes does.
func beadNotes(tx *bolt.Tx, uuid string) ([]Note, error) {
	var row beadRow
	if ok, err := get(tx, bBeads, uuid, &row); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("bead: %w", ErrNotFound)
		}
		return nil, err
	}
	var out []Note
	err := each(tx, bNotes, uuid+"\x00", func(_ []byte, n *Note) error {
		n.Bead = row.ID
		out = append(out, *n)
		return nil
	})
	return out, err
}

// carriedLines renders what an adding ask carries, whole in a terminal
// under the yard's sentences, deeper than any of them, for checking the
// reading against the file; on a phone it is one tap behind.
func carriedLines(carried []AskSource) []string {
	var out []string
	for _, c := range carried {
		out = append(out, "  the declaration of source "+OneLine(c.Name)+", whole as carried:")
		for _, line := range strings.Split(strings.TrimRight(c.Text, "\n"), "\n") {
			out = append(out, "      "+NoteLetters(line))
		}
		for _, f := range c.Files {
			out = append(out, "  the command file "+OneLine(f.Program)+" (sha256 "+OneLine(f.SHA256)+"), whole as carried:")
			for _, line := range strings.Split(strings.TrimRight(f.Text, "\n"), "\n") {
				out = append(out, "      "+NoteLetters(line))
			}
		}
		for _, df := range c.Old {
			out = append(out, "  the change to command file "+OneLine(df.Program)+" (sha256 "+OneLine(or(df.OldSHA256, "(none)"))+" -> "+OneLine(or(df.NewSHA256, "(none)"))+"), whole as carried:")
			for _, line := range strings.Split(strings.TrimRight(df.Diff, "\n"), "\n") {
				out = append(out, "      "+NoteLetters(line))
			}
		}
		for _, rem := range c.Removed {
			if rem.Text == "" {
				continue
			}
			out = append(out, "  the entry "+OneLine(rem.Path)+" the copy would remove (sha256 "+OneLine(rem.SHA256)+"), whole as carried:")
			for _, line := range strings.Split(strings.TrimRight(rem.Text, "\n"), "\n") {
				out = append(out, "      "+NoteLetters(line))
			}
		}
	}
	return out
}

// Lines renders the ask as numbered text: the question, its context, its
// numbered options with the recommendation marked, and its state. Nobody
// reads the raw form.
func (r ResolvedAsk) Lines() []string { return r.lines(nil) }

// LinesMarked renders the ask as Lines does, with an answer's author
// from another yard marked as mail is: "(as <user>)" where as (from
// TrustedAs) trusts the address, the foreign line otherwise. Prime,
// review and chat read asks this way; the socket's decided entries carry
// no author and need no mark.
func (r ResolvedAsk) LinesMarked(as map[string]string) []string {
	return r.lines(func(a string) string { return MarkedAuthor(a, as) })
}

// lines renders the ask, marking an answer's author with mark, identity
// when nil. The asker and the address are this yard's own words and are
// never marked; the answer is the other yard's, and says whose word it
// is.
//
// A weighty card's classes come first after its head, each a line of
// WeightMark and "<class>: <line>" (weight.go). The question's first line
// and the bare context lines stand at the same column, so the mark goes
// from them where they carry it: a line starting with the mark is the
// yard's.
//
// A run ask is drawn so that the asker's words cannot stand for the yard's:
// each context line begins with who says it, a question's and an answer's
// later lines stand further in than any line the yard prints, and the
// yard's part comes last and together, the command whole, what the yard
// says it changes and the command that takes it back. The command is never
// cut or shortened.
func (r ResolvedAsk) lines(mark func(string) string) []string {
	if r.AskErr != nil {
		return []string{fmt.Sprintf("ask %d: not a question any more (%v)", r.Note.Seq, r.AskErr)}
	}
	state := r.State.String()
	if r.Ask.Run != nil && r.State == AskAnswered {
		state = "approved"
	}
	if r.Ask.Setup != nil && r.State == AskAnswered {
		state = "approved"
	}
	if r.Ask.DepotPush != nil && r.State == AskAnswered {
		state = "approved"
	}
	head := fmt.Sprintf("ask %d · %s · to %s · asked by %s · %s ago",
		r.Note.Seq, state, OneLine(r.Ask.To), OneLine(r.Note.Author), askAge(r.Note.At, r.now))
	if r.State == AskAside {
		head = fmt.Sprintf("ask %d · put aside until %s · to %s · asked by %s · %s ago",
			r.Note.Seq, r.ReturnAt.Local().Format("2006-01-02 15:04"), OneLine(r.Ask.To), OneLine(r.Note.Author), askAge(r.Note.At, r.now))
	}
	// The question is the asker's words, and stands in the list its
	// answers stand in: further in than the card's head, as a note's
	// text stands further in than its author line, so no line of it
	// reads as another note. Its later lines stand further in than any
	// line the yard prints, so none reads as one of the yard's.
	out := []string{head}
	// The card's weight is the yard's, and stands before the asker's
	// question, one line a class, so a weighty card reads as one before
	// anything else on it is read.
	for _, w := range r.Ask.Weight {
		out = append(out, "  "+w.Marked())
	}
	// The question's first line stands where the card's weight lines do,
	// so the mark goes from it: an asker cannot type a weight line. Its
	// later lines stand further in than any line the yard prints, and keep
	// their words.
	question := strings.TrimSpace(NoteLetters(r.Ask.Question))
	if i := strings.Index(question, "\n"); i >= 0 {
		question = WithoutWeightMark(question[:i]) + question[i:]
	} else {
		question = WithoutWeightMark(question)
	}
	out = append(out, "  "+strings.ReplaceAll(question, "\n", "\n        "))
	for _, line := range r.Ask.Context {
		// A context line is one line, refused with a break at the
		// write; one stored before that folds to the line it is.
		line = OneLine(line)
		if r.Ask.Run != nil || r.Ask.Setup != nil || r.Ask.DepotPush != nil {
			line = OneLine(r.Note.Author) + " says: " + line
		}
		// A bare line stands where the weight lines do, so the mark goes
		// from it too; where "says:" stands the asker's mark is mid-line
		// and stays.
		out = append(out, "  "+WithoutWeightMark(line))
	}
	if r.Ask.Identity != nil {
		if line := heldLine(r.Ask.Identity.Held); line != "" {
			out = append(out, "  "+line)
		}
	}
	switch {
	case r.Ask.Run != nil && r.State == AskAnswered:
	case r.Ask.Run != nil:
		out = append(out, fmt.Sprintf("  when you approve, the yard runs this as %s, and nothing else:", OneLine(r.Ask.To)),
			"      "+strings.ReplaceAll(NoteLetters(r.Ask.Run.Command()), "\n", "\n      "))
		if r.Ask.Run.Description != "" {
			out = append(out, "  what the command does: "+OneLine(NoteLetters(r.Ask.Run.Description)))
		}
		for _, does := range r.Ask.Run.Does {
			out = append(out, "  the yard says: "+safeSourceCardLine(does))
		}
		out = append(out, carriedLines(r.Ask.Run.Carried)...)
		if r.Ask.Run.Pack != nil && r.Ask.Run.Pack.Plan != nil {
			for _, o := range r.Ask.Run.Pack.Plan.Objects {
				if o.Diff != "" {
					out = append(out, "  the change to file "+OneLine(o.Name)+" (sha256 "+OneLine(or(o.FileOldSHA, "(none)"))+" -> "+OneLine(or(o.FileNewSHA, "(none)"))+"), whole as carried:")
					for _, line := range strings.Split(strings.TrimRight(o.Diff, "\n"), "\n") {
						out = append(out, "      "+NoteLetters(line))
					}
				}
				if o.Text != "" {
					out = append(out, "  the file "+OneLine(o.Name)+" as the pack would install it, whole as carried:")
					for _, line := range strings.Split(strings.TrimRight(o.Text, "\n"), "\n") {
						out = append(out, "      "+NoteLetters(line))
					}
				}
			}
		}
		out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.Run.TakenBack()), "\n", "\n  "))
	case r.Ask.Setup != nil && r.State == AskAnswered:
	case r.Ask.Setup != nil:
		out = append(out, "  when you approve, the yard applies the pack, takes the account, and grants the preset you pick, and nothing else:",
			"      "+strings.ReplaceAll(NoteLetters(r.Ask.Setup.Summary()), "\n", "\n      "))
		for _, does := range r.Ask.Setup.Does {
			out = append(out, "  the yard says: "+safeSourceCardLine(does))
		}
		for i, o := range r.Ask.Options {
			line := fmt.Sprintf("    %d. %s: %s", i+1, OneLine(o.Label), OneLine(o.Consequence))
			if i+1 == r.Ask.Recommend {
				line += fmt.Sprintf(" (recommended: %s)", OneLine(r.Ask.Why))
			}
			out = append(out, line)
		}
		out = append(out, carriedLines(r.Ask.Setup.Carried)...)
		out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.Setup.TakenBack()), "\n", "\n  "))
	case r.Ask.DepotPush != nil && r.State == AskAnswered:
	case r.Ask.DepotPush != nil:
		what := "turn push on for depot " + r.Ask.DepotPush.Depot
		if r.Ask.DepotPush.Off {
			what = "turn push off for depot " + r.Ask.DepotPush.Depot
		}
		out = append(out, "  when you approve, the yard carries this out as "+OneLine(r.Ask.To)+", and nothing else:",
			"      "+what)
		for _, does := range r.Ask.DepotPush.Does {
			out = append(out, "  the yard says: "+safeSourceCardLine(does))
		}
		out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.DepotPush.TakenBack()), "\n", "\n  "))
	case r.Ask.Secret != nil:
		out = append(out, "  "+r.Ask.Secret.Line())
		if r.Ask.Secret.Harness != "" {
			out = append(out, "  the value is kept out of notes, events and logs; each such session receives it in its environment")
		} else if r.Ask.Secret.Login != "" {
			out = append(out, "  it goes to the waiting login only; it is kept nowhere, and no agent is shown it")
		} else {
			out = append(out, "  it goes to a file only the yard's server reads; no agent is shown it")
		}
	case len(r.Ask.Options) == 0:
		out = append(out, "  words wanted: answer with words")
	default:
		for i, o := range r.Ask.Options {
			line := fmt.Sprintf("  %d. %s - %s", i+1, OneLine(o.Label), OneLine(o.Consequence))
			if i+1 == r.Ask.Recommend {
				line += fmt.Sprintf(" (recommended: %s)", OneLine(r.Ask.Why))
			}
			out = append(out, line)
		}
	}
	if len(r.Ask.About) > 0 {
		out = append(out, "  about "+OneLine(strings.Join(r.Ask.About, ", ")))
	}
	who := func(a string) string {
		if mark == nil {
			return a
		}
		return mark(a)
	}
	switch r.State {
	case AskAnswered:
		c := *r.Current
		by := OneLine(ViaMark(who(c.Note.Author), c.Note.Via))
		switch {
		case r.Ask.Run != nil && c.Answer.Ran != nil:
			// An approved apply the server has not carried out yet reads
			// as being carried out, and one whose carrying out failed as
			// failed: the three readings of a run ask (yardr-bht9 3.5).
			switch {
			case c.Answer.Ran.Failed:
				out = append(out, fmt.Sprintf("  %s approved, failed: %s", by, strings.ReplaceAll(NoteLetters(c.Answer.Ran.Command()), "\n", "\n  ")))
			case c.Answer.Ran.Result == CarryPending:
				out = append(out, fmt.Sprintf("  %s approved, being carried out: %s", by, strings.ReplaceAll(NoteLetters(c.Answer.Ran.Command()), "\n", "\n  ")))
			default:
				out = append(out, fmt.Sprintf("  %s approved, ran: %s", by, strings.ReplaceAll(NoteLetters(c.Answer.Ran.Command()), "\n", "\n  ")))
			}
			out = append(out, c.Answer.Ran.resultLines()...)
			out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.Run.TakenBack()), "\n", "\n  "))
		case r.Ask.Setup != nil && c.Answer.Ran != nil:
			// An approved setup the server has not carried out yet reads
			// as waiting, and one whose carrying out failed as failed:
			// the three readings of a setup ask, as a run ask's are.
			preset := ""
			if c.Answer.Option >= 1 && c.Answer.Option <= len(r.Ask.Options) {
				preset = OneLine(r.Ask.Options[c.Answer.Option-1].Label)
			}
			what := fmt.Sprintf("setup of pack %s, preset %s to %s",
				OneLine(r.Ask.Setup.Pack), preset, OneLine(r.Ask.Setup.Grantees()))
			if c.Answer.Account != "" {
				what += "; took " + OneLine(c.Answer.Account)
			} else if len(c.Answer.Given) > 0 {
				what += "; gave " + OneLine(strings.Join(c.Answer.Given, " and "))
			} else {
				what += "; kept"
			}
			switch {
			case c.Answer.Ran.Failed:
				out = append(out, fmt.Sprintf("  %s approved, failed: %s", by, what))
			case c.Answer.Ran.Result == CarryPending:
				out = append(out, fmt.Sprintf("  %s approved, to be carried out: %s", by, what))
			default:
				out = append(out, fmt.Sprintf("  %s approved, ran: %s", by, what))
			}
			out = append(out, c.Answer.Ran.resultLines()...)
			out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.Setup.TakenBack()), "\n", "\n  "))
		case r.Ask.DepotPush != nil && c.Answer.Ran != nil:
			what := "depot-push " + OneLine(r.Ask.DepotPush.Depot)
			if r.Ask.DepotPush.Off {
				what = "depot-push-off " + OneLine(r.Ask.DepotPush.Depot)
			}
			switch {
			case c.Answer.Ran.Failed:
				out = append(out, fmt.Sprintf("  %s approved, failed: %s", by, what))
			case c.Answer.Ran.Result == CarryPending:
				out = append(out, fmt.Sprintf("  %s approved, being carried out: %s", by, what))
			default:
				out = append(out, fmt.Sprintf("  %s approved, ran: %s", by, what))
			}
			out = append(out, c.Answer.Ran.resultLines()...)
			out = append(out, "  "+strings.ReplaceAll(NoteLetters(r.Ask.DepotPush.TakenBack()), "\n", "\n  "))
		case r.Ask.Secret != nil:
			out = append(out, fmt.Sprintf("  %s gave %s", by, OneLine(strings.Join(c.Answer.Given, " and "))))
		case len(r.Ask.Options) == 0:
			out = append(out, fmt.Sprintf("  %s answered: %s", by, strings.ReplaceAll(NoteLetters(c.Answer.Words), "\n", "\n        ")))
		default:
			prev := 0
			for _, a := range r.Answers {
				if a.Note.Seq == c.Note.Seq {
					break
				}
				// Only the immediately current answer is replaced. A later
				// answer puts the ask aside, so a pick after it is not a
				// change to the pick that preceded it.
				prev = a.Answer.Option
			}
			was := ""
			if prev != 0 {
				was = fmt.Sprintf(" (was %d until %s)", prev, c.Note.At.Local().Format("15:04"))
			}
			label := ""
			if c.Answer.Option >= 1 && c.Answer.Option <= len(r.Ask.Options) {
				label = " (" + OneLine(r.Ask.Options[c.Answer.Option-1].Label) + ")"
			}
			line := fmt.Sprintf("  %s picked %d%s%s", by, c.Answer.Option, was, label)
			if c.Answer.Words != "" {
				line += ": " + strings.ReplaceAll(NoteLetters(c.Answer.Words), "\n", "\n        ")
			}
			out = append(out, line)
		}
	case AskAside:
		out = append(out, fmt.Sprintf("  back sooner if %s writes", OneLine(r.Note.Author)))
	case AskDeclined:
		c := *r.Current
		c.Note.Author = ViaMark(who(c.Note.Author), c.Note.Via)
		out = append(out, "  "+declinedLine(c))
	case AskTakenBack:
		c := *r.Current
		c.Note.Author = ViaMark(who(c.Note.Author), c.Note.Via)
		out = append(out, "  "+takenBackLine(c))
	}
	switch {
	case r.State == AskDeclined, r.State == AskTakenBack:
	case r.State == AskAnswered && len(r.Ask.Options) == 0:
	case r.Ask.Setup != nil && r.Ask.Setup.Given && len(r.Ask.Setup.Missing) == 0 && len(r.Ask.Setup.Candidates) > 0:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n>, <n> --secret, or <n> --account <source>", r.Note.Bead, r.Note.Seq))
	case r.Ask.Setup != nil && r.Ask.Setup.Given && len(r.Ask.Setup.Missing) == 0:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n> or <n> --secret", r.Note.Bead, r.Note.Seq))
	case r.Ask.Setup != nil && len(r.Ask.Setup.Candidates) > 0:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n> --secret, or <n> --account <source>", r.Note.Bead, r.Note.Seq))
	case r.Ask.Setup != nil:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n> --secret", r.Note.Bead, r.Note.Seq))
	case r.Ask.Run != nil:
		out = append(out, fmt.Sprintf("  approve with: yardr bead answer %s %d --approve", r.Note.Bead, r.Note.Seq))
	case r.Ask.Secret != nil:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d --secret", r.Note.Bead, r.Note.Seq))
	case len(r.Ask.Options) == 0:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d --words \"<text>\"", r.Note.Bead, r.Note.Seq))
	default:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n> [words]", r.Note.Bead, r.Note.Seq))
	}
	return out
}

// NoteBlocksMarked renders a bead's notes for humans: a plain note as its
// time, author and text, an ask as numbered text with its state, an answer
// folded into its ask. An answer that names no ask of the bead says so
// where it stands. Nobody reads the raw form. A note with files names
// them under its text, on a line of their own starting with the mark
// (NoteFilesLine), each by its path in the yard at home; the
// mark is kept out of every word a note's writer chose, so only that
// line names a file the yard keeps. An answer's author from another yard
// is marked as mail is (LinesMarked).
//
// A note's text may come from another yard, or from a device on the
// listener, and says nothing of who wrote it: the head line does. So in
// every block the head stands alone at the margin and the writer's
// words stand further in, as letters only (NoteLetters), where no line
// of them can be taken for the start of another note.
func NoteBlocksMarked(notes []Note, now time.Time, as map[string]string, home string) []string {
	mark := func(a string) string { return MarkedAuthor(a, as) }
	return blocks(notes, now, home, mark)
}

func blocks(notes []Note, now time.Time, home string, mark func(string) string) []string {
	who := func(a string) string {
		if mark == nil {
			return a
		}
		return mark(a)
	}
	resolved := ResolveAsks(notes, now)
	byAsk := map[uint64]ResolvedAsk{}
	claimed := map[uint64]bool{}
	for _, r := range resolved {
		byAsk[r.Note.Seq] = r
		for _, a := range r.Answers {
			claimed[a.Note.Seq] = true
		}
	}
	var out []string
	for _, n := range notes {
		if n.Kind == NoteAnswer && claimed[n.Seq] {
			continue
		}
		if r, ok := byAsk[n.Seq]; ok {
			// An ask carries no files; its question, context and
			// answers are the writers' words, shown as letters only,
			// so none forges the marked line a note's files stand on.
			out = append(out, NoteLetters(strings.Join(r.lines(mark), "\n")))
			continue
		}
		if n.Kind == NoteAnswer {
			out = append(out, NoteLetters(fmt.Sprintf("[%s %s]\n  answer %d names no ask of this bead",
				n.At.Local().Format("2006-01-02 15:04"), OneLine(who(n.Author)), n.Seq)))
			continue
		}
		text := strings.ReplaceAll(strings.TrimSpace(NoteLetters(n.Text)), "\n", "\n  ")
		block := NoteLetters(fmt.Sprintf("[%s %s]\n  %s",
			n.At.Local().Format("2006-01-02 15:04"), OneLine(ViaMark(who(n.Author), n.Via)), text))
		if line := NoteFilesLine(home, n.Files); line != "" {
			block += "\n  " + line
		}
		out = append(out, block)
	}
	return out
}

// askAge is how long ago t was, coarsely.
func askAge(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
