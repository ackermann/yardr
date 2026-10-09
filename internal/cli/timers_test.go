package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// timerUserYard is userYard with a second crew member and a second user.
func timerUserYard(t *testing.T) store.API {
	t.Helper()
	st := userYard(t)
	mustAs(t, st, "jo", "user", "add", "ada")
	if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return st
}

// yardr timer add, list and rm: who may set one for whom, whose a listing
// shows, and who removes which.
func TestTimerCommands(t *testing.T) {
	st := timerUserYard(t)
	ctx := context.Background()

	out := mustAs(t, st, "yardmaster", "timer", "add", "--in", "40m", "look at the deploy")
	if !strings.HasPrefix(out, "timer t1 set: ") || !strings.Contains(out, "(in 40m): look at the deploy") {
		t.Fatalf("add: %q", out)
	}
	out = mustAs(t, st, "yardmaster", "timer", "add", "--in", "2h", "--to", "jo", "the assembly is red")
	if !strings.Contains(out, "timer t2 set: tells jo ") {
		t.Fatalf("add --to: %q", out)
	}
	var set store.Timer
	if err := json.Unmarshal([]byte(mustAs(t, st, "ada", "timer", "add", "--every", "1d", "--for", "2d", "stand-up", "--json")), &set); err != nil ||
		set.ID != "t3" || set.By != "ada" || set.To != "ada" || !set.ToUser || set.Every != "1d" || set.Ends == nil {
		t.Fatalf("add --json = %+v, %v", set, err)
	}

	refusedAs(t, st, "yardmaster", "nobody sets a timer for another crew member: mail it", "timer", "add", "--in", "1h", "--to", "clerk", "x")
	refusedAs(t, st, "jo", "a user sets timers for themselves", "timer", "add", "--in", "1h", "--to", "ada", "x")
	refusedAs(t, st, "yard-10-new", "neither a crew member nor a user", "timer", "add", "--in", "1h", "x")
	refusedAs(t, st, "yardmaster", "one of --in <duration>, --at <time> or --every <duration>", "timer", "add", "x")
	refusedAs(t, st, "yardmaster", "one of --in <duration>, --at <time> or --every <duration>", "timer", "add", "--in", "1h", "--at", "09:00", "x")
	refusedAs(t, st, "yardmaster", `--in: bad duration "soon"`, "timer", "add", "--in", "soon", "x")
	refusedAs(t, st, "yardmaster", "--every 10m is shorter than the yard allows (timer_min_every 1h)", "timer", "add", "--every", "10m", "x")
	refusedAs(t, st, "yardmaster", "the keys are bead and stage", "timer", "add", "--in", "1h", "--unless", "train=x", "x")
	if _, err := as(t, st, "yardmaster", "timer", "add", "--in", "1h"); err == nil {
		t.Fatal("a timer without a reason was taken")
	}

	// A listing is of one's own: what one set, and what tells one.
	rows := func(actor string, args ...string) string {
		t.Helper()
		var ts []store.Timer
		if err := json.Unmarshal([]byte(mustAs(t, st, actor, append([]string{"timer", "list", "--json"}, args...)...)), &ts); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, tm := range ts {
			ids = append(ids, tm.ID)
		}
		return strings.Join(ids, " ")
	}
	if got := rows("yardmaster"); got != "t1 t2" {
		t.Fatalf("yardmaster's = %q", got)
	}
	if got := rows("jo"); got != "t2" {
		t.Fatalf("jo's = %q", got)
	}
	if got := rows("clerk"); got != "" {
		t.Fatalf("clerk's = %q", got)
	}
	if got := rows("jo", "--all"); got != "t1 t2 t3" {
		t.Fatalf("--all = %q", got)
	}
	refusedAs(t, st, "yardmaster", "only the operator (jo) may list everyone's timers; this is yardmaster", "timer", "list", "--all")
	if out := mustAs(t, st, "yardmaster", "timer", "list"); !strings.Contains(out, "REASON") || !strings.Contains(out, "the assembly is red") {
		t.Fatalf("list:\n%s", out)
	}

	refusedAs(t, st, "clerk", "only the operator (jo) may remove the timer t1, which yardmaster set for yardmaster; this is clerk", "timer", "rm", "t1")
	refusedAs(t, st, "ada", "which yardmaster set for jo; this is ada", "timer", "rm", "t2")
	if _, err := as(t, st, "yardmaster", "timer", "rm", "t9"); exitCode(err) != 3 {
		t.Fatalf("rm of none: %v", err)
	}
	// The one told removes theirs, and the operator anyone's.
	if out := mustAs(t, st, "jo", "timer", "rm", "t2"); out != "timer t2 removed\n" {
		t.Fatalf("rm: %q", out)
	}
	mustAs(t, st, "jo", "timer", "rm", "t3")
	mustAs(t, st, "yardmaster", "timer", "rm", "t1")
	if ts, _ := st.Timers(ctx, ""); len(ts) != 0 {
		t.Fatalf("left: %+v", ts)
	}
}

