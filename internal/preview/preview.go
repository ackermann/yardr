// Package preview is the yard's preview supervisor: it runs approved
// previews as the yard user, keeps them running, captures their logs and
// carries their traffic. It is part of yardr server; the registry it
// reads is the store's previews table, and its yard-side endpoint is
// served over the preview link role beside the yard's own link. The
// edge's origin and the approval card are other beads: this package
// names no preview host.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Store is what the supervisor needs of the yard's store. *store.Store
// satisfies it in the server; tests give a throwaway store the same way.
type Store interface {
	Previews(ctx context.Context) ([]store.Preview, error)
	GetPreview(ctx context.Context, app string) (*store.Preview, error)
	UpdatePreview(ctx context.Context, app string, upd store.PreviewUpdate) (*store.Preview, error)
	CheckPreviewTree(ctx context.Context, app string) error
	GetBead(ctx context.Context, id string) (*store.Bead, error)
	LogPreviewEvent(ctx context.Context, app, kind, detail string) error
	Settings(ctx context.Context) (map[string]string, error)
}

// Defaults: a Supervisor field left at its zero value takes the default
// below, so tests may shorten the clocks without changing the shipped
// figures.
const (
	defaultPoll       = time.Second
	defaultBindWait   = 10 * time.Second
	defaultMemoryTick = 10 * time.Second
	defaultMemoryHold = time.Minute
	defaultStartWait  = 10 * time.Second
)

// crashBackoffs is how long the supervisor waits before restarting a
// crashed preview again: immediately, then 5, 25 and 125 seconds. A fifth
// unclean exit in ten minutes leaves it crashed.
var crashBackoffs = []time.Duration{0, 5 * time.Second, 25 * time.Second, 125 * time.Second}

// crashWindow and crashLimit are the flapping rule: an unclean exit that
// brings the count inside crashWindow to crashLimit leaves the preview
// crashed instead of restarting it.
const (
	crashWindow = 10 * time.Minute
	crashLimit  = 5
)

// Supervisor keeps a yard's approved previews running.
type Supervisor struct {
	Store Store
	// Home is the yard's home: the preview logs live under it.
	Home string
	Log  *slog.Logger

	// Poll is how often the supervisor reconciles the registry with
	// its processes; BindWait how long a start may take to bind its
	// port; MemoryTick and MemoryHold shape the RSS watcher; MemLimit
	// is the RSS cap in bytes. Zero takes the default beside it.
	Poll, BindWait, MemoryTick, MemoryHold time.Duration
	MemLimit                               int64
	// Backoffs replaces crashBackoffs (tests shorten it).
	Backoffs []time.Duration
	// WatchPoll and WatchSettle shape what a watch's stream reads of its
	// directory (watch.go); zero takes the default beside them.
	WatchPoll, WatchSettle time.Duration

	mu       sync.Mutex
	running  map[string]*proc
	inflight map[string]int
	over     map[string]time.Time // app -> first moment over the memory cap
	startMu  sync.Mutex           // one start at a time
	rebooted bool
}

type proc struct {
	cmd      *exec.Cmd
	pgid     int
	started  time.Time
	stopping bool
	logsDone <-chan struct{}
	output   *rotateWriter
	done     chan struct{}
}

// New returns a supervisor for the yard at home.
func New(st Store, home string, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{Store: st, Home: home, Log: log,
		running: map[string]*proc{}, inflight: map[string]int{}, over: map[string]time.Time{}}
}

func (s *Supervisor) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return defaultPoll
}

func (s *Supervisor) bindWait() time.Duration {
	if s.BindWait > 0 {
		return s.BindWait
	}
	return defaultBindWait
}

func (s *Supervisor) backoffs() []time.Duration {
	if len(s.Backoffs) > 0 {
		return s.Backoffs
	}
	return crashBackoffs
}

// logPath is the preview's captured output file.
func (s *Supervisor) logPath(app string) string {
	return filepath.Join(s.Home, "previews", app+".log")
}

// Run supervises until ctx ends, then stops every preview it runs: a
// preview does not outlive the supervisor that watches it, and a server
// that replaces itself starts its successor's previews from the
// registry as stopped.
func (s *Supervisor) Run(ctx context.Context) {
	tick := time.NewTicker(s.poll())
	defer tick.Stop()
	memTick := time.NewTicker(s.memoryTick())
	defer memTick.Stop()
	if ctx.Err() != nil {
		return
	}
	s.reconcile(ctx, true)
	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return
		case <-tick.C:
			s.reconcile(ctx, false)
		case <-memTick.C:
			s.checkMemory(ctx)
		}
	}
}

