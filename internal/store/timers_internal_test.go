package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// timerYard is a store whose clock the test moves: crew member yardmaster and
// clerk, users jo (the operator) and ada. tick moves the clock and runs
// the daemon's part, FireTimers at the new time.
type timerYard struct {
	*Store
	t  *testing.T
	at time.Time
}

// newTimerYard sets the package's fake clock to the yard's now, so its
// callers stay sequential.
func newTimerYard(t *testing.T) *timerYard {
	t.Helper()
	y := &timerYard{t: t, at: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	ctx := context.Background()
	if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"yardmaster", "clerk"} {
		if _, err := s.PutCrew(ctx, CrewMember{Name: r, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return y
}

// tick moves the clock by d and fires what is due then.
func (y *timerYard) tick(d time.Duration) *TimerPass {
	y.t.Helper()
	y.at = y.at.Add(d)
	pass, err := y.FireTimers(context.Background(), y.at)
	if err != nil {
		y.t.Fatal(err)
	}
	return pass
}

func (y *timerYard) add(actor string, nt NewTimer) *Timer {
	y.t.Helper()
	tm, err := y.AddTimer(context.Background(), nt, actor)
	if err != nil {
		y.t.Fatal(err)
	}
	return tm
}

func (y *timerYard) in(d time.Duration) time.Time { return y.at.Add(d) }

// refused is that adding nt as actor fails saying want.
func (y *timerYard) refused(actor string, nt NewTimer, want string) {
	y.t.Helper()
	if _, err := y.AddTimer(context.Background(), nt, actor); err == nil || !strings.Contains(err.Error(), want) {
		y.t.Fatalf("AddTimer(%+v) as %s = %v, want %q", nt, actor, err, want)
	}
}

// fired are the texts of the timer events in name's inbox.
func (y *timerYard) fired(name string) []string {
	y.t.Helper()
	page, err := y.Inbox(context.Background(), name, 0)
	if err != nil {
		y.t.Fatal(err)
	}
	var out []string
	for _, e := range page.Events {
		if e.Kind != "timer" {
			y.t.Fatalf("%s's inbox holds a %s: %+v", name, e.Kind, e)
		}
		text, _ := e.Data["text"].(string)
		out = append(out, text)
	}
	return out
}

// kinds counts the events of each kind in the log.
func (y *timerYard) kinds() map[string]int {
	y.t.Helper()
	evs, err := y.EventsSince(context.Background(), 0, 0)
	if err != nil {
		y.t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range evs {
		out[e.Kind]++
	}
	return out
}

func (y *timerYard) live(name string) []Timer {
	y.t.Helper()
	ts, err := y.Timers(context.Background(), name)
	if err != nil {
		y.t.Fatal(err)
	}
	return ts
}

// A one-shot timer a crew member set for itself is an event of kind timer in
// its inbox when it is due, once, with the reason, who set it and when;
// then the timer is gone.
func TestTimerFiresOnceToACrew(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	tm := y.add("yardmaster", NewTimer{Reason: "look at the deploy", Due: y.in(40 * time.Minute)})
	if tm.To != "yardmaster" || tm.ToUser || tm.ID == "" {
		t.Fatalf("timer = %+v", tm)
	}
	if pass := y.tick(39 * time.Minute); pass.Fired != 0 || pass.Next == nil || !pass.Next.Equal(tm.Due) {
		t.Fatalf("before it is due: %+v", pass)
	}
	if got := y.fired("yardmaster"); len(got) != 0 {
		t.Fatalf("fired early: %v", got)
	}
	if pass := y.tick(time.Minute); pass.Fired != 1 || pass.Next != nil {
		t.Fatalf("when due: %+v", pass)
	}
	want := "timer: look at the deploy (set " + tm.SetAt.Local().Format(timerTime) + " by yardmaster)."
	if got := y.fired("yardmaster"); len(got) != 1 || got[0] != want {
		t.Fatalf("fired %q, want %q", got, want)
	}
	// Not a second time, and nobody else hears of it.
	if pass := y.tick(time.Hour); pass.Fired != 0 {
		t.Fatalf("fired again: %+v", pass)
	}
	if got := y.fired("yardmaster"); len(got) != 1 {
		t.Fatalf("fired %d times", len(got))
	}
	if got := y.fired("clerk"); len(got) != 0 {
		t.Fatalf("clerk heard of yardmaster's timer: %v", got)
	}
	if len(y.live("")) != 0 {
		t.Fatalf("a fired one-shot is still live: %+v", y.live(""))
	}
	if k := y.kinds(); k["timer_set"] != 1 || k["timer"] != 1 {
		t.Fatalf("events %v", k)
	}
}

// A timer for a user fires as mail from whoever set it, marked as a timer
// and saying so in its text. It is in no crew member's inbox: the one who set
// it is not woken.
func TestTimerToAUserIsMail(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	ctx := context.Background()
	tm := y.add("yardmaster", NewTimer{To: "jo", Reason: "the assembly is still red", Due: y.in(10 * time.Minute)})
	if !tm.ToUser || tm.By != "yardmaster" {
		t.Fatalf("timer = %+v", tm)
	}
	y.tick(10 * time.Minute)
	box, err := y.UserInbox(ctx, "jo", 0)
	if err != nil || box.Unread != 1 {
		t.Fatalf("jo's inbox = %+v, %v", box, err)
	}
	l := box.Mail[0]
	if l.From != "yardmaster" || l.Timer != tm.ID || !strings.HasPrefix(l.Text, "timer: the assembly is still red (set ") {
		t.Fatalf("letter = %+v", l)
	}
	// The conversation yardr chat draws holds it too.
	conv, _, err := y.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 || conv[0].Timer != tm.ID {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	for _, r := range []string{"yardmaster", "clerk"} {
		if page, _ := y.Inbox(ctx, r, 0); page.Total != 0 {
			t.Fatalf("%s was told of a timer for jo: %+v", r, page.Events)
		}
	}
	// A user's own timer is mail from themselves.
	y.add("ada", NewTimer{Reason: "stand-up", Due: y.in(time.Hour)})
	y.tick(time.Hour)
	if box, _ := y.UserInbox(ctx, "ada", 0); box.Unread != 1 || box.Mail[0].From != "ada" {
		t.Fatalf("ada's inbox = %+v", box)
	}
}

// Who may set a timer for whom: a crew member or a user for themselves, a
// crew member for a user, and nobody for another crew member.
func TestTimerWhoMaySet(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	due := y.in(time.Hour)
	y.refused("yardmaster", NewTimer{To: "clerk", Reason: "x", Due: due}, "nobody sets a timer for another crew member: mail it (yardr mail clerk")
	y.refused("jo", NewTimer{To: "yardmaster", Reason: "x", Due: due}, "nobody sets a timer for another crew member")
	y.refused("jo", NewTimer{To: "ada", Reason: "x", Due: due}, "a user sets timers for themselves")
	y.refused("yardmaster", NewTimer{To: "nobody", Reason: "x", Due: due}, "no user of that name")
	y.refused("yardr-1-new", NewTimer{Reason: "x", Due: due}, "neither a crew member nor a user")
	y.refused("yardmaster", NewTimer{Reason: "  ", Due: due}, "a reason is required")
	y.refused("yardmaster", NewTimer{Reason: "x"}, "say when")
	if len(y.live("")) != 0 {
		t.Fatalf("a refused timer was kept: %+v", y.live(""))
	}
}

// The limits: timers per setter, the shortest period, how far ahead and
// how long. The two settings move theirs.
func TestTimerLimits(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	ctx := context.Background()
	day := 24 * time.Hour
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(-time.Minute)}, "is not in the future")
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(31 * day)}, "more than 30d ahead")
	y.refused("yardmaster", NewTimer{Reason: "x", Every: 59 * time.Minute}, "--every 59m is shorter than the yard allows (timer_min_every 1h)")
	y.refused("yardmaster", NewTimer{Reason: "x", Every: day, For: 31 * day}, "a recurring timer runs at most 30d")
	y.refused("yardmaster", NewTimer{Reason: "x", Every: 8 * day}, "--every 8d would never fire within the 7d it runs")
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), For: day}, "--for is how long a recurring timer runs")
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), Every: time.Hour}, "give --every alone")

	for i := 0; i < DefaultTimerLimit; i++ {
		y.add("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour)})
	}
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour)},
		"yardmaster has 5 live timers, the most the yard allows one setter (timer_limit)")
	// The limit is each setter's own, and a fired timer leaves it.
	y.add("clerk", NewTimer{Reason: "x", Due: y.in(2 * time.Hour)})
	y.tick(time.Hour)
	y.add("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour)})

	if err := y.SetSetting(ctx, TimerLimitSetting, "1", "jo"); err != nil {
		t.Fatal(err)
	}
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour)}, "yardmaster has 1 live timers")
	if err := y.SetSetting(ctx, TimerMinEverySetting, "5s", "jo"); err != nil {
		t.Fatal(err)
	}
	y.add("ada", NewTimer{Reason: "x", Every: 5 * time.Second})
}

