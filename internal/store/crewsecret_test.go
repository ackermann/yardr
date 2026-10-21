package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crewSecretYard is secretYard with the file a crew member's own
// credential goes to when it has no env_file.
func crewSecretYard(t *testing.T) (*askYard, string) {
	t.Helper()
	y, _ := secretYard(t)
	return y, filepath.Join(y.home, CrewDir, "yardmaster.env")
}

func crewSecretAsk(crew, to string, env ...string) Ask {
	return Ask{Question: "The token it answers with", To: to,
		Context: []string{"Create one at https://crm.example.com/account/api (read only)."},
		Secret:  &AskSecret{Source: "crm", Env: env, Crew: crew}}
}

// crewDefinedEvents counts the crew_defined events of the store.
func crewDefinedEvents(t *testing.T, y *askYard) int {
	t.Helper()
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "crew_defined" {
			n++
		}
	}
	return n
}

const crewSecretLine = "a secret: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"

// An ask for a crew member's own credential in a source is stored with
// the variables the source's declaration names for its credential,
// whatever the asker sent, and its card says for which crew member, that
// the env_file line is the one written, and when it takes effect. It is
// asked of the operator.
func TestCrewAskIsStoredWithTheDeclarationsNames(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	ask := crewSecretAsk("yardmaster", "mckean")
	ask.Secret.Host = "https://evil.example.com"
	seq := y.ask(t, ask, "yardmaster")
	notes := mustNotes(t, y)
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"crew":"yardmaster","env":["CRM_TOKEN"],"host":"https://crm.example.com","source":"crm"}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}
	// Narrowed, in the declaration's order; without --env, every one.
	narrowed := crewSecretAsk("yardmaster", "mckean", "PAIR_UPDATE_TOKEN")
	narrowed.Secret.Source = "pair"
	y.ask(t, narrowed, "yardmaster")
	both := crewSecretAsk("yardmaster", "mckean")
	both.Secret.Source = "pair"
	y.ask(t, both, "yardmaster")
	rs := y.resolved(t)
	if got := rs[1].Ask.Secret.Names(); got != "PAIR_UPDATE_TOKEN" {
		t.Fatalf("the narrowed ask asks for %q", got)
	}
	if got := rs[2].Ask.Secret.Names(); got != "PAIR_TOKEN and PAIR_UPDATE_TOKEN" {
		t.Fatalf("the unnarrowed ask asks for %q", got)
	}

	lines := strings.Join(rs[0].Lines(), "\n")
	for _, want := range []string{
		"  " + crewSecretLine,
		"  it goes to a file only the yard's server reads; no agent is shown it",
		"  answer with: yardr bead answer " + y.bead + " " + itoa(seq) + " --secret",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the card lacks %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "words wanted") || strings.Contains(lines, "evil") {
		t.Errorf("the card:\n%s", lines)
	}
	// In the operator's own queue, as footplate reads it.
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 3 || q.Open[0].Secret == nil || q.Open[0].Secret.Crew != "yardmaster" || q.Open[0].Secret.Line != crewSecretLine ||
		strings.Join(q.Open[0].Secret.Env, ",") != "CRM_TOKEN" {
		t.Fatalf("mckean's queue = %+v", q)
	}
}