func (s *Supervisor) memoryTick() time.Duration {
	if s.MemoryTick > 0 {
		return s.MemoryTick
	}
	return defaultMemoryTick
}

// reconcile brings the processes in line with the registry: approved
// previews start, stopped ones are killed, idle and aged ones stop.
func (s *Supervisor) reconcile(ctx context.Context, first bool) {
	all, err := s.Store.Previews(ctx)
	if err != nil {
		s.Log.Warn("preview: the registry is not read", "err", err)
		return
	}
	settings, _ := s.Store.Settings(ctx)
	idle := store.PreviewIdle(settings)
	now := time.Now()
	if first && !s.rebooted {
		s.rebooted = true
		// A box reboot, or a server restart, leaves every preview
		// stopped for its person to start. A watch runs no code to
		// resurrect, so it stands: an upgrade restarts the server.
		for _, p := range all {
			if p.State == store.PreviewRunning && p.Kind != store.PreviewKindWatch {
				detail := "reboot"
				pid := -1
				if _, err := s.Store.UpdatePreview(ctx, p.App, store.PreviewUpdate{
					State: store.PreviewStopped, StateDetail: &detail, PID: pid}); err != nil {
					s.Log.Warn("preview: reboot stop not recorded", "app", p.App, "err", err)
				}
			}
		}
	}
	for _, p := range all {
		switch p.State {
		case store.PreviewApproved:
			if err := s.Start(ctx, p.App); err != nil {
				s.Log.Debug("preview: not started", "app", p.App, "err", err)
			}
		case store.PreviewRunning:
			if p.Kind == store.PreviewKindWatch {
				s.reapWatch(ctx, p, now, idle)
				continue
			}
			s.mu.Lock()
			pr := s.running[p.App]
			s.mu.Unlock()
			if pr == nil {
				continue // the start path or a restart owns it
			}
			last := pr.started
			if p.LastRequestAt != nil {
				last = *p.LastRequestAt
			}
			switch {
			case now.Sub(pr.started) > store.PreviewAgeLimit:
				s.stop(ctx, p.App, "aged")
			case now.Sub(last) > idle:
				s.mu.Lock()
				active := s.inflight[p.App] > 0
				s.mu.Unlock()
				if !active {
					s.stop(ctx, p.App, "idle")
				}
			}
		case store.PreviewPending, store.PreviewStopped, store.PreviewRefused, store.PreviewCrashed, store.PreviewFailed:
			// A pending row owns no process: one that still runs is that
			// of a row whose approval was taken back while it ran.
			s.mu.Lock()
			pr := s.running[p.App]
			s.mu.Unlock()
			if pr != nil {
				s.kill(p.App)
			}
			if p.State == store.PreviewStopped && p.StateDetail == store.PreviewRestartDetail {
				s.finishRestart(ctx, p.App)
			}
		}
	}
	_ = first
}

// Start starts the approved preview app. A row with no approval record
// is refused, whatever asks, and so is one whose depot tree no longer
// reads as it did when the approval was given: its approval is taken
// back and it waits for a new one (store.CheckPreviewTree). A running
// preview is left as it is. At most PreviewRunningLimit run at once, and
// one start at a time: a start past either limit waits for a later pass.
func (s *Supervisor) Start(ctx context.Context, app string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.running[app] != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	p, err := s.Store.GetPreview(ctx, app)
	if err != nil {
		return err
	}
	if p.Approval == nil {
		return fmt.Errorf("preview %s: no approval record; a start with none is refused", app)
	}
	if p.Command != p.Approval.Command || p.Dir != p.Approval.Dir || p.Port != p.Approval.Port {
		detail := "changed since approval"
		_, _ = s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{State: store.PreviewRefused, StateDetail: &detail})
		_ = s.Store.LogPreviewEvent(ctx, app, "preview_refused", detail)
		return fmt.Errorf("preview %s: its command, directory or port changed since approval; the change waits for its own approval", app)
	}
	if p.State == store.PreviewRunning {
		return fmt.Errorf("preview %s is recorded running but has no process in this supervisor", app)
	}
	if p.State != store.PreviewApproved {
		return fmt.Errorf("preview %s is %s; it is not approved to start", app, p.State)
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	running := len(s.running)
	s.mu.Unlock()
	if running >= store.PreviewRunningLimit {
		return fmt.Errorf("preview %s: %d previews are already running, the most at once", app, store.PreviewRunningLimit)
	}
	// The tree is read last, just before the command is spawned, so the
	// approval is judged on the tree the command will run in.
	if err := s.Store.CheckPreviewTree(ctx, app); err != nil {
		return err
	}
	return s.spawn(ctx, p)
}

