package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bIssueMoves holds the issue moves still to make: <event seq>\x00<hook>.
var bIssueMoves = []byte("issue_moves")

func init() { allBuckets = append(allBuckets, bIssueMoves) }

// IssueMove is an issue to move because the bead tracking it entered a
// stage a hook's map names. It is written with the bead's event, in the
// same transaction, so no stage change is missed and none is heard twice;
// the call to the source is made afterwards by the daemon, which holds the
// resident's credential where the store never does, and the move is gone
// once IssueMoved or IssueMoveSkipped has logged how it ended. Until then it
// survives any restart.
//
// It keeps what the event meant when it was logged: the hook's resident and
// word, and the link. Only the resident's grant and credential are read
// when the call is made.
type IssueMove struct {
	// ID is "<seq>/<hook>": one move per bead event and hook.
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"` // the bead's event
	Hook string `json:"hook"`
	As   string `json:"as"`
	Bead string `json:"bead"` // stored as the UUID, returned as the handle
	Type string `json:"type"`
	// Stage is the stage the bead entered, and State the word the hook's
	// map has for it.
	Stage  string `json:"stage"`
	State  string `json:"state"`
	Source string `json:"source"`
	URL    string `json:"url"`
	Thread string `json:"thread,omitempty"`
	// At is when the bead entered the stage.
	At time.Time `json:"at"`
	// Attempts counts the calls that failed, Error is what the last one
	// said, and RetryAt when the next is due.
	Attempts int        `json:"attempts,omitempty"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

// Why an issue move was not made, as thread_state_skipped names it.
const (
	SkipNoGrant = "no_grant"     // the resident acted as may not set a status in the source
	SkipPast    = "already_past" // the issue stands at or past the status
	SkipFailed  = "failed"       // the source failed for good
)

func issueMoveKey(seq uint64, hook string) string { return join(seqKey(seq), hook) }

// issueMoveID reads "<seq>/<hook>" back into the key.
func issueMoveID(id string) (string, error) {
	n, hook, ok := strings.Cut(id, "/")
	seq, err := strconv.ParseUint(n, 10, 64)
	if !ok || err != nil || hook == "" {
		return "", fmt.Errorf("issue move %q: want <event seq>/<hook>", id)
	}
	return issueMoveKey(seq, hook), nil
}

// queueIssueMoves writes, as the event seq that put row in stage is logged,
// one IssueMove for every issue row tracks whose source has a hook with a
// map for the bead's type and stage. A relates link moves nothing.
func (w *wtx) queueIssueMoves(row *beadRow, seq uint64, stage string) error {
	links, err := beadLinks(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	links = slices.DeleteFunc(links, func(l linkRow) bool { return l.Kind != LinkTracks })
	if len(links) == 0 {
		return nil
	}
	// Collected first: a hook is not read under the cursor a put moves.
	var moves []IssueMove
	err = each(w.Tx, bHooks, "", func(_ []byte, h *Hook) error {
		state, mapped := h.Map[MapKey(row.Type, stage)]
		if !mapped || h.As == "" {
			return nil
		}
		for _, l := range links {
			if l.Source == h.Source {
				moves = append(moves, IssueMove{ID: fmt.Sprintf("%d/%s", seq, h.Name), Seq: seq, Hook: h.Name, As: h.As,
					Bead: row.UUID, Type: row.Type, Stage: stage, State: state,
					Source: l.Source, URL: l.URL, Thread: l.Thread, At: now()})
			}
		}
		return nil
	})
	for _, m := range moves {
		if err == nil {
			err = put(w.Tx, bIssueMoves, issueMoveKey(m.Seq, m.Hook), m)
		}
	}
	return err
}

// hearStage queues the issue moves of bead event e, when it is one that put
// the bead in another stage. A bead sent back to the stage it was in (open
// to open on failed) entered nothing. A bead's creation is heard by
// CreateBead instead, once its links are made.
func (w *wtx) hearStage(e *Event) error {
	if e.Kind != "advanced" {
		return nil
	}
	from, _ := e.Data["from"].(string)
	to, _ := e.Data["to"].(string)
	if to == "" || to == from {
		return nil
	}
	row := new(beadRow)
	if ok, err := get(w.Tx, bBeads, *e.Bead, row); err != nil || !ok {
		return err
	}
	return w.queueIssueMoves(row, e.Seq, to)
}

// dropIssueMoves forgets the moves hook had still to make, and says how
// many.
func dropIssueMoves(tx *bolt.Tx, hook string) (int, error) {
	var drop []string
	err := each(tx, bIssueMoves, "", func(k []byte, m *IssueMove) error {
		if m.Hook == hook {
			drop = append(drop, string(k))
		}
		return nil
	})
	for _, k := range drop {
		if err == nil {
			err = tx.Bucket(bIssueMoves).Delete([]byte(k))
		}
	}
	return len(drop), err
}

// IssueMoves lists the issue moves still to make, oldest event first.
func (s *Store) IssueMoves(ctx context.Context) ([]IssueMove, error) {
	out := []IssueMove{}
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		return each(tx, bIssueMoves, "", func(_ []byte, m *IssueMove) error {
			m.Bead = h.of(m.Bead)
			out = append(out, *m)
			return nil
		})
	})
	return out, err
}

// changeIssueMove reads the move id names for fn, which ends it (done) or
// leaves a changed record to keep.
func (s *Store) changeIssueMove(id string, fn func(w *wtx, m *IssueMove) (done bool, err error)) error {
	key, err := issueMoveID(id)
	if err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		var m IssueMove
		if ok, err := get(w.Tx, bIssueMoves, key, &m); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("issue move %s: %w", id, ErrNotFound)
		}
		done, err := fn(w, &m)
		if err != nil {
			return err
		}
		if done {
			return w.Bucket(bIssueMoves).Delete([]byte(key))
		}
		return put(w.Tx, bIssueMoves, key, m)
	})
}

// IssueMoveFailed records a call that failed and when the next is due. No
// event is logged for a try: the move shows its last error while it waits
// (IssueMoves), and thread_state_skipped tells of one given up.
func (s *Store) IssueMoveFailed(ctx context.Context, id, reason string, retryAt time.Time) error {
	return s.changeIssueMove(id, func(_ *wtx, m *IssueMove) (bool, error) {
		at := retryAt.UTC()
		m.Attempts, m.Error, m.RetryAt = m.Attempts+1, reason, &at
		return false, nil
	})
}

// mayMove is the refusal of a move whose resident may not set a status in
// its source: it is gone, or its definition does not allow the source
// (SetState). "" when it may.
func mayMove(tx *bolt.Tx, m *IssueMove) (string, error) {
	var r Resident
	ok, err := get(tx, bResidents, m.As, &r)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("resident %s, which hook %s acts as, is gone", m.As, m.Hook), nil
	}
	if !slices.Contains(r.MaySetState(), m.Source) {
		return fmt.Sprintf("resident %s may not set the status of threads in %s: its definition does not allow it. The operator allows it with yardr resident set %s %s=%s",
			m.As, m.Source, m.As, SetState, strings.Join(append(r.MaySetState(), m.Source), ",")), nil
	}
	return "", nil
}

// IssueMoveAllowed says why the move id names may not be made (see
// mayMove), "" when it may. The daemon asks before it calls the source.
func (s *Store) IssueMoveAllowed(ctx context.Context, id string) (string, error) {
	key, err := issueMoveID(id)
	if err != nil {
		return "", err
	}
	var why string
	err = s.read(func(tx *bolt.Tx) error {
		var m IssueMove
		if ok, err := get(tx, bIssueMoves, key, &m); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("issue move %s: %w", id, ErrNotFound)
		}
		why, err = mayMove(tx, &m)
		return err
	})
	return why, err
}

// moveData is what both of a move's events say: the hook that caused it,
// the resident acted as, the issue, the stage entered, the word its map has
// for it and the event that put the bead there.
func (m *IssueMove) data(extra map[string]string) map[string]any {
	data := map[string]any{"hook": m.Hook, "resident": m.As, "source": m.Source, "url": m.URL,
		"stage": m.Stage, "state": m.State, "cause": m.Seq}
	if m.Thread != "" {
		data["thread"] = m.Thread
	}
	for k, v := range extra {
		if v != "" {
			data[k] = v
		}
	}
	return data
}

// IssueMoved ends the move id names: the issue was moved. It logs
// thread_state on the bead, as StateSet does on a resident's journal, with
// the hook as the actor and in the data, the stage, the event that caused
// it and the resident acted as; ts says what the source did (its thread and
// title when the link had none). A move whose resident may not set a status
// (mayMove) is refused, so nothing is on record that it was not allowed.
func (s *Store) IssueMoved(ctx context.Context, id string, ts ThreadState) error {
	if ts.Status == "" {
		return fmt.Errorf("issue move %s: the status set is required", id)
	}
	return s.changeIssueMove(id, func(w *wtx, m *IssueMove) (bool, error) {
		if why, err := mayMove(w.Tx, m); err != nil {
			return false, err
		} else if why != "" {
			return false, fmt.Errorf("issue move %s: %s", id, why)
		}
		data := m.data(map[string]string{"thread": ts.Thread, "title": ts.Title, "status": ts.Status, "was": ts.Was, "instead": ts.Instead})
		return true, w.event(m.Bead, "thread_state", "hook:"+m.Hook, data)
	})
}

// IssueSkip is why a move was not made: Reason is SkipNoGrant, SkipPast or
// SkipFailed, Detail says it in words, and Status is the status the issue
// was found in, when it was read.
type IssueSkip struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status,omitempty"`
	Title  string `json:"title,omitempty"`
	Thread string `json:"thread,omitempty"`
}

// IssueMoveSkipped ends the move id names without the issue having been
// moved, and logs thread_state_skipped on the bead with the reason.
func (s *Store) IssueMoveSkipped(ctx context.Context, id string, skip IssueSkip) error {
	if !slices.Contains([]string{SkipNoGrant, SkipPast, SkipFailed}, skip.Reason) {
		return fmt.Errorf("issue move %s: %q is no reason to skip it", id, skip.Reason)
	}
	return s.changeIssueMove(id, func(w *wtx, m *IssueMove) (bool, error) {
		data := m.data(map[string]string{"thread": skip.Thread, "title": skip.Title, "status": skip.Status,
			"reason": skip.Reason, "detail": skip.Detail})
		if m.Attempts > 0 {
			data["attempts"] = m.Attempts
		}
		return true, w.event(m.Bead, "thread_state_skipped", "hook:"+m.Hook, data)
	})
}
