package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// secretYard is a yard whose operator is mckean, with the source crm (two
// variables), one bead, and the file crm's credential goes to.
func secretYard(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "operator", "mckean", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "crm.yaml", keyDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, b.ID, filepath.Join(filepath.Dir(path), "sources", "crm.env")
}

// answerFrom runs yardr bead answer with stdin as its standard input.
func answerFrom(t *testing.T, a *app, stdin string, args ...string) (string, error) {
	t.Helper()
	c := a.beadCmd()
	c.SetIn(strings.NewReader(stdin))
	return run(t, c, append([]string{"answer"}, args...)...)
}

// dyingClient serves st on a unix socket and returns a client of it whose
// one call dies: the server closes the connection in the middle of method,
// as a server that died during the call, and serves every other call.
func dyingClient(t *testing.T, st *store.Store, method string) store.API {
	t.Helper()
	dir, err := os.MkdirTemp("", "ydying")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	real := rpc.Handler(st, nil)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/call/"+method {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		real.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return rpc.NewClient(sock)
}

// bead ask --secret stores the ask with where its answer goes and prints
// the line of the design's section 3.2; --env narrows it; prime shows the
// yard's line and how to answer. bead answer --secret reads NAME=value
// lines from a pipe, as source key does, and prints who gave which names
// and the file, never a value.
func TestBeadAskAndAnswerASecret(t *testing.T) {
	t.Parallel()
	st, id, file := secretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}

	out, err := run(t, yard.beadCmd(), "ask", id, "The API key for the CRM", "--secret", "crm", "--env", "CRM_TOKEN",
		"--context", "Create one at https://crm.example.com/account/api (read only).")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for mckean: a secret, CRM_TOKEN of the source crm\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	out, err = run(t, yard.beadCmd(), "ask", "--new", "Both tokens of the CRM", "--secret", "crm", "--context", "As above.")
	if err != nil || !strings.HasSuffix(out, " created; ask 2 on it for mckean: a secret, CRM_TOKEN and CRM_UPDATE_TOKEN of the source crm\n") {
		t.Fatalf("ask --new printed %q, %v", out, err)
	}
	jsonOut, err := run(t, (&app{st: st, actor: "yardmaster", json: true}).beadCmd(), "ask", id, "Again", "--secret", "crm", "--env", "CRM_UPDATE_TOKEN", "--context", "As above.")
	var asked struct {
		Ask    uint64
		Secret store.AskSecret
	}
	if err != nil || json.Unmarshal([]byte(jsonOut), &asked) != nil || asked.Secret.Source != "crm" || strings.Join(asked.Secret.Env, ",") != "CRM_UPDATE_TOKEN" {
		t.Fatalf("ask --json = %q, %v", jsonOut, err)
	}

	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ask 1 · open", "The API key for the CRM",
		"a secret: CRM_TOKEN, for the source crm (https://crm.example.com)",
		"it goes to a file only the yard's server reads; no agent is shown it",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"secret"`) || strings.Contains(out, "words wanted") {
		t.Errorf("prime:\n%s", out)
	}

	for name, c := range map[string]struct {
		a     *app
		stdin string
		args  []string
		want  string
	}{
		"a pick":            {me, "", []string{id, "1", "1"}, "takes a secret, not words"},
		"words":             {me, "", []string{id, "1", "--words", "s3cret-as-words"}, "takes a secret, not words"},
		"decline, words":    {me, "", []string{id, "1", "--decline", "s3cret-as-why"}, "declining it takes no words"},
		"secret and a pick": {me, "CRM_TOKEN=s3cret\n", []string{id, "1", "1", "--secret"}, "takes nothing else"},
		"another name":      {me, "CRM_UPDATE_TOKEN=s3cret\n", []string{id, "1", "--secret"}, "name that was not asked for"},
		"secret in name":    {me, "s3cret_as_name=ignored\n", []string{id, "1", "--secret"}, "name that was not asked for"},
		"no line":           {me, "# nothing\n", []string{id, "1", "--secret"}, "has no line CRM_TOKEN=<value>"},
		"an agent":          {yard, "CRM_TOKEN=s3cret\n", []string{id, "1", "--secret"}, "yardmaster is a crew member"},
		"no secret ask":     {me, "CRM_TOKEN=s3cret\n", []string{id, "9", "--secret"}, "has no ask 9"},
	} {
		if name == "an agent" {
			if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "mckean"); err != nil {
				t.Fatal(err)
			}
		}
		out, err := answerFrom(t, c.a, c.stdin, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(out+err.Error(), "s3cret") {
			t.Errorf("%s: %q, err = %v, want %q and no value", name, out, err, c.want)
		}
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused answer wrote the file: %v", err)
	}

	out, err = answerFrom(t, me, "CRM_TOKEN=s3cret-read\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mckean gave CRM_TOKEN · it is set in " + file + " · env_file " + file + " does not set CRM_UPDATE_TOKEN\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret-read\n" {
		t.Fatalf("the file holds %q", b)
	}
	out, err = answerFrom(t, me, "CRM_UPDATE_TOKEN=s3cret-write\n", id, "3", "--secret")
	if want := "mckean gave CRM_UPDATE_TOKEN · it is set in " + file + "\n"; err != nil || out != want {
		t.Fatalf("answer printed %q, %v\nwant           %q", out, err, want)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean gave CRM_TOKEN") || strings.Contains(out, "s3cret") {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}

	// Declined without words, which a secret ask alone is.
	if _, err := run(t, yard.beadCmd(), "ask", id, "Once more", "--secret", "crm", "--env", "CRM_TOKEN", "--context", "As above."); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, me.beadCmd(), "answer", id, "6", "--decline", "")
	if err != nil || !strings.Contains(out, "mckean declined to give it") {
		t.Fatalf("decline printed %q, %v", out, err)
	}
}

// bead ask --secret is refused by the yard in each case it lists, and
// --env goes with --secret alone.
func TestBeadAskSecretRefusals(t *testing.T) {
	t.Parallel()
	st, id, _ := secretYard(t)
	a := &app{st: st, actor: "yardmaster"}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no such source": {[]string{"--secret", "nowhere"}, "source nowhere: not found"},
		"a name":         {[]string{"--secret", "crm", "--env", "GITHUB_PASSWORD"}, "is no variable of its credential"},
		"not operator":   {[]string{"--secret", "crm", "--to", "ada"}, "the operator's to give (mckean)"},
		"a peer":         {[]string{"--secret", "crm", "--to", "mckean@north"}, "nothing of a secret crosses a link"},
		"an option":      {[]string{"--secret", "crm", "--option", "Yes: it is given"}, "takes no --option"},
		"about":          {[]string{"--secret", "crm", "--about", id}, "takes no --about"},
		"env alone":      {[]string{"--env", "CRM_TOKEN"}, "goes with --secret"},
	} {
		args := append([]string{"ask", id, "The API key for the CRM", "--context", "where to get it."}, c.args...)
		if _, err := run(t, a.beadCmd(), args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}
}

// bead ask --secret on a source whose credential is minted asks for the
// mint's inputs alone without --env: the token is minted per call, never
// given. An answer of that name is taken, and the exact-names refusal
// still holds for what is asked: the token is no name of the ask.
func TestBeadAskSecretMintedSource(t *testing.T) {
	t.Parallel()
	st, id, adaFile := userSecretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	file := filepath.Join(filepath.Dir(filepath.Dir(adaFile)), "sources", "google-mail.env")

	out, err := run(t, yard.beadCmd(), "ask", id, "The yard's Google account for Gmail", "--secret", "google-mail",
		"--context", "The address step 1 showed: the address alone, no token.")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for mckean: a secret, GOOGLE_ACCOUNT of the source google-mail\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "a secret: GOOGLE_ACCOUNT, for the source google-mail (https://gmail.googleapis.com)") {
		t.Fatalf("prime lacks the card's line, %v:\n%s", err, out)
	}
	if out, err := answerFrom(t, me, "GOOGLE_TOKEN=s3cret-token\n", id, "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "name that was not asked for") || strings.Contains(out+err.Error(), "s3cret") {
		t.Fatalf("the token as the answer: %q, %v", out, err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused answer wrote the file: %v", err)
	}
	out, err = answerFrom(t, me, "GOOGLE_ACCOUNT=op@gmail.example.com\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mckean gave GOOGLE_ACCOUNT · it is set in " + file + " · no mint_command is set: yardr source set google-mail mint_command=<program> [args...]\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "GOOGLE_ACCOUNT=op@gmail.example.com\n" {
		t.Fatalf("the file holds %q", b)
	}
}

// bead ask --secret for names the source marks as no secret stores the ask
// unconcealed, with concealed false; a mixed or secret one concealed.
func TestBeadAskSecretConcealed(t *testing.T) {
	t.Parallel()
	st, id, _ := secretYard(t)
	const mixedDecl = `name: gog
