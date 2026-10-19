package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// The verdicts of yardr upgrade --check's parts: ok, fail, or skip for a
// part the yard does not run.
const (
	checkOK   = "ok"
	checkFail = "fail"
	checkSkip = "skip"
)

// upgradeCheckReport is what yardr upgrade --check found: whether the yard
// runs this build, part by part.
type upgradeCheckReport struct {
	Home string `json:"home"`
	// Build is the revision this yardr runs; "" for a build that carries
	// none (go run, test binaries).
	Build  string             `json:"build"`
	OK     bool               `json:"ok"`
	Server upgradeCheckServer `json:"server"`
	Daemon upgradeCheckDaemon `json:"daemon"`
	Views  upgradeCheckViews  `json:"views"`
}

type upgradeCheckServer struct {
	Verdict string `json:"verdict"` // ok or fail
	Detail  string `json:"detail"`  // the words after "ok:"/"fail:"
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	// Build is the revision it answers on.
	Build string     `json:"build,omitempty"`
	Since *time.Time `json:"since,omitempty"`
}

type upgradeCheckDaemon struct {
	Verdict string `json:"verdict"` // ok, fail or skip
	Detail  string `json:"detail"`
	Running bool   `json:"running"`
	// Build is the build its latest start event names.
	Build string `json:"build,omitempty"`
	// LastPass is when its last pass ended, whatever build that ran on.
	LastPass *time.Time `json:"last_pass,omitempty"`
}

type upgradeCheckViews struct {
	Verdict string             `json:"verdict"` // ok, fail or skip
	Detail  string             `json:"detail"`
	Views   []upgradeCheckView `json:"views"`
}

type upgradeCheckView struct {
	URL string `json:"url"`
	// State is answers, old (from before GET /health) or dead.
	State string `json:"state"`
	// Build is the revision it recorded when it started, for people.
	Build string `json:"build,omitempty"`
}

// The states of one recorded web view, as the check probes it.
const (
	viewAnswers = "answers"
	viewOld     = "old"
	viewDead    = "dead"
)

// upgradeDescribe asks the home's server which process and build it is,
// with why none answers; tests replace it.
var upgradeDescribe = server.DescribeErr

// upgradeDaemonRunning probes the daemon's lock; tests replace it to script
// a flap.
var upgradeDaemonRunning = config.DaemonRunning

// upgradeHTTP is what the check reaches the web views with. A view answers
// on loopback in milliseconds; the bound is for one that hangs, so a hung
// view fails the check rather than the run.
var upgradeHTTP = &http.Client{Timeout: 5 * time.Second}

