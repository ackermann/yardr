package assembly

// The assembly installs the landed build: where the yard's upgrade_from
// setting names the bead's depot, merge-local runs the installed yardr's
// upgrade after the push, folds its one-line summary into the landing note
// and its seconds into the log line, and still merges when the install
// fails but the yard answers. A wagon never installs: it lands on its
// train's branch, not the depot's base, while the install builds the
// depot's checkout. See docs/upgrade.md, "The assembly step".
//
// The tests run merge-local the way newYard's do; what differs per test is
// the yardr the script runs (y.bin) and, for the real installs, the go on
// its PATH (y.opts).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// upgradeFrom sets the yard's upgrade_from setting to depot, giving the
// operator a notify source first, since setting it is refused without one.
func (y *yard) upgradeFrom(depot string) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	const pushover = `name: pushover
base: https://api.pushover.net/1
credential: { env: PUSHOVER_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string, max: 1024 } }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PUSHOVER_USER }, text: text }
`
	if _, err := st.PutSource(context.Background(), "pushover.yaml", pushover, "test"); err != nil {
		y.t.Fatal(err)
	}
	if _, err := st.SetUser(context.Background(), "test", map[string]string{store.UserNotify: "pushover"}, "test"); err != nil {
		y.t.Fatal(err)
	}
	y.yardr("settings", "set", "upgrade_from", depot)
}

// writeScript writes content as an executable file and returns its path.
func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// yardrCopy copies the test binary aside, so a test's install writes its
// own target instead of the binary every test shares.
func yardrCopy(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(yardrBin)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "yardr")
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// fakeGo is a go that answers GOBIN with dir and installs these same bytes
// over dir's yardr by rename, as go install does, so the gate verifies the
// real upgrade's shape with nothing compiled.
func fakeGo(t *testing.T, dir string) string {
	t.Helper()
	fake := t.TempDir()
	// The install writes through a temporary file renamed into place, as go
	// install does, with the executable bit set: without it the gate's
	// samples cannot run the new binary at all.
	script := `#!/bin/sh
if [ "$1" = "env" ] && [ "$2" = "GOBIN" ]; then echo ` + shellQuote(dir) + `; exit 0; fi
if [ "$1" = "install" ]; then new=` + shellQuote(filepath.Join(dir, "yardr.new")) + `; dst=` + shellQuote(filepath.Join(dir, "yardr")) + `; cat ` + shellQuote(yardrBin) + ` > "$new" && chmod 755 "$new" && mv "$new" "$dst"; exit $?; fi
exit 1
`
	return writeScript(t, fake, "go", script)
}

// The five copies of merge-local are one script: the assembly runs the
// installed contrib copy, a yard without a checkout the starter pack's, and
// the design, contributors and website packs carry the starter's byte for
// byte. A change to one is made in all five.
func TestMergeLocalCopiesIdentical(t *testing.T) {
	want, err := os.ReadFile("merge-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../../packs/starter/scripts/merge-local", "../../packs/design/scripts/merge-local", "../../packs/contributors/scripts/merge-local", "../../packs/website/scripts/merge-local"} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from contrib/assembly/merge-local: change all five", p)
		}
	}
}

