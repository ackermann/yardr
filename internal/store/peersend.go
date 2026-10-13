package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/yardkey"
)

// This file is the sending side of a bead worked by another yard
// (docs/peers.md, "The sending side: a stage worked by a linked yard";
// peerbeads.go is the receiving one). A stage is worked by a linked yard
// when the routes send it to a group whose runner is PeerRunner: the claim
// makes a session as for any group, the
// runner's Start asks the store to queue the bead
// message (PeerBeadSend), and the session then waits. It ends when the
// peer's return comes, with the outcome the peer's flow came to, or with
// one of the two outcomes that are this yard's own word: OutcomeRefused,
// the peer did not take the bead, and OutcomeUndelivered, the message
// never reached it.
//
// How a stay at a peer ends is known inside the write that takes the
// message, or gives one up. What it does to the bead is done right after
// that write, in one of its own (settleSent): where the outcome leads is
// the bead's flow to say, and a flow in the depot's repository is read with
// git, which does not run inside a write. Between the two the record says
// the outcome is known and not applied, and the daemon applies what a
// write left (SettlePeerBeads), so nothing is lost to a process that died
// between them or a repository that could not be read.

const (
	// PeerRunner is the runner of a group whose beads another yard works.
	PeerRunner = "peer"
	// GroupPeer and GroupTries are such a group's settings: the peer, by
	// the name this yard has for it, and how often the bead message is
	// posted before it is given up (PeerBeadTries when unset).
	GroupPeer  = "peer"
	GroupTries = "tries"
	// PeerBeadTries posts, with the daemon's backoff, are about a quarter
	// of an hour: a yard that is down for longer is the yardmaster's to hear
	// of, not the outbox's to wait out.
	PeerBeadTries = 5

	// OutcomeUndelivered is the outcome a stage worked by a peer ends with
	// when the bead never got there: the message was given up after its
	// tries, the link no longer carries beads out, or it was removed.
	OutcomeUndelivered = "undelivered"

	// MaxPeerPath is how many yards a bead may have passed through when it
	// arrives; one that has been through more is refused (RefusedTooFar).
	MaxPeerPath = 8
	// RefusedLoop is the refusal of a bead that has been at this yard
	// before on its way, and RefusedTooFar of one that has come through
	// more yards than MaxPeerPath.
	RefusedLoop   = "loop"
	RefusedTooFar = "too_far"
)

// bPeerSent holds the record of every bead sent to another yard, by the
// bead's UUID: the last time it was sent. It is a bucket apart from the
// beads that came in (bPeerBeads), since a bead that came from one yard
// can be sent on to another and then has both.
var bPeerSent = []byte("peer_beads_out")

func init() { allBuckets = append(allBuckets, bPeerSent) }

// peerSent is the record of a bead sent to a peer.
type peerSent struct {
	Bead string `json:"bead"` // this yard's, by UUID
	Key  string `json:"key"`  // the peer's public key
	// Stage is the stage of this yard the bead waits in, Group the group
	// that sent it, and Entered the event that put the bead in the stage:
	// a return is for that stay and no later one.
	Stage   string `json:"stage"`
	Group   string `json:"group,omitempty"`
	Entered uint64 `json:"entered"`
	// Message is the id of the bead message, which the return answers, and
	// Seq the number it was made from (sentID).
	Message string    `json:"message"`
	Seq     uint64    `json:"seq"`
	At      time.Time `json:"at"`
	// Delivered is when the peer had the message, nil while it waits.
	Delivered *time.Time `json:"delivered,omitempty"`
	// Theirs is the peer's handle for its bead, known from a bead event or
	// its return. There is the stage that bead is in at the peer, ThereHeld
	// whether it is held there, and Said when the peer said so: its last
	// bead event of this stay (saidOfBead), nil for a peer that sent none.
	Theirs    string     `json:"theirs,omitempty"`
	There     string     `json:"there,omitempty"`
	ThereHeld bool       `json:"there_held,omitempty"`
	Said      *time.Time `json:"said,omitempty"`
	// Outcome is how the stay ended, at EndedAt: the peer's, or one of
	// this yard's own, with Detail saying why. Notes are what the peer
	// wrote, kept here until they are on the bead.
	Outcome      string     `json:"outcome,omitempty"`
	Detail       string     `json:"detail,omitempty"`
	Notes        []PeerNote `json:"notes,omitempty"`
	NotesLeftOut int        `json:"notes_left_out,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	// Git is where the return says the result is: a branch, its commit and
	// the remote it was pushed to. The outcome is not applied before the
	// daemon has taken the branch (Fetched) or found that it is not for
	// this yard to take (NotTaken, with why): see PeerFetches. Attempts
	// counts the fetches that failed, Error is what the last said and
	// RetryAt when the next is due; GaveUp is when the bead was held over
	// it, after which nothing is tried until someone lifts that hold.
	Git      *PeerGit   `json:"git,omitempty"`
	Fetched  *time.Time `json:"fetched,omitempty"`
	NotTaken string     `json:"not_taken,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	GaveUp   *time.Time `json:"gave_up,omitempty"`
	// Last is the code the bead came back with from an earlier sending,
	// the last whose branch this yard took: this record is of the bead's
	// last sending only, and a bead sent again continues from that code
	// however the sendings between ended (taken).
	Last *PeerGit `json:"last,omitempty"`
	// Settled is when the outcome was applied to the bead, or found to be
	// for a stay the bead had left (settleSent).
	Settled *time.Time `json:"settled,omitempty"`
	// Was is the name the peer had when its link was removed.
	Was string `json:"was,omitempty"`
	// Wrote are the notes someone of this yard wrote on the peer's bead in
	// this stay (PeerNote). The peer sends them back among what was
	// written there, and by them this yard knows its own (wroteHere).
	Wrote []peerWrote `json:"wrote,omitempty"`
}

