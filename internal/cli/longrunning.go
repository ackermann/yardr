package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"io"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/internal/web"
)

// Four commands of yardr run until they are stopped: the daemon, the web
// view, the BDP server and the telemetry export. Each runs in a terminal
// under its own name (yardr daemon, yardr web serve, yardr bdp serve, yardr
// otel export), and each has the same verbs beside that, from the one
// implementation in this file: start (detached, in the background), stop,
// status, and install and uninstall (a login service, service.go). What
// differs between them is a row of longRunnings and nothing else.

// The long-running commands, by the word each is under. It is the kind of
// its login service too.
const (
	svcDaemon = "daemon"
	svcWeb    = "web"
	svcBDP    = "bdp"
	svcOTel   = "otel"
)

// longRunning is one of them.
type longRunning struct {
	// Name is the command it is under, and What it is in words.
	Name string
	What string
	// Run are the words after Name that run it in a terminal: none for the
	// daemon, which is yardr daemon itself.
	Run []string
	// Flags are the flags of that command which start and install take too
	// and hand on to it, and Addr the one of them that says where it
	// listens, "" for one that listens nowhere. An address is always handed
	// on, so that a service's file says where it listens. Setting is the
	// yard's setting that the address is taken from when the flag is not
	// given (addrFromSetting).
	Flags   []string
	Addr    string
	Setting string
	// EnvAddr is the environment override, before Setting, if any.
	EnvAddr func(*config.Config) string
	// Log is the file it logs to when no terminal runs it.
	Log func(*config.Config) string
	// A running one holds a flock on a file for as long as it runs, by which
	// it is found and stopped: Lock is that file where a yard has one of
	// the kind and no more (the daemon), Records the directory of such
	// files where it may have several (see announce).
	Lock    func(*config.Config) string
	Records func(*config.Config) string
	// Env is the environment a detached one starts with, from the
	// starter's. Vars are the variables besides it that the kind reads,
	// which a start and a service's file both hand on.
	Env  func(environ []string, home string) []string
	Vars func(key string) bool
	// Check refuses what start and install must not run. It first checks
	// flags before the yard is asked for Setting, then checks the effective
	// address before anything is started or written.
	Check func(flags *pflag.FlagSet) error
	// FromSetting gives the kind's flags beyond its address their
	// environment and setting values, as addrFromSetting gives the
	// address: a start, an install and the serve itself check and hand
	// on what serve would listen on. Nil for a kind whose address is
	// its only such flag.
	FromSetting func(a *app, ctx context.Context, cfg *config.Config, flags *pflag.FlagSet) error
	// Warn is what a login service of the kind will lack, as far as it
	// shows at the install.
	Warn func(a *app, ctx context.Context, cfg *config.Config, program string) ([]string, error)
	// Without is what the yard is left with when a service's one is
	// stopped, with the yardr to run in place of %[1]s.
	Without string
}

var longRunnings = []*longRunning{
	{Name: svcDaemon, What: "daemon", Flags: []string{"interval", "max-attempts"},
		Log: (*config.Config).DaemonLog, Lock: (*config.Config).DaemonLock, Env: daemonEnv, Warn: (*app).daemonWarnings,
		Without: "The sessions it started carry on in herdr;\nnothing is dispatched until a daemon runs: %[1]s init --start, or %[1]s daemon in a pane of its own."},
	{Name: svcWeb, What: "web view", Run: []string{"serve"}, Flags: []string{"addr", "host", "as", "pages-addr", "pages-host"}, Addr: "addr", Setting: store.WebAddrSetting, EnvAddr: func(c *config.Config) string { return c.WebAddr },
		Log: (*config.Config).WebLog, Records: (*config.Config).WebViews, Env: server.ServerEnv, Check: webLoopback, FromSetting: (*app).pagesFlagsFromSetting},
	{Name: svcBDP, What: "BDP server", Run: []string{"serve"}, Flags: []string{"addr", "scope"}, Addr: "addr", Setting: store.BDPAddrSetting, EnvAddr: func(c *config.Config) string { return c.BDPAddr },
		Log: (*config.Config).BDPLog, Records: (*config.Config).BDPServers, Env: server.ServerEnv},
	// --state and --once are left to a terminal: a background export
	// continues from the home's own file, and runs on.
	{Name: svcOTel, What: "telemetry export", Run: []string{"export"},
		Flags: []string{"endpoint", "traces-endpoint", "logs-endpoint", "header", "after", "include-text"},
		Log:   (*config.Config).OTelLog, Records: (*config.Config).OTelExports, Env: server.ServerEnv, Vars: otelVar, Check: otelCollector},
}

