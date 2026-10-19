package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/storetest"
)

// find is the command of root that words name, nil when there is none.
func find(root *cobra.Command, words ...string) *cobra.Command {
	c, rest, err := root.Find(words)
	if err != nil || len(rest) != 0 {
		return nil
	}
	return c
}

// Every long-running command is a whole row of the table, and has the same
// verbs beside the command that runs it: start, stop, status, install and
// uninstall, with the flags the row hands on.
func TestLongRunningTable(t *testing.T) {
	t.Parallel()
	var names, files []string
	root := (&app{st: storetest.New(t), actor: "me"}).rootCmd()
	cfg := &config.Config{Home: "/h"}
	for _, k := range longRunnings {
		names = append(names, k.Name)
		if k.What == "" || k.Log == nil || k.Env == nil || (k.Lock == nil) == (k.Records == nil) {
			t.Errorf("%s: the row is not whole: %+v", k.Name, k)
			continue
		}
		files = append(files, k.Log(cfg))
		if k.Lock != nil {
			files = append(files, k.Lock(cfg))
		} else {
			files = append(files, k.Records(cfg))
		}
		run := find(root, k.command()...)
		if run == nil || run.RunE == nil {
			t.Errorf("yardr %s is no command that runs", strings.Join(k.command(), " "))
			continue
		}
		if k.Addr != "" && !slices.Contains(k.Flags, k.Addr) {
			t.Errorf("%s: --%s is not handed on", k.Name, k.Addr)
		}
		for _, verb := range []string{"start", "stop", "status", "install", "uninstall"} {
			c := find(root, k.Name, verb)
			if c == nil {
				t.Errorf("yardr %s %s is missing", k.Name, verb)
				continue
			}
			for _, f := range k.Flags {
				if has := c.Flags().Lookup(f) != nil; has != (verb == "start" || verb == "install") {
					t.Errorf("yardr %s %s: --%s there: %v", k.Name, verb, f, has)
				}
			}
		}
		for _, f := range k.Flags {
			if run.Flags().Lookup(f) == nil {
				t.Errorf("yardr %s has no --%s", strings.Join(k.command(), " "), f)
			}
		}
	}
	if want := []string{svcDaemon, svcWeb, svcBDP, svcOTel}; !slices.Equal(names, want) {
		t.Fatalf("kinds = %v, want %v", names, want)
	}
	// No two kinds log to one file or record themselves in one place.
	slices.Sort(files)
	if c := slices.Compact(slices.Clone(files)); len(c) != len(files) {
		t.Fatalf("a file of the home is two kinds': %v", files)
	}
}

// running records the test itself as a running one of the kind, and makes
// a stop signal end it. It returns the pids signalled.
func running(t *testing.T, cfg *config.Config, k *longRunning, fields map[string]any) *[]int {
	t.Helper()
	clear, err := k.announce(cfg, fields)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clear)
	var pids []int
	stub(t, &signalStop, func(pid int) error {
		pids = append(pids, pid)
		clear()
		return nil
	})
	return &pids
}

// yardr web stop stops the view the record under <home>/webviews names and
// says so, with none running it says that, and a start after it answers at
// the same address.
func TestWebStop(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "web", "stop")
	if err != nil || !strings.Contains(out, "no web view is running on the yard in "+home) {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg := &config.Config{Home: home}
	pids := running(t, cfg, kindOf(svcWeb), map[string]any{"addr": "127.0.0.1:8791", "unsafe": false})
	if v := webViews(cfg); len(v) != 1 || v[0].Addr != "127.0.0.1:8791" {
		t.Fatalf("views = %+v", v)
	}
	out, err = run(t, a().rootCmd(), "web", "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "web      running (pid "+strconv.Itoa(os.Getpid())+") at http://127.0.0.1:8791/\n", "service  "+here("not installed (yardr web install)"))
	out, err = run(t, a().rootCmd(), "web", "stop")
	if err != nil || !slices.Equal(*pids, []int{os.Getpid()}) {
		t.Fatalf("%v, signalled %v\n%s", err, *pids, out)
	}
	wantText(t, out, "web view stopped (pid "+strconv.Itoa(os.Getpid())+") at http://127.0.0.1:8791/\n", "Start one again: yardr --home "+home+" web start")
	if v := webViews(cfg); len(v) != 0 {
		t.Fatalf("still running: %+v", v)
	}
	started := stubWeb(t)
	out, err = run(t, a().rootCmd(), "web", "start")
	if err != nil || !slices.Equal(*started, []string{"127.0.0.1:8791"}) {
		t.Fatalf("%v, started %v", err, *started)
	}
	wantText(t, out, "The web view: http://127.0.0.1:8791/")
}

// sleeper starts a process that sleeps until it is signalled, and is reaped
// when the test ends.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it when it exits, so nothing is left for the run's stray check.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd
}

