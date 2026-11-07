package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// realTempDir is a temp dir by the path the OS resolves it to: on macOS the
// temp dir is itself a link, and the install target is a resolved path.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// mustStat is the file's info, or fails the test.
func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// The install target is the binary this yard runs from, symlinks resolved,
// wherever it sits: go's GOBIN and GOPATH are no part of it.
func TestUpgradeInstallTarget(t *testing.T) {
	dir := realTempDir(t)
	bin := filepath.Join(dir, "local", "bin", "yardr")
	link := filepath.Join(dir, "links", "yardr")
	for _, d := range []string{filepath.Dir(bin), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	// Go's own places are elsewhere, as for a yardr put where its person
	// chose.
	t.Setenv("GOBIN", filepath.Join(dir, "gobin"))
	t.Setenv("GOPATH", filepath.Join(dir, "gopath"))
	var running string
	stub(t, &upgradeExecutable, func() (string, error) { return running, nil })
	for _, path := range []string{bin, link} {
		running = path
		if got, err := upgradeInstallTarget(); err != nil || got != bin {
			t.Fatalf("running %s: target = %q, %v, want %q", path, got, err, bin)
		}
	}
}

// The install builds beside the binary and renames the build over it, so a
// watcher of the path sees one complete new file: the binary is replaced where
// it runs from, a link to it stays, and a failed build leaves the old file
// whole with nothing beside it.
func TestUpgradeInstall(t *testing.T) {
	ctx := context.Background()
	// The stand-in build's output is not executable, as the temp file it is
	// asked to write is not: the install gives it the mode of the file it
	// replaces.
	builds := func(_ context.Context, _, file string) (string, error) {
		return "", os.WriteFile(file, []byte("new build"), 0o600)
	}
	binary := func(t *testing.T, dir string) string {
		t.Helper()
		path := filepath.Join(dir, "yardr")
		if err := os.WriteFile(path, []byte("old build"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("replaces the binary where it is", func(t *testing.T) {
		dir := realTempDir(t)
		target := binary(t, dir)
		before := mustStat(t, target)
		stub(t, &upgradeGoBuild, builds)

		if out, err := upgradeInstall(ctx, "checkout", target); err != nil || out != "" {
			t.Fatalf("install = %q, %v", out, err)
		}

		after := mustStat(t, target)
		if got := read(t, target); got != "new build" {
			t.Fatalf("the binary holds %q, want the build", got)
		}
		if after.Mode() != before.Mode() {
			t.Fatalf("mode = %v, want the replaced file's %v", after.Mode(), before.Mode())
		}
		if os.SameFile(before, after) {
			t.Fatalf("the build was written into the old file, not renamed over it")
		}
		if got := dirNames(dir); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the binary: %v, want it alone", got)
		}
	})

	t.Run("a link to the binary stays", func(t *testing.T) {
		dir := realTempDir(t)
		kept, bin := filepath.Join(dir, "kept"), filepath.Join(dir, "bin")
		for _, d := range []string{kept, bin} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		file := binary(t, kept)
		link := filepath.Join(bin, "yardr")
		if err := os.Symlink(file, link); err != nil {
			t.Fatal(err)
		}
		stub(t, &upgradeExecutable, func() (string, error) { return link, nil })
		stub(t, &upgradeGoBuild, builds)
		target, err := upgradeInstallTarget()
		if err != nil || target != file {
			t.Fatalf("target = %q, %v, want the file the link names, %s", target, err, file)
		}

		if out, err := upgradeInstall(ctx, "checkout", target); err != nil || out != "" {
			t.Fatalf("install = %q, %v", out, err)
		}

		if dest, err := os.Readlink(link); err != nil || dest != file {
			t.Fatalf("the link reads %q, %v, want it left naming %s", dest, err, file)
		}
		if got := read(t, link); got != "new build" {
			t.Fatalf("the file behind the link holds %q, want the build", got)
		}
		for _, d := range []string{kept, bin} {
			if got := dirNames(d); !slices.Equal(got, []string{"yardr"}) {
				t.Fatalf("in %s: %v, want the binary alone", d, got)
			}
		}
	})

	t.Run("a failed build leaves the old binary whole", func(t *testing.T) {
		dir := realTempDir(t)
		target := binary(t, dir)
		before := mustStat(t, target)
		stub(t, &upgradeGoBuild, func(_ context.Context, _, file string) (string, error) {
			// The link got as far as part of a file.
			if err := os.WriteFile(file, []byte("half a bui"), 0o600); err != nil {
				t.Error(err)
			}
			return "link: out of memory\n", errors.New("exit status 1")
		})

		out, err := upgradeInstall(ctx, "checkout", target)

		if err == nil || !strings.Contains(err.Error(), "go build . in checkout failed: exit status 1") {
			t.Fatalf("install = %v, want the build's failure named", err)
		}
		if out != "link: out of memory\n" {
			t.Fatalf("output = %q, want the build's", out)
		}
		if !os.SameFile(before, mustStat(t, target)) || read(t, target) != "old build" {
			t.Fatalf("the old binary changed: %q", read(t, target))
		}
		if got := dirNames(dir); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the binary: %v, want it alone", got)
		}
	})

	// The real go, on a module of its own: a binary outside GOBIN and GOPATH
	// is built over where it is, and neither is written to.
	t.Run("go build over a binary outside GOBIN and GOPATH", func(t *testing.T) {
		dir := realTempDir(t)
		src, bin := filepath.Join(dir, "src"), filepath.Join(dir, "bin")
		gobin, gopath := filepath.Join(dir, "gobin"), filepath.Join(dir, "gopath")
		for _, d := range []string{src, bin, gobin, gopath} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range map[string]string{
			"go.mod":  "module example.com/hello\n\ngo 1.21\n",
			"main.go": "package main\n\nimport \"os\"\n\nfunc main() { os.Stdout.WriteString(\"new build\\n\") }\n",
		} {
			if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		target := binary(t, bin)
		t.Setenv("GOBIN", gobin)
		t.Setenv("GOPATH", gopath)
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		if out, err := upgradeInstall(ctx, src, target); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}

		got, err := exec.CommandContext(ctx, target).Output()
		if err != nil || string(got) != "new build\n" {
			t.Fatalf("the binary prints %q, %v, want the module's build", got, err)
		}
		if got := dirNames(gobin); len(got) != 0 {
			t.Fatalf("GOBIN holds %v, want nothing installed", got)
		}
		if _, err := os.Stat(filepath.Join(gopath, "bin")); err == nil {
			t.Fatalf("GOPATH has a bin, want nothing installed")
		}
		if got := dirNames(bin); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the binary: %v, want it alone", got)
		}
	})

	// A checkout whose root package is a library: go build alone would write
	// the package's archive and exit 0.
	t.Run("go build of a checkout that is no program", func(t *testing.T) {
		dir := realTempDir(t)
		src, bin := filepath.Join(dir, "src"), filepath.Join(dir, "bin")
		for _, d := range []string{src, bin} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range map[string]string{
			"go.mod": "module example.com/lib\n\ngo 1.21\n",
			"lib.go": "package lib\n\nfunc F() int { return 1 }\n",
		} {
			if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		target := binary(t, bin)
		before := mustStat(t, target)
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		out, err := upgradeInstall(ctx, src, target)

		if err == nil || !strings.Contains(out, "requires exactly one main package") {
			t.Fatalf("install = %v, output %q, want go's refusal of a build with no program", err, out)
		}
		if !os.SameFile(before, mustStat(t, target)) || read(t, target) != "old build" {
			t.Fatalf("the old binary changed: %q", read(t, target))
		}
		if got := dirNames(bin); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the binary: %v, want it alone", got)
		}
	})
}

// Through the whole run: the binary is replaced where it runs from, outside
// GOBIN and GOPATH, and the install line names it; a link to it stays and the
// file it names is replaced. A build that fails fails the run at the build,
// with the old binary whole, nothing hidden beside it and no gate file left.
// The lock beside the binary comes first: a run that cannot take it writes
// nothing, and says whether another upgrade holds it or the directory is not
// writable, the one a landing finds gone and the other it does not.
func TestUpgradeRunInstall(t *testing.T) {
	// noTemp fails when a hidden file stands beside the binary: the temp
	// file of an install.
	noTemp := func(t *testing.T, target string) {
		t.Helper()
		for _, name := range dirNames(filepath.Dir(target)) {
			if strings.HasPrefix(name, ".") {
				t.Fatalf("%s stands beside the binary", name)
			}
		}
	}

	t.Run("outside GOBIN and GOPATH", func(t *testing.T) {
		sc := newUpgradeScene(t)
		t.Setenv("GOBIN", t.TempDir())
		t.Setenv("GOPATH", t.TempDir())
		before := mustStat(t, sc.target)

		out, err := sc.upgrade(t)

		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, out)
		}
		if want := fmt.Sprintf("%-9s  %s at ", "install", sc.target); !strings.Contains(out, want) {
			t.Fatalf("the output lacks %q:\n%s", want, out)
		}
		if os.SameFile(before, mustStat(t, sc.target)) {
			t.Fatalf("the binary was written in place, not renamed over")
		}
		noTemp(t, sc.target)
	})

	// The file the link names is replaced, and what the run keeps and locks
	// by sits beside that file, not beside the link.
	t.Run("a symlinked binary", func(t *testing.T) {
		sc := newUpgradeScene(t)
		links := realTempDir(t)
		link := filepath.Join(links, "yardr")
		if err := os.Symlink(sc.target, link); err != nil {
			t.Fatal(err)
		}
		stub(t, &upgradeExecutable, func() (string, error) { return link, nil })
		before := mustStat(t, sc.target)

		out, err := sc.upgrade(t)

		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, out)
		}
		if dest, err := os.Readlink(link); err != nil || dest != sc.target {
			t.Fatalf("the link reads %q, %v, want it left naming %s", dest, err, sc.target)
		}
		if os.SameFile(before, mustStat(t, sc.target)) {
			t.Fatalf("the file the link names was not replaced")
		}
		if got := dirNames(links); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the link: %v, want it alone", got)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(sc.target), "yardr.prev")); err != nil {
			t.Fatalf("no yardr.prev beside the file the link names: %v", err)
		}
	})

	t.Run("a failed build", func(t *testing.T) {
		sc := newUpgradeScene(t)
		stub(t, &upgradeGoBuild, func(_ context.Context, _, file string) (string, error) {
			if err := os.WriteFile(file, []byte("half a bui"), 0o600); err != nil {
				t.Error(err)
			}
			return "link: out of memory\n", errors.New("exit status 1")
		})
		before := mustStat(t, sc.target)

		out, err := sc.upgrade(t)

		for _, want := range []string{"go build . in ", " failed: exit status 1", "link: out of memory"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("upgrade = %v, want %q in the reason\n%s", err, want, out)
			}
		}
		after := mustStat(t, sc.target)
		if !os.SameFile(before, after) || after.Size() != before.Size() {
			t.Fatalf("the old binary changed")
		}
		noTemp(t, sc.target)
		if _, err := os.Stat(config.At(sc.home).UpgradeGate()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the gate file stands after a failed build: %v", err)
		}
		evs, err := sc.st.EventsSince(context.Background(), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var told *store.Event
		for i, e := range evs {
			if e.Kind == store.InstallFailed {
				told = &evs[i]
			}
		}
		if told == nil || told.Data["stage"] != "build" {
			t.Fatalf("no install_failed at stage build in %+v", evs)
		}
	})

	// A directory the person upgrading cannot write holds no lock for anyone:
	// the lock file cannot be made, so the run says the directory is not
	// writable, in the terminal, the event and the mail alike, and promises no
	// landing that will find it free.
	t.Run("a directory it cannot write", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a directory made read-only")
		}
		sc := newUpgradeScene(t)
		dir := filepath.Dir(sc.target)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		before := mustStat(t, sc.target)

		reason, mail, err := refusedAtLock(t, sc)

		want := "the directory beside the binary, " + dir + ", is not writable: open " + filepath.Join(dir, "yardr.upgrade.lock") + ": permission denied"
		if err == nil || err.Error() != want {
			t.Fatalf("upgrade = %v, want %q", err, want)
		}
		if reason != want || mail != "yardr upgrade refused: "+want {
			t.Fatalf("the yard was told %q and %q, want %q in both", reason, mail, want)
		}
		for _, text := range []string{err.Error(), reason, mail} {
			if strings.Contains(text, "holds") || strings.Contains(text, "next landing") {
				t.Fatalf("%q tells a lock someone holds, or one the next landing finds free", text)
			}
		}
		// The lock is taken before the backups, the kept copy and the build,
		// so no probe of the directory is needed to fail first.
		if !os.SameFile(before, mustStat(t, sc.target)) {
			t.Fatalf("the binary was replaced")
		}
		if got := dirNames(dir); !slices.Equal(got, []string{"yardr"}) {
			t.Fatalf("beside the binary: %v, want it alone", got)
		}
		if _, err := os.Stat(config.At(sc.home).BeforeInstallBackup()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a pre-install backup was taken: %v", err)
		}
	})

	// A lock another upgrade holds is told as ever: whose it is, and that the
	// next landing tries again.
	t.Run("a lock another upgrade holds", func(t *testing.T) {
		sc := newUpgradeScene(t)
		lock := filepath.Join(filepath.Dir(sc.target), "yardr.upgrade.lock")
		unlock, err := lockDaemon(lock, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()

		reason, mail, err := refusedAtLock(t, sc)

		held := fmt.Sprintf("yardr daemon already running (pid %d holds %s)", os.Getpid(), lock)
		want := "another yardr upgrade holds " + lock + ": " + held + "; the next landing tries again"
		if err == nil || err.Error() != want {
			t.Fatalf("upgrade = %v, want %q", err, want)
		}
		if reason != held || mail != "yardr upgrade refused: "+held {
			t.Fatalf("the yard was told %q and %q, want %q in both", reason, mail, held)
		}
		for _, text := range []string{err.Error(), reason, mail} {
			if strings.Contains(text, "not writable") {
				t.Fatalf("%q tells a held lock as a directory that cannot be written", text)
			}
		}
	})
}

