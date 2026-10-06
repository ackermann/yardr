package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
)

// Results is one run of the benchmark.
type Results struct {
	Machine Machine   `json:"machine"`
	Started time.Time `json:"started"`
	Took    string    `json:"took"`
	Rounds  []Round   `json:"rounds"`
}

// Round is everything measured at one size.
type Round struct {
	Size     int     `json:"size"`
	Fill     Fill    `json:"fill"`
	Beads    int     `json:"beads"`    // work beads when the commands were timed
	Events   uint64  `json:"events"`   // the log's newest sequence number then
	Sessions int     `json:"sessions"` // live sessions then
	Load0    float64 `json:"load_start"`
	Load1    float64 `json:"load_end"`
	StoreMB  float64 `json:"store_mb"`

	// Timed is each timed command by its name in the table.
	Timed    map[string]Stat `json:"timed"`
	Dispatch Dispatch        `json:"dispatch"`

	IdleSecs float64         `json:"idle_secs"`
	Procs    map[string]Proc `json:"procs"`    // server, daemon, web: RSS after the idle time
	IdleCPU  map[string]Stat `json:"idle_cpu"` // CPU seconds each used in it

	Notes      []Load `json:"note_load"`
	Creates    []Load `json:"create_load"`
	ReadyUnder Stat   `json:"ready_under_16_writers"`

	Engine Engine `json:"engine"`
}

// timedOrder is the timed commands in the order of the table: the ones the
// bead asked for, then the filters and listings added to see which read
// every record.
var timedOrder = []string{
	"version (no server)", "server status",
	"ready", "bead list", "bead list -a", "bead show", "prime --bead", "events -n 50", "inbox", "session list",
	"bead create", "bead note", "bead advance", "web /beads (curl)",
	"bead list --stage decide", "bead list --depot beta", "bead show (closed)", "events --bead", "session list -a", "mail --with",
}

func fileMB(path string) float64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(st.Size()) / (1 << 20)
}