func (a *app) upgradeCmd() *cobra.Command {
	var check bool
	var since string
	var noRestore bool
	var timeout time.Duration
	var from string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the depot's build, verified, or roll back",
		Long: `Install the depot's build, verified, or roll back.

Without --check it does the hand step whole: from the checkout of the
depot --from names for one run, or the upgrade_from setting where the
flag is absent, it runs go install ., restarts each covered yard's web
view where it runs as a login service, checks every covered yard runs
it, and otherwise puts the previous build back. The covered yards are
this yard plus every yard registered on this machine whose login
service runs the shared binary. A yard with neither is untouched.

--from is how a hosted-style yard takes an upgrade: one whose operator
names no notify source cannot set upgrade_from, which stays refused so
the automatic path can never install untold, and names its depot on the
command instead. The caller takes a failure through the exit status and
the terminal, as a hand run does. Like the setting, the flag is the
operator's alone.

With --check it asserts the yard runs this build, read-only: the
install gate, by hand.

  server     answers on this build, the build of the yardr that was asked
  daemon     holds its lock on this build, and completed a pass on it
             since --since
  views      every recorded web view answers on this build

A part the yard does not run is skipped and reported, not failed: a yard
with no daemon running skips the daemon check, one with no web view skips
the views. A view on a build from before GET /health (it 404s /health and
answers /) is skipped with an old: line naming the restart, not failed.
A view on another build fails where the yard keeps its view as a login
service, which yardr upgrade restarts; where the view was started by hand
the line names the restart instead, and fails nothing.

The check changes nothing: it starts no server (one that does not answer
fails the server part with why), stores nothing, and a view it probes
keeps serving. The exit status is 1 when any part fails, 0 otherwise.
docs/upgrade.md says every line.`,
		Example: `  yardr upgrade --check
  yardr upgrade --check --since 30m --json | jq .ok
  yardr upgrade
  yardr upgrade --from yardr`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !check {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				return a.upgradeRun(c.OutOrStdout(), c.ErrOrStderr(), c.Context(), cfg, timeout, noRestore, from)
			}
			cutoff, err := upgradeSince(since)
			if err != nil {
				return err
			}
			cfg, err := a.configReadOnly()
			if err != nil {
				return err
			}
			rep, err := a.upgradeCheck(c.Context(), cfg, cutoff, since)
			if err != nil {
				return err
			}
			if err := a.out(c.OutOrStdout(), rep, rep.print); err != nil {
				return err
			}
			if !rep.OK {
				return errCheckFailed // the report said what
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "assert the yard runs this build, read-only, and report part by part")
	cmd.Flags().StringVar(&since, "since", "10m", "the daemon's last pass on this build must be this fresh: a duration back from now (10m, 90s) or a time as RFC3339")
	cmd.Flags().StringVar(&from, "from", "", "name the depot for one run, instead of the upgrade_from setting")
	cmd.Flags().BoolVar(&noRestore, "no-restore", false, "do not restore the pre-install store copies on failure: refused stores stay down for a person to restore")
	cmd.Flags().DurationVar(&timeout, "timeout", upgradeDefaultTimeout, "how long the whole multi-yard gate may take")
	return cmd
}

// upgradeSince is the --since flag as the time the daemon's last pass on
// this build must be at or after: a duration back from now, or the
// install's start as an RFC3339 time, which the gate passes.
func upgradeSince(flag string) (time.Time, error) {
	const want = "give a duration back from now (10m, 90s) or the install's start as RFC3339 (2026-10-14T09:02:11Z)"
	if d, err := parseDays(flag); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("--since %q: %s", flag, want)
		}
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, flag); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: %s", flag, want)
}

// upgradeCheck asserts the yard of cfg runs this build: the server answers
// on it, the daemon holds its lock on it with a pass since cutoff, and
// every recorded web view answers. since names the flag for the lines. A
// part the yard does not run is skipped, not failed. It starts no server:
// one that does not answer fails the server part with why.
func (a *app) upgradeCheck(ctx context.Context, cfg *config.Config, cutoff time.Time, since string) (*upgradeCheckReport, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, fs.ErrNotExist) {
			return nil, notInitialized{cfg.Home}
		}
	}
	rep := &upgradeCheckReport{Home: cfg.Home, Build: version.Current(), OK: true}
	daemonFirst := upgradeDaemonRunning(cfg.DaemonLock())
	rep.Server = checkServer(ctx, cfg)
	if a.st == nil && rep.Server.Running {
		// A client of the server that answered, not a.store: that one
		// starts a server when this one has stopped in between, and the
		// check starts none.
		a.st, a.cfg = rpc.NewClient(cfg.Socket()), cfg
	}
	rep.Daemon = a.checkDaemon(ctx, cfg, daemonFirst, cutoff, since)
	rep.Views = checkViews(cfg)
	// The lock on every sample of the run, not only the last: a daemon
	// that flapped (a crash loop under its service manager) fails.
	if last := upgradeDaemonRunning(cfg.DaemonLock()); last != daemonFirst {
		rep.Daemon = upgradeCheckDaemon{Running: last, Verdict: checkFail}
		if last {
			rep.Daemon.Detail = "started during the check; run it again"
		} else {
			rep.Daemon.Detail = "held the lock, then not: it flapped during the check (a crash loop under its service manager?)"
		}
	}
	rep.OK = rep.Server.Verdict != checkFail && rep.Daemon.Verdict != checkFail && rep.Views.Verdict != checkFail
	return rep, nil
}

