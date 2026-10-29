package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
)

// The yard the fill builds, after the yard yardr itself is built in (244
// beads, 6,900 events when this was written): two depots, a flow of
// backlog, new, review, approved and merged, and per bead about 28 events.
const (
	operator   = "bench"
	yardmaster = "yardmaster"
	flowName   = "bench"
	// openShare of the beads stay open; the rest went the whole way to
	// merged.
	openShare = 0.05
)

var depots = []string{"alpha", "beta"}

// flowText is the lifecycle of both depots. tick is the stage of the
// dispatch measure: an exec group whose command is `true` works it.
const flowText = `backlog  -> new | decide | tick
new      -> review on done | decide on question | backlog
review   -> approved on done | new on changes | decide on question
approved -> merged | new on failed
tick     -> merged
decide   -> backlog
merged terminal
`

// openStore opens the yard's store file in this process, which is only
// possible while no server holds it. noSync opens it as the tests open
// theirs (store.NoSyncEnv): no commit waits for the disk. The variable is
// taken back at once, so that no server started later inherits it.
func openStore(path string, noSync bool) (*store.Store, error) {
	if noSync {
		os.Setenv("YARDR_"+store.NoSyncEnv, "1")
		defer os.Unsetenv("YARDR_" + store.NoSyncEnv)
	}
	var s *store.Store
	var err error
	// A server that was just stopped may hold the file a moment longer.
	for range 10 {
		if s, err = store.Open(path); err == nil {
			return s, nil
		}
	}
	return nil, err
}

// Fill says what one fill wrote.
type Fill struct {
	Beads  int     `json:"beads"`  // written by this fill
	Events uint64  `json:"events"` // logged by this fill
	Secs   float64 `json:"secs"`
	// Open and Closed are one bead of each kind this fill wrote, for the
	// commands that read one bead.
	Open, Closed string `json:"-"`
}

type filler struct {
	ctx context.Context
	s   *store.Store
	mu  sync.Mutex
	// closed and open are handles a later bead may name as its blocker.
	closed, open []string
	sampleOpen   string
}

// fill brings the store at path to total work beads through
// internal/store's own functions, the ones the server calls for the CLI:
// every bead is created, routed, claimed, noted and advanced as a real one
// is, each step a write with its event, so the records, indexes and log are
// what a yard of that size holds. Only the waiting for the disk is left out
// (openStore), and nothing runs: a session is claimed and marked running
// here, where the daemon would have started an agent.
func fill(ctx context.Context, path string, total, workers int) (*Fill, error) {
	s, err := openStore(path, true)
	if err != nil {
		return nil, err
	}
	defer s.Close(ctx)
	start := time.Now()
	have, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		return nil, err
	}
	f := &filler{ctx: ctx, s: s}
	for _, b := range have {
		if b.Status == "closed" {
			f.closed = append(f.closed, b.ID)
		}
	}
	head, err := s.Head(ctx)
	if err != nil {
		return nil, err
	}
	need := total - len(have)
	if need < 0 {
		return nil, fmt.Errorf("the yard has %d beads already, more than %d", len(have), total)
	}
	jobs, errs := make(chan int), make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				// Seeded by the bead's number: a fill is the same yard
				// every time, whatever order the writers take it in.
				r := rand.New(rand.NewSource(int64(len(have) + i)))
				var err error
				if open := float64(i%100) < openShare*100; open {
					err = f.openBead(i, r)
				} else {
					err = f.closedBead(i, r)
				}
				if err != nil {
					select {
					case errs <- fmt.Errorf("bead %d: %w", i, err):
					default:
					}
					return
				}
			}
		}()
	}
