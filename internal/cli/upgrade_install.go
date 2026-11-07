package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// The upgrade install's tunables, as the design has them.
const (
	// upgradePollEvery is how often the gate samples each covered yard.
	upgradePollEvery = 5 * time.Second
	// upgradeDefaultTimeout bounds the whole multi-yard gate run: one
	// deadline for every yard, so the rewind window does not grow with
	// the number of yards.
	upgradeDefaultTimeout = 5 * time.Minute
)

// upgradeServerStart starts the home's server; tests replace it.
var upgradeServerStart = server.Start

// upgradeServerStop stops the home's server; tests replace it.
var upgradeServerStop = stopServer

// upgradeStopDaemon stops the home's daemon; tests replace it.
var upgradeStopDaemon = stopDaemon

// upgradeStartDaemon starts the home's daemon in the background; tests
// replace it.
var upgradeStartDaemon = func(environ []string, cfg *config.Config) (int, error) {
	return kindOf(svcDaemon).start(environ, cfg, nil, "", false)
}

// upgradeExecutable is this process's binary; tests replace it.
var upgradeExecutable = os.Executable

// upgradeGoBuild runs go build -buildmode=exe -o file . in dir; tests replace
// it. The build mode is named because a checkout whose root package is no
// program would otherwise build to its package archive, written to file with
// exit status 0, and the install would put that over the binary.
var upgradeGoBuild = func(ctx context.Context, dir, file string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=exe", "-o", file, ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// upgradeCodesignIdentifier is the signing identifier every signed yardr
// carries: fixed, so a Developer Tools entry naming it holds across
// reinstalls. It is no setting: one yardr binary means one identifier.
const upgradeCodesignIdentifier = "dev.yardr.yardr"

// upgradeCodesign signs target with identity under upgradeCodesignIdentifier;
// tests replace it.
var upgradeCodesign = func(ctx context.Context, identity, target string) (string, error) {
	cmd := exec.CommandContext(ctx, "codesign", "--force", "--sign", identity, "--identifier", upgradeCodesignIdentifier, target)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// upgradeRunInstalled runs the installed binary with --home and args;
// tests replace it to script the gate.
var upgradeRunInstalled = func(ctx context.Context, bin, home string, args ...string) (string, error) {
	full := append([]string{"--home", home}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// upgradeNotifySend tells a user through their source; tests replace it.
var upgradeNotifySend = notify.Send

// upgradeInstallTarget is where the upgrade writes the build: the binary this
// yard runs from, symlinks resolved. The server and the daemon watch that path
// for a newer build, so bytes written anywhere else would verify nothing, and
// where go install would write is no part of it: a yardr kept outside go's bin
// directory upgrades where it is. A link to the binary stays a link; the file
// it names is replaced.
func upgradeInstallTarget() (string, error) {
	self, err := upgradeExecutable()
	if err != nil {
		return "", err
	}
	return resolveProgram(self), nil
}

// upgradeInstall builds the checkout in dir to a temp file beside target and
// renames it over target, so the server's and the daemon's watch of the path
// sees one complete new file and never a build still being linked. The temp is
// removed on every path out: a failed build leaves target as it was and
// nothing beside it. The build's output comes back for the failure to quote.
func upgradeInstall(ctx context.Context, dir, target string) (string, error) {
	fi, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".build-*")
	if err != nil {
		return "", fmt.Errorf("the new build cannot be written beside %s: %w", target, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if out, err := upgradeGoBuild(ctx, dir, tmp.Name()); err != nil {
		return out, fmt.Errorf("go build . in %s failed: %w", dir, err)
	}
	// However go put its output there, the new file takes the mode of the one
	// it replaces, as a copy of it would (copyFile).
	if err := os.Chmod(tmp.Name(), fi.Mode()); err != nil {
		return "", err
	}
	return "", os.Rename(tmp.Name(), target)
}

// upgradeCoveredYards are the yards the install covers: its own home,
// always, plus every yard registered on this machine whose login service
// runs the shared binary — the homes the daemon services name in
// WorkingDirectory whose program is the install target. A registered yard
// on another binary is left out, and a yard with no login service is
// covered only when it is the upgrading yard.
func upgradeCoveredYards(cfg *config.Config, target string) []string {
	seen := map[string]bool{cfg.Home: true}
	out := []string{cfg.Home}
	manager := managerOf()
	if manager == "" {
		return out
	}
	dir, err := serviceDir()
	if err != nil {
		return out
	}
	pattern := "dev.yardr." + svcDaemon + ".*.plist"
	if manager == systemd {
		pattern = "yardr-" + svcDaemon + "-*.service"
	}
	paths, _ := filepath.Glob(filepath.Join(dir, pattern))
	want := resolveProgram(target)
	for _, p := range paths {
		home, prog := parseServiceHomeAndProgram(p)
		if home == "" || home == cfg.Home || seen[home] {
			continue
		}
		if prog == "" || resolveProgram(prog) != want {
			continue
		}
		seen[home] = true
		out = append(out, home)
	}
	slices.Sort(out[1:])
	return out
}

// resolveProgram is the program for comparison: absolute, with symlinks
// followed where the file exists.
func resolveProgram(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// parseServiceHomeAndProgram reads a daemon service file's home
// (WorkingDirectory) and the binary it runs first (ProgramArguments on
// launchd, ExecStart on systemd).
func parseServiceHomeAndProgram(path string) (home, prog string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	if m := workingDir.FindSubmatch(b); m != nil {
		home = strings.TrimSpace(string(m[1]) + string(m[2]))
		home = strings.ReplaceAll(home, "%%", "%")
	}
	if strings.HasSuffix(path, ".plist") {
		prog = plistProgram(b)
	} else {
		prog = unitProgram(b)
	}
	return home, prog
}

// plistProgram is the first string of the plist's ProgramArguments.
func plistProgram(b []byte) string {
	// <key>ProgramArguments</key><array><string>/path/yardr</string>...
	i := bytes.Index(b, []byte("ProgramArguments"))
	if i < 0 {
		return ""
	}
	rest := b[i:]
	j := bytes.Index(rest, []byte("<string>"))
	if j < 0 {
		return ""
	}
	rest = rest[j+len("<string>"):]
	k := bytes.Index(rest, []byte("</string>"))
	if k < 0 {
		return ""
	}
	return strings.TrimSpace(string(rest[:k]))
}

// unitProgram is the first word of the unit's ExecStart.
func unitProgram(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		v, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		// The program is the first word, quoted or not.
		if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`) {
			q := v[:1]
			if i := strings.Index(v[1:], q); i >= 0 {
				return v[1 : 1+i]
			}
			return ""
		}
		if i := strings.IndexAny(v, " \t"); i >= 0 {
			v = v[:i]
		}
		return v
	}
	return ""
}

// copyFile copies src to dst, replacing it, keeping src.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// upgradeGateStart is this process's start time for the gate file, 0 where
// the OS tells none.
func upgradeGateStart() uint64 {
	if st, err := peer.Describe(os.Getpid()); err == nil {
		return st.Start
	}
	return 0
}

// upgradeYardWas is what ran in a covered yard before the upgrade: the gate
// starts a stopped server to verify it, and the rollback restarts only
// what ran.
type upgradeYardWas struct {
	home      string
	server    bool
	daemon    bool
	head      uint64
	backup    string
	startedBy bool // the upgrade started its server for the gate
}

// upgradePollResult is what one covered yard came to in the gate.
type upgradePollResult struct {
	home string
	ok   bool
	why  string
}

// upgradePoll checks every covered yard in parallel under one deadline: each
// sample runs the installed binary's upgrade --check as a subprocess, so
// the predicate always runs in the build it asserts, passing the install's
// start as --since. A yard the gate cannot check fails it, named.
func upgradePoll(ctx context.Context, bin string, yards []string, since string, deadline time.Time) []upgradePollResult {
	// A deadline that only guards the polling loop is not enough: one
	// installed CLI can hang while checking a yard. Give every yard's
	// subprocess the same deadline too, so one stuck check cannot extend the
	// rewind window or hold up the other results.
	pollCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	out := make([]upgradePollResult, len(yards))
	done := make(chan int, len(yards))
	for i, home := range yards {
		go func(i int, home string) {
			out[i] = upgradePollOne(pollCtx, bin, home, since, deadline)
			done <- i
		}(i, home)
	}
	// Every yard answers: a cancelled context ends each poll promptly,
	// which still sends its result.
	for range yards {
		<-done
	}
	return out
}

// upgradePollWithRestartFailures makes a failed service restart fail the
// install gate even when --check sees no recorded view and skips that part.
func upgradePollWithRestartFailures(results []upgradePollResult, restartErrors []error) []upgradePollResult {
	for i, err := range restartErrors {
		if err == nil || i >= len(results) {
			continue
		}
		results[i].ok = false
		if results[i].why != "" {
			results[i].why += "; "
		}
		results[i].why += "web view restart failed: " + err.Error()
	}
	return results
}

// upgradePollOne polls one yard until it passes or the deadline passes.
func upgradePollOne(ctx context.Context, bin, home, since string, deadline time.Time) upgradePollResult {
	last := "the yard could not be checked"
	for {
		if err := ctx.Err(); err != nil {
			if err == context.DeadlineExceeded || !time.Now().Before(deadline) {
				last = "the health check deadline expired"
			}
			return upgradePollResult{home: home, why: last}
		}
		out, err := upgradeRunInstalled(ctx, bin, home, "upgrade", "--check", "--since", since)
		if ctx.Err() == context.DeadlineExceeded || !time.Now().Before(deadline) {
			return upgradePollResult{home: home, why: "the health check deadline expired"}
		}
		if err == nil {
			return upgradePollResult{home: home, ok: true}
		}
		if ctx.Err() != nil {
			return upgradePollResult{home: home, why: last}
		}
		last = strings.TrimSpace(out)
		if last == "" {
			last = err.Error()
		}
		// Keep only the failing lines, so the marker names the yard with why.
		if lines := strings.Split(last, "\n"); len(lines) > 1 {
			var fails []string
			for _, l := range lines {
				if strings.Contains(l, "fail:") {
					fails = append(fails, strings.TrimSpace(l))
				}
			}
			if len(fails) > 0 {
				last = strings.Join(fails, "; ")
			}
		}
		select {
		case <-ctx.Done():
			return upgradePollResult{home: home, why: last}
		case <-time.After(upgradePollEvery):
		}
	}
}

// upgradeRestoreOn reports whether the run restores refused stores: the
// --no-restore flag overrides the setting, and the setting reads through
// its environment variable.
func upgradeRestoreOn(stored map[string]string, noRestore bool) bool {
	if noRestore {
		return false
	}
	set, ok := store.SettingOf(store.UpgradeRestoreSetting)
	if !ok {
		return true
	}
	v, _ := set.Effective(stored, config.Getenv)
	return v != "false"
}

// webRestartWait is how long a restarted web view gets to answer /health
// before the upgrade goes on to the gate, which fails it there.
var webRestartWait = 30 * time.Second

// webServiceServes reports whether the web view's login service keeps a
// view running now: installed, and loaded in launchd (asked as yardr web
// status asks) or active in systemd. A service written but not loaded, as
// on a yard upgraded from an ssh or su session with no GUI login, keeps
// nothing: the view that runs there was started by hand. Tests replace it.
var webServiceServes = func(ctx context.Context, cfg *config.Config) bool {
	manager, name, _, err := unitFile(cfg, svcWeb)
	if err != nil || !serviceInstalled(cfg, svcWeb) {
		return false
	}
	return (&unit{Manager: manager, Name: name}).serving(ctx)
}

// upgradeRestartViews restarts the yard's web view where its login service
// keeps it running, so the view serves the build the install just wrote,
// and waits until /health answers. With no service, or one installed but
// not loaded, there is nothing to restart: a view started by hand is the
// operator's to restart, and the check names it. So is one whose launchd
// domain this session cannot reach (kickstart's exit 125, "Domain does not
// support specified action"), or whose service is gone when asked again
// after the refusal: neither is a failed install, and neither may roll it
// back. Tests replace serviceCtl and serve /health.
func upgradeRestartViews(ctx context.Context, cfg *config.Config) error {
	if !webServiceServes(ctx, cfg) {
		return nil
	}
	manager, name, _, err := unitFile(cfg, svcWeb)
	if err != nil {
		return nil
	}
	u := &unit{Manager: manager, Name: name}
	if err := ctl(ctx, u.restartCommand()); err != nil {
		if domainUnreachable(manager, err) || !u.serving(ctx) {
			return nil
		}
		return err
	}
	addr := serviceAddr(cfg, svcWeb)
	if addr == "" {
		return nil
	}
	health := "http://" + dialAddr(addr) + "/health"
	for deadline := time.Now().Add(webRestartWait); time.Now().Before(deadline); {
		if code, err := getStatus(health); err == nil && code != http.StatusNotFound {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery / 5):
		}
	}
	return fmt.Errorf("the web view did not answer %s within %s after the restart", health, webRestartWait)
}

// domainUnreachable reports whether a launchctl refusal says the session
// cannot act in the user's gui domain: exit 125, "Domain does not support
// specified action", as from an ssh session with no GUI login.
func domainUnreachable(manager string, err error) bool {
	var exit *exec.ExitError
	return manager == launchd && (errors.As(err, &exit) && exit.ExitCode() == 125 ||
		strings.Contains(err.Error(), "Domain does not support specified action"))
}