// checkServer is the server part: it answers on this build. This implies
// the store opened and migrated — the server opens the store before it
// listens, and a migration that fails never listens — so no separate
// migration probe is needed.
func checkServer(ctx context.Context, cfg *config.Config) upgradeCheckServer {
	out := upgradeCheckServer{Verdict: checkOK}
	info, err := upgradeDescribe(ctx, cfg)
	if err != nil {
		out.Verdict, out.Detail = checkFail, serverWhy(cfg, err)
		return out
	}
	out.Running, out.PID, out.Build = true, info.PID, info.Revision
	if !info.Since.IsZero() {
		out.Since = &info.Since
	}
	if info.Revision != version.Current() {
		out.Verdict = checkFail
		out.Detail = fmt.Sprintf("answers on %s, not this build (%s)", version.Show(info.Revision), version.Show(version.Current()))
		return out
	}
	out.Detail = fmt.Sprintf("answers on %s, this build", version.Show(info.Revision))
	return out
}

// serverWhy is why no server answers, in words: stopped when the socket is
// gone, else the dial error.
func serverWhy(cfg *config.Config, err error) string {
	if errors.Is(err, syscall.ENOENT) {
		return fmt.Sprintf("the server is not running (start it: %s server start)", yardrAt(cfg.Home))
	}
	return "the server cannot be reached: " + strings.TrimPrefix(err.Error(), `Get "http://yard/ping": `)
}

// checkDaemon is the daemon part: running is whether it held its lock when
// the run opened. It holds its lock on this build — the to of its latest
// daemon_replaced or daemon_started event — and it completed a pass on
// this build at or after cutoff. One that does not run is skipped.
func (a *app) checkDaemon(ctx context.Context, cfg *config.Config, running bool, cutoff time.Time, since string) upgradeCheckDaemon {
	out := upgradeCheckDaemon{Verdict: checkOK, Running: running}
	if !running {
		out.Verdict, out.Detail = checkSkip, "no daemon runs"
		return out
	}
	if a.st == nil {
		out.Verdict, out.Detail = checkFail, "holds the lock, but its build cannot be read while the server is not running"
		return out
	}
	build, err := latestDaemonBuild(ctx, a.st)
	if err != nil {
		out.Verdict, out.Detail = checkFail, "holds the lock, but its start cannot be read: "+err.Error()
		return out
	}
	yardr := yardrAt(cfg.Home)
	if build == "" {
		// Started fresh before daemon_started existed, and never replaced
		// since: it needs a restart, not a guess.
		out.Verdict, out.Detail = checkFail, fmt.Sprintf("holds the lock, but no start names the build it runs; restart it (%s daemon stop, then %s daemon start)", yardr, yardr)
		return out
	}
	out.Build = build
	this := version.Show(version.Current())
	if build != this {
		out.Verdict = checkFail
		out.Detail = fmt.Sprintf("holds the lock on %s, not this build (%s)", build, this)
		return out
	}
	stamp, err := config.ReadPassStamp(cfg.Home)
	switch {
	case os.IsNotExist(err):
		out.Verdict = checkFail
		out.Detail = fmt.Sprintf("holds the lock on %s, this build, but never stamped a pass on it (wedged?)", this)
	case err != nil:
		out.Verdict = checkFail
		out.Detail = fmt.Sprintf("holds the lock on %s, this build, but its pass stamp cannot be read: %s", this, err)
	default:
		at := stamp.At
		out.LastPass = &at
		if stamp.Build != this {
			out.Verdict = checkFail
			out.Detail = fmt.Sprintf("holds the lock on %s, this build, but its last pass ran on %s, %s (wedged?)", this, stamp.Build, ago(time.Since(stamp.At)))
		} else if stamp.At.Before(cutoff) {
			out.Verdict = checkFail
			out.Detail = fmt.Sprintf("holds the lock on %s, this build, but its last pass on this build ended %s, older than --since %s (wedged?)", this, ago(time.Since(stamp.At)), since)
		} else {
			out.Detail = fmt.Sprintf("holds the lock on %s, this build, pass %s", this, ago(time.Since(stamp.At)))
		}
	}
	return out
}

