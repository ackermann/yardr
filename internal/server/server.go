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
	wrap          func(http.Handler) http.Handler
}

func (r *runner) run(ctx context.Context) error {
	if len(r.c.Socket()) > config.MaxSocket {
		return fmt.Errorf("socket path %s is too long for a unix socket; use a shorter YARDR_HOME", r.c.Socket())
	}
	if r.grace == 0 {
		r.grace = drainGrace
	}
	if r.settle == 0 {
		r.settle = drainSettle
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
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		ln.Close()
		return "", err
	}
	var to string
	defer func() {
		// The pid stays ours across the exec, and clients seeing the pidfile
		// wait for the new build instead of starting a second server.
		if to == "" {
			os.Remove(c.Socket())
			os.Remove(c.PIDFile())
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
	hookSrv := serveHooks(c, st, log)
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
		case serveErr = <-served:
			stopped = true
			break wait
		}
	}
	if hookSrv != nil {
		hookSrv.Close()
	}
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

// serveHooks starts the webhook receiver on its own TCP listener when
// configured. It is a separate server with only the receiver's handler, so
// a port opened to the internet never reaches the store API or BDP. A
// receiver that cannot listen is logged, not fatal: the yard itself works.
func serveHooks(c *config.Config, st *store.Store, log *slog.Logger) *http.Server {
	if c.HookAddr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", c.HookAddr)
	if err != nil {
		log.Error("hook receiver not started", "addr", c.HookAddr, "err", err)
		return nil
	}
	srv := &http.Server{Handler: hooks.NewReceiver(st, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("hook receiver stopped", "err", err)
		}
	}()
	log.Info("hook receiver ready", "addr", ln.Addr().String())
	return srv
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

// serverSettings are the yardr variables the server reads, passed on from
// the starter's environment (under the old names too, while those are
// read). Everything else that names the starter (its actor, bead, session,
// branch) is the starter's, not the server's.
var serverSettings = []string{"HOOK_ADDR", "EVENT_RETENTION", "HOOK_PAYLOAD_RETENTION"}

// ServerEnv is the environment a detached server starts with, from the
// starter's environ: PATH and HOME (git, which reads the rigs' .yardr/ from
// their base, needs both), the serverSettings, and the home it serves,
// under both names, in place of whatever YARDR_HOME said. Nothing else: no
// credential, no identity, no agent's settings.
func ServerEnv(environ []string, home string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == "PATH" || k == "HOME" || config.IsVar(k, serverSettings...) {
			env = append(env, kv)
		}
	}
	return append(env, config.Env("HOME", home)...)
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