// round fills the yard to n beads and measures it there.
func (b *bench) round(n int) (*Round, error) {
	r := &Round{Size: n, Timed: map[string]Stat{}, Procs: map[string]Proc{}, IdleCPU: map[string]Stat{}}
	b.stop()
	b.logf("filling to %d beads", n)
	f, err := fill(b.ctx, filepath.Join(b.home, "yard.db"), n, b.workers)
	if err != nil {
		return nil, fmt.Errorf("fill: %w", err)
	}
	r.Fill = *f
	b.logf("filled: %d beads, %d events in %.0fs", f.Beads, f.Events, f.Secs)
	if err := b.start(); err != nil {
		return nil, err
	}
	if err := b.settle(); err != nil {
		return nil, err
	}
	r.Load0 = loadAvg()
	r.StoreMB = fileMB(filepath.Join(b.home, "yard.db"))
	if r.Events, err = b.head(); err != nil {
		return nil, err
	}
	if r.Beads, err = b.count("bead", "list", "-a", "--json"); err != nil {
		return nil, err
	}
	if r.Sessions, err = b.count("session", "list", "--json"); err != nil {
		return nil, err
	}

	b.logf("timing commands (%d beads, %d events, %d live sessions, store %.0f MB)", r.Beads, r.Events, r.Sessions, r.StoreMB)
	noted, err := b.create("bench note")
	if err != nil {
		return nil, err
	}
	var moved []string
	for i := range b.runs {
		id, err := b.create(fmt.Sprintf("bench advance %d", i))
		if err != nil {
			return nil, err
		}
		moved = append(moved, id)
	}
	cmds := map[string]func(int) []string{
		"version (no server)": same("version"),
		"server status":       same("server", "status"),
		"ready":               same("ready"),
		"bead list":           same("bead", "list"),
		"bead list -a":        same("bead", "list", "-a"),
		"bead show":           same("bead", "show", f.Open),
		"prime --bead":        same("prime", "--bead", f.Open),
		"events -n 50":        same("events", "-n", "50"),
		"inbox":               same("inbox", "--crew", yardmaster),
		"session list":        same("session", "list"),
		"bead create": func(i int) []string {
			return []string{"bead", "create", fmt.Sprintf("bench create %d", i), "--depot", depots[0], "-b", "A bead of the benchmark."}
		},
		"bead note": func(i int) []string {
			return []string{"bead", "note", noted, fmt.Sprintf("note %d of the benchmark", i)}
		},
		"bead advance":             func(i int) []string { return []string{"bead", "advance", moved[i], "--to", "decide"} },
		"web /beads (curl)":        same("curl", "-sf", "-o", os.DevNull, b.webURL+"/beads"),
		"bead list --stage decide": same("bead", "list", "--stage", "decide"),
		"bead list --depot beta":   same("bead", "list", "--depot", depots[1]),
		"bead show (closed)":       same("bead", "show", f.Closed),
		"events --bead":            same("events", "--bead", f.Closed),
		"session list -a":          same("session", "list", "-a"),
		"mail --with":              same("mail", "--with", yardmaster),
	}
	// Once unmeasured, the reads: the server was just started, and the
	// first reader of a page waits for the file.
	for _, name := range timedOrder {
		if !strings.HasPrefix(name, "bead create") && !strings.HasPrefix(name, "bead note") && !strings.HasPrefix(name, "bead advance") {
			a := cmds[name](0)
			if a[0] == "curl" {
				_, err = run("curl", a[1:]...)
			} else {
				_, err = b.yardr(a...)
			}
			if err != nil {
				return nil, err
			}
		}
	}
	for _, name := range timedOrder {
		if r.Timed[name], err = b.timed(cmds[name]); err != nil {
			return nil, err
		}
	}

	b.logf("dispatch delay")
	d, _, err := b.dispatch()
	if err != nil {
		return nil, err
	}
	r.Dispatch = *d

	b.logf("idle for %s", b.idle)
	if err := b.idleCPU(r); err != nil {
		return nil, err
	}

	b.logf("writers at once")
	var writers []string
	for i := range 16 {
		id, err := b.create(fmt.Sprintf("bench writer %d", i))
		if err != nil {
			return nil, err
		}
		writers = append(writers, id)
	}
	for _, clients := range []int{1, 4, 16} {
		l, err := b.load(clients, b.runs, func(c, i int) []string {
			return []string{"bead", "note", writers[c], fmt.Sprintf("note %d of writer %d", i, c)}
		})
		if err != nil {
			return nil, err
		}
		r.Notes = append(r.Notes, l)
		// Half as many: each leaves a bead behind, and the yard is to
		// stay near its size.
		l, err = b.load(clients, max(b.runs/2, 1), func(c, i int) []string {
			return []string{"bead", "create", fmt.Sprintf("bench load %d.%d", c, i), "--depot", depots[1], "-b", "A bead of the benchmark."}
		})
		if err != nil {
			return nil, err
		}
		r.Creates = append(r.Creates, l)
	}
	if r.ReadyUnder, err = b.under(16, writers, "ready"); err != nil {
		return nil, err
	}

	b.logf("the store: backup, prune, compact")
	if err := b.engine(r, f); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	r.Load1 = loadAvg()
	return r, b.closeProbes()
}

// count is how many records a --json listing prints.
func (b *bench) count(args ...string) (int, error) {
	out, err := b.yardr(args...)
	if err != nil {
		return 0, err
	}
	var rows []json.RawMessage
	err = json.Unmarshal([]byte(out), &rows)
	return len(rows), err
}

// idleCPU leaves the yard alone for b.idle and says what CPU time the
// server, the daemon and the web view used meanwhile, and what memory each
// holds at the end.
func (b *bench) idleCPU(r *Round) error {
	pids := map[string]int{"web": b.web.Process.Pid}
	var err error
	if pids["server"], err = b.pidOf("server.pid"); err != nil {
		return err
	}
	if pids["daemon"], err = b.pidOf("daemon.lock"); err != nil {
		return err
	}
	before := map[string]Proc{}
	for name, pid := range pids {
		if before[name], err = procOf(pid); err != nil {
			return err
		}
	}
	start := time.Now()
	time.Sleep(b.idle)
	r.IdleSecs = time.Since(start).Seconds()
	for name, pid := range pids {
		p, err := procOf(pid)
		if err != nil {
			return fmt.Errorf("%s (pid %d) did not outlive the idle time: %w", name, pid, err)
		}
		r.Procs[name] = p
		r.IdleCPU[name] = Stat{Med: p.CPU - before[name].CPU}
	}
	return nil
}