// refusedAtLock runs the scene's upgrade, which must end at the lock, and
// returns what it told the yard besides the error it ends with: the reason
// of the one install_failed event, at stage lock, and the text of the one
// mail to the yardmaster.
func refusedAtLock(t *testing.T, sc *upgradeScene) (reason, mail string, err error) {
	t.Helper()
	ctx := context.Background()
	if _, err := sc.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}

	_, err = sc.upgrade(t)

	evs, eerr := sc.st.EventsSince(ctx, 0, 0)
	if eerr != nil {
		t.Fatal(eerr)
	}
	var told []store.Event
	for _, e := range evs {
		if e.Kind == store.InstallFailed {
			told = append(told, e)
		}
	}
	if len(told) != 1 || told[0].Data["stage"] != "lock" {
		t.Fatalf("install_failed events are %+v, want one at stage lock", told)
	}
	reason, _ = told[0].Data["reason"].(string)
	page, ierr := sc.st.Inbox(ctx, "yardmaster", 10)
	if ierr != nil {
		t.Fatal(ierr)
	}
	var mails []store.Event
	for _, e := range page.Events {
		if e.Kind == "mail" {
			mails = append(mails, e)
		}
	}
	if len(mails) != 1 {
		t.Fatalf("the yardmaster's inbox holds %+v, want the one mail", page.Events)
	}
	mail, _ = mails[0].Data["text"].(string)
	return reason, mail, err
}

