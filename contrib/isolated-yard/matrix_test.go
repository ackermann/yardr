package isolatedyard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/contrib/server-yard/managers"
)

// matrixOperator is the person every yard of the matrix is for. The steps
// act as them (--actor): a command acts as its login otherwise, and the
// login is no user of a yard it did not make.
const matrixOperator = "jo"

// endToEndFuse is how long the agents get to work one bead through its flow.
// It is a fuse against a hang and no measure: the wait ends when the bead
// closes, which takes 15 to 25 s.
const endToEndFuse = 4 * time.Minute

// depotKind is what the steps need to know of the kind of depot a shape
// works in. A git depot's beads are built on branches and land on its base
// through an assembly; a dir depot's are worked in the directory itself and
// land nowhere (docs/dir-depots.md). So the kinds differ in the pack that
// sets them up, in what the stand-in agent does to do its work, and in
// where a bead ends.
type depotKind struct {
	name string // the depot's name in the yard
	kind string // git or dir
	base string // the base branch of a git depot, "" for a dir depot
	pack string // the pack yardr carries for the kind
	// other is the pack it carries for the other kind, which depot init
	// refuses, and refuses is the start of the error it says so with.
	other, refuses string
	// work is what the stand-in builder does to the depot, as shell, before
	// it reports done; approve is what it adds to the reviewer's report.
	work, approve string
	// ends is the stage a bead closes in.
	ends string
}

// attempt is a command that a shape is refused, and how. code is the exit
// status, which tells the kind of refusal (1 a command refused, 2 one that
// had to ask and had no terminal to ask in, 3 one for what is not there:
// exitCode in internal/cli/root.go), and text the start of the error it
// prints, which says why. {bead} stands for the bead the steps work.
type attempt struct {
	args []string
	code int
	text string
}

// shape is one way a yard is set up. What differs between shapes is data
// here, so that the steps can be the same for all of them.
type shape struct {
	name string
	// server is a yard set up as docs/install.md sets up a server, by
	// contrib/server-yard: its daemon and web view are login services, kept
	// by service managers, which in the matrix are the script's stand-ins.
	server bool
	depot  depotKind
	// cannot are the steps (by name) that the shape cannot do, each with the
	// refusal its attempt ends in. The matrix pins the refusal where it
	// would otherwise run the step, and never skips one.
	cannot map[string]attempt
}

var gitDepot = depotKind{
	name: "web", kind: "git", base: "main", pack: "starter",
	other: "dir", refuses: "depot web is a git depot, and pack dir is for a directory depot:",
	work:    `echo greeting > greeting.txt && git add greeting.txt && git commit -q -m "add a greeting"`,
	approve: " --outcome done-safe",
	ends:    "merged",
}

var dirDepot = depotKind{
	name: "notes", kind: "dir", pack: "dir",
	other: "starter", refuses: "depot notes is a directory depot, and pack starter is for a git depot:",
	work: `echo greeting > greeting.txt`,
	ends: "done",
}

// The first shapes. Left out for now: a yard linked with a peer, and a
// hosted one, which waits for the edge to be live.
var shapes = []shape{
	{name: "git depot", depot: gitDepot},
	{name: "dir depot", depot: dirDepot, cannot: map[string]attempt{
		// Nothing merges in a directory: its flow ends at review, so there
		// is no stage to hand a finished bead to for landing.
		"land": {args: []string{"bead", "advance", "{bead}", "--to", "approved"}, code: 1,
			text: "bead {bead}: flow dir.default does not allow done -> approved"},
	}},
	{name: "server yard", server: true, depot: gitDepot},
}

// step is one thing every shape is walked through, in this order; each
// needs what the ones before it made.
type step struct {
	name string
	do   func(t *testing.T, y *yard)
}

var steps = []step{
	{"init", stepInit},
	{"doctor", stepDoctor},
	{"depot add", stepDepotAdd},
	{"depot init", stepDepotInit},
	{"bead create", stepBeadCreate},
	{"bead list", stepBeadList},
	{"bead show", stepBeadShow},
	{"bead end to end", stepEndToEnd},
	{"land", stepLand},
	{"status", stepStatus},
	{"daemon stop", stepDaemonStop},
	{"daemon start", stepDaemonStart},
	{"daemon stop again", stepDaemonStopAgain},
}

