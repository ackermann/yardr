package dispatch

import (
	"os"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/version"
)

// gatePaused reports whether the upgrade gate pauses dispatch: while
// <home>/upgrade.gate names the live upgrade process, passes claim nothing
// and start no agent, and still stamp. A gate naming no live process is
// stale — its upgrade ended without removing it — and is removed, so a
// crashed upgrade never pauses dispatch for good. An unreadable gate is
// removed too: it names no process the daemon can wait on. Without a home
// there is no gate.
func (d *Dispatcher) gatePaused() bool {
	if d.Home == "" {
		return false
	}
	path := config.At(d.Home).UpgradeGate()
	g, err := config.ReadUpgradeGate(d.Home)
	if err != nil {
		if !os.IsNotExist(err) {
			d.Log.Warn("the upgrade gate is unreadable; removing it, and dispatch resumes", "err", err)
			if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
				d.Log.Warn("the upgrade gate could not be removed", "err", rerr)
			}
		}
		return false
	}
	if peer.Alive(g.PID, g.Start) {
		d.Log.Info("dispatch paused by the upgrade gate", "pid", g.PID)
		return true
	}
	d.Log.Info("the upgrade gate's process is gone; removing it, and dispatch resumes", "pid", g.PID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		d.Log.Warn("the upgrade gate could not be removed", "err", err)
	}
	return false
}

// stampPass records this completed pass's build and time, so the upgrade
// check reads evidence of a pass on the build it verifies. A pass without
// a home stamps nothing; a stamp that cannot be written warns, and fails
// no pass.
func (d *Dispatcher) stampPass() {
	if d.Home == "" {
		return
	}
	build := ""
	if d.Binary != nil {
		build = d.Binary.Running
	}
	s := config.PassStamp{Build: version.Show(build), At: d.now()}
	if err := config.WritePassStamp(d.Home, s); err != nil {
		d.Log.Warn("pass stamp", "err", err)
	}
}
