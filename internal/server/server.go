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
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/preview"
	"yardr.dev/yardr/internal/rpc"
	runners "yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/telemetry"
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
		// A test that starts the server through the real CLI cannot set
		// the interval itself, so it names a shorter tick in the
		// environment instead; a stray value keeps the real minute with
		// one warning, as a malformed owner does.
		if tick, ok := config.TestHomeCheck(); ok {
			r.homeCheck = tick
		} else if raw := config.Getenv(config.TestHomeCheckEnv); raw != "" {
			r.log.Warn("ignoring a malformed YARDR_TEST_HOME_CHECK", "value", raw)
		}
	}
	for {
		to, replacing, err := r.serve(ctx)
		if err != nil || !replacing {
			if err == nil {
				r.log.Info("yardr server stopped")
			}
			return err
		}
		if ctx.Err() != nil {
			// Told to stop while draining for the replace: stop, rather than
			// come back up as the new build. serve has already said it is not
			// replacing in this case; this covers a stop that lands just after.
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
// returns that build's revision and true, with the store closed and in-flight
// calls answered. A build can carry no revision (one made without a VCS
// stamp, whose file's time says it is newer), so whether this is a replace is
// the bool's to say: an empty revision is a build to exec like any other,
// never a stop.
func (r *runner) serve(ctx context.Context) (string, bool, error) {
	c, log := r.c, r.log
	st, err := store.Open(c.DBPath())
	if errors.Is(err, store.ErrLocked) {
		return "", false, fmt.Errorf("another yardr server owns %s", c.DBPath())
	} else if err != nil {
		return "", false, err
	}
	defer st.Close(context.Background())
	// What a bead this yard sends to a linked one says of the yard.
	st.SetYard(c.PeerKey(), yards.Says().Yardr)
	// How a pack apply ask reads the packs this build embeds, and a
	// preset grant ask their presets by source.
	st.SetPackReader(packReaderFor(c.Home))
	st.SetPresetReader(presetReaderFor())
	// How a hosted-Google source call mints: in-process through the edge,
	// never by running the mint command its setting names.
	st.SetHostedMint(hostedMintFor(st, c))
	// How the operator's SessionScreen reads a pane's screen: herdr, as
	// the runner builds it, with the store to follow the yard's herdr
	// session setting on every read.
	st.SetPaneReader(&runners.Herdr{Bin: "herdr", WorktreeRoot: c.Worktrees(), YardHome: c.Home, Store: st})
	// How TelemetryShow and TelemetrySet turn the switch and preview the
	// next request: the CLI's own functions, on this home's state file.
	st.SetTelemetrySwitch(telemetry.Switch{})
	// The carrying out of approved applies (carry.go): this start's
	// boot takes up what an earlier one left, before it serves.
	boot := newBoot()
	watchCtx, stopWatchCtx := context.WithCancel(ctx)
	defer stopWatchCtx()
	go runCarryWatcher(watchCtx, st, c.Home, boot, log)
	go runDaemonWatcher(watchCtx, st, c.Home, log)

	// We own the store, so a socket file left behind is stale.
	_ = os.Remove(c.Socket())
	ln, err := net.Listen("unix", c.Socket())
	if err != nil {
		return "", false, err
	}
	if err := os.Chmod(c.Socket(), 0o600); err != nil {
		ln.Close()
		return "", false, err
	}
	if err := writePID(c); err != nil {
		ln.Close()
		return "", false, err
	}
	held, err := c.Hold(c.Socket())
	if err != nil {
		ln.Close()
		return "", false, err
	}
	defer held.Close()
	var to string
	replacing := false // a newer build is installed: serve ends so that run execs it
	lost := false      // the home or the socket is no longer ours
	defer func() {
		switch {
		case lost:
			// The socket at that path, if any, is another server's. The
			// pidfile and the receiver's state are still ours only while
			// the pidfile names us.
			if pid, err := readPID(c); err == nil && pid == os.Getpid() {
				os.Remove(c.PIDFile())
				os.Remove(c.PIDStart())
				os.Remove(c.HookState())
			}
		case !replacing:
			// The pid stays ours across the exec, and clients seeing the
			// pidfile wait for the new build instead of starting a second
			// server.
			os.Remove(c.Socket())
			os.Remove(c.PIDFile())
			os.Remove(c.PIDStart())
			os.Remove(c.HookState())
			os.Remove(c.EdgeState())
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
		// Both the backstop and a client's skew land here; the yardmaster sees
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
	// The preview supervisor (design yardr-hosted-q0xp, sections 5-7):
	// part of this server, holding its registry in the store, starting
	// approved previews and carrying their traffic on its yard-side
	// endpoint, served over a preview link beside the yard's own.
	supervisor := preview.New(st, c.Home, log)
	supervisorDone := make(chan struct{})
	go func() {
		defer close(supervisorDone)
		supervisor.Run(watchCtx)
	}()
	defer func() {
		stopWatchCtx()
		<-supervisorDone
	}()
	stopPreviewLink := KeepEdge(ctx, c, st, "preview", supervisor.Handler(), log)
	defer servePprof(config.Getenv(PprofEnv), log)()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("yardr server ready", "store", c.DBPath(), "socket", c.Socket(), "revision", version.Show(r.rev))

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
	watch, stopHomeWatch := context.WithCancel(ctx)
	defer stopHomeWatch()
	gone := held.Watch(watch, r.homeCheck)
	// A test names its process to the server it starts (YARDR_TEST_OWNER),
	// which stops when the owner is gone: a test binary killed by its
	// timeout runs no cleanup, and the home it leaves intact gives the home
	// watch nothing to act on. A real yard never sets the variable, and a
	// stray value cannot stop it: a malformed one is a warning, never a
	// stop. The watch stops through the normal path (lost stays false):
	// the home, socket and pidfiles are still this server's.
	owner, ownerStart, watched := config.TestOwner()
	var ownerGone <-chan struct{}
	if watched {
		ownerGone = config.WatchOwner(watch, r.homeCheck, owner, ownerStart)
	} else if raw := config.Getenv(config.TestOwnerEnv); raw != "" {
		log.Warn("ignoring a malformed YARDR_TEST_OWNER", "value", raw)
	}
	var serveErr error
	stopped := false // Serve has returned
wait:
	for {
		select {
		case <-ctx.Done():
			log.Info("yardr server told to stop")
			break wait
		case to = <-replace:
			replacing = true
			log.Info("yardr server draining for a replace", "to", version.Show(to))
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
		case <-ownerGone:
			log.Warn("the test that started the yardr server is gone; stopping", "owner", owner)
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
	stopPreviewLink()
	stopHomeWatch()
	stopWatchCtx()
	<-supervisorDone
	r.settleHandlers(&inflight)
	if ctx.Err() != nil {
		replacing = false // a stop during the replace drain wins over the replace
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		replacing = false
	}
	return to, replacing, serveErr
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

// serveHooks starts the yard's listener: the webhook receiver, and with it
// the receiver of the yard's peers and the calls of the users' devices, on
// its own TCP listener when it has an address: YARDR_HOOK_ADDR where the
// server started, else the yard's hook_addr setting, read once here, so a
// change takes the next start. A receiver that cannot listen is not fatal,
// the yard itself works: two yards given one address is the usual cause, so
// it is logged as an event and left in the receiver's state for doctor to
// say.
//
// It is a separate server from the socket's, and what a port opened to the
// internet reaches is what its three handlers serve, each letting in its
// own callers and nothing else:
//
//   - /hooks/ takes a source's delivery by its signature or token;
//   - /peers/ takes a linked yard's request by its key's signature;
//   - /user/ serves a device that carries a token of a user of this yard
//     the calls docs/api.md names (rpc.Calls), the stream and the blobs, as
//     that user. It is the one place the port reaches the store API: those
//     calls and no other method of it, and nothing of it without a
//     credential (rpc.UserServer). A device of the operator is served
//     the operator's calls besides (rpc.OperatorCalls), a device of
//     another user GiveSecret for their own address in a source alone, and
//     nobody else either. A yard with no device answers 404 there.
//     The one path without a credential is /user/pair, while the operator
//     has a pairing open for a device: whoever has that pairing's code is
//     answered with the device's token, once.
//
// BDP and the rest of the socket are not on it.
//
// The same handlers, the one mux, serve what comes down the yard's link to
// its edge (KeepEdge), with or without an address, less /peers/: so a
// delivery through the edge meets the receiver's own check, a device call
// its own token check, and nothing new decides who is let in.
//
// The function returned stops the listener and the link and has the
// receivers log the refusals they had still counted (hooks.Receiver.Run,
// rpc.UserServer.Run); it is called while the store is open.
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
	// Beside the hooks, under /peers/: the yards this one is linked with
	// (docs/peers.md); under /user/: the devices of its users (docs/api.md,
	// "Over the network"). Each handler serves its own paths and nothing
	// else.
	mux := http.NewServeMux()
	rc := hooks.NewReceiver(st, c.Home, log)
	users := rpc.NewUserServer(st, func(ctx context.Context) string {
		all, _ := st.Settings(ctx)
		return runners.YardName(c.Home, all)
	}, log)
	running, stopRunning := context.WithCancel(context.Background())
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		var wg sync.WaitGroup
		wg.Go(func() { users.Run(running) })
		rc.Run(running)
		wg.Wait()
	}()
	mux.Handle("/hooks/", rc.Handler())
	mux.Handle("/peers/", yards.NewReceiver(st, c.PeerKey(), log).Handler())
	device := users.Handler()
	mux.Handle(rpc.UserPath, device)
	// Without the slash too, or the mux would answer a redirect there to
	// whoever asks.
	mux.Handle(strings.TrimSuffix(rpc.UserPath, "/"), device)
	stopEdge := KeepEdge(ctx, c, st, "", withoutPeers(mux), log)
	stopListener := func() {}
	if l.Addr != "" {
		stopListener = listenHooks(ctx, st, &l, mux, log)
	}
	return func() {
		stopListener()
		stopEdge()
		stopRunning()
		<-flushed
	}
}

// listenHooks serves mux on l's address, or records in l why it cannot.
func listenHooks(ctx context.Context, st *store.Store, l *hooks.Listener, mux http.Handler, log *slog.Logger) (stop func()) {
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
	srv := &http.Server{Handler: mux,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("hook receiver stopped", "err", err)
		}
	}()
	log.Info("hook receiver ready", "addr", ln.Addr().String(), "from", l.From)
	return func() { srv.Close() }
}

// Connect returns a client for the running server, starting one if none
// answers.
func Connect(ctx context.Context, c *config.Config) (*rpc.Client, error) {
	client := rpc.NewClient(c.Socket())
	err := pingOnce(ctx, client)
	if err == nil {
		return client, nil
	}
	// A connect that is not permitted stays so: no wait outlasts it, and a
	// server started here could be reached no better. A sandbox around the
	// command is the usual cause, so the error names the socket and says
	// so, for an agent to ask for the command outside it.
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return nil, fmt.Errorf("cannot connect to the yard's socket %s: %w "+
			"(a sandbox that denies unix sockets does this: run the command outside it)", c.Socket(), denial(err))
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
	info, err := DescribeErr(ctx, c)
	return info, err == nil
}

// DescribeErr is Describe with the reason: the Owner error when no server
// answers, so the upgrade check fails with why. It starts none.
func DescribeErr(ctx context.Context, c *config.Config) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pid, rev, err := rpc.NewClient(c.Socket()).Owner(ctx)
	if err != nil {
		return Info{}, err
	}
	info := Info{PID: pid, Revision: rev}
	if named, err := readPID(c); err == nil && named == pid {
		if fi, err := os.Stat(c.PIDFile()); err == nil {
			info.Since = fi.ModTime()
		}
	}
	return info, nil
}

func ping(ctx context.Context, client *rpc.Client) bool {
	return pingOnce(ctx, client) == nil
}

func pingOnce(ctx context.Context, client *rpc.Client) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return client.Ping(ctx)
}

// denial is the errno of a refused connect, without the request and the
// dial that err wraps it in: Connect's message names the socket itself.
func denial(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return err
}

// SpawnDetached starts cmd in a session of its own, so that it outlives the
// terminal and the command that started it, with what it prints appended to
// the file log. exited gets its end, should it come while the starter still
// runs. The server, the daemon and every command yardr starts in the
// background (yardr web start and its like) are started here; what each is
// given of the starter's environment and directory is the caller's to set
// on cmd.
func SpawnDetached(cmd *exec.Cmd, log string) (exited <-chan error, err error) {
	logf, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}

// executable is the binary Start runs as the server; tests replace it.
var executable = os.Executable

// startWaitDefault is how long Start waits for the server it started to
// answer.
const startWaitDefault = 10 * time.Second

// startWait is how long Start waits for the server it started to answer: ten
// seconds, or the bound the test process named in config.TestStartWaitEnv. A
// test that runs the CLI cannot reach the bound from Go, and a machine under
// load does not always leave a server ten seconds to start in. A bound that
// is missing, malformed or not positive is the ten seconds: a zero wait would
// give up before the first ping.
func startWait() time.Duration {
	wait, err := time.ParseDuration(config.Getenv(config.TestStartWaitEnv))
	if err != nil || wait <= 0 {
		return startWaitDefault
	}
	return wait
}

// Start launches `yardr server run` detached and waits until it answers.
//
// The server outlives the command that started it and serves every later
// one, so it gets none of that command's environment or working directory:
// the first command on a yard is as often an agent's (whose pane holds its
// credentials from an env_file) as the operator's, and a server holding a
// crew member's tokens, or a cwd in a worktree that is removed under it, would
// be wrong for everyone else. It gets what it needs (ServerEnv) and runs
// in the home.
func Start(ctx context.Context, c *config.Config) error {
	self, err := executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "server", "run")
	cmd.Env = ServerEnv(os.Environ(), c.Home)
	cmd.Dir = c.Home
	exited, err := SpawnDetached(cmd, c.LogFile())
	if err != nil {
		return err
	}
	client := rpc.NewClient(c.Socket())
	wait := startWait()
	deadline := time.Now().Add(wait)
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
	return fmt.Errorf("yardr server did not answer within %gs; see %s", wait.Seconds(), c.LogFile())
}

// ServerEnv is the environment a detached server starts with, from the
// starter's environ: PATH and HOME (git, which reads the depots' .yardr/ from
// their base, needs both), the proxy and CA variables (source.IsEgressVar),
// without which a box behind a company's proxy reaches nothing its calls, its
// git and its sources' commands go to, the settings the server reads
// (config.ServerSettings), the test process that started it
// (config.TestOwnerEnv), which the server stops with when that test is
// killed, the test-only tick that shortens its watch (config.TestHomeCheckEnv),
// and the home it serves, in place of whatever YARDR_HOME said.
// Nothing else: no credential (a proxy's address, which may hold one, is the
// operator's to give and goes as it is), no identity (the starter's actor,
// bead, session and branch are the starter's, not the server's), no agent's
// settings. The settings reach it from an operator's shell only: a session
// or a script is started without them (config.WithoutServerSettings).
func ServerEnv(environ []string, home string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == "PATH" || k == "HOME" || source.IsEgressVar(k) || config.IsVar(k, config.TestOwnerEnv, config.TestHomeCheckEnv) || config.IsVar(k, config.ServerSettings...) {
			env = append(env, kv)
		}
	}
	return append(env, config.Env("HOME", home)...)
}