feed:
	for i := range need {
		select {
		case jobs <- i:
		case err = <-errs:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err == nil {
		select {
		case err = <-errs:
		default:
		}
	}
	if err != nil {
		return nil, err
	}
	// The yardmaster has read its mail, but for the last few.
	if now, err := s.Head(ctx); err != nil {
		return nil, err
	} else if err := s.AckInbox(ctx, yardmaster, now); err != nil {
		return nil, err
	}
	r := rand.New(rand.NewSource(int64(total)))
	for range 5 {
		if err := s.Mail(ctx, yardmaster, text(r, 300), operator); err != nil {
			return nil, err
		}
	}
	now, err := s.Head(ctx)
	if err != nil {
		return nil, err
	}
	out := &Fill{Beads: need, Events: now - head, Secs: time.Since(start).Seconds(), Open: f.sampleOpen}
	if len(f.closed) > 0 {
		out.Closed = f.closed[len(f.closed)-1]
	}
	return out, nil
}

// pick is one of the handles in *from, or "" when there is none yet.
func (f *filler) pick(r *rand.Rand, from *[]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(*from) == 0 {
		return ""
	}
	return (*from)[r.Intn(len(*from))]
}

func (f *filler) add(to *[]string, id string) {
	f.mu.Lock()
	*to = append(*to, id)
	f.mu.Unlock()
}

// session is what the dispatcher does to start work on a bead in a stage:
// the claim, and the runner's report that it runs. handle is what a herdr
// session records of its pane and worktree; it names nothing that exists,
// and no pid, and the session is marked stopped as soon as it has ended, so
// the daemon never has anything of it to stop.
func (f *filler) session(id, stage, group string, herdr bool) (string, error) {
	sess, err := f.s.Claim(f.ctx, id, group, stage, "yard")
	if err != nil {
		return "", err
	}
	handle := map[string]any{"log": "/nowhere/" + group + ".log"}
	if herdr {
		handle = map[string]any{
			"agent": id + "-" + stage, "branch": "yard/" + id, "pane": "bench:p0", "tab": "bench:t0", "workspace": "bench",
			"repo": "/nowhere/repo", "worktree": "/nowhere/worktrees/" + id, "brief": "/nowhere/repo/.git/worktrees/" + id + "/yardr/brief-" + sess.ID + ".md",
			"head": "6d0756bf5745c7dfeef9620773da718701dafab3",
		}
	}
	if err := f.s.MarkRunning(f.ctx, sess.ID, handle); err != nil {
		return "", err
	}
	if herdr {
		if err := f.s.LogDispatch(f.ctx, id, "session_prompted", "yard", map[string]any{"session": sess.ID, "shown": true}); err != nil {
			return "", err
		}
	}
	return sess.ID, nil
}

// closedBead writes a bead that went the whole way: shaped in backlog,
// built, reviewed (one in five sent back once), landed by the assembly,
// closed, its workspace removed. Around it, what a yard logs that is on no
// bead: the yardmaster's status as it works, its journal, mail.
func (f *filler) closedBead(i int, r *rand.Rand) error {
	s, ctx := f.s, f.ctx
	b, err := s.CreateBead(ctx, store.NewBead{Title: title(r), Body: text(r, 1500+r.Intn(2400)), Depot: depots[i%len(depots)], Priority: 1 + r.Intn(3)}, yardmaster)
	if err != nil {
		return err
	}
	id := b.ID
	if i%4 == 0 {
		if blocker := f.pick(r, &f.closed); blocker != "" {
			if err := s.AddDep(ctx, blocker, id, "blocks", "", "", yardmaster); err != nil {
				return err
			}
		}
	}
	if _, err := s.Advance(ctx, id, "new", store.Expect{}, yardmaster); err != nil {
		return err
	}
	var sessions []string
	stage := func(name, group string, herdr bool, end func(actor string) error) error {
		sess, err := f.session(id, name, group, herdr)
		if err != nil {
			return err
		}
		sessions = append(sessions, sess)
		if err := end(id + "-" + name); err != nil {
			return err
		}
		return s.MarkStopped(ctx, sess)
	}
	build := func(actor string) error {
		if err := s.AddNote(ctx, id, text(r, 200+r.Intn(400)), actor); err != nil {
			return err
		}
		_, err := s.Done(ctx, id, "", text(r, 500+r.Intn(900)), store.Expect{}, actor)
		return err
	}
	if err := stage("new", "builders", true, build); err != nil {
		return err
	}
	if r.Intn(5) == 0 {
		err := stage("review", "reviewers", true, func(actor string) error {
			_, err := s.Done(ctx, id, "changes", text(r, 300+r.Intn(500)), store.Expect{}, actor)
			return err
		})
		if err != nil {
			return err
		}
		if err := stage("new", "builders", true, build); err != nil {
			return err
		}
	}
	err = stage("review", "reviewers", true, func(actor string) error {
		_, err := s.Done(ctx, id, "", text(r, 300+r.Intn(700)), store.Expect{}, actor)
		return err
	})
	if err != nil {
		return err
	}
	err = stage("approved", "assembly", false, func(actor string) error {
		_, err := s.AdvanceNote(ctx, id, "merged", "landed on main at "+fmt.Sprintf("%07x", r.Int31()), store.Expect{}, actor)
		return err
	})
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if err := s.MarkCleaned(ctx, sess); err != nil {
			return err
		}
	}
	if err := s.LogDispatch(ctx, id, "workspace_removed", "yard", map[string]any{"worktree": "/nowhere/worktrees/" + id, "branch": "yard/" + id}); err != nil {
		return err
	}
	f.add(&f.closed, id)

	for range 5 {
		was, is := "idle", "working"
		if r.Intn(2) == 0 {
			was, is = is, was
		}
		if err := s.LogEvent(ctx, "", "crew_status", "yard", map[string]any{"name": yardmaster, "status": is, "was": was}); err != nil {
			return err
		}
	}
	if _, err := s.Journal(ctx, yardmaster, store.NoteJournal, text(r, 200+r.Intn(300)), yardmaster); err != nil {
		return err
	}
	switch i % 5 {
	case 0:
		return s.Mail(ctx, yardmaster, text(r, 100+r.Intn(400)), operator)
	case 2:
		return s.Mail(ctx, operator, id+": "+text(r, 100+r.Intn(400)), yardmaster)
	}
	return nil
}

