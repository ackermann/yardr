package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

const buildCalls = `name: crm
base: BASE/api
credential:
  env: CRM_TOKEN
  update_env: CRM_UPDATE_TOKEN
  header: "Authorization: Bearer {credential}"
idempotency: Idempotency-Key
calls:
  note:
    kind: update
    params:
      id: { type: string, log: true }
      count: { type: int }
      all: { type: bool }
      mode: { type: enum, values: [roundtrip, refuse, invalid] }
      extra: { type: string, optional: true }
      defaulted: { type: bool, default: false }
    request:
      method: POST
      path: "/customers/{id}/notes"
      query: { fixed: v1 }
    build: [crm/build]
    answer: { select: [id] }
`

type buildRequest struct {
	method, uri, auth, key, contentType, body string
}

// A built call gets typed params and its update credential, makes its own
// credentialed pre-read of YARDR_SOURCE_BASE, and posts only the body it
// prints to the declared path. A refusal or a broken build posts nothing,
// records its own status, and keeps the update's key for the next attempt.
func TestSourceCallBuildRoundTripAndRefusals(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the fixture build script calls the stand-in with it")
	}
	home := t.TempDir()
	s, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var requests []buildRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, buildRequest{r.Method, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type"), string(body)})
		mu.Unlock()
		switch r.URL.Path {
		case "/api/preread":
			io.WriteString(w, `"from-standin"`)
		case "/api/customers/7/notes":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"n1","secret":"not selected"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	if _, err := s.PutSource(ctx, "crm.yaml", strings.Replace(buildCalls, "BASE", srv.URL, 1), "jo"); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "crm.env")
	if err := os.WriteFile(envFile, []byte("CRM_TOKEN=read-token\nCRM_UPDATE_TOKEN=write-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSource(ctx, "crm", store.SourceEnvFile, envFile, "jo"); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
cat > build-stdin.json
env > build-environment
if grep -q '"mode":"refuse"' build-stdin.json; then
  echo '3 matches; pass all=true to replace them all' >&2
  exit 1
fi
if grep -q '"mode":"invalid"' build-stdin.json; then
  printf 'stdout-must-not-leak'
  exit 0
fi
pre=$(curl -sS --fail -H "Authorization: Bearer $CRM_UPDATE_TOKEN" "$YARDR_SOURCE_BASE/preread")
printf '{"text":%s}' "$pre"
`
	if err := source.WriteCopies(home, "crm", []source.FileCopy{{Program: "crm/build", Data: []byte(script)}}); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", "crm"))
	t.Setenv("YARDR_SESSION", "not-for-a-build")
	t.Setenv("CRM_UPDATE_TOKEN", "not-this-one")

	res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "note", Actor: "jo", Params: map[string]string{
		"id": "7", "count": "2", "all": "true", "mode": "roundtrip",
	}})
	if err != nil || res.Status != http.StatusCreated || string(res.Answer) != `{"id":"n1"}` {
		t.Fatalf("CallSource = %+v, %v", res, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(home, "build-stdin.json"))
	if string(stdin) != `{"params":{"all":true,"count":2,"defaulted":false,"id":"7","mode":"roundtrip"}}` {
		t.Fatalf("build stdin = %s", stdin)
	}
	environment, _ := os.ReadFile(filepath.Join(home, "build-environment"))
	for _, want := range []string{"CRM_UPDATE_TOKEN=write-token", "YARDR_SOURCE_BASE=" + srv.URL + "/api"} {
		if !strings.Contains(string(environment), want+"\n") {
			t.Errorf("build environment lacks %q:\n%s", want, environment)
		}
	}
	for _, unwanted := range []string{"CRM_TOKEN=", "YARDR_SESSION=", "not-this-one"} {
		if strings.Contains(string(environment), unwanted) {
			t.Errorf("build environment contains %q:\n%s", unwanted, environment)
		}
	}

	mu.Lock()
	got := append([]buildRequest(nil), requests...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("stand-in requests = %+v", got)
	}
	if got[0].method != "GET" || got[0].uri != "/api/preread" || got[0].auth != "Bearer write-token" || got[0].key != "" || got[0].body != "" {
		t.Fatalf("pre-read = %+v", got[0])
	}
	post := got[1]
	if post.method != "POST" || post.uri != "/api/customers/7/notes?fixed=v1" || post.auth != "Bearer write-token" || len(post.key) != 36 || post.contentType != "application/json" || post.body != `{"text":"from-standin"}` {
		t.Fatalf("post = %+v", post)
	}
	if evs := calls(t, s); len(evs) != 1 {
		t.Fatalf("one outer call has %d source_call events", len(evs))
	}

	for name, tc := range map[string]struct {
		mode, reason, words string
	}{
		"refusal":      {"refuse", source.BuildRefused, "3 matches; pass all=true to replace them all"},
		"invalid JSON": {"invalid", source.BuildFailed, "what it printed is not one JSON value"},
	} {
		t.Run(name, func(t *testing.T) {
			params := map[string]string{"id": "7", "count": "2", "all": "false", "mode": tc.mode}
			for attempt := 1; attempt <= 2; attempt++ {
				_, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "note", Actor: "jo", Params: params})
				var failed *source.CallError
				if !errors.As(err, &failed) || failed.Reason != tc.reason || !strings.Contains(err.Error(), tc.words) {
					t.Fatalf("attempt %d = %v, want %s containing %q", attempt, err, tc.reason, tc.words)
				}
				if strings.Contains(err.Error(), "stdout-must-not-leak") {
					t.Fatalf("the error holds build stdout: %v", err)
				}
				_, data := lastCall(t, s)
				if !strings.Contains(data, `"status":"`+tc.reason+`"`) || strings.Contains(data, tc.words) || strings.Contains(data, "write-token") {
					t.Fatalf("event = %s", data)
				}
				if attempt == 2 && !strings.Contains(data, `"attempt":2`) {
					t.Fatalf("the update's key was not kept: %s", data)
				}
			}
		})
	}
	mu.Lock()
	left := append([]buildRequest(nil), requests...)
	mu.Unlock()
	if len(left) != 2 {
		t.Fatalf("a refused or broken build sent a request: %+v", left)
	}
	all, _ := json.Marshal(calls(t, s))
	for _, unwanted := range []string{"write-token", "read-token", "from-standin", "3 matches", "stdout-must-not-leak"} {
		if strings.Contains(string(all), unwanted) {
			t.Fatalf("events contain %q: %s", unwanted, all)
		}
	}
}
