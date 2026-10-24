// Stage asks are put by the yard on entering a templated manual stage, and
// taken back on leaving it. A manual stage may carry an ask template, an ##
// Ask section at the end of its stage file (asktemplate.go): when a bead
// enters the stage, the yard puts an ask on it in the same write, to the
// stage's people, built from the template; when the bead leaves, the yard
// takes the stage's open and put-aside asks back in the same write, with
// the move named. Answered and declined asks stay as history.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// The yard asks in its own name, as the mail it sends is authored.
const stageAskAuthor = "yard"

// A broken template puts no ask: the bead enters as today, and the yard
// logs one badTemplateEvent naming the stage and the error, as bad_flow
// does for a flow. A remote member whose link is missing or does not carry
// ask out gets no ask, and the put logs one askUnreachableEvent naming the
// bead, the stage and the member.
const (
	badTemplateEvent    = "bad_template"
	askUnreachableEvent = "ask_unreachable"
)

// stageHome is the stage files of a store flow pre-read from the yard's
// home before a write: git and disk never run inside one. flow names the
// flow they were read for; files maps each stage that has a file to it,
// and a stage without one is absent.
type stageHome struct {
	flow  string
	files map[string]*StageFile
}

// stageHomeFor reads the stage files of the store flow flowName for stages
// from the yard's home, as they are on disk.
func (s *Store) stageHomeFor(flowName string, stages []string) (*stageHome, error) {
	home := &stageHome{flow: flowName, files: map[string]*StageFile{}}
	for _, st := range stages {
		sf, err := HomeStageFile(s.home, flowName, st)
		if err != nil {
			return nil, err
		}
		if sf != nil {
			home.files[st] = sf
		}
	}
	return home, nil
}

// stageHomeBefore reads the store flow's stage files before a write that may
// enter a stage of it: the flow governing pre, resolved with repo. Nil for
// a bead no flow governs, or a repository flow, whose files travel with
// repo. A flow that does not resolve reads none: the write fails with its
// error, or tries again when home is stale.
func (s *Store) stageHomeBefore(pre *Bead, repo *RepoFlows) (*stageHome, error) {
	var f *Flow
	var src *FlowSource
	_ = s.read(func(tx *bolt.Tx) error {
		f, src, _ = resolveFlow(tx, pre.Depot, pre.Type, PinnedFlow(pre), repo)
		return nil
	})
	if f == nil || src == nil || !src.InStore() {
		return nil, nil
	}
	return s.stageHomeFor(f.Name, f.Stages)
}

// stageTemplate is the ask template of stage in f, which came from src:
// through repo for a repository flow, through home for a store flow.
// Broken is why the section is broken, a parse error or an outcome that
// does not resolve under f; nil with no error when the stage has no
// template. Fatal is changedMeanwhile when home is stale, the flow it was
// read for is not f: the write is tried again with fresh files.
func stageTemplate(f *Flow, src *FlowSource, repo *RepoFlows, home *stageHome, stage string) (tmpl *AskTemplate, broken, fatal error) {
	if f == nil {
		return nil, nil, nil
	}
	var sf *StageFile
	if src.InStore() {
		if home == nil || home.flow != f.Name {
			return nil, nil, changedMeanwhile(errNeedRepo)
		}
		sf = home.files[stage]
	} else {
		if repo == nil {
			return nil, nil, changedMeanwhile(errNeedRepo)
		}
		sf = repo.StageFile(f.Name, stage)
	}
	if sf == nil {
		return nil, nil, nil
	}
	tmpl, err := sf.Ask()
	if err != nil {
		return nil, err, nil
	}
	if tmpl == nil {
		return nil, nil, nil
	}
	if err := tmpl.Check(f, stage); err != nil {
		return nil, err, nil
	}
	return tmpl, nil, nil
}

