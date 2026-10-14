package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type bench struct {
	d, home string
	runs    int
	idle    time.Duration
	workers int
	ctx     context.Context

	web    *exec.Cmd
	webURL string
}

// yardr runs the isolated yard's yardr and returns what it printed. The
// environment is this process's, which is D/e's.
func (b *bench) yardr(args ...string) (string, error) {
	return run(filepath.Join(b.d, "bin", "yardr"), args...)
}

func run(name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// setup makes the yard: the yardmaster, running in the isolated herdr as a
// real yard's does, two git depots that follow one flow, and the groups of
// its stages. Open beads only ever wait in the stages people work, so the
// agent groups exist, are routed to, and start nothing.
func (b *bench) setup() error {
	b.logf("setting up the yard in %s", b.home)
	if _, err := b.yardr("init", "--harness", "claude", "--operator", operator, "--start", "--no-chat"); err != nil {
		return err
	}
	flow := filepath.Join(b.d, "bench.flow")
	if err := os.WriteFile(flow, []byte(flowText), 0o644); err != nil {
		return err
	}
	steps := [][]string{{"flow", "set", flowName, "-f", flow}}
	for _, depot := range depots {
		dir := filepath.Join(b.d, "h", depot)
		if _, err := run("git", "init", "-q", dir); err != nil {
			return err
		}
		if _, err := run("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", "start"); err != nil {
			return err
		}
		steps = append(steps,
			[]string{"depot", "add", depot, dir, "--base", "main"},
			[]string{"depot", "flow", depot, flowName})
	}
	steps = append(steps,
		// The limits of the yard this is modelled on: 50 beads of a
		// stage are held for people at once, the rest wait their turn.
		[]string{"group", "add", "backlog", "--runner", "manual", "--limit", "50", "--members", operator + "," + yardmaster},
		[]string{"group", "add", "decisions", "--runner", "manual", "--limit", "50", "--members", operator + "," + yardmaster},
		// The fill claims for these itself, several beads at once.
		[]string{"group", "add", "builders", "--runner", "herdr", "--limit", "64", "--set", "kind=claude"},
		[]string{"group", "add", "reviewers", "--runner", "herdr", "--limit", "64", "--set", "kind=claude"},
		// The command is the JSON string: --set cmd=true stores the
		// boolean, which the exec runner takes for no command at all.
		[]string{"group", "add", "assembly", "--runner", "exec", "--limit", "64", "--set", `cmd="true"`},
		[]string{"group", "add", "tick", "--runner", "exec", "--limit", "4", "--set", `cmd="true"`},
		[]string{"route", "add", "--stage", "backlog", "--group", "backlog"},
		[]string{"route", "add", "--stage", "decide", "--group", "decisions"},
		[]string{"route", "add", "--stage", "new", "--group", "builders"},
		[]string{"route", "add", "--stage", "review", "--group", "reviewers"},
		[]string{"route", "add", "--stage", "approved", "--group", "assembly"},
		[]string{"route", "add", "--stage", "tick", "--group", "tick"},
	)
	for _, s := range steps {
		if _, err := b.yardr(s...); err != nil {
			return err
		}
	}
	return nil
}

// stop stops what holds the store: the web view, the daemon, the server.
func (b *bench) stop() {
	if b.web != nil {
		_ = b.web.Process.Kill()
		_ = b.web.Wait()
		b.web = nil
	}
	_, _ = b.yardr("daemon", "stop")
	_, _ = b.yardr("server", "stop")
}

var urlRe = regexp.MustCompile(`url=(http://[^ ]+?)/?( |$)`)

// start starts the server, the daemon and a web view, each as its own
// process in the background, the daemon as `yardr init --start` starts it.
func (b *bench) start() error {
	if _, err := b.yardr("server", "start"); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(b.home, "daemon.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	daemon := exec.Command(filepath.Join(b.d, "bin", "yardr"), "daemon")
	daemon.Stdout, daemon.Stderr = logf, logf
	daemon.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := daemon.Start(); err != nil {
		return err
	}
	go daemon.Wait()

	// A free loopback port, never a fixed one: 8791 is the real web view's.
	b.web = exec.Command(filepath.Join(b.d, "bin", "yardr"), "web", "serve", "--addr", "127.0.0.1:0")
	// Whichever of the two it logs to.
	pr, pw := io.Pipe()
	b.web.Stdout, b.web.Stderr = pw, pw
	if err := b.web.Start(); err != nil {
		return err
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if m := urlRe.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
	}()
	select {
	case b.webURL = <-found:
	case <-time.After(30 * time.Second):
		return fmt.Errorf("yardr web serve did not say where it listens")
	}
	return nil
}

// settle waits until the yard has nothing more to do after a start: no new
// event for 5 seconds (the daemon claims the beads that wait for people,
// one write each).
func (b *bench) settle() error {
	last, since := uint64(0), time.Now()
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		head, err := b.head()
		if err != nil {
			return err
		}
		if head != last {
			last, since = head, time.Now()
		} else if time.Since(since) >= 5*time.Second {
			return nil
		}
	}
	return fmt.Errorf("the yard did not settle in 10 minutes")
}

type event struct {
	Seq  uint64         `json:"seq"`
	Bead string         `json:"bead"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
	At   time.Time      `json:"at"`
}

func (b *bench) head() (uint64, error) {
	out, err := b.yardr("events", "-n", "1", "--json")
	if err != nil {
		return 0, err
	}
	var evs []event
	if err := json.Unmarshal([]byte(out), &evs); err != nil || len(evs) == 0 {
		return 0, err
	}
	return evs[0].Seq, nil
}

// create makes a bead in backlog through the CLI and returns its handle.
func (b *bench) create(title string) (string, error) {
	out, err := b.yardr("bead", "create", title, "--depot", depots[0], "-b", "A bead of the benchmark.", "--json")
	if err != nil {
		return "", err
	}
	var bead struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &bead); err != nil || bead.ID == "" {
		return "", fmt.Errorf("bead create printed no id: %s", out)
	}
	return bead.ID, nil
}

// Stat is one measure at one size: for a timed command the median and the
// 95th percentile of its runs in milliseconds, for anything else one value
// in Med.
type Stat struct {
	Med float64 `json:"med"`
	P95 float64 `json:"p95,omitempty"`
}

// quantiles are the median and the 95th percentile (nearest rank: of 20
// runs, the 19th) of ds, in milliseconds.
func quantiles(ds []time.Duration) Stat {
	if len(ds) == 0 {
		return Stat{}
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := func(q float64) float64 {
		i := int(q*float64(len(s))+0.999999) - 1
		return float64(s[min(max(i, 0), len(s)-1)]) / float64(time.Millisecond)
	}
	med := rank(0.5)
	if len(s)%2 == 0 {
		med = float64(s[len(s)/2-1]+s[len(s)/2]) / 2 / float64(time.Millisecond)
	}
	return Stat{Med: med, P95: rank(0.95)}
}

// timed runs a command b.runs times, one after the other, and times each
// from start to exit: what someone at a terminal waits. args gives the
// i-th run's command line; the first word "curl" runs curl, anything else
// the yard's yardr.
func (b *bench) timed(args func(i int) []string) (Stat, error) {
	var ds []time.Duration
	for i := range b.runs {
		a := args(i)
		name := filepath.Join(b.d, "bin", "yardr")
		if a[0] == "curl" {
			name, a = "curl", a[1:]
		}
		cmd := exec.Command(name, a...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		t := time.Now()
		err := cmd.Run()
		ds = append(ds, time.Since(t))
		if err != nil {
			return Stat{}, fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(a, " "), err, strings.TrimSpace(stderr.String()))
		}
	}
	return quantiles(ds), nil
}

// same is timed for a command line that does not change between runs.
func same(args ...string) func(int) []string { return func(int) []string { return args } }

// Proc is what ps says of one process.
type Proc struct {
	RSSMB float64 `json:"rss_mb"`
	CPU   float64 `json:"cpu_secs"` // user and system time since it started
}

// pidOf reads the pid a server or daemon left in its home.
func (b *bench) pidOf(file string) (int, error) {
	data, err := os.ReadFile(filepath.Join(b.home, file))
	if err != nil {
		return 0, err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	pid, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	return strconv.Atoi(pid)
}

func procOf(pid int) (Proc, error) {
	out, err := run("ps", "-o", "rss=,time=", "-p", strconv.Itoa(pid))
	if err != nil {
		return Proc{}, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return Proc{}, fmt.Errorf("ps -p %d printed %q", pid, out)
	}
	kb, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return Proc{}, err
	}
	cpu, err := cpuSecs(f[1])
	return Proc{RSSMB: kb / 1024, CPU: cpu}, err
}

// cpuSecs reads ps's cumulative CPU time: macOS prints minutes:seconds.hundredths,
// procps [days-]hours:minutes:seconds, whole seconds only.
func cpuSecs(s string) (float64, error) {
	days := 0.0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		v, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, fmt.Errorf("ps time %q", s)
		}
		days, s = v, rest
	}
	total := 0.0
	for _, part := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("ps time %q", s)
		}
		total = total*60 + v
	}
	return days*86400 + total, nil
}

// watcher follows the yard's events through `yardr watch`, one process for
// a whole measure, so that waiting for an event starts no command that
// would itself load the server.
type watcher struct {
	cmd *exec.Cmd
	evs chan event
}

func (b *bench) watch() (*watcher, error) {
	head, err := b.head()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(b.d, "bin", "yardr"), "watch", "--json", "--after", strconv.FormatUint(head, 10))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &watcher{cmd: cmd, evs: make(chan event, 4096)}
	go func() {
		defer close(w.evs)
		dec := json.NewDecoder(pipe)
		for {
			var e event
			if dec.Decode(&e) != nil {
				return
			}
			w.evs <- e
		}
	}()
	return w, nil
}

func (w *watcher) stop() {
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
}

// Dispatch is the wait of a bead for its session, from the commit of the
// advance that made it ready to the commit of each event, by the times the
// store gave them.
type Dispatch struct {
	Claimed Stat `json:"claimed"`
	Started Stat `json:"started"`
}

// dispatch measures how long a bead advanced into tick, the stage an exec
// group whose command is `true` works, waits for its session to start. The
// README says the dispatcher wakes on the committed write; a wait near the
// daemon's 30s safety-net pass would say it did not.
func (b *bench) dispatch() (*Dispatch, []string, error) {
	w, err := b.watch()
	if err != nil {
		return nil, nil, err
	}
	defer w.stop()
	var claimed, started []time.Duration
	var made []string
	for i := range b.runs {
		id, err := b.create(fmt.Sprintf("bench dispatch %d", i))
		if err != nil {
			return nil, made, err
		}
		made = append(made, id)
		if _, err := b.yardr("bead", "advance", id, "--to", "tick"); err != nil {
			return nil, made, err
		}
		var t0 time.Time
		timeout := time.After(2 * time.Minute)
	wait:
		for {
			select {
			case e, ok := <-w.evs:
				if !ok {
					return nil, made, fmt.Errorf("yardr watch ended")
				}
				if e.Bead != id {
					continue
				}
				switch {
				case e.Kind == "advanced" && e.Data["to"] == "tick":
					t0 = e.At
				case e.Kind == "claimed" && !t0.IsZero():
					claimed = append(claimed, e.At.Sub(t0))
				case e.Kind == "started" && !t0.IsZero():
					started = append(started, e.At.Sub(t0))
					break wait
				}
			case <-timeout:
				return nil, made, fmt.Errorf("bead %s: no session started within 2 minutes of its advance to tick", id)
			}
		}
		// Closing it ends the session, which frees its slot for the next.
		if _, err := b.yardr("bead", "close", id, "--reason", "benchmark"); err != nil {
			return nil, made, err
		}
	}
	return &Dispatch{Claimed: quantiles(claimed), Started: quantiles(started)}, made, nil
}

// Load is one command written by several clients at once.
type Load struct {
	Clients int     `json:"clients"`
	PerSec  float64 `json:"per_sec"` // commands finished per second, all clients together
	Stat
}

// load runs a write command from clients at once, each a loop of per runs
// of the CLI, as that many people or agents typing it would: every run is a
// process that connects, writes and waits for its commit.
func (b *bench) load(clients, per int, args func(client, i int) []string) (Load, error) {
	var mu sync.Mutex
	var ds []time.Duration
	var first error
	var wg sync.WaitGroup
	start := time.Now()
	for c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				t := time.Now()
				_, err := b.yardr(args(c, i)...)
				d := time.Since(t)
				mu.Lock()
				ds = append(ds, d)
				if err != nil && first == nil {
					first = err
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	took := time.Since(start)
	return Load{Clients: clients, PerSec: float64(len(ds)) / took.Seconds(), Stat: quantiles(ds)}, first
}

// under times a read command while clients write notes without pause.
func (b *bench) under(clients int, beads []string, read ...string) (Stat, error) {
	var stop atomic.Bool
	var wg sync.WaitGroup
	for c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				_, _ = b.yardr("bead", "note", beads[c%len(beads)], fmt.Sprintf("note %d under load", i))
			}
		}()
	}
	time.Sleep(time.Second) // every writer is writing
	st, err := b.timed(same(read...))
	stop.Store(true)
	wg.Wait()
	return st, err
}

// Machine is where the benchmark ran.
type Machine struct {
	OS    string  `json:"os"`
	Cores int     `json:"cores"`
	MemGB float64 `json:"mem_gb,omitempty"`
}

func machine() Machine {
	m := Machine{OS: runtime.GOOS + "/" + runtime.GOARCH, Cores: runtime.NumCPU()}
	if out, err := run("sysctl", "-n", "hw.memsize"); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(out), 64); err == nil {
			m.MemGB = v / (1 << 30)
		}
	} else if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 1 && f[0] == "MemTotal:" {
			if v, err := strconv.ParseFloat(f[1], 64); err == nil {
				m.MemGB = v / (1 << 20)
			}
		}
	}
	return m
}

// loadAvg is the machine's 1-minute load average, or -1 when it cannot be
// read.
func loadAvg() float64 {
	text := ""
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		text = string(data)
	} else if out, err := run("sysctl", "-n", "vm.loadavg"); err == nil {
		text = strings.Trim(strings.TrimSpace(out), "{} ")
	}
	if f := strings.Fields(text); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			return v
		}
	}
	return -1
}
