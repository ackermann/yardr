package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A credential given over the socket is in the source's env file and in
// nothing else the server keeps or says: not the store's file, not an
// event, not what the call answers, and not the server's log, which is
// handed no request's arguments. A giving that is refused does not repeat
// what was sent either.
func TestKeySourceKeepsTheValueInTheFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			st.Close(context.Background())
		}
	})
	var log bytes.Buffer
	sock := socket(t)
	h := Handler(st, slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	serve(t, sock, h.ServeHTTP)
	c := NewClient(sock)
	ctx := context.Background()

	const decl = "name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	if _, err := c.PutSource(ctx, "crm.yaml", decl, "jo"); err != nil {
		t.Fatal(err)
	}
	const value = "s3cret-0f-the-yard"
	said := []string{}
	for _, refused := range []map[string]string{
		{"CRM_TOKEN": value + "\nMORE=1"},
		{"OTHER": value},
	} {
		_, err := c.KeySource(ctx, "crm", refused, "jo")
		if err == nil {
			t.Fatalf("%v was taken", refused)
		}
		said = append(said, err.Error())
	}
	// Arguments that are no arguments of the call, the value among them.
	if err := c.call(ctx, "KeySource", nil, "crm", value, "jo"); err == nil {
		t.Fatal("a value in the place of the names was taken")
	} else {
		said = append(said, err.Error())
	}
	keyed, err := c.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": value}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(path), "sources", "crm.env")
	if keyed.File != file || keyed.State != "CRM_TOKEN is set in "+file {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Fatalf("the env file holds %q", b)
	}

	evs, err := c.EventsSince(ctx, 0, 1000)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	answered, _ := json.Marshal(map[string]any{"events": evs, "answer": keyed, "refusals": said})
	closed = true
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for where, kept := range map[string][]byte{"the events, the answer and the refusals": answered, "the store's file": db, "the server's log": log.Bytes()} {
		if bytes.Contains(kept, []byte(value)) {
			t.Errorf("%s holds the value", where)
		}
	}
	// Nothing beside the env file either: no second file, no copy.
	left, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(left) != 1 {
		t.Fatalf("beside the env file: %v, %v", left, err)
	}
	if !strings.Contains(strings.Join(said, "\n"), "has a line break in it") {
		t.Fatalf("the refusals: %q", said)
	}
}