// openBead writes a bead that is still open, in a stage a person works, so
// the daemon starts nothing for it: most wait in backlog, some for a
// decision, some behind another open bead, a few are held.
func (f *filler) openBead(i int, r *rand.Rand) error {
	s, ctx := f.s, f.ctx
	b, err := s.CreateBead(ctx, store.NewBead{Title: title(r), Body: text(r, 1500+r.Intn(2400)), Depot: depots[i%len(depots)], Priority: 1 + r.Intn(3)}, yardmaster)
	if err != nil {
		return err
	}
	id, held := b.ID, false
	switch k := r.Intn(10); {
	case k < 2:
		if blocker := f.pick(r, &f.closed); blocker != "" {
			if err := s.AddDep(ctx, blocker, id, "blocks", "", "", yardmaster); err != nil {
				return err
			}
		}
		if err := s.AddNote(ctx, id, text(r, 300+r.Intn(500)), yardmaster); err != nil {
			return err
		}
		if _, err := s.AdvanceNote(ctx, id, "decide", text(r, 200+r.Intn(300)), store.Expect{}, yardmaster); err != nil {
			return err
		}
		f.mu.Lock()
		if f.sampleOpen == "" {
			f.sampleOpen = id
		}
		f.mu.Unlock()
	case k == 2:
		if blocker := f.pick(r, &f.open); blocker != "" {
			if err := s.AddDep(ctx, blocker, id, "blocks", "", "", yardmaster); err != nil {
				return err
			}
		}
	case k == 3:
		if _, err := s.SetHold(ctx, id, true, store.Expect{}, operator); err != nil {
			return err
		}
		held = true
	case k < 7:
		if err := s.AddNote(ctx, id, text(r, 300+r.Intn(500)), yardmaster); err != nil {
			return err
		}
	}
	if !held {
		f.add(&f.open, id)
	}
	return nil
}

var words = strings.Fields(`the a bead stage flow group route depot session daemon server store event note
review builder branch worktree merge commit test check light full yard crew mail inbox hook peer
when then after before each every which that this with without from into over under still never always
reads writes starts stops claims routes closes opens waits fails passes holds lands returns counts names
is was has had does did says said must may can cannot should would could not only also once again`)

// text is about n bytes of words: the store keeps what it is given as it
// is, so only the length matters.
func text(r *rand.Rand, n int) string {
	var sb strings.Builder
	for sb.Len() < n {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(words[r.Intn(len(words))])
	}
	return sb.String()
}

func title(r *rand.Rand) string { return text(r, 30+r.Intn(50)) }
