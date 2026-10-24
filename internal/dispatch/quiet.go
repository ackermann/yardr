package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// The whole suite when the yard is quiet (docs/testing.md R6): the landing no
// longer gates every bead with it once the incremental landing lands, so
// it still runs, here, as a backstop. It starts on yard events only: the
// trigger subscribes to the event stream (Store.Subscribe, pushed as
// events commit) and judges the yard on each event that can make it quiet
// or owe a run -- a bead advancing (leaving approved, landing in merged),
// a bead closing, a session ending. There is no poll, no timer and no
// sleep loop in the trigger: between events it waits on the stream, and
// the yard's own tick never judges quiet. The run starts only when all
// hold at once, so a threshold reached while the yard is busy is not
// lost: the count stays in the log, and the next quiet event fires it.
//
//   - no bead waits in approved for the assembly anywhere in the yard;
//   - no live bead session is running anywhere in the yard: one that runs
//     something on this machine, an agent or a script (builder, reviewer
//     and assembly sessions do; crew sessions are not bead sessions). A
//     manual group's claim holds a bead for a person and a peer group's
//     for another yard, so neither counts;
//   - at least quiet_landings code landings (store.QuietLandings, ten
//     while unset) have landed on the depot's base since the last quiet
//     run started there.
//
// One run at a time, over every depot: while one runs, a further event
// starts no second, and the trigger re-arms only when the first finishes.
// The run holds the machine-wide race lock ($HOME/.yardr/race.lock, the
// flock .yardr/test's race legs hold) for its whole length and tells the
// test command so (YARDR_RACE_HELD), so it never overlaps a landing leg.
// A green run is told as quiet_passed, a red base as quiet_failed naming
// the failing packages, the failing tests and the base commit; each
// carries the wall time and the load the run started under.

// QuietResult is how one quiet whole-suite run ended, as QuietRun (or the
// real runner) reports it: the base commit it tested, how long it took,
// the load it started under, and, on a red run, the packages that failed,
// the failing test names, capped, and each failing package's output, by
// package. A red run that names no package keeps its whole output under
// quietRunKey instead. A passing run keeps nothing: Outputs is nil.
type QuietResult struct {
	Commit   string
	Wall     time.Duration
	Load     string
	Packages []string
	Tests    []string
	Outputs  map[string]string
}

// quietRelevant says whether an event can change whether a quiet run is
// owed or can start: a bead moving (approved emptying, a landing), a bead
// closing, a session ending. Anything else is not judged, so a busy log
// costs the trigger nothing.
func quietRelevant(e store.Event) bool {
	switch e.Kind {
	case "advanced", "closed", "held", "released", "group_removed",
		"session_died", "session_failed", "session_aborted":
		return true
	case "setting_changed":
		return e.Data["key"] == store.QuietLandingsSetting
	}
	return false
}

// runQuietTrigger is the quiet run's trigger, for Run to keep beside the
// reconcile loop: judge once at start (landings that passed while no
// daemon ran are still owed), then on each relevant event of the stream.
// When the stream ends (the store closed, or this subscriber fell too far
// behind), it subscribes again from the last event seen; judging is
// idempotent, so no run is lost or doubled by the seam.
func (d *Dispatcher) runQuietTrigger(ctx context.Context) {
	after, err := d.Store.Head(ctx)
	if err != nil {
		d.Log.Warn("quiet run: cannot read the event log; the backstop is off until the daemon restarts", "err", err)
		return
	}
	for first := true; ctx.Err() == nil; first = false {
		ch, err := d.Store.Subscribe(ctx, after)
		if err != nil {
			d.Log.Warn("quiet run: cannot watch events; the backstop is off until the daemon restarts", "err", err)
			return
		}
		if first {
			// Subscribed first, so nothing committed while judging is
			// missed: it is on the stream behind this pass.
			d.maybeQuiet(ctx)
		}
		for e := range ch {
			after = e.Seq
			if quietRelevant(e) {
				d.maybeQuiet(ctx)
			}
		}
	}
}

