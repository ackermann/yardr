package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/version"
)

// upgradeYard is a home with a store behind it, whose server answers (or
// not) as stubbed per call. The app's store is the test's, so the check
// reads daemon events without a socket.
func upgradeYard(t *testing.T) (string, store.API, func() *app) {
	t.Helper()
	home := newHome(t)
	st := storetest.New(t)
	return home, st, func() *app { return &app{st: st, actor: "me"} }
}

// stubServer has upgradeDescribe answer (or not) as a server of rev would.
func stubServer(t *testing.T, rev string, err error) {
	t.Helper()
	stub(t, &upgradeDescribe, func(context.Context, *config.Config) (server.Info, error) {
		if err != nil {
			return server.Info{}, err
		}
		return server.Info{PID: 4711, Revision: rev, Since: time.Date(2026, 10, 14, 9, 2, 11, 0, time.UTC)}, nil
	})
}

// logDaemonStart records a daemon start of build on the store.
func logDaemonStart(t *testing.T, st store.API, kind, build string) {
	t.Helper()
	if err := st.LogDispatch(context.Background(), "", kind, "me", map[string]any{"to": build}); err != nil {
		t.Fatal(err)
	}
}

// stampPass writes a pass stamp of build at at to the home.
func stampPass(t *testing.T, home, build string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.WritePassStamp(home, config.PassStamp{Build: build, At: at}); err != nil {
		t.Fatal(err)
	}
}

// serveView starts a test web view answering /health with health and / with
// root, and announces it on the home as web serve does. /health answers on
// this build, as a view the test binary started would.
func serveView(t *testing.T, home string, health, root int) string {
	t.Helper()
	return serveViewOn(t, home, version.Current(), health, root)
}

// serveViewOn starts a test web view answering /health on build, and
// announces it on the home as web serve does.
func serveViewOn(t *testing.T, home, build string, health, root int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(health)
			fmt.Fprintf(w, `{"view":{"build":%q}}`, build)
		default:
			w.WriteHeader(root)
			fmt.Fprintln(w, "home")
		}
	}))
	t.Cleanup(srv.Close)
	announce(t, home, srv.Listener.Addr().String(), false, "")
	return "http://" + srv.Listener.Addr().String() + "/"
}

// upgradeJSON runs yardr upgrade --check --json with more flags if given.
func upgradeJSON(t *testing.T, a *app, flags ...string) (upgradeCheckReport, error) {
	t.Helper()
	out, err := run(t, a.rootCmd(), append([]string{"upgrade", "--check", "--json"}, flags...)...)
	var rep upgradeCheckReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("upgrade --check --json: %v\n%s", jerr, out)
	}
	return rep, err
}

// The yard runs this build: one line per check in the shape of section 3.7,
// exit 0. The test binary carries no revision, so "" is this build.
func TestUpgradeCheckAllOK(t *testing.T) {
	home, st, a := upgradeYard(t)
	stubServer(t, "", nil)
	holdDaemon(t, home)
	logDaemonStart(t, st, "daemon_started", version.Show(version.Current()))
	stampPass(t, home, version.Show(version.Current()), time.Now())
	serveView(t, home, http.StatusOK, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"server     ok: answers on unknown, this build\n",
		"daemon     ok: holds the lock on unknown, this build, pass ",
		" ago\n",
		"views      ok: 1 answers on unknown\n")
	if rep, err := upgradeJSON(t, a()); err != nil || !rep.OK ||
		rep.Server.Verdict != checkOK || rep.Daemon.Verdict != checkOK || rep.Views.Verdict != checkOK {
		t.Fatalf("json: %+v %v", rep, err)
	}
}

// Every part uses the documented lower-case JSON fields, including detail.
func TestUpgradeCheckJSONShape(t *testing.T) {
	_, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	out, err := run(t, a().rootCmd(), "upgrade", "--check", "--json")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	for _, part := range []string{"server", "daemon", "views"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(report[part], &fields); err != nil {
			t.Fatalf("%s: %v", part, err)
		}
		if _, ok := fields["detail"]; !ok {
			t.Errorf("%s is missing lower-case detail: %s", part, report[part])
		}
		if _, ok := fields["Detail"]; ok {
			t.Errorf("%s has undocumented capitalized Detail: %s", part, report[part])
		}
	}
}

