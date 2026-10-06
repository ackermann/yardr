// Package server runs the one process that owns the yard's store file and
// serves it to every other yard process over a unix socket. The CLI starts
// it on first use, detached, so it outlives the call that started it.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/internal/yards"
)

// Run owns the store and serves it until ctx ends. When a new build of
// yardr is installed over the binary it runs, it drains, closes the store and
// execs that build in its place (same pid), so `go install` is all an
// upgrade takes.
func Run(ctx context.Context, c *config.Config, log *slog.Logger) error {
	bin, err := version.Self()
	if err != nil {
		log.Warn("cannot watch the yardr binary; the server will not replace itself", "err", err)
	}
	r := &runner{c: c, log: log, bin: bin, rev: version.Current(), check: 10 * time.Second}
	return r.run(ctx)
}

// The drain, when the server stops or replaces itself: drainGrace bounds
// the wait for connections to go idle, then drainSettle bounds the wait for
// handlers still running after that, so the store is not closed under a
// call that has it in hand. A client's retry (rpc.ReplaceRetry) must outlast
// the two together and the new build's start, or a call made during a
// replace fails and Connect starts a second server that cannot take the
// store.
const (
	drainGrace  = 5 * time.Second
	drainSettle = 10 * time.Second
)

// runner is one server process: it serves, and replaces itself when told.
type runner struct {
	c     *config.Config
	log   *slog.Logger
	bin   *version.Binary // nil: never replace
	rev   string          // the revision this process runs
	check time.Duration   // how often the binary is checked without a client asking
	// grace and settle are the drain's bounds (default drainGrace and
	// drainSettle); tests shorten them.
	grace, settle time.Duration
	// homeCheck is how often the server looks whether its home and socket
	// are still its own (default config.HomeCheck); tests shorten it.
	homeCheck time.Duration
	wrap      func(http.Handler) http.Handler
}

func (r *runner) run(ctx context.Context) error {
	if len(r.c.Socket()) > config.MaxSocket {
		return fmt.Errorf("socket path %s is too long for a unix socket; give --home (or YARDR_HOME) a shorter directory", r.c.Socket())
	}
	if r.grace == 0 {
		r.grace = drainGrace
	}
	if r.settle == 0 {
		r.settle = drainSettle
	}
	if r.homeCheck == 0 {
		r.homeCheck = config.HomeCheck
	}
	for {
		to, err := r.serve(ctx)
		if err != nil || to == "" {
			return err
		}
		if ctx.Err() != nil {
			// Told to stop while draining for the replace: stop, rather than
			// come back up as the new build. serve has already returned "" in
			// this case; this covers a stop that lands just after it.
			r.log.Info("yardr server stopped during a replace; not starting the new build", "to", version.Show(to))
			return nil
		}
		r.log.Info("replacing the yardr server", "from", version.Show(r.rev), "to", version.Show(to), "binary", r.bin.Path)
		// The new build logs server_replaced once it owns the store.
		if err := r.bin.Replace(config.Env("REPLACED_FROM", version.Show(r.rev))...); err != nil {
			// Keep serving the old build; clients that need the new one get
			// the skew message until someone stops this server.
			r.log.Error("cannot replace the yardr server; serving on", "binary", r.bin.Path, "err", err)
			continue
		}
		return nil // only a test's fake exec returns
	}
}