// maybeQuiet starts a quiet run for the first depot that is owed one and
// quiet, unless one already runs. It reads the threshold fresh, so a
// changed quiet_landings is judged by the next event.
func (d *Dispatcher) maybeQuiet(ctx context.Context) {
	d.quietMu.Lock()
	if d.quietRunning {
		d.quietMu.Unlock()
		return
	}
	d.quietMu.Unlock()
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		d.Log.Warn("quiet run: settings", "err", err)
		return
	}
	n := store.QuietLandings(settings)
	if n == 0 {
		return // turned off; the whole suite runs where someone runs it
	}
	depots, err := d.Store.Depots(ctx)
	if err != nil {
		d.Log.Warn("quiet run: depots", "err", err)
		return
	}
	slices.SortFunc(depots, func(a, b store.Depot) int { return strings.Compare(a.Name, b.Name) })
	for _, depot := range depots {
		if depot.IsDir() || depot.Test == "" {
			continue // no suite to run: a directory, or no test command
		}
		ready, since, err := d.quietReady(ctx, depot, n)
		if err != nil {
			d.Log.Warn("quiet run: judging", "depot", depot.Name, "err", err)
			continue
		}
		if !ready {
			continue
		}
		d.quietMu.Lock()
		if d.quietRunning {
			d.quietMu.Unlock()
			return
		}
		d.quietRunning = true
		d.quietMu.Unlock()
		go d.runQuietDepot(ctx, depot, since)
		return
	}
}

// quietReady judges whether one depot owes a run (at least n code landings
// since its last one started) while the whole yard is quiet: nothing waits
// in approved and no live bead session runs anything on this machine. since
// is the landing count either way, for the run's own telling.
func (d *Dispatcher) quietReady(ctx context.Context, depot store.Depot, n int) (ready bool, since int, err error) {
	since, err = d.landingsSinceQuiet(ctx, depot.Name)
	if err != nil || since < n {
		return false, since, err
	}
	waiting, err := d.Store.ListBeads(ctx, store.BeadFilter{Stage: "approved"})
	if err != nil {
		return false, since, err
	}
	if len(waiting) > 0 {
		return false, since, nil
	}
	sessions, err := d.Store.Sessions(ctx, false)
	if err != nil {
		return false, since, err
	}
	// Only a session that runs something here loads the machine, which is
	// what store.RunsHere says and what max_sessions counts by. A manual
	// group's claim holds a bead for a person, and the backlog alone holds
	// dozens at any time; a peer group's bead is worked by another yard.
	// Counting either kept the yard from ever being quiet (yardr-hqsn).
	for _, s := range sessions {
		if store.RunsHere(s.RunnerName()) {
			return false, since, nil
		}
	}
	return true, since, nil
}

// landingsSinceQuiet counts a depot's code landings since its last quiet
// run started: beads advanced to merged after that run's quiet_started,
// wagons left out (they land on a train's branch; the train's own landing
// on the base is the one that counts). The epoch is the run's start, not
// its end, so a landing that passes while a run tests an older base is
// counted toward the next run, never covered unseen.
func (d *Dispatcher) landingsSinceQuiet(ctx context.Context, depot string) (int, error) {
	var epoch uint64
	// Keep a marker for every depot. A fixed cap could drop a quieter
	// depot's last start behind runs from other depots, then count its old
	// landings again and schedule a redundant suite run.
	started, err := d.Store.EventsOfKind(ctx, store.QuietStarted, int(^uint(0)>>1))
	if err != nil {
		return 0, err
	}
	for _, e := range started {
		if name, _ := e.Data["depot"].(string); name == depot && e.Seq > epoch {
			epoch = e.Seq
		}
	}
	count, after := 0, epoch
	for {
		evs, err := d.Store.EventsSince(ctx, after, 1000)
		if err != nil {
			return 0, err
		}
		for _, e := range evs {
			after = e.Seq
			if e.Kind != "advanced" || e.Bead == nil {
				continue
			}
			if to, _ := e.Data["to"].(string); to != "merged" {
				continue
			}
			b, err := d.Store.GetBead(ctx, *e.Bead)
			if err != nil || b.Depot == nil || *b.Depot != depot || b.Type == "wagon" {
				continue
			}
			count++
		}
		if len(evs) < 1000 {
			return count, nil
		}
	}
}