// holdLockWith holds an exclusive flock on path with content, as a daemon
// holds its lock, and releases it when the test ends.
func holdLockWith(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// A lock file whose pid another process has reused must not get that
// process signalled: the daemon's stop signals nothing (yardr-yz19).
func TestDaemonStopLeavesAReusedPIDAlone(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("the OS reports no process start times")
	}
	cfg := &config.Config{Home: newHome(t)}
	sleep := sleeper(t)
	pid := sleep.Process.Pid
	real, err := peer.Describe(pid)
	if err != nil {
		t.Fatal(err)
	}
	holdLockWith(t, cfg.DaemonLock(), strconv.Itoa(pid)+" "+strconv.FormatUint(real.Start+1, 10)+"\n")
	var signalled []int
	stub(t, &signalStop, func(pid int) error {
		signalled = append(signalled, pid)
		return nil
	})
	if stopped, err := kindOf(svcDaemon).stop(cfg, 0); err != nil || len(stopped) != 0 {
		t.Fatalf("stop = %+v, %v; want the reused pid reported gone", stopped, err)
	}
	if len(signalled) != 0 {
		t.Fatalf("stop signalled %v", signalled)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("stop killed the decoy: %v", err)
	}
}

// A lock file from before starts were recorded names a daemon a stop
// cannot verify: it is refused, not signalled.
func TestDaemonStopRefusesAPidItCannotVerify(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("without start times the pid is trusted, as before")
	}
	cfg := &config.Config{Home: newHome(t)}
	sleep := sleeper(t)
	pid := sleep.Process.Pid
	holdLockWith(t, cfg.DaemonLock(), strconv.Itoa(pid)+"\n")
	var signalled []int
	stub(t, &signalStop, func(pid int) error {
		signalled = append(signalled, pid)
		return nil
	})
	if _, err := kindOf(svcDaemon).stop(cfg, 0); err == nil {
		t.Fatal("stop passed a pid it could not verify")
	}
	if len(signalled) != 0 {
		t.Fatalf("stop signalled %v", signalled)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("stop killed the decoy: %v", err)
	}
}

// A record whose pid another process has reused must not get that
// process signalled: the kind's stop signals nothing (yardr-vhs2).
func TestRecordStopLeavesAReusedPIDAlone(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("the OS reports no process start times")
	}
	cfg := &config.Config{Home: newHome(t)}
	k := kindOf(svcWeb)
	sleep := sleeper(t)
	pid := sleep.Process.Pid
	real, err := peer.Describe(pid)
	if err != nil {
		t.Fatal(err)
	}
	holdLockWith(t, filepath.Join(k.Records(cfg), "web-test.json"),
		fmt.Sprintf("{\"pid\":%d,\"start\":%d,\"addr\":\"127.0.0.1:8791\"}\n", pid, real.Start+1))
	var signalled []int
	stub(t, &signalStop, func(pid int) error {
		signalled = append(signalled, pid)
		return nil
	})
	if stopped, err := k.stop(cfg, 0); err != nil || len(stopped) != 0 {
		t.Fatalf("stop = %+v, %v; want the reused pid reported gone", stopped, err)
	}
	if len(signalled) != 0 {
		t.Fatalf("stop signalled %v", signalled)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("stop killed the decoy: %v", err)
	}
}

// A record from before starts were recorded names a holder a stop
// cannot verify: it is refused, not signalled.
func TestRecordStopRefusesAPidItCannotVerify(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("without start times the pid is trusted, as before")
	}
	cfg := &config.Config{Home: newHome(t)}
	k := kindOf(svcBDP)
	sleep := sleeper(t)
	pid := sleep.Process.Pid
	holdLockWith(t, filepath.Join(k.Records(cfg), "bdp-test.json"),
		fmt.Sprintf("{\"pid\":%d,\"addr\":\"127.0.0.1:8765\"}\n", pid))
	var signalled []int
	stub(t, &signalStop, func(pid int) error {
		signalled = append(signalled, pid)
		return nil
	})
	if _, err := k.stop(cfg, 0); err == nil {
		t.Fatal("stop passed a pid it could not verify")
	}
	if len(signalled) != 0 {
		t.Fatalf("stop signalled %v", signalled)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("stop killed the decoy: %v", err)
	}
}