// stageAsk builds the ask to puts on entering stage, to who, with entry
// the seq of the event that entered the bead: the template's question and
// context, the options per kind, frozen with the stage, the move per stored
// option and the entry. Choice offers each outcome with its consequence;
// input wants words with its move as the single entry; check offers Done,
// whose consequence is fixed, firing its move. YouDecide is last wherever
// there are options, with an empty move, as newAsk adds it.
func (t *AskTemplate) stageAsk(to, stage string, entry uint64) Ask {
	context := slices.Clone(t.Context)
	switch t.Kind {
	case AskChoice:
		options := make([]AskOption, 0, len(t.Options)+1)
		move := make([]string, 0, len(t.Options)+1)
		for _, o := range t.Options {
			options = append(options, AskOption{Label: o.Outcome, Consequence: o.Consequence})
			move = append(move, o.Outcome)
		}
		options = append(options, AskOption{Label: YouDecide, Consequence: "leave it to " + stageAskAuthor})
		move = append(move, "")
		return Ask{Question: t.Question, To: to, Context: context, Options: options,
			Recommend: t.Recommend, Why: t.Why, Stage: stage, Move: move, Entry: entry}
	case AskCheck:
		done := fmt.Sprintf("the bead leaves %s (outcome %s)", stage, t.Move)
		return Ask{Question: t.Question, To: to, Context: context,
			Options: []AskOption{
				{Label: "Done", Consequence: done},
				{Label: YouDecide, Consequence: "leave it to " + stageAskAuthor},
			},
			Stage: stage, Move: []string{t.Move, ""}, Entry: entry}
	default: // AskInput wants words; its move is the single entry.
		return Ask{Question: t.Question, To: to, Context: context,
			Stage: stage, Move: []string{t.Move}, Entry: entry}
	}
}

// putStageAsks puts the ask tmpl builds on entering stage, with entry the
// seq of the event that entered the bead row: one ask per user-member of
// the stage's manual group, authored yard. A member naming another yard
// gets one ask addressed across, queued directly as the yard's own traffic
// the way settles are; a remote member whose link is missing or does not
// carry ask out gets no ask, and the put logs one attention event naming
// the bead, the stage and the member. Crew members and names that are no
// user have no decisions queue and are skipped. When no member got an ask,
// the ask goes to the operator. Entering a stage that already has an open
// or put-aside ask of that stage puts none, so the put is idempotent. A
// broken template puts no ask and logs one event naming the stage and the
// error; a stage without a template, or one no manual group works, puts
// nothing. A local user's ask follows their configured peer reach, as any
// ask for a user of the yard does.
func (w *wtx) putStageAsks(row *beadRow, stage string, entry uint64, tmpl *AskTemplate, broken error) error {
	if broken != nil {
		return w.event(row.UUID, badTemplateEvent, stageAskAuthor, map[string]any{
			"stage": stage, "reason": broken.Error()})
	}
	if tmpl == nil {
		return nil
	}
	g, err := stageGroup(w.Tx, row)
	if err != nil {
		return err
	}
	if g == nil || g.Runner != "manual" {
		return nil
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.AskErr == nil && r.Ask.Stage == stage && (r.State == AskOpen || r.State == AskAside) {
			return nil
		}
	}
	got := 0
	for _, member := range g.Members {
		if strings.Contains(member, "@") {
			ok, err := w.putRemoteAsk(row, tmpl, stage, entry, member)
			if err != nil {
				return err
			}
			if ok {
				got++
			}
			continue
		}
		if w.Bucket(bCrew).Get([]byte(member)) != nil {
			continue
		}
		if w.Bucket(bUsers).Get([]byte(member)) == nil {
			continue
		}
		if err := w.putLocalStageAsk(row, tmpl.stageAsk(member, stage, entry)); err != nil {
			return err
		}
		got++
	}
	if got > 0 {
		return nil
	}
	operator, err := operatorIn(w.Tx)
	if err != nil || operator == "" {
		return err
	}
	ask := tmpl.stageAsk(operator, stage, entry)
	ask.Fallback = true
	return w.putLocalStageAsk(row, ask)
}