// A recurring timer fires each period and ends when its time is up, with
// a last firing that says so.
func TestTimerRecurs(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	tm := y.add("yardmaster", NewTimer{Reason: "sweep the backlog", Every: time.Hour, For: 3 * time.Hour})
	if tm.Every != "1h" || tm.Ends == nil || !tm.Ends.Equal(tm.SetAt.Add(3*time.Hour)) || !tm.Due.Equal(tm.SetAt.Add(time.Hour)) {
		t.Fatalf("timer = %+v", tm)
	}
	for i := 1; i <= 3; i++ {
		if pass := y.tick(30 * time.Minute); pass.Fired != 0 {
			t.Fatalf("fired between periods: %+v", pass)
		}
		if pass := y.tick(30 * time.Minute); pass.Fired != 1 {
			t.Fatalf("period %d: %+v", i, pass)
		}
		if got := y.fired("yardmaster"); len(got) != i {
			t.Fatalf("period %d: fired %d times", i, len(got))
		}
	}
	got := y.fired("yardmaster")
	if strings.Contains(got[1], LastFiring) || !strings.HasSuffix(got[2], "by yardmaster, every 1h). "+LastFiring) {
		t.Fatalf("firings:\n%s", strings.Join(got, "\n"))
	}
	if len(y.live("")) != 0 {
		t.Fatalf("still live after its last firing: %+v", y.live(""))
	}
	if pass := y.tick(5 * time.Hour); pass.Fired != 0 {
		t.Fatalf("fired after its end: %+v", pass)
	}
	// Without --for it runs seven days.
	if tm := y.add("yardmaster", NewTimer{Reason: "x", Every: 24 * time.Hour}); !tm.Ends.Equal(tm.SetAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("default end = %s, set %s", tm.Ends, tm.SetAt)
	}
}

