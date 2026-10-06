package cli

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// statusYard is a home with a yard on it, as init leaves one, whose server
// answers as pid 4711 of build rev since a fixed time. It returns the home
// and an app on it per call.
func statusYard(t *testing.T, rev string) (string, store.API, func() *app) {
	t.Helper()
	home := newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return true })
	stub(t, &serverInfo, func(context.Context, *config.Config) (server.Info, bool) {
		return server.Info{PID: 4711, Revision: rev, Since: time.Date(2026, 10, 5, 9, 12, 0, 0, time.Local)}, true
	})
	stub(t, &machineAddr, func() string { return "192.168.7.7" })
	return home, st, a
}

// statusJSON runs yardr status --json.
func statusJSON(t *testing.T, a *app) statusReport {
	t.Helper()
	out, err := run(t, a.rootCmd(), "status", "--json")
	var rep statusReport
	if jerr := json.Unmarshal([]byte(out), &rep); err != nil || jerr != nil {
		t.Fatalf("status --json: %v %v\n%s", err, jerr, out)
	}
	return rep
}

// announce records a web view of the home as web serve does, for the test.
func announce(t *testing.T, home, addr string, unsafe bool) {
	t.Helper()
	clear, err := announceWeb(&config.Config{Home: home}, addr, unsafe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clear)
}

// A yard with nothing but its server: status says what is there and what
// does not run, has no line for hooks or peers, exits 0, and names doctor
// for the faults doctor reports (no daemon, no yardmaster) without saying them.
func TestStatusServerOnly(t *testing.T) {
	home, _, a := statusYard(t, "abcdef123456")
	out, err := run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"yard       "+home+"  name y  operator Jo\n",
		"server     running  pid 4711  build abcdef123456  since 10-05 09:12\n",
		// The test binary carries no revision: another build than the server's.
		"           this yardr is build unknown\n",
		"daemon     not running\n",
		"           login service: not installed\n",
		"residents  yardmaster  not running\n",
		"web view   "+here("not running: yardr web serve")+"\n",
		"work       0 sessions running; 0 held\n",
		"\n"+here("yardr doctor reports 2 faults, and says what they are.")+"\n")
	for _, not := range []string{"hooks", "peers", "FAULT", "no bead is dispatched"} {
		if strings.Contains(out, not) {
			t.Errorf("status says %q:\n%s", not, out)
		}
	}
	if rep := statusJSON(t, a()); rep.Faults == nil || *rep.Faults != 2 || rep.Hooks != nil || rep.Peers != nil || rep.Daemon.Running ||
		!rep.Server.Running || !rep.Server.Differs || rep.Server.PID != 4711 {
		t.Fatalf("json: %+v", rep)
	}
}

// A server of the build that was asked is not said to differ.
func TestStatusSameBuild(t *testing.T) {
	_, _, a := statusYard(t, "")
	out, _ := run(t, a().rootCmd(), "status")
	wantText(t, out, "server     running  pid 4711  build unknown  since 10-05 09:12\n")
	if strings.Contains(out, "this yardr is build") {
		t.Errorf("the same build is said to differ:\n%s", out)
	}
	if rep := statusJSON(t, a()); rep.Server.Differs {
		t.Fatalf("json: %+v", rep.Server)
	}
}

