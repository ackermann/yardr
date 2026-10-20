package store

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func harnessKeyAsk(harness, crew, to string, env ...string) Ask {
	return Ask{Question: "The key it runs with", To: to,
		Context: []string{"Create one at https://console.anthropic.com/ (API usage billing)."},
		Secret:  &AskSecret{Harness: harness, Crew: crew, Env: env}}
}

const harnessKeyLine = "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster; the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"

// An ask for a crew member's harness API key is stored with the variables
// the kind's harness file names, whatever the asker sent, and its card
// says whose harness runs with it, that the env_file line is the one
// written, and when it takes effect. It is asked of the operator, and no
// source declares it.
func TestHarnessKeyAskIsStoredWithTheFilesNames(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	ask := harnessKeyAsk("claude", "yardmaster", "mckean")
	seq := y.ask(t, ask, "yardmaster")
	notes := mustNotes(t, y)
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"crew":"yardmaster","env":["ANTHROPIC_API_KEY"],"harness":"claude","source":""}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}

	rs := y.resolved(t)
	lines := strings.Join(rs[0].Lines(), "\n")
	for _, want := range []string{
		"  " + harnessKeyLine,
		"  it goes to a file only the yard's server reads; no agent is shown it",
		"  answer with: yardr bead answer " + y.bead + " " + itoa(seq) + " --secret",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the card lacks %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "words wanted") {
		t.Errorf("the card:\n%s", lines)
	}
	// In the operator's own queue, as footplate reads it.
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 || q.Open[0].Secret == nil || q.Open[0].Secret.Harness != "claude" || q.Open[0].Secret.Crew != "yardmaster" ||
		q.Open[0].Secret.Line != harnessKeyLine || strings.Join(q.Open[0].Secret.Env, ",") != "ANTHROPIC_API_KEY" {
		t.Fatalf("mckean's queue = %+v", q)
	}
}

// It is refused, and nothing is stored: for a kind with no file, one
// whose file names no login_env, a name the file does not name, no crew
// member, one the yard does not have, anyone asked but the operator,
// another yard, and beside a source, a user, a hook or a waiting login.
func TestHarnessKeyAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	with := func(change func(*Ask)) Ask {
		a := harnessKeyAsk("claude", "yardmaster", "mckean")
		change(&a)
		return a
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"no such kind", with(func(a *Ask) { a.Secret.Harness = "nope" }), "harness nope: the yard has no harness file for it"},
		{"no login_env", with(func(a *Ask) { a.Secret.Harness = "codex" }), "harness codex: its file names no login_env, so there is no API key to ask for"},
		{"a name", with(func(a *Ask) { a.Secret.Env = []string{"OTHER_KEY"} }), "OTHER_KEY is no variable of its API key (its file names ANTHROPIC_API_KEY)"},
		{"no crew", with(func(a *Ask) { a.Secret.Crew = "" }), "asked for the crew member whose harness runs with it"},
		{"no such crew", with(func(a *Ask) { a.Secret.Crew = "eve" }), "crew member eve: not found"},
		{"not operator", with(func(a *Ask) { a.To = "ada" }), "giving a crew member's harness key is theirs alone, so ada cannot be asked for one"},
		{"a crew member", with(func(a *Ask) { a.To = "yardmaster" }), "so yardmaster cannot be asked for one"},
		{"another yard", with(func(a *Ask) { a.To = "mckean@north" }), "nothing of a secret crosses a link"},
		{"options", with(func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
		{"a source beside it", with(func(a *Ask) { a.Secret.Source = "crm" }), "names a harness or a source, not both"},
		{"a user beside it", with(func(a *Ask) { a.Secret.User = "ada" }), "not ada's address: it takes no --for-user"},
		{"a hook beside it", with(func(a *Ask) { a.Secret.Hook = "bot" }), "is no hook's signing secret: it takes no hook"},
		{"a login beside it", with(func(a *Ask) { a.Secret.Harness = ""; a.Secret.Login = "claude-x" }), "it takes no --for-crew"},
	} {
		_, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if notes := mustNotes(t, y); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}
}

