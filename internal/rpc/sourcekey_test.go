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
// event, not a note, not what the call answers, and not the server's log,
// which is handed no request's arguments. A giving that is refused does not
// repeat what was sent either. It holds for the two calls that take one:
// KeySource, for a person nobody asked, and GiveSecret, for a secret ask.
func TestAGivenValueIsInTheFileAlone(t *testing.T) {
	const value = "s3cret-0f-the-yard"
	t.Run("KeySource", func(t *testing.T) {
		valueInTheFileAlone(t, value, func(ctx context.Context, c *Client) giving {
			return giving{
				give: func(values map[string]string) (*store.SourceKeyed, error) {
					return c.KeySource(ctx, "crm", values, "jo")
				},
				// Arguments that are no arguments of the call, the value
				// among them.
				refused: []func() error{func() error { return c.call(ctx, "KeySource", nil, "crm", value, "jo") }},
			}
		})
	})
	t.Run("GiveSecret", func(t *testing.T) {
		valueInTheFileAlone(t, value, func(ctx context.Context, c *Client) giving {
			if _, err := c.AddUser(ctx, "jo", "jo"); err != nil {
				t.Fatal(err)
			}
			if err := c.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
				t.Fatal(err)
			}
			b, err := c.CreateBead(ctx, store.NewBead{Title: "Read the CRM"}, "yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			seq, err := c.AddAsk(ctx, b.ID, store.Ask{Question: "The API key for the CRM", To: "jo",
				Context: []string{"Create one in the CRM's settings."}, Secret: &store.AskSecret{Source: "crm"}}, "yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			return giving{
				give: func(values map[string]string) (*store.SourceKeyed, error) {
					return c.GiveSecret(ctx, b.ID, seq, values, "jo", true)
				},
				refused: []func() error{
					func() error { return c.call(ctx, "GiveSecret", nil, b.ID, seq, value, "jo") },
					// What a client that does not know the field would
					// send: the value as words, with a pick, and as the
					// why of a decline.
					func() error { return c.AnswerAsk(ctx, b.ID, seq, 0, value, "jo", false) },
					func() error { return c.AnswerAsk(ctx, b.ID, seq, 1, value, "jo", false) },
					func() error { return c.DeclineAsk(ctx, b.ID, seq, value, "jo") },
					func() error {
						return c.call(ctx, "GiveSecret", nil, map[string]any{"bead": b.ID, "ask": seq, "values": value, "actor": "jo", "weighed": true})
					},
					// The value where the name of an argument goes.
					func() error {
						return c.call(ctx, "GiveSecret", nil, map[string]any{"bead": b.ID, "ask": seq, value: "CRM_TOKEN", "actor": "jo"})
					},
				},
				kept: func() any {
					notes, err := c.Notes(ctx, b.ID)
					if err != nil || len(notes) != 2 {
						t.Fatalf("the bead's notes: %d, %v: want the ask and one answer", len(notes), err)
					}
					v, err := c.Bead(ctx, b.ID)
					if err != nil {
						t.Fatal(err)
					}
					q, err := c.UserAsks(ctx, "jo", "jo")
					if err != nil {
						t.Fatal(err)
					}
					return []any{notes, v, q}
				},
			}
		})
	})
}

// giving is one way a value is given over the socket: the call, calls with
// the value that are refused, and what the yard shows of it afterwards.
type giving struct {
	give    func(values map[string]string) (*store.SourceKeyed, error)
	refused []func() error
	kept    func() any
}

func valueInTheFileAlone(t *testing.T, value string, prepare func(context.Context, *Client) giving) {
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
	g := prepare(ctx, c)
	said := []string{}
	for _, refused := range []map[string]string{
		{"CRM_TOKEN": value + "\nMORE=1"},
		{"OTHER": value},
	} {
		_, err := g.give(refused)
		if err == nil {
			t.Fatalf("%v was taken", refused)
		}
		said = append(said, err.Error())
	}
	for i, refused := range g.refused {
		err := refused()
		if err == nil {
			t.Fatalf("the refused call %d was taken", i)
		}
		said = append(said, err.Error())
	}
	keyed, err := g.give(map[string]string{"CRM_TOKEN": value})
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
	var kept any
	if g.kept != nil {
		kept = g.kept()
	}
	answered, _ := json.Marshal(map[string]any{"events": evs, "answer": keyed, "refusals": said, "kept": kept})
	closed = true
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for where, kept := range map[string][]byte{"the events, the notes, the answer and the refusals": answered, "the store's file": db, "the server's log": log.Bytes()} {
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