// runQuietDepot runs one depot's whole suite and tells the yard how it
// ended, then re-arms the trigger: landings counted during the run may
// already owe the next one, and the finishing is itself the quiet event
// that fires it.
func (d *Dispatcher) runQuietDepot(ctx context.Context, depot store.Depot, since int) {
	defer func() {
		d.quietMu.Lock()
		d.quietRunning = false
		d.quietMu.Unlock()
		if ctx.Err() == nil {
			d.maybeQuiet(ctx)
		}
	}()
	base := depot.Base
	if base == "" {
		base = "main"
	}
	if err := d.Store.LogDispatch(ctx, "", store.QuietStarted, actor,
		map[string]any{"depot": depot.Name, "base": base, "landings": since}); err != nil {
		d.Log.Warn("quiet run: started event", "depot", depot.Name, "err", err)
	}
	run := d.QuietRun
	if run == nil {
		run = d.execQuietRun
	}
	res, err := run(ctx, depot)
	data := map[string]any{"depot": depot.Name, "base": base, "commit": res.Commit,
		"wall_s": int(res.Wall.Round(time.Second).Seconds()), "load": res.Load, "landings": since}
	if err != nil {
		data["packages"] = res.Packages
		data["tests"] = res.Tests
		data["reason"] = err.Error()
		if files := d.quietFiles(ctx, depot, res); len(files) > 0 {
			data["files"] = files
		}
		d.Log.Error("quiet whole-suite run failed", "depot", depot.Name, "commit", res.Commit,
			"wall", res.Wall.Round(time.Second), "load", res.Load, "packages", res.Packages, "tests", res.Tests, "err", err)
		if lerr := d.Store.LogDispatch(ctx, "", store.QuietFailed, actor, data); lerr != nil {
			d.Log.Warn("quiet run: failed event", "depot", depot.Name, "err", lerr)
		}
		return
	}
	d.Log.Info("quiet whole-suite run passed", "depot", depot.Name, "commit", res.Commit,
		"wall", res.Wall.Round(time.Second), "load", res.Load)
	if lerr := d.Store.LogDispatch(ctx, "", store.QuietPassed, actor, data); lerr != nil {
		d.Log.Warn("quiet run: passed event", "depot", depot.Name, "err", lerr)
	}
}

// quietFiles stores a red run's failing packages' output, each as a blob,
// and answers the refs, as mail names the files it carries: one blob per
// failing package, in the packages' order, named for the package. A red run
// that names no package keeps one blob in their place, its whole output
// named run.log. One past the yard's blob cap is truncated to it, since
// PutBlob refuses over-cap with nothing stored: a package's output keeps its
// start, the run's keeps its end, as its verdict comes last. A package with
// no output, or one the yard will not keep, names nothing: the failure is
// told either way.
func (d *Dispatcher) quietFiles(ctx context.Context, depot store.Depot, res QuietResult) []store.BlobRef {
	limit := int64(store.DefaultBlobMax)
	if settings, err := d.Store.Settings(ctx); err == nil {
		if v, ok := settings[store.BlobMaxSetting]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				limit = n
			}
		}
	}
	keys, truncate := res.Packages, quietTruncate
	if len(keys) == 0 {
		keys, truncate = []string{quietRunKey}, quietTruncateTail
	}
	var files []store.BlobRef
	for _, key := range keys {
		out, ok := res.Outputs[key]
		if !ok || out == "" {
			continue
		}
		ref, err := d.Store.PutBlob(ctx, truncate([]byte(out), limit))
		if err != nil {
			d.Log.Warn("quiet run: keeping a red run's output", "depot", depot.Name, "output", key, "err", err)
			continue
		}
		ref.Name = quietFileName(key)
		files = append(files, *ref)
	}
	return files
}

// quietRunKey is the key a red run that names no failing package keeps its
// own output under in QuietResult.Outputs. Outputs holds the packages' keys
// or this one, never both: quietFiles reads it only when no package is named.
const quietRunKey = "run"

// quietFileName is the name a red run's blob carries for the package (or for
// the run, under quietRunKey): the package with its slashes flattened, so it
// holds as a file's name, and a .log suffix, so the inbox line reads which
// package each blob is.
func quietFileName(pkg string) string {
	flat := strings.ReplaceAll(strings.ReplaceAll(pkg, "/", "_"), "\\", "_")
	return flat + ".log"
}

// quietTruncate holds one failing package's output to the yard's blob cap:
// the headful that fits, then a marker naming the cap when it fits too.
// Under that size, the marker itself would make PutBlob refuse the output,
// so the available prefix is kept instead.
func quietTruncate(out []byte, limit int64) []byte {
	if int64(len(out)) <= limit {
		return out
	}
	marker := fmt.Sprintf("\n[truncated at %d bytes]\n", limit)
	if limit < int64(len(marker)) {
		return slices.Clone(out[:limit])
	}
	keep := limit - int64(len(marker))
	if keep < 0 {
		keep = 0
	}
	return append(slices.Clone(out[:keep]), marker...)
}

// quietTruncateTail holds a red run's own output to the yard's blob cap, the
// reverse of quietTruncate: what the file is for (a budget verdict, a vet or
// build error, the test command's last words) comes last, so the end is what
// it keeps, behind a marker naming the cap when that fits too. Under that
// size the marker itself would make PutBlob refuse the output, so the
// available end is kept instead.
func quietTruncateTail(out []byte, limit int64) []byte {
	if int64(len(out)) <= limit {
		return out
	}
	marker := fmt.Sprintf("[truncated at %d bytes, the start is not kept]\n", limit)
	if limit < int64(len(marker)) {
		return slices.Clone(out[int64(len(out))-limit:])
	}
	keep := limit - int64(len(marker))
	return append([]byte(marker), out[int64(len(out))-keep:]...)
}