// putLocalStageAsk puts ask in the local user's queue and, as any ask for a
// user of this yard, copies it to names the links give reach for that user.
// The copies are kept so a later take-back reaches the same queues.
func (w *wtx) putLocalStageAsk(row *beadRow, ask Ask) error {
	n, err := w.storeStageAsk(row, ask)
	if err != nil {
		return err
	}
	if err := w.queueAskPush(ask.To, row.UUID, n.Seq, ""); err != nil {
		return err
	}
	var peers []Peer
	if err := each(w.Tx, bPeers, "", func(_ []byte, p *Peer) error {
		peers = append(peers, *p)
		return nil
	}); err != nil {
		return err
	}
	var copies []askCopy
	for _, target := range AskReach(peers, ask.To, stageAskAuthor) {
		i := slices.IndexFunc(peers, func(p Peer) bool { return p.Name == target.Peer })
		if i < 0 {
			continue // AskReach only names peers in this list.
		}
		out, err := w.queueStageAskPeer(row, ask, n, &peers[i], target.Name)
		if err != nil {
			if err := w.copyUndelivered(ask.To, target.Address(), ask.Question, row.ID, n.Seq, "", err.Error()); err != nil {
				return err
			}
		} else if out != nil {
			copies = append(copies, askCopy{Key: peers[i].Key, Name: target.Name, As: ask.To, Message: out.ID})
		}
	}
	if len(copies) == 0 {
		return nil
	}
	return put(w.Tx, bAskCopies, join(row.UUID, seqKey(n.Seq)), copies)
}

// putRemoteAsk puts tmpl's ask for the remote member, addressed across to
// name@peer: stored on the bead here and queued to that yard, where it
// stands among the named person's decisions. The yard asking for its own
// stage is none of the CLI's askers, so none of their gates is asked: the
// link must only be one and carry ask out. A member whose link is missing
// or carries none gets no ask, and the put logs one event naming the bead,
// the stage and the member. It reports whether the member got an ask.
func (w *wtx) putRemoteAsk(row *beadRow, tmpl *AskTemplate, stage string, entry uint64, member string) (bool, error) {
	to, peer, _ := strings.Cut(member, "@")
	unreachable := func(reason string) error {
		return w.event(row.UUID, askUnreachableEvent, stageAskAuthor, map[string]any{
			"stage": stage, "member": member, "reason": reason})
	}
	if strings.Count(member, "@") != 1 || !peerMailName.MatchString(to) {
		return false, unreachable(fmt.Sprintf("member %s is no person of another yard: want <name>@<peer>", member))
	}
	p, err := loadPeer(w.Tx, peer)
	if err != nil {
		return false, unreachable(err.Error())
	}
	if !p.carries(PeerAsk, false) {
		return false, unreachable(fmt.Sprintf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer))
	}
	ask := tmpl.stageAsk(member, stage, entry)
	n, err := w.storeStageAsk(row, ask)
	if err != nil {
		return false, err
	}
	out, err := w.queueStageAskPeer(row, ask, n, p, to)
	if err != nil {
		return false, err
	}
	if out == nil {
		// Checked above, in the same write: the link cannot have
		// stopped carrying ask since.
		return false, unreachable(fmt.Sprintf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer))
	}
	return true, put(w.Tx, bAskCopies, join(row.UUID, seqKey(n.Seq)), []askCopy{{Key: p.Key, Name: to, Message: out.ID}})
}

// queueStageAskPeer sends the yard's stage ask to name on p. A direct remote
// member and a local member reached through p use the same peer ask path.
func (w *wtx) queueStageAskPeer(row *beadRow, ask Ask, n *Note, p *Peer, name string) (*PeerOut, error) {
	sent := ask
	sent.To = name + "@" + p.Name
	body, err := json.Marshal(peerAsk{
		V: PeerAskVersion, To: name, From: stageAskAuthor,
		Bead: row.ID, Title: row.Title, Topic: topicOf(row.Bead), Stage: row.Stage,
		Labels: row.Labels, Body: cutNote(row.Body),
		Seq: n.Seq, Since: n.At, Ask: sent,
	})
	if err != nil {
		return nil, err
	}
	m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Body: body}
	if err := m.check(); err != nil {
		return nil, err
	}
	return w.queuePeer(p, m, stageAskAuthor)
}