// Covered yards are the upgrading yard plus the login services whose
// program is the install target; a yard on another binary is left out.
func TestUpgradeCoveredYards(t *testing.T) {
	home := newHomeDir(t)
	target := filepath.Join(t.TempDir(), "yardr")
	other := filepath.Join(t.TempDir(), "other-yard")
	third := filepath.Join(t.TempDir(), "third-yard")
	svc := t.TempDir()
	stub(t, &serviceDir, func() (string, error) { return svc, nil })
	stub(t, &goos, "linux")
	writeUnit := func(name, wd, prog string) {
		body := fmt.Sprintf("[Service]\nExecStart=%s daemon\nWorkingDirectory=%s\n", prog, wd)
		if err := os.WriteFile(filepath.Join(svc, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeUnit("yardr-daemon-aaa.service", other, target)
	writeUnit("yardr-daemon-bbb.service", third, "/other/bin/yardr")
	got := upgradeCoveredYards(config.At(home), target)
	if len(got) != 2 || got[0] != home || got[1] != other {
		t.Fatalf("covered = %v, want [%s %s]", got, home, other)
	}
	// A plist names the program in ProgramArguments.
	stub(t, &goos, "darwin")
	os.RemoveAll(svc)
	os.MkdirAll(svc, 0o755)
	plist := fmt.Sprintf(`<plist><dict><key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>`+
		`<key>WorkingDirectory</key><string>%s</string></dict></plist>`, target, other)
	if err := os.WriteFile(filepath.Join(svc, "dev.yardr.daemon.aaa.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	got = upgradeCoveredYards(config.At(home), target)
	if len(got) != 2 || got[1] != other {
		t.Fatalf("covered from plist = %v", got)
	}
}

// Restoring is the setting, through its environment variable, with the
// flag overriding for one run; unset it is on.
func TestUpgradeRestoreOn(t *testing.T) {
	if !upgradeRestoreOn(nil, false) {
		t.Fatalf("unset restores nothing, want on")
	}
	if upgradeRestoreOn(nil, true) {
		t.Fatalf("--no-restore restores, want off")
	}
	if !upgradeRestoreOn(map[string]string{store.UpgradeRestoreSetting: "true"}, false) {
		t.Fatalf("true restores nothing")
	}
	if upgradeRestoreOn(map[string]string{store.UpgradeRestoreSetting: "false"}, false) {
		t.Fatalf("false restores")
	}
	t.Setenv("YARDR_UPGRADE_RESTORE", "false")
	if upgradeRestoreOn(map[string]string{store.UpgradeRestoreSetting: "true"}, false) {
		t.Fatalf("the variable does not override the setting")
	}
	t.Setenv("YARDR_UPGRADE_RESTORE", "true")
	if !upgradeRestoreOn(map[string]string{store.UpgradeRestoreSetting: "false"}, false) {
		t.Fatalf("the variable does not override the setting back")
	}
}

func TestUpgradeRejectsNonPositiveTimeout(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{0, -time.Second} {
		err := (&app{}).upgradeRun(io.Discard, io.Discard, context.Background(), config.At(t.TempDir()), timeout, false, "")
		if err == nil || !strings.Contains(err.Error(), "--timeout must be greater than zero") {
			t.Fatalf("timeout %s: err = %v", timeout, err)
		}
	}
}

// The gate poll runs the installed binary per yard, passing the install's
// start as --since: an old caller checks a new server through the new
// binary, never in-process.
func TestUpgradePollRunsInstalledBinary(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	stub(t, &upgradeRunInstalled, func(_ context.Context, bin, home string, args ...string) (string, error) {
		mu.Lock()
		calls = append(calls, bin+" --home "+home+" "+strings.Join(args, " "))
		mu.Unlock()
		return "", nil
	})
	since := time.Now().Format(time.RFC3339)
	res := upgradePoll(context.Background(), "/bin/new-yardr", []string{"/h1", "/h2"}, since, time.Now().Add(time.Minute))
	for _, r := range res {
		if !r.ok {
			t.Fatalf("yard %s did not pass: %+v", r.home, r)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want one per yard", calls)
	}
	for _, c := range calls {
		if !strings.Contains(c, "/bin/new-yardr") || !strings.Contains(c, "upgrade --check --since "+since) {
			t.Fatalf("call = %q, want the installed binary's --check with --since", c)
		}
	}
}

// A yard the gate cannot check fails it, named with why, and the run waits
// only to the one deadline.
func TestUpgradePollFailure(t *testing.T) {
	stub(t, &upgradeRunInstalled, func(_ context.Context, _, home string, _ ...string) (string, error) {
		return "server    fail: the server is not running", fmt.Errorf("exit 1")
	})
	deadline := time.Now().Add(10 * time.Millisecond)
	res := upgradePoll(context.Background(), "/bin/yardr", []string{"/h1"}, time.Now().Format(time.RFC3339), deadline)
	if len(res) != 1 || res[0].ok || !strings.Contains(res[0].why, "not running") {
		t.Fatalf("result = %+v, want the yard named with why", res)
	}
}

// A service restart that never brings the view back fails the install gate,
// even when the installed binary's check skips the missing view.
func TestUpgradePollRestartFailureFailsSkippedView(t *testing.T) {
	t.Parallel()
	results := upgradePollWithRestartFailures(
		[]upgradePollResult{{home: "/h", ok: true}},
		[]error{fmt.Errorf("the web view did not answer /health")},
	)
	if len(results) != 1 || results[0].ok || results[0].home != "/h" ||
		!strings.Contains(results[0].why, "web view restart failed") ||
		!strings.Contains(results[0].why, "did not answer /health") {
		t.Fatalf("result = %+v, want the skipped view's restart failure named", results)
	}
}

// Every installed-binary check is canceled by the shared deadline. A
// subprocess stuck while checking one yard must not stretch the rewind
// window beyond --timeout.
func TestUpgradePollDeadlineCancelsCheck(t *testing.T) {
	started := make(chan struct{}, 1)
	stub(t, &upgradeRunInstalled, func(ctx context.Context, _, home string, _ ...string) (string, error) {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		<-ctx.Done()
		return "", ctx.Err()
	})
	deadline := time.Now().Add(50 * time.Millisecond)
	done := make(chan []upgradePollResult, 1)
	go func() {
		done <- upgradePoll(context.Background(), "/bin/yardr", []string{"/h1", "/h2"}, "since", deadline)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the installed-binary check did not start")
	}
	select {
	case results := <-done:
		if len(results) != 2 {
			t.Fatalf("results = %+v, want one per yard", results)
		}
		for _, result := range results {
			if result.ok || !strings.Contains(result.why, "deadline") {
				t.Fatalf("result = %+v, want a deadline failure", result)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("the shared deadline did not cancel the installed-binary checks")
	}
}

// Doctor faults on the marker with the server down, and is quiet without one.
func TestDoctorUpgradeFailed(t *testing.T) {
	t.Parallel()
	home := newHomeDir(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if line, ok := upgradeFailedLine(home); ok {
		t.Fatalf("no marker faults as %+v", line)
	}
	m := config.UpgradeFailed{Kind: store.RolledBack, Revision: "1904417", Stage: "health", Reason: "daemon wedged", At: time.Now()}
	if err := config.WriteUpgradeFailed(home, m); err != nil {
		t.Fatal(err)
	}
	line, ok := upgradeFailedLine(home)
	if !ok || line.tag != tagFault || line.name != "upgrade" || !strings.Contains(line.text, "1904417") || !strings.Contains(line.text, "health") {
		t.Fatalf("line = %+v, %v", line, ok)
	}
}

// The next successful upgrade logs the marked event and clears it: told
// only where every direct send succeeded, untold otherwise.
func TestUpgradeLogMarked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := t.TempDir()
	st := storetest.New(t)
	for _, sends := range [][]config.UpgradeSend{
		{{User: "jo", Source: "pushover", Status: 200, OK: true}},
		{{User: "jo", Source: "pushover", Status: 500, OK: false}},
	} {
		m := config.UpgradeFailed{Kind: store.RolledBack, Revision: "1904417", Stage: "health",
			Reason: "daemon wedged", Yards: []config.UpgradeYardOutcome{{Home: home, Outcome: "down"}},
			Beads: []string{"yardr-1"}, Sends: sends, At: time.Now()}
		if err := config.WriteUpgradeFailed(home, m); err != nil {
			t.Fatal(err)
		}
		upgradeLogMarked(io.Discard, ctx, st, home, "yard")
		if _, err := os.Stat(config.At(home).UpgradeFailed()); !os.IsNotExist(err) {
			t.Fatalf("the marker stays: %v", err)
		}
		evs, err := st.EventsOfKind(ctx, store.RolledBack, 1)
		if err != nil || len(evs) != 1 {
			t.Fatalf("events = %+v, %v", evs, err)
		}
		_, told := evs[0].Data["told"]
		if want := len(sends) > 0 && sends[0].OK; told != want {
			t.Fatalf("told = %v, want %v (sends %+v)", told, want, sends)
		}
	}
}

// The escape hatch sends the one message to subscribed users from the
// pre-install copy, skipping the actor and the unsubscribed.
func TestUpgradeHatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	if _, err := st.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "al", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.JoinWhere([]string{store.RolledBack})}}, "jo"); err != nil {
		t.Fatal(err)
	}
	const pushoverFile = "name: pushover\nbase: https://api.pushover.net/1\ncredential: { env: PUSHOVER_TOKEN }\n" +
		"calls:\n  message:\n    kind: update\n    params: { user: { type: string }, text: { type: string } }\n" +
		"    request: { method: POST, path: /messages.json }\nnotify: { call: message, address: { param: user, env: PUSHOVER_USER }, text: text }\n"
	if _, err := st.PutSource(ctx, "pushover.yaml", pushoverFile, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "jo", map[string]string{"notify": "pushover"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// The store's own home: a backup is written in its backups alone.
	home := filepath.Dir(path)
	backup := config.At(home).BeforeInstallBackup()
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	var sent []string
	stub(t, &upgradeNotifySend, func(_ context.Context, _ notify.Store, _ string, user string, _ notify.Message) (*notify.Sent, error) {
		sent = append(sent, user)
		return &notify.Sent{User: user, Source: "pushover", Status: 200}, nil
	})
	sends := upgradeHatch(ctx, home, store.RolledBack, "yard", "1904417", "daemon wedged", nil)
	if len(sends) != 1 || sends[0].User != "jo" || !sends[0].OK {
		t.Fatalf("sends = %+v, sent %v", sends, sent)
	}
	// The actor is not told of what they did themselves.
	sends = upgradeHatch(ctx, home, store.RolledBack, "jo", "1904417", "daemon wedged", nil)
	if len(sends) != 0 {
		t.Fatalf("the actor is told: %+v", sends)
	}
}

// writeWebService writes the home's web view service file, serving on addr,
// into dir (stubServices'), as yardr web install would.
func writeWebService(t *testing.T, cfg *config.Config, dir, addr string) string {
	t.Helper()
	_, name, path, err := unitFile(cfg, svcWeb)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	u := &unit{Kind: svcWeb, Manager: managerOf(), Name: name, Path: path, Home: cfg.Home,
		Program: "/opt/yardr/bin/yardr", Args: []string{"web", "serve", "--addr", addr}}
	if err := os.WriteFile(path, []byte(u.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// The upgrade restarts the web view's login service where the manager has
// it loaded, and waits for /health: launchctl kickstart -k on macOS,
// systemctl --user restart on Linux, after asking launchctl print or
// systemctl --user is-active. A service installed but not loaded (an
// upgrade from an ssh or su session with no GUI login) keeps no view:
// nothing is restarted and nothing fails, and the check names the restart
// of the view started by hand. Nor does a kickstart that cannot reach the
// gui domain (exit 125) fail. With no service there is nothing to restart.
func TestUpgradeRestartViews(t *testing.T) {
	for _, tc := range []struct {
		name, system string
		loaded       bool
		refuse       string // the manager's answer to the restart, when it refuses it
		gone         bool   // the refusal leaves the unit no longer active
		want         []string
	}{
		{name: "loaded", system: "darwin", loaded: true, want: []string{"launchctl print gui/", "launchctl kickstart -k gui/"}},
		{name: "loaded", system: "linux", loaded: true, want: []string{"systemctl --user is-active ", "systemctl --user restart "}},
		{name: "not loaded", system: "darwin", want: []string{"launchctl print gui/"}},
		{name: "not loaded", system: "linux", want: []string{"systemctl --user is-active "}},
		{name: "gui domain unreachable", system: "darwin", loaded: true,
			refuse: "Domain does not support specified action",
			want:   []string{"launchctl print gui/", "launchctl kickstart -k gui/"}},
		{name: "gone when refused", system: "linux", loaded: true, refuse: "Unit not found.", gone: true,
			want: []string{"systemctl --user is-active ", "systemctl --user restart ", "systemctl --user is-active "}},
	} {
		t.Run(tc.system+" "+tc.name, func(t *testing.T) {
			home := newHomeDir(t)
			cfg := config.At(home)
			// A view answering /health on the service's address.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintln(w, `{"view":{"build":"new"}}`)
			}))
			t.Cleanup(srv.Close)
			dir, m := stubServices(t, tc.system)
			name := writeWebService(t, cfg, dir, srv.Listener.Addr().String())
			m.loaded[name] = tc.loaded
			var calls []string
			stub(t, &serviceCtl, func(ctx context.Context, cmd string, args ...string) (string, error) {
				calls = append(calls, strings.Join(append([]string{cmd}, args...), " "))
				if tc.refuse != "" && (slices.Contains(args, "kickstart") || slices.Contains(args, "restart")) {
					if tc.gone {
						m.loaded[name] = false
						return tc.refuse, errors.New("exit status 5")
					}
					return tc.refuse, errors.New("exit status 125")
				}
				return m.ctl(ctx, cmd, args...)
			})
			if err := upgradeRestartViews(context.Background(), cfg); err != nil {
				t.Fatalf("restart = %v", err)
			}
			if len(calls) != len(tc.want) {
				t.Fatalf("manager calls = %q, want %q", calls, tc.want)
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(calls[i], want) || !strings.HasSuffix(calls[i], name) {
					t.Fatalf("manager call %d = %q, want %s...%s", i, calls[i], want, name)
				}
			}
		})
	}
	// With no service installed, nothing is restarted and the manager is
	// not asked.
	home := newHomeDir(t)
	stubServices(t, "darwin")
	calls := 0
	stub(t, &serviceCtl, func(context.Context, string, ...string) (string, error) {
		calls++
		return "", nil
	})
	if err := upgradeRestartViews(context.Background(), config.At(home)); err != nil {
		t.Fatalf("no service: %v", err)
	}
	if calls != 0 {
		t.Fatalf("the manager was asked %d times with no service", calls)
	}
}

// A restart the manager refuses while it still has the service active
// fails, so the upgrade warns and the gate fails the view on its old build.
func TestUpgradeRestartViewsRefused(t *testing.T) {
	home := newHomeDir(t)
	cfg := config.At(home)
	dir, m := stubServices(t, "linux")
	name := writeWebService(t, cfg, dir, "127.0.0.1:8791")
	m.loaded[name] = true
	m.fail = func(line string) bool { return strings.Contains(line, " restart ") }
	if err := upgradeRestartViews(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "systemctl --user restart") {
		t.Fatalf("refused restart = %v, want the command named", err)
	}
}
