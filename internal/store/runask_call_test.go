package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type callHit struct {
	method, path, body string
}

// callYard is a yard with a source shaped as the cloudflare source's
// update calls are, at a stand-in: a Worker upload from a file, a
// Worker secret from a secret file, and an Email Routing rule.
func callYard(t *testing.T, hits *[]callHit, defaultScript ...string) *askYard {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*hits = append(*hits, callHit{r.Method, r.URL.Path, string(raw)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"mail-worker"}}`))
	}))
	t.Cleanup(srv.Close)
	y, _ := runYard(t)
	defaultCall := ""
	if len(defaultScript) > 0 {
		defaultCall = fmt.Sprintf(`
  worker_upload_default:
    kind: update
    params:
      account: { type: string, log: true }
      name: { type: string, log: true }
      script: { type: file, max: 1048576, default: %q }
    request:
      method: PUT
      path: /accounts/{account}/workers/scripts/{name}
      body: { "worker.js": "{script}", metadata: { main_module: "worker.js" } }`, defaultScript[0])
	}
	text := "\nname: cfmail\nbase: " + srv.URL + `
bases: { alternate: "http://127.0.0.1:9/alternate" }
credential: { env: CF_TOKEN }
calls:
  worker_upload:
    kind: update
    params:
      account: { type: string, log: true }
      name: { type: string, log: true }
      script: { type: file, max: 1048576 }
    request:
      method: PUT
      path: /accounts/{account}/workers/scripts/{name}
      body: { "worker.js": "{script}", metadata: { main_module: "worker.js" } }
  worker_secret:
    kind: update
    params:
      account: { type: string, log: true }
      name: { type: string, log: true }
      secret: { type: string, log: true }
      value: { type: secret-file, max: 8192 }
    request:
      method: PUT
      path: /accounts/{account}/workers/scripts/{name}/secrets
      body: { name: "{secret}", text: "{value}", type: "secret_text" }
  email_rule:
    kind: update
    params:
      zone: { type: string, log: true }
      address: { type: string, log: true }
      worker: { type: string, log: true }
    request:
      method: POST
      path: /zones/{zone}/email/routing/rules
      body: { name: "{address} to {worker}", enabled: true, matchers: [{ type: "literal", field: "to", value: "{address}" }], actions: [{ type: "worker", value: ["{worker}"] }] }
  email_rule_edit:
    kind: update
    params:
      zone: { type: string, log: true }
      rule: { type: string, log: true }
      address: { type: string, log: true }
      worker: { type: string, log: true }
    request:
      method: PUT
      path: /zones/{zone}/email/routing/rules/{rule}
      body: { name: "{address} to {worker}", enabled: true, matchers: [{ type: "literal", field: "to", value: "{address}" }], actions: [{ type: "worker", value: ["{worker}"] }] }` +
		defaultCall + "\n"

	if _, err := y.PutSource(y.ctx, "cfmail.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "cf.env")
	if err := os.WriteFile(env, []byte("CF_TOKEN=the-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "cfmail", SourceEnvFile, env, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func callAsk(command string) Ask {
	return Ask{Question: "May the yard make this call?", To: "mckean",
		Context: []string{"the mail Worker is the job"}, Run: &AskRun{Argv: []string{command}}}
}

const callAccount = "0123456789abcdef0123456789abcdef"

// The upload card shows the file's digest and weighs code and
// outside (strong); approving it sends the file's bytes, once, and a
// device approval of it is refused with the strong words. The script
// text stands in no card, note, event or store file.
func TestSourceCallUploadCard(t *testing.T) {
	var hits []callHit
	dir := t.TempDir()
	script := "export default { async fetch() { return new Response('mail-one') } }\n"
	scriptFile := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(scriptFile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	defaultScript := "export default { async fetch() { return new Response('default-mail') } }\n"
	defaultScriptFile := filepath.Join(dir, "default-worker.js")
	if err := os.WriteFile(defaultScriptFile, []byte(defaultScript), 0o644); err != nil {
		t.Fatal(err)
	}
	y := callYard(t, &hits, defaultScriptFile)
	sum := sha256.Sum256([]byte(script))
	command := "call cfmail worker_upload account=" + callAccount + " name=mail-worker script=" + scriptFile
	ask := y.ask(t, callAsk(command), "yardmaster")
	got := y.asked(t, ask).Ask.Run
	if !strings.Contains(strings.Join(got.Does, "\n"), "sha256 "+hex.EncodeToString(sum[:])) {
		t.Errorf("upload card shows no digest of the file:\n%s", strings.Join(got.Does, "\n"))
	}
	if strings.Contains(strings.Join(got.Does, "\n"), "mail-one") {
		t.Errorf("upload card holds the script text:\n%s", strings.Join(got.Does, "\n"))
	}
	if classes := weightClasses(y.asked(t, ask).Ask.Weight); !slices.Equal(classes, []string{WeightCode, WeightOutside}) && !slices.Equal(classes, []string{WeightOutside, WeightCode}) {
		t.Fatalf("the upload card weighs %v, want code and outside", classes)
	}
	// A device approval of the strong card is refused, and runs nothing.
	phone := WithVia(y.ctx, "mckean", "phone")
	want := "ask " + itoa(ask) + " is strong (code): this approval arrived through the device phone, and a strong card is not approved through a device. Approve it at a terminal on the yard's machine. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(phone, y.bead, ask, "mckean", true, nil); err == nil || err.Error() != want {
		t.Fatalf("approving the upload through a device = %v, want %q", err, want)
	}
	if len(hits) != 0 {
		t.Fatalf("the refused approval sent %d calls", len(hits))
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done.Result, "cfmail: called worker_upload: 200 in ") {
		t.Errorf("approval = %q", done.Result)
	}
	if len(hits) != 1 || hits[0].method != "PUT" || hits[0].path != "/accounts/"+callAccount+"/workers/scripts/mail-worker" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].body, script) {
		t.Errorf("the upload sent none of the file's bytes")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "approved already") {
		t.Fatalf("a second approval: err=%v", err)
	}
	db, err := os.ReadFile(filepath.Join(y.home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), "mail-one") {
		t.Fatal("the script text stands in the store file")
	}
	defaultSum := sha256.Sum256([]byte(defaultScript))
	defaultAsk := y.ask(t, callAsk("call cfmail worker_upload_default account="+callAccount+" name=default-worker"), "yardmaster")
	defaultCard := y.asked(t, defaultAsk).Ask.Run
	if !strings.Contains(strings.Join(defaultCard.Does, "\n"), "sha256 "+hex.EncodeToString(defaultSum[:])) {
		t.Fatalf("default upload card shows no digest of the default file:\n%s", strings.Join(defaultCard.Does, "\n"))
	}
	if classes := weightClasses(y.asked(t, defaultAsk).Ask.Weight); !slices.Contains(classes, WeightCode) {
		t.Fatalf("default upload card weighs %v, want code", classes)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, defaultAsk, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[1].path != "/accounts/"+callAccount+"/workers/scripts/default-worker" || !strings.Contains(hits[1].body, defaultScript) {
		t.Fatalf("default upload sent %+v, want its default file bytes", hits)
	}
}

// The secret card shows the secret's name and the file's name alone;
// approving it sends the value the file holds, and the value stands
// in no card, note, event, approval or store file.
func TestSourceCallSecretCard(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	const secretValue = "mail-secret-value-123"
	secretFile := filepath.Join(t.TempDir(), "mail-key")
	if err := os.WriteFile(secretFile, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "call cfmail worker_secret account=" + callAccount + " name=mail-worker secret=MAIL_KEY value=" + secretFile
	ask := y.ask(t, callAsk(command), "yardmaster")
	got := y.asked(t, ask).Ask.Run
	card := strings.Join(got.Does, "\n")
	if !strings.Contains(card, "secret is MAIL_KEY") || !strings.Contains(card, secretFile) {
		t.Errorf("secret card shows neither the secret's name nor its file:\n%s", card)
	}
	if strings.Contains(card, secretValue) {
		t.Errorf("secret card holds the secret's value:\n%s", card)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].body, `"text":"`+secretValue+`"`) {
		t.Fatalf("hits = %+v, want the secret's value sent once", hits)
	}
	changedSecret := "changed-secret-value-456"
	changedFile := filepath.Join(t.TempDir(), "another-mail-key")
	if err := os.WriteFile(changedFile, []byte(secretValue), 0o600); err != nil {
		t.Fatal(err)
	}
	changedAsk := y.ask(t, callAsk("call cfmail worker_secret account="+callAccount+" name=mail-worker secret=MAIL_KEY value="+changedFile), "yardmaster")
	if err := os.WriteFile(changedFile, []byte(changedSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, changedAsk, "mckean", true, nil); err == nil ||
		!strings.Contains(err.Error(), "another secret in the file of value") {
		t.Fatalf("approving a changed secret file = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("the stale secret approval sent %d calls", len(hits)-1)
	}
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, secretValue) || strings.Contains(n.Text, changedSecret) {
			t.Fatal("a secret's value stands in a note")
		}
	}
	events, err := y.Events(y.ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), secretValue) || strings.Contains(string(raw), changedSecret) {
			t.Fatal("a secret's value stands in an event")
		}
	}
	if strings.Contains(done.Result, secretValue) || strings.Contains(done.Result, changedSecret) {
		t.Fatal("the secret's value stands in the approval")
	}
	db, err := os.ReadFile(filepath.Join(y.home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), secretValue) || strings.Contains(string(db), changedSecret) {
		t.Fatal("a secret's value stands in the store file")
	}
}

// A changed script file refuses its upload card as stale, and a rule
// card approving sends the address and the Worker.
func TestSourceCallStaleAndRule(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	if _, err := y.SetSource(y.ctx, "cfmail", SourceBase, "alternate", "mckean"); err != nil {
		t.Fatal(err)
	}
	baseAsk := y.ask(t, callAsk("call cfmail email_rule zone=zone1 address=ada@example.com worker=mail-worker"), "yardmaster")
	if card := strings.Join(y.asked(t, baseAsk).Ask.Run.Does, "\n"); !strings.Contains(card, "http://127.0.0.1:9") {
		t.Fatalf("card does not show the source's selected base:\n%s", card)
	}
	if _, err := y.SetSource(y.ctx, "cfmail", SourceBase, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	scriptFile := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(scriptFile, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ask := y.ask(t, callAsk("call cfmail worker_upload account="+callAccount+" name=mail-worker script="+scriptFile), "yardmaster")
	if err := os.WriteFile(scriptFile, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err == nil ||
		!strings.Contains(err.Error(), "was written for another script file") {
		t.Fatalf("approving a changed file = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("the stale approval sent %d calls", len(hits))
	}
	rule := y.ask(t, callAsk("call cfmail email_rule zone=zone1 address=ada@example.com worker=mail-worker"), "yardmaster")
	if classes := weightClasses(y.asked(t, rule).Ask.Weight); !slices.Equal(classes, []string{WeightOutside}) {
		t.Fatalf("the rule card weighs %v, want outside alone", classes)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, rule, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].method != "POST" || hits[0].path != "/zones/zone1/email/routing/rules" ||
		!strings.Contains(hits[0].body, `"value":"ada@example.com"`) || !strings.Contains(hits[0].body, `"value":["mail-worker"]`) {
		t.Fatalf("hits = %+v", hits)
	}
	edit := y.ask(t, callAsk("call cfmail email_rule_edit zone=zone1 rule=rule1 address=ada@example.com worker=mail-worker"), "yardmaster")
	editCard := strings.Join(y.asked(t, edit).Ask.Run.Does, "\n")
	for _, want := range []string{"rule is rule1", "address is ada@example.com", "worker is mail-worker"} {
		if !strings.Contains(editCard, want) {
			t.Errorf("edit card lacks %q:\n%s", want, editCard)
		}
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, edit, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[1].method != "PUT" || hits[1].path != "/zones/zone1/email/routing/rules/rule1" ||
		!strings.Contains(hits[1].body, `"value":"ada@example.com"`) || !strings.Contains(hits[1].body, `"value":["mail-worker"]`) {
		t.Fatalf("edit hits = %+v", hits)
	}
}

// What a card keeps of a secret file gives a guess nothing to be tested
// against: no digest of the value stands in it.
func TestSourceCallCardKeepsNoDigestOfTheSecret(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	const secretValue = "short-pin"
	secretFile := filepath.Join(t.TempDir(), "mail-key")
	if err := os.WriteFile(secretFile, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask := y.ask(t, callAsk("call cfmail worker_secret account="+callAccount+" name=mail-worker secret=MAIL_KEY value="+secretFile), "yardmaster")
	got := y.asked(t, ask).Ask.Run.Seen["secret-file value"]
	if got == "" {
		t.Fatal("the card kept nothing of the secret file, and could not go stale")
	}
	if got == seen(secretFile, secretValue) {
		t.Fatal("the card keeps a digest of the secret's value")
	}
}

// A call made as a user is weighed as-user, a read as well as an update.
func TestSourceCallAsUserWeighs(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	ask := y.ask(t, callAsk("call cfmail email_rule zone=z address=a@b.example worker=w --as mckean"), "yardmaster")
	classes := weightClasses(y.asked(t, ask).Ask.Weight)
	if !slices.Contains(classes, WeightAsUser) || !slices.Contains(classes, WeightOutside) {
		t.Fatalf("the card made as a user weighs %v, want as-user and outside", classes)
	}
}

// A grant of a call that sends a program is that call's strong card
// given for good, so a device approves neither: a grant of the upload is
// refused through a device, and a grant of a call that sends none is not.
func TestSourceGrantOfUploadIsStrong(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	phone := WithVia(y.ctx, "mckean", "phone")
	upload := y.ask(t, callAsk("source grant cfmail worker_upload --crew yardmaster"), "yardmaster")
	if classes := weightClasses(y.asked(t, upload).Ask.Weight); !slices.Contains(classes, WeightCode) {
		t.Fatalf("the grant of the upload weighs %v, want code", classes)
	}
	if _, err := y.ApproveAsk(phone, y.bead, upload, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "a strong card is not approved through a device") {
		t.Fatalf("approving the grant through a device = %v", err)
	}
	rule := y.ask(t, callAsk("source grant cfmail email_rule --crew yardmaster"), "yardmaster")
	if classes := weightClasses(y.asked(t, rule).Ask.Weight); slices.Contains(classes, WeightCode) {
		t.Fatalf("the grant of the rule weighs %v, want no code", classes)
	}
}

// A setup whose preset grants the upload weighs code, whichever preset
// the pick takes, as it weighs outside for an update.
func TestSetupGrantingUploadWeighsCode(t *testing.T) {
	var hits []callHit
	y := callYard(t, &hits)
	src, err := y.GetSource(y.ctx, "cfmail")
	if err != nil {
		t.Fatal(err)
	}
	setup := AskSetup{Pack: "cfmail", Account: []string{"CF_TOKEN"}, To: []Grantee{{Crew: "yardmaster"}},
		Carried: []AskSource{{Name: "cfmail", Text: src.Text}},
		Presets: []AskPreset{{Name: "deploy", Grants: []AskPresetGrant{{Source: "cfmail", Calls: []string{"worker_upload"}}}}}}
	var weight []AskWeight
	if err := y.writeVia(y.ctx, func(w *wtx) (err error) {
		weight, err = w.setupWeight(setup)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if classes := weightClasses(weight); !slices.Contains(classes, WeightCode) {
		t.Fatalf("the setup weighs %v, want code", classes)
	}
}