// An old caller checks a new server: the server on another build fails with
// a line naming it, exit 1. A missing daemon and view skip instead.
func TestUpgradeCheckServerOtherBuild(t *testing.T) {
	_, _, a := upgradeYard(t)
	stubServer(t, "ffffffffffff", nil)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out,
		"server     fail: answers on ffffffffffff, not this build (unknown)\n",
		"daemon     skip: no daemon runs\n",
		"views      skip: no web view runs\n")
	if rep, _ := upgradeJSON(t, a()); rep.OK || rep.Server.Verdict != checkFail ||
		rep.Daemon.Verdict != checkSkip || rep.Views.Verdict != checkSkip {
		t.Fatalf("json: %+v", rep)
	}
}

// A server that does not answer makes the check fail with why, and the
// check starts none: no socket appears, exit 1.
func TestUpgradeCheckServerDown(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", fmt.Errorf("dial: %w", syscall.ENOENT))

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "server     fail: the server is not running (start it: "+here("yardr server start")+")\n")
	if _, serr := os.Stat((&config.Config{Home: home}).Socket()); serr == nil {
		t.Fatal("the check started a server")
	}

	stubServer(t, "", errors.New(`Get "http://yard/ping": dial unix /y/yard.sock: connect: connection refused`))
	out, err = run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "server     fail: the server cannot be reached: dial unix /y/yard.sock: connect: connection refused\n")
}

// On a home with no yard the check says so as any command does: exit 2,
// naming init, creating nothing, starting no server.
func TestUpgradeCheckNoYard(t *testing.T) {
	home := newHome(t)
	errs := captureStderr(t)
	var code int
	out := capture(t, func() { code = execute(&app{}, []string{"upgrade", "--check"}) })
	if code != 2 || out != "" {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantText(t, errs.String(), "no yard in "+home+" yet", here("yardr init --harness <kind>"))
	if _, err := os.Stat(home); err == nil {
		t.Fatal("the check created the home directory")
	}
}

// A daemon on another build fails with a line naming it.
func TestUpgradeCheckDaemonOtherBuild(t *testing.T) {
	home, st, a := upgradeYard(t)
	stubServer(t, "", nil)
	holdDaemon(t, home)
	logDaemonStart(t, st, "daemon_started", "bbbbbbbbbbbb")

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "daemon     fail: holds the lock on bbbbbbbbbbbb, not this build (unknown)\n")
}

// The daemon's build is the to of the latest daemon_replaced or
// daemon_started event, whichever came last.
func TestUpgradeCheckDaemonBuildIsLatestStart(t *testing.T) {
	home, st, a := upgradeYard(t)
	stubServer(t, "", nil)
	holdDaemon(t, home)
	this := version.Show(version.Current())
	logDaemonStart(t, st, "daemon_started", this)
	logDaemonStart(t, st, "daemon_replaced", "bbbbbbbbbbbb")
	stampPass(t, home, this, time.Now())

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "daemon     fail: holds the lock on bbbbbbbbbbbb, not this build ("+this+")\n")
}

// A daemon whose build is in no event fails naming the restart, not a guess.
func TestUpgradeCheckDaemonNoBuild(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	holdDaemon(t, home)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "daemon     fail: holds the lock, but no start names the build it runs; restart it ("+
		here("yardr daemon stop")+", then "+here("yardr daemon start")+")\n")
}

// A daemon that holds its lock with no stamp on this build since --since
// fails as wedged: no stamp, a stamp on another build, a stamp too old.
func TestUpgradeCheckDaemonWedged(t *testing.T) {
	this := version.Show(version.Current())
	for _, tc := range []struct {
		name  string
		stamp func(t *testing.T, home string)
		flags []string
		want  string
	}{
		{"no stamp", func(t *testing.T, _ string) {}, nil,
			"fail: holds the lock on " + this + ", this build, but never stamped a pass on it (wedged?)"},
		{"stamp on another build", func(t *testing.T, home string) {
			stampPass(t, home, "bbbbbbbbbbbb", time.Now())
		}, nil,
			"fail: holds the lock on " + this + ", this build, but its last pass ran on bbbbbbbbbbbb, "},
		{"stamp too old", func(t *testing.T, home string) {
			stampPass(t, home, this, time.Now().Add(-time.Hour))
		}, nil,
			"fail: holds the lock on " + this + ", this build, but its last pass on this build ended 60 min ago, older than --since 10m (wedged?)"},
		{"stamp too old for --since", func(t *testing.T, home string) {
			stampPass(t, home, this, time.Now().Add(-20*time.Minute))
		}, []string{"--since", "10m"},
			"fail: holds the lock on " + this + ", this build, but its last pass on this build ended 20 min ago, older than --since 10m (wedged?)"},
		{"stamp fresh for --since", func(t *testing.T, home string) {
			stampPass(t, home, this, time.Now().Add(-20*time.Minute))
		}, []string{"--since", "30m"},
			"ok: holds the lock on " + this + ", this build, pass 20 min ago"},
		{"since as the install's start", func(t *testing.T, home string) {
			stampPass(t, home, this, time.Now().Add(-time.Minute))
		}, []string{"--since", time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)},
			"ok: holds the lock on " + this + ", this build, pass "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, st, a := upgradeYard(t)
			stubServer(t, "", nil)
			holdDaemon(t, home)
			logDaemonStart(t, st, "daemon_started", this)
			tc.stamp(t, home)
			out, err := run(t, a().rootCmd(), append([]string{"upgrade", "--check"}, tc.flags...)...)
			if strings.Contains(tc.want, "fail:") && !errors.Is(err, errCheckFailed) {
				t.Fatalf("err = %v, want check failed\n%s", err, out)
			}
			if strings.Contains(tc.want, "ok:") && err != nil {
				t.Fatalf("err = %v\n%s", err, out)
			}
			wantText(t, out, "daemon     "+tc.want)
		})
	}
}

