package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const runTestDeclaration = `name: mail
base: http://127.0.0.1:1143
credential:
  env: MAIL_PASSWORD
calls:
  search:
    kind: read
    about: Find messages in a folder
    params:
      folder: { type: string, max: 100 }
      limit: { type: int, default: 10 }
      unread: { type: bool, optional: true }
    run: [mail/imap, search]
    answer: { select: [messages] }
  ping: { kind: read, request: { method: GET, path: /ping } }
`

// runDeclaration is the declaration of runTestDeclaration, parsed, and the
// program of its search call written under a home of its own as the yard
// installs one: the body is a script for sh.
func runDeclaration(t *testing.T, text string) (d *Declaration, home string, write func(body string)) {
	t.Helper()
	d, err := Parse("mail.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	program := filepath.Join(home, "sources", "mail", "mail", "imap")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	return d, home, func(body string) {
		t.Helper()
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// The line that shows a run call stands on the cards the operator reads,
// and its words are the declaration's text: a word that holds a space or a
// break is quoted, so the line stays one line.
func TestRunCallLineQuotesItsWords(t *testing.T) {
	text := strings.Replace(runTestDeclaration, "run: [mail/imap, search]", `run: [mail/imap, "all mail", "a\nb"]`, 1)
	d, err := Parse("mail.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := d.Call("search")
	if !ok {
		t.Fatal("the search call is gone")
	}
	if got := c.Line(); got != `runs mail/imap "all mail" "a\nb"` {
		t.Fatalf("line = %q", got)
	}
}

func TestRunCallHandsTypedParamsAndCredentialAndAnswersWithTheObject(t *testing.T) {
	d, home, write := runDeclaration(t, runTestDeclaration)
	printed := `{"messages":[{"id":1,"from":"ada"}],"private":"not selected"}`
	write(`cat > run-stdin; env > run-environment; printf '%s' "$1" > run-argument; printf '%s' '` + printed + `'`)
	t.Setenv("YARDR_SESSION", "not-for-a-run")
	t.Setenv("MAIL_PASSWORD", "not-this-one")
	res, err := RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX", "limit": "5"}, "mail-password", home, noSecrets)
	if err != nil {
		t.Fatal(err)
	}
	// The answer is a 200 with the object after the call's select, and its
	// size is what the command printed.
	if res.Status != http.StatusOK || !res.OK() || string(res.Answer) != `{"messages":[{"id":1,"from":"ada"}]}` || res.Bytes != len(printed) || res.Took <= 0 {
		t.Fatalf("result = %+v", res)
	}
	in, _ := os.ReadFile(filepath.Join(home, "run-stdin"))
	if string(in) != `{"params":{"folder":"INBOX","limit":5}}` {
		t.Fatalf("stdin = %s", in)
	}
	if arg, _ := os.ReadFile(filepath.Join(home, "run-argument")); string(arg) != "search" {
		t.Fatalf("argument = %q", arg)
	}
	env, _ := os.ReadFile(filepath.Join(home, "run-environment"))
	for _, want := range []string{"MAIL_PASSWORD=mail-password", "YARDR_SOURCE_BASE=http://127.0.0.1:1143"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("environment lacks %q:\n%s", want, env)
		}
	}
	for _, unwanted := range []string{"YARDR_SESSION=", "not-this-one"} {
		if strings.Contains(string(env), unwanted) {
			t.Errorf("environment contains %q:\n%s", unwanted, env)
		}
	}
	// What the declaration says of the answer holds for a command's: left
	// whole where it names no select, refused where it is over the bound.
	whole := strings.Replace(runTestDeclaration, "    answer: { select: [messages] }\n", "", 1)
	d, home, write = runDeclaration(t, whole)
	write(`printf '%s' '` + printed + `'`)
	if res, err = RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "mail-password", home, noSecrets); err != nil || string(res.Answer) != printed {
		t.Fatalf("a call that selects nothing = %+v, %v", res, err)
	}
	bound := strings.Replace(whole, "    run: [mail/imap, search]\n", "    run: [mail/imap, search]\n    answer: { max: 20 }\n", 1)
	d, home, write = runDeclaration(t, bound)
	write(`printf '%s' '` + printed + `'`)
	_, err = RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "mail-password", home, noSecrets)
	if reason(err) != TooLarge || !strings.Contains(err.Error(), fmt.Sprintf("the answer is %d bytes, over the call's bound of 20", len(printed))) || strings.Contains(err.Error(), "ada") {
		t.Fatalf("an answer over its bound = %v", err)
	}
}