// spawn runs p's approved command and records it running.
func (s *Supervisor) spawn(ctx context.Context, p *store.Preview) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if addr := listeningAddress(p.Port); addr != "" {
		return s.failStart(ctx, p, "port already in use at "+addr)
	}
	if err := os.MkdirAll(filepath.Join(s.Home, "previews"), 0o700); err != nil {
		return s.failStart(ctx, p, "log directory: "+err.Error())
	}
	out, err := newRotateWriter(s.logPath(p.App))
	if err != nil {
		return s.failStart(ctx, p, "log file: "+err.Error())
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		out.Close()
		return s.failStart(ctx, p, "log pipe: "+err.Error())
	}
	cmd := exec.Command("sh", "-c", p.Approval.Command)
	cmd.Dir = p.Approval.Dir
	cmd.Env = previewEnv(p.App, p.Approval.Port)
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		writer.Close()
		reader.Close()
		out.Close()
		return s.failStart(ctx, p, err.Error())
	}
	writer.Close()
	logsDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, reader)
		reader.Close()
		close(logsDone)
	}()
	pr := &proc{cmd: cmd, started: time.Now(), logsDone: logsDone, output: out, done: make(chan struct{})}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		pr.pgid = pgid
	} else {
		pr.pgid = cmd.Process.Pid
	}
	s.mu.Lock()
	s.running[p.App] = pr
	s.mu.Unlock()
	// The waiter holds its exit until the registry decides whether this
	// start won a concurrent stop or forget. That keeps a fast clean exit
	// from racing the write that records the process as running.
	recorded := make(chan bool, 1)
	go func() {
		err := cmd.Wait()
		if <-recorded {
			s.onExit(ctx, p.App, pr, err)
			return
		}
		terminateGroup(pr.pgid)
		<-pr.logsDone
		pr.output.Close()
		close(pr.done)
	}()
	started := pr.started
	detail := ""
	if err := ctx.Err(); err != nil {
		recorded <- false
		s.kill(p.App)
		return err
	}
	if _, err := s.Store.UpdatePreview(ctx, p.App, store.PreviewUpdate{
		ExpectedState: store.PreviewApproved,
		State:         store.PreviewRunning, StateDetail: &detail, PID: cmd.Process.Pid, StartedAt: &started,
	}); err != nil {
		recorded <- false
		s.kill(p.App)
		return fmt.Errorf("preview %s: start not recorded: %w", p.App, err)
	}
	recorded <- true
	_ = s.Store.LogPreviewEvent(ctx, p.App, "preview_started", strconv.Itoa(cmd.Process.Pid))
	go s.watchBind(ctx, p.App, p.Approval.Port, pr)
	return nil
}

// failStart leaves a proposal failed when the supervisor cannot start it,
// instead of retrying a permanent error on every reconciliation pass.
func (s *Supervisor) failStart(ctx context.Context, p *store.Preview, detail string) error {
	_, err := s.Store.UpdatePreview(ctx, p.App, store.PreviewUpdate{
		ExpectedState: store.PreviewApproved,
		State:         store.PreviewFailed,
		StateDetail:   &detail,
	})
	if err == nil {
		_ = s.Store.LogPreviewEvent(ctx, p.App, "preview_failed", detail)
	}
	return fmt.Errorf("preview %s: %s", p.App, detail)
}

// onExit records a preview's exit: a clean exit has stopped, an unclean
// one restarts under the crash rule.
func (s *Supervisor) onExit(ctx context.Context, app string, pr *proc, err error) {
	// The command may have left a background child in its process group.
	// Its inherited log pipe must close before this exit can be recorded.
	terminateGroup(pr.pgid)
	<-pr.logsDone
	pr.output.Close()
	s.mu.Lock()
	if s.running[app] == pr {
		delete(s.running, app)
	}
	stopping := pr.stopping
	s.mu.Unlock()
	close(pr.done)
	if stopping || ctx.Err() != nil {
		return
	}
	p, gerr := s.Store.GetPreview(ctx, app)
	if gerr != nil || p.State != store.PreviewRunning {
		return // stopped, refused or forgotten while it ran
	}
	pid := -1
	if err == nil {
		detail := "exited"
		if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
			ExpectedState: store.PreviewRunning,
			State:         store.PreviewStopped, StateDetail: &detail, PID: pid,
		}); err == nil {
			_ = s.Store.LogPreviewEvent(ctx, app, "preview_stopped", detail)
		}
		return
	}
	now := time.Now()
	exits := append(pruneExits(p.Exits, now), now)
	if len(exits) >= crashLimit {
		detail := err.Error()
		if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
			ExpectedState: store.PreviewRunning,
			State:         store.PreviewCrashed, StateDetail: &detail, PID: pid, Exits: exits,
		}); err == nil {
			_ = s.Store.LogPreviewEvent(ctx, app, "preview_crashed", detail)
		}
		return
	}
	restarts := p.RestartCount + 1
	detail := ""
	if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
		ExpectedState: store.PreviewRunning,
		StateDetail:   &detail, PID: pid, Restarts: &restarts, Exits: exits,
	}); err != nil {
		return
	}
	_ = s.Store.LogPreviewEvent(ctx, app, "preview_exited", err.Error())
	backoffs := s.backoffs()
	delay := backoffs[min(restarts-1, len(backoffs)-1)]
	go func() {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
			s.restart(ctx, app)
		}
	}()
}

func terminateGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitGroupGone(pgid, 2*time.Second) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	waitGroupGone(pgid, 2*time.Second)
}

func waitGroupGone(pgid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// restart changes a crash's running row back to approved only if no stop
// or forget won while its backoff was elapsing.
func (s *Supervisor) restart(ctx context.Context, app string) {
	cur, err := s.Store.GetPreview(ctx, app)
	if err != nil || cur.State != store.PreviewRunning || cur.Approval == nil {
		return
	}
	detail := ""
	if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
		ExpectedState: store.PreviewRunning,
		State:         store.PreviewApproved,
		StateDetail:   &detail,
	}); err != nil {
		return
	}
	if err := s.Start(ctx, app); err != nil {
		s.Log.Debug("preview: crash restart not started", "app", app, "err", err)
	}
}

func pruneExits(exits []time.Time, now time.Time) []time.Time {
	out := exits[:0]
	for _, at := range exits {
		if now.Sub(at) < crashWindow {
			out = append(out, at)
		}
	}
	return out
}

// watchBind checks, within BindWait of a start, that the preview holds
// its approved port on loopback alone. A wider bind is stopped and
// refused; no bind at all is failed.
func (s *Supervisor) watchBind(ctx context.Context, app string, port int, pr *proc) {
	deadline := time.Now().Add(s.bindWait())
	nextWideCheck := time.Time{}
	bound := false
	for {
		now := time.Now()
		if nextWideCheck.IsZero() || !now.Before(nextWideCheck) {
			if wide := nonLoopbackBind(port); wide != "" {
				s.refuseBind(ctx, app, wide)
				return
			}
			nextWideCheck = now.Add(250 * time.Millisecond)
		}
		if !bound && loopbackHost(port) != "" {
			bound = true
		}
		s.mu.Lock()
		gone := s.running[app] != pr
		s.mu.Unlock()
		if gone || ctx.Err() != nil {
			return
		}
		if !bound && now.After(deadline) {
			detail := "no listen"
			s.kill(app)
			if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
				ExpectedState: store.PreviewRunning,
				State:         store.PreviewFailed, StateDetail: &detail, PID: -1,
			}); err == nil {
				_ = s.Store.LogPreviewEvent(ctx, app, "preview_failed", detail)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (s *Supervisor) refuseBind(ctx context.Context, app, addr string) {
	detail := "bound " + addr
	s.kill(app)
	if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
		ExpectedState: store.PreviewRunning,
		State:         store.PreviewRefused, StateDetail: &detail, PID: -1,
	}); err == nil {
		_ = s.Store.LogPreviewEvent(ctx, app, "preview_refused", detail)
	}
}

// loopbackHost returns the approved loopback address the preview answers
// on. Both loopback addresses are valid — 127.0.0.1 first, then ::1 —
// though an app only needs one.
func loopbackHost(port int) string {
	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return host
		}
	}
	return ""
}

func listeningAddress(port int) string {
	if host := loopbackHost(port); host != "" {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	return nonLoopbackBind(port)
}

// nonLoopbackBind returns an address other than 127.0.0.1 or ::1 that
// answers on the preview port. Dialling the machine's interface addresses
// catches wildcard and interface-specific binds, including link-local IPv6.
func nonLoopbackBind(port int) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsMulticast() || ip.IsLoopback() {
				continue
			}
			host := ip.String()
			if ip.To4() == nil && ip.IsLinkLocalUnicast() {
				host += "%" + iface.Name
			}
			target := net.JoinHostPort(host, strconv.Itoa(port))
			if c, err := net.DialTimeout("tcp", target, 50*time.Millisecond); err == nil {
				c.Close()
				return target
			}
		}
	}
	return ""
}