// kindOf is the long-running command named name. Every caller names one of
// the constants above.
func kindOf(name string) *longRunning {
	for _, k := range longRunnings {
		if k.Name == name {
			return k
		}
	}
	panic("no long-running command is named " + name)
}

// what is the kind in words.
func what(kind string) string { return kindOf(kind).What }

// command is the yardr command line that runs it in a terminal, without
// its flags.
func (k *longRunning) command() []string { return append([]string{k.Name}, k.Run...) }

// logName is the name of its log in the home.
func (k *longRunning) logName() string { return filepath.Base(k.Log(&config.Config{})) }

// webLoopback keeps a web view that no terminal runs on loopback, whatever
// yardr web serve --unsafe allows. The names --host gives it are held to
// what a view on loopback takes, here and not in a log nobody reads: they
// are names it answers to, and open no interface.
func webLoopback(flags *pflag.FlagSet) error {
	const never = "yardr web start and yardr web install never listen off loopback"
	if err := web.CheckAddr("--addr", flags.Lookup("addr").Value.String(), false); err != nil {
		return fmt.Errorf("%w. %s", err, never)
	}
	// The sandbox origin stays on loopback with these two, as the view
	// does, and a public name that is no name is refused here and not
	// in a log nobody reads.
	if err := web.CheckAddr("--pages-addr", flags.Lookup("pages-addr").Value.String(), false); err != nil {
		return fmt.Errorf("%w. %s", err, never)
	}
	if v := flags.Lookup("pages-host").Value.String(); v != "" {
		if _, err := store.CheckWebHost("--pages-host", v); err != nil {
			return err
		}
	}
	host := flags.Lookup("host")
	_, err := webHosts(host.Value.(pflag.SliceValue).GetSlice(), host.Changed, false)
	if off := hostOffLoopback(""); errors.As(err, &off) {
		// Its own words name --unsafe, which these two do not have.
		return fmt.Errorf("--host %s refused: %s, and a view on loopback answers to no IP address off it", string(off), never)
	}
	return err
}

// otelVar reports whether key is one of the standard variables yardr otel
// export reads its collector and its headers from.
func otelVar(key string) bool { return strings.HasPrefix(key, "OTEL_EXPORTER_OTLP_") }

// otelCollector refuses an export with nowhere to send to: in a terminal it
// says so and ends, which in the background nobody would read.
func otelCollector(flags *pflag.FlagSet) error {
	for _, name := range []string{"endpoint", "traces-endpoint", "logs-endpoint"} {
		if flags.Lookup(name).Value.String() != "" {
			return nil
		}
	}
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		if os.Getenv(name) != "" {
			return nil
		}
	}
	return errors.New("no collector: set OTEL_EXPORTER_OTLP_ENDPOINT or give --endpoint; nothing would be exported")
}

// takeFlags gives each of cmds the flags of run that the kind hands on. A
// flag is a copy that sets the same variable: whether it was given is each
// command's own to know.
func (k *longRunning) takeFlags(run *cobra.Command, cmds ...*cobra.Command) {
	for _, name := range k.Flags {
		f := run.Flags().Lookup(name)
		if f == nil {
			panic(fmt.Sprintf("yardr %s has no --%s to hand on", strings.Join(k.command(), " "), name))
		}
		for _, c := range cmds {
			own := *f
			c.Flags().AddFlag(&own)
		}
	}
}

// args are the flags to run it with, as flags gave them: those that were
// given, and its address.
func (k *longRunning) args(flags *pflag.FlagSet) []string {
	var args []string
	for _, name := range k.Flags {
		f := flags.Lookup(name)
		if !f.Changed && name != k.Addr {
			continue
		}
		if many, ok := f.Value.(pflag.SliceValue); ok {
			for _, v := range many.GetSlice() {
				args = append(args, "--"+name, v)
			}
		} else if f.Value.Type() == "bool" {
			args = append(args, "--"+name+"="+f.Value.String())
		} else {
			args = append(args, "--"+name, f.Value.String())
		}
	}
	return args
}

