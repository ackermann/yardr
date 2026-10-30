package rpc

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Every method the socket serves has a class in Access, and Access names
// none it does not serve: a method added to store.API cannot ship without
// someone saying who may call it.
func TestAccessClassesEveryServedMethod(t *testing.T) {
	t.Parallel()
	served := newAPI(nil, StreamWriteTimeout).methods
	// Arrivals and Flows are served on the socket but computed over the
	// store API, not in it.
	for _, name := range []string{"Arrivals", "Flows"} {
		if _, ok := served[name]; !ok {
			t.Errorf("%s is not served on the socket", name)
		}
	}
	if n := reflect.TypeOf((*store.API)(nil)).Elem().NumMethod(); len(served) != n-len(notCallable)+2 {
		t.Fatalf("the socket serves %d methods, store.API has %d less %d not callable plus 2 computed", len(served), n, len(notCallable))
	}
	for name := range served {
		if Access[name] == Unclassed {
			t.Errorf("%s is served on the socket with no class in Access: say who may call it", name)
		}
	}
	for name := range Access {
		if _, ok := served[name]; !ok {
			t.Errorf("Access classes %s, which the socket does not serve", name)
		}
	}
	for _, name := range []string{"SessionScreen", "Teammates", "TelemetryShow", "TelemetrySet", "DevicePairState"} {
		if got := Access[name]; got != Operator {
			t.Errorf("%s has class %v, want operator", name, got)
		}
	}
}

// Who each class lets through, by the kind of caller: a bead's pane only
// to what is read or marked for agents, a crew member's pane to all but
// the operator's and the daemon's, and the daemon's calls to the daemon.
func TestClassAllows(t *testing.T) {
	t.Parallel()
	kinds := []store.CallerKind{store.CallerOwner, store.CallerUnknown, store.CallerPerson, store.CallerBead, store.CallerCrew, store.CallerDaemon, store.CallerScript}
	want := map[Class][]bool{
		// owner, unknown, person, bead, crew, daemon, script
		Unclassed:        {true, false, false, false, false, false, false},
		Read:             {true, true, true, true, true, true, true},
		Agent:            {true, false, true, true, true, true, true},
		Admin:            {true, false, true, false, true, true, true},
		Operator:         {true, false, true, false, false, true, true},
		Daemon:           {true, false, false, false, false, true, false},
		DaemonOrOperator: {true, false, true, false, false, true, true},
	}
	for c, row := range want {
		for i, k := range kinds {
			if got := c.allows(k); got != row[i] {
				t.Errorf("class %v allows %s = %v, want %v", c, k, got, row[i])
			}
		}
	}
}

func TestSocketPeerWithoutCredentialsCanOnlyRead(t *testing.T) {
	t.Parallel()

	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx := ConnContext(context.Background(), conn)
	if kind, err := st.CallerOf(ctx); err != nil || kind != store.CallerUnknown {
		t.Fatalf("caller = %v, %v: want an unidentified socket peer", kind, err)
	}
	if err := allowed(ctx, st, "Groups"); err != nil {
		t.Errorf("read call from an unidentified peer: %v", err)
	}
	for _, method := range []string{"AddNote", "PutGroup", "Claim", "Backup", "SessionScreen", "TelemetryShow", "TelemetrySet"} {
		if err := allowed(ctx, st, method); err == nil {
			t.Errorf("%s from an unidentified peer was allowed", method)
		}
	}
}
