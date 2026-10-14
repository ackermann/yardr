package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The install target is computed as go install does: GOBIN, else the first
// GOPATH's bin, compared with the running binary's path.
func TestUpgradeInstallTarget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin", "yardr")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub(t, &upgradeExecutable, func() (string, error) { return bin, nil })
	stub(t, &upgradeGoEnv, func(names ...string) (string, error) {
		if names[0] == "GOBIN" {
			return filepath.Dir(bin), nil
		}
		return "", nil
	})
	got, err := upgradeInstallTarget()
	if err != nil || got != filepath.Join(filepath.Dir(bin), "yardr") {
		t.Fatalf("target = %q, %v", got, err)
	}
	// Else the first GOPATH's bin.
	stub(t, &upgradeGoEnv, func(names ...string) (string, error) {
		if names[0] == "GOBIN" {
			return "", nil
		}
		return filepath.Dir(filepath.Dir(bin)) + string(os.PathListSeparator) + "/other", nil
	})
	// GOPATH is the parent of bin's parent? Make it match: GOPATH/bin must
	// be bin's dir.
	gopath := filepath.Dir(filepath.Dir(bin))
	stub(t, &upgradeGoEnv, func(names ...string) (string, error) {
		if names[0] == "GOBIN" {
			return "", nil
		}
		return gopath, nil
	})
	if _, err := upgradeInstallTarget(); err != nil {
		t.Fatalf("GOPATH target: %v", err)
	}
	// Refusing when they differ.
	stub(t, &upgradeGoEnv, func(names ...string) (string, error) { return t.TempDir(), nil })
	if _, err := upgradeInstallTarget(); err == nil || !strings.Contains(err.Error(), "would write") {
		t.Fatalf("differing target: %v, want a refusal", err)
	}
}

// Covered yards are the upgrading yard plus the login services whose
// program is the install target; a yard on another binary is left out.
func TestUpgradeCoveredYards(t *testing.T) {
	home := newHome(t)
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
	for _, timeout := range []time.Duration{0, -time.Second} {
		err := (&app{}).upgradeRun(context.Background(), config.At(t.TempDir()), timeout, false)
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

// A second upgrade refuses on the lock beside the binary, naming the
// holder's pid, as a second daemon does.
func TestUpgradeLockRefusesSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yardr.upgrade.lock")
	unlock, err := lockDaemon(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lockDaemon(path, 0); err == nil || !strings.Contains(err.Error(), "already running") || !strings.Contains(err.Error(), "pid ") {
		t.Fatalf("second lock = %v, want the holder named", err)
	}
}

// Doctor faults on the marker with the server down, and is quiet without one.
func TestDoctorUpgradeFailed(t *testing.T) {
	home := newHome(t)
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
		upgradeLogMarked(ctx, st, home, "yard")
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
	home := t.TempDir()
	backup := config.At(home).BeforeInstallBackup()
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	var sent []string
	stub(t, &upgradeNotifySend, func(_ context.Context, _ notify.Store, user string, _ notify.Message) (*notify.Sent, error) {
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

// The beads changed in the window are the distinct beads of the events
// since the recorded head.
func TestUpgradeBeadsSince(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := upgradeBeadsSince(ctx, st, head); len(got) != 0 {
		t.Fatalf("empty window = %v", got)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "one"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, b.ID, "a note", "me"); err != nil {
		t.Fatal(err)
	}
	got := upgradeBeadsSince(ctx, st, head)
	if len(got) != 1 {
		t.Fatalf("window = %v, want the one bead", got)
	}
	_ = b
}
