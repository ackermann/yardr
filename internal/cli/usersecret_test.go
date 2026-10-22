package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

const googleMailDecl = `name: google-mail
base: https://gmail.googleapis.com
credential: { env: GOOGLE_TOKEN, mint: [GOOGLE_ACCOUNT], header: "Authorization: Bearer {credential}" }
calls:
  search: { kind: read, request: { method: GET, path: /gmail/v1/users/me/messages } }
`

// userSecretYard is secretYard with the users ada and bob, the source
// open, which names no credential, and the source google-mail, whose
// credential is minted from GOOGLE_ACCOUNT. It answers the file ada's
// own credential goes to when she has no env_file.
func userSecretYard(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	st, id, crmFile := secretYard(t)
	ctx := context.Background()
	for _, u := range []string{"ada", "bob"} {
		if _, err := st.AddUser(ctx, u, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	const openDecl = "name: open\nbase: https://open.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	if _, err := st.PutSource(ctx, "open.yaml", openDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "google-mail.yaml", googleMailDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(crmFile))
	return st, id, filepath.Join(home, "users", "ada.env")
}

// bead ask --secret <source> --as <user> asks that user for their own
// credential in the source, and bead answer --secret gives it into the
// user's file and names the check that tries it as them. Without --env a
// minted credential asks for its mint's inputs alone.
func TestBeadAskAndAnswerAUsersCredential(t *testing.T) {
	t.Parallel()
	st, id, file := userSecretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	ada := &app{st: st, actor: "ada"}
	bob := &app{st: st, actor: "bob"}
	const value = "ada@gmail.example.com"

	ask := []string{"ask", id, "Your Google account for your mail?", "--context", "The yard reads your mail as you when it is granted it as you."}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"as alone":         {[]string{"--as", "ada"}, "it goes with --secret <source>"},
		"a user beside it": {[]string{"--secret", "crm", "--as", "ada", "--for-user", "ada"}, "one of the two"},
		"a crew beside it": {[]string{"--secret", "crm", "--as", "ada", "--for-crew", "yardmaster"}, "one of the two"},
		"no credential":    {[]string{"--secret", "open", "--as", "ada"}, "its declaration names no credential"},
		"no such source":   {[]string{"--secret", "nowhere", "--as", "ada"}, "source nowhere: not found"},
		"no such user":     {[]string{"--secret", "crm", "--as", "eve"}, "a user's credential is asked for a user of the yard"},
		"a name":           {[]string{"--secret", "crm", "--as", "ada", "--env", "GITHUB_PASSWORD"}, "is no variable of its credential"},
		"not the user":     {[]string{"--secret", "crm", "--as", "ada", "--to", "bob"}, "so bob cannot be asked for it"},
		"another yard":     {[]string{"--secret", "crm", "--as", "ada", "--to", "ada@north"}, "nothing of a secret crosses a link"},
		"with an option":   {[]string{"--secret", "crm", "--as", "ada", "--option", "Yes: it is given"}, "takes no --option"},
		"about other work": {[]string{"--secret", "crm", "--as", "ada", "--about", id}, "takes no --about"},
		"a login beside":   {[]string{"--secret-login", "claude-x", "--for-user", "ada"}, "takes no --for-user"},
		"a hook beside":    {[]string{"--secret-hook", "h", "--source", "crm", "--as", "ada"}, "it goes with --secret <source>"},
	} {
		if _, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}

	out, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "crm", "--as", "ada", "--env", "CRM_TOKEN")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for ada: a secret, CRM_TOKEN of the source crm, as ada\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	// Without --env, every static variable of the credential; asked of
	// the user, where --to defaults.
	out, err = run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "crm", "--as", "ada")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 2 on " + id + " for ada: a secret, CRM_TOKEN and CRM_UPDATE_TOKEN of the source crm, as ada\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	// A minted credential asks for its mint's inputs alone.
	out, err = run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "google-mail", "--as", "ada")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 3 on " + id + " for ada: a secret, GOOGLE_ACCOUNT of the source google-mail, as ada\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	jsonOut, err := run(t, (&app{st: st, actor: "yardmaster", json: true}).beadCmd(), append(append([]string{}, ask...), "--secret", "google-mail", "--as", "ada")...)
	var asked struct {
		Ask    uint64
		Secret store.AskSecret
	}
	if err != nil || json.Unmarshal([]byte(jsonOut), &asked) != nil || asked.Secret.As != "ada" || asked.Secret.User != "" {
		t.Fatalf("ask --json = %q, %v", jsonOut, err)
	}

	out, err = run(t, ada.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a secret: GOOGLE_ACCOUNT, ada's own credential for the source google-mail (https://gmail.googleapis.com), read as ada when a call is made --as ada",
		"it goes to a file only the yard's server reads; no agent is shown it",
		"answer with: yardr bead answer " + id + " 3 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}

	if _, err := answerFrom(t, bob, "GOOGLE_ACCOUNT="+value+"\n", id, "3", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "where ada is told outside the yard is set by ada, or by the operator (mckean)") ||
		strings.Contains(err.Error(), value) {
		t.Fatalf("bob answering the ask: %v", err)
	}
	if _, err := answerFrom(t, ada, value, id, "3", "--words", value); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("the credential as words: %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused answer wrote the file: %v", err)
	}
	out, err = answerFrom(t, ada, "GOOGLE_ACCOUNT="+value+"\n", id, "3", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ada gave GOOGLE_ACCOUNT · it is set in " + file + "\nnext: yardr source check google-mail --as ada\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOOGLE_ACCOUNT="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode: %v, %v", fi.Mode(), err)
	}
	if u, err := st.GetUser(context.Background(), "ada"); err != nil || u.EnvFile != file {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	out, err = run(t, ada.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 3 · answered") || !strings.Contains(out, "ada gave GOOGLE_ACCOUNT") || strings.Contains(out, value) {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}
	// The operator gives for them.
	if _, err := answerFrom(t, me, "GOOGLE_ACCOUNT=typed-again\n", id, "3", "--secret"); err != nil {
		t.Fatalf("the operator answering for ada: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOOGLE_ACCOUNT=typed-again\n" {
		t.Fatalf("the file holds %q after the second giving", b)
	}
	// A login that is theirs own is asked of them as well, for the code its
	// sign-in page shows.
	out, err = run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret-login", "claude-x", "--as", "ada")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := " on " + id + " for ada: a secret, INPUT for ada's waiting login claude-x\n"; !strings.HasPrefix(out, "ask ") || !strings.HasSuffix(out, want) {
		t.Fatalf("ask printed %q\nwant it to end %q", out, want)
	}
}

// yardr source key <source> --as <user> is the same giving typed: the
// user's own credential in the source, into the user's file with their
// other lines kept. The user gives their own, or the operator does it for
// them; a user the yard has not is refused before anything is typed.
func TestSourceKeyAs(t *testing.T) {
	t.Parallel()
	st, _, file := userSecretYard(t)
	me := &app{st: st, actor: "mckean"}
	ada := &app{st: st, actor: "ada"}
	bob := &app{st: st, actor: "bob"}
	const account, token = "ada@gmail.example.com", "tok-0f-ada-s3cret"

	if _, err := keyFrom(t, me, "CRM_TOKEN="+token+"\n", "crm", "--as", "eve"); err == nil || !strings.Contains(err.Error(), "user eve: not found") {
		t.Fatalf("a user the yard has not: %v", err)
	}
	if _, err := keyFrom(t, me, "CRM_TOKEN="+token+"\n", "open", "--as", "ada"); err == nil || !strings.Contains(err.Error(), "its declaration names no credential") {
		t.Fatalf("a source with no credential: %v", err)
	}
	if _, err := keyFrom(t, me, "CRM_TOKEN="+token+"\n", "crm", "PATH", "--as", "ada"); err == nil || !strings.Contains(err.Error(), "PATH is no variable of its credential") {
		t.Fatalf("a name the declaration does not have: %v", err)
	}
	if _, err := keyFrom(t, bob, "GOOGLE_ACCOUNT="+account+"\n", "google-mail", "--as", "ada"); err == nil ||
		!strings.Contains(err.Error(), "where ada is told outside the yard is set by ada, or by the operator (mckean)") ||
		strings.Contains(err.Error(), account) {
		t.Fatalf("bob giving ada's credential: %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote the file: %v", err)
	}

	out, err := keyFrom(t, me, "GOOGLE_ACCOUNT="+account+"\n", "google-mail", "--as", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ada gave GOOGLE_ACCOUNT · it is set in " + file + " · yardr source check google-mail --as ada\n"; out != want {
		t.Fatalf("key printed %q\nwant        %q", out, want)
	}
	// Narrowed to one name, with ada's other lines kept.
	out, err = keyFrom(t, ada, "CRM_TOKEN="+token+"\n", "crm", "CRM_TOKEN", "--as", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ada gave CRM_TOKEN · it is set in " + file + " · yardr source check crm --as ada\n"; out != want {
		t.Fatalf("key printed %q\nwant        %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOOGLE_ACCOUNT="+account+"\nCRM_TOKEN="+token+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode: %v, %v", fi.Mode(), err)
	}
	if u, err := st.GetUser(context.Background(), "ada"); err != nil || u.EnvFile != file {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	var printed []string
	for _, cmd := range [][]string{{"show", "google-mail"}, {"show", "crm"}} {
		out, err := run(t, me.sourceCmd(), cmd...)
		if err != nil {
			t.Fatal(err)
		}
		printed = append(printed, out)
	}
	out, err = run(t, me.userCmd(), "show", "ada")
	if err != nil {
		t.Fatal(err)
	}
	printed = append(printed, out)
	for _, p := range printed {
		if strings.Contains(p, account) || strings.Contains(p, token) {
			t.Fatalf("a showing holds a value:\n%s", p)
		}
	}
}

// yardr source show says for each user whether their file sets the names
// a call as them reads, and with which card a missing one is given;
// yardr user show names the sources that hold a credential of the user.
// Neither prints a value.
func TestSourceShowAndUserShowSayAUsersCredential(t *testing.T) {
	t.Parallel()
	st, _, file := userSecretYard(t)
	me := &app{st: st, actor: "mckean"}
	ada := &app{st: st, actor: "ada"}
	const account = "ada@gmail.example.com"

	card := func(user string) string {
		return `yardr bead ask <bead> "<question>" --secret google-mail --as ` + user + ` --context "<where to get it>" asks ` + user + ` for it`
	}
	out, err := run(t, me.sourceCmd(), "show", "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  credential as ada: GOOGLE_ACCOUNT is not set for ada: " + card("ada") + "\n",
		"  credential as bob: GOOGLE_ACCOUNT is not set for bob: " + card("bob") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	out, err = run(t, me.userCmd(), "show", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "credentials:") {
		t.Fatalf("show names a credential before any is given:\n%s", out)
	}

	// A file of hers without the names is said so with the card that
	// gives them; once given, both showings name them.
	bobFile := filepath.Join(filepath.Dir(filepath.Dir(file)), "users", "bob.env")
	if err := os.MkdirAll(filepath.Dir(bobFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bobFile, []byte("OTHER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, ada.userCmd(), "set", "bob", "env_file="+bobFile); err == nil {
		t.Fatalf("ada set where bob is told")
	}
	bob := &app{st: st, actor: "bob"}
	if _, err := run(t, bob.userCmd(), "set", "bob", "env_file="+bobFile); err != nil {
		t.Fatal(err)
	}
	if _, err := keyFrom(t, ada, "GOOGLE_ACCOUNT="+account+"\n", "google-mail", "--as", "ada"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, me.sourceCmd(), "show", "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  credential as ada: GOOGLE_ACCOUNT is set in " + file + "\n",
		"  credential as bob: env_file " + bobFile + " does not set GOOGLE_ACCOUNT, or GOOGLE_TOKEN: " + card("bob") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, account) {
		t.Fatalf("show holds the value:\n%s", out)
	}
	out, err = run(t, me.userCmd(), "show", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  credentials: google-mail\n") || strings.Contains(out, account) {
		t.Fatalf("user show:\n%s", out)
	}
	out, err = run(t, me.userCmd(), "show", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "credentials:") {
		t.Fatalf("bob's show names a credential:\n%s", out)
	}
	// And under --json, by the same names.
	jsonOut, err := run(t, (&app{st: st, actor: "mckean", json: true}).userCmd(), "show", "ada")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Credentials []string
	}
	if json.Unmarshal([]byte(jsonOut), &v) != nil || len(v.Credentials) != 1 || v.Credentials[0] != "google-mail" {
		t.Fatalf("user show --json = %q", jsonOut)
	}
}