// A refusal pinned under a name that is no step would never run, and so
// never be pinned: the table must name steps there are.
func TestYardShapesRefuseOnlyRealSteps(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, st := range steps {
		if names[st.name] {
			t.Errorf("two steps are called %q", st.name)
		}
		names[st.name] = true
	}
	for _, sh := range shapes {
		for name := range sh.cannot {
			if !names[name] {
				t.Errorf("shape %q cannot do %q, which is no step", sh.name, name)
			}
		}
	}
}

// TestYardShapes walks every shape of yard through the same steps, so that
// what is proved of the default setup is proved of the others too: a yard
// of a git depot, one of a directory, and one set up as a server. Each
// shape is a yard of its own (isolated-yard up, with its stand-in agent
// and its herdr), and every step runs through that yard's D/e. A shape that
// cannot do a step has the refusal it gets named in the table, and the
// matrix pins that exit status and the start of that error in place of the
// step: a refusal is a result, and a step is never skipped.
//
// It runs in the quiet run alone (docs/testing.md R6), which sets
// YARDR_QUIET_RUN: a shape takes 35 to 50 s, the three run beside one
// another, and neither a builder's gate nor a landing should pay for that.
// Everywhere else it skips. The package is on R1's allow-list for it
// (.yardr/test).
func TestYardShapes(t *testing.T) {
	if os.Getenv("YARDR_QUIET_RUN") == "" {
		t.Skip("the yard-shape matrix runs in the quiet run only, which sets YARDR_QUIET_RUN=1 (docs/testing.md R6)")
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			// A yard of its own: a directory under /tmp, a herdr server
			// and a home that nothing else shares, and no port.
			t.Parallel()
			y := newYard(t, sh)
			for _, st := range steps {
				ok := t.Run(st.name, func(t *testing.T) {
					if a, cannot := sh.cannot[st.name]; cannot {
						y.refused(t, a)
						return
					}
					st.do(t, y)
				})
				if !ok {
					t.Logf("the steps after %q are not run: each needs what the ones before it made", st.name)
					return
				}
			}
			y.finish(t)
		})
	}
}

// yard is the yard of one shape, once it is up.
type yard struct {
	d     string   // the isolated yard
	home  string   // its yardr home
	via   []string // the command that runs a program in it: D/e, and for the server yard `env NAME=value ...` after it
	shape shape
	repo  string // the depot's directory
	bead  string // the bead the steps work
}

// newYard brings up the isolated yard of a shape. A server yard also gets
// the stand-in service managers of contrib/server-yard on its PATH, and
// every command it runs after that carries the variables they read their
// state from.
func newYard(t *testing.T, sh shape) *yard {
	t.Helper()
	d := up(t)
	y := &yard{d: d, home: filepath.Join(d, "y"), via: []string{filepath.Join(d, "e")}, shape: sh}
	if sh.server {
		env, err := managers.Install(repoRoot(t), filepath.Join(d, "bin"), filepath.Join(d, "managers"))
		if err != nil {
			t.Fatal(err)
		}
		y.via = append(append(y.via, "env"), env...)
	}
	return y
}

// repoRoot is the repository's root, where the scripts the matrix runs are.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// yardr runs yardr in the yard as the operator, as drive does, and returns
// its exit status.
func (y *yard) yardr(t *testing.T, v any, args ...string) int {
	t.Helper()
	return driveVia(t, y.via, v, append([]string{"--actor", matrixOperator}, args...)...)
}

// ok is yardr for a command that must go through.
func (y *yard) ok(t *testing.T, v any, args ...string) {
	t.Helper()
	if code := y.yardr(t, v, args...); code != 0 {
		t.Fatalf("yardr %s: exit %d, %+v", strings.Join(args, " "), code, v)
	}
}

// refused runs an attempt and checks that it was refused the way it says.
func (y *yard) refused(t *testing.T, a attempt) {
	t.Helper()
	args := make([]string, len(a.args))
	for i, arg := range a.args {
		args[i] = strings.ReplaceAll(arg, "{bead}", y.bead)
	}
	text := strings.ReplaceAll(a.text, "{bead}", y.bead)
	var no refusal
	if code := y.yardr(t, &no, args...); code != a.code || !strings.HasPrefix(no.Error, text) {
		t.Fatalf("yardr %s: exit %d, error %q; want exit %d and an error starting %q",
			strings.Join(args, " "), code, no.Error, a.code, text)
	}
}