// writePID records this process in the pidfile, and the start time the OS
// reports for it beside the pidfile, by which Stop and alive tell this
// process from a later one with its pid. Each is written by renaming a
// finished file over it: the build a server execs writes the same pid over
// the old build's file while clients in Connect read it to see whether to
// wait; written in place, the file is empty for a moment, the client takes
// the server for gone and starts a second one. The start goes first, so a
// pidfile without one is from before starts were recorded.
func writePID(c *config.Config) error {
	if peer.HaveStat() {
		st, err := peer.Describe(os.Getpid())
		if err != nil {
			return err
		}
		if err := renameFile(c.PIDStart(), []byte(strconv.FormatUint(st.Start, 10)+"\n")); err != nil {
			return err
		}
	}
	return renameFile(c.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"))
}

// renameFile writes b to a finished temporary file and renames it over path.
func renameFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// alive reports whether the pidfile names the running server: a live pid
// whose start time matches the recorded one. A reused pid, and a pidfile
// from before starts were recorded, read as no server; where the OS
// reports no start time, any live pid is the server's, as before.
func alive(c *config.Config) bool {
	pid, err := readPID(c)
	if err != nil {
		return false
	}
	if !peer.HaveStat() {
		return syscall.Kill(pid, 0) == nil
	}
	start, err := readStart(c)
	if err != nil {
		return false
	}
	return peer.Alive(pid, start)
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
	if pid <= 0 {
		return 0, fmt.Errorf("bad pidfile: pid %d names no process", pid)
	}
	return pid, nil
}

// readStart is the start time writePID recorded beside the pidfile.
func readStart(c *config.Config) (uint64, error) {
	b, err := os.ReadFile(c.PIDStart())
	if err != nil {
		return 0, err
	}
	start, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad start file: %w", err)
	}
	return start, nil
}