// A timer that was due while no daemon ran fires once when one runs again,
// marked late with by how much; a recurring one fires once for all the
// periods missed and then keeps its rhythm.
func TestTimerLateAfterTheDaemonWasAway(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	y.add("yardmaster", NewTimer{Reason: "one-shot", Due: y.in(10 * time.Minute)})
	every := y.add("clerk", NewTimer{Reason: "hourly", Every: time.Hour, For: 24 * time.Hour})
	// Nothing calls FireTimers for five and a half hours.
	if pass := y.tick(5*time.Hour + 30*time.Minute); pass.Fired != 2 {
		t.Fatalf("back after the gap: %+v", pass)
	}
	if got := y.fired("yardmaster"); len(got) != 1 || !strings.HasSuffix(got[0], "Late by 5h20m: the yard's daemon was not running when it was due.") {
		t.Fatalf("one-shot: %q", got)
	}
	got := y.fired("clerk")
	if len(got) != 1 || !strings.Contains(got[0], "Late by 4h30m") {
		t.Fatalf("recurring fired %d times for the time missed: %q", len(got), got)
	}
	// Its next firing is the next whole period, not one per period missed.
	ts := y.live("clerk")
	if len(ts) != 1 || !ts[0].Due.Equal(every.SetAt.Add(6*time.Hour)) {
		t.Fatalf("next due %+v, want %s", ts, every.SetAt.Add(6*time.Hour))
	}
	if pass := y.tick(29 * time.Minute); pass.Fired != 0 {
		t.Fatalf("fired again for the gap: %+v", pass)
	}
	if pass := y.tick(time.Minute); pass.Fired != 1 {
		t.Fatalf("next period: %+v", pass)
	}
	if got := y.fired("clerk"); len(got) != 2 || strings.Contains(got[1], "Late") {
		t.Fatalf("on time again: %q", got)
	}
	// A pass a few seconds after the due time is not late.
	y.add("yardmaster", NewTimer{Reason: "soon", Due: y.in(time.Minute)})
	y.tick(time.Minute + 20*time.Second)
	if got := y.fired("yardmaster"); len(got) != 2 || strings.Contains(got[1], "Late") {
		t.Fatalf("a pass's delay counted as late: %q", got)
	}
}