// daemonState is what yardr daemon status says.
type daemonState struct {
	Running bool `json:"running"`
	PID     int  `json:"pid"`
	Service struct {
		Installed bool `json:"installed"`
		Loaded    bool `json:"loaded"`
	} `json:"service"`
}

// doctorState is what yardr doctor says of a yard, as far as the steps ask.
type doctorState struct {
	Home  string `json:"home"`
	Yard  bool   `json:"yard"`
	State struct {
		Server     string `json:"server"`
		Daemon     bool   `json:"daemon"`
		Yardmaster struct {
			Running bool `json:"running"`
		} `json:"yardmaster"`
	} `json:"state"`
	Faults []string `json:"faults"`
	// Next is the first setup step not done; absent when all are.
	Next *struct {
		Key string `json:"key"`
	} `json:"next"`
}

// beadState is a bead as the commands print it, as far as the steps ask.
type beadState struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Stage       string      `json:"stage"`
	Status      string      `json:"status"`
	Depot       string      `json:"depot"`
	Base        string      `json:"base"`
	Hold        bool        `json:"hold"`
	CloseReason string      `json:"close_reason"`
	Notes       []noteState `json:"notes"`
}

// noteState is a note on a bead.
type noteState struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

// depotState is a depot as depot add and depot show print it.
type depotState struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	Base string `json:"base"`
}

// waitFor polls cond every 250 ms until it holds, and reports whether it did
// within fuse. The fuse is against a hang; what is waited for is the
// condition.
func waitFor(fuse time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(fuse); ; time.Sleep(250 * time.Millisecond) {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// daemon is the yard's daemon as it says it runs now.
func (y *yard) daemon(t *testing.T) daemonState {
	t.Helper()
	var d daemonState
	y.ok(t, &d, "daemon", "status")
	return d
}

// doctor is yardr doctor's report, with the exit status checked: it is 0
// while the yard lacks nothing it needs.
func (y *yard) doctor(t *testing.T) doctorState {
	t.Helper()
	var rep doctorState
	y.ok(t, &rep, "doctor")
	if rep.Home != y.home || !rep.Yard || len(rep.Faults) != 0 {
		t.Fatalf("doctor: %+v; want the yard in %s and no fault", rep, y.home)
	}
	return rep
}

// nextStep is the key of the first setup step not done, "" when all are.
func (y *yard) nextStep(t *testing.T) string {
	t.Helper()
	if next := y.doctor(t).Next; next != nil {
		return next.Key
	}
	return ""
}

// dump prints what the yard kept of its agents and daemon, for a step that
// failed waiting.
func (y *yard) dump(t *testing.T) {
	t.Helper()
	for _, name := range []string{"agents.log", "run.out", filepath.Join("y", "daemon.log")} {
		b, _ := os.ReadFile(filepath.Join(y.d, name))
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		t.Logf("%s, the last of it:\n%s", name, strings.Join(lines[max(0, len(lines)-30):], "\n"))
	}
	out, _ := runIn(y.d, "yardr", "--actor", matrixOperator, "session", "list")
	t.Logf("yardr session list:\n%s", out)
}

// finish takes the yard down and checks that nothing of it is left: the
// server the steps never stopped included.
func (y *yard) finish(t *testing.T) {
	t.Helper()
	down(t, y.d, pidIn(t, filepath.Join(y.home, "server.pid")))
}

func stepInit(t *testing.T, y *yard) {
	if y.shape.server {
		y.setUpServer(t)
	} else {
		var got struct {
			Home       string `json:"home"`
			Harness    string `json:"harness"`
			Yardmaster string `json:"yardmaster"`
			Operator   string `json:"operator"`
			Start      struct {
				Running    bool `json:"running"`
				Yardmaster struct {
					Agent string `json:"agent"`
				} `json:"yardmaster"`
			} `json:"start"`
		}
		y.ok(t, &got, "init", "--harness", "claude", "--operator", matrixOperator, "--start", "--no-chat")
		if got.Home != y.home || got.Harness != "claude" || got.Yardmaster != "yardmaster" || got.Operator != matrixOperator ||
			!got.Start.Running || got.Start.Yardmaster.Agent == "" {
			t.Fatalf("init = %+v", got)
		}
	}
	// However it was made, a server answers and a daemon runs. A daemon that
	// a service manager has just started may still be taking its lock.
	var srv struct {
		Running bool `json:"running"`
	}
	y.ok(t, &srv, "server", "status")
	if !srv.Running || !waitFor(30*time.Second, func() bool { return y.daemon(t).Running }) {
		t.Fatalf("after init: server running %v, daemon %+v", srv.Running, y.daemon(t))
	}
}

// setUpServer runs contrib/server-yard in the yard, as docs/install.md has a
// server set up, and checks what it leaves besides the yard: herdr's unit,
// linger, and the daemon and the web view as services.
func (y *yard) setUpServer(t *testing.T) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "contrib/server-yard/server-yard")
	cmd := exec.Command(y.via[0], append(slices.Clone(y.via[1:]), script, "--harness", "claude", "--operator", matrixOperator)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("server-yard: %v\n%s", err, out)
	}
	unit := filepath.Join(y.d, "c", "systemd", "user", "herdr.service")
	linger := filepath.Join(y.d, "managers", "loginctl", "linger")
	for _, f := range []string{unit, linger} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("server-yard left no %s: %v", f, err)
		}
	}
}