func TestRunCallRefusalIsOnlyTheCommandsWords(t *testing.T) {
	d, home, write := runDeclaration(t, runTestDeclaration)
	write(`printf '%s' 'stdout-must-not-leak'; echo 'no folder INBOX2; try one of: INBOX, Sent' >&2; exit 1`)
	_, err := RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX2"}, "mail-password", home, noSecrets)
	if reason(err) != RunRefused || err.Error() != "source mail, call search: no folder INBOX2; try one of: INBOX, Sent" {
		t.Fatalf("refusal = %v", err)
	}
	if strings.Contains(err.Error(), "stdout-must-not-leak") || strings.Contains(err.Error(), "exit status") {
		t.Fatalf("refusal = %v", err)
	}
	// A command that says nothing is told by the exit itself, and one that
	// echoes the credential does not give it away, refusing or breaking.
	for name, tc := range map[string]struct {
		body    string
		reason  string
		mention string
	}{
		"a silent refusal":    {`exit 3`, RunRefused, "exit status 3"},
		"a refusal that echo": {`echo "login failed with $MAIL_PASSWORD" >&2; exit 1`, RunRefused, "login failed with [credential redacted]"},
		"a failure that echo": {`echo "login failed with $MAIL_PASSWORD" >&2; printf 'not JSON'`, RunFailed, "login failed with [credential redacted]"},
	} {
		t.Run(name, func(t *testing.T) {
			write(tc.body)
			_, err := RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "mail-password", home, noSecrets)
			if reason(err) != tc.reason || !strings.Contains(err.Error(), tc.mention) || strings.Contains(err.Error(), "mail-password") {
				t.Fatalf("RunCall = %v; want %s with %q and without the credential", err, tc.reason, tc.mention)
			}
		})
	}
}

func TestRunCallBreakage(t *testing.T) {
	d, home, write := runDeclaration(t, runTestDeclaration)
	call := func() error {
		t.Helper()
		_, err := RunCall(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "mail-password", home, noSecrets)
		return err
	}
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"nothing":       {`exit 0`, "it printed nothing"},
		"no JSON":       {`printf '%s' 'stdout-must-not-leak'`, "what it printed is not one JSON object"},
		"trailing data": {`printf '{} trailing'`, "what it printed is not one JSON object"},
		"two objects":   {`printf '{} {}'`, "it printed more than one JSON object"},
		"a list":        {`printf '%s' '[{"messages":[]}]'`, "its answer is no JSON object"},
		"a string":      {`printf '"stdout-must-not-leak"'`, "its answer is no JSON object"},
		"a number":      {`printf '2'`, "its answer is no JSON object"},
		"too much":      {`head -c 65537 /dev/zero | tr '\0' ' '; printf '{}'`, "printed more than 65536 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			write(tc.body)
			err := call()
			if reason(err) != RunFailed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RunCall = %v; want %s containing %q", err, RunFailed, tc.want)
			}
			if strings.Contains(err.Error(), "stdout-must-not-leak") {
				t.Fatalf("the error holds stdout: %v", err)
			}
		})
	}

	// The most a command may print is what it prints exactly.
	write(`head -c 65500 /dev/zero | tr '\0' ' '; printf '{}'`)
	if err := call(); err != nil {
		t.Fatalf("an answer inside the bound: %v", err)
	}

	if err := os.Remove(filepath.Join(home, "sources", "mail", "mail", "imap")); err != nil {
		t.Fatal(err)
	}
	if err := call(); reason(err) != RunFailed {
		t.Fatalf("a missing program = %v", err)
	}

	write(`exec sleep 300`)
	was := Timeout
	Timeout = 50 * time.Millisecond
	t.Cleanup(func() { Timeout = was })
	if err := call(); reason(err) != RunFailed || !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Fatalf("a slow command = %v", err)
	}
}

