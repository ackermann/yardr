package limits

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// sources is a yard's store as an ask reads it.
type sources []store.Source

func (s sources) Sources(context.Context) ([]store.Source, error) { return s, nil }

// standIn is a script in place of a provider's program: it is run as
// <script> <provider> --json, and body is what it does.
func standIn(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// yard is a store with one source, quokka, that says the limits of kimi (as
// "moonshot") and claude by running program.
func yard(program string) sources {
	return sources{
		{Name: "pushover", Text: "name: pushover\nbase: https://api.pushover.net/1\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"},
		{Name: "quokka", Text: "name: quokka\nlimits:\n  command: [" + program + ", \"{provider}\", --json]\n  kinds: { kimi: moonshot, claude: claude }\n"},
		{Name: "later", Text: "name: later\nsomething: this build does not read\n"},
	}
}

// kimi is what aiquokka printed for Kimi on 6 Oct 2026, with the keys it
// has added since: a window used up, and one that is not.
const kimi = `{"provider":"Kimi","plan":"Allegretto","windows":[` +
	`{"id":"5h","label":"5h","used_percent":100,"resets_at":"2026-10-06T22:02:32.309185Z","used":100,"limit":100,"duration_seconds":18000},` +
	`{"label":"Weekly","used_percent":38,"resets_at":"2026-10-10T09:02:32+02:00","used":38,"limit":100,"remaining":12.5,"currency":"USD"}],"extra":{"a":[1]}}`

// The command is run for the provider's name the kind maps to, without a
// shell, in the yard's home, with nothing to read and the caller's
// environment, and its answer is the report.
func TestAsk(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("QUOKKA_TEST", "from the caller")
	p := standIn(t, `{ echo "args: $*"; echo "dir: $(pwd)"; echo "env: $QUOKKA_TEST"; echo "stdin: $(cat)"; } >`+seen+`; echo '`+kimi+`'`)
	before := time.Now()
	r, err := Ask(context.Background(), yard(p), home, "kimi")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(seen)
	if want := "args: moonshot --json\ndir: " + home + "\nenv: from the caller\nstdin: \n"; string(got) != want {
		t.Fatalf("the command saw\n%s\nwant\n%s", got, want)
	}
	if r.Source != "quokka" || r.Provider != "Kimi" || r.Plan != "Allegretto" || len(r.Windows) != 2 || r.AskedAt.Before(before) || r.AskedAt.After(time.Now()) {
		t.Fatalf("report = %+v", r)
	}
	five, week := r.Windows[0], r.Windows[1]
	if five.Label != "5h" || *five.UsedPercent != 100 || *five.Used != 100 || *five.Limit != 100 ||
		!five.ResetsAt.Equal(time.Date(2026, 10, 6, 22, 2, 32, 309185000, time.UTC)) || !five.UsedUp() {
		t.Fatalf("5h = %+v", five)
	}
	if week.Label != "Weekly" || *week.UsedPercent != 38 || !week.ResetsAt.Equal(time.Date(2026, 10, 10, 7, 2, 32, 0, time.UTC)) || week.UsedUp() {
		t.Fatalf("Weekly = %+v", week)
	}
	if w := r.UsedUp(); w == nil || w.Label != "5h" {
		t.Fatalf("UsedUp = %+v", w)
	}

	// A kind no source lists is not asked for: nothing is known, as in a
	// yard that never heard of limits.
	os.Remove(seen)
	var none *NoSource
	if _, err := Ask(context.Background(), yard(p), home, "codex"); !errors.As(err, &none) || none.Kind != "codex" || !strings.Contains(err.Error(), "no source of this yard says the limits of codex") {
		t.Fatalf("a kind no source lists: %v", err)
	}
	if _, err := Ask(context.Background(), sources{}, home, "kimi"); !errors.As(err, &none) {
		t.Fatalf("a yard with no source: %v", err)
	}
	if _, err := os.Stat(seen); err == nil {
		t.Fatal("the command was run for a kind it does not answer for")
	}

	as, err := Answerers(context.Background(), yard(p), home)
	if err != nil || len(as) != 1 || as[0].Source != "quokka" || as[0].Missing() != nil {
		t.Fatalf("Answerers = %+v, %v", as, err)
	}
	if as, _ = Answerers(context.Background(), yard(filepath.Join(home, "nope")), home); len(as) != 1 || as[0].Missing() == nil ||
		!strings.Contains(as[0].Missing().Error(), "limits command "+filepath.Join(home, "nope")) {
		t.Fatalf("a program that is not there: %+v", as)
	}
}

// Whatever is not exit 0 with the report on stdout is no answer, and says
// why in a line that ends with what the command said on stderr. What it
// printed on stdout is in no error: a provider that fails may have printed
// anything there.
func TestAskThatIsNotAnswered(t *testing.T) {
	const printed = "sk-what-a-provider-must-not-leak"
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"exit 1":           {`echo '{"provider":"` + printed + `"}'; echo "aiquokka: kimi: not logged in" >&2; exit 1`, []string{"exit status 1", "its stderr ended: aiquokka: kimi: not logged in"}},
		"no JSON":          {`echo "` + printed + ` 5h 100%"`, []string{"what it printed is not the JSON object the yard reads (provider, windows)", "no JSON at byte"}},
		"nothing":          {`exit 0`, []string{"it printed nothing"}},
		"half an object":   {`printf '{"provider":"` + printed + `","windows":['`, []string{"the JSON ends early"}},
		"two objects":      {`echo '{"provider":"K","windows":[]}{"provider":"K","windows":[]}'`, []string{"more than one JSON object"}},
		"a list":           {`echo '[{"provider":"K","windows":[]}]'`, []string{"is not the JSON object the yard reads"}},
		"no provider":      {`echo '{"windows":[]}'`, []string{"its answer has no provider"}},
		"no windows":       {`echo '{"provider":"K"}'`, []string{"its answer has no windows"}},
		"windows by name":  {`echo '{"provider":"K","windows":{"5h":{}}}'`, []string{"windows is object, not"}},
		"a window unnamed": {`echo '{"provider":"K","windows":[{"used_percent":3}]}'`, []string{"window 1 of its answer has no label"}},
		"percent as text":  {`echo '{"provider":"K","windows":[{"label":"5h","used_percent":"100%"}]}'`, []string{"used_percent is string, not float64"}},
		"used as a part":   {`echo '{"provider":"K","windows":[{"label":"5h","used":1.5,"limit":3}]}'`, []string{"used is number 1.5, not int64"}},
		"a reset in words": {`echo '{"provider":"K","windows":[{"label":"5h","resets_at":"in 42 minutes"}]}'`, []string{`window 5h of its answer resets at "in 42 minutes", which is no time as RFC 3339 writes one`}},
		"too much":         {`head -c 70000 /dev/zero | tr '\0' ' '; echo '{"provider":"K","windows":[]}'`, []string{"printed more than 65536 bytes"}},
		"not there":        {"", []string{"no such file or directory"}},
	} {
		p := standIn(t, tc.body)
		if name == "not there" {
			os.Remove(p)
		}
		r, err := Ask(context.Background(), yard(p), t.TempDir(), "kimi")
		if err == nil {
			t.Errorf("%s: an answer: %+v", name, r)
			continue
		}
		var none *NoSource
		if errors.As(err, &none) {
			t.Errorf("%s: a failing provider reads as no source: %v", name, err)
		}
		for _, w := range append(tc.want, "source quokka: limits command "+p+" moonshot --json: ") {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", name, err, w)
			}
		}
		if strings.Contains(err.Error(), printed) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: the error shows what was printed, or is no line: %q", name, err)
		}
	}
}