// storeStageAsk stores ask on the bead row as a note of kind ask, written
// by the yard, and answers the note.
func (w *wtx) storeStageAsk(row *beadRow, ask Ask) (*Note, error) {
	text, err := json.Marshal(ask)
	if err != nil {
		return nil, err
	}
	return w.addNoted(row, NoteAsk, string(text), stageAskAuthor)
}

// takeBackStageAsks takes back the open and put-aside asks put for stage on
// the bead row, in the same write, authored yard with reason: the bead left
// the stage. Answered and declined asks stay as history. Where an ask also
// stands in another yard, addressed across, that yard is told in the same
// write.
func (w *wtx) takeBackStageAsks(row *beadRow, stage, reason string) error {
	return w.takeBackStage(row, reason, func(ask Ask) bool { return ask.Stage == stage })
}

// takeBackAllStageAsks takes back every open and put-aside stage ask on the
// bead row, whatever stage it was put for: the bead closed.
func (w *wtx) takeBackAllStageAsks(row *beadRow, reason string) error {
	return w.takeBackStage(row, reason, func(ask Ask) bool { return ask.Stage != "" })
}

func (w *wtx) takeBackStage(row *beadRow, reason string, match func(Ask) bool) error {
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.AskErr != nil || !match(r.Ask) || (r.State != AskOpen && r.State != AskAside) {
			continue
		}
		ans := Answer{Ask: r.Note.Seq, Words: reason, TakeBack: true}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		if _, err = w.addNoted(row, NoteAnswer, string(text), stageAskAuthor); err != nil {
			return err
		}
		if err := w.settleCopies(row, ans, nil, ""); err != nil {
			return err
		}
	}
	return nil
}

// latestStayEntry is the seq of the event that entered the bead into its
// stage: the latest of its created, advanced and reopened events, which a
// stage ask's Entry names. Closing ends no stay and starts none.
func latestStayEntry(tx *bolt.Tx, uuid string) (uint64, error) {
	var entry uint64
	for _, k := range keys(tx, bEventsBy, uuid+"\x00") {
		var e Event
		if ok, err := get(tx, bEvents, k[len(uuid)+1:], &e); err != nil {
			return 0, err
		} else if !ok {
			continue
		}
		switch e.Kind {
		case "created", "advanced", "reopened":
			entry = max(entry, e.Seq)
		}
	}
	return entry, nil
}

// stayTemplateError is why the bead's stay in stage put no stage ask: the
// bad_template event's reason logged for it since its entry, "" when the
// entry put its asks. entry is latestStayEntry; an event of an earlier stay
// is history, as a removed or fixed template's is.
func stayTemplateError(tx *bolt.Tx, uuid, stage string, entry uint64) (string, error) {
	if entry == 0 {
		return "", nil
	}
	var reason string
	var at uint64
	for _, k := range keys(tx, bEventsBy, uuid+"\x00") {
		var e Event
		if ok, err := get(tx, bEvents, k[len(uuid)+1:], &e); err != nil {
			return "", err
		} else if !ok {
			continue
		}
		if e.Kind != badTemplateEvent || e.Seq <= entry || e.Seq < at {
			continue
		}
		if s, _ := e.Data["stage"].(string); s != stage {
			continue
		}
		if r, _ := e.Data["reason"].(string); r != "" {
			reason, at = r, e.Seq
		}
	}
	return reason, nil
}