// A run call is held to what every call is before its command runs: it has
// to be there and name a command, the credential has to be set and one line,
// and the parameters have to be those it declares. None of them starts the
// command.
func TestRunCallHoldsTheCallsRulesBeforeItRuns(t *testing.T) {
	d, home, write := runDeclaration(t, runTestDeclaration)
	ran := filepath.Join(home, "ran")
	write(`: > ran; printf '{}'`)
	folder := map[string]string{"folder": "INBOX"}
	for name, tc := range map[string]struct {
		call       string
		params     map[string]string
		credential string
		reason     string
		want       string
	}{
		"no such call":               {"nosuch", folder, "pw", NoCall, "the source has no call of that name"},
		"a call of a request":        {"ping", nil, "pw", RunFailed, "the call names no run command"},
		"no credential":              {"search", folder, "", NoCredential, "no credential: the source's env file does not set MAIL_PASSWORD"},
		"a credential of lines":      {"search", folder, "pw\nword", NoCredential, "has a control character in it"},
		"a parameter missing":        {"search", nil, "pw", BadParam, "parameter folder is missing"},
		"a parameter unknown":        {"search", map[string]string{"folder": "INBOX", "nosuch": "x"}, "pw", BadParam, `"nosuch" is no parameter of the call`},
		"a parameter that is no int": {"search", map[string]string{"folder": "INBOX", "limit": "many"}, "pw", BadParam, "parameter limit is no whole number"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RunCall(context.Background(), d, tc.call, tc.params, tc.credential, home, noSecrets)
			if reason(err) != tc.reason || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "many") {
				t.Fatalf("RunCall = %v; want %s containing %q", err, tc.reason, tc.want)
			}
			if _, statErr := os.Stat(ran); statErr == nil {
				t.Fatal("the command ran")
			}
		})
	}
}

// A request is made by Do, and no request is made of a call that names a
// command: nothing is sent to the base, and the refusal says why.
func TestDoRefusesARunCall(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	d, err := Parse("mail.yaml", []byte(strings.Replace(runTestDeclaration, "http://127.0.0.1:1143", srv.URL, 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, do := range []func() (*Result, error){
		func() (*Result, error) {
			return Do(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "pw", noSecrets)
		},
		func() (*Result, error) {
			return DoKeyed(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "pw", "key", noSecrets)
		},
		func() (*Result, error) {
			return DoKeyedBody(context.Background(), d, "search", map[string]string{"folder": "INBOX"}, "pw", "key", []byte(`{}`), noSecrets)
		},
	} {
		res, err := do()
		if res != nil || reason(err) != RunFailed || !strings.Contains(err.Error(), "the call names run, and a command answers it") {
			t.Fatalf("Do = %+v, %v", res, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests were made of a call that sends none", n)
	}
}

// A command is handed no credential that is the address (credential.url):
// the call goes nowhere a command could go for it, and nothing it says is
// the address either.
func TestRunCallIsHandedNoCredentialThatIsTheAddress(t *testing.T) {
	d, err := Parse("hook.yaml", []byte("name: hook\ncredential: { env: HOOK_URL, url: true }\ncalls:\n  look: { kind: read, run: [hook/look] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	program := filepath.Join(home, "sources", "hook", "hook", "look")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	script := func(body string) {
		t.Helper()
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	address := "https://hooks.example.com/hooks/" + hookSecret
	script(`env > run-environment; printf '{"looked":true}'`)
	res, err := RunCall(context.Background(), d, "look", nil, address, home, noSecrets)
	if err != nil || string(res.Answer) != `{"looked":true}` {
		t.Fatalf("RunCall = %+v, %v", res, err)
	}
	env, _ := os.ReadFile(filepath.Join(home, "run-environment"))
	for _, unwanted := range []string{"HOOK_URL", hookSecret, "hooks.example.com"} {
		if strings.Contains(string(env), unwanted) {
			t.Errorf("environment contains %q:\n%s", unwanted, env)
		}
	}
	script(`echo "no good" >&2; exit 1`)
	if _, err = RunCall(context.Background(), d, "look", nil, address, home, noSecrets); reason(err) != RunRefused || addressLeak(err, address) != "" {
		t.Fatalf("refusal = %v", err)
	}
}