// addr is where flags say it listens, "" for a kind that listens nowhere.
func (k *longRunning) addr(flags *pflag.FlagSet) string {
	if k.Addr == "" {
		return ""
	}
	return flags.Lookup(k.Addr).Value.String()
}

// addrFromSetting gives the kind's address flag the environment override,
// else the yard's setting, when the flag was not given. The command, its
// start and its install then listen on --addr, else the environment, else
// the setting, else the flag's default. The flag stays one that was not
// given. The yard is asked only when the setting decides.
func (a *app) addrFromSetting(ctx context.Context, cfg *config.Config, k *longRunning, flags *pflag.FlagSet) error {
	if k.Setting == "" || flags.Changed(k.Addr) {
		return nil
	}
	if cfg == nil {
		var err error
		cfg, err = a.config()
		if err != nil {
			return err
		}
	}
	if k.EnvAddr != nil {
		if v := k.EnvAddr(cfg); v != "" {
			return flags.Lookup(k.Addr).Value.Set(v)
		}
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if v := all[k.Setting]; v != "" {
		return flags.Lookup(k.Addr).Value.Set(v)
	}
	return nil
}

// addrSettingOf is the kind whose address the setting key is, nil for any
// other setting.
func addrSettingOf(key string) *longRunning {
	for _, k := range longRunnings {
		if k.Setting == key {
			return k
		}
	}
	return nil
}

// addrTakes is what yardr settings set says after the address of a kind:
// what takes it, and what keeps the address it has, as hookAddrTakes says
// for the server's.
func (k *longRunning) addrTakes(cfg *config.Config) string {
	yardr, line := yardrAt(cfg.Home), "yardr "+strings.Join(k.command(), " ")
	takes := fmt.Sprintf("%s, yardr %s start and yardr %[2]s install take it when no --%[3]s is given", line, k.Name, k.Addr)
	if setting, ok := store.SettingOf(k.Setting); ok && setting.Env != "" {
		takes = fmt.Sprintf("YARDR_%s, if set, takes it instead; otherwise %s", setting.Env, takes)
	}
	text := fmt.Sprintf("%s. A %[4]s that runs keeps its address until it is stopped and started again: %[5]s %[2]s stop, then %[5]s %[2]s start.",
		takes, k.Name, k.Addr, k.What, yardr)
	if serviceInstalled(cfg, k.Name) {
		_, _, path, _ := unitFile(cfg, k.Name)
		text += fmt.Sprintf("\nThe login service keeps the address in its file (%s, %s) until it is installed again: %s %s install",
			or(serviceAddr(cfg, k.Name), "none"), path, yardr, k.Name)
	}
	return text
}

// env is the environment a detached one starts with.
func (k *longRunning) env(environ []string, home string) []string {
	return append(k.Env(environ, home), k.vars(environ)...)
}

// vars are the kind's own variables in environ.
func (k *longRunning) vars(environ []string) []string {
	var env []string
	for _, kv := range environ {
		if key, _, _ := strings.Cut(kv, "="); k.Vars != nil && k.Vars(key) {
			env = append(env, kv)
		}
	}
	return env
}

// instance is a running one.
type instance struct {
	PID int `json:"pid"`
	// Addr is where it listens, as it recorded it.
	Addr string `json:"addr,omitempty"`
	// path is the file it holds.
	path string
}

// held are the records in dir that a running process holds. A record
// nobody holds is one that ended without clearing it (a crash, kill -9, a
// reboot), and is told from a running one's without asking whether its pid
// is alive: that pid may be another program's by then.
func held(dir string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return slices.DeleteFunc(paths, func(p string) bool { return !config.DaemonRunning(p) })
}

// announce records this process as a running one of the kind, with what
// else there is to say of it, until clear is called. The record is written
// and locked under a name no reader looks at and only then given its own,
// so what a reader finds is whole and held.
func (k *longRunning) announce(cfg *config.Config, fields map[string]any) (clear func(), err error) {
	dir := k.Records(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sweep(dir)
	f, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, k.Name+"-"+strings.TrimPrefix(filepath.Base(f.Name()), ".new-")+".json")
	record := map[string]any{"pid": os.Getpid(), "started": time.Now().UTC(), "revision": viewRevision()}
	// The OS start time, by which a stop verifies the pid is still this
	// process's before signalling it. Where the OS reports none the pid
	// stands alone, as before.
	if st, err := peer.Describe(os.Getpid()); err == nil {
		record["start"] = st.Start
	}
	for key, v := range fields {
		record[key] = v
	}
	b, _ := json.Marshal(record)
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return func() {
		os.Remove(path)
		f.Close()
	}, nil
}

// announced is announce for a command that runs without its record too: one
// that could not be written is a line in its log, and clear then does
// nothing.
func (k *longRunning) announced(cfg *config.Config, log *slog.Logger, fields map[string]any) (clear func()) {
	clear, err := k.announce(cfg, fields)
	if err != nil {
		log.Warn(fmt.Sprintf("yardr %[1]s stop and yardr %[1]s status will not find this %[2]s: it could not be recorded", k.Name, k.What), "err", err)
		return func() {}
	}
	return clear
}

// viewRevision is the build a running one records itself as; tests replace
// it.
var viewRevision = version.Current

// sweep removes the records in dir of those that ended without clearing
// theirs. A record that is held is a running one's and stays.
func sweep(dir string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range paths {
		if !config.DaemonRunning(p) {
			os.Remove(p)
		}
	}
}

// running are the ones of the kind that run on the home now.
func (k *longRunning) running(cfg *config.Config) []instance {
	run := []instance{}
	if k.Lock != nil {
		if lock := k.Lock(cfg); config.DaemonRunning(lock) {
			run = append(run, instance{PID: config.DaemonPID(lock), path: lock})
		}
		return run
	}
	for _, p := range held(k.Records(cfg)) {
		r := instance{path: p}
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &r) == nil {
			run = append(run, r)
		}
	}
	return run
}

