package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PassStamp is what the daemon writes to DaemonPass at the end of every
// completed pass: the build the pass ran on and when it ended. The upgrade
// check reads it as evidence of a pass on the build it verifies.
type PassStamp struct {
	// Build is the build the pass ran on, as version.Show has it.
	Build string `json:"build"`
	// At is when the pass ended, by the dispatcher's clock.
	At time.Time `json:"at"`
}

// WritePassStamp writes s to the home's DaemonPass, through a temporary
// file renamed into place, so a reader never sees a half-written stamp.
func WritePassStamp(home string, s PassStamp) error {
	return writeJSON(At(home).DaemonPass(), s)
}

// ReadPassStamp reads the home's DaemonPass. A home with no stamp errs as
// a missing file does.
func ReadPassStamp(home string) (PassStamp, error) {
	var s PassStamp
	if err := readJSON(At(home).DaemonPass(), &s); err != nil {
		return PassStamp{}, err
	}
	return s, nil
}

// UpgradeGate is what an upgrade writes to UpgradeGate before it installs:
// the process whose life pauses dispatch, and the store's head when the
// upgrade backed it up. The daemon reads only PID and Start; Head is the
// upgrade's own, for what the install's window held.
type UpgradeGate struct {
	PID int `json:"pid"`
	// Start tells the process from a later one with its pid, as
	// peer.Alive reads it; 0 where the OS tells no start time.
	Start uint64 `json:"start"`
	// Head is the store's head read with the pre-install backup.
	Head uint64 `json:"head"`
}

// WriteUpgradeGate writes g to the home's UpgradeGate, through a temporary
// file renamed into place, so the daemon never reads a half-written gate.
func WriteUpgradeGate(home string, g UpgradeGate) error {
	return writeJSON(At(home).UpgradeGate(), g)
}

// ReadUpgradeGate reads the home's UpgradeGate. A home with no gate errs
// as a missing file does; a gate that names no process errs as a bad one.
func ReadUpgradeGate(home string) (UpgradeGate, error) {
	var g UpgradeGate
	path := At(home).UpgradeGate()
	if err := readJSON(path, &g); err != nil {
		return UpgradeGate{}, err
	}
	if g.PID <= 0 {
		return UpgradeGate{}, fmt.Errorf("bad upgrade gate %s: no pid", path)
	}
	return g, nil
}

// UpgradeYardOutcome is what one covered yard came to in an upgrade that
// failed: its home and the outcome in words, with whether its store was
// restored from the pre-install copy.
type UpgradeYardOutcome struct {
	Home     string `json:"home"`
	Outcome  string `json:"outcome"`
	Restored bool   `json:"restored,omitempty"`
}

// UpgradeSend is who the dead-yard escape hatch told outside the yard,
// with what the send answered.
type UpgradeSend struct {
	User   string `json:"user"`
	Source string `json:"source,omitempty"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// OK is whether the send succeeded. The marked event is logged told
	// only when every send did.
	OK bool `json:"ok"`
}

// UpgradeFailed is what a failed upgrade writes to UpgradeFailed when the
// yard is left down: the revision it tried, the stage and the reason,
// every covered yard and its outcome, each bead that changed in the
// window, and who was already told outside the yard. Doctor reports it as
// a FAULT until the next successful upgrade logs the failure event and
// clears it, or it is removed by hand.
type UpgradeFailed struct {
	// Kind is the failure event to log on recovery: install_failed or
	// rolled_back.
	Kind     string               `json:"kind"`
	Revision string               `json:"revision"`
	Stage    string               `json:"stage"`
	Reason   string               `json:"reason"`
	Yards    []UpgradeYardOutcome `json:"yards"`
	Beads    []string             `json:"beads,omitempty"`
	Sends    []UpgradeSend        `json:"sends,omitempty"`
	At       time.Time            `json:"at"`
}

// WriteUpgradeFailed writes m to the home's UpgradeFailed, through a
// temporary file renamed into place.
func WriteUpgradeFailed(home string, m UpgradeFailed) error {
	return writeJSON(At(home).UpgradeFailed(), m)
}

// ReadUpgradeFailed reads the home's UpgradeFailed. A home with no marker
// errs as a missing file does.
func ReadUpgradeFailed(home string) (UpgradeFailed, error) {
	var m UpgradeFailed
	if err := readJSON(At(home).UpgradeFailed(), &m); err != nil {
		return UpgradeFailed{}, err
	}
	return m, nil
}

// AllTold reports whether every direct send in the marker succeeded. The
// recovery logging marks the event told only then; a failed send leaves
// it untold, so normal delivery tells again once the yard is back. With
// no sends there is nothing marked told.
func (m UpgradeFailed) AllTold() bool {
	if len(m.Sends) == 0 {
		return false
	}
	for _, s := range m.Sends {
		if !s.OK {
			return false
		}
	}
	return true
}

// writeJSON writes v as JSON to path through a temporary file in its
// directory, renamed into place.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readJSON reads path as JSON into v. A missing file errs as one does.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("bad %s: %w", filepath.Base(path), err)
	}
	return nil
}
