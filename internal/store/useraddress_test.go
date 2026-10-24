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

const pushFile = `name: pushover
base: https://api.pushover.net/1
credential: { env: PUSHOVER_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string, max: 1024 } }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PUSHOVER_USER }, text: text }
`

// addressYard is secretYard with the source pushover, which tells people,
// and the user bob besides mckean (the operator) and ada. It answers the
// file ada's own address goes to when she has no env_file.
func addressYard(t *testing.T) (*askYard, string) {
	t.Helper()
	y, _ := secretYard(t)
	if _, err := y.PutSource(y.ctx, "pushover.yaml", pushFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddUser(y.ctx, "bob", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y, filepath.Join(y.home, "users", "ada.env")
}

func addressAsk(user, to string) Ask {
	return Ask{Question: "Your Pushover user key", To: to,
		Context: []string{"It is on the first page of pushover.net after login."},
		Secret:  &AskSecret{Source: "pushover", User: user}}
}

// userSetEvents counts the user_set events of the store.
func userSetEvents(t *testing.T, y *askYard) int {
	t.Helper()
	evs, err := y.EventsSince(y.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "user_set" {
			n++
		}
	}
	return n
}

// An ask for a user's own address in a source is stored with the one
// variable the source's notify key names, whatever the asker sent, and its
// card says whose address it is and which command tells the user through
// the source. It is asked of that user or of the operator for them.
func TestAddressAskIsStoredWithTheNotifyKeysName(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := addressYard(t)
	ask := addressAsk("ada", "ada")
	ask.Secret.Host = "https://evil.example.com"
	seq := y.ask(t, ask, "yardmaster")
	notes := mustNotes(t, y)
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"env":["PUSHOVER_USER"],"host":"https://api.pushover.net","source":"pushover","user":"ada"}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}
	const line = "a secret: PUSHOVER_USER, ada's own address in the source pushover (https://api.pushover.net); ada is told through pushover once it is given and yardr user set ada notify=pushover is run"
	lines := strings.Join(y.resolved(t)[0].Lines(), "\n")
	for _, want := range []string{
		"  " + line,
		"  it goes to a file only the yard's server reads; no agent is shown it",
		"  answer with: yardr bead answer " + y.bead + " " + itoa(seq) + " --secret",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the card lacks %q:\n%s", want, lines)
		}
	}
	// In the user's own queue, as footplate reads it.
	q, err := y.UserAsks(y.ctx, "ada", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 || q.Open[0].Secret == nil || q.Open[0].Secret.User != "ada" || q.Open[0].Secret.Line != line ||
		strings.Join(q.Open[0].Secret.Env, ",") != "PUSHOVER_USER" {
		t.Fatalf("ada's queue = %+v", q)
	}
	// The operator may be asked for them.
	y.ask(t, addressAsk("ada", "mckean"), "yardmaster")
}