// status says what runs and starts nothing (yardr-t68z): on a yard whose
// server is stopped it leaves it stopped, says how it is started, lists
// what the home and the machine say without the store (the daemon, the web
// views, the login services) and has one line for what only the server
// knows. The yard is there: exit 0.
func TestStatusLeavesAStoppedServerStopped(t *testing.T) {
	home := newHome(t)
	cfg := &config.Config{Home: home}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// The store of a yard, which nobody serves.
	if err := os.WriteFile(cfg.DBPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stub(t, &connect, func(context.Context, *config.Config) (store.API, error) {
		t.Error("status connected to the store, which starts a server")
		return nil, errors.New("no server in this test")
	})
	stub(t, &machineAddr, func() string { return "192.168.7.7" })
	holdDaemon(t, home)
	announce(t, home, "127.0.0.1:9000", false)
	announce(t, home, "0.0.0.0:9001", true)
	before, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}

	var code int
	out := capture(t, func() { code = execute(&app{}, []string{"status"}) })
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	me := strconv.Itoa(os.Getpid())
	wantText(t, out,
		"yard       "+home+"\n",
		"server     not running: any yardr command that needs the yard starts it ("+here("yardr ready")+"), or "+here("yardr server start")+"\n",
		"daemon     running (pid "+me+")\n",
		"           login service: not installed\n",
		"web view   http://127.0.0.1:9000/    pid "+me+"  build unknown  this machine only\n",
		"           http://192.168.7.7:9001/  pid "+me+"  build unknown  open to the network (--unsafe), no login\n",
		"\nresidents, hooks, peers and work: not known while the server is not running\n")
	for _, not := range []string{"name ", "operator", "doctor", "started", "\nresidents ", "\nwork "} {
		if strings.Contains(out, not) {
			t.Errorf("status says %q:\n%s", not, out)
		}
	}

	var raw map[string]json.RawMessage
	js := capture(t, func() { code = execute(&app{}, []string{"status", "--json"}) })
	if err := json.Unmarshal([]byte(js), &raw); err != nil || code != 0 {
		t.Fatalf("exit %d, %v:\n%s", code, err, js)
	}
	for _, key := range []string{"name", "operator", "residents", "hooks", "peers", "work", "faults"} {
		if _, ok := raw[key]; ok {
			t.Errorf("json has %q, which is not known without the server:\n%s", key, js)
		}
	}
	var rep statusReport
	if err := json.Unmarshal([]byte(js), &rep); err != nil || rep.Server.Running || rep.Server.PID != 0 || !rep.Daemon.Running || len(rep.Web.Views) != 2 {
		t.Fatalf("json: %v %+v", err, rep)
	}

	// No server afterwards: no socket, no pidfile, no log, nothing new in
	// the home at all.
	for _, p := range []string{cfg.Socket(), cfg.PIDFile(), cfg.LogFile()} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("status left %s: a server was started", p)
		}
	}
	after, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the home changed: %v, was %v", after, before)
	}
	if server.Running(context.Background(), cfg) {
		t.Error("a server answers on the home")
	}
}

// A web view runs the build it was started on until it is started again:
// it records that build, and status lists it and says when this yardr is
// another.
func TestStatusWebViewBuild(t *testing.T) {
	home, _, a := statusYard(t, "")
	announce(t, home, "127.0.0.1:9000", false)
	out, _ := run(t, a().rootCmd(), "status")
	wantText(t, out, "web view   http://127.0.0.1:9000/  pid "+strconv.Itoa(os.Getpid())+"  build unknown  this machine only\n")
	if strings.Contains(out, "this yardr is build") {
		t.Errorf("a view of this build is said to differ:\n%s", out)
	}

	stub(t, &viewRevision, func() string { return "0123456789ab" })
	announce(t, home, "127.0.0.1:9001", false)
	out, _ = run(t, a().rootCmd(), "status")
	wantText(t, out,
		"           http://127.0.0.1:9001/  pid "+strconv.Itoa(os.Getpid())+"  build 0123456789ab  this machine only\n",
		"           this yardr is build unknown: a view runs the build it was started on until it is started again\n",
		"           login service: not installed\n")
	v := statusJSON(t, a()).Web.Views
	if len(v) != 2 || v[0].Differs || v[0].Build != "unknown" || !v[1].Differs || v[1].Revision != "0123456789ab" || v[1].Build != "0123456789ab" {
		t.Fatalf("views: %+v", v)
	}
}