// --since takes a duration or the install's start as RFC3339, and nothing else.
func TestUpgradeCheckSinceFlag(t *testing.T) {
	_, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	for _, flag := range []string{"yesterday", "-5m", "2026-10-14"} {
		if out, err := run(t, a().rootCmd(), "upgrade", "--check", "--since", flag); err == nil ||
			!strings.Contains(err.Error(), "--since \""+flag+"\"") {
			t.Fatalf("--since %s: err = %v\n%s", flag, err, out)
		}
	}
}

func TestUpgradeSincePreservesInstallStartPrecision(t *testing.T) {
	installStart := time.Date(2026, 10, 14, 9, 2, 11, 875000000, time.UTC)
	got, err := upgradeSince(installStart.Format(time.RFC3339Nano))
	if err != nil || !got.Equal(installStart) {
		t.Fatalf("since = %v, %v; want exact install start %v", got, err, installStart)
	}
}

// A daemon that flaps during the run fails: held, then not, or the reverse.
func TestUpgradeCheckDaemonFlap(t *testing.T) {
	_, st, a := upgradeYard(t)
	stubServer(t, "", nil)
	logDaemonStart(t, st, "daemon_started", version.Show(version.Current()))
	script := []bool{true, false}
	stub(t, &upgradeDaemonRunning, func(string) bool {
		held := script[0]
		if len(script) > 1 {
			script = script[1:]
		}
		return held
	})
	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "daemon     fail: held the lock, then not: it flapped during the check")

	script = []bool{false, true}
	out, err = run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "daemon     fail: started during the check; run it again\n")
}

// A view on a build from before /health — 404 on /health, answers / —
// skips with the old: restart line, and fails nothing.
func TestUpgradeCheckOldView(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	url := serveView(t, home, http.StatusNotFound, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "views      ok: 1 old: restart it ("+here("yardr web stop")+", then "+here("yardr web start")+") ("+url+")\n")
	if rep, _ := upgradeJSON(t, a()); !rep.OK || rep.Views.Verdict != checkOK ||
		len(rep.Views.Views) != 1 || rep.Views.Views[0].State != viewOld {
		t.Fatalf("json: %+v", rep)
	}
}

// A view that answers /health answers, whatever the status: a 500 is the
// yard failing, which the server and daemon parts say, not the view's.
func TestUpgradeCheckViewAnswersOn500(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	serveView(t, home, http.StatusInternalServerError, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "views      ok: 1 answers on unknown\n")
}

// A view that answers nothing fails with a line naming it.
func TestUpgradeCheckDeadView(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing answers there now
	announce(t, home, addr, false, "")

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "views      fail: 1 dead: answers nothing (http://"+addr+"/)\n")
}

// Answering and old views share one line, in the shape of section 3.7.
func TestUpgradeCheckMixedViews(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	old := serveView(t, home, http.StatusNotFound, http.StatusOK)
	serveView(t, home, http.StatusOK, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "views      ok: 1 answers on unknown; 1 old: restart it (")
	if !strings.Contains(out, old+")") {
		t.Fatalf("the old view's URL is missing:\n%s", out)
	}
}

