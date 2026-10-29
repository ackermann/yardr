package packs_test

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
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

// The cloudflare pack is one declared source of kind source with one
// read preset: its declaration, parsed as the yard parses it, names
// the token's variable and no update call, so nothing it grants can
// write to Cloudflare.
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
	if len(p.Presets) != 1 || p.Presets[0].Name != "read" {
		t.Fatalf("presets %+v, want the one read preset", p.Presets)
	}
	if got := p.Presets[0].Grants; len(got) != 1 || got[0].Source != "cloudflare" ||
		!reflect.DeepEqual(got[0].Calls, []string{store.GrantRead}) {
		t.Fatalf("read preset grants %+v, want cloudflare [read]", got)
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
	var names []string
	for _, c := range d.Calls {
		names = append(names, c.Name)
		if c.Kind != source.Read {
			t.Errorf("call %s is %s: this pack reads and writes nothing", c.Name, c.Kind)
		}
	}
	want := []string{"verify", "token", "accounts", "zones", "workers", "email_rules", "email_addresses"}
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
	scopes       []string
	failAccounts bool
}

func (s *cloudflareStandin) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.last, s.auth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
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
