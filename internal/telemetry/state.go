package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	// so a pass makes at most one attempt per day; LastDaySent is the
	// source-day cursor, advanced only after a 2xx. The daemon's sender
	// keeps both; show reads LastAttemptDay.
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
// writing leaves the old one. Writers are single CLI invocations for
// now; the daemon's sender joins them later.
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
