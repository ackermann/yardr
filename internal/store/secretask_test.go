package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

const twoKeyFile = `name: pair
base: https://pair.example.com:8443/v1
credential: { env: PAIR_TOKEN, update_env: PAIR_UPDATE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
  put: { kind: update, request: { method: POST, path: /it } }
`

const noKeyFile = `name: open
base: https://open.example.com
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// secretYard is an ask yard whose operator is mckean, with the crew member
// yardmaster, the user ada, and three sources: crm with one variable, pair
// with two, and open with no credential. It answers the file crm's
// credential goes to when none is set.
func secretYard(t *testing.T) (*askYard, string) {
	t.Helper()
	y := answerYard(t)
	if err := y.SetSetting(y.ctx, OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	for file, text := range map[string]string{"crm.yaml": keyedFile, "pair.yaml": twoKeyFile, "open.yaml": noKeyFile} {
		if _, err := y.PutSource(y.ctx, file, text, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	return y, filepath.Join(y.home, "sources", "crm.env")
}

func secretAsk(source string, env ...string) Ask {
	return Ask{Question: "The API key for the CRM", To: "mckean",
		Context: []string{"Create one at https://crm.example.com/account/api (read only)."},
		Secret:  &AskSecret{Source: source, Env: env}}
}

// answerNotes are the bead's notes of kind answer.
func answerNotes(t *testing.T, y *askYard) []Note {
	t.Helper()
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	var out []Note
	for _, n := range notes {
		if n.Kind == NoteAnswer {
			out = append(out, n)
		}
	}
	return out
}

// A secret ask is stored with the field that says where its answer goes:
// the source, the names its declaration gives the credential, and where
// its calls go. All of it is the yard's, whatever the asker sent.
func TestSecretAskIsStoredWithTheDeclarationsNames(t *testing.T) {
	y, _ := secretYard(t)
	ask := secretAsk("crm")
	ask.Secret.Host = "https://evil.example.com"
	seq := y.ask(t, ask, "yardmaster")
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"env":["CRM_TOKEN"],"host":"https://crm.example.com","source":"crm"}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}
	// Narrowed, in the declaration's order; without --env, every one.
	y.ask(t, secretAsk("pair", "PAIR_UPDATE_TOKEN"), "yardmaster")
	y.ask(t, secretAsk("pair"), "yardmaster")
	rs := y.resolved(t)
	if got := rs[1].Ask.Secret.Names(); got != "PAIR_UPDATE_TOKEN" {
		t.Fatalf("the narrowed ask asks for %q", got)
	}
	if got := rs[2].Ask.Secret.Line(); got != "a secret: PAIR_TOKEN and PAIR_UPDATE_TOKEN, for the source pair (https://pair.example.com:8443)" {
		t.Fatalf("the line = %q", got)
	}

	lines := strings.Join(rs[0].Lines(), "\n")
	for _, want := range []string{
		"  a secret: CRM_TOKEN, for the source crm (https://crm.example.com)",
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
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

// A secret ask is refused, and nothing is stored, in each case the design
// lists: a source the yard does not have, one without a credential, a name
// its declaration does not have, anyone but the operator, another yard, and
// with options, a recommendation or about-beads.
func TestSecretAskRefusals(t *testing.T) {
	y, _ := secretYard(t)
	other, err := y.CreateBead(y.ctx, NewBead{Title: "another"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	with := func(source string, change func(*Ask)) Ask {
		a := secretAsk(source)
		if change != nil {
			change(&a)
		}
		return a
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"no such source", with("nowhere", nil), "source nowhere: not found"},
		{"no source named", with(" ", nil), "names the source"},
		{"no credential", with("open", nil), "its declaration names no credential"},
		{"a name it does not have", with("crm", func(a *Ask) { a.Secret.Env = []string{"GITHUB_PASSWORD"} }), "GITHUB_PASSWORD is no variable of its credential"},
		{"not the operator", with("crm", func(a *Ask) { a.To = "ada" }), "a secret is the operator's to give (mckean)"},
		{"another yard", with("crm", func(a *Ask) { a.To = "mckean@north" }), "nothing of a secret crosses a link"},
		{"options", with("crm", func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
		{"about", with("crm", func(a *Ask) { a.About = []string{other.ID} }), "takes no --about"},
	} {
		_, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if _, err := y.AskNew(y.ctx, NewBead{}, c.ask, "yardmaster"); err == nil {
			t.Errorf("%s: asked with a new bead", c.name)
		}
	}
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}
	if beads, _ := y.ListBeads(y.ctx, BeadFilter{All: true}); len(beads) != 2 {
		t.Fatalf("refused asks left %d beads, want the 2 there were", len(beads))
	}
	// A yard without an operator has nobody a secret can be asked of.
	if err := y.SetSetting(y.ctx, OperatorSetting, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, secretAsk("crm"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "this yard has none") {
		t.Fatalf("without an operator: %v", err)
	}
}

// A secret ask is not copied to a name a link gives reach for the operator,
// where any other ask for them is.
func TestSecretAskIsNotCopiedToALinkedYard(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	if err := y.SetSetting(y.ctx, OperatorSetting, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "crm.yaml", keyedFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	bead, _ := y.asked(t, linksAsk())
	if got := y.copies(t, "north"); len(got) != 1 {
		t.Fatalf("a plain ask for the operator was copied %d times, want once: %q", len(got), got)
	}
	if _, err := y.AddAsk(y.ctx, bead, secretAsk("crm"), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := y.copies(t, "north"); len(got) != 1 {
		t.Fatalf("a secret ask was copied to north: %q", got)
	}
}

// GiveSecret writes the value into the file the source's credential is read
// from and answers the ask with a note that holds the names: the ask reads
// "gave <names>" in every view, the asker's inbox says who gave what for
// which source, and the value is in the file and in nothing the store has.
func TestGiveSecret(t *testing.T) {
	y, file := secretYard(t)
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	const value = "s3cret-0f-the-yard"
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": value}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.State != "CRM_TOKEN is set in "+file || strings.Join(keyed.Given, ",") != "CRM_TOKEN" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if src, err := y.GetSource(y.ctx, "crm"); err != nil || src.EnvFile != file {
		t.Fatalf("the source = %+v, %v", src, err)
	}
	if n := sourceSetEvents(t, y.Store); n != 1 {
		t.Fatalf("%d source_set events, want 1", n)
	}

	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "mckean" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["CRM_TOKEN"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	r := y.resolved(t)[0]
	if r.State != AskAnswered {
		t.Fatalf("the ask is %s", r.State)
	}
	lines := strings.Join(r.Lines(), "\n")
	if !strings.Contains(lines, "  mckean gave CRM_TOKEN") || !strings.Contains(lines, "  a secret: CRM_TOKEN, for the source crm") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	if blocks := strings.Join(NoteBlocks(mustNotes(t, y), y.now, ""), "\n"); !strings.Contains(blocks, "mckean gave CRM_TOKEN") || strings.Contains(blocks, `"given"`) {
		t.Fatalf("the notes read:\n%s", blocks)
	}

	// Bead and UserAsks, as footplate and the web view read them.
	v, err := y.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	a := v.Asks[0]
	if a.State != "answered" || a.Secret == nil || a.Secret.Line != "a secret: CRM_TOKEN, for the source crm (https://crm.example.com)" ||
		a.Secret.Source != "crm" || strings.Join(a.Secret.Env, ",") != "CRM_TOKEN" || strings.Join(a.Given, ",") != "CRM_TOKEN" || a.Words != "" {
		t.Fatalf("Bead's ask = %+v (secret %+v)", a, a.Secret)
	}
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Secret == nil || strings.Join(q.Decided[0].Given, ",") != "CRM_TOKEN" {
		t.Fatalf("UserAsks = %+v", q)
	}

	// The asker hears it, in the words of the design's section 3.5.
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the asker heard %q", got)
	}
	if line, _ := e.Data["answer"].(string); line != "mckean gave CRM_TOKEN for the source crm (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}

	// Giving again replaces, and is a note more.
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "typed-again"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=typed-again\n" {
		t.Fatalf("the file holds %q after the second giving", b)
	}
	if n := len(answerNotes(t, y)); n != 2 {
		t.Fatalf("%d answer notes, want 2", n)
	}
	if n := sourceSetEvents(t, y.Store); n != 1 {
		t.Fatalf("%d source_set events after the second giving, want 1", n)
	}

	// Neither value is in a note, an event or the store's file.
	notes, _ := json.Marshal(mustNotes(t, y))
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := json.Marshal(evs)
	// Every write above is committed, so the file has all the store keeps.
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

func mustNotes(t *testing.T, y *askYard) []Note {
	t.Helper()
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

// An ask with two names takes both together, and neither alone.
func TestGiveSecretTakesExactlyTheNamesAsked(t *testing.T) {
	y, _ := secretYard(t)
	seq := y.ask(t, secretAsk("pair"), "yardmaster")
	one := y.ask(t, secretAsk("pair", "PAIR_TOKEN"), "yardmaster")
	const value = "s3cret-0f-the-yard"
	for _, c := range []struct {
		ask    uint64
		values map[string]string
		want   string
	}{
		{seq, map[string]string{"PAIR_TOKEN": value}, "PAIR_UPDATE_TOKEN was not given"},
		{seq, map[string]string{}, "PAIR_TOKEN was not given"},
		{one, map[string]string{"PAIR_TOKEN": value, "PAIR_UPDATE_TOKEN": value}, "a name it did not ask for"},
		// A value where a name goes is not said back.
		{one, map[string]string{value: "PAIR_TOKEN"}, "a name it did not ask for"},
		{one, map[string]string{"PAIR_TOKEN": ""}, "empty"},
		{one, map[string]string{"PAIR_TOKEN": value + "\nMORE=1"}, "line break"},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, c.ask, c.values, "mckean")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), value) {
			t.Errorf("%v: err = %v, want %q and no value", c.values, err, c.want)
		}
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("refused givings left %d answer notes", n)
	}
	if _, err := os.Lstat(filepath.Join(y.home, "sources")); !os.IsNotExist(err) {
		t.Fatalf("refused givings wrote a file: %v", err)
	}
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PAIR_TOKEN": value, "PAIR_UPDATE_TOKEN": "another"}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keyed.Given, ",") != "PAIR_TOKEN,PAIR_UPDATE_TOKEN" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if lines := strings.Join(y.resolved(t)[0].Lines(), "\n"); !strings.Contains(lines, "mckean gave PAIR_TOKEN and PAIR_UPDATE_TOKEN") {
		t.Fatalf("the card:\n%s", lines)
	}
}

// GiveSecret refuses an ask that is none, one that asks for no secret and
// one that was declined, and whoever may not set the source's credential:
// anyone but the operator, and a crew member whatever it calls itself.
func TestGiveSecretRefusals(t *testing.T) {
	y, file := secretYard(t)
	plain := y.ask(t, wordsAsk("mckean"), "yardmaster")
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	other, err := y.CreateBead(y.ctx, NewBead{Title: "another"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"CRM_TOKEN": "s3cret"}
	for _, c := range []struct {
		name, bead string
		ask        uint64
		actor      string
		want       string
	}{
		{"another bead", other.ID, seq, "mckean", "has no ask"},
		{"no ask", y.bead, 9999, "mckean", "has no ask"},
		{"an ask for words", y.bead, plain, "mckean", "asks for no secret"},
		{"not the operator", y.bead, seq, "ada", "is the operator's (mckean)"},
		{"a crew member", y.bead, seq, "yardmaster", "yardmaster is a crew member"},
	} {
		_, err := y.GiveSecret(y.ctx, c.bead, c.ask, values, c.actor)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote the file: %v", err)
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("refused givings left %d answer notes", n)
	}

	// Put aside, it still takes the secret; declined, it does not.
	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, values, "mckean"); err != nil {
		t.Fatalf("giving to an ask put aside: %v", err)
	}
	declined := y.ask(t, secretAsk("crm"), "yardmaster")
	y.answer(t, Answer{Ask: declined, Decline: true}, "mckean")
	if _, err := y.GiveSecret(y.ctx, y.bead, declined, values, "mckean"); err == nil || !strings.Contains(err.Error(), "was declined: mckean declined to give it") {
		t.Fatalf("giving to a declined ask: %v", err)
	}
	for _, r := range y.resolved(t) {
		if r.Note.Seq == declined && !strings.Contains(strings.Join(r.Lines(), "\n"), "  mckean declined to give it") {
			t.Fatalf("the declined card:\n%s", strings.Join(r.Lines(), "\n"))
		}
	}
}

// No other path takes what a person typed for a secret ask: a pick, words,
// and a decline with words are refused without saying back what was sent,
// and nothing is kept. Later is as on any ask. This is what holds a client
// that does not know the field and shows the ask as "words wanted".
func TestSecretAskTakesNoWords(t *testing.T) {
	y, _ := secretYard(t)
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	const typed = "s3cret-typed-as-words"
	takes := "ask " + itoa(seq) + " takes a secret, not words: yardr bead answer " + y.bead + " " + itoa(seq) + " --secret"
	for _, c := range []struct {
		name string
		do   func() error
		want string
	}{
		{"words", func() error { return y.AnswerAsk(y.ctx, y.bead, seq, 0, typed, "mckean") }, takes},
		{"a pick", func() error { return y.AnswerAsk(y.ctx, y.bead, seq, 1, "", "mckean") }, takes},
		{"a pick with words", func() error { return y.AnswerAsk(y.ctx, y.bead, seq, 1, typed, "mckean") }, takes},
		{"a decline with words", func() error { return y.DeclineAsk(y.ctx, y.bead, seq, typed, "mckean") }, "declining it takes no words"},
		{"a forged giving", func() error {
			return y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Given: []string{"CRM_TOKEN"}}, "mckean")
		}, "recorded by the yard alone"},
	} {
		err := c.do()
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), typed) {
			t.Errorf("%s: err = %v, want %q and nothing of what was sent", c.name, err, c.want)
		}
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("refused answers left %d notes", n)
	}
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := json.Marshal(evs); bytes.Contains(kept, []byte(typed)) {
		t.Fatal("an event holds what was typed")
	}
	if err := y.DeferAsk(y.ctx, y.bead, seq, "mckean"); err != nil {
		t.Fatalf("putting a secret ask aside: %v", err)
	}
	// Any other ask still needs words to be declined.
	plain := y.ask(t, wordsAsk("mckean"), "yardmaster")
	if err := y.DeclineAsk(y.ctx, y.bead, plain, " ", "mckean"); err == nil || !strings.Contains(err.Error(), "needs words") {
		t.Fatalf("declining an ask for words without any: %v", err)
	}
}

// When the transaction fails after the file's rename, the value is in the
// file and the store says nothing of it: the ask is open and has no answer
// note. The caller is told with the names and the file, never the value,
// and giving again answers the ask with one note.
func TestGiveSecretNotRecorded(t *testing.T) {
	y, file := secretYard(t)
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	full := errors.New("no space left on device")
	afterKeyRename = func() error { return full }
	t.Cleanup(func() { afterKeyRename = nil })

	_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "s3cret"}, "mckean")
	var half *KeyNotRecorded
	if !errors.As(err, &half) || !errors.Is(err, full) {
		t.Fatalf("err = %v, want a KeyNotRecorded around the store's error", err)
	}
	if want := "CRM_TOKEN is written to " + file + ", but the yard could not record it: no space left on device. Give it again."; err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q, want the value: the rename is not undone", b)
	}
	if r := y.resolved(t)[0]; r.State != AskOpen || len(r.Answers) != 0 {
		t.Fatalf("the ask is %s with %d answers, want open with none", r.State, len(r.Answers))
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes after a giving that was not recorded", n)
	}
	if src, err := y.GetSource(y.ctx, "crm"); err != nil || src.EnvFile != "" {
		t.Fatalf("the source = %+v, %v: want no env_file", src, err)
	}
	if q, err := y.UserAsks(y.ctx, "mckean"); err != nil || len(q.Open) != 1 || q.Open[0].Secret == nil {
		t.Fatalf("the operator's queue = %+v, %v: want the ask open still", q, err)
	}

	afterKeyRename = nil
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "s3cret"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.State != AskAnswered || len(r.Answers) != 1 {
		t.Fatalf("the ask is %s with %d answers, want answered with one", r.State, len(r.Answers))
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := sourceSetEvents(t, y.Store); n != 1 {
		t.Fatalf("%d source_set events, want 1", n)
	}
}

// The card says where the source's calls go, as they went when the ask was
// written. A source replaced since by one whose calls go elsewhere is not
// given the secret under the old card: the ask is to be asked again.
func TestGiveSecretRefusesASourceThatMoved(t *testing.T) {
	y, file := secretYard(t)
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	moved := strings.Replace(keyedFile, "https://crm.example.com/api", "https://elsewhere.example.net/api", 1)
	if _, err := y.PutSource(y.ctx, "crm.yaml", moved, "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "s3cret"}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "https://crm.example.com, and the source's calls go to https://elsewhere.example.net now") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("the refused giving wrote the file: %v", err)
	}
	// A path that changed under the same host is the same place.
	seq = y.ask(t, secretAsk("crm"), "yardmaster")
	if _, err := y.PutSource(y.ctx, "crm.yaml", strings.Replace(moved, "/api", "/api/v2", 1), "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "s3cret"}, "mckean"); err != nil {
		t.Fatal(err)
	}
}

// write's batch may run a function twice for one call: once in a
// transaction that another caller's function fails, and again by itself.
// The giving ends the same then as when it ran once: one answer note, the
// file with the line once, one source_set event.
func TestGiveSecretRunTwice(t *testing.T) {
	y, file := secretYard(t)
	seq := y.ask(t, secretAsk("crm"), "yardmaster")
	var k sourceKey
	var lg loginSent
	give := func(w *wtx) error {
		return w.giveSecret(y.ctx, y.Store, y.home, y.bead, seq, map[string]string{"CRM_TOKEN": "s3cret"}, "mckean", &k, &lg)
	}
	others := errors.New("another caller's function failed")
	err := y.db.Update(func(tx *bolt.Tx) error {
		if err := give(&wtx{Tx: tx, mentions: &y.mentions}); err != nil {
			return err
		}
		return others
	})
	if !errors.Is(err, others) {
		t.Fatal(err)
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes after the run that was rolled back", n)
	}
	if err := y.write(give); err != nil {
		t.Fatal(err)
	}
	if !k.renamed || k.file != file {
		t.Fatalf("the giving = %+v", k)
	}
	if n := len(answerNotes(t, y)); n != 1 {
		t.Fatalf("%d answer notes, want 1", n)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := sourceSetEvents(t, y.Store); n != 1 {
		t.Fatalf("%d source_set events, want 1", n)
	}
}
