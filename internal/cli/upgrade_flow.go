package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// upgradeRun installs the depot's build, verified, or rolls back: the
// command of design yardr-6ja6 sections 3.1-3.3 and 3.6. Timeout bounds the
// whole multi-yard gate; noRestore overrides the upgrade_restore setting
// for one run, and fromFlag names the depot for one run instead of the
// upgrade_from setting.
func (a *app) upgradeRun(w, warn io.Writer, ctx context.Context, cfg *config.Config, timeout time.Duration, noRestore bool, fromFlag string) error {
	runStarted := time.Now()
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be greater than zero")
	}
	if fromFlag != "" {
		if set, ok := store.SettingOf(store.UpgradeFromSetting); ok && set.Check != nil {
			if err := set.Check(fromFlag); err != nil {
				return err
			}
		}
	}
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
			return notInitialized{cfg.Home}
		}
	}
	serverWasOwn := serverRunning(ctx, cfg)
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	// --from stands in for upgrade_from, which only the operator may set:
	// the same line holds for the flag, or anyone could pick the source
	// built over the yard's binary.
	if fromFlag != "" {
		if err := a.operatorOnly(ctx, st, "name the depot to install from (--from)"); err != nil {
			return err
		}
	}
	from := fromFlag
	if from == "" {
		from = settings[store.UpgradeFromSetting]
	}
	if from == "" {
		return fmt.Errorf("upgrade_from is not set: the yard installs nothing until it names a depot (%s settings set upgrade_from <depot>)", yardrAt(cfg.Home))
	}
	depot, err := st.GetDepot(ctx, from)
	if err != nil {
		return fmt.Errorf("upgrade_from names depot %s, which is not in the yard: %v", from, err)
	}
	if fi, err := os.Stat(depot.Path); err != nil || !fi.IsDir() {
		return fmt.Errorf("depot %s has no checkout on this machine (%s): pull it first", from, depot.Path)
	}
	if _, err := lookPath("go"); err != nil {
		return fmt.Errorf("go is not on PATH: the install builds from the checkout, so it needs the toolchain (%v)", err)
	}
	target, err := upgradeInstallTarget()
	if err != nil {
		return err
	}
	if goos == "darwin" {
		if home, herr := userHomeDir(); herr == nil {
			if in := protectedDepots([]store.Depot{*depot}, home); len(in) > 0 {
				fmt.Fprintf(warn, "warn: the depot is in a folder macOS protects (%s): the new build may need its access granted again\n", strings.Join(in, ", "))
			}
		}
	}
	yards := upgradeCoveredYards(cfg, target)
	restoreOn := upgradeRestoreOn(settings, noRestore)
	actor, err := config.Actor()
	if err != nil {
		return err
	}

	// The install serializes beside the binary, not in any yard's home:
	// what it guards, the installed file and the one-generation yardr.prev,
	// is shared by every yard on the machine.
	lockPath := filepath.Join(filepath.Dir(target), "yardr.upgrade.lock")
	unlock, err := lockDaemon(lockPath, 0)
	if err != nil {
		reason := err.Error()
		data := map[string]any{"revision": version.Show(version.Current()), "stage": "lock", "reason": reason}
		upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed, data,
			fmt.Sprintf("yardr upgrade refused: %s", reason))
		return fmt.Errorf("another yardr upgrade holds %s: %s; the next landing tries again", lockPath, reason)
	}
	defer unlock()

	// What ran before, per yard, and the pre-install backup with its head.
	was := make([]upgradeYardWas, 0, len(yards))
	for _, home := range yards {
		yc := config.At(home)
		before := upgradeYardWas{home: home, server: serverRunning(ctx, yc), daemon: config.DaemonRunning(yc.DaemonLock())}
		if home == cfg.Home {
			before.server = serverWasOwn || before.server
		}
		was = append(was, before)
	}
	for i, before := range was {
		yc := config.At(before.home)
		if !before.server {
			// The backup needs the server, and the gate needs one to
			// verify; what the upgrade started it leaves running on
			// success, and restarts nothing of on rollback.
			if err := upgradeServerStart(ctx, yc); err != nil {
				upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed,
					map[string]any{"revision": version.Show(version.Current()), "stage": "backup", "reason": fmt.Sprintf("the server in %s could not be started: %v", before.home, err)},
					fmt.Sprintf("yardr upgrade failed at backup: the server in %s could not be started: %v", before.home, err))
				return fmt.Errorf("the server in %s could not be started: %w", before.home, err)
			}
			was[i].startedBy = true
			was[i].server = true
		}
		backup := yc.BeforeInstallBackup()
		if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
			return err
		}
		var res *store.BackupResult
		if before.home == cfg.Home {
			res, err = st.Backup(ctx, backup)
		} else {
			c := rpc.NewClient(yc.Socket())
			res, err = c.Backup(ctx, backup)
			_ = c.Close(ctx)
		}
		if err != nil {
			reason := fmt.Sprintf("the store in %s could not be backed up: %v", before.home, err)
			upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed,
				map[string]any{"revision": version.Show(version.Current()), "stage": "backup", "reason": reason},
				"yardr upgrade failed at backup: "+reason)
			return errors.New(reason)
		}
		was[i].head, was[i].backup = res.Head, res.Path
		fmt.Fprintf(w, "%-9s  %s\n", "backup", res.Path)
	}

	bindir := filepath.Dir(target)
	prev := filepath.Join(bindir, "yardr.prev")
	prevRev := version.Show(version.Current())
	if b, err := version.OfFile(target); err == nil {
		prevRev = version.Show(b.Revision)
	}
	if err := copyFile(target, prev); err != nil {
		reason := fmt.Sprintf("the running binary could not be kept as %s: %v", prev, err)
		upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed,
			map[string]any{"revision": prevRev, "stage": "install", "reason": reason},
			"yardr upgrade failed at install: "+reason)
		return errors.New(reason)
	}
	fmt.Fprintf(w, "%-9s  %s (%s)\n", "keep", prev, prevRev)

	gateStart := upgradeGateStart()
	for _, before := range was {
		if err := config.WriteUpgradeGate(before.home, config.UpgradeGate{PID: os.Getpid(), Start: gateStart, Head: before.head}); err != nil {
			for _, r := range was {
				_ = os.Remove(config.At(r.home).UpgradeGate())
			}
			return err
		}
	}
	removeGates := func() {
		for _, before := range was {
			_ = os.Remove(config.At(before.home).UpgradeGate())
		}
	}

	// Start both the pass-evidence window and the shared health deadline at
	// the install boundary. Preflight work (backups and the previous binary)
	// must not let a pass from before installation satisfy the gate, or eat
	// into the time the new build has to prove itself.
	installStarted := time.Now()
	gateDeadline := installStarted.Add(timeout)
	if out, err := upgradeGoInstall(ctx, depot.Path); err != nil {
		removeGates()
		reason := fmt.Sprintf("go install . in %s failed: %v", depot.Path, err)
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			if len(trimmed) > 2000 {
				trimmed = trimmed[:2000]
			}
			reason += ": " + trimmed
		}
		upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed,
			map[string]any{"revision": prevRev, "stage": "build", "reason": reason},
			"yardr upgrade failed at build: "+reason)
		return errors.New(reason)
	}
	newBuild, err := version.OfFile(target)
	if err != nil {
		removeGates()
		reason := fmt.Sprintf("the installed binary at %s cannot be read: %v", target, err)
		upgradeTellLive(warn, ctx, st, settings, actor, store.InstallFailed,
			map[string]any{"revision": prevRev, "stage": "install", "reason": reason},
			"yardr upgrade failed at install: "+reason)
		return errors.New(reason)
	}
	newRev := version.Show(newBuild.Revision)
	fmt.Fprintf(w, "%-9s  go install . at %s\n", "install", newRev)

	// A stopped server is started first, per yard, so there is always a
	// server to verify; nothing else is started.
	for i, before := range was {
		yc := config.At(before.home)
		if !serverRunning(ctx, yc) {
			if _, err := upgradeRunInstalled(ctx, target, before.home, "server", "start"); err == nil {
				was[i].startedBy = true
			}
		}
	}

	// Each yard's web view where it runs as a login service is restarted
	// on the new build, so the view serves what the install wrote. Keep a
	// failed restart for the gate too: --check skips a view that did not
	// come back, and would otherwise let the install pass.
	restartErrors := make([]error, len(was))
	for i, before := range was {
		if err := upgradeRestartViews(ctx, config.At(before.home)); err != nil {
			restartErrors[i] = err
			fmt.Fprintf(warn, "warn: the web view in %s could not be restarted: %v\n", before.home, err)
		}
	}

	since := installStarted.Format(time.RFC3339Nano)
	homes := make([]string, len(was))
	for i, before := range was {
		homes[i] = before.home
	}
	results := upgradePoll(ctx, target, homes, since, gateDeadline)
	results = upgradePollWithRestartFailures(results, restartErrors)
	failed := []upgradePollResult{}
	for _, r := range results {
		if !r.ok {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		removeGates()
		upgradeLogMarked(warn, ctx, st, cfg.Home, actor)
		fmt.Fprintf(w, "%-9s  ok in %s\n", "health", time.Since(runStarted).Round(time.Second))
		return nil
	}

	// The gate failed: what the window held, read from the live stores
	// before they are stopped and possibly replaced.
	beads := []string{}
	seen := map[string]bool{}
	for _, before := range was {
		var evs []store.Event
		if before.home == cfg.Home {
			evs, _ = st.EventsSince(ctx, before.head, 0)
		} else {
			c := rpc.NewClient(config.At(before.home).Socket())
			evs, _ = c.EventsSince(ctx, before.head, 0)
			_ = c.Close(ctx)
		}
		for _, e := range evs {
			if e.Bead != nil && !seen[*e.Bead] {
				seen[*e.Bead] = true
				beads = append(beads, *e.Bead)
			}
		}
	}
	reason := "the new build failed its health check: "
	parts := []string{}
	for _, f := range failed {
		parts = append(parts, fmt.Sprintf("%s: %s", f.home, f.why))
	}
	reason += strings.Join(parts, "; ")

	// Rollback: the previous bytes back over the installed path once, then
	// per yard daemon first and server, as yardr down does, stopping no
	// agent. The old build is each store's oracle.
	outcomes := make([]config.UpgradeYardOutcome, 0, len(was))
	restoredAny := false
	downAny := false
	if err := copyFile(prev, target); err != nil {
		reason += fmt.Sprintf("; the previous binary could not be restored: %v", err)
		for _, before := range was {
			outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: "the previous binary could not be restored: " + err.Error()})
		}
		downAny = true
	} else {
		for _, before := range was {
			yc := config.At(before.home)
			outcome := "back on " + prevRev
			restored := false
			// Daemon first: nothing is dispatched while the rest is stopped.
			if _, err := upgradeStopDaemon(yc, stopWait); err != nil {
				outcome = "the daemon could not be stopped: " + err.Error()
				outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
				downAny = true
				continue
			}
			if err := upgradeServerStop(yc); err != nil && !strings.Contains(err.Error(), "no yardr server is running") {
				outcome = "the server could not be stopped: " + err.Error()
				outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
				downAny = true
				continue
			}
			needsRestore := false
			if s, err := store.Open(yc.DBPath()); err != nil {
				if errors.Is(err, store.ErrNewerSchema) {
					needsRestore = true
				} else if !errors.Is(err, store.ErrLocked) {
					// The old build refuses the store for another reason:
					// it stays down with the marker.
					outcome = "the old build refuses the store: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
					downAny = true
					continue
				} else {
					outcome = "the store is still held: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
					downAny = true
					continue
				}
			} else {
				_ = s.Close(context.Background())
			}
			if needsRestore {
				if !restoreOn {
					outcome = fmt.Sprintf("the old build refuses the migrated store, and %s is off: restore %s over %s by hand, then %s up",
						store.UpgradeRestoreSetting, before.backup, yc.DBPath(), yardrAt(before.home))
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
					downAny = true
					continue
				}
				if err := copyFile(before.backup, yc.DBPath()); err != nil {
					outcome = "the pre-install copy could not be restored: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome})
					downAny = true
					continue
				}
				restored, restoredAny = true, true
				outcome = "restored from the pre-install copy, back on " + prevRev
				// The old build on the pre-install copy is the bottom:
				// where even that refuses, the yard stays down.
				if s, err := store.Open(yc.DBPath()); err != nil {
					_ = s
					outcome = "the old build refuses even the pre-install copy: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome, Restored: true})
					downAny = true
					continue
				} else {
					_ = s.Close(context.Background())
				}
			}
			// Only what ran before is restarted, per yard; agents keep
			// running through the restart, and their sessions predate
			// the snapshot.
			if before.server {
				if err := upgradeServerStart(ctx, yc); err != nil {
					outcome = "the server could not be started again: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome, Restored: restored})
					downAny = true
					continue
				}
				if !serverRunning(ctx, yc) {
					outcome = "the server does not answer after the rollback"
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome, Restored: restored})
					downAny = true
					continue
				}
			}
			if before.daemon {
				if _, err := upgradeStartDaemon(yc); err != nil {
					outcome = "the daemon could not be started again: " + err.Error()
					outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome, Restored: restored})
					downAny = true
					continue
				}
			}
			outcomes = append(outcomes, config.UpgradeYardOutcome{Home: before.home, Outcome: outcome, Restored: restored})
		}
	}
	removeGates()

	if !downAny {
		data := map[string]any{"from": newRev, "to": prevRev, "restored": restoredAny, "beads": beads, "reason": reason}
		for i, o := range outcomes {
			data[fmt.Sprintf("yard_%d", i)] = map[string]any{"home": o.Home, "outcome": o.Outcome, "restored": o.Restored}
		}
		text := fmt.Sprintf("yardr upgrade to %s failed its health check and was rolled back to %s", newRev, prevRev)
		if restoredAny {
			text += " (stores restored)"
		}
		text += ": " + reason
		if len(beads) > 0 {
			text += "; beads changed in the window: " + strings.Join(beads, ", ")
		}
		upgradeTellLive(warn, ctx, st, settings, actor, store.RolledBack, data, text)
		return errors.New(text)
	}

	// Dead yards: the marker is the record, and the escape hatch tells
	// subscribed users directly from the pre-install copy.
	sends := upgradeHatch(ctx, cfg.Home, store.RolledBack, actor, newRev, reason, beads)
	marker := config.UpgradeFailed{Kind: store.RolledBack, Revision: newRev, Stage: "health", Reason: reason, Yards: outcomes, Beads: beads, Sends: sends, At: time.Now()}
	_ = config.WriteUpgradeFailed(cfg.Home, marker)
	text := fmt.Sprintf("yardr upgrade to %s failed and the yard is down: %s", newRev, reason)
	if len(beads) > 0 {
		text += "; beads changed in the window: " + strings.Join(beads, ", ")
	}
	text += fmt.Sprintf("; see %s, and yardr doctor faults on it until the next successful upgrade clears it", config.At(cfg.Home).UpgradeFailed())
	// A hand run without a notify source prints the failure on the
	// terminal, where the person is: not silent either way.
	fmt.Fprintf(warn, "%s\n", text)
	return errors.New(text)
}