// stageMove is the outcome a pick of option n, 1-based into the stage ask's
// options, reports: "" for You decide, which moves nothing, and for an
// option past the moves, which a corrupt ask moves nothing for.
func stageMove(ask Ask, n int) string {
	if n < 1 || n > len(ask.Move) {
		return ""
	}
	return ask.Move[n-1]
}

// stageWordsMove is the outcome words on the stage ask report: an input
// ask's single entry, "" for move none and for none.
func stageWordsMove(ask Ask) string {
	if len(ask.Move) == 0 || ask.Move[0] == AskMoveNone {
		return ""
	}
	return ask.Move[0]
}

// stageAnswerAuth reports whether actor may answer the stage ask ask on row:
// who may move the bead out of the stage by hand (the same check), a name
// the links trust as such a member, or the ask's addressee where the yard
// put the ask to the operator as fallback. A group with no members refuses
// nobody, as on the hand path. Who asks is the store's own word before the
// extras, as checkMembers reads it: a bead's agent answering as a member,
// and a crew member answering as another name, are refused whatever the
// extras say. The error is checkMembers', word for word.
func (w *wtx) stageAnswerAuth(ctx context.Context, row *beadRow, ask Ask, actor string) error {
	g, err := stageGroup(w.Tx, row)
	if err != nil {
		return err
	}
	return w.may(ctx, actor, ActAnswerStageAsk, Object{
		Kind: KindBead, Name: row.ID, Group: g, To: ask.To, Fallback: ask.Fallback, row: row,
	})
}

// stageAnswerSpoofTx is whether the answer's caller is who checkMembers
// refuses whatever name it answers under: a bead's agent session, or a
// crew pane naming another name.
func stageAnswerSpoofTx(ctx context.Context, tx *bolt.Tx, row *beadRow, actor string) (bool, error) {
	sess, err := callerSessionTx(tx, callerOf(ctx), Expect{}, row)
	if err != nil {
		return false, err
	}
	if sess != nil && !sess.Manual() {
		return true, nil
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil {
		return false, err
	}
	return r != nil && r.Name != actor, nil
}

// stageAnswer decides what ans, by actor, does to the stage ask it names
// among notes, the notes of the bead row: the outcome to report through
// Done in the write that stores the answer, "" to record without moving.
// answerable has passed: the ask is one, and takes this shape of answer.
// With a refusal it answers what the other yard is told of one that came
// from there: RefusedAskAnswered for a pick the ask takes no more, which
// ends its copy as answered, "" for an answer that is none of its members'
// to give.
//
// Every answer on a stage ask is its members' to give: a pick, words,
// Later or Decline from anyone else is refused whole, with the membership
// reason. Later and Decline need no further rule: they move nothing. A
// pick moves only for the stay that put it: one whose current answer moved
// takes no further pick, one for another stage names both, and one for an
// earlier stay names the fresh ask where one stands, else says the template
// went or broke and the bead is moved by hand. Picking You decide records
// without moving, as do words with no move. A closed bead moves no more.
func (w *wtx) stageAnswer(ctx context.Context, row *beadRow, notes []Note, ans Answer, actor string, repo *RepoFlows, home *stageHome) (outcome, reason string, err error) {
	var current *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == ans.Ask {
			current = &resolved[i]
		}
	}
	if current == nil || current.AskErr != nil {
		// answerable passed, so neither happens; refused as no ask.
		return "", RefusedNoSuchAsk, fmt.Errorf("bead %s has no ask %d", row.ID, ans.Ask)
	}
	ask := current.Ask
	if ask.Stage == "" {
		return "", "", nil
	}
	if err := w.stageAnswerAuth(ctx, row, ask, actor); err != nil {
		return "", "", err
	}
	if ans.Later || ans.Decline {
		return "", "", nil
	}
	move := ""
	if ans.Option != 0 {
		move = stageMove(ask, ans.Option)
	} else {
		move = stageWordsMove(ask)
	}
	if row.Status == "closed" && (ans.Option != 0 || move != "") {
		return "", RefusedAskAnswered, fmt.Errorf("ask %d: bead %s is closed: a closed bead moves no more", ans.Ask, row.ID)
	}
	if current.State == AskAnswered && current.Current != nil && current.Current.Answer.Option != 0 &&
		stageMove(ask, current.Current.Answer.Option) != "" {
		return "", RefusedAskAnswered, fmt.Errorf("ask %d already moved bead %s with the outcome %s: a stage ask takes no pick after one that moved it",
			ans.Ask, row.ID, stageMove(ask, current.Current.Answer.Option))
	}
	if ask.Stage != row.Stage {
		return "", RefusedAskAnswered, fmt.Errorf("ask %d was put for the stage %s, and bead %s is in %s: a stage ask takes a pick only while the bead is in its stage",
			ans.Ask, ask.Stage, row.ID, row.Stage)
	}
	entry, err := latestStayEntry(w.Tx, row.UUID)
	if err != nil {
		return "", "", err
	}
	if ask.Entry != entry {
		return w.earlierStay(row, notes, current, actor, entry, repo, home)
	}
	return move, "", nil
}