// AtPeer is the peer a group's beads are worked by, as this yard names it:
// "" for a group of any other runner.
func (g Group) AtPeer() string {
	if g.Runner != PeerRunner {
		return ""
	}
	return setting(g.Config[GroupPeer])
}

// setting reads a group setting that is a word: --set writes what reads as
// JSON as JSON, so a peer called 7 is a number there.
func setting(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// peerTries is how often the group's bead message is posted before it is
// given up.
func (g Group) peerTries() int {
	if n, err := strconv.Atoi(setting(g.Config[GroupTries])); err == nil && n > 0 {
		return n
	}
	return PeerBeadTries
}

// peerRemote is the remote of the depot a branch the group's peer returns is
// fetched from.
func (g Group) peerRemote() string {
	return or(setting(g.Config[GroupRemote]), PeerRemote)
}

// checkPeerGroup refuses a group of the peer runner that could send
// nothing: it names no peer, one this yard is not linked with, or one
// whose link carries no beads out.
func checkPeerGroup(tx *bolt.Tx, g *Group) error {
	if g.Runner != PeerRunner {
		return nil
	}
	name := g.AtPeer()
	if name == "" {
		return fmt.Errorf("group %s: a peer group names the yard that works its beads: --set %s=<peer>", g.Name, GroupPeer)
	}
	if v, set := g.Config[GroupTries]; set {
		if n, err := strconv.Atoi(setting(v)); err != nil || n < 1 {
			return fmt.Errorf("group %s: %s is how often a bead is posted to %s before it is given up, at least 1 (unset: %d)", g.Name, GroupTries, name, PeerBeadTries)
		}
	}
	if v, set := g.Config[GroupRemote]; set && !gitRemoteName.MatchString(setting(v)) {
		return fmt.Errorf("group %s: %s is the name of the remote a branch %s returns is fetched from, as git remote lists it (unset: %s)", g.Name, GroupRemote, name, PeerRemote)
	}
	p, err := loadPeer(tx, name)
	if err != nil {
		return fmt.Errorf("group %s: %w", g.Name, err)
	}
	if !p.carries(PeerBead, false) {
		return fmt.Errorf("group %s: the link with %s does not carry %s out: the operator allows it with yardr peer allow %s --send %s", g.Name, name, PeerBead, name, PeerBead)
	}
	return nil
}

// peerGroupsOf are the groups whose beads the peer name works.
func peerGroupsOf(tx *bolt.Tx, name string) ([]Group, error) {
	var out []Group
	err := each(tx, bGroups, "", func(_ []byte, g *Group) error {
		if g.AtPeer() == name {
			out = append(out, *g)
		}
		return nil
	})
	return out, err
}

// renamePeerGroups makes the groups that send to the peer name send to it
// under its new name, and returns them: it is the yard they were set up
// for.
func renamePeerGroups(tx *bolt.Tx, name, to string) ([]string, error) {
	gs, err := peerGroupsOf(tx, name)
	var names []string
	for _, g := range gs {
		if err != nil {
			break
		}
		g.Config[GroupPeer] = to
		names = append(names, g.Name)
		err = put(tx, bGroups, g.Name, g)
	}
	return names, err
}

// SetYard tells the store the file of this yard's own key, which it reads
// when a bead crosses a link (the key is what a bead's path names this
// yard by), and the yardr it runs, which a bead it sends says. The process
// that owns the store says both once, on opening.
func (s *Store) SetYard(keyFile, build string) { s.yardKeyFile, s.build = keyFile, build }

// yardKey is this yard's public key, "" when it has none, or the store was
// not told where it is.
func (s *Store) yardKey() string {
	if s.yardKeyFile == "" {
		return ""
	}
	k, err := yardkey.Load(s.yardKeyFile)
	if err != nil {
		return ""
	}
	return yardkey.Public(k)
}

// taken is the code the bead last came back with that is this yard's
// branch of it since: of this sending when its return's branch was fetched,
// else of an earlier one. nil for a bead no yard returned code for.
func (rec *peerSent) taken() *PeerGit {
	switch {
	case rec == nil:
		return nil
	case rec.Git != nil && rec.Fetched != nil:
		return rec.Git
	}
	return rec.Last
}

func peerSentOf(tx *bolt.Tx, uuid string) (*peerSent, error) {
	rec := new(peerSent)
	if ok, err := get(tx, bPeerSent, uuid, rec); err != nil || !ok {
		return nil, err
	}
	return rec, nil
}

// sentID is the id of the message that sends the bead uuid: made of the
// bead and the number of the event that put it in its stage, so a bead
// claimed twice in one stay (a daemon that died while it started the
// session) is sent under one id, which the peer takes once.
func sentID(uuid string, seq uint64) string { return uuid + "-" + strconv.FormatUint(seq, 10) }

// sentBead is the bead a message id of sentID's making names, "" for any
// other id.
func sentBead(id string) string {
	if len(id) < 38 || id[36] != '-' || !isUUID(id[:36]) {
		return ""
	}
	return id[:36]
}

// stayOf reads off the bead's events which one put it in the stage it is
// in (it was created there, moved there or reopened), and the last time
// its hold was lifted since; 0 for what the log no longer has.
func stayOf(tx *bolt.Tx, uuid string) (entered, unheld uint64, err error) {
	ks := keys(tx, bEventsBy, uuid+"\x00")
	for i := len(ks) - 1; i >= 0; i-- {
		var e Event
		if ok, err := get(tx, bEvents, ks[i][len(uuid)+1:], &e); err != nil {
			return 0, 0, err
		} else if !ok {
			continue
		}
		switch e.Kind {
		case "unheld":
			unheld = max(unheld, e.Seq)
		case "advanced", "created", "reopened":
			return e.Seq, unheld, nil
		}
	}
	return 0, unheld, nil
}

// PeerBeadSend sends the bead the session works to the peer its group
// names: a message of kind PeerBead in the outbox for the peer, for the
// daemon to post or the peer to ask for, with the record that joins the
// two beads and peer_bead_sent on the bead. It is what the peer runner's
// Start does, and it is safe to do again: a bead already sent in this stay
// in its stage is not sent twice.
//
// A bead that came back from a yard with code and is sent again starts
// where this yard's branch of it stands, not at the base once more: what
// is returned then comes after the branch, and is taken (againOf).
//
// A bead that cannot be sent ends its stage here, as one that was sent and
// not taken does later: undelivered when the peer is gone or its link
// carries no beads out, refused when the bead is larger than crosses a
// link, or when its branch has commits of this yard's own since a yard
// returned it.
func (s *Store) PeerBeadSend(ctx context.Context, session string) (*BeadPeer, error) {
	sess, err := s.GetSession(ctx, session)
	if err != nil {
		return nil, err
	}
	pre, err := s.GetBead(ctx, sess.Bead)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre))
	if err != nil {
		return nil, err
	}
	own := s.yardKey()
	// What the work starts from, for a bead of a repository whose base has
	// a remote: read now, since git does not run inside the write.
	var from, last *PeerGit
	moved := ""
	if err := s.read(func(tx *bolt.Tx) error {
		rec, err := peerSentOf(tx, pre.UUID)
		last = rec.taken()
		return err
	}); err != nil {
		return nil, err
	}
	if pre.Depot != nil {
		depot, err := s.GetDepot(ctx, *pre.Depot)
		if err != nil {
			return nil, err
		}
		fam, err := s.Family(ctx, pre.UUID)
		if err != nil {
			return nil, err
		}
		gctx, cancel := context.WithTimeout(ctx, peerFetchTimeout)
		from, moved = againOf(gctx, *depot, pre.ID, baseOf(gctx, *depot, fam.Base), last)
		cancel()
	}
	err = s.write(func(w *wtx) error {
		row, err := w.loadBead(pre.UUID)
		if err != nil {
			return err
		}
		var stored Session
		if ok, err := get(w.Tx, bSessions, session, &stored); err != nil {
			return err
		} else if !ok || !stored.live() || row.Session == nil || *row.Session != session {
			return fmt.Errorf("%w: session %s does not work bead %s any more", ErrConflict, session, row.ID)
		}
		var g Group
		if _, err := get(w.Tx, bGroups, stored.Group, &g); err != nil {
			return err
		}
		name := g.AtPeer()
		if name == "" {
			return fmt.Errorf("group %s names no peer to send bead %s to (yardr group add %s --runner %s --set %s=<peer>)", stored.Group, row.ID, stored.Group, PeerRunner, GroupPeer)
		}
		entered, unheld, err := stayOf(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		seq := entered
		old, err := peerSentOf(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		if old != nil && old.Entered == entered && old.Stage == row.Stage {
			switch {
			case old.awaitsFetch():
				// The stay has its answer, and the branch that came with it
				// is still being fetched: the bead waits for that, and is
				// not sent out again from under it.
				return nil
			case old.EndedAt == nil && old.Was == "":
				// Sent already in this stay: the claim is a second one. The
				// message is queued again only when it is neither there nor
				// was taken, which no write leaves behind.
				p, err := peerByKey(w.Tx, old.Key)
				if err != nil {
					return err
				}
				if p != nil && p.Name == name && (old.Delivered != nil || queued(w.Tx, p.Name, old.Message)) {
					return nil
				}
				seq = old.Seq
			case unheld > old.Seq:
				// The stay had its answer, the bead was kept, held, and
				// someone released it: it is sent anew, under an id the peer
				// has not seen.
				seq = unheld
			default:
				seq = w.Bucket(bEvents).Sequence()
			}
		}
		if t := old.taken(); (t == nil) != (last == nil) || t != nil && *t != *last {
			// A return's branch was taken between the look at the bead's
			// branch and this write: what was found is of the code before.
			return fmt.Errorf("%w: bead %s came back with code while it was being sent", ErrConflict, row.ID)
		}
		rec := &peerSent{Bead: row.UUID, Stage: row.Stage, Group: g.Name, Entered: entered, Message: sentID(row.UUID, seq), Seq: seq, At: now(), Last: last}
		p, err := loadPeer(w.Tx, name)
		if errors.Is(err, ErrNotFound) {
			rec.Was = name
			return w.endSent(rec, name, OutcomeUndelivered, fmt.Sprintf("this yard is not linked with a yard %s any more", name), "yard", nil)
		} else if err != nil {
			return err
		}
		rec.Key = p.Key
		if !p.carries(PeerBead, false) {
			return w.endSent(rec, name, OutcomeUndelivered, fmt.Sprintf("the link with %s does not carry %s out (yardr peer allow %s --send %s)", name, PeerBead, name, PeerBead), "yard", nil)
		}
		if len(row.Title) > MaxPeerBeadTitle || len(row.Body) > MaxPeerBeadBody {
			return w.endSent(rec, name, OutcomeRefused, fmt.Sprintf("too large to send: a bead crosses a link with a title of up to %d bytes and a body of up to %d, and this one has %d and %d",
				MaxPeerBeadTitle, MaxPeerBeadBody, len(row.Title), len(row.Body)), "yard", nil)
		}
		if moved != "" {
			return w.endSent(rec, name, OutcomeRefused, moved, "yard", nil)
		}
		if own == "" {
			return fmt.Errorf("this yard has no key, so bead %s cannot say which yard it comes from: yardr peer key makes one", row.ID)
		}
		msg := PeerBeadMessage{V: PeerBeadVersion, Yardr: s.build, Bead: row.ID, Stage: row.Stage, Title: row.Title, Body: row.Body,
			Type: row.Type, Priority: row.Priority}
		f, _, err := resolveFlow(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil {
			return changedMeanwhile(err)
		}
		if f != nil {
			for _, o := range f.OutcomesFrom(row.Stage) {
				msg.Outcomes = append(msg.Outcomes, o.Name)
			}
		}
		// Where the bead has been: the yards before this one, when it came
		// from one, and this yard.
		if in, err := peerBeadOf(w.Tx, row.UUID); err != nil {
			return err
		} else if in != nil {
			msg.Path = slices.Clone(in.Path)
			if len(msg.Path) == 0 {
				msg.Path = []string{in.Key}
			}
			// Sent on, it starts where it started here, under the name
			// the yard it came from had for that.
			if from != nil && in.Git != nil && from.BaseCommit == in.Git.BaseCommit {
				from.Base = in.Git.Base
			}
		}
		msg.Path = append(msg.Path, own)
		msg.Git = from
		links, err := beadLinks(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		for _, l := range links[:min(len(links), 32)] {
			msg.Links = append(msg.Links, PeerLink{Kind: l.Kind, Source: l.Source, URL: l.URL})
		}
		var notes []PeerNote
		if err := each(w.Tx, bNotes, join(row.UUID, ""), func(_ []byte, n *Note) error {
			if n.Kind == "" || n.Kind == NoteNote {
				notes = append(notes, PeerNote{Author: n.Author, At: n.At, Text: n.Text})
			}
			return nil
		}); err != nil {
			return err
		}
		body, left := fitNotes(notes, func(kept []PeerNote, left int) any {
			msg.Notes, msg.NotesLeftOut = kept, left
			return msg
		})
		m := PeerMessage{ID: rec.Message, Kind: PeerBead, Body: body}
		if err := m.check(); err != nil {
			return w.endSent(rec, name, OutcomeRefused, "too large to send: "+err.Error(), "yard", nil)
		}
		out, err := w.queuePeer(p, m, "yard")
		if err != nil || out == nil {
			return err
		}
		out.Tries = g.peerTries()
		if err := put(w.Tx, bPeerOut, join(p.Name, seqKey(out.Seq)), out); err != nil {
			return err
		}
		if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
			return err
		}
		// A new stay: what was read in an earlier one is not of it.
		if err := dropPeerRead(w.Tx, rec.Bead); err != nil {
			return err
		}
		data := map[string]any{"peer": name, "id": m.ID, "stage": row.Stage, "group": g.Name, "session": session, "notes": len(notes) - left, "tries": out.Tries}
		if left > 0 {
			data["notes_left_out"] = left
		}
		if len(msg.Path) > 1 {
			data["yards_before"] = len(msg.Path) - 1
		}
		if from != nil {
			data["base"], data["base_commit"], data["remote"] = from.Base, from.BaseCommit, from.Remote
		}
		return w.event(row.UUID, "peer_bead_sent", "yard", data)
	})
	if err != nil {
		return nil, err
	}
	b, err := s.GetBead(ctx, pre.UUID)
	if err != nil {
		return nil, err
	}
	return b.Away(), nil
}

// queued reports whether the message id waits in the outbox for the peer
// name.
func queued(tx *bolt.Tx, name, id string) bool {
	found := false
	_ = each(tx, bPeerOut, join(name, ""), func(_ []byte, m *PeerOut) error {
		found = found || m.ID == id && m.Failed == nil
		return nil
	})
	return found
}

// endSent records how the stay of a bead at the peer called name ended:
// outcome, with detail saying why when it is this yard's own, and extra
// for the event. The record is saved, what was kept of reading the peer's
// bead is dropped, the event logged on the bead
// (peer_bead_returned, peer_bead_refused or peer_bead_undelivered), and the
// bead is settled after the write (settleSent).
func (w *wtx) endSent(rec *peerSent, name, outcome, detail, actor string, extra map[string]any) error {
	at := now()
	rec.Outcome, rec.Detail, rec.EndedAt = outcome, detail, &at
	if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
		return err
	}
	// What was read of the peer's bead goes with the stay.
	if err := dropPeerRead(w.Tx, rec.Bead); err != nil {
		return err
	}
	w.settle = append(w.settle, rec.Bead)
	kind := "peer_bead_returned"
	data := map[string]any{"peer": name, "id": rec.Message, "stage": rec.Stage, "outcome": outcome}
	if detail != "" {
		kind = "peer_bead_" + outcome
		data["reason"] = detail
	}
	for k, v := range extra {
		data[k] = v
	}
	return w.event(rec.Bead, kind, actor, data)
}

// sentGivenUp ends the stay of the bead whose message m was given up: as
// refused when it was the peer's word that it does not take it, else as
// undelivered. reason is the daemon's, or the pull's, and names the peer
// first, which the bead's line says already.
func (w *wtx) sentGivenUp(m *PeerOut, reason string, refused bool) error {
	if m.Kind != PeerBead {
		return nil
	}
	rec, err := peerSentOf(w.Tx, sentBead(m.ID))
	if err != nil || rec == nil || rec.Message != m.ID || rec.EndedAt != nil {
		return err
	}
	reason = strings.TrimPrefix(strings.TrimPrefix(reason, "peer "+m.Peer+": "), "refused by the peer: ")
	if refused {
		// The refusal came from the peer, but refused is this yard's
		// outcome for the stay: keep it attributed to the sender.
		return w.endSent(rec, m.Peer, OutcomeRefused, reason, "yard", nil)
	}
	return w.endSent(rec, m.Peer, OutcomeUndelivered, fmt.Sprintf("%s was not reached in %d tries since %s: %s", m.Peer, m.Attempts, stamp(m.At), reason),
		"yard", map[string]any{"attempts": m.Attempts})
}

// sentDelivered notes that the peer has the bead message m.
func (w *wtx) sentDelivered(m *PeerOut) error {
	if m.Kind != PeerBead {
		return nil
	}
	rec, err := peerSentOf(w.Tx, sentBead(m.ID))
	if err != nil || rec == nil || rec.Message != m.ID || rec.Delivered != nil {
		return err
	}
	at := now()
	rec.Delivered = &at
	return put(w.Tx, bPeerSent, rec.Bead, rec)
}

// returnedBead takes the return m that p sent for a bead of this yard: the
// record of the bead is given the outcome and the notes, for settleSent to
// apply, and peer_bead_returned is logged on the bead. A return for a stay
// that is over (the bead left the stage, a person took it back, or the
// stay already had its answer) changes nothing, and is logged with stale.
// It returns why a return that is none was not taken.
func (w *wtx) returnedBead(p *Peer, m PeerMessage, way string) (string, error) {
	actor := "peer:" + p.Name
	refuse := func(reason string) (string, error) {
		return reason, w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, map[string]any{"direction": "in", "way": way, "reason": reason}))
	}
	var head struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(m.Body, &head); err != nil {
		return refuse(RefusedNotBead)
	}
	if head.V == nil || *head.V != PeerBeadVersion {
		return refuse(RefusedVersion)
	}
	var r PeerReturnMessage
	if err := json.Unmarshal(m.Body, &r); err != nil || !peerStage.MatchString(r.Outcome) || !validID.MatchString(r.Bead) || isUUID(r.Bead) {
		return refuse(RefusedNotBead)
	}
	data := map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "from": r.Bead, "outcome": r.Outcome, "way": way}
	stale := func(bead, why string) (string, error) {
		data["stale"], data["reason"] = true, why
		return "", w.event(bead, "peer_bead_returned", actor, data)
	}
	rec, err := peerSentOf(w.Tx, sentBead(m.Re))
	if err != nil {
		return "", err
	}
	if rec == nil || rec.Message != m.Re || rec.Key != p.Key || rec.Was != "" {
		bead := ""
		if rec != nil && rec.Key == p.Key && rec.Was == "" {
			bead = rec.Bead
		}
		return stale(bead, "it answers no bead this yard has at "+p.Name)
	}
	if rec.EndedAt != nil {
		return stale(rec.Bead, "the bead's stay at "+p.Name+" had ended, as "+rec.Outcome)
	}
	row, err := w.loadBead(rec.Bead)
	if err != nil {
		return "", err
	}
	entered, _, err := stayOf(w.Tx, rec.Bead)
	if err != nil {
		return "", err
	}
	if row.Status != "open" || row.Stage != rec.Stage || entered != rec.Entered {
		// The stay is over all the same: nothing more is waited for.
		at := now()
		rec.Theirs, rec.Outcome, rec.EndedAt, rec.Settled = r.Bead, r.Outcome, &at, &at
		if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
			return "", err
		}
		if err := dropPeerRead(w.Tx, rec.Bead); err != nil {
			return "", err
		}
		return stale(rec.Bead, fmt.Sprintf("the bead has left the stage %s it was sent from", rec.Stage))
	}
	// Held to what a bead's notes are held to on the way out: a peer that
	// sends more is cut, not refused, since its work is done.
	notes := slices.DeleteFunc(slices.Clone(r.Notes), func(n PeerNote) bool { return strings.TrimSpace(n.Text) == "" })
	kept, left := keepNotes(notes)
	rec.Theirs, rec.Notes, rec.NotesLeftOut = r.Bead, kept, left+r.NotesLeftOut
	if rec.Delivered == nil {
		at := now()
		rec.Delivered = &at
	}
	data["notes"] = len(kept)
	if rec.NotesLeftOut > 0 {
		data["notes_left_out"] = rec.NotesLeftOut
	}
	// A result in git: kept as the peer's word, cut to what a line shows.
	// Whether it is taken is the daemon's to find (PeerFetches).
	if g := r.Git; g != nil && (g.Branch != "" || g.Commit != "") {
		word := func(s string) string {
			s = cleanTitle(s)
			return s[:min(len(s), 256)]
		}
		rec.Git = &PeerGit{Branch: word(g.Branch), Commit: word(g.Commit), Remote: or(RemoteForm(g.Remote), word(g.Remote))}
		data["branch"], data["commit"], data["remote"] = rec.Git.Branch, rec.Git.Commit, rec.Git.Remote
	}
	delete(data, "outcome") // endSent says it
	return "", w.endSent(rec, p.Name, r.Outcome, "", actor, data)
}