// closeProbes closes the beads the measures made (their titles start with
// "bench "), so the next size's open beads are the fill's.
func (b *bench) closeProbes() error {
	out, err := b.yardr("bead", "list", "--json")
	if err != nil {
		return err
	}
	var beads []struct{ ID, Title string }
	if err := json.Unmarshal([]byte(out), &beads); err != nil {
		return err
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if _, err := b.yardr("bead", "close", id, "--reason", "benchmark"); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, bead := range beads {
		if strings.HasPrefix(bead.Title, "bench ") {
			jobs <- bead.ID
		}
	}
	close(jobs)
	wg.Wait()
	return first
}

// Engine is what was measured on copies of the store, away from the
// server: where the file's bytes are, what pruning and compacting give
// back, and how much of a command's time is the store's own.
type Engine struct {
	// The file: the copy as backed up, the copy compacted as it is, and
	// the copy after every bead was closed and everything prunable pruned,
	// before and after compacting.
	// UsedMB and PrunedUsedMB are the pages the buckets hold, however full
	// each is, before and after the prune: the file less its free pages.
	// A compact also packs the pages, so a compacted file can be smaller
	// than that.
	BackupMB          float64 `json:"backup_mb"`
	UsedMB            float64 `json:"used_mb"`
	CompactedMB       float64 `json:"compacted_mb"`
	PrunedMB          float64 `json:"pruned_mb"`
	PrunedUsedMB      float64 `json:"pruned_used_mb"`
	PrunedCompactedMB float64 `json:"pruned_compacted_mb"`
	PrunedEvents      int     `json:"pruned_events"`
	PrunedSessions    int     `json:"pruned_sessions"`
	HeldBy            string  `json:"held_by,omitempty"`
	// Buckets are the largest buckets of the copy as backed up, by the
	// bytes of their pages.
	Buckets []Bucket `json:"buckets"`
	// In this process, no socket and no CLI between: bbolt alone walking
	// the beads bucket, then the store's functions that walk it and
	// decode every record.
	ScanBeadsMs float64 `json:"scan_beads_ms"`
	ListAllMs   float64 `json:"list_all_ms"`
	ListOpenMs  float64 `json:"list_open_ms"`
	ReadyMs     float64 `json:"ready_ms"`
	GetBeadMs   float64 `json:"get_bead_ms"`
	// One AddNote, committed with and without waiting for the disk.
	NoteSyncMs   Stat `json:"note_sync_ms"`
	NoteNoSyncMs Stat `json:"note_nosync_ms"`
}

// Bucket is one top-level bucket of the store file.
type Bucket struct {
	Name string  `json:"name"`
	Keys int     `json:"keys"`
	MB   float64 `json:"mb"`
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// engine backs the store up through the running server and takes the copy
// apart in homes of its own under the yard's (so isolated-yard down still
// finds anything started for them). The yard itself is not touched.
func (b *bench) engine(r *Round, f *Fill) error {
	e := &r.Engine
	pruned, compacted := filepath.Join(b.home, "pruned"), filepath.Join(b.home, "compacted")
	for _, dir := range []string{pruned, compacted} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		defer os.RemoveAll(dir)
	}
	path := filepath.Join(pruned, "yard.db")
	if _, err := b.yardr("store", "backup", path); err != nil {
		return err
	}
	e.BackupMB = fileMB(path)
	if err := copyFile(path, filepath.Join(compacted, "yard.db")); err != nil {
		return err
	}
	if _, err := b.yardr("--home", compacted, "store", "compact"); err != nil {
		return err
	}
	e.CompactedMB = fileMB(filepath.Join(compacted, "yard.db"))
	if err := os.RemoveAll(compacted); err != nil {
		return err
	}

	if err := rawStore(path, e); err != nil {
		return err
	}
	ctx := b.ctx
	// With the disk, as the server opens it.
	s, err := openStore(path, false)
	if err != nil {
		return err
	}
	e.ListAllMs = inProcess(func() { _, _ = s.ListBeads(ctx, store.BeadFilter{All: true}) })
	e.ListOpenMs = inProcess(func() { _, _ = s.ListBeads(ctx, store.BeadFilter{}) })
	e.ReadyMs = inProcess(func() { _, _ = s.Ready(ctx) })
	e.GetBeadMs = inProcess(func() { _, _ = s.GetBead(ctx, f.Open) })
	e.NoteSyncMs, err = notes(ctx, s, f.Open)
	if cerr := s.Close(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	s, err = openStore(path, true)
	if err != nil {
		return err
	}
	e.NoteNoSyncMs, err = notes(ctx, s, f.Open)
	if err == nil {
		err = pruneAll(ctx, s, e)
	}
	if cerr := s.Close(ctx); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	e.PrunedMB = fileMB(path)
	if _, e.PrunedUsedMB, err = buckets(path); err != nil {
		return err
	}
	if _, err := b.yardr("--home", pruned, "store", "compact"); err != nil {
		return err
	}
	e.PrunedCompactedMB = fileMB(path)
	return nil
}

// inProcess is the median of 5 runs of fn, in milliseconds.
func inProcess(fn func()) float64 {
	var ds []time.Duration
	for range 5 {
		t := time.Now()
		fn()
		ds = append(ds, time.Since(t))
	}
	return quantiles(ds).Med
}

func notes(ctx context.Context, s *store.Store, bead string) (Stat, error) {
	var ds []time.Duration
	for i := range 30 {
		t := time.Now()
		if err := s.AddNote(ctx, bead, fmt.Sprintf("note %d on the copy", i), operator); err != nil {
			return Stat{}, err
		}
		ds = append(ds, time.Since(t))
	}
	return quantiles(ds), nil
}

// buckets reads a store file with bbolt alone: its top-level buckets,
// largest first, and the megabytes of pages they hold together.
func buckets(path string) (bks []Bucket, used float64, err error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
			st := bk.Stats()
			mb := float64(st.LeafAlloc+st.BranchAlloc) / (1 << 20)
			bks = append(bks, Bucket{Name: string(name), Keys: st.KeyN, MB: mb})
			used += mb
			return nil
		})
	})
	sort.Slice(bks, func(i, j int) bool { return bks[i].MB > bks[j].MB })
	return bks, used, err
}

