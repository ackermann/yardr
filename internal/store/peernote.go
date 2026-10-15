package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// While a bead of this yard is at another, someone here may write a note on
// that yard's bead (docs/peers.md, "Answering on the other yard's bead").
// This file is both sides of that: the message the asking yard queues
// (PeerNote), and what the working yard does with one it takes (noteBead):
// the note, and where people work the first stage of the bead's flow, the
// move back to it (noteBack).
//
// PeerBeadNote is the kind of the message, which PeerThread allows. It
// names the PeerBead message of the stay in its Re and goes against the
// bead events, from the asking yard that sends thread to the working yard
// that receives it (Peer.carries).
const PeerBeadNote = "bead-note"

// RefusedNotNote is the refusal of a note that is none: a body that does
// not read, no text, a writer's name that is no name, or no message named
// in its Re.
const RefusedNotNote = "not_a_note"

// PeerNoteMessage is the body of a message of kind PeerBeadNote: From is
// who the asking yard says wrote it, and Text the note.
type PeerNoteMessage struct {
	V    int    `json:"v"`
	From string `json:"from"`
	Text string `json:"text"`
}

// PeerNoted is a note that waits for the yard a bead is at: the message's
// ID, this yard's bead, the peer, and the peer's handle for its bead, ""
// until a bead event or a read's answer has named it.
type PeerNoted struct {
	ID     string `json:"id"`
	Bead   string `json:"bead"`
	Peer   string `json:"peer"`
	Theirs string `json:"theirs,omitempty"`
}

// peerWrote is a note someone of this yard wrote on the bead a peer made
// of one of this yard's, as the record of the stay keeps it: By is who
// wrote it and Sum the SHA-256 of its text, in hex. The text is not kept:
// it is on the peer's bead, and comes back with a read and the return.
type peerWrote struct {
	By  string `json:"by"`
	Sum string `json:"sum"`
}

func noteSum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// wroteHere says whether n, a note the peer of the stay s sent among those
// written on its bead, is one that someone of this yard wrote there, and
// who. The peer has it by <name>@<what it calls this yard>, which is no
// name here and says nothing this yard can check, so it is known by what
// this yard kept when it sent the note: that writer, and that text to the
// byte. A peer can make no other note this yard's own by it, only say
// again what the writer said. s is nil for notes of no stay.
func (s *peerSent) wroteHere(n PeerNote) (string, bool) {
	name, _, at := strings.Cut(n.Author, "@")
	if s == nil || !at || !slices.Contains(s.Wrote, peerWrote{By: name, Sum: noteSum(n.Text)}) {
		return "", false
	}
	return name, true
}