// unsendPeerBeads ends, as undelivered and for why, the stay of every bead
// that is at p: its link is removed (gone), or stopped carrying beads out,
// and no return can come over it. What still waits in the outbox of a link
// that stays is given up with it.
func (w *wtx) unsendPeerBeads(p *Peer, why string, gone bool) (int, error) {
	var recs []peerSent
	if err := each(w.Tx, bPeerSent, "", func(_ []byte, rec *peerSent) error {
		if rec.Key == p.Key && rec.Was == "" {
			recs = append(recs, *rec)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range recs {
		if gone {
			rec.Was = p.Name
		}
		if rec.EndedAt != nil {
			if !gone {
				continue
			}
			if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
				return n, err
			}
			continue
		}
		n++
		if err := w.endSent(&rec, p.Name, OutcomeUndelivered, why, "yard", nil); err != nil {
			return n, err
		}
	}
	// After the beads, which have their reason by now: the message of each
	// only has to stop being posted.
	if !gone {
		var waiting []PeerOut
		if err := each(w.Tx, bPeerOut, join(p.Name, ""), func(_ []byte, m *PeerOut) error {
			if m.Kind == PeerBead && m.Failed == nil {
				waiting = append(waiting, *m)
			}
			return nil
		}); err != nil {
			return n, err
		}
		for _, m := range waiting {
			if err := m.giveUp(w, why, nil, false); err != nil {
				return n, err
			}
			if err := put(w.Tx, bPeerOut, join(p.Name, seqKey(m.Seq)), m); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// awayAt counts the beads of this yard that are at p now.
func awayAt(tx *bolt.Tx, p *Peer) error {
	p.Away = 0
	return each(tx, bPeerSent, "", func(_ []byte, rec *peerSent) error {
		if rec.Key == p.Key && rec.Was == "" && rec.EndedAt == nil {
			p.Away++
		}
		return nil
	})
}

// stamp is a time as a note or a line says it.
func stamp(t time.Time) string { return t.Local().Format("01-02 15:04") }

// SettlePeerBeads applies every outcome of a bead's stay at a peer that is
// known and not applied yet (settleSent). The write that learns an outcome
// applies it itself right after; this is for what that left: the daemon
// calls it on every pass.
func (s *Store) SettlePeerBeads(ctx context.Context) error {
	var pending []string
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPeerSent, "", func(_ []byte, rec *peerSent) error {
			if rec.EndedAt != nil && rec.Settled == nil {
				pending = append(pending, rec.Bead)
			}
			return nil
		})
	})
	for _, uuid := range pending {
		err = errors.Join(err, s.settleSent(ctx, uuid))
	}
	return err
}

// settleSent applies the outcome of the bead's stay at a peer to the bead:
// the peer's notes are added, each under its author's name with @<peer>,
// and the outcome is reported as Done reports one, by peer:<name> for the
// peer's own and by the yard for refused and undelivered, with a note that
// says why. A stage that does not name the outcome keeps the bead, held,
// with a note naming the outcome and the stage's own: a hold reaches the
// yardmaster. A bead that has left the stage meanwhile is left alone.
func (s *Store) settleSent(ctx context.Context, uuid string) error {
	pending := func(tx *bolt.Tx) (*peerSent, error) {
		rec, err := peerSentOf(tx, uuid)
		if err != nil || rec == nil || rec.EndedAt == nil || rec.Settled != nil {
			return nil, err
		}
		// A branch came back and is not here yet: the outcome waits for it
		// (PeerFetched), unless the bead has left the stage meanwhile.
		if rec.awaitsFetch() {
			if left, err := leftStay(tx, rec); err != nil || !left {
				return nil, err
			}
		}
		return rec, nil
	}
	var rec *peerSent
	if err := s.read(func(tx *bolt.Tx) (err error) {
		rec, err = pending(tx)
		return err
	}); err != nil || rec == nil {
		return err
	}
	pre, err := s.GetBead(ctx, uuid)
	if err != nil {
		return err
	}
	repo, err := s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre))
	if err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		rec, err := pending(w.Tx)
		if err != nil || rec == nil {
			return err
		}
		at := now()
		rec.Settled = &at
		name := rec.Was
		if rec.Was == "" {
			if p, err := peerByKey(w.Tx, rec.Key); err != nil {
				return err
			} else if p != nil {
				name = p.Name
			}
		}
		theirs := rec.Outcome != OutcomeRefused && rec.Outcome != OutcomeUndelivered || rec.Detail == ""
		actor := "yard"
		if theirs {
			actor = "peer:" + name
		}
		row, err := w.loadBead(uuid)
		if err != nil {
			return err
		}
		entered, _, err := stayOf(w.Tx, uuid)
		if err != nil {
			return err
		}
		if row.Status != "open" || row.Stage != rec.Stage || entered != rec.Entered {
			rec.Notes = nil
			if err := put(w.Tx, bPeerSent, uuid, rec); err != nil {
				return err
			}
			return w.event(uuid, "peer_bead_returned", actor, map[string]any{"peer": name, "id": rec.Message, "outcome": rec.Outcome, "stale": true,
				"reason": fmt.Sprintf("the bead has left the stage %s it was sent from", rec.Stage)})
		}
		if len(rec.Notes) > 0 {
			if _, err := w.peerNotes(row, name, rec.Notes, rec); err != nil {
				return err
			}
			if err := w.mintBead(row, actor); err != nil {
				return err
			}
		}
		f, _, err := resolveFlow(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil {
			return changedMeanwhile(err)
		}
		to, named := "", []string{}
		if f != nil {
			for _, o := range f.OutcomesFrom(rec.Stage) {
				named = append(named, o.Name)
				if o.Name == rec.Outcome {
					to = o.To
				}
			}
		}
		var note []string
		switch {
		case theirs && rec.NotesLeftOut > 0:
			note = append(note, fmt.Sprintf("The yard %s returned this bead as %s; %d of its older notes did not fit and were left out.", name, rec.Outcome, rec.NotesLeftOut))
		case rec.Outcome == OutcomeRefused && !theirs:
			note = append(note, fmt.Sprintf("The yard %s did not take this bead: %s.", name, strings.TrimSuffix(rec.Detail, ".")))
		case !theirs:
			note = append(note, fmt.Sprintf("This bead did not reach the yard %s: %s. Sending it again is moving it back into the stage: yardr bead advance %s --to %s",
				name, strings.TrimSuffix(rec.Detail, "."), row.ID, rec.Stage))
		}
		// The code that came with it, or did not: whoever works the next
		// stage reads the notes, and is to know whose the branch is.
		switch g := rec.Git; {
		case g == nil:
		case rec.Fetched != nil:
			note = append(note, fmt.Sprintf("The yard %s returned its branch %s at %s, which is %s here now. The work on it is that yard's, and so is any test line in its notes: this yard has not tested it.",
				name, g.Branch, short(g.Commit), Branch(row.ID)))
		default:
			note = append(note, fmt.Sprintf("The yard %s returned a branch (%s at %s) that this yard did not take: %s. What came back is the outcome and the notes.",
				name, g.Branch, short(g.Commit), strings.TrimSuffix(rec.NotTaken, ".")))
		}
		if to == "" {
			names := "no outcomes"
			if len(named) > 0 {
				names = strings.Join(named, ", ")
			}
			note = append(note, fmt.Sprintf("Its stage %s ended as %s, which the stage does not name (it names %s), so the bead is kept here, held. Move it on with yardr bead advance %s --to <stage>; yardr bead unhold %s sends it to %s again.",
				rec.Stage, rec.Outcome, names, row.ID, row.ID, name))
		}
		if len(note) > 0 {
			if err := w.addNote(uuid, NoteNote, strings.Join(note, " "), "yard"); err != nil {
				return err
			}
		}
		rec.Notes = nil // on the bead now
		if err := put(w.Tx, bPeerSent, uuid, rec); err != nil {
			return err
		}
		if to == "" {
			_, err := w.changeBead(uuid, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
				data := map[string]any{"outcome": rec.Outcome, "peer": name, "reason": fmt.Sprintf("stage %s does not name the outcome %s", rec.Stage, rec.Outcome)}
				if b.Session != nil {
					data["session"] = *b.Session
				}
				b.Hold = true
				return "held", data, w.endSession(b, "held")
			})
			return err
		}
		_, err = w.moveBead(ctx, Expect{}, uuid, actor, repo, func(_ *Flow, _ *beadRow, data map[string]any) (string, error) {
			data["outcome"], data["peer"] = rec.Outcome, name
			return to, nil
		})
		return err
	})
}

