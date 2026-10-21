package deploy

// These tests run deploy-void the way a deploy group does: started by the
// exec runner with the environment it builds (see deploy_dokku_test.go for
// the yard), against a yardr server of their own, a throwaway repository,
// and a stand-in for npx that serves the Void CLI's part: it records its
// calls and writes deploy-*.jsonl logs as void 1.1.12's openDeployLog does.

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
)

const (
	voidToken  = "test-void-token-7f3a"
	voidAPI    = "https://void.example.test"
	voidAccess = "test-access-credentials-9b1e"
	voidURL    = "https://site.example.test"
)

// fakeNPX stands in for npx. It records every call, the HOME each ran with,
// and whether the credential was in reach (never its value), then serves
// the void connect and deploy the script runs. A deploy writes its log to
// $HOME/.void/logs as openDeployLog does; the files beside it switch its
// modes: nocli (the tree has no void), noconnect (the platform is not
// reached), fail (exit 3 with 150 lines and an error), signal (ended by
// SIGTERM), degraded (exit 0 with a deploy_degraded record), nolog (exit 0
// with no log) and changedlog (exit 0 with a log of other records). ST is
// the stand-in's state directory, baked in by standin.
const fakeNPX = `printf '%s\n' "$*" >>"$ST/calls"
printf '%s\n' "$HOME" >>"$ST/homes"
printf 'token=%s project=%s api=%s\n' "${VOID_TOKEN:+set}" "${VOID_PROJECT:+set}" "${VOID_API_URL:+set}" >>"$ST/npx-env"
case $1 in
--no-install) shift ;;
*) echo "npx stand-in: want --no-install first, got: $*" >&2; exit 1 ;;
esac
[ "$1" = void ] || { echo "npx stand-in: want void, got: $1" >&2; exit 1; }
shift
cli_missing() { echo "npx: could not determine the void executable to run: the tree has none installed" >&2; exit 1; }
case $1 in
connect)
  shift
  [ -e "$ST/nocli" ] && cli_missing
  if [ -e "$ST/noconnect" ]; then echo "connect: could not reach the platform at $1" >&2; exit 1; fi
  echo "connected to $1"
  ;;
deploy)
  shift
  [ -e "$ST/nocli" ] && cli_missing
  [ -e "$ST/signal" ] && kill -TERM $$
  if [ -e "$ST/fail" ]; then
    i=1
    while [ "$i" -le 150 ]; do echo "deploy out-$i"; i=$((i + 1)); done
    echo "deploy: build failed: no Dockerfile" >&2
    exit 3
  fi
  git rev-parse HEAD >>"$ST/deployed"
  logs=$HOME/.void/logs
  mkdir -p "$logs"
  name=deploy-$(date +%s)-$$-standin.jsonl
  if [ -e "$ST/nolog" ]; then
    echo "deployed (the CLI wrote no log)"
  elif [ -e "$ST/changedlog" ]; then
    printf '%s\n' '{"lvl":"info","msg":"deploy_start","deploymentId":"d9"}' '{"lvl":"info","msg":"deploy_done","deploymentId":"d9"}' >"$logs/$name"
    echo "deployed"
  elif [ -e "$ST/degraded" ]; then
    printf '%s\n' '{"lvl":"info","msg":"deploy_start","deploymentId":"d7"}' '{"lvl":"info","msg":"deploy_degraded","deploymentId":"d7","url":"https://site.example.test"}' >"$logs/$name"
    echo "warning: routing was not published for this deploy" >&2
  else
    printf '%s\n' '{"lvl":"info","msg":"deploy_start","deploymentId":"d2"}' '{"lvl":"info","msg":"deploy_success","deploymentId":"d2","url":"https://first.example.test"}' '{"lvl":"info","msg":"deploy_success","deploymentId":"d2","url":"https://site.example.test"}' >"$logs/$name"
    echo "deployed https://site.example.test"
  fi
  ;;
*) echo "npx stand-in: no void $1" >&2; exit 1 ;;
esac
`

// fakeNPM stands in for npm, the default VOID_INSTALL: it records its call,
// its environment and the tree it ran in.
const fakeNPM = `printf '%s\n' "$*" >>"$ST/npm-calls"
env | sort >"$ST/install-env"
git rev-parse HEAD >"$ST/install-head"
echo "installed"
`