// signalStop tells the process of pid to stop; tests replace it.
var signalStop = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// stopWait is how long a stop waits for what it stopped to end: a daemon
// finishes the pass it is in first, a server the requests it has.
const stopWait = 30 * time.Second

// stop tells every one of the kind that runs on the home to stop and waits
// until each has let go of the file it holds. It returns those it stopped,
// none when none ran.
func (k *longRunning) stop(cfg *config.Config, wait time.Duration) ([]instance, error) {
	run := k.running(cfg)
	var stopping []instance
	for _, r := range run {
		if r.PID <= 0 {
			return nil, fmt.Errorf("a %s holds %s, and the file names no pid to stop", k.What, r.path)
		}
		pid := r.PID
		// The pid is read again and verified before it is signalled: the
		// holder may have let go since running saw it, and the pid may
		// be another process's now.
		var gone bool
		var err error
		if k.Lock != nil {
			pid, gone, err = verifyLock(r.path)
		} else {
			pid, gone, err = verifyRecord(r.path, k.What)
		}
		if err != nil {
			return nil, err
		} else if gone {
			continue
		}
		r.PID = pid
		if err := signalStop(pid); err != nil {
			if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
				continue // gone between the check and the signal
			}
			return nil, fmt.Errorf("stop the %s (pid %d): %w", k.What, pid, err)
		}
		stopping = append(stopping, r)
	}
	deadline := time.Now().Add(wait)
	for _, r := range stopping {
		for ; config.DaemonRunning(r.path); time.Sleep(pollEvery / 5) {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("the %s (pid %d) was told to stop and still holds %s after %s", k.What, r.PID, r.path, wait)
			}
		}
	}
	return stopping, nil
}

// verifyLock reads the daemon's lock file again and returns the pid to
// signal: the holder's, verified by its recorded start time. Gone (the lock
// free, the pid dead or reused) is reported as such and never signalled; a
// lock that is held but names no verifiable pid is an error, not a signal.
func verifyLock(path string) (pid int, gone bool, err error) {
	pid = config.DaemonPID(path)
	start, recorded := config.DaemonStart(path)
	if pid > 0 && (!peer.HaveStat() || (recorded && peer.Alive(pid, start))) {
		return pid, false, nil
	}
	// The holder let go, or the file names another process now; tell them
	// apart by the lock, read again.
	if !config.DaemonRunning(path) {
		return 0, true, nil
	}
	if pid <= 0 {
		return 0, false, fmt.Errorf("a daemon holds %s, and the file names no pid to stop", path)
	}
	if !recorded {
		return 0, false, fmt.Errorf("a daemon holds %s, but pid %d cannot be verified as its holder (the lock file predates start-time verification); not signalling it", path, pid)
	}
	return 0, true, nil // the recorded daemon is gone or its pid was reused
}