// upgradeTellLive logs a yard failure event with no bead and mails the
// yardmaster and the operator, while the yard answers. Where an older
// binary does not know the kinds yet, logging refuses with "not a dispatch
// event", and the command carries on with the mails.
func upgradeTellLive(warn io.Writer, ctx context.Context, st store.API, settings map[string]string, actor, kind string, data map[string]any, mailText string) {
	if err := st.LogDispatch(ctx, "", kind, actor, data); err != nil && !strings.Contains(err.Error(), "not a dispatch event") {
		fmt.Fprintf(warn, "warn: the %s event could not be logged: %v\n", kind, err)
	}
	ym := settings[store.YardmasterSetting]
	if ym == "" {
		ym = "yardmaster"
	}
	op := settings[store.OperatorSetting]
	for _, to := range []string{ym, op} {
		if to == "" {
			continue
		}
		if err := st.Mail(ctx, to, mailText, actor); err != nil {
			fmt.Fprintf(warn, "warn: mail to %s could not be sent: %v\n", to, err)
		}
	}
}

// upgradeHatch sends the one message to subscribed users with the server
// down: it opens the pre-install copy read-only and matches its
// yardr-source subscriptions with the store's own matcher, then sends
// through each matched user's source. During the outage only users are
// told; the yardmaster reads the stored event on recovery.
func upgradeHatch(ctx context.Context, home, kind, actor, rev, reason string, beads []string) []config.UpgradeSend {
	var sends []config.UpgradeSend
	h, err := store.OpenHatchCopy(config.At(home).BeforeInstallBackup())
	if err != nil {
		return nil
	}
	text := fmt.Sprintf("yardr upgrade to %s failed and the yard is down: %s", rev, reason)
	if len(beads) > 0 {
		text += "; beads changed in the window: " + strings.Join(beads, ", ")
	}
	for _, u := range h.Users() {
		if u.Name == actor {
			continue
		}
		matched := false
		for _, sub := range u.Subscriptions {
			if store.MatchBeadless(sub, kind, actor) != "" {
				matched = true
				break
			}
		}
		if !matched || u.Notify == "" {
			continue
		}
		sent, err := upgradeNotifySend(ctx, h, u.Name, notify.Message{Title: "yardr upgrade failed (" + kind + ")", Text: text})
		s := config.UpgradeSend{User: u.Name, Source: u.Notify}
		if err != nil {
			s.Error = err.Error()
		} else if sent != nil {
			s.Source, s.Status, s.OK = sent.Source, sent.Status, sent.OK()
			if !s.OK {
				s.Error = fmt.Sprintf("the source answered %d", sent.Status)
			}
		}
		sends = append(sends, s)
	}
	return sends
}