// What a provider leaves out is not known, and so is a reset in year 1,
// which is how a time that was never set is printed. No window is a report
// too.
func TestAskReadsWhatIsLeftOut(t *testing.T) {
	p := standIn(t, `echo '{"provider":"Claude","windows":[{"label":"5h","used_percent":99.5},{"label":"Weekly","resets_at":"0001-01-01T00:00:00Z","used":7,"limit":7},{"label":"Open","used":0,"limit":0}]}'`)
	r, err := Ask(context.Background(), yard(p), t.TempDir(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	five, week, open := r.Windows[0], r.Windows[1], r.Windows[2]
	if five.ResetsAt != nil || five.Used != nil || five.Limit != nil || five.UsedUp() {
		t.Fatalf("5h = %+v", five)
	}
	if week.ResetsAt != nil || week.UsedPercent != nil || !week.UsedUp() {
		t.Fatalf("Weekly = %+v", week)
	}
	if open.UsedUp() {
		t.Fatalf("a window with no limit is used up: %+v", open)
	}
	if w := r.UsedUp(); w == nil || w.Label != "Weekly" {
		t.Fatalf("UsedUp = %+v", w)
	}
	p = standIn(t, `echo '{"provider":"Claude","windows":[]}'`)
	if r, err = Ask(context.Background(), yard(p), t.TempDir(), "claude"); err != nil || len(r.Windows) != 0 || r.UsedUp() != nil {
		t.Fatalf("no window = %+v, %v", r, err)
	}
}

// Of the windows that are used up, the one an agent waits for resets last;
// a reset that is known says more than one that is not.
func TestUsedUpIsTheWindowThatResetsLast(t *testing.T) {
	at := func(h int) *time.Time { t := time.Date(2026, 10, 7, h, 0, 0, 0, time.UTC); return &t }
	full := 100.0
	r := &Report{Windows: []Window{
		{Label: "unknown", UsedPercent: &full},
		{Label: "5h", UsedPercent: &full, ResetsAt: at(2)},
		{Label: "Weekly", UsedPercent: &full, ResetsAt: at(9)},
		{Label: "Monthly", ResetsAt: at(20)},
	}}
	if w := r.UsedUp(); w == nil || w.Label != "Weekly" {
		t.Fatalf("UsedUp = %+v", w)
	}
	r.Windows = r.Windows[:1]
	if w := r.UsedUp(); w == nil || w.Label != "unknown" {
		t.Fatalf("UsedUp = %+v", w)
	}
}

// reached is a context whose deadline has passed once the returned func is
// called, and not a moment before: the limit of an ask, at a time the test
// decides (as internal/config's tests of a command do).
func reached() (context.Context, func()) {
	d := &deadline{Context: context.Background(), done: make(chan struct{})}
	return d, func() { close(d.done) }
}

type deadline struct {
	context.Context
	done chan struct{}
}

func (d *deadline) Done() <-chan struct{} { return d.done }

func (d *deadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// A command that does not answer in time is given up on and said to have
// been, with what it said it was doing, and nothing of it is left: the
// script, and the sleep it started, are killed as one group. The time is up
// when the script has started its sleep, not by the clock: how fast the
// machine starts a script is not what is tested.
func TestAskGivesUpAtTheTimeoutAndLeavesNoProcess(t *testing.T) {
	old := Timeout
	Timeout = time.Hour
	t.Cleanup(func() { Timeout = old })
	dir := t.TempDir()
	pidfile, waiting := filepath.Join(dir, "pid"), filepath.Join(dir, "waiting")
	if err := syscall.Mkfifo(waiting, 0o600); err != nil {
		t.Fatal(err)
	}
	p := standIn(t, `echo $$ >`+pidfile+`; echo "refreshing the login" >&2; sleep 300 & : >`+waiting+`; wait`)
	ctx, timeUp := reached()
	go func() {
		// Returns when the script opens the fifo: it has said what it
		// does, and its sleep runs.
		if f, err := os.Open(waiting); err == nil {
			f.Close()
		}
		timeUp()
	}()
	start := time.Now()
	_, err := Ask(ctx, yard(p), t.TempDir(), "kimi")
	if err == nil || !strings.Contains(err.Error(), "no answer within 1h0m0s; its stderr ended: refreshing the login") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 2*time.Minute {
		t.Fatalf("took %s: the sleep was waited for", took)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	// The command is its group's leader, so the group has its pid.
	pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pgid <= 1 {
		t.Fatalf("pid %q: %v", b, err)
	}
	// A killed child is in the group until it is reaped, which is not
	// ours to do: it is given a moment.
	gone := false
	for wait := time.Now().Add(5 * time.Second); !gone && time.Now().Before(wait); time.Sleep(10 * time.Millisecond) {
		gone = syscall.Kill(-pgid, 0) == syscall.ESRCH
	}
	if !gone {
		syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatal("the command left a process running")
	}
}

// The limit itself: a command that does not answer within Timeout is given
// up on. Whether it had started by then is the machine's affair, so nothing
// of its stderr is asked for here.
func TestAskTimeout(t *testing.T) {
	old := Timeout
	Timeout = 50 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	start := time.Now()
	_, err := Ask(context.Background(), yard(standIn(t, `exec sleep 300`)), t.TempDir(), "kimi")
	if err == nil || !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 2*time.Minute {
		t.Fatalf("took %s: the sleep was waited for", took)
	}
}
