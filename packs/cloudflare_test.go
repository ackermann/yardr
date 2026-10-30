package packs_test

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

const (
	cfAccount = "0123456789abcdef0123456789abcdef"
	cfZone    = "fedcba9876543210fedcba9876543210"
	cfTokenID = "tok123abc"
	cfToken   = "cf-test-token"
)

// The cloudflare pack is one declared source of kind source with a
// read preset and a deploy preset: its declaration, parsed as the
// yard parses it, names the token's variable and the four update
// calls the deploy preset grants.
func TestCloudflarePackDeclaresItsSource(t *testing.T) {
	sub, err := fs.Sub(packs.FS, "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Load(sub, packs.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "source" || len(p.Declared) != 1 || p.Declared[0].Name != "cloudflare" {
		t.Fatalf("kind %q, declared %+v, want one source cloudflare", p.Kind, p.Declared)
	}
	if len(p.Presets) != 2 || p.Presets[0].Name != "read" || p.Presets[1].Name != "deploy" {
		t.Fatalf("presets %+v, want the read and deploy presets", p.Presets)
	}
	if got := p.Presets[0].Grants; len(got) != 1 || got[0].Source != "cloudflare" ||
		!reflect.DeepEqual(got[0].Calls, []string{store.GrantRead}) {
		t.Fatalf("read preset grants %+v, want cloudflare [read]", got)
	}
	if got := p.Presets[1].Grants; len(got) != 1 || got[0].Source != "cloudflare" ||
		!reflect.DeepEqual(got[0].Calls, []string{store.GrantRead, "worker_upload", "worker_secret", "email_rule", "email_rule_edit"}) {
		t.Fatalf("deploy preset grants %+v, want cloudflare [read worker_upload worker_secret email_rule email_rule_edit]", got)
	}
	d := p.Declared[0].Declaration
	if d.Base != "https://api.cloudflare.com/client/v4" {
		t.Errorf("base %q, want the Cloudflare API v4 base", d.Base)
	}
	if d.Credential == nil || d.Credential.Env != "CLOUDFLARE_API_TOKEN" ||
		d.Credential.Header != "Authorization: Bearer {credential}" {
		t.Errorf("credential = %+v, want CLOUDFLARE_API_TOKEN as a Bearer token", d.Credential)
	}
	if d.Check != "verify" {
		t.Errorf("check %q, want verify", d.Check)
	}
	updates := map[string]bool{"worker_upload": true, "worker_secret": true, "email_rule": true, "email_rule_edit": true}
	var names []string
	for _, c := range d.Calls {
		names = append(names, c.Name)
		if updates[c.Name] && c.Kind != source.Update {
			t.Errorf("call %s is %s, want update", c.Name, c.Kind)
		}
		if !updates[c.Name] && c.Kind != source.Read {
			t.Errorf("call %s is %s, want read", c.Name, c.Kind)
		}
	}
	want := []string{"verify", "token", "accounts", "zones", "workers", "email_rules", "email_addresses", "worker_upload", "worker_secret", "email_rule", "email_rule_edit"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("calls = %v, want %v", names, want)
	}
	// Every file the pack ships is named, so the pack's digest covers
	// it and an apply installs it: pack.yaml, the setup notes, each
	// declaration, and the program of each command a declaration
	// names, beside its declaration. A file named by none of them
	// (a program the declaration never mentions) ships uncovered.
	named := map[string]bool{pack.File: true, p.Setup: true}
	for _, declared := range p.Declared {
		named[declared.File] = true
		for _, c := range declared.Declaration.Commands() {
			if c.Relative() {
				named[filepath.Join(filepath.Dir(declared.File), c.Argv[0])] = true
			}
		}
	}
	err = fs.WalkDir(sub, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !named[path] {
			t.Errorf("file %s is in the pack but named by nothing: the digest does not cover it and no apply installs it", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// cloudflareStandin answers as the Cloudflare API v4 does for the
// declaration's calls, and keeps the last request it was sent. Its
// token carries scopes, the permission groups Cloudflare reports for
// it, so a test can take one away.
type cloudflareStandin struct {
	t            *testing.T
	mu           sync.Mutex
	last         string
	auth         string
	body         string
	contentType  string
	scopes       []string
	failAccounts bool
}

func (s *cloudflareStandin) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.last, s.auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
	s.body, s.contentType = string(raw), r.Header.Get("Content-Type")
	scopes := s.scopes
	s.mu.Unlock()
	if s.auth != "Bearer "+cfToken {
		s.t.Errorf("%s: Authorization %q, want the Bearer token", s.last, s.auth)
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET /user/tokens/verify":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"`+cfTokenID+`","status":"active"}}`)
	case "GET /user/tokens/" + cfTokenID:
		canReadToken := false
		for _, name := range scopes {
			if name == "API Tokens Read" {
				canReadToken = true
				break
			}
		}
		if !canReadToken {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"missing API Tokens Read"}],"messages":[],"result":null}`)
			return
		}
		var groups []string
		for _, name := range scopes {
			groups = append(groups, `{"id":"g","name":"`+name+`"}`)
		}
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"`+cfTokenID+
			`","name":"yardr","status":"active","policies":[{"id":"p","effect":"allow","permission_groups":[`+
			strings.Join(groups, ",")+`],"resources":{"com.cloudflare.api.account.*":"*"}}]}}`)
	case "GET /accounts":
		if s.failAccounts {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"token cannot list accounts"}],"messages":[],"result":null}`)
			return
		}
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"`+cfAccount+`","name":"Ada's Account","type":"standard"}],`+
			`"result_info":{"page":1,"per_page":20,"total_pages":1,"count":1,"total_count":1}}`)
	case "GET /zones":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"`+cfZone+`","name":"example.com","status":"active"}],`+
			`"result_info":{"page":1,"per_page":20,"total_pages":1,"count":1,"total_count":1}}`)
	case "GET /accounts/" + cfAccount + "/workers/scripts":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"mail-worker","created_on":"2026-10-29T00:00:00Z","modified_on":"2026-10-29T00:00:00Z"}],`+
			`"result_info":{"page":1,"per_page":20,"total_pages":1,"count":1,"total_count":1}}`)
	case "GET /zones/" + cfZone + "/email/routing/rules":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"rule1","name":"mail to worker","enabled":true,"matchers":[{"type":"literal","field":"to","value":"ada@example.com"}],"actions":[{"type":"worker","value":["mail-worker"]}]}],`+
			`"result_info":{"page":1,"per_page":20,"total_pages":1,"count":1,"total_count":1}}`)
	case "GET /accounts/" + cfAccount + "/email/routing/addresses":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"addr1","email":"ada@example.com","verified":"2026-10-29T00:00:00Z","created":"2026-10-29T00:00:00Z"}],`+
			`"result_info":{"page":1,"per_page":20,"total_pages":1,"count":1,"total_count":1}}`)
	case "PUT /accounts/" + cfAccount + "/workers/scripts/mail-worker":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"mail-worker","etag":"e1"}}`)
	case "PUT /accounts/" + cfAccount + "/workers/scripts/mail-worker/secrets":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"name":"MAIL_KEY","type":"secret_text"}}`)
	case "POST /zones/" + cfZone + "/email/routing/rules":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"rule2","name":"ada@example.com to mail-worker","enabled":true,"matchers":[{"type":"literal","field":"to","value":"ada@example.com"}],"actions":[{"type":"worker","value":["mail-worker"]}]}}`)
	case "PUT /zones/" + cfZone + "/email/routing/rules/rule1":
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"rule1","name":"ada@example.com to mail-worker","enabled":true,"matchers":[{"type":"literal","field":"to","value":"ada@example.com"}],"actions":[{"type":"worker","value":["mail-worker"]}]}}`)
	default:
		s.t.Errorf("the stand-in has no answer for %s %s", r.Method, r.URL.Path)
		http.Error(w, "no such recorded shape", 404)
	}
}