// verifyRecord reads the kind's record file again and returns the pid to
// signal: the holder's, verified by its recorded start time. Gone (the
// record free, the pid dead or reused) is reported as such and never
// signalled; a record that is held but names no verifiable pid is an
// error, not a signal.
func verifyRecord(path, what string) (pid int, gone bool, err error) {
	pid, start, recorded := recordIdentity(path)
	if pid > 0 && (!peer.HaveStat() || (recorded && peer.Alive(pid, start))) {
		return pid, false, nil
	}
	// The holder let go, or the file names another process now; tell them
	// apart by the lock, read again.
	if !config.DaemonRunning(path) {
		return 0, true, nil
	}
	if pid <= 0 {
		return 0, false, fmt.Errorf("a %s holds %s, and the file names no pid to stop", what, path)
	}
	if !recorded {
		return 0, false, fmt.Errorf("a %s holds %s, but pid %d cannot be verified as its holder (the record predates start-time verification); not signalling it", what, path, pid)
	}
	return 0, true, nil // the recorded holder is gone or its pid was reused
}

// recordIdentity is the pid and the OS start time the held record at
// path names. It is zero when no process holds the record or the record
// cannot be read; recorded is false when the holder recorded no start
// time: a record from before starts were recorded.
func recordIdentity(path string) (pid int, start uint64, recorded bool) {
	if !config.DaemonRunning(path) {
		return 0, 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	var rec struct {
		PID   int     `json:"pid"`
		Start *uint64 `json:"start"`
	}
	if json.Unmarshal(b, &rec) != nil {
		return 0, 0, false
	}
	if rec.Start != nil {
		return rec.PID, *rec.Start, true
	}
	return rec.PID, 0, false
}

// stopDaemon stops the home's daemon. It returns the pid it stopped, 0 when
// no daemon ran.
func stopDaemon(cfg *config.Config, wait time.Duration) (int, error) {
	run, err := kindOf(svcDaemon).stop(cfg, wait)
	if err != nil || len(run) == 0 {
		return 0, err
	}
	return run[0].PID, nil
}

// spawn starts one of the kind for the home with args, detached from this
// terminal and logging to the kind's log in the home, the way server.Start
// starts the server. exited gets its end, should it come, and stop kills
// it. environ is the starting run's environment (see app.environ), of
// which the kind keeps what its env says. Tests replace it.
var spawn = func(environ []string, cfg *config.Config, k *longRunning, args []string) (pid int, exited <-chan error, stop func() error, err error) {
	self, err := os.Executable()
	if err != nil {
		return 0, nil, nil, err
	}
	cmd := exec.Command(self, append(k.command(), args...)...)
	cmd.Env = k.env(environ, cfg.Home)
	cmd.Dir = cfg.Home
	if exited, err = server.SpawnDetached(cmd, k.Log(cfg)); err != nil {
		return 0, nil, nil, err
	}
	return cmd.Process.Pid, exited, cmd.Process.Kill, nil
}

// startWait is how long a started one gets to be up.
var startWait = 10 * time.Second

// start starts one of the kind in the background and returns once it is
// up: it listens on addr, or, for a kind that listens nowhere, holds its
// file. Such a kind is started once: nothing tells a second from the first.
func (k *longRunning) start(environ []string, cfg *config.Config, args []string, addr string) (int, error) {
	if addr != "" && answers(addr) {
		return 0, fmt.Errorf("something listens on %s already: a %s that runs, perhaps (http://%s/). --%s takes another port", addr, k.What, addr, k.Addr)
	}
	if run := k.running(cfg); addr == "" && len(run) > 0 {
		return 0, fmt.Errorf("a %s runs on the yard in %s already (pid %d). To stop it: %s %s stop", k.What, cfg.Home, run[0].PID, yardrAt(cfg.Home), k.Name)
	}
	pid, exited, stop, err := spawn(environ, cfg, k, args)
	if err != nil {
		return 0, err
	}
	up, did := func() bool { return answers(addr) }, "listen on "+addr
	if addr == "" {
		up, did = func() bool {
			return slices.ContainsFunc(k.running(cfg), func(r instance) bool { return r.PID == pid })
		}, "say that it runs"
	}
	for deadline := time.Now().Add(startWait); time.Now().Before(deadline); {
		select {
		case <-exited:
			return 0, fmt.Errorf("the %s ended right after it started; see %s", k.What, k.Log(cfg))
		case <-time.After(pollEvery / 5):
		}
		if up() {
			return pid, nil
		}
	}
	if err := stop(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return 0, fmt.Errorf("the %s (pid %d) did not %s within %s; could not stop it: %v; see %s", k.What, pid, did, startWait, err, k.Log(cfg))
	}
	return 0, fmt.Errorf("the %s (pid %d) did not %s within %s; stopped it; see %s", k.What, pid, did, startWait, k.Log(cfg))
}

// verbs are the commands every long-running kind has beside the one that
// runs it, for the kind's own file to say more in (a Long, an Example).
type verbs struct{ start, stop, status, install, uninstall *cobra.Command }

// longRunningCmds adds start, stop, status, install and uninstall to cmd,
// the command named as the kind is, for the kind that run runs.
func (a *app) longRunningCmds(cmd, run *cobra.Command, k *longRunning) *verbs {
	line := "yardr " + strings.Join(k.command(), " ")
	var fileOnly bool
	var wait time.Duration
	v := &verbs{}
	v.start = &cobra.Command{
		Use:   "start",
		Short: fmt.Sprintf("Start the %s in the background", k.What),
		Long: fmt.Sprintf(`Start %[1]s detached from this terminal, logging to <home>/%[2]s,
wait until it is up and print its pid. It runs until yardr %[3]s stop stops
it or the machine restarts; yardr %[3]s install keeps one running from login
on instead. The flags are those of %[1]s that a background one takes, and
are handed on to it.`, line, k.logName(), k.Name),
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if k.Check != nil {
				if err := k.Check(c.Flags()); err != nil {
					return err
				}
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if err := a.addrFromSetting(c.Context(), cfg, k, c.Flags()); err != nil {
				return err
			}
			if k.FromSetting != nil {
				if err := k.FromSetting(a, c.Context(), cfg, c.Flags()); err != nil {
					return err
				}
			}
			// The environment may have supplied the address after the
			// first check. Validate the effective value before starting it.
			if k.Setting != "" && k.Check != nil {
				if err := k.Check(c.Flags()); err != nil {
					return err
				}
			}
			addr := k.addr(c.Flags())
			pid, err := k.start(a.environ(), cfg, k.args(c.Flags()), addr)
			if err != nil {
				return err
			}
			res := map[string]any{"pid": pid, "log": k.Log(cfg)}
			if addr != "" {
				res["address"], res["url"] = addr, webURL(addr)
			}
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				if addr != "" {
					fmt.Fprintf(w, "The %s: %s\n\n", k.What, webURL(addr))
				}
				fmt.Fprintf(w, "The %s runs in the background (pid %d) and logs to %s. To stop it: %s %s stop\n",
					k.What, pid, k.Log(cfg), yardrAt(cfg.Home), k.Name)
			})
		},
	}
	v.stop = &cobra.Command{
		Use:   "stop",
		Short: fmt.Sprintf("Stop the yard's %s", k.What),
		Long: fmt.Sprintf(`Tell every %[1]s that runs on this yard to stop, however it was started
(in a terminal, by yardr %[2]s start, or by the service that starts when you log in), and wait until it
has.

One run by the service that starts when you log in stays stopped too: the
service starts it again at the next login, or when it ends with a failure, and a stop is none.
yardr %[2]s start starts one in the background before that.`, k.What, k.Name),
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			run, err := k.stop(cfg, wait)
			if err != nil {
				return err
			}
			res := map[string]any{"home": cfg.Home, "stopped": len(run) != 0, "pid": 0, "instances": run}
			if len(run) != 0 {
				res["pid"] = run[0].PID
			}
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				if len(run) == 0 {
					fmt.Fprintf(w, "no %s is running on the yard in %s\n", k.What, cfg.Home)
					return
				}
				for _, r := range run {
					fmt.Fprintf(w, "%s stopped (pid %d)%s\n", k.What, r.PID, at(r.Addr))
				}
				fmt.Fprintf(w, "Start one again: %s %s start\n", yardrAt(cfg.Home), k.Name)
			})
		},
	}
	v.stop.Flags().DurationVar(&wait, "wait", stopWait, "how long to wait for it to end")
	v.status = &cobra.Command{
		Use:   "status",
		Short: fmt.Sprintf("Report whether the %s runs, and whether it starts when you log in", k.What),
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			run, service := k.running(cfg), stateOf(c.Context(), cfg, k.Name)
			res := map[string]any{"home": cfg.Home, "running": len(run) != 0, "pid": 0, "instances": run, "service": service}
			if len(run) != 0 {
				res["pid"] = run[0].PID
			}
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				for _, r := range run {
					fmt.Fprintf(w, "%-8s running (pid %d)%s\n", k.Name, r.PID, at(r.Addr))
				}
				if len(run) == 0 {
					fmt.Fprintf(w, "%-8s not running\n", k.Name)
				}
				fmt.Fprintf(w, "service  %s\n", service.line(k.Name, yardrAt(cfg.Home)))
			})
		},
	}
	v.install = &cobra.Command{
		Use:   "install",
		Short: fmt.Sprintf("Start the %s at login: a launchd agent (macOS) or a systemd user unit (Linux)", k.What),
		Long: fmt.Sprintf(`Write %[1]s as a service that starts when you log in, and load it: a launchd
agent in ~/Library/LaunchAgents on macOS, a systemd user unit in
~/.config/systemd/user on Linux. The service manager starts the %[2]s now
and at every login, and starts it again when it ends with a failure. It logs
to <home>/%[3]s.

The service is this yard's: its name carries the home. Its file holds the
yardr that ran the install (by its path), the flags given here, YARDR_HOME
and the PATH of the shell it was installed from. docs/configuration.md,
"Login services", says what else, and what a reboot and an upgrade do.

		--file-only writes the file and loads nothing.`, line, k.What, k.logName()),
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if k.Check != nil {
				if err := k.Check(c.Flags()); err != nil {
					return err
				}
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if err := a.addrFromSetting(c.Context(), cfg, k, c.Flags()); err != nil {
				return err
			}
			if k.FromSetting != nil {
				if err := k.FromSetting(a, c.Context(), cfg, c.Flags()); err != nil {
					return err
				}
			}
			if k.Setting != "" && k.Check != nil {
				if err := k.Check(c.Flags()); err != nil {
					return err
				}
			}
			u, err := newUnit(a.environ(), cfg, k, k.args(c.Flags()))
			if err != nil {
				return err
			}
			var warn []string
			if k.Warn != nil {
				if warn, err = k.Warn(a, c.Context(), cfg, u.Program); err != nil {
					return err
				}
			}
			res, err := u.install(c.Context(), cfg, fileOnly)
			if err != nil {
				return err
			}
			res.Address = k.addr(c.Flags())
			res.Warnings = append(res.Warnings, warn...)
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				res.print(w)
				if k.Name == svcDaemon {
					// The daemon is one of three things a reboot stops;
					// herdr's server is another, whose service is yardr up
					// --login's (docs/install.md, "Keep it running after a
					// reboot").
					fmt.Fprintln(w)
					fmt.Fprintln(w, herdrLoginPart(cfg).line())
				}
			})
		},
	}
	v.uninstall = &cobra.Command{
		Use:   "uninstall",
		Short: fmt.Sprintf("Remove the %[1]s's service that starts when you log in: unload it, which stops its %[1]s, and delete its file", k.What),
		Long: fmt.Sprintf(`Take the %[1]s's service that starts when you log in from its manager and delete its file: all
that yardr %[2]s install made. The manager stops the %[1]s the service ran.
One started any other way is not the service's and is left running.

--file-only deletes the file and unloads nothing.`, k.What, k.Name),
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			return a.uninstall(c.OutOrStdout(), c.Context(), cfg, k.Name, fileOnly)
		},
	}
	for _, c := range []*cobra.Command{v.install, v.uninstall} {
		c.Flags().BoolVar(&fileOnly, "file-only", false, "write or delete the service's file only; leave the service manager alone")
	}
	k.takeFlags(run, v.start, v.install)
	cmd.AddCommand(v.start, v.stop, v.status, v.install, v.uninstall)
	return v
}
