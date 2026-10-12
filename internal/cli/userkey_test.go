package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// addressYard is a yard whose operator is jo, with the users ada and bob,
// the source pushover, whose calls go to base and which tells people, the
// source crm, which does not, and one bead. It answers the yard's home.
func addressYard(t *testing.T, base string) (store.API, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if _, err := st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	text, err := os.ReadFile("testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", strings.Replace(string(text), "https://api.pushover.net/1", base, 1), "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "crm.yaml", keyDecl, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, b.ID, filepath.Dir(path)
}

// userKeyFrom runs yardr user key with stdin as its standard input.
func userKeyFrom(t *testing.T, a *app, stdin string, args ...string) (string, error) {
	t.Helper()
	c := a.userCmd()
	c.SetIn(strings.NewReader(stdin))
	return run(t, c, append([]string{"key"}, args...)...)
}

// Both keys of a source that tells people are given without a file written
// by hand, as the packs' SETUP pages say it: yardr source key for the
// yard's, yardr user key for the person's own, then notify and one test
// message, which the stand-in gets with both. Neither value is printed.
func TestUserKeyThenNotifyThenCheck(t *testing.T) {
	const token, address = "tok-7d10-not-to-be-seen", "u9f2k4xq71ab03cd55ef8gh2ij6kl9"
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		got = append(got, body)
		io.WriteString(w, `{"status":1}`)
	}))
	defer srv.Close()
	st, _, home := addressYard(t, srv.URL)
	jo, ada, bob := &app{st: st, actor: "jo"}, &app{st: st, actor: "ada"}, &app{st: st, actor: "bob"}
	file := filepath.Join(home, "users", "ada.env")

	var printed []string
	out, err := keyFrom(t, jo, "PUSHOVER_TOKEN="+token+"\n", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	printed = append(printed, out)

	// Not another user's to give, and not for a source that tells nobody.
	if _, err := userKeyFrom(t, bob, "PUSHOVER_USER="+address+"\n", "ada", "pushover"); err == nil ||
		!strings.Contains(err.Error(), "where ada is told outside the yard is set by ada, or by the operator (jo)") || strings.Contains(err.Error(), address) {
		t.Fatalf("bob giving ada's address: %v", err)
	}
	if _, err := userKeyFrom(t, ada, "CRM_TOKEN="+address+"\n", "ada", "crm"); err == nil || !strings.Contains(err.Error(), "source crm: its declaration has no notify key") {
		t.Fatalf("a source with no notify key: %v", err)
	}
	if _, err := userKeyFrom(t, ada, "PUSHOVER_TOKEN="+address+"\n", "ada", "pushover"); err == nil || strings.Contains(err.Error(), address) {
		t.Fatalf("the source's own key as ada's address: %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote %s: %v", file, err)
	}

	out, err = userKeyFrom(t, ada, "PUSHOVER_USER="+address+"\n", "ada", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	if want := "user ada: PUSHOVER_USER is set in " + file + "\nnext: yardr user set ada notify=pushover tells ada through it\n"; out != want {
		t.Fatalf("out = %q\nwant  %q", out, want)
	}
	printed = append(printed, out)
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER="+address+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode: %v, %v", fi.Mode(), err)
	}

	out, err = run(t, ada.userCmd(), "set", "ada", "notify=pushover")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user ada: told outside the yard: through pushover (PUSHOVER_USER is set in "+file+")") {
		t.Fatalf("set printed %q", out)
	}
	printed = append(printed, out)
	out, err = run(t, ada.sourceCmd(), "check", "pushover", "--user", "ada")
	if err != nil {
		t.Fatal(err)
	}
	printed = append(printed, out)
	if len(got) != 1 || got[0]["user"] != address || got[0]["token"] != token {
		t.Fatalf("the service got %v", got)
	}
	// Given again once the user is told through it: the next line is the try.
	out, err = userKeyFrom(t, jo, "PUSHOVER_USER="+address+"\n", "ada", "pushover")
	if err != nil || !strings.Contains(out, "next: yardr source check pushover --user ada sends one test message\n") {
		t.Fatalf("given again: %q, %v", out, err)
	}
	printed = append(printed, out)
	for _, v := range []string{token, address} {
		if strings.Contains(strings.Join(printed, ""), v) {
			t.Errorf("a command printed a value:\n%s", strings.Join(printed, ""))
		}
	}
}

// bead ask --secret <source> --for-user <user> asks that user for their
// own address in the source, and bead answer --secret gives it into the
// user's file and names the command that tells them through the source.
func TestBeadAskAndAnswerAUsersAddress(t *testing.T) {
	st, id, home := addressYard(t, "https://api.pushover.net/1")
	yard, ada, bob := &app{st: st, actor: "yardmaster"}, &app{st: st, actor: "ada"}, &app{st: st, actor: "bob"}
	file := filepath.Join(home, "users", "ada.env")
	const address = "u9f2k4xq71ab03cd55ef8gh2ij6kl9"

	ask := []string{"ask", id, "Your Pushover user key", "--context", "It is on the first page of pushover.net after login."}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"for-user alone":   {[]string{"--for-user", "ada"}, "it goes with --secret <source>"},
		"an env":           {[]string{"--secret", "pushover", "--for-user", "ada", "--env", "PUSHOVER_USER"}, "--for-user takes no --env"},
		"no notify key":    {[]string{"--secret", "crm", "--for-user", "ada"}, "source crm: its declaration has no notify key"},
		"no such user":     {[]string{"--secret", "pushover", "--for-user", "eve"}, "user eve: not found"},
		"another user":     {[]string{"--secret", "pushover", "--for-user", "ada", "--to", "bob"}, "so bob cannot be asked for it"},
		"another yard":     {[]string{"--secret", "pushover", "--for-user", "ada", "--to", "ada@north"}, "nothing of a secret crosses a link"},
		"with an option":   {[]string{"--secret", "pushover", "--for-user", "ada", "--option", "Yes: it is given"}, "takes no --option"},
		"about other work": {[]string{"--secret", "pushover", "--for-user", "ada", "--about", id}, "takes no --about"},
	} {
		if _, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}

	// --to defaults to the user whose address it is.
	out, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "pushover", "--for-user", "ada")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for ada: a secret, PUSHOVER_USER, ada's own address in the source pushover\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	out, err = run(t, yard.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a secret: PUSHOVER_USER, ada's own address in the source pushover (https://api.pushover.net); ada is told through pushover once it is given and yardr user set ada notify=pushover is run",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime lacks %q:\n%s", want, out)
		}
	}

	if _, err := answerFrom(t, bob, "PUSHOVER_USER="+address+"\n", id, "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "this is bob") || strings.Contains(err.Error(), address) {
		t.Fatalf("bob answering ada's ask: %v", err)
	}
	if _, err := answerFrom(t, ada, address, id, "1", "--words", address); err == nil || strings.Contains(err.Error(), address) {
		t.Fatalf("the address as words: %v", err)
	}
	out, err = answerFrom(t, ada, "PUSHOVER_USER="+address+"\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ada gave PUSHOVER_USER · it is set in " + file + "\nnext: yardr user set ada notify=pushover tells ada through it\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSHOVER_USER="+address+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if u, err := st.GetUser(context.Background(), "ada"); err != nil || u.EnvFile != file || u.Notify != "" {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	out, err = run(t, yard.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ada gave PUSHOVER_USER") || strings.Contains(out, address) {
		t.Fatalf("prime after the giving (%v):\n%s", err, out)
	}
}