// finishRestart completes a restart the person asked for on the card:
// the process is stopped above, so the approved form is approved again
// and starts on the next pass, on the tree the ask found unchanged. The
// row is read again first, so a stop or forget that won while the
// process stopped stands instead of the restart.
func (s *Supervisor) finishRestart(ctx context.Context, app string) {
	cur, err := s.Store.GetPreview(ctx, app)
	if err != nil || cur.State != store.PreviewStopped || cur.StateDetail != store.PreviewRestartDetail {
		return
	}
	detail := ""
	if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
		ExpectedState: store.PreviewStopped,
		State:         store.PreviewApproved, StateDetail: &detail,
	}); err != nil {
		s.Log.Debug("preview: restart not started", "app", app, "err", err)
	}
}

// stop stops the preview app for the named reason ("idle", "aged",
// "memory", "stopped"): its process group is killed and the row is
// left stopped with the reason, its approval intact.
func (s *Supervisor) stop(ctx context.Context, app, reason string) {
	s.kill(app)
	if _, err := s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{
		ExpectedState: store.PreviewRunning,
		State:         store.PreviewStopped, StateDetail: &reason, PID: -1,
	}); err == nil {
		_ = s.Store.LogPreviewEvent(ctx, app, "preview_stopped", reason)
	}
}

func (s *Supervisor) kill(app string) {
	s.mu.Lock()
	pr := s.running[app]
	if pr != nil {
		pr.stopping = true
		delete(s.running, app)
	}
	s.mu.Unlock()
	if pr == nil {
		return
	}
	if pr.pgid > 0 {
		_ = syscall.Kill(-pr.pgid, syscall.SIGTERM)
	}
	select {
	case <-pr.done:
	case <-time.After(2 * time.Second):
		if pr.pgid > 0 {
			_ = syscall.Kill(-pr.pgid, syscall.SIGKILL)
		}
		<-pr.done
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	apps := make([]string, 0, len(s.running))
	for app := range s.running {
		apps = append(apps, app)
	}
	s.mu.Unlock()
	for _, app := range apps {
		s.kill(app)
	}
}

// checkMemory stops a preview whose process-group RSS has stood over
// the cap for the full hold: a watcher, not a hard limit.
func (s *Supervisor) checkMemory(ctx context.Context) {
	limit := s.MemLimit
	if limit <= 0 {
		limit = store.PreviewMemoryLimit
	}
	hold := s.MemoryHold
	if hold <= 0 {
		hold = defaultMemoryHold
	}
	s.mu.Lock()
	type target struct {
		app  string
		pgid int
	}
	var targets []target
	for app, pr := range s.running {
		targets = append(targets, target{app, pr.pgid})
	}
	s.mu.Unlock()
	now := time.Now()
	for _, t := range targets {
		rss, err := processGroupRSS(t.pgid)
		if err != nil || rss <= limit {
			s.mu.Lock()
			delete(s.over, t.app)
			s.mu.Unlock()
			continue
		}
		s.mu.Lock()
		since, seen := s.over[t.app]
		if !seen {
			s.over[t.app] = now
		}
		s.mu.Unlock()
		if seen && now.Sub(since) >= hold {
			s.mu.Lock()
			delete(s.over, t.app)
			s.mu.Unlock()
			s.stop(ctx, t.app, "memory")
		}
	}
}

// previewEnv is the environment a preview starts with: the yard's
// ordinary environment less what a listener does not need — the
// yard's own sockets and session, the agent runtime's, and any
// credential carried in a name — plus the three names an app may read
// its port by. Files remain readable; this is hygiene, not a wall,
// and is described as such.
func previewEnv(app string, port int) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "YARDR_"), strings.HasPrefix(k, "HERDR_"):
		case k == "PORT", k == "PREVIEW_APP", k == "PREVIEW_PORT":
		case strings.Contains(k, "TOKEN") || strings.Contains(k, "SECRET") || strings.HasSuffix(k, "_KEY"),
			strings.Contains(k, "PASSWORD") || strings.Contains(k, "PASSWD") || strings.Contains(k, "CREDENTIAL"),
			// A socket for another process (the ssh agent's above all)
			// would let a long-lived listener act as the yard's user
			// elsewhere for as long as it runs.
			strings.HasSuffix(k, "_SOCK") || strings.HasSuffix(k, "_SOCKET"):
		default:
			out = append(out, kv)
		}
	}
	p := strconv.Itoa(port)
	return append(out, "PREVIEW_APP="+app, "PREVIEW_PORT="+p, "PORT="+p)
}

// Running reports whether the supervisor is running the preview app.
func (s *Supervisor) Running(app string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[app] != nil
}
