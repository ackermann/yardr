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