const timerFlow = `
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged
merged terminal
`

// flowBead is a bead of depot web, governed by timerFlow, in new.
func (y *timerYard) flowBead(id string) {
	y.t.Helper()
	ctx := context.Background()
	if _, err := y.GetDepot(ctx, "web"); errors.Is(err, ErrNotFound) {
		if err := y.PutDepot(ctx, Depot{Name: "web", Base: "main"}, "jo"); err != nil {
			y.t.Fatal(err)
		}
		f, err := ParseFlow(timerFlow)
		if err != nil {
			y.t.Fatal(err)
		}
		f.Name = "web"
		if _, err := y.PutFlow(ctx, *f, "jo"); err != nil {
			y.t.Fatal(err)
		}
		if err := y.SetFlowPointer(ctx, "web", "", "web", "jo"); err != nil {
			y.t.Fatal(err)
		}
	}
	if _, err := y.CreateBead(ctx, NewBead{ID: id, Title: id, Depot: "web", Stage: "new"}, "jo"); err != nil {
		y.t.Fatal(err)
	}
}

func (y *timerYard) advance(id, stage string) {
	y.t.Helper()
	if _, err := y.Advance(context.Background(), id, stage, Expect{}, "jo"); err != nil {
		y.t.Fatal(err)
	}
}

// dropped is the why of the only timer_dropped event.
func (y *timerYard) dropped() string {
	y.t.Helper()
	evs, _ := y.EventsSince(context.Background(), 0, 0)
	why := ""
	n := 0
	for _, e := range evs {
		if e.Kind == "timer_dropped" {
			n++
			why, _ = e.Data["why"].(string)
		}
	}
	if n != 1 {
		y.t.Fatalf("%d timer_dropped events, want 1", n)
	}
	return why
}

// --unless bead=<id>: the timer is dropped when the bead closes before it
// is due, with a timer_dropped event and nothing for the receiver.
func TestTimerUnlessDropsOnClose(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	ctx := context.Background()
	y.flowBead("web-1")
	tm := y.add("yardmaster", NewTimer{To: "jo", Reason: "web-1 is not done", Due: y.in(time.Hour), UnlessBead: "web-1"})
	if tm.Unless == nil || tm.Unless.String() != "bead=web-1" {
		t.Fatalf("unless = %+v", tm.Unless)
	}
	if _, err := y.CloseBead(ctx, "web-1", "done", Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Dropped on the next pass, before it is due.
	if pass := y.tick(time.Minute); pass.Dropped != 1 || pass.Fired != 0 || pass.Next != nil {
		t.Fatalf("pass = %+v", pass)
	}
	if why := y.dropped(); why != "web-1 is closed" {
		t.Fatalf("why = %q", why)
	}
	y.tick(time.Hour)
	if box, _ := y.UserInbox(ctx, "jo", 0); box.Unread != 0 {
		t.Fatalf("jo was told of a dropped timer: %+v", box.Mail)
	}
	if k := y.kinds(); k["timer"] != 0 {
		t.Fatalf("events %v", k)
	}
	// A bead that is closed already is nothing to wait for.
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), UnlessBead: "web-1"}, "web-1 is closed: there is nothing left to wait for")
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), UnlessBead: "web-9"}, "not found")
	y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), UnlessStage: "review"}, "needs its bead")
}