// latestDaemonBuild is the build the daemon runs: the to of the latest
// daemon_replaced or daemon_started event, "" when neither was ever logged.
func latestDaemonBuild(ctx context.Context, st store.API) (string, error) {
	var latest *store.Event
	for _, kind := range []string{"daemon_replaced", "daemon_started"} {
		evs, err := st.EventsOfKind(ctx, kind, 1)
		if err != nil {
			return "", err
		}
		if len(evs) > 0 && (latest == nil || evs[0].Seq > latest.Seq) {
			latest = &evs[0]
		}
	}
	if latest == nil {
		return "", nil
	}
	build, _ := latest.Data["to"].(string)
	return build, nil
}

// ago is d in words, as the pass line says it.
func ago(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	default:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
}

// checkViews is the views part: every recorded web view answers on this
// build. A view that 404s /health but answers / is on a build from before
// the route and is skipped with an old: line, not failed. A view that
// answers /health on another build fails when the yard keeps its view as a
// login service — yardr upgrade restarts that service, so an old build
// after the restart means the restart failed — and is told with the
// restart when the view was started by hand, which the upgrade leaves
// alone. With no view at all the part is skipped.
func checkViews(cfg *config.Config) upgradeCheckViews {
	out := upgradeCheckViews{Verdict: checkOK, Views: []upgradeCheckView{}}
	views := webViews(cfg)
	if len(views) == 0 {
		out.Verdict, out.Detail = checkSkip, "no web view runs"
		return out
	}
	this := version.Current()
	service := webService(cfg)
	var current int
	var stale, byHand []viewBuild
	var olds, deads []string
	for _, v := range views {
		state, build := probeView(v.Addr)
		if build == "" {
			build = v.Revision
		}
		out.Views = append(out.Views, upgradeCheckView{URL: v.URL, State: state, Build: version.Show(build)})
		switch state {
		case viewAnswers:
			if build == this {
				current++
				continue
			}
			if service {
				stale = append(stale, viewBuild{URL: v.URL, Build: build})
			} else {
				byHand = append(byHand, viewBuild{URL: v.URL, Build: build})
			}
		case viewOld:
			olds = append(olds, v.URL)
		default:
			deads = append(deads, v.URL)
		}
	}
	parts := []string{}
	if current > 0 {
		parts = append(parts, answeredViews(current, this))
	}
	if len(stale) > 0 {
		out.Verdict = checkFail
		parts = append(parts, staleViews(stale, this))
	}
	if len(byHand) > 0 {
		parts = append(parts, handViews(byHand, yardrAt(cfg.Home)))
	}
	if len(olds) > 0 {
		parts = append(parts, oldViews(olds, yardrAt(cfg.Home)))
	}
	if len(deads) > 0 {
		out.Verdict = checkFail
		parts = append(parts, deadViews(deads))
	}
	out.Detail = strings.Join(parts, "; ")
	return out
}

// viewBuild is a recorded web view that answers /health on another build
// than the yardr that was asked.
type viewBuild struct {
	URL   string
	Build string // raw revision, as /health answered it
}

// answeredViews is the views that answer /health on this build, in words.
func answeredViews(n int, build string) string {
	on := " on " + version.Show(build)
	if n == 1 {
		return "1 answers" + on
	}
	return fmt.Sprintf("%d answer%s", n, on)
}