// rawStore reads the copy with bbolt alone: each bucket's size, and the
// time one walk over the beads bucket takes that reads every byte of every
// record and decodes none. The bucket's name is the store's
// (internal/store/store.go, bBeads).
func rawStore(path string, e *Engine) error {
	var err error
	if e.Buckets, e.UsedMB, err = buckets(path); err != nil {
		return err
	}
	if len(e.Buckets) > 8 {
		e.Buckets = e.Buckets[:8]
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	e.ScanBeadsMs = inProcess(func() {
		_ = db.View(func(tx *bolt.Tx) error {
			sum := 0
			return tx.Bucket([]byte("beads")).ForEach(func(k, v []byte) error {
				for _, c := range v {
					sum += int(c)
				}
				return nil
			})
		})
	})
	return nil
}

// pruneAll closes every open bead of the copy, stops its crew and prunes
// with no retention at all: what is left is what a yard keeps for good,
// its beads, notes and edges.
func pruneAll(ctx context.Context, s *store.Store, e *Engine) error {
	open, err := s.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return err
	}
	for _, bead := range open {
		if _, err := s.CloseBead(ctx, bead.ID, "benchmark", store.Expect{}, operator); err != nil {
			return err
		}
	}
	crew, err := s.Crew(ctx)
	if err != nil {
		return err
	}
	for _, c := range crew {
		if err := s.SetCrewEnabled(ctx, c.Name, false, "", operator); err != nil {
			return err
		}
		if err := s.CrewStopped(ctx, c.Name, "benchmark"); err != nil {
			return err
		}
	}
	// What the daemon does for a session that ended: only then may it go.
	ended, err := s.Unstopped(ctx)
	if err != nil {
		return err
	}
	for _, sess := range ended {
		if err := s.MarkStopped(ctx, sess.ID); err != nil {
			return err
		}
	}
	if ended, err = s.ClosedWithWorkspace(ctx); err != nil {
		return err
	}
	for _, sess := range ended {
		if err := s.MarkCleaned(ctx, sess.ID); err != nil {
			return err
		}
	}
	time.Sleep(10 * time.Millisecond)
	res, err := s.Prune(ctx, store.Retention{Events: time.Millisecond, HookPayloads: time.Millisecond, Inbox: time.Millisecond}, operator)
	if err != nil {
		return err
	}
	e.PrunedEvents, e.PrunedSessions, e.HeldBy = res.Events, res.Sessions, res.HeldBy
	return nil
}