// The two settings bound what timer add takes, and refuse what is no
// number and no duration.
func TestTimerSettings(t *testing.T) {
	st := timerUserYard(t)
	refusedAs(t, st, "jo", "timer_limit \"many\": a whole number, 1 or more", "settings", "set", "timer_limit", "many")
	refusedAs(t, st, "jo", "timer_min_every: bad duration", "settings", "set", "timer_min_every", "often")
	mustAs(t, st, "jo", "settings", "set", "timer_limit", "1")
	mustAs(t, st, "jo", "settings", "set", "timer_min_every", "2s")
	mustAs(t, st, "yardmaster", "timer", "add", "--every", "2s", "--for", "1m", "x")
	refusedAs(t, st, "yardmaster", "yardmaster has 1 live timers, the most the yard allows one setter (timer_limit)", "timer", "add", "--in", "1h", "x")
	if out := mustAs(t, st, "jo", "settings", "get"); !strings.Contains(out, "timer_limit") || !strings.Contains(out, "timer_min_every") {
		t.Fatalf("settings get:\n%s", out)
	}
}

// A fired timer reads as a timer where its receiver reads: a crew member's
// inbox, a user's inbox and the conversation with whoever set it.
func TestTimerFiringIsRead(t *testing.T) {
	st := timerUserYard(t)
	ctx := context.Background()
	mustAs(t, st, "yardmaster", "timer", "add", "--in", "10m", "look at the deploy")
	mustAs(t, st, "yardmaster", "timer", "add", "--in", "10m", "--to", "jo", "the assembly is red")
	if pass, err := st.FireTimers(ctx, time.Now().Add(10*time.Minute+time.Second)); err != nil || pass.Fired != 2 {
		t.Fatalf("pass = %+v, %v", pass, err)
	}
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "  timer: look at the deploy (set ") ||
		!strings.Contains(out, " by yardmaster).") || strings.Contains(out, "assembly") {
		t.Fatalf("the yardmaster's inbox:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "inbox"); !strings.Contains(out, "mail from yardmaster: timer: the assembly is red (set ") {
		t.Fatalf("jo's inbox:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "mail", "--with", "yardmaster"); !strings.Contains(out, "yardmaster: timer: the assembly is red") {
		t.Fatalf("the conversation:\n%s", out)
	}
	var letters []store.Letter
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--json")), &letters); err != nil ||
		len(letters) != 1 || letters[0].Timer != "t2" {
		t.Fatalf("--json = %+v, %v", letters, err)
	}
}

// A crew member's live timers are in its prime and in crew show, a user's
// in user show; the prime says in its "How you work" when a timer is the
// right tool.
func TestTimersAreSeen(t *testing.T) {
	st := timerUserYard(t)
	mustAs(t, st, "yardmaster", "timer", "add", "--in", "40m", "look at the deploy")
	mustAs(t, st, "yardmaster", "timer", "add", "--every", "1d", "--to", "jo", "the assembly is red")
	mustAs(t, st, "clerk", "timer", "add", "--in", "1h", "the clerk's own")

	prime := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	for _, want := range []string{
		"## Timers: 2 live (yardr timer list, yardr timer rm <id>)",
		"(in 40m): look at the deploy",
		"- t2  tells jo ", ", then every 1d until ", ": the assembly is red",
		"- yardr timer add --in 40m|--at 15:04|--every 1d [--to <user>] [--unless bead=<id>[,stage=<stage>]]",
		"Prefer an event (a subscription, a bead's done",
	} {
		if !strings.Contains(prime, want) {
			t.Errorf("the prime lacks %q", want)
		}
	}
	if strings.Contains(prime, "the clerk's own") {
		t.Error("the yardmaster's prime lists the clerk's timer")
	}
	if t.Failed() {
		t.Logf("prime:\n%s", prime)
	}
	// With none, the prime has no section for them.
	mustAs(t, st, "jo", "user", "add", "pat")
	if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "scribe", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if out := mustAs(t, st, "jo", "prime", "--crew", "scribe"); strings.Contains(out, "## Timers") {
		t.Fatalf("a prime without timers has their section:\n%s", out)
	}

	if out := mustAs(t, st, "jo", "crew", "show", "yardmaster"); !strings.Contains(out, "  timer t1 ") ||
		!strings.Contains(out, "  timer t2 tells jo ") || strings.Contains(out, "clerk's own") {
		t.Fatalf("crew show:\n%s", out)
	}
	var shown struct {
		Timers []store.Timer `json:"timers"`
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "crew", "show", "yardmaster", "--json")), &shown); err != nil || len(shown.Timers) != 2 {
		t.Fatalf("crew show --json timers = %+v, %v", shown.Timers, err)
	}
	if out := mustAs(t, st, "jo", "user", "show", "jo"); !strings.Contains(out, "  timer t2 set by yardmaster, tells you ") {
		t.Fatalf("user show:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "user", "show", "pat"); strings.Contains(out, "timer") {
		t.Fatalf("user show of one without timers:\n%s", out)
	}
}

func TestParseAt(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	now := time.Date(2026, 10, 5, 14, 30, 0, 0, loc)
	for in, want := range map[string]time.Time{
		"15:04":                     time.Date(2026, 10, 5, 15, 4, 0, 0, loc),
		"09:00":                     time.Date(2026, 10, 6, 9, 0, 0, 0, loc), // gone today: tomorrow's
		"14:30":                     time.Date(2026, 10, 6, 14, 30, 0, 0, loc),
		"2026-10-07T08:00:00Z":      time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC),
		"2026-10-07T08:00:00+02:00": time.Date(2026, 10, 7, 8, 0, 0, 0, loc),
	} {
		if got, err := parseAt(in, now); err != nil || !got.Equal(want) {
			t.Errorf("parseAt(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "tomorrow", "25:00", "9am", "2026-10-07"} {
		if _, err := parseAt(bad, now); err == nil || !strings.Contains(err.Error(), "use RFC 3339") {
			t.Errorf("parseAt(%q) = %v", bad, err)
		}
	}
}

func TestParseUnless(t *testing.T) {
	for in, want := range map[string][2]string{
		"":                             {"", ""},
		"bead=yardr-1a2b":              {"yardr-1a2b", ""},
		"bead=yardr-1a2b,stage=review": {"yardr-1a2b", "review"},
		"stage=review, bead=web-1":     {"web-1", "review"},
	} {
		if bead, stage, err := parseUnless(in); err != nil || bead != want[0] || stage != want[1] {
			t.Errorf("parseUnless(%q) = %q, %q, %v", in, bead, stage, err)
		}
	}
	for _, bad := range []string{"yardr-1a2b", "stage=review", "bead=", "bead=a,bead=b", "bead=a,done=yes"} {
		if _, _, err := parseUnless(bad); err == nil {
			t.Errorf("parseUnless(%q) accepted", bad)
		}
	}
}
