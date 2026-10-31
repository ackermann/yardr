package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mintKeyFile = `name: gog
base: https://gog.example.com
credential: { env: GOG_TOKEN, mint: [GOG_ACCOUNT], header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// userSecretYard is secretYard with the source gog, whose credential is
// minted from GOG_ACCOUNT with GOG_TOKEN as the static fallback, and the
// source pushover, which tells people. It answers the file a user's own
// credential goes to when they have no env_file.
func userSecretYard(t *testing.T) (*askYard, string) {
	t.Helper()
	y, _ := secretYard(t)
	if _, err := y.PutSource(y.ctx, "gog.yaml", mintKeyFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "pushover.yaml", pushFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y, filepath.Join(y.home, "users", "ada.env")
}

func userSecretAsk(user, to string, env ...string) Ask {
	return Ask{Question: "Your account at gog", To: to,
		Context: []string{"It is the account address the mint reads."},
		Secret:  &AskSecret{Source: "gog", Env: env, As: user}}
}

const userSecretLine = "a secret: GOG_ACCOUNT, ada's own credential for the source gog (https://gog.example.com), read as ada when a call is made --as ada"

// An ask for a user's own credential in a source is stored with the names
// a call as them mints or uses — every name under mint where the
// credential is minted, else the static ones — whatever the asker sent,
// and its card says whose credential it is and that a call made as them
// reads it. It is asked of that user or of the operator for them.
func TestUserAskIsStoredWithTheMintOrStaticNames(t *testing.T) {
	t.Parallel()
	y, _ := userSecretYard(t)
	ask := userSecretAsk("ada", "ada")
	ask.Secret.Host = "https://evil.example.com"
	seq := y.ask(t, ask, "yardmaster")
	notes := mustNotes(t, y)
	var stored map[string]any
	if err := json.Unmarshal([]byte(notes[len(notes)-1].Text), &stored); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(stored["secret"])
	if want := `{"as":"ada","env":["GOG_ACCOUNT"],"host":"https://gog.example.com","source":"gog"}`; string(got) != want {
		t.Fatalf("the ask's secret = %s\nwant %s", got, want)
	}
	// Narrowed to the static fallback, which the default leaves out;
	// without --env on a static credential, every static one.
	narrowed := userSecretAsk("ada", "ada", "GOG_TOKEN")
	y.ask(t, narrowed, "yardmaster")
	both := userSecretAsk("ada", "ada")
	both.Secret.Source = "pair"
	y.ask(t, both, "yardmaster")
	rs := y.resolved(t)
	if got := rs[1].Ask.Secret.Names(); got != "GOG_TOKEN" {
		t.Fatalf("the narrowed ask asks for %q", got)
	}
	if got := rs[2].Ask.Secret.Names(); got != "PAIR_TOKEN and PAIR_UPDATE_TOKEN" {
		t.Fatalf("the static ask asks for %q", got)
	}

	lines := strings.Join(rs[0].Lines(), "\n")
	for _, want := range []string{
		"  " + userSecretLine,
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
	// In the user's own queue, as footplate reads it.
	q, err := y.UserAsks(y.ctx, "ada", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 3 || q.Open[0].Secret == nil || q.Open[0].Secret.As != "ada" || q.Open[0].Secret.Line != userSecretLine ||
		strings.Join(q.Open[0].Secret.Env, ",") != "GOG_ACCOUNT" {
		t.Fatalf("ada's queue = %+v", q)
	}
	// The operator may be asked for them.
	y.ask(t, userSecretAsk("ada", "mckean"), "yardmaster")
}

// It is refused, and nothing is stored: for a source with no credential,
// a user the yard does not have, a name the declaration does not have,
// anyone asked but that user and the operator, another yard, and beside
// a user, a crew member or a hook (a waiting login takes --as itself:
// loginwait_test.go).
func TestUserAskRefusals(t *testing.T) {
	t.Parallel()
	y, _ := userSecretYard(t)
	with := func(change func(*Ask)) Ask {
		a := userSecretAsk("ada", "ada")
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
		{"no such user", with(func(a *Ask) { a.Secret.As = "eve" }), "a user's credential is asked for a user of the yard (yardr user list): user eve: not found"},
		{"a name", with(func(a *Ask) { a.Secret.Env = []string{"GOG_PASSWORD"} }), "GOG_PASSWORD is no variable of its credential (its declaration names GOG_ACCOUNT, or GOG_TOKEN)"},
		{"not the user", with(func(a *Ask) { a.To = "bob" }), "ada's own credential in the source gog is theirs to give, or the operator's (mckean) for them: a call made as them reads it, so bob cannot be asked for it"},
		{"a crew member", with(func(a *Ask) { a.To = "yardmaster" }), "so yardmaster cannot be asked for it"},
		{"another yard", with(func(a *Ask) { a.To = "ada@north" }), "nothing of a secret crosses a link"},
		{"options", with(func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it is given"}} }), "takes no --option"},
		{"a user beside it", with(func(a *Ask) { a.Secret.User = "ada" }), "a secret ask names a user's address or a user's credential, not both"},
		{"a crew member beside it", with(func(a *Ask) { a.Secret.Crew = "yardmaster" }), "a secret ask names a crew member's credential or a user's, not both"},
		{"a hook beside it", with(func(a *Ask) { a.Secret.Hook = "bot" }), "it takes no --as"},
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

// GiveSecret writes a user's own credential into the user's file, sets it
// as their env_file where they had none, and answers the ask with a note
// that holds the names. The value is in the file and in nothing the store
// has. The user gives their own, or the operator does it for them.
func TestGiveSecretGivesAUsersOwnCredential(t *testing.T) {
	t.Parallel()
	y, file := userSecretYard(t)
	seq := y.ask(t, userSecretAsk("ada", "ada"), "yardmaster")
	const value = "ada@gog.example.com"
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"GOG_ACCOUNT": value}, "ada", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.As != "ada" || keyed.User != "" ||
		keyed.State != "GOG_ACCOUNT is set in "+file || strings.Join(keyed.Given, ",") != "GOG_ACCOUNT" ||
		keyed.Next != "yardr source check gog --as ada" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOG_ACCOUNT="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode = %v, %v", st.Mode(), err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil || u.EnvFile != file {
		t.Fatalf("ada = %+v, %v: want the env_file set", u, err)
	}
	if n := userSetEvents(t, y); n != 1 {
		t.Fatalf("%d user_set events, want 1: the env_file set as user set sets it", n)
	}
	// The source's own credential is another file, and untouched.
	if src, err := y.GetSource(y.ctx, "gog"); err != nil || src.EnvFile != "" {
		t.Fatalf("the source = %+v, %v", src, err)
	}

	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "ada" || answers[0].Text != `{"ask":`+itoa(seq)+`,"given":["GOG_ACCOUNT"]}` {
		t.Fatalf("the answer notes = %+v", answers)
	}
	if lines := strings.Join(y.resolved(t)[0].Lines(), "\n"); !strings.Contains(lines, "  ada gave GOG_ACCOUNT") || strings.Contains(lines, "answer with") {
		t.Fatalf("the card:\n%s", lines)
	}
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the asker heard %q", got)
	}
	if line, _ := e.Data["answer"].(string); line != "ada gave GOG_ACCOUNT for the source gog, as ada (ask "+itoa(seq)+" on "+y.bead+")" {
		t.Fatalf("the sentence = %q", line)
	}

	// The operator gives for them, and giving again replaces.
	keyed, err = y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"GOG_ACCOUNT": "typed-again"}, "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.As != "ada" || keyed.Next != "yardr source check gog --as ada" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOG_ACCOUNT=typed-again\n" {
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

// A user's own credential is given by that user or the operator for them,
// and by nobody else: another user and a crew member are refused, and so
// is a name the ask did not ask for. No refusal says back what was sent,
// and none writes the file, sets the env_file, or notes an answer.
func TestGiveSecretRefusesAnotherUsersCredential(t *testing.T) {
	t.Parallel()
	y, file := userSecretYard(t)
	seq := y.ask(t, userSecretAsk("ada", "ada"), "yardmaster")
	const value = "ada@gog.example.com"
	for actor, want := range map[string]string{
		"bob":        "where ada is told outside the yard is set by ada, or by the operator (mckean)",
		"yardmaster": "yardmaster is a crew member",
		"":           "this is nobody",
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"GOG_ACCOUNT": value}, actor, true)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), value) {
			t.Errorf("as %q: err = %v, want %q", actor, err, want)
		}
	}
	for _, values := range []map[string]string{
		{"GOG_TOKEN": value},
		{value: "GOG_ACCOUNT"},
		{"GOG_ACCOUNT": value, "MORE": "1"},
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
	// A user gone since the ask is refused naming that.
	if err := y.DeleteUser(y.ctx, "ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"GOG_ACCOUNT": value}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "user ada: not found") || strings.Contains(err.Error(), value) {
		t.Fatalf("after the user went: %v", err)
	}
}

// A --as ask answered with a URL-credential value that is no address is
// refused before the answer note, in words that name the fault and none of
// the value; an https address answers it. A refused value leaves no note
// and no file.
func TestGiveSecretRefusesAUsersCredentialThatIsNoAddress(t *testing.T) {
	t.Parallel()
	y, file := userSecretYard(t)
	if _, err := y.PutSource(y.ctx, "hook.yaml", urlKeyFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := userSecretAsk("ada", "ada")
	ask.Secret.Source = "hook"
	seq := y.ask(t, ask, "yardmaster")
	const secret = "abc123"
	for _, tc := range []struct{ value, want string }{
		{"https://hooks.example.com/catch/" + secret + "?key=" + secret, "has a query or a fragment"},
		{"http://hooks.example.com/catch/" + secret, "is plain http to a machine that is not this one"},
	} {
		_, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"HOOK_URL": tc.value}, "ada", true)
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
	keyed, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"HOOK_URL": "https://hooks.example.com/catch/" + secret}, "ada", true)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.As != "ada" || strings.Join(keyed.Given, ",") != "HOOK_URL" {
		t.Fatalf("keyed = %+v", keyed)
	}
}

// KeyUserCredential is the same giving typed: the user's own credential
// in a source that need tell nobody, into the user's file with their
// other lines kept. An address given beside it stays, and the file keeps
// its comments.
func TestKeyUserCredential(t *testing.T) {
	t.Parallel()
	y, file := userSecretYard(t)
	const address, account = "u9f2k4xq71ab03cd55ef8gh2ij6kl9", "ada@gog.example.com"
	if _, err := y.KeyUser(y.ctx, "ada", "pushover", map[string]string{"PUSHOVER_USER": address}, "ada"); err != nil {
		t.Fatal(err)
	}
	keyed, err := y.KeyUserCredential(y.ctx, "ada", "gog", map[string]string{"GOG_ACCOUNT": account}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.As != "ada" || keyed.User != "" ||
		keyed.State != "GOG_ACCOUNT is set in "+file || strings.Join(keyed.Given, ",") != "GOG_ACCOUNT" ||
		keyed.Next != "yardr source check gog --as ada" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER="+address+"\nGOG_ACCOUNT="+account+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode = %v, %v", st.Mode(), err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil || u.EnvFile != file {
		t.Fatalf("ada = %+v, %v", u, err)
	}

	// Refused: another user, a source with no credential, a name the
	// declaration does not have, and nothing given.
	for name, c := range map[string]struct {
		user, src string
		values    map[string]string
		actor     string
		want      string
	}{
		"another user":     {"ada", "gog", map[string]string{"GOG_ACCOUNT": account}, "bob", "where ada is told outside the yard is set by ada"},
		"no credential":    {"ada", "open", map[string]string{"GOG_ACCOUNT": account}, "ada", "source open: its declaration names no credential, so there is no credential of ada's to give"},
		"a name":           {"ada", "gog", map[string]string{"GOG_PASSWORD": account}, "ada", "GOG_PASSWORD is no variable of its credential (its declaration names GOG_ACCOUNT, or GOG_TOKEN)"},
		"nothing given":    {"ada", "gog", map[string]string{}, "ada", "source gog: no value was given (its declaration names GOG_ACCOUNT, or GOG_TOKEN)"},
		"no such user":     {"eve", "gog", map[string]string{"GOG_ACCOUNT": account}, "mckean", "user eve: not found"},
		"a user's address": {"ada", "gog", map[string]string{"PUSHOVER_USER": address}, "ada", "PUSHOVER_USER is no variable of its credential"},
	} {
		_, err := y.KeyUserCredential(y.ctx, c.user, c.src, c.values, c.actor)
		if err == nil || !strings.Contains(err.Error(), c.want) ||
			strings.Contains(err.Error(), account) || strings.Contains(err.Error(), address) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER="+address+"\nGOG_ACCOUNT="+account+"\n" {
		t.Fatalf("a refused giving changed the file: %q", b)
	}
}

// A user with an env_file already has the value written there, their
// other lines and comments kept.
func TestKeyUserCredentialKeepsAnEnvFile(t *testing.T) {
	t.Parallel()
	y, _ := userSecretYard(t)
	file := filepath.Join(t.TempDir(), "ada.env")
	if err := os.WriteFile(file, []byte("# mine\nOTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: file}, "ada"); err != nil {
		t.Fatal(err)
	}
	keyed, err := y.KeyUserCredential(y.ctx, "ada", "pair", map[string]string{"PAIR_TOKEN": "tok-0f-ada"}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.As != "ada" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "# mine\nOTHER=1\nPAIR_TOKEN=tok-0f-ada\n" {
		t.Fatalf("the file holds %q", b)
	}
}