// serve owns the store until ctx ends or a new build is installed; it
// returns that build's revision, with the store closed and in-flight calls
// answered.
func (r *runner) serve(ctx context.Context) (string, error) {
	c, log := r.c, r.log
	st, err := store.Open(c.DBPath())
	if errors.Is(err, store.ErrLocked) {
		return "", fmt.Errorf("another yardr server owns %s", c.DBPath())
	} else if err != nil {
		return "", err
	}
	defer st.Close(context.Background())

	// We own the store, so a socket file left behind is stale.
	_ = os.Remove(c.Socket())
	ln, err := net.Listen("unix", c.Socket())
	if err != nil {
		return "", err
	}
	if err := os.Chmod(c.Socket(), 0o600); err != nil {
		ln.Close()
		return "", err
	}
	if err := writePID(c); err != nil {
		ln.Close()
		return "", err
	}
	held, err := c.Hold(c.Socket())
	if err != nil {
		ln.Close()
		return "", err
	}
	defer held.Close()
	var to string
	lost := false // the home or the socket is no longer ours
	defer func() {
		switch {
		case lost:
			// The socket at that path, if any, is another server's. The
			// pidfile and the receiver's state are still ours only while
			// the pidfile names us.
			if pid, err := readPID(c); err == nil && pid == os.Getpid() {
				os.Remove(c.PIDFile())
				os.Remove(c.HookState())
			}
		case to == "":
			// The pid stays ours across the exec, and clients seeing the
			// pidfile wait for the new build instead of starting a second
			// server.
			os.Remove(c.Socket())
			os.Remove(c.PIDFile())
			os.Remove(c.HookState())
		}
	}()
	if from := config.Getenv("REPLACED_FROM"); from != "" {
		config.Unsetenv("REPLACED_FROM")
		log.Info("yardr server replaced", "from", from, "to", version.Show(r.rev))
		if err := st.LogEvent(ctx, "", "server_replaced", "yard",
			map[string]any{"from": from, "to": version.Show(r.rev), "pid": os.Getpid()}); err != nil {
			log.Warn("server_replaced event", "err", err)
		}
	}

	if r.bin != nil {
		// Both the backstop and a client's skew land here; the mayor sees
		// in events why a build installed over ours was not taken.
		r.bin.Skipped = func(rev, reason string) {
			log.Warn("not replacing the yardr server", "candidate", version.Show(rev), "reason", reason)
			if err := st.LogEvent(context.Background(), "", "replace_skipped", "yard",
				map[string]any{"process": "server", "running": version.Show(r.rev), "candidate": version.Show(rev), "reason": reason}); err != nil {
				log.Warn("replace_skipped event", "err", err)
			}
		}
	}
	replace := make(chan string, 1)
	offer := func(clientRevision string) bool {
		if r.bin == nil {
			return false
		}
		rev, ok := r.bin.Replacement()
		// Replacing is only safe when the caller is the build installed at
		// our executable path. An older or unrelated client must not be able
		// to make the server upgrade (or otherwise turn a failed upgrade into
		// a downgrade decision).
		matched := ok && (clientRevision == "" || clientRevision == rev)
		if matched {
			select {
			case replace <- rev:
			default:
			}
		}
		return matched
	}
	streams, stopStreams := context.WithCancel(context.Background())
	defer stopStreams()
	// inflight counts handlers running, which Shutdown stops waiting for at
	// its deadline and Close does not wait for at all.
	var inflight sync.WaitGroup
	h := r.handler(st, offer, streams)
	counted := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		inflight.Add(1)
		defer inflight.Done()
		h.ServeHTTP(w, req)
	})
	srv := &http.Server{Handler: counted, ConnContext: rpc.ConnContext, ReadHeaderTimeout: 10 * time.Second}
	// Streams (events, wake) never finish on their own; end them when
	// shutdown starts so it only waits for calls in flight.
	srv.RegisterOnShutdown(stopStreams)
	stopHooks := serveHooks(ctx, c, st, log)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("yardr server ready", "store", c.DBPath(), "socket", c.Socket(), "revision", version.Show(r.rev))
	if c.IsLegacyHome() {
		// Said here, once per server, rather than on every command.
		log.Warn("using the old home because ~/.yardr does not exist; "+
			"once no server, agent or worktree uses it, it can be moved there", "home", c.Home)
	}

	var check <-chan time.Time
	if r.bin != nil && r.check > 0 {
		t := time.NewTicker(r.check)
		defer t.Stop()
		check = t.C
	}
	// A server outlives the command that started it, so nothing else stops
	// one whose home was removed under it (a test's temp directory, an
	// isolated yard torn down): no client can reach it there, and it would
	// run until the machine restarts.
	watch, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	gone := held.Watch(watch, r.homeCheck)
	var serveErr error
	stopped := false // Serve has returned
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case to = <-replace:
			break wait
		case <-check:
			offer("")
		case why := <-gone:
			log.Warn("the yardr server's home is no longer its own; stopping", "home", c.Home, "reason", why)
			lost = true
			// Closing a unix listener unlinks its path, which by now names
			// nothing or another server's socket.
			if ul, ok := ln.(*net.UnixListener); ok {
				ul.SetUnlinkOnClose(false)
			}
			break wait
		case serveErr = <-served:
			stopped = true
			break wait
		}
	}
	stopHooks()
	sctx, cancel := context.WithTimeout(context.Background(), r.grace)
	defer cancel()
	if srv.Shutdown(sctx) != nil {
		log.Warn("calls still running at shutdown; closing their connections", "after", r.grace)
		srv.Close()
	}
	if !stopped {
		serveErr = <-served
	}
	r.settleHandlers(&inflight)
	if ctx.Err() != nil {
		to = "" // a stop during the replace drain wins over the replace
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		to = ""
	}
	return to, serveErr
}