// A landing on the depot upgrade_from names installs the landed build and
// notes the outcome: the summary joins the landing note, the seconds the
// log line, and the install's own lines the log. The install also refreshes
// the scripts the yard already has from the tree that landed, and the note
// names them, so a fix landed in merge-local is live for the next landing
// with no hand step; a script the yard lacks is not added.
func TestMergeLocalInstallsOnTheNamedDepot(t *testing.T) {
	y := newYardServer(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	const old, landed = "#!/bin/sh\necho old\n", "#!/bin/sh\necho landed\n"
	if err := os.MkdirAll(filepath.Join(y.wt, "contrib", "assembly"), 0o755); err != nil {
		t.Fatal(err)
	}
	y.commit(y.wt, "contrib/assembly/merge-local", landed)
	y.commit(y.wt, "contrib/assembly/pr-train", landed)
	scripts := filepath.Join(y.home, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, scripts, "merge-local", old)
	y.upgradeFrom("r")
	bin := t.TempDir()
	y.bin = yardrCopy(t, bin)
	y.opts = "PATH=" + shellQuote(filepath.Dir(fakeGo(t, bin))) + ":$PATH"

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if !regexp.MustCompile(`; install ok in \d+ s \(scripts refreshed: merge-local\)`).MatchString(y.notes()) {
		t.Fatalf("no install summary naming the refreshed script in the landing note:\n%s", y.notes())
	}
	if !regexp.MustCompile(`(?m)^== r-1 took: the tests \d+ s, upgrade \d+ s$`).MatchString(y.log) {
		t.Fatalf("the log line has no upgrade seconds:\n%s", y.log)
	}
	for _, want := range []string{"backup", "keep", "install    go install . at ", "scripts    merge-local refreshed", "health     ok in "} {
		if !strings.Contains(y.log, want) {
			t.Fatalf("the log lacks the install's %q line:\n%s", want, y.log)
		}
	}
	for file, want := range map[string]string{"merge-local": landed, "merge-local.previous": old} {
		if got, err := os.ReadFile(filepath.Join(scripts, file)); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(scripts, "pr-train")); err == nil {
		t.Fatalf("the install added pr-train, which the yard did not have")
	}
	for _, p := range []string{filepath.Join(y.home, "backups", "yard.db.before-install"), filepath.Join(bin, "yardr.prev")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the install left no %s: %v", p, err)
		}
	}
	if r := y.refused(); len(r) > 0 {
		t.Fatalf("refused moves: %v", r)
	}
}

// A landing on any other depot runs exactly as before: the install step
// never runs, so the note and the log line say nothing of it. The script's
// yardr fails the test if upgrade is called at all.
func TestMergeLocalSkipsInstallOnAnotherDepot(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	y.yardr("depot", "add", "other", y.repo, "--base", "main")
	y.upgradeFrom("other")
	y.bin = writeScript(t, t.TempDir(), "yardr", `#!/bin/sh
if [ "$1" = "upgrade" ]; then echo "upgrade ran on another depot" >&2; exit 99; fi
exec `+shellQuote(yardrBin)+` "$@"
`)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if strings.Contains(y.notes(), "install") {
		t.Fatalf("the landing note speaks of an install:\n%s", y.notes())
	}
	if !regexp.MustCompile(`(?m)^== r-1 took: the tests \d+ s$`).MatchString(y.log) {
		t.Fatalf("the log line is not the one without the step:\n%s", y.log)
	}
}

// A scripts line that says none were refreshed names nothing in the landing
// note: the note reads names from lines that end in "refreshed", and this
// one must not be taken for a script called "none".
func TestMergeLocalNamesNoScriptsThatWereKept(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	y.upgradeFrom("r")
	y.bin = writeScript(t, t.TempDir(), "yardr", `#!/bin/sh
if [ "$1" = "upgrade" ] && [ "$2" = "--help" ]; then
  printf '%s\n' 'Usage:' '  yardr upgrade [flags]'
  exit 0
fi
if [ "$1" = "upgrade" ]; then
  printf '%s\n' 'install    go install . at deadbeefcd12' 'scripts    kept as they were, see the warnings above' 'health     ok in 1 s'
  exit 0
fi
exec `+shellQuote(yardrBin)+` "$@"
`)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if !regexp.MustCompile(`; install ok in \d+ s`).MatchString(y.notes()) {
		t.Fatalf("no install summary in the landing note:\n%s", y.notes())
	}
	if strings.Contains(y.notes(), "(scripts") {
		t.Fatalf("the landing note names scripts that were kept:\n%s", y.notes())
	}
}