var failPackage = regexp.MustCompile(`(?m)^FAIL[ \t]+(\S+)`)

// quietPackages names the packages a whole-suite output failed: the FAIL
// lines go test prints per package, each once, in the order printed.
func quietPackages(out string) []string {
	var pkgs []string
	for _, m := range failPackage.FindAllStringSubmatch(out, -1) {
		if !slices.Contains(pkgs, m[1]) {
			pkgs = append(pkgs, m[1])
		}
	}
	return pkgs
}

var failTest = regexp.MustCompile(`--- FAIL: (\S+)`)

// quietTestCap is how many failing test names a red quiet run carries: a
// broken helper fails tens of tests, and the event names enough to triage
// by, not all of them.
const quietTestCap = 20

// quietTests names the tests a whole-suite output failed: the --- FAIL
// lines go test prints, as the gate's flaky lines read them (an indented
// one is a subtest, named with its parents), each once, in the order
// printed, capped. A run that failed with no test line (a panic, a build
// failure) names none, and its packages still say where.
func quietTests(out string) []string {
	var tests []string
	for _, m := range failTest.FindAllStringSubmatch(out, -1) {
		if slices.Contains(tests, m[1]) {
			continue
		}
		tests = append(tests, m[1])
		if len(tests) == quietTestCap {
			break
		}
	}
	return tests
}

// okPackage is a passing package's summary line: ok, or ? for a package
// without test files, then the package and its duration. The duration is
// required, so a test's own two-word "ok ..." line never drops a failing
// block.
var okPackage = regexp.MustCompile(`^(?:ok|\?)[ \t]+\S+[ \t]+\S+`)

// quietOutputs slices a whole-suite output per failing package: go test
// prints each package's output as one block ending in its summary line, so
// a block ending in a FAIL line is that package's output, kept by package,
// and one ending in ok (or ?, for a package without test files) is
// dropped: a passing package keeps nothing, and lines before the first
// summary go with the first block, kept or dropped with it. Lines after
// the last summary join the last failing
// block, so the run's own tail is kept with it. A package named twice
// keeps its first block, as quietPackages names each once. Nil where
// nothing failed.
func quietOutputs(out string) map[string]string {
	var outputs map[string]string
	var block []string
	last := ""
	flush := func() {
		if last != "" && len(block) > 0 {
			outputs[last] += strings.Join(block, "\n") + "\n"
		}
		block = nil
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			continue // the split's tail, not a line of the output
		}
		if m := failPackage.FindStringSubmatch(line); m != nil {
			block = append(block, line)
			if outputs == nil {
				outputs = map[string]string{}
			}
			if _, seen := outputs[m[1]]; !seen {
				outputs[m[1]] = strings.Join(block, "\n") + "\n"
				last = m[1]
			}
			block = nil
			continue
		}
		if okPackage.MatchString(line) {
			block = nil
			continue
		}
		block = append(block, line)
	}
	flush()
	return outputs
}

// quietRunOutputs is what a red run keeps of its output: each failing
// package's (quietOutputs) or, where no FAIL line names a package, the whole
// output under quietRunKey. A package over the 60 s budget under the strict
// R1 verdict (YARDR_R1_STRICT) and a vet or build error fail a run with no
// such line, and without its output the reader runs the whole suite again to
// learn which it was.
func quietRunOutputs(out string) map[string]string {
	if outputs := quietOutputs(out); outputs != nil {
		return outputs
	}
	return map[string]string{quietRunKey: out}
}

// quietLoad is the machine's one-minute load as the yard finds it, for a
// quiet run to start under and be logged with: /proc/loadavg where the
// system keeps it, sysctl's vm.loadavg elsewhere, "unknown" where neither
// answers. It never fails a run.
func quietLoad() string {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f, _, ok := strings.Cut(string(b), " "); ok {
			return f
		}
	}
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return "unknown"
	}
	// "{ 3.52 3.61 3.40 }" on darwin.
	f := strings.Fields(strings.Trim(string(out), "{} \n"))
	if len(f) == 0 {
		return "unknown"
	}
	return f[0]
}

