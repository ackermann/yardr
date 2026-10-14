package cli

import (
	"bytes"
	"context"
	"fmt"
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
var upgradeStartDaemon = func(cfg *config.Config) (int, error) {
	return kindOf(svcDaemon).start(cfg, nil, "")
}

// upgradeExecutable is this process's binary; tests replace it.
var upgradeExecutable = os.Executable

// upgradeGoEnv runs go env for the names; tests replace it.
var upgradeGoEnv = func(names ...string) (string, error) {
	out, err := exec.Command("go", append([]string{"env"}, names...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// upgradeGoInstall runs go install . in dir; tests replace it.
var upgradeGoInstall = func(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "install", ".")
	cmd.Dir = dir
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

// upgradeInstallTarget is where go install . writes the yardr binary: GOBIN,
// else the first GOPATH's bin, joined with this binary's name. It refuses
// when that is not the binary this yard runs from: installing bytes the
// server does not watch would verify nothing.
func upgradeInstallTarget() (string, error) {
	self, err := upgradeExecutable()
	if err != nil {
		return "", err
	}
	self, _ = filepath.Abs(self)
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	name := filepath.Base(self)
	gobin, err := upgradeGoEnv("GOBIN")
	if err != nil {
		return "", fmt.Errorf("go env GOBIN: %w", err)
	}
	gobin = strings.TrimSpace(strings.Split(gobin, "\n")[0])
	dir := gobin
	if dir == "" {
		gopath, err := upgradeGoEnv("GOPATH")
		if err != nil {
			return "", fmt.Errorf("go env GOPATH: %w", err)
		}
		first := strings.TrimSpace(strings.Split(gopath, "\n")[0])
		if first == "" {
			return "", fmt.Errorf("go env GOPATH is empty, and GOBIN is unset: go install has nowhere to write")
		}
		// GOPATH is a list; go install uses the first.
		sep := string(os.PathListSeparator)
		if i := strings.Index(first, sep); i >= 0 {
			first = first[:i]
		}
		dir = filepath.Join(first, "bin")
	}
	target := filepath.Join(dir, name)
	abs, _ := filepath.Abs(target)
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	} else {
		abs = target
		if a, err := filepath.Abs(target); err == nil {
			abs = a
		}
	}
	if abs != self {
		return "", fmt.Errorf("go install would write %s, not the binary this yard runs from (%s): install it by hand there first", target, self)
	}
	return target, nil
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
	out := make([]upgradePollResult, len(yards))
	done := make(chan int, len(yards))
	for i, home := range yards {
		go func(i int, home string) {
			out[i] = upgradePollOne(ctx, bin, home, since, deadline)
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

// upgradePollOne polls one yard until it passes or the deadline passes.
func upgradePollOne(ctx context.Context, bin, home, since string, deadline time.Time) upgradePollResult {
	last := "the yard could not be checked"
	for {
		out, err := upgradeRunInstalled(ctx, bin, home, "upgrade", "--check", "--since", since)
		if err == nil {
			return upgradePollResult{home: home, ok: true}
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
		if time.Now().After(deadline) {
			return upgradePollResult{home: home, why: last}
		}
		select {
		case <-ctx.Done():
			return upgradePollResult{home: home, why: last}
		case <-time.After(upgradePollEvery):
		}
	}
}

// upgradeBeadsSince lists the distinct beads the window held: the events
// since head, read from the live store before it is replaced.
func upgradeBeadsSince(ctx context.Context, st store.API, head uint64) []string {
	evs, err := st.EventsSince(ctx, head, 0)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range evs {
		if e.Bead == nil || seen[*e.Bead] {
			continue
		}
		seen[*e.Bead] = true
		out = append(out, *e.Bead)
	}
	slices.Sort(out)
	return out
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
	v, _ := set.Effective(stored)
	return v != "false"
}