// upgradeLogMarked logs the previous dead-yard failure on a successful
// upgrade, marked told only where every direct send succeeded, and clears
// the marker. A failed send leaves it untold, so normal delivery tells
// again once the yard is back.
func upgradeLogMarked(warn io.Writer, ctx context.Context, st store.API, home, actor string) {
	m, err := config.ReadUpgradeFailed(home)
	if err != nil {
		return
	}
	data := map[string]any{"revision": m.Revision, "stage": m.Stage, "reason": m.Reason, "beads": m.Beads}
	for i, o := range m.Yards {
		data[fmt.Sprintf("yard_%d", i)] = map[string]any{"home": o.Home, "outcome": o.Outcome, "restored": o.Restored}
	}
	if m.AllTold() {
		data["told"] = true
	}
	if err := st.LogDispatch(ctx, "", m.Kind, actor, data); err != nil {
		if strings.Contains(err.Error(), "not a dispatch event") {
			_ = os.Remove(config.At(home).UpgradeFailed())
			return
		}
		fmt.Fprintf(warn, "warn: the marked %s event could not be logged: %v; the marker stays\n", m.Kind, err)
		return
	}
	_ = os.Remove(config.At(home).UpgradeFailed())
}

// serverRunning is defined in doctor.go; re-export for this file's tests.
var _ = server.Running