// Stop terminates the server recorded in the pidfile. A pidfile whose pid
// is gone, or is another process's now, is no server: Stop signals nothing
// and reports that. A pidfile from before starts were recorded names a
// process Stop cannot verify: a dead one is gone as well, and a live one
// is refused, not signalled. Where the OS reports no start time the pid is
// trusted, as before.
func Stop(c *config.Config) error {
	pid, err := readPID(c)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no yardr server is running")
	} else if err != nil {
		return err
	}
	var start uint64
	if peer.HaveStat() {
		var serr error
		start, serr = readStart(c)
		if serr != nil {
			// From before starts were recorded: a dead pid is gone
			// either way, and another user's live one is not the
			// server; a live one of ours may be, and is refused.
			if syscall.Kill(pid, 0) == nil {
				return fmt.Errorf("cannot verify pid %d is the yardr server (the pidfile predates start-time verification); not signalling it", pid)
			}
			return nil
		}
		if !peer.Alive(pid, start) {
			return nil // gone, or the pid reused
		}
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
			return nil // gone between the check and the signal
		}
		return err
	}
	up := func() bool { return syscall.Kill(pid, 0) == nil }
	if peer.HaveStat() {
		up = func() bool { return peer.Alive(pid, start) }
	}
	for i := 0; i < 200 && up(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if up() {
		return fmt.Errorf("yardr server (pid %d) did not stop", pid)
	}
	return nil
}