func (s *cloudflareStandin) request() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *cloudflareStandin) sent() (body, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body, s.contentType
}

func cloudflareDeclaration(t *testing.T, standin string) *source.Declaration {
	t.Helper()
	raw, err := fs.ReadFile(packs.FS, "cloudflare/cloudflare.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse("cloudflare.yaml",
		[]byte(strings.Replace(string(raw), "base: https://api.cloudflare.com/client/v4", "base: "+standin, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != standin {
		t.Fatalf("base %s, want the stand-in", d.Base)
	}
	return d
}

// Every read call of the declaration, against the stand-in: the
// request it sends and the list its answer selects. A call the
// stand-in has no recorded shape for fails here, as does one that
// sends its parameters anywhere but the path the API has for it.
func TestCloudflareCallsAgainstTheStandin(t *testing.T) {
	standin := &cloudflareStandin{t: t, scopes: []string{"API Tokens Read", "Workers Scripts Edit", "Email Routing Addresses Read", "Email Routing Rules Edit", "Zone Read"}}
	srv := httptest.NewServer(http.HandlerFunc(standin.handler))
	defer srv.Close()
	d := cloudflareDeclaration(t, srv.URL)
	for _, tc := range []struct {
		call   string
		params map[string]string
		path   string
		want   string
	}{
		{"verify", nil, "GET /user/tokens/verify", `"status":"active"`},
		{"token", map[string]string{"token_id": cfTokenID}, "GET /user/tokens/" + cfTokenID, "Workers Scripts Edit"},
		{"accounts", nil, "GET /accounts", "Ada's Account"},
		{"zones", nil, "GET /zones", "example.com"},
		{"workers", map[string]string{"account": cfAccount}, "GET /accounts/" + cfAccount + "/workers/scripts", "mail-worker"},
		{"email_rules", map[string]string{"zone": cfZone}, "GET /zones/" + cfZone + "/email/routing/rules", "ada@example.com"},
		{"email_addresses", map[string]string{"account": cfAccount}, "GET /accounts/" + cfAccount + "/email/routing/addresses", "ada@example.com"},
	} {
		res, err := source.Do(context.Background(), d, tc.call, tc.params, cfToken)
		if err != nil {
			t.Errorf("call %s: %v", tc.call, err)
			continue
		}
		if !res.OK() {
			t.Errorf("call %s: status %d, want a success", tc.call, res.Status)
			continue
		}
		if got := standin.request(); got != tc.path {
			t.Errorf("call %s sent %q, want %q", tc.call, got, tc.path)
		}
		if !strings.Contains(string(res.Answer), tc.want) {
			t.Errorf("call %s answer %s lacks %q", tc.call, res.Answer, tc.want)
		}
		if strings.Contains(string(res.Answer), cfToken) {
			t.Errorf("call %s answer repeats the token", tc.call)
		}
	}
	// What a missing scope does after setup: the token read is refused
	// (403, no answer), so the scope SETUP.md names shows by its
	// absence beside the policies the token does carry, and a refused
	// accounts list is no success either. Neither refusal is an error
	// of the call: the service answered, and the answer is the refusal.
	noTokenRead := &cloudflareStandin{t: t, scopes: []string{"Workers Scripts Edit", "Email Routing Addresses Read", "Email Routing Rules Edit", "Zone Read"}}
	srv2 := httptest.NewServer(http.HandlerFunc(noTokenRead.handler))
	defer srv2.Close()
	res, err := source.Do(context.Background(), cloudflareDeclaration(t, srv2.URL), "token", map[string]string{"token_id": cfTokenID}, cfToken)
	if err != nil {
		t.Fatalf("token call without API Tokens Read: %v", err)
	}
	if res.OK() || res.Status != http.StatusForbidden {
		t.Errorf("token call without API Tokens Read: status %d, want a 403 refusal", res.Status)
	}
	noAccounts := &cloudflareStandin{t: t, scopes: []string{"API Tokens Read", "Workers Scripts Edit", "Email Routing Addresses Read", "Email Routing Rules Edit", "Zone Read"}, failAccounts: true}
	srv3 := httptest.NewServer(http.HandlerFunc(noAccounts.handler))
	defer srv3.Close()
	res, err = source.Do(context.Background(), cloudflareDeclaration(t, srv3.URL), "accounts", nil, cfToken)
	if err != nil {
		t.Fatalf("accounts call the stand-in refuses: %v", err)
	}
	if res.OK() || res.Status != http.StatusForbidden {
		t.Errorf("accounts call the stand-in refuses: status %d, want a 403 refusal", res.Status)
	}
}

// Every update call of the declaration, against the stand-in: the
// upload sends the file's bytes as one part beside its metadata, the
// secret sends the value its file holds and nothing names that value
// but the request's body, and the rule calls send the address and the
// Worker, the edit to the standing rule's own address.
func TestCloudflareUpdateCallsAgainstTheStandin(t *testing.T) {
	standin := &cloudflareStandin{t: t, scopes: []string{"API Tokens Read", "Workers Scripts Edit", "Email Routing Addresses Read", "Email Routing Rules Edit", "Zone Read"}}
	srv := httptest.NewServer(http.HandlerFunc(standin.handler))
	defer srv.Close()
	d := cloudflareDeclaration(t, srv.URL)
	dir := t.TempDir()
	script := "export default { async fetch() { return new Response('mail') } }\n"
	scriptFile := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(scriptFile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := source.Do(context.Background(), d, "worker_upload", map[string]string{
		"account": cfAccount, "name": "mail-worker", "script": scriptFile}, cfToken)
	if err != nil || !res.OK() {
		t.Fatalf("worker_upload: %v, %+v", err, res)
	}
	if got, want := standin.request(), "PUT /accounts/"+cfAccount+"/workers/scripts/mail-worker"; got != want {
		t.Errorf("worker_upload sent %q, want %q", got, want)
	}
	body, contentType := standin.sent()
	if !strings.HasPrefix(contentType, "multipart/form-data; boundary=") {
		t.Errorf("worker_upload content type %q, want a multipart upload", contentType)
	}
	if !strings.Contains(body, `"main_module":"worker.js"`) || !strings.Contains(body, script) {
		t.Errorf("worker_upload body lacks its metadata or the file's bytes: %q", body)
	}
	const secretValue = "mail-secret-value-123"
	secretFile := filepath.Join(dir, "mail-key")
	if err := os.WriteFile(secretFile, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = source.Do(context.Background(), d, "worker_secret", map[string]string{
		"account": cfAccount, "name": "mail-worker", "secret": "MAIL_KEY", "value": secretFile}, cfToken)
	if err != nil || !res.OK() {
		t.Fatalf("worker_secret: %v, %+v", err, res)
	}
	if got, want := standin.request(), "PUT /accounts/"+cfAccount+"/workers/scripts/mail-worker/secrets"; got != want {
		t.Errorf("worker_secret sent %q, want %q", got, want)
	}
	body, _ = standin.sent()
	if !strings.Contains(body, `"name":"MAIL_KEY"`) || !strings.Contains(body, `"text":"`+secretValue+`"`) || !strings.Contains(body, `"type":"secret_text"`) {
		t.Errorf("worker_secret body %q lacks the secret's name, its file's value and its kind", body)
	}
	if strings.Contains(string(res.Answer), secretValue) {
		t.Errorf("worker_secret answer repeats the secret's value")
	}
	call, _ := d.Call("worker_secret")
	secretParams := map[string]string{"account": cfAccount, "name": "mail-worker", "secret": "MAIL_KEY", "value": secretFile}
	inputs, err := call.ReadInputs(secretParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("changed after the card\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = source.DoKeyedWithInputs(context.Background(), d, "worker_secret", secretParams, cfToken, "", inputs)
	if err != nil || !res.OK() {
		t.Fatalf("worker_secret with captured input: %v, %+v", err, res)
	}
	body, _ = standin.sent()
	if !strings.Contains(body, `"text":"`+secretValue+`"`) || strings.Contains(body, "changed after the card") {
		t.Errorf("worker_secret sent %q instead of the value captured for approval", body)
	}
	for _, tc := range []struct {
		call   string
		params map[string]string
		path   string
	}{
		{"email_rule", map[string]string{"zone": cfZone, "address": "ada@example.com", "worker": "mail-worker"},
			"POST /zones/" + cfZone + "/email/routing/rules"},
		{"email_rule_edit", map[string]string{"zone": cfZone, "rule": "rule1", "address": "ada@example.com", "worker": "mail-worker"},
			"PUT /zones/" + cfZone + "/email/routing/rules/rule1"},
	} {
		res, err := source.Do(context.Background(), d, tc.call, tc.params, cfToken)
		if err != nil || !res.OK() {
			t.Fatalf("%s: %v, %+v", tc.call, err, res)
			continue
		}
		if got := standin.request(); got != tc.path {
			t.Errorf("%s sent %q, want %q", tc.call, got, tc.path)
		}
		body, _ := standin.sent()
		if !strings.Contains(body, `"value":"ada@example.com"`) || !strings.Contains(body, `"value":["mail-worker"]`) {
			t.Errorf("%s body %q lacks the address or the Worker", tc.call, body)
		}
	}
	tooLongAddress := strings.Repeat("a", 88) + "@x.y"
	if _, err := source.Do(context.Background(), d, "email_rule", map[string]string{
		"zone": cfZone, "address": tooLongAddress, "worker": "mail-worker"}, cfToken); err == nil ||
		!strings.Contains(err.Error(), "at most 90") {
		t.Errorf("email_rule with a long address: err %v, want the API's 90-character bound", err)
	}
	if _, err := source.Do(context.Background(), d, "email_rule_edit", map[string]string{
		"zone": cfZone, "rule": strings.Repeat("a", 33), "address": "ada@example.com", "worker": "mail-worker"}, cfToken); err == nil ||
		!strings.Contains(err.Error(), "at most 32") {
		t.Errorf("email_rule_edit with a long rule id: err %v, want the API's 32-character bound", err)
	}
	// A secret file the yard may not read (its mode is not 0600) is
	// refused before anything is sent, naming the parameter alone.
	loose := filepath.Join(dir, "loose-key")
	if err := os.WriteFile(loose, []byte(secretValue), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Do(context.Background(), d, "worker_secret", map[string]string{
		"account": cfAccount, "name": "mail-worker", "secret": "MAIL_KEY", "value": loose}, cfToken); err == nil ||
		!strings.Contains(err.Error(), "parameter value") || strings.Contains(err.Error(), secretValue) || strings.Contains(err.Error(), loose) {
		t.Errorf("worker_secret with a loose file: err %v, want a refusal naming the parameter alone", err)
	}
}