// awaitsFetch reports whether the stay ended with a branch that is neither
// taken nor found not to be this yard's to take.
func (rec *peerSent) awaitsFetch() bool {
	return rec.EndedAt != nil && rec.Settled == nil && rec.Git != nil && rec.Fetched == nil && rec.NotTaken == ""
}

// leftStay reports whether the bead is no longer in the stay at a peer
// that rec is of: closed, moved out of the stage, or back in it anew.
func leftStay(tx *bolt.Tx, rec *peerSent) (bool, error) {
	var row beadRow
	if ok, err := get(tx, bBeads, rec.Bead, &row); err != nil || !ok {
		return true, err
	}
	entered, _, err := stayOf(tx, rec.Bead)
	return row.Status != "open" || row.Stage != rec.Stage || entered != rec.Entered, err
}

// PeerFetch is a branch a peer returned for a bead of this yard, to be
// taken before the bead's outcome is applied: Git as the return names it,
// from Remote of Depot (the bead's group's GroupRemote), into the bead's own
// branch Into. Attempts counts the fetches that failed, of the Tries its
// group allows, with Error the last one's and RetryAt when the next is due.
type PeerFetch struct {
	Bead     string     `json:"bead"`
	Message  string     `json:"message"`
	Peer     string     `json:"peer"`
	Depot    Depot      `json:"depot"`
	Remote   string     `json:"remote"`
	Into     string     `json:"into"`
	Git      PeerGit    `json:"git"`
	Attempts int        `json:"attempts,omitempty"`
	Tries    int        `json:"tries"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

// PeerFetches are the branches that came back from peers and are not taken
// yet, for the daemon to take (TakePeerBranch) and report (PeerFetched,
// PeerFetchFailed). One whose bead was held when its fetch was given up is
// not among them while that hold lasts: lifting it is how a person says
// try again.
func (s *Store) PeerFetches(ctx context.Context) ([]PeerFetch, error) {
	var out []PeerFetch
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		return each(tx, bPeerSent, "", func(_ []byte, rec *peerSent) error {
			if !rec.awaitsFetch() {
				return nil
			}
			if left, err := leftStay(tx, rec); err != nil || left {
				return err // settled as stale on the daemon's pass
			}
			var row beadRow
			if _, err := get(tx, bBeads, rec.Bead, &row); err != nil {
				return err
			}
			if rec.GaveUp != nil && row.Hold {
				return nil
			}
			f := PeerFetch{Bead: h.of(rec.Bead), Message: rec.Message, Peer: rec.Was, Into: Branch(row.ID), Git: *rec.Git,
				Attempts: rec.Attempts, Error: rec.Error, RetryAt: rec.RetryAt, Remote: PeerRemote, Tries: PeerBeadTries}
			if rec.GaveUp != nil {
				// Released since: a round of its own.
				f.Attempts, f.RetryAt = 0, nil
			}
			if p, err := peerByKey(tx, rec.Key); err != nil {
				return err
			} else if p != nil && rec.Was == "" {
				f.Peer = p.Name
			}
			var g Group
			if ok, err := get(tx, bGroups, rec.Group, &g); err != nil {
				return err
			} else if ok {
				f.Remote, f.Tries = g.peerRemote(), g.peerTries()
			}
			if row.Depot != nil {
				if _, err := get(tx, bDepots, *row.Depot, &f.Depot); err != nil {
					return err
				}
			}
			if f.Depot.Name == "" {
				// A bead of no depot has no repository: said as a directory.
				f.Depot.Kind = DepotDir
			}
			out = append(out, f)
			return nil
		})
	})
	return out, err
}

// fetching is the record of the fetch the daemon reports on: of the bead,
// for the message its return answered, and still waited for.
func fetching(w *wtx, bead, message string) (*peerSent, *beadRow, error) {
	row, err := w.loadBead(bead)
	if err != nil {
		return nil, nil, err
	}
	rec, err := peerSentOf(w.Tx, row.UUID)
	if err != nil || rec == nil || rec.Message != message || !rec.awaitsFetch() {
		return nil, nil, err
	}
	return rec, row, nil
}

// PeerFetched records that the branch the peer returned for the bead is
// its own branch now, or with notTaken why it is not this yard's to take
// (a directory depot, another remote): either way the outcome of the return
// is applied next (settleSent), with a note that says which.
func (s *Store) PeerFetched(ctx context.Context, bead, message, notTaken string) error {
	return s.write(func(w *wtx) error {
		rec, row, err := fetching(w, bead, message)
		if err != nil || rec == nil {
			return err
		}
		name := peerNamed(w.Tx, rec.Key, rec.Was)
		at := now()
		data := map[string]any{"peer": name, "branch": rec.Git.Branch, "commit": rec.Git.Commit, "remote": rec.Git.Remote}
		kind := "peer_bead_fetched"
		if notTaken = strings.TrimSpace(notTaken); notTaken != "" {
			rec.NotTaken = notTaken
			kind, data["reason"] = "peer_bead_not_fetched", notTaken
		} else {
			rec.Fetched = &at
			data["into"] = Branch(row.ID)
		}
		rec.Error, rec.RetryAt, rec.GaveUp = "", nil, nil
		if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
			return err
		}
		w.settle = append(w.settle, rec.Bead)
		return w.event(rec.Bead, kind, "yard", data)
	})
}

// PeerFetchFailed records a try to take the bead's returned branch that
// failed, for reason, and when the next is due. With final it is the last:
// the bead is held, with a note that says what failed and that lifting the
// hold tries again, and its outcome stays unapplied. The bead stays at the
// peer's group meanwhile, so nothing sends it out again.
func (s *Store) PeerFetchFailed(ctx context.Context, bead, message, reason string, retryAt time.Time, final bool) error {
	return s.write(func(w *wtx) error {
		rec, row, err := fetching(w, bead, message)
		if err != nil || rec == nil {
			return err
		}
		if rec.GaveUp != nil {
			rec.Attempts, rec.GaveUp = 0, nil
		}
		at := retryAt.UTC()
		rec.Attempts, rec.Error, rec.RetryAt = rec.Attempts+1, reason, &at
		if !final {
			return put(w.Tx, bPeerSent, rec.Bead, rec)
		}
		t := now()
		rec.GaveUp, rec.RetryAt = &t, nil
		if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
			return err
		}
		name := peerNamed(w.Tx, rec.Key, rec.Was)
		if err := w.event(rec.Bead, "peer_bead_not_fetched", "yard", map[string]any{"peer": name, "branch": rec.Git.Branch, "commit": rec.Git.Commit,
			"remote": rec.Git.Remote, "reason": reason, "attempts": rec.Attempts, "held": true}); err != nil {
			return err
		}
		note := fmt.Sprintf("The yard %s returned this bead as %s with its branch %s at %s, and this yard could not take the branch (%s): %s. The outcome is not applied before the branch is here, so the bead is held. yardr bead unhold %s tries again; yardr bead advance %s --to <stage> moves it on without the code.",
			name, rec.Outcome, rec.Git.Branch, short(rec.Git.Commit), tries(rec.Attempts), strings.TrimSuffix(reason, "."), row.ID, row.ID)
		if err := w.addNote(rec.Bead, NoteNote, note, "yard"); err != nil {
			return err
		}
		_, err = w.changeBead(rec.Bead, "yard", func(_ *wtx, b *beadRow) (string, map[string]any, error) {
			b.Hold = true
			return "held", map[string]any{"peer": name, "reason": "the branch " + name + " returned was not taken"}, nil
		})
		return err
	})
}

// tries is a count of tries as a line says it.
func tries(n int) string {
	if n == 1 {
		return "1 try"
	}
	return fmt.Sprintf("%d tries", n)
}

// peerNamed is the name of the peer whose key is key, or was when its link
// was removed.
func peerNamed(tx *bolt.Tx, key, was string) string {
	if was == "" {
		if p, err := peerByKey(tx, key); err == nil && p != nil {
			return p.Name
		}
	}
	return was
}

// keepNotes cuts each of notes to what one may be and keeps the newest
// that fit together; left counts the older ones that did not.
func keepNotes(notes []PeerNote) (kept []PeerNote, left int) {
	for i, n := range notes {
		notes[i].Text = cutNote(n.Text)
	}
	size, from := 0, len(notes)
	for from > 0 && size+len(notes[from-1].Text) <= MaxPeerNotes {
		from--
		size += len(notes[from].Text)
	}
	return notes[from:], from
}

// fitNotes is the body build makes with as many of notes as the limits
// allow, the newest kept: a note longer than one may be is cut, and the
// oldest are left out, and counted, until the rest fit. It returns how
// many were left out.
func fitNotes(notes []PeerNote, build func(kept []PeerNote, left int) any) (json.RawMessage, int) {
	kept, from := keepNotes(notes)
	for {
		body, err := json.Marshal(build(kept, from))
		// Escaped, a text takes more room than it has bytes.
		if err == nil && (len(body) <= MaxPeerBody || len(kept) == 0) {
			return body, from
		}
		if err != nil {
			return json.RawMessage(`{}`), len(notes)
		}
		kept, from = kept[1:], from+1
	}
}
