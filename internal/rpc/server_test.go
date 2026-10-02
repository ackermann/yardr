package rpc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

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