// settleHandlers waits, up to r.settle, for handlers still running after
// shutdown, so the deferred store close does not pull the store from under
// them.
func (r *runner) settleHandlers(inflight *sync.WaitGroup) {
	settled := make(chan struct{})
	go func() {
		inflight.Wait()
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(r.settle):
		r.log.Warn("closing the store with calls still running", "waited", r.settle)
	}
}

// handler serves the store API, stamps replies with this build, and offers
// a replace when a client of another build calls.
func (r *runner) handler(st *store.Store, offer func(string) bool, streams context.Context) http.Handler {
	h := rpc.Handler(st, r.log)
	if r.wrap != nil {
		h = r.wrap(h)
	}
	pid := strconv.Itoa(os.Getpid())
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set(version.Header, r.rev)
		w.Header().Set(rpc.PIDHeader, pid)
		if req.Method == http.MethodGet {
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			defer context.AfterFunc(streams, cancel)()
			req = req.WithContext(ctx)
		}
		// A client built differently may be the new build just installed:
		// if so, refuse its call (it retries) and hand over to that build.
		// An older client finds the binary unchanged and is served as is.
		if theirs := req.Header.Get(version.Header); theirs != "" && theirs != r.rev &&
			strings.HasPrefix(req.URL.Path, "/call/") && offer(theirs) {
			rpc.WriteReplacing(w)
			return
		}
		h.ServeHTTP(w, req)
	})
}

// serveHooks starts the webhook receiver, and with it the receiver of the
// yard's peers, on its own TCP listener when it
// has an address: YARDR_HOOK_ADDR where the server started, else the yard's
// hook_addr setting, read once here, so a change takes the next start. It
// is a separate server with only the receiver's handler, so a port opened
// to the internet never reaches the store API or BDP. A receiver that
// cannot listen is not fatal, the yard itself works: two yards given one
// address is the usual cause, so it is logged as an event and left in the
// receiver's state for doctor to say.
//
// The function returned stops the listener and has the receiver log the
// refusals it had still counted (hooks.Receiver.Run); it is called while the
// store is open.
func serveHooks(ctx context.Context, c *config.Config, st *store.Store, log *slog.Logger) (stop func()) {
	l := hooks.Listener{Addr: c.HookAddr, From: hooks.FromVariable}
	if l.Addr == "" {
		all, err := st.Settings(ctx)
		if err != nil {
			log.Warn("hook_addr setting not read", "err", err)
		}
		l.Addr, l.From = all[hooks.FromSetting], hooks.FromSetting
	}
	if l.Addr == "" {
		l.From = ""
	}
	defer func() {
		if err := hooks.WriteListener(c, l); err != nil {
			log.Warn("hook receiver state not written", "err", err)
		}
	}()
	if l.Addr == "" {
		return func() {}
	}
	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		l.Error = err.Error()
		log.Error("hook receiver not started", "addr", l.Addr, "from", l.From, "err", err)
		if err := st.LogEvent(ctx, "", "hook_listener_failed", "yard",
			map[string]any{"addr": l.Addr, "from": l.From, "error": l.Error}); err != nil {
			log.Warn("hook_listener_failed event", "err", err)
		}
		return func() {}
	}
	// Beside the hooks, under /peers/: the yards this one is linked with
	// (docs/peers.md). Each handler serves its own paths and nothing else.
	mux := http.NewServeMux()
	rc := hooks.NewReceiver(st, log)
	running, stopRunning := context.WithCancel(context.Background())
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		rc.Run(running)
	}()
	mux.Handle("/hooks/", rc.Handler())
	mux.Handle("/peers/", yards.NewReceiver(st, c.PeerKey(), log).Handler())
	srv := &http.Server{Handler: mux,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("hook receiver stopped", "err", err)
		}
	}()
	log.Info("hook receiver ready", "addr", ln.Addr().String(), "from", l.From)
	return func() {
		srv.Close()
		stopRunning()
		<-flushed
	}
}