// --unless bead=<id>,stage=<stage>: dropped when the bead entered the
// stage, is in one the flow orders after it, was there and went back, or
// closed. A bead still short of it lets the timer fire, and the firing
// says what was awaited and where the bead stands.
func TestTimerUnlessStage(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	stuck := func(t *testing.T, move func(y *timerYard)) *timerYard {
		t.Helper()
		y := newTimerYard(t)
		y.flowBead("web-1")
		y.add("yardmaster", NewTimer{To: "jo", Reason: "web-1 is stuck", Due: y.in(time.Hour), UnlessBead: "web-1", UnlessStage: "review"})
		move(y)
		y.tick(time.Hour)
		return y
	}
	for name, c := range map[string]struct {
		move func(y *timerYard)
		why  string
	}{
		"in the stage":   {func(y *timerYard) { y.advance("web-1", "review") }, "web-1 is in review"},
		"past the stage": {func(y *timerYard) { y.advance("web-1", "review"); y.advance("web-1", "approved") }, "web-1 has been in review and is in approved"},
		"back from it":   {func(y *timerYard) { y.advance("web-1", "review"); y.advance("web-1", "new") }, "web-1 has been in review and is in new"},
		"closed": {func(y *timerYard) {
			if _, err := y.CloseBead(context.Background(), "web-1", "dropped", Expect{}, "jo"); err != nil {
				y.t.Fatal(err)
			}
		}, "web-1 is closed"},
	} {
		t.Run(name, func(t *testing.T) {
			y := stuck(t, c.move)
			if why := y.dropped(); why != c.why {
				t.Fatalf("why = %q, want %q", why, c.why)
			}
			if box, _ := y.UserInbox(context.Background(), "jo", 0); box.Unread != 0 {
				t.Fatalf("jo was told: %+v", box.Mail)
			}
		})
	}
	t.Run("still short of it", func(t *testing.T) {
		y := stuck(t, func(y *timerYard) { y.advance("web-1", "backlog") })
		box, _ := y.UserInbox(context.Background(), "jo", 0)
		if box.Unread != 1 || !strings.HasSuffix(box.Mail[0].Text, "by yardmaster). web-1 is still in backlog; this timer waited for it to reach review.") {
			t.Fatalf("mail = %+v", box.Mail)
		}
		if k := y.kinds(); k["timer_dropped"] != 0 {
			t.Fatalf("events %v", k)
		}
	})
	t.Run("refused when set", func(t *testing.T) {
		y := newTimerYard(t)
		y.flowBead("web-1")
		y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), UnlessBead: "web-1", UnlessStage: "revue"},
			`stage "revue" is not in web-1's flow web (backlog, new, review, approved, merged)`)
		y.refused("yardmaster", NewTimer{Reason: "x", Due: y.in(time.Hour), UnlessBead: "web-1", UnlessStage: "new"},
			"web-1 is in new: there is nothing left to wait for")
	})
}