// The whole of a yard that runs: the daemon with its pid, each resident
// with its state and agent, a view on loopback and one open to the network,
// the hooks with where they are posted to, the peers, and the work counted.
func TestStatusRunningYard(t *testing.T) {
	home, st, a := statusYard(t, "abcdef123456")
	ctx := context.Background()
	holdDaemon(t, home)
	if err := st.ResidentStarted(ctx, "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	if err := st.ResidentStatus(ctx, "yardmaster", "idle"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutResident(ctx, store.Resident{Name: "refinery", Enabled: true}, "me"); err != nil {
		t.Fatal(err)
	}
	announce(t, home, "127.0.0.1:9000", false)
	announce(t, home, "0.0.0.0:9001", true)
	// --unsafe on a loopback address opens nothing to the network.
	announce(t, home, "[::1]:9002", true)

	if _, err := st.PutHook(ctx, store.Hook{Name: "tickets", Source: "linear", SecretFile: "/s"}, "me"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home}
	if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hooks.WriteListener(cfg, hooks.Listener{Addr: "127.0.0.1:9100", From: hooks.FromSetting}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Peer{{Name: "north", URL: "https://north.example"}, {Name: "laptop"}} {
		key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
		if err != nil {
			t.Fatal(err)
		}
		p.Key = yardkey.Public(key)
		if _, err := st.AddPeer(ctx, p, "me"); err != nil {
			t.Fatal(err)
		}
	}

	rig := gitRig(t, st)
	commitRig(t, rig)
	// Two beads an agent works, one a script works, three that two manual
	// groups hold for a person, and one held.
	claimed := workYard(t, st, rig.Name, []store.Group{
		{Name: "web/builders", Runner: "herdr", Limit: 2},
		{Name: "web/refinery", Runner: "exec", Limit: 1},
		{Name: "backlog", Runner: "manual", Limit: 5},
		{Name: "decisions", Runner: "manual", Limit: 5},
	}, "web/builders", "web/builders", "web/refinery", "backlog", "backlog", "decisions", "")
	if _, err := st.SetHold(ctx, claimed[6], true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	me := strconv.Itoa(os.Getpid())
	wantText(t, out,
		"daemon     running (pid "+me+")\n",
		"residents  refinery    not running\n",
		"           yardmaster  idle         herdr agent y-yardmaster\n",
		"web view   http://127.0.0.1:9000/    pid "+me+"  build unknown  this machine only\n",
		"           http://192.168.7.7:9001/  pid "+me+"  build unknown  open to the network (--unsafe), no login\n",
		"           http://[::1]:9002/        pid "+me+"  build unknown  this machine only\n",
		"           login service: not installed\n",
		"hooks      the server takes webhooks on 127.0.0.1:9100\n",
		"           tickets  linear  http://127.0.0.1:9100/hooks/tickets\n",
		"peers      laptop  it calls this yard\n",
		"           north   https://north.example\n",
		"work       3 sessions running (web/builders 2, web/refinery 1); 3 beads wait for a person (backlog 2, decisions 1); 1 held\n")
	if strings.Contains(out, "doctor") {
		t.Errorf("doctor is named on a yard it reports no fault on:\n%s", out)
	}

	rep := statusJSON(t, a())
	if rep.Name != "y" || rep.Operator != "Jo" || !rep.Daemon.Running || rep.Daemon.PID != os.Getpid() || rep.Faults == nil || *rep.Faults != 0 {
		t.Fatalf("json: %+v", rep)
	}
	if len(rep.Residents) != 2 || rep.Residents[1] != (residentStatus{"yardmaster", "idle", "y-yardmaster"}) || rep.Residents[0].Agent != "" {
		t.Fatalf("residents: %+v", rep.Residents)
	}
	if v := rep.Web.Views; len(v) != 3 || v[0].Open || v[0].URL != "http://127.0.0.1:9000/" || !v[1].Open || !v[1].Unsafe ||
		v[1].Addr != "0.0.0.0:9001" || v[1].PID != os.Getpid() || v[2].Open {
		t.Fatalf("views: %+v", v)
	}
	if h := rep.Hooks; h == nil || !h.Listening || len(h.Hooks) != 1 || h.Hooks[0].URL != "http://127.0.0.1:9100/hooks/tickets" {
		t.Fatalf("hooks: %+v", h)
	}
	if w := rep.Work; w == nil || w.Held != 1 || len(rep.Peers) != 2 ||
		!maps.Equal(w.Sessions, map[string]int{"web/builders": 2, "web/refinery": 1}) ||
		!maps.Equal(w.Waiting, map[string]int{"backlog": 2, "decisions": 1}) {
		t.Fatalf("work: %+v, peers: %+v", w, rep.Peers)
	}
}

// workYard puts the groups and a bead per name in claims, claimed by the
// group of that name ("" for none), and returns the beads.
func workYard(t *testing.T, st store.API, rig string, groups []store.Group, claims ...string) []string {
	t.Helper()
	ctx := context.Background()
	for _, g := range groups {
		if err := st.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	var beads []string
	for i, group := range claims {
		b, err := st.CreateBead(ctx, store.NewBead{Title: "bead " + strconv.Itoa(i), Rig: rig}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads = append(beads, b.ID)
		if group == "" {
			continue
		}
		if _, err := st.Claim(ctx, b.ID, group, b.Stage, "yard"); err != nil {
			t.Fatal(err)
		}
	}
	return beads
}

// A manual group's claim is a person's turn and no session running
// (yardr-7r3h: a yard with 2 agents and 35 beads in backlog said "37
// sessions running"). A yard no person waits on has no word about it, and
// one bead waits where two wait.
func TestStatusWorkLine(t *testing.T) {
	work := func(t *testing.T, groups []store.Group, claims ...string) (string, *workCounts) {
		t.Helper()
		_, st, a := statusYard(t, "abcdef123456")
		rig := gitRig(t, st)
		commitRig(t, rig)
		workYard(t, st, rig.Name, groups, claims...)
		out, err := run(t, a().rootCmd(), "status")
		if err != nil {
			t.Fatalf("err = %v\n%s", err, out)
		}
		i := strings.Index(out, "work  ")
		if i < 0 {
			t.Fatalf("no work line:\n%s", out)
		}
		line, _, _ := strings.Cut(out[i:], "\n")
		return line, statusJSON(t, a()).Work
	}
	builders := store.Group{Name: "builders", Runner: "herdr", Limit: 2}
	backlog := store.Group{Name: "backlog", Runner: "manual", Limit: 5}

	t.Run("no manual claim", func(t *testing.T) {
		line, w := work(t, []store.Group{builders, backlog}, "builders", "")
		if want := "work       1 session running (builders 1); 0 held"; line != want {
			t.Errorf("line = %q, want %q", line, want)
		}
		if w == nil || !maps.Equal(w.Sessions, map[string]int{"builders": 1}) || w.Waiting == nil || len(w.Waiting) != 0 {
			t.Errorf("json: %+v", w)
		}
	})
	t.Run("one manual claim", func(t *testing.T) {
		line, w := work(t, []store.Group{builders, backlog}, "backlog")
		if want := "work       0 sessions running; 1 bead waits for a person (backlog 1); 0 held"; line != want {
			t.Errorf("line = %q, want %q", line, want)
		}
		if w == nil || len(w.Sessions) != 0 || !maps.Equal(w.Waiting, map[string]int{"backlog": 1}) {
			t.Errorf("json: %+v", w)
		}
	})
}

// A view that ended without clearing its record (kill -9, a reboot) holds
// it no longer, and is not listed: not by its pid, which is alive here.
// The next view to start removes what it left.
func TestStatusStaleWebView(t *testing.T) {
	home, _, a := statusYard(t, "abcdef123456")
	cfg := &config.Config{Home: home}
	clear, err := announceWeb(cfg, "127.0.0.1:9000", false)
	if err != nil {
		t.Fatal(err)
	}
	if v := webViews(cfg); len(v) != 1 {
		t.Fatalf("the running view: %+v", v)
	}
	records, _ := filepath.Glob(filepath.Join(webViewsDir(cfg), "view-*.json"))
	if len(records) != 1 {
		t.Fatalf("records: %v", records)
	}
	body, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"revision"`) {
		t.Fatalf("the record does not say which build the view runs: %s", body)
	}
	if strings.Contains(string(body), "url") || strings.Contains(string(body), "open") || strings.Contains(string(body), "differs") {
		t.Fatalf("the record holds what is worked out when it is read: %s", body)
	}
	// It ends cleanly: the record is gone.
	clear()
	if left, _ := os.ReadDir(webViewsDir(cfg)); len(left) != 0 {
		t.Fatalf("left after a clean end: %v", left)
	}
	// It ends any other way: the record stays, held by nobody.
	stale := filepath.Join(webViewsDir(cfg), "view-gone.json")
	if err := os.WriteFile(stale, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "web view   "+here("not running: yardr web serve")+"\n")
	if strings.Contains(out, "9000") {
		t.Errorf("a view that has ended is listed:\n%s", out)
	}
	if rep := statusJSON(t, a()); len(rep.Web.Views) != 0 {
		t.Fatalf("json: %+v", rep.Web)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("status is read-only, and the record is gone: %v", err)
	}
	announce(t, home, "127.0.0.1:9003", false)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the next view left the stale record: %v", err)
	}
	if v := webViews(cfg); len(v) != 1 || v[0].Addr != "127.0.0.1:9003" {
		t.Fatalf("views: %+v", v)
	}
}

// On a home with no yard status says so as any command does without a
// terminal, in one too: exit 2, naming init, no onboarding and no store.
func TestStatusNoYard(t *testing.T) {
	home := newHome(t)
	for _, terminal := range []bool{false, true} {
		setInteractive(t, terminal)
		stubAsk(t, func(...huh.Field) error {
			t.Fatal("status started the onboarding")
			return nil
		})
		errs := captureStderr(t)
		var code int
		out := capture(t, func() { code = execute(&app{}, []string{"status"}) })
		if code != 2 || out != "" {
			t.Fatalf("terminal %v: exit %d:\n%s", terminal, code, out)
		}
		wantText(t, errs.String(), "no yard in "+home+" yet", here("yardr init --harness <kind>"))
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil {
		t.Fatal("status created a store")
	}
}

// A yard whose server cannot be reached is still a yard: status says so,
// with what it knows without the store, and exits 0.
func TestStatusUnreachableServer(t *testing.T) {
	home := newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	announce(t, home, "127.0.0.1:9000", false)
	// It answers on its socket, and nothing it is asked comes back.
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return true })
	out, err := run(t, (&app{st: brokenStore{storetest.New(t)}, actor: "me"}).rootCmd(), "status")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "yard       "+home+"\n", "server     cannot be reached: server gone\n", "daemon     not running\n",
		"web view   http://127.0.0.1:9000/  pid ", here("yardr doctor reports 1 fault, and says what it is."))
	if strings.Contains(out, "residents") || strings.Contains(out, "work ") {
		t.Errorf("nothing of the store is known:\n%s", out)
	}
}

// doctor's web view line says both things apart: whether a view runs now,
// and whether a login service keeps one running. A view open to the network
// is a note and no fault.
func TestDoctorWebView(t *testing.T) {
	home, st, a := statusYard(t, "abcdef123456")
	holdDaemon(t, home)
	if err := st.ResidentStarted(context.Background(), "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	stubServices(t, "darwin")
	web := func() (string, optionalPart) {
		t.Helper()
		out, _ := run(t, a().rootCmd(), "doctor")
		rep, _ := doctorJSON(t, a().st)
		for _, p := range rep.Optional {
			if p.Key == "web" {
				for _, f := range rep.Faults {
					if strings.Contains(f, "web") {
						t.Errorf("the web view is a fault: %s", f)
					}
				}
				return out, p
			}
		}
		t.Fatalf("no web part:\n%s", out)
		return "", optionalPart{}
	}
	me := strconv.Itoa(os.Getpid())

	// No view, no service.
	out, part := web()
	wantText(t, out, "not configured  web view: not running, and nothing keeps it running; "+here("yardr web serve shows the yard in a browser while it runs"),
		here("yardr web install keeps it running from login on (docs/web.md)"))
	if part.Configured || len(part.Views) != 0 || len(part.Notes) != 0 {
		t.Fatalf("part: %+v", part)
	}

	// A view started by hand, no service: what misled before (yardr-t68z),
	// when the line was about the service alone and read as "no view".
	clear, err := announceWeb(&config.Config{Home: home}, "127.0.0.1:9000", false)
	if err != nil {
		t.Fatal(err)
	}
	out, part = web()
	wantText(t, out, "not configured  web view: running at http://127.0.0.1:9000/ (pid "+me+", this machine only); nothing keeps it running: "+
		here("yardr web install keeps it running from login on (docs/web.md)"))
	if part.Configured || len(part.Views) != 1 || len(part.Notes) != 0 {
		t.Fatalf("part: %+v", part)
	}
	if strings.Contains(out, "note            web view") {
		t.Errorf("a view on loopback has a note:\n%s", out)
	}

	// And one open to the network: a note.
	announce(t, home, "0.0.0.0:9001", true)
	out, part = web()
	wantText(t, out, "http://192.168.7.7:9001/ (pid "+me+", open to the network (--unsafe), no login); nothing keeps it running",
		"note            web view: http://192.168.7.7:9001/ (pid "+me+") is open to the network, no login: it was started with --unsafe, "+
			"and anyone who can reach this machine on port 9001 can read the yard")
	if len(part.Views) != 2 || !part.Views[1].Open || len(part.Notes) != 1 {
		t.Fatalf("part: %+v", part)
	}
	if rep, err := doctorJSON(t, a().st); err != nil || len(rep.Faults) != 0 {
		t.Fatalf("a view open to the network is a fault: %v %v", err, rep.Faults)
	}

	// A service and views that run.
	if _, err := run(t, a().rootCmd(), "web", "install", "--addr", "127.0.0.1:9000", "--file-only"); err != nil {
		t.Fatal(err)
	}
	out, part = web()
	wantText(t, out, "configured      web view: kept running as a login service at http://127.0.0.1:9000/ (",
		".plist); running at http://127.0.0.1:9000/ (pid "+me+", this machine only), http://192.168.7.7:9001/")
	if !part.Configured || len(part.Views) != 2 {
		t.Fatalf("part: %+v", part)
	}

	// A service, and no view up.
	clear()
	for _, p := range mustGlob(t, filepath.Join(home, "webviews", "view-*.json")) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	out, part = web()
	wantText(t, out, "configured      web view: kept running as a login service at http://127.0.0.1:9000/ (", ".plist); not running now\n")
	if !part.Configured || len(part.Views) != 0 || len(part.Notes) != 0 {
		t.Fatalf("part: %+v", part)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// web serve records itself while it serves and clears the record when it
// is told to stop, so status lists it for exactly that long.
func TestWebServeRecordsItself(t *testing.T) {
	home, st, _ := statusYard(t, "abcdef123456")
	cfg := &config.Config{Home: home}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := (&app{st: st, cfg: cfg, actor: "me"}).rootCmd()
	cmd.SetArgs([]string{"web", "serve", "--addr", "127.0.0.1:0"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	var views []webView
	for deadline := time.Now().Add(10 * time.Second); len(views) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("web serve recorded nothing")
		}
		views = webViews(cfg)
	}
	v := views[0]
	if v.PID != os.Getpid() || v.Unsafe || v.Open || !strings.HasPrefix(v.Addr, "127.0.0.1:") || strings.HasSuffix(v.Addr, ":0") ||
		v.URL != "http://"+v.Addr+"/" {
		t.Fatalf("view: %+v", v)
	}
	if c, err := net.DialTimeout("tcp", v.Addr, 5*time.Second); err != nil {
		t.Fatalf("nothing listens on the address recorded: %v", err)
	} else {
		c.Close()
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("web serve did not stop")
	}
	if left := webViews(cfg); len(left) != 0 {
		t.Fatalf("listed after it stopped: %+v", left)
	}
	if left, _ := os.ReadDir(webViewsDir(cfg)); len(left) != 0 {
		t.Fatalf("record left after it stopped: %v", left)
	}
}
