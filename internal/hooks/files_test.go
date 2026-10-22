package hooks_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A delivery's file that its thread.file command failed to fetch is kept
// with the failure as its reason — and the credential the command was
// handed is taken out of it first. The record is read by every device
// (DeliveryFiles), so a command that prints its credential on failure must
// not put it in the store, as a build command's stderr is redacted.
func TestFetchRedactsCredentialFromFailureReason(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	const marker = "acme-knvf-TOKEN-38c1"
	text := `name: acme
base: https://acme.test
credential:
  env: ACME_TOKEN
hook:
  verify:
    scheme: secret
    signature: { header: X-Acme-Secret }
  bodies: unique
  normalize: [normalize]
thread:
  file: [getfile]
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	declDir := t.TempDir()
	for name, body := range map[string]string{
		"normalize": "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"type\":\"message\",\"text\":\"[photo]\",\"files\":[{\"id\":\"f-1\",\"name\":\"shed.png\",\"type\":\"photo\",\"size\":7}]}'\n",
		"getfile":   "#!/bin/sh\ncat >/dev/null\necho \"get of $ACME_TOKEN failed\" >&2\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(declDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := source.Parse("acme.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, r.home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(r.home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "acme.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(t.TempDir(), "acme.secret")
	if err := os.WriteFile(secretFile, []byte("acme-hook-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "acme.env")
	if err := os.WriteFile(env, []byte("ACME_TOKEN="+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetSource(ctx, "acme", store.SourceEnvFile, env, "me"); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/acme", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Acme-Secret", "acme-hook-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answered %d %s", resp.StatusCode, raw)
	}
	r.rc.Wait()
	got, err := r.st.DeliveryFiles(ctx, telegramSeq(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the delivery's files = %+v, want the one failure", got)
	}
	if why := got[0].NotTaken; strings.Contains(why, marker) || !strings.Contains(why, "[credential redacted]") {
		t.Fatalf("the kept reason carries the credential: %q", why)
	} else if !strings.Contains(why, "photo not fetched") {
		t.Fatalf("the kept reason says nothing of the failure: %q", why)
	}
}
