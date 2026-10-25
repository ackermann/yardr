package server

import (
	"context"
	"log/slog"
	"os"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// The server's watch on the daemon (design yardr-zw3v): one goroutine
// looks at the daemon's last known activity on a slow tick. The daemon
// alone checks crew liveness, so a dead daemon leaves every crew session's
// stored state behind as it was, and nothing would notice; the server, which
// the machine's service manager keeps up, notices instead. Where the
// newest known evidence of life (the last pass or a later start) is over
// daemonLostAfter old, with no daemon holding the lock and no episode
// standing, one daemon_lost event is written with a run card for yardr
// up beside it, and a fresh daemon_started ends the state.

// daemonWatchEvery is how often the server looks at the daemon's last
// pass; daemonLostAfter is how old it may be before the daemon reads as
// lost. A pass ends in seconds, so three minutes with no pass and no
// daemon holding the lock is a dead daemon, not a slow one.
const (
	daemonWatchEvery = 15 * time.Second
	daemonLostAfter  = 3 * time.Minute
)

// runDaemonWatcher is the server's one daemon-watching goroutine: it
// looks at once, and again on every tick, until ctx ends, and records a
// lost daemon (daemonWatchOnce).
func runDaemonWatcher(ctx context.Context, st *store.Store, home string, log *slog.Logger) {
	if _, err := daemonWatchOnce(ctx, st, home, time.Now(), log); err != nil && ctx.Err() == nil {
		log.Warn("daemon watch", "err", err)
	}
	tick := time.NewTicker(daemonWatchEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if _, err := daemonWatchOnce(ctx, st, home, now, log); err != nil && ctx.Err() == nil {
				log.Warn("daemon watch", "err", err)
			}
		}
	}
}

// daemonWatchOnce is one look at the daemon's last known activity. Where
// it is stale (daemonStale), no daemon holds the lock and no episode
// stands, it records the loss (store.NoteDaemonLost), which writes the
// one event and raises the yard's run card beside it. It answers whether
// it wrote. A daemon that holds the lock is starting or mid-pass, not
// lost, however old its last known activity reads.
func daemonWatchOnce(ctx context.Context, st *store.Store, home string, now time.Time, log *slog.Logger) (bool, error) {
	var stamp, start *time.Time
	if pass, err := config.ReadPassStamp(home); err != nil {
		if !os.IsNotExist(err) {
			log.Warn("daemon watch: the last pass is not read", "err", err)
		}
	} else {
		stamp = &pass.At
	}
	// A start is also evidence of life. Use it even when an older pass
	// stamp remains from the prior daemon: the new daemon may die before
	// completing its first pass, but it is not lost until its start is old.
	if at, ok, err := store.LastDaemonStart(ctx, st); err != nil {
		return false, err
	} else if ok {
		start = &at
	}
	if !daemonStale(now, stamp, start) {
		return false, nil
	}
	if config.DaemonRunning(config.At(home).DaemonLock()) {
		return false, nil
	}
	if _, open, err := store.DaemonLostEpisode(ctx, st); err != nil {
		return false, err
	} else if open {
		return false, nil
	}
	last := daemonLastAlive(stamp, start)
	bead, wrote, err := st.NoteDaemonLost(ctx, *last)
	if err != nil {
		return false, err
	}
	if wrote {
		if bead == "" {
			log.Info("daemon lost: no card, the yard has no operator", "last_alive", *last)
		} else {
			log.Info("daemon lost: its card stands", "last_alive", *last, "bead", bead)
		}
	}
	return wrote, nil
}

// daemonStale is the staleness rule: the newest known evidence of life is
// stale when it is over daemonLostAfter old. A yard with neither a pass
// nor a start never reads stale, and a daemon started within the threshold
// never does either, however old a prior pass stamp is.
func daemonStale(now time.Time, stamp, start *time.Time) bool {
	at := daemonLastAlive(stamp, start)
	return at != nil && now.Sub(*at) > daemonLostAfter
}

// daemonLastAlive is the newer of the last completed pass and the last
// start event. A start newer than the stamp supersedes that prior pass:
// the current daemon may have died before writing a fresh stamp.
func daemonLastAlive(stamp, start *time.Time) *time.Time {
	if stamp == nil || (start != nil && start.After(*stamp)) {
		return start
	}
	return stamp
}