// Where the installed yardr predates the command, the landing notes the
// bootstrap line instead of failing: install the new yardr once by hand
// first, and the bead still merges.
func TestMergeLocalOldBinaryNotesBootstrap(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	y.upgradeFrom("r")
	y.bin = writeScript(t, t.TempDir(), "yardr", `#!/bin/sh
if [ "$1" = "upgrade" ] && [ "$2" = "--help" ]; then
  printf '%s\n' 'Usage:' '  yardr [flags]' '  yardr [command]'
  exit 0
fi
if [ "$1" = "upgrade" ]; then echo 'yardr: unknown command "upgrade" for "yardr"' >&2; exit 1; fi
exec `+shellQuote(yardrBin)+` "$@"
`)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if want := "; install skipped: the installed yardr knows no upgrade (install the new yardr once by hand first)"; !strings.Contains(y.notes(), want) {
		t.Fatalf("no bootstrap line %q in the note:\n%s", want, y.notes())
	}
	if strings.Contains(y.log, "upgrade ") && regexp.MustCompile(`took:.*upgrade \d+ s`).MatchString(y.log) {
		t.Fatalf("the log line times an install that never ran:\n%s", y.log)
	}
}

// An upgrade refused on the lock notes that line and still merges: another
// install holds the binary, and the next landing tries again.
func TestMergeLocalUpgradeLockRefusedStillMerges(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	y.upgradeFrom("r")
	bin := t.TempDir()
	y.bin = yardrCopy(t, bin)
	y.opts = "PATH=" + shellQuote(filepath.Dir(fakeGo(t, bin))) + ":$PATH"
	lock, err := os.OpenFile(filepath.Join(bin, "yardr.upgrade.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	for _, want := range []string{"; install failed: another yardr upgrade holds ", "(pid " + strconv.Itoa(os.Getpid()) + " holds ", "the next landing tries again"} {
		if !strings.Contains(y.notes(), want) {
			t.Fatalf("the note lacks %q:\n%s", want, y.notes())
		}
	}
	if !regexp.MustCompile(`(?m)^== r-1 took: the tests \d+ s, upgrade \d+ s$`).MatchString(y.log) {
		t.Fatalf("the log line has no upgrade seconds:\n%s", y.log)
	}
}

// When the install leaves the yard dead, no advance is possible: the bead
// is not merged, and the session fails with the install's lines and the
// refused calls in its log, beside the marker the install wrote.
func TestMergeLocalDeadYardFailsWithoutAdvance(t *testing.T) {
	y := newYardServer(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")
	y.upgradeFrom("r")
	dir := t.TempDir()
	flag := filepath.Join(dir, "dead")
	y.bin = writeScript(t, dir, "yardr", `#!/bin/sh
REAL=`+shellQuote(yardrBin)+`
FLAG=`+shellQuote(flag)+`
if [ "$1" = "upgrade" ] && [ "$2" != "--help" ]; then
  "$REAL" server stop >/dev/null 2>&1
  : > "$FLAG"
  printf '%s\n' "backup    `+y.home+`/backups/yard.db.before-install"
  printf '%s\n' "keep      `+dir+`/yardr.prev (1904410)"
  printf '%s\n' "install   go install . at deadbeefcd12"
  echo "yardr: yardr upgrade to deadbeefcd12 failed and the yard is down: simulated for the assembly test" >&2
  exit 1
fi
if [ -e "$FLAG" ]; then
  echo "yardr: the server cannot be reached: simulated dead yard" >&2
  exit 1
fi
exec "$REAL" "$@"
`)

	y.assembly()

	// The reads below go over rpc, so the server the upgrade stopped is
	// started again first; before, the show and prime commands did that by
	// connecting. Starting it moves nothing and notes nothing.
	y.yardr("server", "start")

	if b := y.bead(); b.Stage != "approved" || b.Status != "open" {
		t.Fatalf("bead %s/%s, want still approved and open: nothing may advance a dead yard", b.Stage, b.Status)
	}
	if strings.Contains(y.notes(), "install") {
		t.Fatalf("a landing note was written over a dead yard:\n%s", y.notes())
	}
	for _, want := range []string{"install   go install . at deadbeefcd12", "the server cannot be reached: simulated dead yard"} {
		if !strings.Contains(y.log, want) {
			t.Fatalf("the log lacks %q:\n%s", want, y.log)
		}
	}
}