// A recurring timer with --unless fires while the bead is short of what
// it waits for and ends when that holds.
func TestTimerRecurringUnlessEnds(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	y.flowBead("web-1")
	y.add("yardmaster", NewTimer{Reason: "web-1 open", Every: time.Hour, UnlessBead: "web-1"})
	y.tick(time.Hour)
	y.tick(time.Hour)
	if got := y.fired("yardmaster"); len(got) != 2 || !strings.HasSuffix(got[1], "web-1 is still in new; this timer waited for it to close.") {
		t.Fatalf("fired %q", got)
	}
	if _, err := y.CloseBead(context.Background(), "web-1", "done", Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.tick(time.Hour)
	if got := y.fired("yardmaster"); len(got) != 2 || len(y.live("")) != 0 || y.dropped() != "web-1 is closed" {
		t.Fatalf("after the close: fired %q, live %+v", got, y.live(""))
	}
}

// A timer whose receiver left the yard is dropped, not kept for whoever
// takes the name next.
func TestTimerReceiverGone(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	y.add("yardmaster", NewTimer{To: "ada", Reason: "x", Due: y.in(time.Hour)})
	if err := y.DeleteUser(context.Background(), "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if pass := y.tick(time.Hour); pass.Dropped != 1 || pass.Fired != 0 {
		t.Fatalf("pass = %+v", pass)
	}
	if why := y.dropped(); why != "ada is no longer in the yard" {
		t.Fatalf("why = %q", why)
	}
}

// Timers lists what a party set or is told by, soonest first; RemoveTimer
// is theirs, and the operator removes any.
func TestTimerListAndRemove(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	ctx := context.Background()
	own := y.add("yardmaster", NewTimer{Reason: "later", Due: y.in(2 * time.Hour)})
	forJo := y.add("yardmaster", NewTimer{To: "jo", Reason: "sooner", Due: y.in(time.Hour)})
	clerks := y.add("clerk", NewTimer{Reason: "clerk's", Due: y.in(3 * time.Hour)})

	ids := func(ts []Timer) string {
		var out []string
		for _, tm := range ts {
			out = append(out, tm.ID)
		}
		return strings.Join(out, " ")
	}
	if got, want := ids(y.live("yardmaster")), forJo.ID+" "+own.ID; got != want {
		t.Fatalf("yardmaster's = %s, want %s", got, want)
	}
	if got := ids(y.live("jo")); got != forJo.ID {
		t.Fatalf("jo's = %s", got)
	}
	if got, want := ids(y.live("")), forJo.ID+" "+own.ID+" "+clerks.ID; got != want {
		t.Fatalf("all = %s, want %s", got, want)
	}

	if _, err := y.RemoveTimer(ctx, clerks.ID, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (jo) may remove the timer "+clerks.ID+", which clerk set for clerk; this is yardmaster") {
		t.Fatalf("yardmaster removing clerk's = %v", err)
	}
	if _, err := y.RemoveTimer(ctx, "t99", "yardmaster"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing none = %v", err)
	}
	// The one told removes a timer set for them; the operator any.
	if _, err := y.RemoveTimer(ctx, forJo.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.RemoveTimer(ctx, clerks.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.RemoveTimer(ctx, own.ID, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if k := y.kinds(); k["timer_removed"] != 3 || len(y.live("")) != 0 {
		t.Fatalf("events %v, live %+v", k, y.live(""))
	}
	if pass := y.tick(4 * time.Hour); pass.Fired != 0 {
		t.Fatalf("a removed timer fired: %+v", pass)
	}
}

// A crew member's prime lists its live timers: its own and those it set for
// a user.
func TestTimersInPrime(t *testing.T) {
	// Sequential: the package's fake clock (via newTimerYard).
	y := newTimerYard(t)
	y.add("yardmaster", NewTimer{Reason: "own", Due: y.in(time.Hour)})
	y.add("yardmaster", NewTimer{To: "jo", Reason: "for jo", Due: y.in(2 * time.Hour)})
	y.add("clerk", NewTimer{Reason: "clerk's", Due: y.in(time.Hour)})
	p, err := y.CrewPrime(context.Background(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Timers) != 2 || p.Timers[0].Reason != "own" || p.Timers[1].Reason != "for jo" {
		t.Fatalf("prime timers = %+v", p.Timers)
	}
}

func TestSpan(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"1d": "1d", "24h": "1d", "90m": "1h30m", "2h": "2h", "45s": "45s", "30d": "30d", "36h": "36h"} {
		d, err := ParseSpan(in)
		if err != nil || Span(d) != want {
			t.Errorf("Span(ParseSpan(%q)) = %q, %v; want %q", in, Span(d), err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-1h", "d", "soon"} {
		if _, err := ParseSpan(bad); err == nil {
			t.Errorf("ParseSpan(%q) accepted", bad)
		}
	}
}