base: https://gog.example.com
credential:
  env: GOG_TOKEN
  header: "Authorization: Bearer {credential}"
  mint:
    - name: GOG_ACCOUNT
      secret: false
    - GOG_KEY
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	if _, err := st.PutSource(context.Background(), "gog.yaml", mixedDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	yard := &app{st: st, actor: "yardmaster"}
	ask := func(env ...string) uint64 {
		t.Helper()
		args := []string{"ask", id, "The account", "--secret", "gog", "--context", "Where to get it."}
		for _, e := range env {
			args = append(args, "--env", e)
		}
		out, err := run(t, yard.beadCmd(), args...)
		if err != nil {
			t.Fatal(err)
		}
		var seq uint64
		if _, err := fmt.Sscanf(out, "ask %d ", &seq); err != nil {
			t.Fatalf("ask printed %q: %v", out, err)
		}
		return seq
	}
	open := ask("GOG_ACCOUNT")
	secret := ask("GOG_KEY")
	mixed := ask("GOG_ACCOUNT", "GOG_KEY")
	v, err := st.Bead(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	bySeq := map[uint64]store.BeadAsk{}
	for _, a := range v.Asks {
		bySeq[a.Ask] = a
	}
	if c := bySeq[open].Concealed; c == nil || *c {
		t.Errorf("the not-secret ask is concealed: %+v", c)
	}
	for seq, name := range map[uint64]string{secret: "secret", mixed: "mixed"} {
		if c := bySeq[seq].Concealed; c != nil {
			t.Errorf("the %s ask is unconcealed: %+v", name, *c)
		}
	}
}

// A server that dies during the giving: bead answer --secret says the
// words every client says of one, never the transport's error and never
// the value.
func TestBeadAnswerSecretWithoutAnAnswer(t *testing.T) {
	t.Parallel()
	st, id, _ := secretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	if _, err := run(t, yard.beadCmd(), "ask", id, "The API key for the CRM", "--secret", "crm", "--env", "CRM_TOKEN",
		"--context", "Create one at https://crm.example.com/account/api (read only)."); err != nil {
		t.Fatal(err)
	}
	me := &app{st: dyingClient(t, st, "GiveSecret"), actor: "mckean"}
	out, err := answerFrom(t, me, "CRM_TOKEN=s3cret\n", id, "1", "--secret")
	if err == nil || err.Error() != store.SecretNoAnswer {
		t.Fatalf("answer = %q, err = %v, want the words %q", out, err, store.SecretNoAnswer)
	}
	if out != "" || strings.Contains(out+err.Error(), "s3cret") {
		t.Fatalf("answer printed %q with the value in it", out)
	}
}

// bead ask --secret-login asks for a waiting login's input and prints where
// its answer goes; bead answer --secret hands it over from NAME=value lines
// on a pipe, and prints who gave which names and the login, never a value.
// With no login waiting the answer is refused naming that.
func TestBeadAskAndAnswerALoginInput(t *testing.T) {
	t.Parallel()
	st, id, _ := secretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}

	out, err := run(t, yard.beadCmd(), "ask", id, "The code the sign-in page showed", "--secret-login", "claude-a1b2c3d4",
		"--context", "The login waits for what the sign-in page showed.")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for mckean: a secret, INPUT for the waiting login claude-a1b2c3d4\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	for name, args := range map[string][]string{
		"a source beside it":  {"--secret-login", "claude-x", "--secret", "crm"},
		"an env beside it":    {"--secret-login", "claude-x", "--env", "INPUT"},
		"a user beside it":    {"--secret-login", "claude-x", "--for-user", "mckean"},
		"a command beside it": {"--secret-login", "claude-x", "--run", "peer allow north --mail-limit 300"},
	} {
		full := append([]string{"ask", id, "The code", "--context", "where to get it."}, args...)
		if _, err := run(t, yard.beadCmd(), full...); err == nil || !strings.Contains(err.Error(), "one of the two") && !strings.Contains(err.Error(), "takes no --") {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a secret: INPUT, for the waiting login claude-a1b2c3d4",
		"it goes to the waiting login only; it is kept nowhere, and no agent is shown it",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}

	for name, c := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"another name":   {stdin: "CODE=s3cret\n", args: []string{id, "1", "--secret"}, want: "name that was not asked for"},
		"no line":        {stdin: "# nothing\n", args: []string{id, "1", "--secret"}, want: "has no line INPUT=<value>"},
		"no login waits": {stdin: "INPUT=s3cret\n", args: []string{id, "1", "--secret"}, want: "no login waits under it"},
	} {
		out, err := answerFrom(t, me, c.stdin, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(out+err.Error(), "s3cret") {
			t.Errorf("%s: %q, err = %v, want %q and no value", name, out, err, c.want)
		}
	}

	// The wait registers in its own goroutine: the answer below starts once
	// a probe is refused as a duplicate. A wait refused as a duplicate
	// tries again: a probe may hold the reference for the moment it checks.
	waited := make(chan string, 1)
	go func() {
		for {
			value, err := st.LoginWait(context.Background(), "claude-a1b2c3d4")
			if err == nil {
				waited <- value
				return
			}
			if !strings.Contains(err.Error(), "already waits") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, leave := context.WithCancel(context.Background())
		leave()
		_, err := st.LoginWait(probe, "claude-a1b2c3d4")
		if err != nil && strings.Contains(err.Error(), "already waits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wait never registered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err = answerFrom(t, me, "INPUT=s3cret-code\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mckean gave INPUT · it went to the waiting login claude-a1b2c3d4, and is kept nowhere\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if got := <-waited; got != "s3cret-code" {
		t.Fatalf("the waiter was handed %q", got)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean gave INPUT") || strings.Contains(out, "s3cret") {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}
}

// bead answer --secret gives a harness-key secret ask -- the yard's led
// login asks for it, no bead ask flag does -- from NAME=value lines on a
// pipe, and prints who gave which names and the file, never a value.
func TestBeadAnswerAHarnessKey(t *testing.T) {
	t.Parallel()
	st, id, srcFile := secretYard(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAsk(ctx, id, store.Ask{Question: "The key it runs with", To: "mckean",
		Context: []string{"Create one at https://console.anthropic.com/ (API usage billing).", "It is written into the operator's harness key file and read by every Claude Code session on the operator's login."},
		Secret:  &store.AskSecret{Harness: "claude", Crew: "yardmaster"}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	me := &app{st: st, actor: "mckean"}

	out, err := run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login",
		"the value is kept out of notes, events and logs; each such session receives it in its environment",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}

	if out, err := answerFrom(t, me, "OTHER_KEY=s3cret\n", id, "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "name that was not asked for") || strings.Contains(out+err.Error(), "s3cret") {
		t.Fatalf("another name: %q, err = %v", out, err)
	}
	out, err = answerFrom(t, me, "ANTHROPIC_API_KEY=sk-ant-s3cret\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	// The operator's key file of the kind, which every session on their
	// login reads, and no crew member's.
	file := filepath.Join(filepath.Dir(filepath.Dir(srcFile)), store.HarnessDir, "claude", "mckean.env")
	want := "mckean gave ANTHROPIC_API_KEY · it is set in " + file + "\nnext: every Claude Code session that burns mckean's login reads it from its next start\n"
	if out != want {
		t.Fatalf("answer printed %q\nwant            %q", out, want)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "ANTHROPIC_API_KEY=sk-ant-s3cret\n" {
		t.Fatalf("the file holds %q, %v", b, err)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean gave ANTHROPIC_API_KEY") || strings.Contains(out, "s3cret") {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}
}