// staleViews is the views that answer /health on another build where the
// yard keeps its view as a login service, in words: they fail the part,
// since the upgrade restarts that service.
func staleViews(views []viewBuild, this string) string {
	builds := viewBuilds(views)
	want := ", not this build (" + version.Show(this) + ")"
	if len(views) == 1 {
		return "1 answers on " + builds + want + " (" + views[0].URL + ")"
	}
	urls := make([]string, len(views))
	for i, v := range views {
		urls[i] = v.URL
	}
	return fmt.Sprintf("%d answer on %s%s (%s)", len(views), builds, want, strings.Join(urls, ", "))
}

// handViews is the views that answer /health on another build where no
// login service keeps one: started by hand, which the upgrade leaves
// alone, with the restart named so the operator sees it. They fail
// nothing.
func handViews(views []viewBuild, yardr string) string {
	restart := fmt.Sprintf("%s web stop && %s web start", yardr, yardr)
	builds := viewBuilds(views)
	if len(views) == 1 {
		return "view on " + builds + ", started by hand: " + restart
	}
	return fmt.Sprintf("%d views on %s, started by hand: %s", len(views), builds, restart)
}

// viewBuilds are the distinct builds the views answer on, for people.
func viewBuilds(views []viewBuild) string {
	var builds []string
	seen := map[string]bool{}
	for _, v := range views {
		if !seen[v.Build] {
			seen[v.Build] = true
			builds = append(builds, version.Show(v.Build))
		}
	}
	return strings.Join(builds, ", ")
}

// oldViews is the views from before GET /health, in words: skipped, with
// the restart named.
func oldViews(urls []string, yardr string) string {
	restart := fmt.Sprintf("restart it (%s web stop, then %s web start)", yardr, yardr)
	if len(urls) == 1 {
		return "1 old: " + restart + " (" + urls[0] + ")"
	}
	return fmt.Sprintf("%d old: %s (%s)", len(urls), strings.Replace(restart, "restart it", "restart them", 1), strings.Join(urls, ", "))
}

// deadViews is the views that answer nothing, in words: they fail the part.
func deadViews(urls []string) string {
	if len(urls) == 1 {
		return "1 dead: answers nothing (" + urls[0] + ")"
	}
	return fmt.Sprintf("%d dead: answer nothing (%s)", len(urls), strings.Join(urls, ", "))
}

// probeView is what one recorded web view comes to: answers when GET
// /health says anything but 404, with the build it answers on; old when
// /health 404s and / answers; dead when nothing answers. The build is ""
// when /health answers what carries none.
func probeView(addr string) (state, build string) {
	base := "http://" + dialAddr(addr)
	if code, body, err := getHealth(base + "/health"); err == nil {
		if code != http.StatusNotFound {
			return viewAnswers, viewBuildOf(body)
		}
		if _, err := getStatus(base + "/"); err == nil {
			return viewOld, ""
		}
	}
	return viewDead, ""
}

// viewBuildOf is the build a /health body answers on: its view.build, ""
// when the body carries none.
func viewBuildOf(body []byte) string {
	var health struct {
		View struct {
			Build string `json:"build"`
		} `json:"view"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return ""
	}
	return health.View.Build
}

// getHealth is the status and body GET url answers with, the body capped:
// /health is small, and a view that answers megabytes is not one.
func getHealth(url string) (int, []byte, error) {
	resp, err := upgradeHTTP.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// getStatus is the status GET url answers with.
func getStatus(url string) (int, error) {
	code, _, err := getHealth(url)
	return code, err
}

// dialAddr is addr as the check reaches it: a view that listens on every
// interface is asked on loopback.
func dialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// print is the report for a person: one line per check.
func (r *upgradeCheckReport) print(w io.Writer) {
	fmt.Fprintf(w, "%-9s  %s: %s\n", "server", r.Server.Verdict, r.Server.Detail)
	fmt.Fprintf(w, "%-9s  %s: %s\n", "daemon", r.Daemon.Verdict, r.Daemon.Detail)
	fmt.Fprintf(w, "%-9s  %s: %s\n", "views", r.Views.Verdict, r.Views.Detail)
}