// A stop whose signal finds the pid gone ends it there: the process died on
// its own between the check and the signal. One refused with EPERM reached
// nothing of ours either.
func TestStopSkipsAPidGoneBeforeItsSignal(t *testing.T) {
	cfg := &config.Config{Home: newHome(t)}
	k := kindOf(svcBDP)
	clear, err := k.announce(cfg, map[string]any{"addr": "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer clear()
	for _, gone := range []error{syscall.ESRCH, syscall.EPERM} {
		stub(t, &signalStop, func(int) error { return gone })
		if run, err := k.stop(cfg, 0); err != nil || len(run) != 0 {
			t.Fatalf("signal %v: stopped %+v, %v", gone, run, err)
		}
	}
}

// A stop waits for what it signalled, and says which one did not end.
func TestStopWaits(t *testing.T) {
	home := newHome(t)
	cfg := &config.Config{Home: home}
	k := kindOf(svcBDP)
	clear, err := k.announce(cfg, map[string]any{"addr": "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer clear()
	stub(t, &signalStop, func(int) error { return nil })
	if _, err := k.stop(cfg, 0); err == nil || !strings.Contains(err.Error(), "was told to stop and still holds "+cfg.BDPServers()) {
		t.Fatalf("err = %v", err)
	}
	stub(t, &signalStop, func(int) error { return errors.New("no such process") })
	if _, err := k.stop(cfg, 0); err == nil || !strings.Contains(err.Error(), "stop the BDP server (pid ") {
		t.Fatalf("err = %v", err)
	}
	// A record nobody holds is no running one: nothing is signalled.
	clear()
	if err := os.WriteFile(filepath.Join(cfg.BDPServers(), "bdp-gone.json"), []byte(`{"pid":1,"addr":"127.0.0.1:8765"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if run, err := k.stop(cfg, 0); err != nil || len(run) != 0 {
		t.Fatalf("stopped %+v, %v", run, err)
	}
}

// yardr bdp and yardr otel start and stop as the web view does: a start
// hands the flags given on to the command it runs, a server is up when it
// listens and an export when it holds its record, and a stop ends each.
func TestBDPAndOTelStartStop(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home}
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(name, "")
	}
	var started [][]string
	var pids *[]int
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, append(k.command(), args...))
		// What is started says that it runs, as the real one does.
		pids = running(t, cfg, k, nil)
		return os.Getpid(), nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(addr string) bool { return len(started) > 0 && slices.Contains(started[len(started)-1], addr) })

	out, err := run(t, a().rootCmd(), "bdp", "start", "--scope", "https://beads.example/team/")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bdp", "serve", "--addr", "127.0.0.1:8765", "--scope", "https://beads.example/team/"}; !slices.Equal(started[0], want) {
		t.Fatalf("started %q", started[0])
	}
	wantText(t, out, "The BDP server: http://127.0.0.1:8765/", "logs to "+home+"/bdp.log", "To stop it: yardr --home "+home+" bdp stop")
	out, err = run(t, a().rootCmd(), "bdp", "stop")
	if err != nil || len(*pids) != 1 || !strings.Contains(out, "BDP server stopped (pid ") {
		t.Fatalf("%v, signalled %v\n%s", err, *pids, out)
	}

	// An export with nowhere to send to is not started.
	if _, err := run(t, a().rootCmd(), "otel", "start"); err == nil || !strings.Contains(err.Error(), "no collector") || len(started) != 1 {
		t.Fatalf("err = %v, started %q", err, started)
	}
	out, err = run(t, a().rootCmd(), "otel", "start", "--endpoint", "http://127.0.0.1:4318", "--header", "a=1", "--header", "b=2", "--include-text")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"otel", "export", "--endpoint", "http://127.0.0.1:4318", "--header", "a=1", "--header", "b=2", "--include-text=true"}; !slices.Equal(started[1], want) {
		t.Fatalf("started %q", started[1])
	}
	wantText(t, out, "The telemetry export runs in the background (pid ", home+"/otel.log")
	// One export at a time: a second would send every event again.
	if _, err := run(t, a().rootCmd(), "otel", "start", "--endpoint", "http://127.0.0.1:4318"); err == nil ||
		!strings.Contains(err.Error(), "a telemetry export runs on the yard in "+home+" already") || len(started) != 2 {
		t.Fatalf("err = %v, started %q", err, started)
	}
	out, err = run(t, a().rootCmd(), "otel", "stop")
	if err != nil || len(*pids) != 1 || !strings.Contains(out, "telemetry export stopped (pid ") {
		t.Fatalf("%v, signalled %v\n%s", err, *pids, out)
	}
	out, err = run(t, a().rootCmd(), "otel", "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "otel     not running\n")

	// The collector may come from the environment, which a started export
	// gets with its headers, and nothing else of the shell.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	if err := otelCollector(find(a().rootCmd(), "otel", "start").Flags()); err != nil {
		t.Fatal(err)
	}
	env := kindOf(svcOTel).env([]string{"PATH=/bin", "OTEL_EXPORTER_OTLP_HEADERS=k=v", "GITHUB_TOKEN=secret"}, home)
	if !slices.Contains(env, "OTEL_EXPORTER_OTLP_HEADERS=k=v") || slices.Contains(env, "GITHUB_TOKEN=secret") {
		t.Fatalf("env = %q", env)
	}
}

// yardr otel install writes a service that others cannot read: its
// environment may hold the collector's credentials.
func TestOTelInstall(t *testing.T) {
	home := newHome(t)
	dir, _ := stubServices(t, "darwin")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer t0ken")
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a().rootCmd(), "otel", "install", "--endpoint", "http://127.0.0.1:4318"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "dev.yardr.otel."+homeID(home)+".plist")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plutilLint(t, string(body))
	wantText(t, string(body), "<string>otel</string>\n\t\t<string>export</string>\n\t\t<string>--endpoint</string>\n\t\t<string>http://127.0.0.1:4318</string>",
		"<key>OTEL_EXPORTER_OTLP_HEADERS</key>\n\t\t<string>Authorization=Bearer t0ken</string>", "<string>"+home+"/otel.log</string>")
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if _, err := run(t, a().rootCmd(), "otel", "uninstall"); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}