// It is refused, and nothing is stored: for a source with no notify key, a
// user the yard does not have, a name the notify key does not name, anyone
// asked but that user and the operator, and another yard.
func TestAddressAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := addressYard(t)
	with := func(change func(*Ask)) Ask {
		a := addressAsk("ada", "ada")
		change(&a)
		return a
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"no notify key", with(func(a *Ask) { a.Secret.Source = "crm" }), "source crm: its declaration has no notify key"},
		{"no such source", with(func(a *Ask) { a.Secret.Source = "nowhere" }), "source nowhere: not found"},
		{"no such user", with(func(a *Ask) { a.Secret.User = "eve" }), "user eve: not found"},
		{"the source's own key", with(func(a *Ask) { a.Secret.Env = []string{"PUSHOVER_TOKEN"} }), "PUSHOVER_TOKEN is not what it reads of a user's file (its notify key names PUSHOVER_USER)"},
		{"another user", with(func(a *Ask) { a.To = "bob" }), "ada's own address in the source pushover is theirs to give, or the operator's (mckean) for them"},
		{"a crew member", with(func(a *Ask) { a.To = "yardmaster" }), "so yardmaster cannot be asked for it"},
		{"another yard", with(func(a *Ask) { a.To = "ada@north" }), "nothing of a secret crosses a link"},
		{"options", with(func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
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

// GiveSecret writes a user's own address into the user's file, sets it as
// their env_file where they had none, and answers the ask with a note that
// holds the name. It does not say the user is told through the source: the
// answer names the command that does. The value is in the file and in
// nothing the store has.
func TestGiveSecretGivesAUsersOwnAddress(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := addressYard(t)
	seq := y.ask(t, addressAsk("ada", "ada"), "yardmaster")
	const value = "u-0f-ada-s3cret"
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PUSHOVER_USER": value}, "ada", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.User != "ada" || keyed.State != "PUSHOVER_USER is set in "+file || strings.Join(keyed.Given, ",") != "PUSHOVER_USER" ||
		keyed.Next != "yardr user set ada notify=pushover tells ada through it" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode = %v, %v", st.Mode(), err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil || u.EnvFile != file || u.Notify != "" {
		t.Fatalf("ada = %+v, %v: want the env_file set and no notify", u, err)
	}
	if n := userSetEvents(t, y); n != 1 {
		t.Fatalf("%d user_set events, want 1", n)
	}
	// The source's own credential is another file, and untouched.
	if src, err := y.GetSource(y.ctx, "pushover"); err != nil || src.EnvFile != "" {
		t.Fatalf("the source = %+v, %v", src, err)
	}

	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "ada" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["PUSHOVER_USER"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	if lines := strings.Join(y.resolved(t)[0].Lines(), "\n"); !strings.Contains(lines, "  ada gave PUSHOVER_USER") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the asker heard %q", got)
	}
	if line, _ := e.Data["answer"].(string); line != "ada gave PUSHOVER_USER, ada's own address in the source pushover (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}

	// Once the user is told through the source, the next line is the try;
	// the operator gives for them, and giving again replaces.
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserNotify: "pushover"}, "ada"); err != nil {
		t.Fatal(err)
	}
	keyed, err = y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PUSHOVER_USER": "typed-again"}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.Next != "yardr source check pushover --user ada sends one test message" {
		t.Fatalf("next = %q", keyed.Next)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER=typed-again\n" {
		t.Fatalf("the file holds %q after the second giving", b)
	}
	if n := userSetEvents(t, y); n != 2 {
		t.Fatalf("%d user_set events, want 2: the env_file once, and the notify", n)
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

// A user's own address is given by that user or by the operator, and by
// nobody else: another user and a crew member are refused, whoever was
// asked, and so is a name the ask did not ask for. No refusal says back
// what was sent, and none writes the file or a note.
func TestGiveSecretRefusesAnotherUsersAddress(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := addressYard(t)
	seq := y.ask(t, addressAsk("ada", "ada"), "yardmaster")
	const value = "u-0f-ada-s3cret"
	for actor, want := range map[string]string{
		"bob":        "where ada is told outside the yard is set by ada, or by the operator (mckean): what lands in their inbox goes there; this is bob",
		"yardmaster": "yardmaster is a crew member",
		"":           "this is nobody",
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PUSHOVER_USER": value}, actor, true)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), value) {
			t.Errorf("as %q: err = %v, want %q", actor, err, want)
		}
	}
	for _, values := range []map[string]string{
		{"PUSHOVER_TOKEN": value},
		{value: "PUSHOVER_USER"},
		{"PUSHOVER_USER": value, "MORE": "1"},
		{},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, values, "ada", true)
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
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || u.EnvFile != "" {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	// An ask for a source's credential is still the operator's alone,
	// though the one who gives is a user.
	crm := y.ask(t, secretAsk("crm"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, crm, map[string]string{"CRM_TOKEN": value}, "ada", true); err == nil || !strings.Contains(err.Error(), "setting a source's credential is the operator's (mckean)") {
		t.Fatalf("ada giving the source's credential: %v", err)
	}
}

// KeyUser is the giving nobody asked for: into the user's env_file where
// one is set, with its other lines kept and no write to the store, and
// refused for a source with no notify key, for any name but the one that
// key names, and for a user whose name would be a path of the yard's
// choosing.
func TestKeyUser(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := addressYard(t)
	own := filepath.Join(t.TempDir(), "bob.env")
	if err := os.WriteFile(own, []byte("# bob's\nOTHER=1\nPUSHOVER_USER=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "bob", map[string]string{UserEnvFile: own}, "bob"); err != nil {
		t.Fatal(err)
	}
	before := userSetEvents(t, y)
	keyed, err := y.KeyUser(y.ctx, "bob", "pushover", map[string]string{"PUSHOVER_USER": "u-0f-bob"}, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != own || keyed.User != "bob" || keyed.Next != "yardr user set bob notify=pushover tells bob through it" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(own); string(b) != "# bob's\nOTHER=1\nPUSHOVER_USER=u-0f-bob\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := userSetEvents(t, y); n != before {
		t.Fatalf("a giving into a file that was set wrote %d user_set events", n-before)
	}

	for _, c := range []struct {
		name, user, source string
		values             map[string]string
		actor, want        string
	}{
		{"no notify key", "ada", "crm", map[string]string{"CRM_TOKEN": "x-s3cret"}, "ada", "source crm: its declaration has no notify key"},
		{"no such source", "ada", "nowhere", map[string]string{"PUSHOVER_USER": "x-s3cret"}, "ada", "source nowhere: not found"},
		{"no such user", "eve", "pushover", map[string]string{"PUSHOVER_USER": "x-s3cret"}, "mckean", "user eve: not found"},
		{"the source's own key", "ada", "pushover", map[string]string{"PUSHOVER_TOKEN": "x-s3cret"}, "ada", "the source pushover reads PUSHOVER_USER of a user's file"},
		{"a value as a name", "ada", "pushover", map[string]string{"x-s3cret": "PUSHOVER_USER"}, "ada", "that name is given, and no other"},
		{"nothing", "ada", "pushover", nil, "ada", "that name is given, and no other"},
		{"an empty value", "ada", "pushover", map[string]string{"PUSHOVER_USER": ""}, "ada", "the value for PUSHOVER_USER is empty"},
		{"another user", "ada", "pushover", map[string]string{"PUSHOVER_USER": "x-s3cret"}, "bob", "this is bob"},
	} {
		_, err := y.KeyUser(y.ctx, c.user, c.source, c.values, c.actor)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "x-s3cret") {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || u.EnvFile != "" {
		t.Fatalf("ada = %+v, %v: a refused giving set a file", u, err)
	}

	// A user's name may hold a "/" and dots: the yard chooses no file for
	// one, since it could be another user's or outside the yard's home.
	if _, err := y.AddUser(y.ctx, "team/../ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err = y.KeyUser(y.ctx, "team/../ada", "pushover", map[string]string{"PUSHOVER_USER": "x-s3cret"}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "the yard chooses no file for a name with a path separator in it: yardr user set team/../ada env_file=<file> first") {
		t.Fatalf("a name with a /: %v", err)
	}
	_, err = userEnvFile(y.home, `team\..\ada`)
	if err == nil || !strings.Contains(err.Error(), "the yard chooses no file for a name with a path separator in it") {
		t.Fatalf("a name with a backslash: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(y.home, "users")); !os.IsNotExist(err) {
		t.Fatalf("the refused giving made %s: %v", filepath.Join(y.home, "users"), err)
	}
}

// When the transaction fails after the file's rename, the value is in the
// user's file and the store says nothing of it: no env_file, no event, the
// ask open with no answer. Giving again ends as one giving that worked.
func TestGiveAddressNotRecorded(t *testing.T) {
	// Sequential: overrides afterKeyRename, and uses the fake clock (via newAskYard).
	y, file := addressYard(t)
	seq := y.ask(t, addressAsk("ada", "ada"), "yardmaster")
	full := errors.New("no space left on device")
	afterKeyRename = func() error { return full }
	t.Cleanup(func() { afterKeyRename = nil })

	_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PUSHOVER_USER": "s3cret"}, "ada", true)
	var half *KeyNotRecorded
	if !errors.As(err, &half) || !errors.Is(err, full) {
		t.Fatalf("err = %v, want a KeyNotRecorded around the store's error", err)
	}
	if want := "PUSHOVER_USER is written to " + file + ", but the yard could not record it: no space left on device. Give it again."; err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER=s3cret\n" {
		t.Fatalf("the file holds %q, want the value: the rename is not undone", b)
	}
	if r := y.resolved(t)[0]; r.State != AskOpen || len(r.Answers) != 0 {
		t.Fatalf("the ask is %s with %d answers, want open with none", r.State, len(r.Answers))
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || u.EnvFile != "" {
		t.Fatalf("ada = %+v, %v: want no env_file", u, err)
	}
	if n := userSetEvents(t, y); n != 0 {
		t.Fatalf("%d user_set events after a giving that was not recorded", n)
	}

	afterKeyRename = nil
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"PUSHOVER_USER": "s3cret"}, "ada", true); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.State != AskAnswered || len(r.Answers) != 1 {
		t.Fatalf("the ask is %s with %d answers, want answered with one", r.State, len(r.Answers))
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := userSetEvents(t, y); n != 1 {
		t.Fatalf("%d user_set events, want 1", n)
	}
}

// write's batch may run a function twice for one call. The giving ends the
// same then as when it ran once: one answer note, the file with the line
// once, one user_set event.
func TestGiveAddressRunTwice(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := addressYard(t)
	seq := y.ask(t, addressAsk("ada", "ada"), "yardmaster")
	var k sourceKey
	var lg loginSent
	give := func(w *wtx) error {
		return w.giveSecret(y.ctx, y.Store, y.home, y.bead, seq, map[string]string{"PUSHOVER_USER": "s3cret"}, "ada", true, &k, &lg)
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
	if err := y.write(give); err != nil {
		t.Fatal(err)
	}
	if !k.renamed || k.file != file {
		t.Fatalf("the giving = %+v", k)
	}
	if n := len(answerNotes(t, y)); n != 1 {
		t.Fatalf("%d answer notes, want 1", n)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := userSetEvents(t, y); n != 1 {
		t.Fatalf("%d user_set events, want 1", n)
	}
}