// fakeCustomInstall is a VOID_INSTALL of the group's own: it records the
// same as the default.
const fakeCustomInstall = `printf '%s\n' "$*" >>"$ST/myinstall-calls"
env | sort >"$ST/install-env"
git rev-parse HEAD >"$ST/install-head"
echo "custom install ok"
`

// fakeFailInstall is a VOID_INSTALL that fails.
const fakeFailInstall = `echo "installing the tree's dependencies"
echo "failinstall: cannot write node_modules: permission denied"
exit 1
`

// voidSetup turns a dokku yard into a void one: the deploy-void script, its
// options, a 0600 env_file with the credential, and the stand-ins first on
// PATH.
func voidSetup(y *yard) *yard {
	y.t.Helper()
	y.script = "deploy-void"
	y.opts = map[string]string{"VOID_PROJECT": "site"}
	y.writeEnv(map[string]string{
		"VOID_TOKEN":              voidToken,
		"VOID_API_URL":            voidAPI,
		"VOID_ACCESS_CREDENTIALS": voidAccess,
	})
	y.standin("npx", fakeNPX)
	y.standin("npm", fakeNPM)
	return y
}

// voidYard is newYard for deploy-void: bead r-1 waiting in merged.
func voidYard(t *testing.T) *yard { return voidSetup(newYard(t)) }

// standin writes a stand-in of that name in the run's bin, with the state's
// directory baked in as ST.
func (y *yard) standin(name, body string) {
	y.t.Helper()
	scripttest.Write(y.t, filepath.Join(y.bin, name), "ST="+shellQuote(y.state)+"\n"+body)
}

// writeEnv writes the group's env_file with these entries, 0600 like the
// operator's.
func (y *yard) writeEnv(entries map[string]string) {
	y.t.Helper()
	var b strings.Builder
	for k, v := range entries {
		b.WriteString(k + "=" + v + "\n")
	}
	path := filepath.Join(y.state, "void.env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		y.t.Fatal(err)
	}
	y.envFile = path
}

// vhome is the depot's CLI home the run deploys with.
func (y *yard) vhome() string { return filepath.Join(y.home, "deploy", "void", y.depot) }