// GiveSecret writes a crew member's harness API key into the crew
// member's file, sets it as their env_file where they had none, and
// answers the ask with a note that holds the names. It restarts nothing:
// the answer says when the value takes effect. The value is in the file
// and in nothing the store has.
func TestGiveSecretGivesAHarnessKey(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := crewSecretYard(t)
	seq := y.ask(t, harnessKeyAsk("claude", "yardmaster", "mckean"), "yardmaster")
	const value = "sk-ant-s3cret"
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"ANTHROPIC_API_KEY": value}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.Source != nil || keyed.File != file || keyed.Crew != "yardmaster" || keyed.User != "" ||
		keyed.State != "ANTHROPIC_API_KEY is set in "+file || strings.Join(keyed.Given, ",") != "ANTHROPIC_API_KEY" ||
		keyed.Next != "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "ANTHROPIC_API_KEY="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode = %v, %v", st.Mode(), err)
	}
	r, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil || r.Config["env_file"] != file {
		t.Fatalf("yardmaster = %+v, %v: want the env_file set", r, err)
	}
	if n := crewDefinedEvents(t, y); n != 2 {
		t.Fatalf("%d crew_defined events, want 2: the definition, and the env_file set as crew set sets it", n)
	}
	// The running session is restarted by nothing: no restart asked.
	if r.RestartRequested != "" {
		t.Fatalf("a restart was requested: %q", r.RestartRequested)
	}

	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "mckean" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["ANTHROPIC_API_KEY"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	if lines := strings.Join(y.resolved(t)[0].Lines(), "\n"); !strings.Contains(lines, "  mckean gave ANTHROPIC_API_KEY") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	e := lastAnswer(t, y)
	if line, _ := e.Data["answer"].(string); line != "mckean gave ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}

	// Giving again replaces.
	keyed, err = y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"ANTHROPIC_API_KEY": "typed-again"}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.Next != "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)" {
		t.Fatalf("next = %q", keyed.Next)
	}
	if b, _ := os.ReadFile(file); string(b) != "ANTHROPIC_API_KEY=typed-again\n" {
		t.Fatalf("the file holds %q after the second giving", b)
	}

	// Neither value is in a note, an event or the store's file.
	notes, _ := json.Marshal(mustNotes(t, y))
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := json.Marshal(evs)
	db, err := os.ReadFile(y.db.Path())
	if err != nil {
		t.Fatal(err)
	}
	for where, kept := range map[string][]byte{"the notes": notes, "the events": events, "the store's file": db} {
		for _, v := range []string{value, "typed-again"} {
			if bytes.Contains(kept, []byte(v)) {
				t.Errorf("%s hold a value", where)
			}
		}
	}
}

// A crew member's harness API key is given by the operator, and by
// nobody else: another user and a crew member are refused, and so is a
// name the ask did not ask for. No refusal says back what was sent, and
// none writes the file, sets the env_file, or notes an answer.
func TestGiveSecretRefusesAHarnessKey(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := crewSecretYard(t)
	seq := y.ask(t, harnessKeyAsk("claude", "yardmaster", "mckean"), "yardmaster")
	const value = "sk-ant-s3cret"
	for actor, want := range map[string]string{
		"ada":        "only the operator (mckean) may give a crew member's harness key; this is ada",
		"yardmaster": "only the operator (mckean) may give a crew member's harness key; this is yardmaster",
		"":           "this is nobody",
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"ANTHROPIC_API_KEY": value}, actor)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), value) {
			t.Errorf("as %q: err = %v, want %q", actor, err, want)
		}
	}
	for _, values := range []map[string]string{
		{"OTHER_KEY": value},
		{value: "ANTHROPIC_API_KEY"},
		{"ANTHROPIC_API_KEY": value, "MORE": "1"},
		{},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, values, "mckean")
		if err == nil || strings.Contains(err.Error(), value) {
			t.Errorf("%d names: err = %v", len(values), err)
		}
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote the file: %v", err)
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes after refused givings", n)
	}
	if r, err := y.GetCrew(y.ctx, "yardmaster"); err != nil || len(r.Config) != 0 {
		t.Fatalf("yardmaster = %+v, %v", r, err)
	}
	// A crew member gone since the ask is refused naming that.
	if err := y.DeleteCrew(y.ctx, "yardmaster", "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"ANTHROPIC_API_KEY": value}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "crew member yardmaster: not found") || strings.Contains(err.Error(), value) {
		t.Fatalf("after the crew member went: %v", err)
	}
}