func stepDoctor(t *testing.T, y *yard) {
	rep := y.doctor(t)
	if rep.State.Server != "running" || !rep.State.Daemon || !rep.State.Yardmaster.Running {
		t.Fatalf("doctor: %+v; want the server, the daemon and the yardmaster running", rep.State)
	}
	// A yard with nothing in it asks for a depot first.
	if rep.Next == nil || rep.Next.Key != "depot" {
		t.Fatalf("doctor: next step %+v, want depot", rep.Next)
	}
}

func stepDepotAdd(t *testing.T, y *yard) {
	k := y.shape.depot
	y.repo = filepath.Join(y.d, "h", k.name)
	if k.kind == "git" {
		in(t, y.d, "git", "init", "-q", "-b", "main", y.repo)
		in(t, y.d, "git", "-C", y.repo, "commit", "-q", "--allow-empty", "-m", "first")
	} else {
		if err := os.MkdirAll(y.repo, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(y.repo, "a.txt"), []byte("a note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"depot", "add", k.name, y.repo}
	if k.base != "" {
		args = append(args, "--base", k.base)
	}
	want := depotState{k.name, y.repo, k.kind, k.base}
	var added, shown depotState
	y.ok(t, &added, args...)
	y.ok(t, &shown, "depot", "show", k.name)
	var listed []depotState
	y.ok(t, &listed, "depot", "list")
	if added != want || shown != want || !slices.Equal(listed, []depotState{want}) {
		t.Fatalf("depot added %+v, shown %+v, listed %+v; want %+v", added, shown, listed, want)
	}
}

func stepDepotInit(t *testing.T, y *yard) {
	k := y.shape.depot
	answers := []string{"--bind", "builders=claude", "--bind", "reviewers=claude",
		"--members", "backlog=" + matrixOperator, "--members", "decisions=" + matrixOperator}
	// The pack of the other kind of depot is refused, before anything is
	// written: the next step is still the depot's files.
	y.refused(t, attempt{args: append([]string{"depot", "init", k.name, "--pack", k.other, "--yes"}, answers...),
		code: 1, text: k.refuses})
	if next := y.nextStep(t); next != "depot_files" {
		t.Fatalf("after the refused pack the next step is %q, want depot_files", next)
	}
	var got struct {
		Result struct {
			Pack    string `json:"pack"`
			Depot   string `json:"depot"`
			Created int    `json:"created"`
		} `json:"result"`
	}
	y.ok(t, &got, append([]string{"depot", "init", k.name, "--yes"}, answers...)...)
	if got.Result.Pack != k.pack || got.Result.Depot != k.name || got.Result.Created == 0 {
		t.Fatalf("depot init: %+v, want pack %s made for depot %s", got.Result, k.pack, k.name)
	}
	if next := y.nextStep(t); next != "first_bead" {
		t.Fatalf("after depot init the next step is %q, want first_bead", next)
	}
}

func stepBeadCreate(t *testing.T, y *yard) {
	k := y.shape.depot
	var b beadState
	y.ok(t, &b, "bead", "create", "Add a greeting", "--depot", k.name, "-b", "Done when greeting.txt says hello.")
	if !strings.HasPrefix(b.ID, k.name+"-") || b.Title != "Add a greeting" || b.Stage != "backlog" || b.Status != "open" || b.Depot != k.name {
		t.Fatalf("bead create: %+v", b)
	}
	y.bead = b.ID
}

func stepBeadList(t *testing.T, y *yard) {
	var list []beadState
	y.ok(t, &list, "bead", "list")
	if len(list) != 1 || list[0].ID != y.bead || list[0].Stage != "backlog" || list[0].Status != "open" {
		t.Fatalf("bead list: %+v; want %s, open in backlog, alone", list, y.bead)
	}
}

func stepBeadShow(t *testing.T, y *yard) {
	k := y.shape.depot
	var b beadState
	y.ok(t, &b, "bead", "show", y.bead)
	if b.ID != y.bead || b.Title != "Add a greeting" || b.Body != "Done when greeting.txt says hello." ||
		b.Stage != "backlog" || b.Status != "open" || b.Depot != k.name || b.Base != k.base {
		t.Fatalf("bead show: %+v; want it as created, on base %q", b, k.base)
	}
}

// stepEndToEnd has the stand-in agents work a bead through the depot's flow
// and waits for it to end. The stand-in runs no command a prompt asks for,
// so what its pane does is written to D/run, a command to a line, each run
// by every stand-in that takes a first prompt: the lines name the group
// they are for. They are written now, after the yardmaster's stand-in took
// its prompt, so the yardmaster never ran them.
func stepEndToEnd(t *testing.T, y *yard) {
	k := y.shape.depot
	run := fmt.Sprintf("[ \"$YARDR_GROUP\" = %s-builders ] && %s && yardr bead done \"$YARDR_BEAD\"\n"+
		"[ \"$YARDR_GROUP\" = %s-reviewers ] && yardr bead done \"$YARDR_BEAD\"%s\n", k.name, k.work, k.name, k.approve)
	if err := os.WriteFile(filepath.Join(y.d, "run"), []byte(run), 0o600); err != nil {
		t.Fatal(err)
	}
	var moved beadState
	y.ok(t, &moved, "bead", "advance", y.bead, "--to", "new")
	if moved.Stage != "new" {
		t.Fatalf("bead advance: %+v", moved)
	}
	var b beadState
	if !waitFor(endToEndFuse, func() bool {
		b = beadState{}
		y.ok(t, &b, "bead", "show", y.bead)
		return b.Status == "closed" || b.Hold
	}) || b.Hold {
		y.dump(t)
		t.Fatalf("bead %s: stage %s, status %s, held %v; want it closed in %s", y.bead, b.Stage, b.Status, b.Hold, k.ends)
	}
	if b.Stage != k.ends || b.CloseReason != k.ends {
		t.Fatalf("bead %s closed in %s as %q, want %s", y.bead, b.Stage, b.CloseReason, k.ends)
	}

	// The stand-in agents did it: one started for the bead in each group
	// that worked it, and their work is where the kind of depot keeps it.
	log, err := os.ReadFile(filepath.Join(y.d, "agents.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{k.name + "-builders", k.name + "-reviewers"} {
		// Each start is a line of the log; the variables it was started with
		// are fields of it, which a whole-field match tells from a longer name.
		if !slices.ContainsFunc(strings.Split(string(log), "\n"), func(line string) bool {
			fields := strings.Fields(line)
			return slices.Contains(fields, "start") && slices.Contains(fields, "YARDR_BEAD="+y.bead) &&
				slices.Contains(fields, "YARDR_GROUP="+group)
		}) {
			t.Errorf("no stand-in agent was started for %s in group %s:\n%s", y.bead, group, log)
		}
	}
	var greeting string
	if k.kind == "git" {
		greeting = in(t, y.d, "git", "-C", y.repo, "show", k.base+":greeting.txt")
	} else {
		file, err := os.ReadFile(filepath.Join(y.repo, "greeting.txt"))
		if err != nil {
			t.Fatal(err)
		}
		greeting = string(file)
	}
	if greeting != "greeting\n" {
		t.Errorf("the agent's greeting.txt reads %q", greeting)
	}
}

// stepLand checks that the bead landed on the base: the assembly's note
// names the commit at the base's tip. A shape that cannot land has the
// refusal it gets in its table entry instead.
func stepLand(t *testing.T, y *yard) {
	k := y.shape.depot
	tip := strings.TrimSpace(in(t, y.d, "git", "-C", y.repo, "rev-parse", "--short", k.base))
	var b beadState
	y.ok(t, &b, "bead", "show", y.bead)
	if !slices.ContainsFunc(b.Notes, func(n noteState) bool {
		return n.Author == k.name+"-assembly" && strings.HasPrefix(n.Text, "merged "+tip)
	}) {
		t.Fatalf("no note of %s-assembly says it merged %s: %+v", k.name, tip, b.Notes)
	}
}

func stepStatus(t *testing.T, y *yard) {
	var st struct {
		Home   string `json:"home"`
		Server struct {
			Running bool `json:"running"`
		} `json:"server"`
		Daemon daemonState `json:"daemon"`
		Crew   []struct {
			Name  string `json:"name"`
			Agent string `json:"agent"`
		} `json:"crew"`
		Web struct {
			Service struct {
				Installed bool `json:"installed"`
				Loaded    bool `json:"loaded"`
			} `json:"service"`
		} `json:"web"`
		Faults int `json:"faults"`
	}
	y.ok(t, &st, "status")
	if st.Home != y.home || !st.Server.Running || !st.Daemon.Running || st.Faults != 0 ||
		len(st.Crew) != 1 || st.Crew[0].Name != "yardmaster" || st.Crew[0].Agent == "" {
		t.Fatalf("status: %+v", st)
	}
	// Only a server yard keeps its daemon and web view as services.
	if got, want := st.Daemon.Service.Installed && st.Daemon.Service.Loaded, y.shape.server; got != want {
		t.Errorf("the daemon's service is installed and loaded: %v, want %v", got, want)
	}
	if got, want := st.Web.Service.Installed && st.Web.Service.Loaded, y.shape.server; got != want {
		t.Errorf("the web view's service is installed and loaded: %v, want %v", got, want)
	}
	// Status says what is there, doctor what is missing: nothing is, and
	// the setup has no step left.
	if next := y.nextStep(t); next != "" {
		t.Errorf("doctor: the setup still asks for %q after a bead ended", next)
	}
}

func stepDaemonStop(t *testing.T, y *yard) {
	before := y.daemon(t)
	if !before.Running || before.PID == 0 {
		t.Fatalf("daemon status before the stop: %+v", before)
	}
	y.stopDaemon(t, before.PID)
	// The daemon's service, if the yard has one, is the manager's to keep;
	// stopping the daemon takes it from neither the manager nor the server.
	after := y.daemon(t)
	if after.Running || after.Service != before.Service {
		t.Errorf("daemon status after the stop: %+v, was %+v", after, before)
	}
	var srv struct {
		Running bool `json:"running"`
	}
	y.ok(t, &srv, "server", "status")
	if !srv.Running {
		t.Error("stopping the daemon stopped the server")
	}
}

// stopDaemon stops the daemon of pid and checks that it is gone.
func (y *yard) stopDaemon(t *testing.T, pid int) {
	t.Helper()
	var stop struct {
		Stopped bool `json:"stopped"`
		PID     int  `json:"pid"`
	}
	y.ok(t, &stop, "daemon", "stop")
	if !stop.Stopped || stop.PID != pid {
		t.Fatalf("daemon stop: %+v, want it to have stopped pid %d", stop, pid)
	}
	if !waitFor(10*time.Second, func() bool { return !running(pid) }) {
		t.Fatalf("daemon %d still runs after daemon stop", pid)
	}
}

func stepDaemonStart(t *testing.T, y *yard) {
	var start struct {
		PID int `json:"pid"`
	}
	y.ok(t, &start, "daemon", "start")
	if d := y.daemon(t); start.PID == 0 || !d.Running || d.PID != start.PID || !running(start.PID) {
		t.Fatalf("daemon start: %+v; daemon status %+v", start, d)
	}
	// A yard has one daemon: a second start is refused, naming the first.
	y.refused(t, attempt{args: []string{"daemon", "start"}, code: 1,
		text: fmt.Sprintf("a daemon runs on the yard in %s already (pid %d)", y.home, start.PID)})
}

// stepDaemonStopAgain stops the daemon the last step started, and then stops
// once more with none running: a stop that can be run again is not an error.
func stepDaemonStopAgain(t *testing.T, y *yard) {
	d := y.daemon(t)
	y.stopDaemon(t, d.PID)
	var again struct {
		Stopped bool `json:"stopped"`
		PID     int  `json:"pid"`
	}
	y.ok(t, &again, "daemon", "stop")
	if again.Stopped || again.PID != 0 || y.daemon(t).Running {
		t.Fatalf("daemon stop with none running: %+v; want it to stop nothing, and succeed", again)
	}
}