// stateLines is the lines of the stand-in state file of that name, none
// for no such file.
func (y *yard) stateLines(name string) []string {
	y.t.Helper()
	b, err := os.ReadFile(filepath.Join(y.state, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		y.t.Fatal(err)
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// worktrees counts the depot's registered checkouts.
func (y *yard) worktrees() int {
	y.t.Helper()
	n := 0
	for _, line := range strings.Split(y.git(y.repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			n++
		}
	}
	return n
}

// noWorktreeLeft fails unless the depot holds exactly the checkouts it held
// before the run: the deploy's worktree is gone.
func (y *yard) noWorktreeLeft(before int) {
	y.t.Helper()
	if got := y.worktrees(); got != before {
		y.t.Fatalf("the depot has %d checkouts, want the %d from before the run", got, before)
	}
}

// mailedAs says whether that crew member has mail.
func (y *yard) mailedAs(who string) bool {
	return strings.Contains(y.yardr("crew", "show", who), "inbox 1 unread")
}

// voidRefused fails unless id went to decide with want in its note, the
// yardmaster was mailed, and no stand-in ran nor worktree left.
func (y *yard) voidRefused(id, want string) {
	y.t.Helper()
	if b := y.bead(id); b.Stage != "decide" || b.Hold {
		y.t.Fatalf("bead %s hold=%v, want decide", b.Stage, b.Hold)
	}
	if notes := y.notes(id); !strings.Contains(notes, want) || !strings.Contains(notes, "nothing was deployed") {
		y.t.Fatalf("the note does not say %q and that nothing was deployed:\n%s", want, notes)
	}
	if !y.mailed() {
		y.t.Fatal("the yardmaster was not mailed")
	}
	for _, f := range []string{"calls", "npm-calls"} {
		if _, err := os.Stat(filepath.Join(y.state, f)); !os.IsNotExist(err) {
			y.t.Fatalf("a stand-in ran for a refused deploy: %s holds %q", f, y.stateLines(f))
		}
	}
	y.noWorktreeLeft(1)
}

// installWithoutTheCredential fails unless the install's recorded
// environment holds none of the credential and its value nowhere.
func (y *yard) installWithoutTheCredential() {
	y.t.Helper()
	env := y.stateLines("install-env")
	if len(env) == 0 {
		y.t.Fatal("the install recorded no environment")
	}
	for _, line := range env {
		if strings.HasPrefix(line, "VOID_TOKEN=") || strings.HasPrefix(line, "VOID_API_URL=") ||
			strings.HasPrefix(line, "VOID_ACCESS_CREDENTIALS=") {
			k, _, _ := strings.Cut(line, "=")
			y.t.Fatalf("the install ran with %s in its environment", k)
		}
		if line == "HOME="+y.vhome() {
			y.t.Fatal("the install ran with the CLI's HOME")
		}
	}
	if joined := strings.Join(env, "\n"); strings.Contains(joined, voidToken) || strings.Contains(joined, voidAccess) {
		y.t.Fatal("the credential's value reached the install's environment")
	}
}

// The base's commit is checked out in a worktree of its own, installed
// without the credential, and deployed; the bead goes to live with the URL
// from the run's own log, the commit and the seconds in its note. The
// depot's checkout is untouched and no worktree is left.
func TestVoidDeploysTheBase(t *testing.T) {
	y := voidYard(t)
	sha := y.commit(y.repo, "b.txt", "landed\n")
	if err := os.WriteFile(filepath.Join(y.repo, "wip.txt"), []byte("someone's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A log of an earlier run, with another URL: the run reads its own.
	logs := filepath.Join(y.vhome(), ".void", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"lvl":"info","msg":"deploy_success","deploymentId":"d0","url":"https://stale.example.test"}` + "\n"
	if err := os.WriteFile(filepath.Join(logs, "deploy-old.jsonl"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want live and closed", b.Stage, b.Status)
	}
	notes := y.notes("r-1")
	if !regexp.MustCompile(`live ` + voidURL + ` at ` + sha[:7] + ` in \d+ s`).MatchString(notes) {
		t.Fatalf("the note does not name the URL, the commit and the seconds:\n%s", notes)
	}
	// The stand-in saw exactly connect then deploy.
	if got, want := y.stateLines("calls"), []string{
		"--no-install void connect " + voidAPI + " --no-login",
		"--no-install void deploy --platform void --project site",
	}; !slices.Equal(got, want) {
		t.Fatalf("npx calls:\n%q\nwant:\n%q", got, want)
	}
	// Each ran with the depot's HOME, and with the credential in reach.
	if got := y.stateLines("homes"); len(got) != 2 || got[0] != y.vhome() || got[1] != y.vhome() {
		t.Fatalf("the CLI's HOMEs: %q, want two runs at %s", got, y.vhome())
	}
	if got, want := y.stateLines("npx-env"), []string{"token=set project=set api=set", "token=set project=set api=set"}; !slices.Equal(got, want) {
		t.Fatalf("the CLI's credential: %q, want it in reach for both commands", got)
	}
	// The base's commit is what was installed and deployed.
	if got := y.stateLines("deployed"); !slices.Equal(got, []string{sha}) {
		t.Fatalf("deployed %q, want the base %s", got, sha)
	}
	if got := y.stateLines("install-head"); !slices.Equal(got, []string{sha}) {
		t.Fatalf("installed %q, want the base %s", got, sha)
	}
	// The default install is npm ci, without the credential.
	if got := y.stateLines("npm-calls"); !slices.Equal(got, []string{"ci"}) {
		t.Fatalf("npm calls: %q, want one ci", got)
	}
	y.installWithoutTheCredential()
	// The credential is in no log and no note.
	if strings.Contains(y.log+notes, voidToken) || strings.Contains(y.log+notes, voidAccess) {
		t.Fatal("the credential is in the group's log or a note")
	}
	if got := y.git(y.repo, "status", "--porcelain"); got != "?? wip.txt" {
		t.Fatalf("the checkout was touched: %q", got)
	}
	y.noWorktreeLeft(1)
}

// What must be there is asked for before anything runs: the project, the
// token and the platform's URL, a slug and an https URL. Each refusal goes
// to decide with the reason, mails the yardmaster, and runs no stand-in.
func TestVoidRefusesBeforeAnything(t *testing.T) {
	full := map[string]string{"VOID_TOKEN": voidToken, "VOID_API_URL": voidAPI, "VOID_ACCESS_CREDENTIALS": voidAccess}
	without := func(k string) map[string]string {
		m := maps.Clone(full)
		delete(m, k)
		return m
	}
	withAPI := func(v string) map[string]string {
		m := maps.Clone(full)
		m["VOID_API_URL"] = v
		return m
	}
	for name, c := range map[string]struct {
		project string
		env     map[string]string
		want    string
	}{
		"no project":                      {"", full, "VOID_PROJECT is not set"},
		"an upper case project":           {"Site", full, "VOID_PROJECT is no project slug"},
		"a project with a space":          {"my site", full, "VOID_PROJECT is no project slug"},
		"a project with an underscore":    {"my_site", full, "VOID_PROJECT is no project slug"},
		"a project with a leading hyphen": {"-site", full, "VOID_PROJECT is no project slug"},
		"no token":                        {"site", without("VOID_TOKEN"), "VOID_TOKEN is not set"},
		"no api url":                      {"site", without("VOID_API_URL"), "VOID_API_URL is not set"},
		"an http api url":                 {"site", withAPI("http://void.example.test"), "VOID_API_URL is no https URL"},
		"a bare host api url":             {"site", withAPI("void.example.test"), "VOID_API_URL is no https URL"},
		"an api url with no host":         {"site", withAPI("https://"), "VOID_API_URL is no https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			y := voidYard(t)
			y.lead()
			y.opts["VOID_PROJECT"] = c.project
			y.writeEnv(c.env)
			y.deploy("r-1")
			y.voidRefused("r-1", c.want)
		})
	}
}

// A child of a train landed on the train's branch: nothing is deployed, and
// it goes on to live with a note that names the train and says so.
func TestVoidChildOfATrainDeploysNothing(t *testing.T) {
	y := voidSetup(trainYard(t))
	if fam := y.family("r-1"); fam.Train != "r-e" || fam.Base != "yard/r-e" {
		t.Fatalf("r-1 has train %q and base %q, want r-e and yard/r-e", fam.Train, fam.Base)
	}
	tip := y.git(y.repo, "rev-parse", "--short", "yard/r-e")
	before := y.worktrees()

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" {
		t.Fatalf("child in %s, want live", b.Stage)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "not deployed: landed on train r-e's branch yard/r-e (at "+tip+")") {
		t.Fatalf("the note does not name the train and its tip:\n%s", notes)
	}
	if calls := y.stateLines("calls"); len(calls) != 0 {
		t.Fatalf("a child of a train reached the CLI: %q", calls)
	}
	if calls := y.stateLines("npm-calls"); len(calls) != 0 {
		t.Fatalf("a child of a train was installed: %q", calls)
	}
	y.noWorktreeLeft(before)
}

// The train itself has no train and the depot's base as its own: its run,
// once its child is through, deploys the base like any landing.
func TestVoidTrainItselfDeploysTheBase(t *testing.T) {
	y := voidSetup(trainYard(t))
	y.deploy("r-1")
	y.git(y.repo, "merge", "-q", "--ff-only", "yard/r-e") // the train landed
	sha := y.git(y.repo, "rev-parse", "main")

	y.deploy("r-e")

	if b := y.bead("r-e"); b.Stage != "live" {
		t.Fatalf("train in %s, want live", b.Stage)
	}
	if got := y.stateLines("deployed"); !slices.Equal(got, []string{sha}) {
		t.Fatalf("deployed %q, want the base %s", got, sha)
	}
	if notes := y.notes("r-e"); !strings.Contains(notes, "live "+voidURL+" at "+sha[:7]) {
		t.Fatalf("the train's note does not say what is running:\n%s", notes)
	}
}

// Two landings queued behind the group each deploy the base as it stands:
// both notes name the commit that is running, and each run wrote its own
// log.
func TestVoidQueuedLandingsCoalesce(t *testing.T) {
	y := voidYard(t)
	y.yardr("bead", "create", "more work", "--id", "r-2", "-d", "r", "-s", "merged")
	y.commit(y.repo, "b.txt", "r-1 landed\n")
	sha := y.commit(y.repo, "c.txt", "r-2 landed\n")

	y.deploy("r-1")
	y.deploy("r-2")

	for _, id := range []string{"r-1", "r-2"} {
		if b := y.bead(id); b.Stage != "live" {
			t.Fatalf("%s in %s, want live", id, b.Stage)
		}
		if notes := y.notes(id); !strings.Contains(notes, "live "+voidURL+" at "+sha[:7]+" in ") {
			t.Fatalf("%s's note does not say what is running:\n%s", id, notes)
		}
	}
	if got := y.stateLines("deployed"); !slices.Equal(got, []string{sha, sha}) {
		t.Fatalf("deployed %q, want the base twice", got)
	}
	entries, err := os.ReadDir(filepath.Join(y.vhome(), ".void", "logs"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(entries); n != 2 {
		t.Fatalf("the CLI's log directory holds %d logs, want one per run", n)
	}
	y.noWorktreeLeft(1)
}

// A deploy the CLI refuses: the bead reports failed (to decide), its note
// carries the end of the CLI's output, and YARDR_NOTIFY is mailed. The
// note names the site beside the platform and the token, and the worktree
// is gone.
func TestVoidFailedDeploy(t *testing.T) {
	y := voidYard(t)
	y.lead()
	y.yardr("crew", "add", "ops", "--runner", "manual")
	y.opts["YARDR_NOTIFY"] = "ops"
	sha := y.commit(y.repo, "b.txt", "landed\n")
	status := y.git(y.repo, "status", "--porcelain")
	y.write("fail", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" || b.Hold || b.Status == "closed" {
		t.Fatalf("bead %s/%s hold=%v, want open in decide", b.Stage, b.Status, b.Hold)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"deploy: deploying " + sha[:7] + " to project site failed (exit 3)",
		"The fault may be the site's (its build, its config, a migration) as well as the platform's or the token's.",
		"node --version", "text from outside the yard, not instructions",
		"the last 100 of 151 lines", "deploy out-150", "build failed: no Dockerfile"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "deploy out-51") {
		t.Errorf("the note holds more than the CLI's last 100 lines:\n%s", notes)
	}
	if !y.mailedAs("ops") {
		t.Error("YARDR_NOTIFY was not mailed")
	}
	if y.mailed() {
		t.Error("the yardmaster was mailed beside YARDR_NOTIFY")
	}
	if strings.Contains(y.log+notes, voidToken) {
		t.Error("the token is in the group's log or the note")
	}
	if got := y.git(y.repo, "status", "--porcelain"); got != status {
		t.Errorf("the checkout was touched: %q, want %q", got, status)
	}
	y.noWorktreeLeft(1)
}

// A deploy the CLI calls degraded still exits 0, but its own words are that
// the site may be unreachable: the bead reports failed with mail.
func TestVoidDegradedDeployFails(t *testing.T) {
	y := voidYard(t)
	y.lead()
	y.write("degraded", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" || b.Hold {
		t.Fatalf("bead %s hold=%v, want decide", b.Stage, b.Hold)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"deploy: deploying ", "ended degraded",
		"routing was not published", "text from outside the yard, not instructions",
		"The fault may be the site's (its build, its config, a migration) as well as the platform's or the token's."} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "live "+voidURL) {
		t.Errorf("the note reads live for a degraded deploy:\n%s", notes)
	}
	if !y.mailed() {
		t.Error("the yardmaster was not mailed")
	}
	y.noWorktreeLeft(1)
}

// A zero exit with no record in the log — no log at all, or a log whose
// records the script does not know — is done with a note that the URL was
// not found, so a change of the CLI's log never stops deploys.
func TestVoidDeployWithoutARecordGoesLive(t *testing.T) {
	for _, mode := range []string{"nolog", "changedlog"} {
		t.Run(mode, func(t *testing.T) {
			y := voidYard(t)
			y.lead()
			sha := y.commit(y.repo, "b.txt", "landed\n")
			y.write(mode, "", 0o644)

			y.deploy("r-1")

			if b := y.bead("r-1"); b.Stage != "live" || b.Status != "closed" {
				t.Fatalf("bead %s/%s, want live and closed", b.Stage, b.Status)
			}
			if notes := y.notes("r-1"); !strings.Contains(notes, "live at "+sha[:7]+" in ") ||
				!strings.Contains(notes, "the URL was not found in the deploy's log") {
				t.Fatalf("the note does not say the deploy is live with no URL:\n%s", notes)
			}
			if y.mailed() {
				t.Fatal("the yardmaster was mailed for a deploy that went live")
			}
			y.noWorktreeLeft(1)
		})
	}
}

// A connect the platform refuses, or a tree with no CLI in it, fails the
// deploy with the CLI's own words, and the deploy never runs.
func TestVoidConnectFailureFailsTheDeploy(t *testing.T) {
	for name, c := range map[string]struct{ file, want string }{
		"the platform is not reached": {"noconnect", "connect: could not reach the platform"},
		"the tree has no CLI":         {"nocli", "the tree has none installed"},
	} {
		t.Run(name, func(t *testing.T) {
			y := voidYard(t)
			y.lead()
			y.write(c.file, "", 0o644)

			y.deploy("r-1")

			if b := y.bead("r-1"); b.Stage != "decide" || b.Hold {
				t.Fatalf("bead %s hold=%v, want decide", b.Stage, b.Hold)
			}
			notes := y.notes("r-1")
			for _, want := range []string{"deploy: connecting to the platform (" + voidAPI + ") failed", c.want,
				"The fault may be the site's (its build, its config, a migration) as well as the platform's or the token's.",
				"node --version"} {
				if !strings.Contains(notes, want) {
					t.Errorf("the note lacks %q:\n%s", want, notes)
				}
			}
			if !y.mailed() {
				t.Error("the yardmaster was not mailed")
			}
			if got := y.stateLines("calls"); len(got) != 1 || !strings.Contains(got[0], " void connect ") {
				t.Fatalf("npx calls: %q, want connect alone", got)
			}
			y.noWorktreeLeft(1)
		})
	}
}

// A deploy ended by a signal says nothing about the platform: the bead is
// held where it is, to be deployed again, and the worktree is gone.
func TestVoidDeployEndedBySignalHolds(t *testing.T) {
	y := voidYard(t)
	y.commit(y.repo, "b.txt", "landed\n")
	status := y.git(y.repo, "status", "--porcelain")
	y.write("signal", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "merged" || !b.Hold {
		t.Fatalf("bead %s hold=%v, want held in merged", b.Stage, b.Hold)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "was ended by signal 15") || !strings.Contains(notes, "deploying ") {
		t.Fatalf("the note does not name the signal:\n%s", notes)
	}
	if got := y.git(y.repo, "status", "--porcelain"); got != status {
		t.Fatalf("the checkout was touched: %q, want %q", got, status)
	}
	y.noWorktreeLeft(1)
}

// A VOID_INSTALL of the group's own runs instead of the default, in the
// tree and without the credential.
func TestVoidHonorsVoidInstall(t *testing.T) {
	y := voidYard(t)
	sha := y.commit(y.repo, "b.txt", "landed\n")
	y.standin("myinstall", fakeCustomInstall)
	y.opts["VOID_INSTALL"] = "myinstall --frozen-lockfile"

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" {
		t.Fatalf("bead in %s, want live", b.Stage)
	}
	if got := y.stateLines("myinstall-calls"); !slices.Equal(got, []string{"--frozen-lockfile"}) {
		t.Fatalf("myinstall calls: %q, want its arguments", got)
	}
	if _, err := os.Stat(filepath.Join(y.state, "npm-calls")); !os.IsNotExist(err) {
		t.Fatal("npm ran beside VOID_INSTALL")
	}
	if got := y.stateLines("install-head"); !slices.Equal(got, []string{sha}) {
		t.Fatalf("installed %q, want the base %s", got, sha)
	}
	y.installWithoutTheCredential()
	y.noWorktreeLeft(1)
}

// An install that fails fails the deploy before the CLI runs: the note
// names the site's scripts, and the yardmaster is mailed.
func TestVoidFailedInstallDecides(t *testing.T) {
	y := voidYard(t)
	y.lead()
	sha := y.commit(y.repo, "b.txt", "landed\n")
	y.standin("failinstall", fakeFailInstall)
	y.opts["VOID_INSTALL"] = "failinstall"

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" || b.Hold || b.Status == "closed" {
		t.Fatalf("bead %s/%s hold=%v, want open in decide", b.Stage, b.Status, b.Hold)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"deploy: installing the tree at " + sha[:7], "failed (exit 1)",
		"the fault is likeliest the site's", "cannot write node_modules"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() {
		t.Error("the yardmaster was not mailed")
	}
	if calls := y.stateLines("calls"); len(calls) != 0 {
		t.Fatalf("a failed install reached the CLI: %q", calls)
	}
	y.noWorktreeLeft(1)
}

// An install ended by a signal holds the bead where it is, as a deploy
// ended by one does.
func TestVoidInstallEndedBySignalHolds(t *testing.T) {
	y := voidYard(t)
	y.standin("siginstall", "kill -TERM $$\n")
	y.opts["VOID_INSTALL"] = "siginstall"

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "merged" || !b.Hold {
		t.Fatalf("bead %s hold=%v, want held in merged", b.Stage, b.Hold)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "was ended by signal 15") || !strings.Contains(notes, "installing the tree's dependencies") {
		t.Fatalf("the note does not name the signal:\n%s", notes)
	}
	y.noWorktreeLeft(1)
}
