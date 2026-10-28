package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// State is the install id and its consent bookkeeping, kept in
// <home>/telemetry.json (mode 0600), not in the store, so a store backup
// cannot resurrect a forgotten id into a restored yard, and deleting the
// file is a complete local forget. Days are UTC dates, stamps UTC
// timestamps to the second.
type State struct {
	// InstallID is the random id PostHog sees as distinct_id.
	InstallID string `json:"install_id"`
	// Minted is the UTC date the id was first minted.
	Minted string `json:"minted"`
	// Grants and Withdrawals are the UTC timestamps of consent grants
	// (yardr telemetry on) and withdrawals (yardr telemetry off), oldest
	// first, so the operator can show when telemetry was enabled.
	Grants      []string `json:"grants,omitempty"`
	Withdrawals []string `json:"withdrawals,omitempty"`
	// EligibleFrom is the first full UTC day after the latest on: a
	// source day counts only when telemetry was enabled for that whole
	// day, so activity from before consent is never included.
	EligibleFrom string `json:"eligible_from,omitempty"`
	// LastAttemptDay is the UTC sending day of the latest POST attempt,
	// written before the request begins so that a restarted daemon makes
	// no second attempt that day; LastDaySent is the source-day cursor,
	// advanced only after a 2xx that names nothing quota-limited. The
	// daemon's sender keeps both (MarkAttempt, MarkSent); show reads
	// LastAttemptDay.
	LastAttemptDay string `json:"last_attempt_day,omitempty"`
	LastDaySent    string `json:"last_day_sent,omitempty"`
}

// LoadState reads the state file; a missing file is no state, not an
// error, since show does not mint. A file that does not parse names
// itself and the way out.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w; remove it to forget this yard's install id and start over", path, err)
	}
	return st, nil
}

// Save replaces the state file in one step, so a run killed while
// writing leaves the old one. A writer that reads the file first holds
// lockState from its read to its Save.
func (s *State) Save(path string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".telemetry-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// latestGrant is the latest consent grant's timestamp, "" for none.
func (s *State) latestGrant() string {
	if s == nil || len(s.Grants) == 0 {
		return ""
	}
	return s.Grants[len(s.Grants)-1]
}

// since is the UTC date of the latest grant, "" for none: the "since" of
// telemetry: on.
func (s *State) since() string {
	if g := s.latestGrant(); len(g) >= len(dayLayout) {
		return g[:len(dayLayout)]
	}
	return ""
}

// lockState takes the exclusive lock of the state file's writers, waiting
// while another holds it, and returns its release. The file has two kinds
// of writer, yardr telemetry on and off and the daemon's sender, each
// reading it, changing a few fields and writing it back, so each does so
// under this lock and none writes back what it read before another
// changed it. The lock is a sidecar file: Save replaces the state file
// itself, and a lock on the replaced file would hold nothing. A lock dies
// with its process, so a dead holder never wedges the yard.
func lockState(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// lockedState reads the state file under the writers' lock and returns it
// with the lock's release, which is always safe to call. A file that is
// not there is nil and takes no lock: a yard that never turned telemetry
// on has nothing to lock, and an onboarding that was answered No must not
// leave a lock file in its home.
func lockedState(path string) (*State, func(), error) {
	none := func() {}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, none, nil
	}
	unlock, err := lockState(path)
	if err != nil {
		return nil, none, err
	}
	state, err := LoadState(path)
	if err != nil || state == nil {
		unlock()
		return nil, none, err
	}
	return state, unlock, nil
}

// update changes the state file as it stands under its lock, when it is
// there and holds install id id: the sender builds its batch from a copy
// it read before a request that may take seconds, and what it writes after
// must not undo what an off and an on, or a forget, did meanwhile. change
// returns whether to write the file back; update reports whether it did.
// A file that is gone is not recreated: deleting it is a complete local
// forget.
func update(path, id string, change func(*State) bool) (bool, error) {
	state, unlock, err := lockedState(path)
	if err != nil {
		return false, err
	}
	defer unlock()
	if state == nil || state.InstallID != id || !change(state) {
		return false, nil
	}
	return true, state.Save(path)
}

// MarkAttempt records the UTC day of now as the day of a POST attempt,
// before the request begins, so a restarted daemon cannot make a second
// attempt that day. It reports false, and sends nothing should follow,
// when the file is gone, holds another id than the batch's, or already
// holds the day.
func MarkAttempt(path, id string, now time.Time) (bool, error) {
	day := dayOf(now)
	return update(path, id, func(s *State) bool {
		if s.LastAttemptDay == day {
			return false
		}
		s.LastAttemptDay = day
		return true
	})
}

// MarkSent advances the source-day cursor through day, after a 2xx that
// named nothing quota-limited. It never moves the cursor back.
func MarkSent(path, id, through string) error {
	_, err := update(path, id, func(s *State) bool {
		if through <= s.LastDaySent {
			return false
		}
		s.LastDaySent = through
		return true
	})
	return err
}
