package rpc

import (
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
	if n := reflect.TypeOf((*store.API)(nil)).Elem().NumMethod(); len(served) != n-len(notCallable) {
		t.Fatalf("the socket serves %d methods, store.API has %d less %d not callable", len(served), n, len(notCallable))
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
}

// Who each class lets through, by the kind of caller: a bead's pane only
// to what is read or marked for agents, a crew member's pane to all but
// the operator's and the daemon's, and the daemon's calls to the daemon.
func TestClassAllows(t *testing.T) {
	t.Parallel()
	kinds := []store.CallerKind{store.CallerOwner, store.CallerPerson, store.CallerBead, store.CallerCrew, store.CallerDaemon}
	want := map[Class][]bool{
		// owner, person, bead, crew, daemon
		Unclassed:        {true, false, false, false, false},
		Read:             {true, true, true, true, true},
		Agent:            {true, true, true, true, true},
		Admin:            {true, true, false, true, true},
		Operator:         {true, true, false, false, true},
		Daemon:           {true, false, false, false, true},
		DaemonOrOperator: {true, true, false, false, true},
	}
	for c, row := range want {
		for i, k := range kinds {
			if got := c.allows(k); got != row[i] {
				t.Errorf("class %s allows %s = %v, want %v", c, k, got, row[i])
			}
		}
	}
}