// PeerBack says of a bead that came from another yard that a note from
// that yard sent it back to the first stage of its flow: the stage it Left,
// who wrote the note (By, the name the peer gave) and when it came (At). It
// is on the bead's record until the bead leaves the stage again.
type PeerBack struct {
	Left string    `json:"left"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// PeerNote queues a note by actor on the bead that the yard the bead is at
// made of it: a message of kind PeerBeadNote in the outbox for that peer,
// which the daemon delivers or the peer asks for. It logs peer_bead_noted
// on this yard's bead, without the text, and a crew member that wrote it
// takes part in the bead's thread of SourcePeer from then on, as after any
// reply.
//
// It is refused, with nothing queued, for a bead that is not away, for an
// actor who may not (mayAtPeer: mail's rule, a crew member needs the peer
// in its MailPeers setting, a user needs nothing, a bead's agent is
// refused), and over a link that does not send PeerThread. From a
// session's or a crew member's pane the note goes as that session's actor
// or that crew member, whatever actor says; from anywhere else, as given
// (paneAuthor).
func (s *Store) PeerNote(ctx context.Context, bead, text, actor string) (*PeerNoted, error) {
	switch {
	case strings.TrimSpace(text) == "" || len(text) > MaxPeerNoteBytes:
		return nil, fmt.Errorf("a note on another yard's bead must be 1..%d bytes", MaxPeerNoteBytes)
	case !utf8.ValidString(text):
		return nil, fmt.Errorf("a note on another yard's bead must be text")
	case !peerMailName.MatchString(actor):
		return nil, fmt.Errorf("a note from %q: a name of letters, digits, '.', '_', '-' and '/' is what another yard is told", actor)
	}
	var out *PeerNoted
	err := s.writeVia(ctx, func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		row, err := w.loadBead(bead)
		if err != nil {
			return err
		}
		sent, p, err := awayStay(w.Tx, row)
		if err != nil {
			return err
		}
		if err := mayAtPeer(ctx, w.Tx, actor, p.Name, "write on a bead of"); err != nil {
			return err
		}
		body, err := json.Marshal(PeerNoteMessage{V: PeerBeadVersion, From: actor, Text: text})
		if err != nil {
			return err
		}
		if !p.carries(PeerBeadNote, false) {
			return fmt.Errorf("peer %s: the link does not carry %s out, and a note on its bead goes out: the operator allows it with yardr peer allow %s --send %s",
				p.Name, PeerThread, p.Name, PeerThread)
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerBeadNote, Re: sent.Message, Body: body}
		if err := m.check(); err != nil {
			return err
		}
		if _, err := w.queuePeer(p, m, actor); err != nil {
			return err
		}
		// By this the note is known as this yard's when the peer sends it
		// back among what was written on its bead (wroteHere).
		if wrote := (peerWrote{By: actor, Sum: noteSum(text)}); !slices.Contains(sent.Wrote, wrote) {
			sent.Wrote = append(sent.Wrote, wrote)
			if err := put(w.Tx, bPeerSent, row.UUID, sent); err != nil {
				return err
			}
		}
		out = &PeerNoted{ID: m.ID, Bead: row.ID, Peer: p.Name, Theirs: sent.Theirs}
		data := map[string]any{"peer": p.Name, "id": m.ID, "re": sent.Message, "direction": "out"}
		if sent.Theirs != "" {
			data["to"] = sent.Theirs
		}
		if err := w.event(row.UUID, "peer_bead_noted", actor, data); err != nil {
			return err
		}
		// Whoever answered in a thread takes part in it (Replied).
		if w.Bucket(bCrew).Get([]byte(actor)) == nil || participates(w.Tx, actor, SourcePeer, row.ID) {
			return nil
		}
		part := Participation{CrewMember: actor, Source: SourcePeer, Thread: row.ID, Title: row.Title, How: ParticipantReplied, Since: now()}
		if sent.Theirs != "" {
			part.URL = peerIssue(sent.Theirs, p.Name)
		}
		return put(w.Tx, bThreads, threadKey(actor, SourcePeer, row.ID), part)
	})
	return out, err
}

// noteFlows are the repository flows that moving the beads the notes and
// answers among msgs are for depends on, by the bead's UUID, read before
// the write that takes them: git does not run inside one. A note for no
// bead of the peer name has none, and neither has a bead whose flow is the
// store's; an answer names its bead in the body.
func (s *Store) noteFlows(ctx context.Context, name string, msgs []PeerMessage) map[string]*RepoFlows {
	var rows []*beadRow
	_ = s.read(func(tx *bolt.Tx) error {
		p, err := loadPeer(tx, name)
		if err != nil {
			return nil // said where the messages are taken
		}
		for _, m := range msgs {
			rows = append(rows, takingRows(tx, p, m)...)
		}
		return nil
	})
	var out map[string]*RepoFlows
	for _, row := range rows {
		if repo, _ := s.repoFor(ctx, row.Depot, row.Type, PinnedFlow(&row.Bead)); repo != nil {
			if out == nil {
				out = map[string]*RepoFlows{}
			}
			out[row.UUID] = repo
		}
	}
	return out
}

// noteHomes are the store flow's stage files that moving the beads the
// notes and answers among msgs are for depends on, by the bead's UUID,
// read before the write that takes them: disk does not run inside one. A
// note for no bead has none, and neither has a bead whose flow is the
// repository's; an answer names its bead in the body.
func (s *Store) noteHomes(ctx context.Context, name string, msgs []PeerMessage) map[string]*stageHome {
	var rows []*beadRow
	_ = s.read(func(tx *bolt.Tx) error {
		p, err := loadPeer(tx, name)
		if err != nil {
			return nil
		}
		for _, m := range msgs {
			rows = append(rows, takingRows(tx, p, m)...)
		}
		return nil
	})
	var out map[string]*stageHome
	for _, row := range rows {
		repo, _ := s.repoFor(ctx, row.Depot, row.Type, PinnedFlow(&row.Bead))
		if home, _ := s.stageHomeBefore(&row.Bead, repo); home != nil {
			if out == nil {
				out = map[string]*stageHome{}
			}
			out[row.UUID] = home
		}
	}
	return out
}

// takingRows are the beads moving m may move: the bead a note names in its
// Re, or the bead an answer names in its body. None for any other message,
// and for one that names no bead of this yard: refused where it is taken.
func takingRows(tx *bolt.Tx, p *Peer, m PeerMessage) []*beadRow {
	switch m.Kind {
	case PeerBeadNote:
		if _, row, _ := beadFor(tx, p, m.Re); row != nil {
			return []*beadRow{row}
		}
	case PeerAsk:
		// An answer, naming its bead in the body; an ask names none.
		if m.Re != "" {
			var a peerAnswer
			if json.Unmarshal(m.Body, &a) == nil {
				if row, err := loadBead(tx, a.Bead); err == nil {
					return []*beadRow{row}
				}
			}
		}
	}
	return nil
}

// noteBead takes the note m that p sent for a bead of this yard that came
// from it: the text becomes a note of that bead, by <name>@<peer>, written
// as the notes that came with the bead are (peerNotes), so that no noted
// event is matched against subscriptions with a peer's text;
// peer_bead_noted is logged on the bead, with the writer's name and none of
// the text; and where people work the first stage of the bead's flow the
// bead goes back to it (noteBack). Nothing else changes: a note lifts no
// hold, moves no bead forward and answers no ask. It returns why a note
// that is none, or one for no open bead of p's, was not taken.
//
// Every note counts toward the peer's hour beside its mail, asks and reads
// (PeerMailLimit), the refused too; of those over the limit only the first
// of the hour is logged. It changes p (the count), which the caller saves.
func (w *wtx) noteBead(p *Peer, m PeerMessage, way string, tk *taking) (string, error) {
	actor := "peer:" + p.Name
	extra := map[string]any{"direction": "in", "way": way}
	refuse := func(reason string) (string, error) {
		extra["reason"] = reason
		return reason, w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, extra))
	}
	p.hour(now())
	if p.Mails++; p.Mails > PeerMailLimit {
		if p.MailLimited {
			return RefusedRate, nil
		}
		p.MailLimited = true
		extra["limit"], extra["until"] = PeerMailLimit, p.MailSince.Add(PeerMailWindow)
		return refuse(RefusedRate)
	}
	var n PeerNoteMessage
	switch err := json.Unmarshal(m.Body, &n); {
	case err != nil || m.Re == "":
		return refuse(RefusedNotNote)
	case n.V != PeerBeadVersion:
		extra["v"], extra["known"] = n.V, PeerBeadVersion
		return refuse(RefusedVersion)
	case !peerMailName.MatchString(n.From) || strings.TrimSpace(n.Text) == "" || !utf8.ValidString(n.Text):
		return refuse(RefusedNotNote)
	case len(n.Text) > MaxPeerNoteBytes:
		return refuse(RefusedTooLarge)
	}
	rec, row, err := beadFor(w.Tx, p, m.Re)
	if err != nil {
		return "", err
	}
	if rec == nil {
		return refuse(RefusedNoSuchBead)
	}
	at := now()
	if _, err := w.peerNotes(row, p.Name, []PeerNote{{Author: n.From, At: at, Text: n.Text}}, nil); err != nil {
		return "", err
	}
	if err := w.mintBead(row, actor); err != nil {
		return "", err
	}
	data := map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "from": n.From, "to": rec.Theirs, "way": way}
	if err := w.event(row.UUID, "peer_bead_noted", actor, data); err != nil {
		return "", err
	}
	return "", w.noteBack(p, rec, row, PeerBack{By: n.From, At: at}, tk.flows[row.UUID], tk.homes[row.UUID])
}

// noteBack sends the bead row, which came from p and has just been given a
// note of p's, back to the first stage of its flow where people work that
// stage and the bead is in another: one move by the yard, which ends a
// session that works the bead and leaves its worktree and branch, with the
// stage it left in the event, in a note and on the record (back.Left). The
// first stage is where this yard looks at another yard's text before an
// agent with this yard's permissions reads it, and a note is more of that
// yard's text. Where agents, a script or another yard work the first stage
// the operator has said that the other yard's text starts work unseen, and
// the note lands as the bead did: marked, and moving nothing. A held bead
// goes back as any does and stays held.
//
// A bead whose flow does not read has no first stage to ask about: it is
// held, with a note of the yard's that says why, since a hold reaches the
// yardmaster and nothing starts on a held bead.
func (w *wtx) noteBack(p *Peer, rec *peerBead, row *beadRow, back PeerBack, repo *RepoFlows, home *stageHome) error {
	writer := back.By + "@" + p.Name
	f, _, err := resolveFlow(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
	if err != nil {
		why := strings.TrimSuffix(changedMeanwhile(err).Error(), ".")
		note := fmt.Sprintf("A note came from the yard %s (by %s) and this bead's flow does not read (%s), so who looks at that yard's text first is not known: the bead is held. Read the note, mend the flow, and lift the hold with yardr bead unhold %s.",
			p.Name, writer, why, row.ID)
		if err := w.addNote(row.UUID, NoteNote, note, "yard"); err != nil || row.Hold {
			return err
		}
		_, err := w.changeBead(row.UUID, "yard", func(w *wtx, b *beadRow) (string, map[string]any, error) {
			data := map[string]any{"peer": p.Name, "reason": "a note came from " + p.Name + " and the bead's flow does not read: " + why}
			if b.Session != nil {
				data["session"] = *b.Session
			}
			b.Hold = true
			return "held", data, w.endSession(b, "held")
		})
		return err
	}
	first := firstStage(f)
	if f == nil || row.Stage == first {
		return nil
	}
	var routes []Route
	if err := each(w.Tx, bRoutes, "", func(_ []byte, r *Route) error {
		routes = append(routes, *r)
		return nil
	}); err != nil {
		return err
	}
	SortRoutes(routes)
	work, err := stageWork(w.Tx, routes, row.Bead, first)
	if err != nil || work.Who != WhoPeople {
		return err
	}
	back.Left = row.Stage
	note := fmt.Sprintf("A note came from the yard %s (by %s) while this bead was in the stage %s. It is back in the stage %s, which people work, so that someone of this yard reads the note before an agent does. Move it on with yardr bead advance %s --to %s.",
		p.Name, writer, back.Left, first, row.ID, back.Left)
	if err := w.addNote(row.UUID, NoteNote, note, "yard"); err != nil {
		return err
	}
	// The yard's own move: no group's members are asked, since nobody of
	// the yard chose it.
	if _, err := w.move(row.UUID, "yard", repo, home, nil, func(_ *Flow, _ *beadRow, data map[string]any) (string, error) {
		data["peer"], data["reason"] = p.Name, "a note came from "+p.Name
		return first, nil
	}); err != nil {
		return err
	}
	// After the move, which forgets an earlier one (forgetBack).
	rec.Back = &back
	return put(w.Tx, bPeerBeads, row.UUID, rec)
}

// forgetBack drops what the record of the bead uuid says of a note that
// sent it back to its first stage: the bead has left that stage.
func forgetBack(tx *bolt.Tx, uuid string) error {
	rec, err := peerBeadOf(tx, uuid)
	if err != nil || rec == nil || rec.Back == nil {
		return err
	}
	rec.Back = nil
	return put(tx, bPeerBeads, uuid, rec)
}

// Returned is how a bead that a note from the yard called peer sent back
// to the stage says so, for as long as it is there: when the note came and
// by whom, the stage the bead left, and who reads the note and moves the
// bead on. bead is this yard's handle for it.
func (w StageWork) Returned(peer, bead string, back PeerBack) string {
	line := fmt.Sprintf("back in the stage %s: a note came from %s (%s, by %s@%s) while this bead was in %s: ", w.Stage, peer, stamp(back.At), back.By, peer, back.Left)
	on := fmt.Sprintf("(yardr bead advance %s --to %s)", bead, back.Left)
	switch {
	case w.Who != WhoPeople:
		// The routes changed since the note came.
		return line + w.then(peer, false)
	case len(w.Members) == 0:
		return line + fmt.Sprintf("the group %s names no members, so anyone of this yard may read it and move the bead on, a crew member too %s", w.Group, on)
	}
	return line + fmt.Sprintf("a member of the group %s (%s) reads it and moves the bead on %s", w.Group, strings.Join(w.Members, ", "), on)
}

// noteGivenUp tells whoever wrote the note m, which was given up, that it
// did not reach the other yard's bead, as mail from the yard: a crew
// member, or a user. Anyone else reads peer_message_failed and yardr peer
// show.
func (w *wtx) noteGivenUp(m *PeerOut, reason string) error {
	if m.Kind != PeerBeadNote {
		return nil
	}
	var n PeerNoteMessage
	if err := json.Unmarshal(m.Body, &n); err != nil {
		return nil
	}
	var row beadRow
	if ok, err := get(w.Tx, bBeads, sentBead(m.Re), &row); err != nil || !ok {
		return err
	}
	reason = strings.TrimPrefix(reason, "peer "+m.Peer+": ")
	data := map[string]any{"to": n.From, "undelivered": m.ID,
		"text": fmt.Sprintf("your note of %s on %s's bead for %s was not delivered: %s", m.At.Local().Format("01-02 15:04"), m.Peer, row.ID, reason)}
	switch {
	case w.Bucket(bCrew).Get([]byte(n.From)) != nil:
	case w.Bucket(bUsers).Get([]byte(n.From)) != nil:
		data["to_user"] = true
	default:
		return nil
	}
	return w.event("", "mail", "yard", data)
}
