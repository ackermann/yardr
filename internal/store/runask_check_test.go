package store

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type checkHit struct {
	method, path string
	body         map[string]any
}

// checkYard is a yard with a source that tells a user at a stand-in,
// its credential set and ada's address given. status is what the
// stand-in answers until the test changes it; hits are its requests.
func checkYard(t *testing.T, hits *[]checkHit, status *int) *askYard {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hit := checkHit{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(raw, &hit.body)
		*hits = append(*hits, hit)
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			_, _ = w.Write([]byte(`{"ok":false,"description":"no"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	y, _ := runYard(t)
	text := "\nname: phone\nbase: " + srv.URL + `
credential: { env: PHONE_TOKEN }
calls:
  message:
    kind: update
    params: { user: {type: string}, title: {type: string}, text: {type: string} }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PHONE_USER }, title: title, text: text }
`
	if _, err := y.PutSource(y.ctx, "phone.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "phone.env")
	if err := os.WriteFile(env, []byte("PHONE_TOKEN=the-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "phone", SourceEnvFile, env, "mckean"); err != nil {
		t.Fatal(err)
	}
	userEnv := filepath.Join(t.TempDir(), "ada.env")
	if err := os.WriteFile(userEnv, []byte("PHONE_USER=ada-at-phone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: userEnv}, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func checkAsk(to, command string) Ask {
	return Ask{Question: "May the yard send the test message?", To: to,
		Context: []string{"ada's phone should show the test"},
		Run:     &AskRun{Argv: []string{command}}}
}

// The card of source check --user names the test message whole, the
// variable it goes to without the address, and that nobody takes a
// sent message back.
func TestSourceCheckCard(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	ask := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	got := y.asked(t, ask).Ask.Run
	want := []string{
		"it sends one real test message to ada through the source phone (http://127.0.0.1:",
		`the message is titled "yardr: a test" and reads "ada is told here by the yard, through the source phone. Sent by yardr source check phone --user ada."`,
		"goes to the PHONE_USER of ada's env file (",
		"the address itself stands in no card, note, event or answer",
	}
	for _, w := range want {
		if !strings.Contains(strings.Join(got.Does, "\n"), w) {
			t.Errorf("card without %q:\n%s", w, strings.Join(got.Does, "\n"))
		}
	}
	if strings.Contains(strings.Join(got.Does, "\n"), "ada-at-phone") {
		t.Errorf("the card holds the address:\n%s", strings.Join(got.Does, "\n"))
	}
	if got.Back != "a sent test message cannot be unsent" || got.BackBy != "nobody" {
		t.Errorf("taken back = %q by %q", got.Back, got.BackBy)
	}
	for _, key := range []string{"source phone", "user ada", "user ada PHONE_USER"} {
		if len(got.Seen[key]) != 64 {
			t.Errorf("seen[%q] = %q, want a SHA-256", key, got.Seen[key])
		}
	}
	if len(got.Seen) != 3 {
		t.Errorf("seen holds %d keys, want 3", len(got.Seen))
	}
}

// A source check ask is refused, and nothing is stored, for a user or
// a source the yard has not, a source that tells nobody, a credential
// or an address that is not set up, a check without --user, and one
// the asker may run themselves.
func TestSourceCheckAskRefusals(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		to      string
		command string
		actor   string
		want    string
	}{
		{"no such user", "mckean", "source check phone --user bob", "yardmaster", "a run ask sends a test message to a user of the yard (yardr user list), and bob is none"},
		{"no such source", "mckean", "source check pager --user ada", "yardmaster", "source pager: not found"},
		{"tells nobody", "mckean", "source check crm --user ada", "yardmaster", "the source crm has no notify key"},
		{"plain check", "mckean", "source check phone", "yardmaster", "needs no approval: run it yourself"},
		{"check as", "mckean", "source check phone --as ada", "yardmaster", "needs no approval: run it yourself"},
		{"the user asks for themselves", "ada", "source check phone --user ada", "ada", "needs no approval: run it yourself"},
		{"the operator runs it", "mckean", "source check phone --user ada", "mckean", "needs no approval: run it yourself"},
		{"asked of nobody", "bob", "source check phone --user ada", "yardmaster", "this command is ada's to approve, or the operator's (mckean) for them"},
		{"two users", "mckean", "source check phone --user ada --user bob", "yardmaster", "--user once"},
		{"user and as", "mckean", "source check phone --user ada --as ada", "yardmaster", "one of the two"},
		{"no source", "mckean", "source check --user ada", "yardmaster", "it names no source"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, checkAsk(c.to, c.command), c.actor); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want %q", c.name, err, c.want)
		}
	}
	// No address: no env file, and a file that does not set the name.
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: ""}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, checkAsk("mckean", "source check phone --user ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "user ada has no env_file: yardr user key ada phone gives PHONE_USER") {
		t.Errorf("no env file: err=%v", err)
	}
	other := filepath.Join(t.TempDir(), "ada.env")
	if err := os.WriteFile(other, []byte("OTHER=ada-at-phone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: other}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, checkAsk("mckean", "source check phone --user ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "env_file "+other+" does not set PHONE_USER") {
		t.Errorf("an env file without the name: err=%v", err)
	}
	// No credential: the source's env file is unset.
	if _, err := y.SetSource(y.ctx, "phone", SourceEnvFile, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, checkAsk("mckean", "source check phone --user ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "source phone: ") {
		t.Errorf("no credential: err=%v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a refused ask sent %d messages", len(hits))
	}
}

// Approving the card sends the test message once, with the user's
// address and the yard's credential, and records what the terminal
// command prints. The address and the credential stand in no note,
// event, answer or store file.
func TestSourceCheckApprovalSendsOnce(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	ask := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("the approval sent %d messages", len(hits))
	}
	hit := hits[0]
	if hit.method != "POST" || hit.path != "/messages.json" {
		t.Errorf("hit = %s %s", hit.method, hit.path)
	}
	if hit.body["user"] != "ada-at-phone" || hit.body["token"] != "the-token" {
		t.Errorf("hit body = %v", hit.body)
	}
	if hit.body["title"] != "yardr: a test" || hit.body["message"] != "ada is told here by the yard, through the source phone. Sent by yardr source check phone --user ada." {
		t.Errorf("hit body = %v", hit.body)
	}
	if !strings.Contains(done.Result, "phone: told ada: 200 in ") || !strings.Contains(done.Result, "next: yardr user set ada notify=phone") {
		t.Errorf("approval = %q", done.Result)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approved already") {
		t.Fatalf("a second approval: err=%v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("a second approval sent again: %d messages", len(hits))
	}
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, "ada-at-phone") || strings.Contains(n.Text, "the-token") {
			t.Fatal("the address or the credential stands in a note")
		}
	}
	events, err := y.Events(y.ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "ada-at-phone") || strings.Contains(string(raw), "the-token") {
			t.Fatal("the address or the credential stands in an event")
		}
	}
	if strings.Contains(done.Result, "ada-at-phone") || strings.Contains(done.Result, "the-token") {
		t.Fatal("the address or the credential stands in the approval")
	}
	db, err := os.ReadFile(filepath.Join(y.home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), "ada-at-phone") || strings.Contains(string(db), "the-token") {
		t.Fatal("the address or the credential stands in the store file")
	}
}

// A send the service answers with no success leaves the ask open,
// saying the answer, and approving again retries it.
func TestSourceCheckFailureLeavesAskOpenAndRetries(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusInternalServerError
	y := checkYard(t, &hits, &status)
	ask := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	_, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil)
	if err == nil || !strings.Contains(err.Error(), "was answered 500") ||
		!strings.Contains(err.Error(), "Approve again to retry") {
		t.Fatalf("failed approval = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("the failed approval sent %d messages", len(hits))
	}
	if r := y.asked(t, ask); r.State != AskOpen {
		t.Fatalf("the ask is %s after a failed send", r.State)
	}
	status = http.StatusOK
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done.Result, "phone: told ada: 200 in ") {
		t.Errorf("retry approval = %q", done.Result)
	}
	if len(hits) != 2 {
		t.Fatalf("the retry sent %d messages altogether", len(hits))
	}
}

// A send that reaches no service fails the approval the same way: the
// ask stays open, and the failure is said with the retry.
func TestSourceCheckUnreachedServiceSaysSo(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	src, err := y.GetSource(y.ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	// The source at a closed port of the loopback: plain http to this
	// machine is a stand-in's, so the call is refused, not the address.
	lines := strings.Split(src.Text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "base: ") {
			lines[i] = "base: http://127.0.0.1:1"
		}
	}
	if _, err := y.PutSource(y.ctx, "phone.yaml", strings.Join(lines, "\n"), "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	_, err = y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil)
	if err == nil || !strings.Contains(err.Error(), "telling ada through phone failed") ||
		!strings.Contains(err.Error(), "Approve again to retry") {
		t.Fatalf("unreached approval = %v", err)
	}
	if r := y.asked(t, ask); r.State != AskOpen {
		t.Fatalf("the ask is %s after a failed send", r.State)
	}
}

// An approval is held to the card: a replaced source, a re-set user,
// and an address given, removed or changed since refuse it as stale.
func TestSourceCheckStale(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	ask := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	file := u.EnvFile
	// The file's value changed under the same path: another address.
	if err := os.WriteFile(file, []byte("PHONE_USER=ada-moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, ask, "was written while ada's PHONE_USER for phone was another: it was set since")
	if err := os.WriteFile(file, []byte("OTHER=nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, ask, "was written while ada's PHONE_USER for phone was set: none is set now")
	if err := os.WriteFile(file, []byte("PHONE_USER=ada-at-phone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The user set under their name: another env file.
	other := filepath.Join(t.TempDir(), "ada.env")
	if err := os.WriteFile(other, []byte("PHONE_USER=ada-at-phone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: other}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, ask, "was written when the user ada was another: they were set since")
	if _, err := y.SetUser(y.ctx, "ada", map[string]string{UserEnvFile: file}, "mckean"); err != nil {
		t.Fatal(err)
	}
	src, err := y.GetSource(y.ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "phone.yaml", src.Text+"# touched\n", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, ask, "was written from another declaration of the source phone: it was replaced since")
	if len(hits) != 0 {
		t.Fatalf("a stale approval sent %d messages", len(hits))
	}
}

// The test message is the user's own to approve, or the operator's for
// them: the one asked approves, and nobody else.
func TestSourceCheckApprovalPermission(t *testing.T) {
	t.Parallel()
	var hits []checkHit
	status := http.StatusOK
	y := checkYard(t, &hits, &status)
	if _, err := y.AddUser(y.ctx, "bob", "mckean"); err != nil {
		t.Fatal(err)
	}
	own := y.ask(t, checkAsk("ada", "source check phone --user ada"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "approving this command is ada's, whom it was asked of") {
		t.Fatalf("the operator's approval of ada's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "yardmaster", false, nil); err == nil ||
		!strings.Contains(err.Error(), "yardmaster is a crew member") {
		t.Fatalf("a crew member's approval = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "bob", false, nil); err == nil ||
		!strings.Contains(err.Error(), "approving this command is ada's, whom it was asked of") {
		t.Fatalf("another user's approval = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, own, "ada", false, nil); err != nil {
		t.Fatalf("the user's own approval = %v", err)
	}
	ops := y.ask(t, checkAsk("mckean", "source check phone --user ada"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, ops, "ada", false, nil); err == nil ||
		!strings.Contains(err.Error(), "approving this command is mckean's, whom it was asked of") {
		t.Fatalf("ada's approval of the operator's ask = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ops, "mckean", false, nil); err != nil {
		t.Fatalf("the operator's approval = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("the approvals sent %d messages", len(hits))
	}
}