// Connect returns a client for the running server, starting one if none
// answers.
func Connect(ctx context.Context, c *config.Config) (*rpc.Client, error) {
	client := rpc.NewClient(c.Socket())
	if ping(ctx, client) {
		return client, nil
	}
	// A live pid without an answer is a server between builds (it execs in
	// place): wait for it, as long as a call keeps retrying, rather than
	// race a second one for the store.
	for deadline := time.Now().Add(client.Retry); alive(c) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		if ping(ctx, client) {
			return client, nil
		}
	}
	if err := Start(ctx, c); err != nil {
		return nil, err
	}
	return client, nil
}

// Running reports whether a server answers on the socket.
func Running(ctx context.Context, c *config.Config) bool {
	return ping(ctx, rpc.NewClient(c.Socket()))
}

// Info is what a running server says of itself.
type Info struct {
	PID int `json:"pid"`
	// Revision is the build it runs; "" for one that carries none.
	Revision string `json:"revision"`
	// Since is when this build of it started: when it wrote the pidfile,
	// which a server that replaces itself writes again. Zero when the
	// pidfile is not this server's.
	Since time.Time `json:"since"`
}

// Describe is the server that answers on the socket; false when none does.
// It starts none.
func Describe(ctx context.Context, c *config.Config) (Info, bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pid, rev, err := rpc.NewClient(c.Socket()).Owner(ctx)
	if err != nil {
		return Info{}, false
	}
	info := Info{PID: pid, Revision: rev}
	if named, err := readPID(c); err == nil && named == pid {
		if fi, err := os.Stat(c.PIDFile()); err == nil {
			info.Since = fi.ModTime()
		}
	}
	return info, true
}

func ping(ctx context.Context, client *rpc.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return client.Ping(ctx) == nil
}

// executable is the binary Start runs as the server; tests replace it.
var executable = os.Executable

// Start launches `yardr server run` detached and waits until it answers.
//
// The server outlives the command that started it and serves every later
// one, so it gets none of that command's environment or working directory:
// the first command on a yard is as often an agent's (whose pane holds its
// credentials from an env_file) as the operator's, and a server holding a
// resident's tokens, or a cwd in a worktree that is removed under it, would
// be wrong for everyone else. It gets what it needs (ServerEnv) and runs
// in the home.
func Start(ctx context.Context, c *config.Config) error {
	self, err := executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(c.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "server", "run")
	cmd.Env = ServerEnv(os.Environ(), c.Home)
	cmd.Dir = c.Home
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	client := rpc.NewClient(c.Socket())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ping(ctx, client) {
			return nil
		}
		select {
		case <-exited:
			// Lost a race with another starter? Then its server answers.
			if ping(ctx, client) {
				return nil
			}
			return fmt.Errorf("yardr server exited during startup; see %s", c.LogFile())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("yardr server did not answer within 10s; see %s", c.LogFile())
}

// ServerEnv is the environment a detached server starts with, from the
// starter's environ: PATH and HOME (git, which reads the rigs' .yardr/ from
// their base, needs both), the settings the server reads
// (config.ServerSettings, under the old names too, while those are read),
// and the home it serves, under both names, in place of whatever YARDR_HOME
// said. Nothing else: no credential, no identity (the starter's actor,
// bead, session and branch are the starter's, not the server's), no agent's
// settings. The settings reach it from an operator's shell only: a session
// or a gate is started without them (config.WithoutServerSettings).
func ServerEnv(environ []string, home string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == "PATH" || k == "HOME" || config.IsVar(k, config.ServerSettings...) {
			env = append(env, kv)
		}
	}
	return append(env, config.Env("HOME", home)...)
}

// writePID records this process in the pidfile, by renaming a finished file
// over it. The build a server execs writes the same pid over the old
// build's file while clients in Connect read it to see whether to wait;
// written in place, the file is empty for a moment, the client takes the
// server for gone and starts a second one.
func writePID(c *config.Config) error {
	tmp := c.PIDFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.PIDFile()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// alive reports whether the pidfile names a running process.
func alive(c *config.Config) bool {
	pid, err := readPID(c)
	return err == nil && syscall.Kill(pid, 0) == nil
}

func readPID(c *config.Config) (int, error) {
	b, err := os.ReadFile(c.PIDFile())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("bad pidfile: %w", err)
	}
	return pid, nil
}

// Stop terminates the server recorded in the pidfile.
func Stop(c *config.Config) error {
	pid, err := readPID(c)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no yardr server is running")
	} else if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	for i := 0; i < 200 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		return fmt.Errorf("yardr server (pid %d) did not stop", pid)
	}
	return nil
}