// stageOutcome checks that outcome still moves the bead row under its
// flow: what the move would refuse, before an answer arriving over a link
// is stored, since a refusal there commits. errNeedRepo is a flow read
// for another configuration meanwhile, the move's to retry; any other
// error refuses the answer.
func (w *wtx) stageOutcome(row *beadRow, outcome string, repo *RepoFlows) error {
	f, _, err := resolveFlow(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
	if err != nil {
		return err
	}
	to, err := donePick(outcome, nil)(f, row, map[string]any{})
	if err != nil {
		return err
	}
	return checkWagon(w.Tx, f, row, to)
}

// earlierStay refuses a pick on a stage ask put for the bead row's stage in
// an earlier stay: it names the fresh ask where one stands open, else says
// the template went or broke and the bead is moved by hand. entry is the
// stay's, which the ask's Entry is not.
func (w *wtx) earlierStay(row *beadRow, notes []Note, current *ResolvedAsk, actor string, entry uint64, repo *RepoFlows, home *stageHome) (string, string, error) {
	seq := current.Note.Seq
	var fresh *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		r := &resolved[i]
		if r.AskErr != nil || r.Note.Seq == seq || r.Ask.Stage != row.Stage || r.Ask.Entry != entry ||
			(r.State != AskOpen && r.State != AskAside) {
			continue
		}
		if fresh == nil || r.Ask.To == actor {
			fresh = r
		}
	}
	if fresh != nil {
		return "", RefusedAskAnswered, fmt.Errorf("ask %d was put for an earlier stay in %s: ask %d was put for this one",
			seq, row.Stage, fresh.Note.Seq)
	}
	byHand := fmt.Sprintf("move the bead by hand (yardr bead advance %s --to <stage>)", row.ID)
	none := fmt.Errorf("ask %d was put for an earlier stay in %s, and no ask stands for this one: %s",
		seq, row.Stage, byHand)
	f, src, err := resolveFlow(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
	if err != nil {
		// The stay is over whatever the flow says; the hand move names
		// the flow's error where it is refused.
		return "", RefusedAskAnswered, none
	}
	tmpl, broken, fatal := stageTemplate(f, src, repo, home, row.Stage)
	switch {
	case broken != nil:
		return "", RefusedAskAnswered, fmt.Errorf("ask %d was put for an earlier stay in %s, and its ask template is broken (%s): %s",
			seq, row.Stage, broken.Error(), byHand)
	case fatal == nil && tmpl == nil:
		return "", RefusedAskAnswered, fmt.Errorf("ask %d was put for an earlier stay in %s, and the stage has no ask template any more: %s",
			seq, row.Stage, byHand)
	default:
		// A valid template with no fresh ask (taken back by hand, or put
		// with no operator to fall back to), or files read for another
		// flow meanwhile: no ask stands either way.
		return "", RefusedAskAnswered, none
	}
}