// An ask for a crew member's own credential in a source whose credential
// is minted is stored asking for the mint's inputs alone, as the source's
// own is: the token is minted per call, never given.
func TestCrewAskMintedSourceAsksForTheMintsInputs(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	if _, err := y.PutSource(y.ctx, "gog.yaml", mintKeyFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := crewSecretAsk("yardmaster", "mckean")
	ask.Secret.Source = "gog"
	y.ask(t, ask, "yardmaster")
	if got := y.resolved(t)[0].Ask.Secret.Names(); got != "GOG_ACCOUNT" {
		t.Fatalf("the unnarrowed ask asks for %q", got)
	}
}

// It is refused, and nothing is stored: for a source with no credential,
// a crew member the yard does not have, a name the declaration does not
// have, anyone asked but the operator, another yard, and beside a user,
// a hook or a waiting login.
func TestCrewAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	with := func(change func(*Ask)) Ask {
		a := crewSecretAsk("yardmaster", "mckean")
		change(&a)
		return a
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"no credential", with(func(a *Ask) { a.Secret.Source = "open" }), "source open: its declaration names no credential, so there is no secret to ask for"},
		{"no such source", with(func(a *Ask) { a.Secret.Source = "nowhere" }), "source nowhere: not found"},
		{"no such crew", with(func(a *Ask) { a.Secret.Crew = "eve" }), "crew member eve: not found"},
		{"a name", with(func(a *Ask) { a.Secret.Env = []string{"PUSHOVER_TOKEN"} }), "PUSHOVER_TOKEN is no variable of its credential (its declaration names CRM_TOKEN)"},
		{"not operator", with(func(a *Ask) { a.To = "ada" }), "giving a crew member's credential is theirs alone, so ada cannot be asked for one"},
		{"a crew member", with(func(a *Ask) { a.To = "yardmaster" }), "so yardmaster cannot be asked for one"},
		{"another yard", with(func(a *Ask) { a.To = "mckean@north" }), "nothing of a secret crosses a link"},
		{"options", with(func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
		{"a user beside it", with(func(a *Ask) { a.Secret.User = "ada" }), "a secret ask names a user or a crew member, not both"},
		{"a hook beside it", with(func(a *Ask) { a.Secret.Hook = "bot" }), "it takes no --for-crew"},
		{"a login beside it", with(func(a *Ask) { a.Secret.Source = ""; a.Secret.Login = "claude-x" }), "it takes no --for-crew"},
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

// GiveSecret writes a crew member's own credential into the crew
// member's file, sets it as their env_file where they had none, and
// answers the ask with a note that holds the names. It restarts nothing:
// the answer says when the value takes effect. The value is in the file
// and in nothing the store has.
func TestGiveSecretGivesACrewMembersOwnCredential(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := crewSecretYard(t)
	seq := y.ask(t, crewSecretAsk("yardmaster", "mckean"), "yardmaster")
	const value = "tok-0f-s3cret"
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": value}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.Crew != "yardmaster" || keyed.User != "" ||
		keyed.State != "CRM_TOKEN is set in "+file || strings.Join(keyed.Given, ",") != "CRM_TOKEN" ||
		keyed.Next != "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+value+"\n" {
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
	// The source's own credential is another file, and untouched.
	if src, err := y.GetSource(y.ctx, "crm"); err != nil || src.EnvFile != "" {
		t.Fatalf("the source = %+v, %v", src, err)
	}
	// The running session is restarted by nothing: no restart asked.
	if r.RestartRequested != "" {
		t.Fatalf("a restart was requested: %q", r.RestartRequested)
	}

	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "mckean" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["CRM_TOKEN"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	if lines := strings.Join(y.resolved(t)[0].Lines(), "\n"); !strings.Contains(lines, "  mckean gave CRM_TOKEN") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the asker heard %q", got)
	}
	if line, _ := e.Data["answer"].(string); line != "mckean gave CRM_TOKEN for crew member yardmaster in the source crm (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}

	// Giving again replaces.
	keyed, err = y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "typed-again"}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.Next != "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)" {
		t.Fatalf("next = %q", keyed.Next)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=typed-again\n" {
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

// A crew member's own credential is given by the operator, and by nobody
// else: another user and a crew member are refused, and so is a name the
// ask did not ask for. No refusal says back what was sent, and none
// writes the file, sets the env_file, or notes an answer.
func TestGiveSecretRefusesACrewMembersCredential(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := crewSecretYard(t)
	seq := y.ask(t, crewSecretAsk("yardmaster", "mckean"), "yardmaster")
	const value = "tok-0f-s3cret"
	for actor, want := range map[string]string{
		"ada":        "only the operator (mckean) may give a crew member's credential; this is ada",
		"yardmaster": "only the operator (mckean) may give a crew member's credential; this is yardmaster",
		"":           "this is nobody",
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": value}, actor, true)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), value) {
			t.Errorf("as %q: err = %v, want %q", actor, err, want)
		}
	}
	for _, values := range []map[string]string{
		{"CRM_UPDATE_TOKEN": value},
		{value: "CRM_TOKEN"},
		{"CRM_TOKEN": value, "MORE": "1"},
		{},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, values, "mckean", true)
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
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": value}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "crew member yardmaster: not found") || strings.Contains(err.Error(), value) {
		t.Fatalf("after the crew member went: %v", err)
	}
}

// A crew member with an env_file already has the value written there, its
// other lines kept and its definition untouched; one with an env_command
// as well keeps both, the env_file line being the one written.
func TestGiveCrewSecretKeepsAnEnvFileAndAnEnvCommand(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := crewSecretYard(t)
	file := filepath.Join(t.TempDir(), "crew.env")
	if err := os.WriteFile(file, []byte("OTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"env_file": file, "env_command": "vault read yardmaster"}}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, crewSecretAsk("yardmaster", "mckean"), "yardmaster")
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "tok-0f-s3cret"}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.Crew != "yardmaster" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "OTHER=1\nCRM_TOKEN=tok-0f-s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	after, err := y.GetCrew(y.ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.Config["env_file"] != file || after.Config["env_command"] != "vault read yardmaster" {
		t.Fatalf("the definition moved: %+v", after)
	}
}

// A crew ask answered with a URL-credential value that is no address is
// refused before the answer note, in words that name the fault and none of
// the value; an https address answers it. A refused value leaves no note
// and no file.
func TestGiveSecretRefusesACrewCredentialThatIsNoAddress(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := crewSecretYard(t)
	if _, err := y.PutSource(y.ctx, "hook.yaml", urlKeyFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := crewSecretAsk("yardmaster", "mckean")
	ask.Secret.Source = "hook"
	seq := y.ask(t, ask, "yardmaster")
	const secret = "abc123"
	for _, tc := range []struct{ value, want string }{
		{"https://hooks.example.com/catch/" + secret + "?key=" + secret, "has a query or a fragment"},
		{"http://hooks.example.com/catch/" + secret, "is plain http to a machine that is not this one"},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"HOOK_URL": tc.value}, "mckean", true)
		if err == nil || !strings.Contains(err.Error(), "source hook: the value of HOOK_URL "+tc.want) {
			t.Errorf("%q: err = %v, want the fault named", tc.value, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hooks.example.com") {
			t.Errorf("%q: the error repeats the value: %v", tc.value, err)
		}
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote the file: %v", err)
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes after refused givings", n)
	}
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"HOOK_URL": "https://hooks.example.com/catch/" + secret}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.Crew != "yardmaster" || strings.Join(keyed.Given, ",") != "HOOK_URL" {
		t.Fatalf("keyed = %+v", keyed)
	}
}
