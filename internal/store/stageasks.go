// Stage asks are put by the yard on entering a templated manual stage, and
// taken back on leaving it. A manual stage may carry an ask template, an ##
// Ask section at the end of its stage file (asktemplate.go): when a bead
// enters the stage, the yard puts an ask on it in the same write, to the
// stage's people, built from the template; when the bead leaves, the yard
// takes the stage's open and put-aside asks back in the same write, with
// the move named. Answered and declined asks stay as history.
package store

import (
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
// nothing.
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
		n, err := w.storeStageAsk(row, tmpl.stageAsk(member, stage, entry))
		if err != nil {
			return err
		}
		if err := w.queueAskPush(member, row.UUID, n.Seq, ""); err != nil {
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
	n, err := w.storeStageAsk(row, tmpl.stageAsk(operator, stage, entry))
	if err != nil {
		return err
	}
	return w.queueAskPush(operator, row.UUID, n.Seq, "")
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
	sent := ask
	sent.To = member
	body, err := json.Marshal(peerAsk{
		V: PeerAskVersion, To: to, From: stageAskAuthor,
		Bead: row.ID, Title: row.Title, Topic: topicOf(row.Bead), Stage: row.Stage,
		Labels: row.Labels, Body: cutNote(row.Body),
		Seq: n.Seq, Since: n.At, Ask: sent,
	})
	if err != nil {
		return false, err
	}
	m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Body: body}
	if err := m.check(); err != nil {
		return false, err
	}
	out, err := w.queuePeer(p, m, stageAskAuthor)
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