// A view answering /health on another build fails where the yard keeps its
// view as a login service: the upgrade restarts that service, so an old
// build after the restart means the restart failed. The line names the build.
func TestUpgradeCheckViewOtherBuildFails(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	stub(t, &webService, func(*config.Config) bool { return true })
	url := serveViewOn(t, home, "bbbbbbbbbbbb", http.StatusOK, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want check failed\n%s", err, out)
	}
	wantText(t, out, "views      fail: 1 answers on bbbbbbbbbbbb, not this build (unknown) ("+url+")\n")
	if rep, _ := upgradeJSON(t, a()); rep.OK || rep.Views.Verdict != checkFail ||
		len(rep.Views.Views) != 1 || rep.Views.Views[0].Build != "bbbbbbbbbbbb" {
		t.Fatalf("json: %+v", rep)
	}
}

// A view answering /health on another build where no login service keeps
// one was started by hand, which the upgrade leaves alone: the line names
// the build and the restart, and fails nothing.
func TestUpgradeCheckViewOtherBuildByHand(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	serveViewOn(t, home, "bbbbbbbbbbbb", http.StatusOK, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "views      ok: view on bbbbbbbbbbbb, started by hand: "+
		here("yardr web stop")+" && "+here("yardr web start")+"\n")
	if rep, _ := upgradeJSON(t, a()); !rep.OK || rep.Views.Verdict != checkOK ||
		len(rep.Views.Views) != 1 || rep.Views.Views[0].Build != "bbbbbbbbbbbb" {
		t.Fatalf("json: %+v", rep)
	}
}

// A view answering /health on this build where the yard keeps its view as
// a login service passes, and the line names the build: the service
// restarted (stubbed) and the new build answers.
func TestUpgradeCheckViewServiceCurrent(t *testing.T) {
	home, _, a := upgradeYard(t)
	stubServer(t, "", nil)
	stub(t, &webService, func(*config.Config) bool { return true })
	serveView(t, home, http.StatusOK, http.StatusOK)

	out, err := run(t, a().rootCmd(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "views      ok: 1 answers on unknown\n")
}

// dialAddr asks a view that listens on every interface on loopback.
func TestDialAddr(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8791": "127.0.0.1:8791",
		"0.0.0.0:8791":   "127.0.0.1:8791",
		"[::]:8791":      "127.0.0.1:8791",
		"localhost:8791": "localhost:8791",
		"no-port":        "no-port",
	} {
		if got := dialAddr(addr); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}

// Without --check the command installs: on a home with no yard it says so,
// and on a yard without the setting it refuses and installs nothing.
func TestUpgradeWithoutCheck(t *testing.T) {
	newHome(t)
	out, err := run(t, (&app{actor: "me"}).rootCmd(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "no yard in") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	_, _, a := upgradeYard(t)
	out, err = run(t, a().rootCmd(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "upgrade_from is not set") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// --from names the depot for one run, on a yard with upgrade_from unset
// and no operator notify source: an unknown depot fails with the "not in
// the yard" line, and a depot with no checkout with the "no checkout"
// line. A value the setting would refuse is refused the same way, and an
// empty one reads as unset.
func TestUpgradeFromFlag(t *testing.T) {
	ctx := context.Background()
	_, st, a := upgradeYard(t)
	if err := st.SetSetting(ctx, store.OperatorSetting, "probeuser", "probeuser"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "upgrade", "--from", "missing")
	if err == nil || !strings.Contains(err.Error(), "not in the yard") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("unknown depot: err = %v\n%s", err, out)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Path: gone}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a().rootCmd(), "upgrade", "--from", "yardr")
	if err == nil || !strings.Contains(err.Error(), "no checkout") || !strings.Contains(err.Error(), gone) {
		t.Fatalf("no checkout: err = %v\n%s", err, out)
	}
	out, err = run(t, a().rootCmd(), "upgrade", "--from", "Bad Name")
	if err == nil || !strings.Contains(err.Error(), `depot name "Bad Name"`) {
		t.Fatalf("bad name: err = %v\n%s", err, out)
	}
	out, err = run(t, a().rootCmd(), "upgrade", "--from", "")
	if err == nil || !strings.Contains(err.Error(), "upgrade_from is not set") {
		t.Fatalf("empty flag: err = %v\n%s", err, out)
	}
}

// With upgrade_from set to one depot and --from naming another, the run
// uses the flag's: the setting's depot has no checkout, so a run on the
// setting would fail "no checkout", while the flag's unknown depot fails
// "not in the yard".
func TestUpgradeFromFlagBeatsSetting(t *testing.T) {
	ctx := context.Background()
	_, st, a := upgradeYard(t)
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	decl, err := os.ReadFile("testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", string(decl), "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "jo", map[string]string{store.UserNotify: "pushover"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "one", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "one", Path: filepath.Join(t.TempDir(), "gone")}, "jo"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "upgrade", "--from", "missing")
	if err == nil || !strings.Contains(err.Error(), "not in the yard") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}
