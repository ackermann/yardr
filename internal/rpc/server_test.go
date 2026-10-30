package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// The tests' stores do not wait for the disk (store.NoSyncEnv). storetest
// says so for every other package's; it imports this one, so here it is
// said again.
func TestMain(m *testing.M) {
	if config.Getenv(store.NoSyncEnv) == "" {
		os.Setenv("YARDR_"+store.NoSyncEnv, "1")
	}
	os.Exit(m.Run())
}

// Only the process that owns the store records webhook deliveries or logs
// events of any kind; over the socket a client may log dispatch decisions
// and nothing else, so it cannot forge a `hook` event.
func TestSocketCannotForgeEvents(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	sock := socket(t)
	h := Handler(st, nil)
	serve(t, sock, h.ServeHTTP)
	c := NewClient(sock)
	ctx := context.Background()

	for _, m := range []string{"RecordHook", "LogEvent"} {
		err := c.call(ctx, m, nil, "linear", map[string]any{"type": "comment"}, []byte("{}"))
		if err == nil || !strings.Contains(err.Error(), "no such method "+m) {
			t.Errorf("%s over the socket: %v", m, err)
		}
	}
	if err := c.LogDispatch(ctx, "", "hook", "hook:linear", map[string]any{"source": "linear"}); err == nil {
		t.Error("LogDispatch accepted a hook event")
	}
	if err := c.LogDispatch(ctx, "", "daemon_replaced", "yard", nil); err != nil {
		t.Fatalf("dispatch event: %v", err)
	}
	evs, err := st.Events(ctx, "", 10)
	if err != nil || len(evs) != 1 || evs[0].Kind != "daemon_replaced" {
		t.Fatalf("events: %+v %v", evs, err)
	}
}

// The witness header is the user and their authentication time in seconds
// since the epoch, apart by one space; anything else refuses the call. No
// second before the epoch counts: one names no authentication time, which
// the gate refuses in its own words.
func TestParseFreshWitness(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_700_000_000, 0).UTC()
	for _, c := range []struct {
		name string
		raw  string
		want store.FreshWitness
		ok   bool
	}{
		{"user and time", "jo 1700000000", store.FreshWitness{User: "jo", AuthTime: at}, true},
		{"no time before the epoch", "jo 0", store.FreshWitness{User: "jo"}, true},
		{"before the epoch", "jo -3", store.FreshWitness{User: "jo"}, true},
		{"no user", " 1700000000", store.FreshWitness{}, false},
		{"no time", "jo", store.FreshWitness{}, false},
		{"no time after the space", "jo ", store.FreshWitness{}, false},
		{"a time that is no number", "jo now", store.FreshWitness{}, false},
		{"a second space", "jo 1700000000 x", store.FreshWitness{}, false},
		{"empty", "", store.FreshWitness{}, false},
	} {
		fw, err := parseFreshWitness(c.raw)
		if ok := err == nil; ok != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
			continue
		}
		if c.ok && fw != c.want {
			t.Errorf("%s: witness = %+v, want %+v", c.name, fw, c.want)
		}
	}
}

// A fresh sign-in crosses the socket on its header: the store's gate reads
// it back and witnesses the approval, a stale one is refused there, and a
// header that names no user and time refuses the call before it is made.
func TestSocketCarriesAFreshWitness(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.WebJWTPrivilegedSessionMaxAgeSetting, "15m", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard",
		Config: map[string]any{"kind": "claude", "args": []string{"--careful"}}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the users"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(name string) uint64 {
		t.Helper()
		seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Add " + name + "?",
			To: "jo", Context: []string{"they join the yard"},
			Run: &store.AskRun{Argv: []string{"user", "add", name}}}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	sock := socket(t)
	h := Handler(st, nil)
	serve(t, sock, h.ServeHTTP)
	c := NewClient(sock)

	seq := ask("bob")
	fresh := store.WithFreshWitness(ctx, "jo", time.Now())
	if _, err := c.ApproveAsk(fresh, b.ID, seq, "jo", true, nil); err != nil {
		t.Fatalf("approving over the socket under a fresh sign-in = %v", err)
	}
	if _, err := st.GetUser(ctx, "bob"); err != nil {
		t.Fatalf("the approval added no user: %v", err)
	}
	evs, err := st.Events(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	witnessed := false
	for _, e := range evs {
		if w, _ := e.Data["witness"].(string); w == store.WitnessFreshSignIn {
			witnessed = true
		}
	}
	if !witnessed {
		t.Fatalf("no event is witnessed a fresh sign-in: %+v", evs)
	}

	stale := ask("carol")
	if _, err := c.ApproveAsk(store.WithFreshWitness(ctx, "jo", time.Now().Add(-time.Hour)), b.ID, stale, "jo", true, nil); err == nil ||
		!strings.Contains(err.Error(), "outside the gateway's window") {
		t.Fatalf("approving over the socket under a stale sign-in = %v", err)
	}
	if _, err := st.GetUser(ctx, "carol"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused approval added carol: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/call/Head", strings.NewReader("[]"))
	req.Header.Set(FreshWitnessHeader, "bogus")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var r struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error == nil || r.Error.Message != "the call's fresh witness names no user and time" {
		t.Fatalf("a malformed witness header answers %+v", r.Error)
	}
}
