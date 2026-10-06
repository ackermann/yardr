package rpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// A client of the build before this one, which the upgrade does not
// replace (the web view, a chat left open), calls the crew's methods by
// the names they had when the crew were the residents, and the depots'
// by theirs.
func TestOldMethodNamesStillAnswer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	sock := socket(t)
	serve(t, sock, Handler(st, nil).ServeHTTP)
	c := NewClient(sock)
	ctx := context.Background()

	put, err := callV[*store.CrewMember](c, ctx, "PutResident", store.CrewMember{Name: "clerk", Runner: "manual"}, "me")
	if err != nil || put.Name != "clerk" {
		t.Fatalf("PutResident = %+v, %v", put, err)
	}
	if all, err := callV[[]store.CrewMember](c, ctx, "Residents"); err != nil || len(all) != 1 || all[0].Name != "clerk" {
		t.Fatalf("Residents = %+v, %v", all, err)
	}
	if got, err := callV[*store.CrewMember](c, ctx, "GetResident", "clerk"); err != nil || got.Journal != "clerk-journal" {
		t.Fatalf("GetResident = %+v, %v", got, err)
	}
	if err := c.call(ctx, "ResidentStatus", nil, "clerk", "idle"); err != nil {
		t.Fatalf("ResidentStatus: %v", err)
	}
	if _, err := callV[*store.Prime](c, ctx, "ResidentPrime", "clerk"); err != nil {
		t.Fatalf("ResidentPrime: %v", err)
	}
	if now, err := c.Crew(ctx); err != nil || len(now) != 1 {
		t.Fatalf("Crew = %+v, %v", now, err)
	}
	// And the depots' by the names they had when a depot was a rig.
	if err := c.call(ctx, "PutRig", nil, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"); err != nil {
		t.Fatalf("PutRig: %v", err)
	}
	if all, err := callV[[]store.Depot](c, ctx, "Rigs"); err != nil || len(all) != 1 || all[0].Name != "web" {
		t.Fatalf("Rigs = %+v, %v", all, err)
	}
	if got, err := callV[*store.Depot](c, ctx, "GetRig", "web"); err != nil || got.Name != "web" {
		t.Fatalf("GetRig = %+v, %v", got, err)
	}
	if _, err := callV[[]store.DepotFlow](c, ctx, "RigFlows", "web"); err != nil {
		t.Fatalf("RigFlows: %v", err)
	}
	if gone, err := callV[*store.DepotRemoval](c, ctx, "DeleteRig", "web", "me"); err != nil || gone.Depot != "web" {
		t.Fatalf("DeleteRig = %+v, %v", gone, err)
	}
}