// quietRaceLock holds the machine-wide race lock (.yardr/test's flock)
// until the returned release, so a quiet run never overlaps a landing
// leg. The wait is the kernel's, like the script's: a landing leg ahead
// is waited out, never polled. The lock is under the process user's home,
// even when this yard has a custom home of its own.
func quietRaceLock() (release func(), err error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home := filepath.Join(userHome, ".yardr")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, "race.lock"), os.O_CREATE|os.O_RDWR, 0o600)
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

// quietCommand gives a test command its own process group. CommandContext
// otherwise kills only the shell running .yardr/test; a go test below it
// could outlive the daemon, after the daemon had released the race lock.
func quietCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}

// execQuietRun is the real quiet run: the depot's base in a worktree of
// its own, tested as a landing tests it (the depot's test command with
// YARDR_LANDING=1), under the race lock throughout, except that R1 is
// judged strictly (YARDR_R1_STRICT, which the quiet run alone sets): a
// package over 60 s alone fails however loaded the machine, so a slowdown
// cannot loud-pass here as it may at a landing. YARDR_QUIET_RUN is the
// quiet run's other mark, for the tests themselves: a test that costs
// minutes (the yard-shape matrix, contrib/isolated-yard/matrix_test.go)
// runs only where it is set, so neither a builder's gate nor a landing
// pays for it. The depot's own checkout is never touched, and the
// worktree goes with the run.
func (d *Dispatcher) execQuietRun(ctx context.Context, depot store.Depot) (QuietResult, error) {
	base := depot.Base
	if base == "" {
		base = "main"
	}
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	commit, err := git("-C", depot.Path, "rev-parse", "refs/heads/"+base)
	if err != nil {
		return QuietResult{Load: quietLoad()}, fmt.Errorf("quiet run: %s has no base %s: %w", depot.Name, base, err)
	}
	tmp, err := os.MkdirTemp("", "yardr-quiet-")
	if err != nil {
		return QuietResult{Commit: commit, Load: quietLoad()}, err
	}
	// The test command tells its packages by the worktree's path as git
	// prints it (git rev-parse --show-toplevel in .yardr/test's pick),
	// which is resolved (/private/tmp for /tmp here); an unresolved
	// temp path names no package there and the run fails at the pick.
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	defer os.RemoveAll(tmp)
	if _, err := git("-C", depot.Path, "worktree", "add", "--detach", tmp, commit); err != nil {
		return QuietResult{Commit: commit, Load: quietLoad()}, fmt.Errorf("quiet run: worktree of %s at %s: %w", depot.Name, commit, err)
	}
	defer func() {
		out, err := exec.Command("git", "-C", depot.Path, "worktree", "remove", "--force", tmp).CombinedOutput()
		if err != nil && d.Log != nil {
			d.Log.Warn("quiet run: remove worktree", "depot", depot.Name, "worktree", tmp,
				"err", err, "output", strings.TrimSpace(string(out)))
		}
	}()

	release, err := quietRaceLock()
	if err != nil {
		return QuietResult{Commit: commit, Load: quietLoad()}, fmt.Errorf("quiet run: race lock: %w", err)
	}
	defer release()
	load := quietLoad()

	var cmd *exec.Cmd
	if _, err := os.Stat(filepath.Join(tmp, depot.Test)); err == nil && !strings.ContainsAny(depot.Test, " \t|&;<>") {
		cmd = quietCommand(ctx, "sh", depot.Test) // the base's own copy, as a landing runs it
	} else {
		cmd = quietCommand(ctx, "sh", "-c", depot.Test)
	}
	cmd.Dir = tmp
	// The environment of work the yard starts (config.WithoutServerSettings,
	// as the runner withholds for a session or a script): the daemon's own
	// server settings are not the suite's, and a server a test starts from
	// them would listen on this yard's webhook, web or BDP address.
	cmd.Env = append(config.WithoutServerSettings(os.Environ()), "YARDR_LANDING=1", "YARDR_BASE="+base, "YARDR_RACE_HELD=1", "YARDR_R1_STRICT=1", "YARDR_QUIET_RUN=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// The tests start with nothing of the daemon's above stdio, as a
	// landing's do (runner.Exec): this run is a landing's tests without the
	// landing script that closes them.
	version.CloseOnExecAboveStdio()
	start := time.Now()
	runErr := cmd.Run()
	res := QuietResult{Commit: commit, Wall: time.Since(start), Load: load,
		Packages: quietPackages(out.String()), Tests: quietTests(out.String())}
	if runErr != nil {
		res.Outputs = quietRunOutputs(out.String())
		return res, fmt.Errorf("the whole suite failed on %s at %s: %w", base, commit, runErr)
	}
	return res, nil
}
