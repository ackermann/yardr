package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
)

// policyFunc is a Policy of one function.
type policyFunc func(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error

func (f policyFunc) May(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error {
	return f(ctx, tx, actor, act, obj)
}

// A policy set on the store decides every gate in place of the core: one
// that refuses everything refuses the operator, and is asked with the
// action and the object of the gate; nil puts the core back.
func TestPolicyRefusingEverything(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	no := errors.New("the policy refuses everything")
	type asked struct {
		actor string
		act   store.Action
		obj   store.Object
	}
	// The server's goroutine asks it over the socket leg (YARDR_TEST_RPC).
	var mu sync.Mutex
	var got []asked
	own.SetPolicy(policyFunc(func(_ context.Context, _ *bolt.Tx, actor string, act store.Action, obj store.Object) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, asked{actor, act, obj})
		return no
	}))
	if _, err := api.AddUser(ctx, "kim", "jo"); err == nil || err.Error() != no.Error() {
		t.Fatalf("a user add by the operator under a policy that refuses everything = %v", err)
	}
	mu.Lock()
	first := append([]asked(nil), got...)
	mu.Unlock()
	if len(first) == 0 || first[0].actor != "jo" || first[0].act != store.ActAddUser || first[0].obj.Kind != store.KindUser || first[0].obj.Name != "kim" {
		t.Fatalf("the policy was asked %+v, want jo, %q, a user kim", first, store.ActAddUser)
	}
	if err := api.DeleteRoute(ctx, "r1", "jo"); err == nil || err.Error() != no.Error() {
		t.Fatalf("a route removal by the operator under a policy that refuses everything = %v", err)
	}
	if _, err := api.UserInbox(ctx, "ada", 0, "ada"); err == nil || err.Error() != no.Error() {
		t.Fatalf("ada's own inbox under a policy that refuses everything = %v", err)
	}
	if _, err := api.SetUser(ctx, "ada", map[string]string{store.UserRole: store.RoleOperator}, "jo"); err == nil || err.Error() != no.Error() {
		t.Fatalf("a role change under a policy that refuses everything = %v", err)
	}
	own.SetPolicy(nil)
	if _, err := api.AddUser(ctx, "kim", "jo"); err != nil {
		t.Fatalf("a user add by the operator with the core back: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, call := range got {
		if call.act == store.ActChangeRole && call.obj.Kind == store.KindUser && call.obj.Name == "ada" {
			return
		}
	}
	t.Fatal("the policy was not asked to change ada's role with her user object")
}

// A policy may let a member do what the core refuses them, and hand
// everything else to CorePolicy, which refuses in its own words.
func TestPolicyAllowingAMember(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	const refused = "only the operator (jo) may add a user; this is ada"
	if _, err := api.AddUser(ctx, "kim", "ada"); err == nil || err.Error() != refused {
		t.Fatalf("a user add by the member ada under the core = %v, want %q", err, refused)
	}
	own.SetPolicy(policyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error {
		if act == store.ActAddUser && actor == "ada" {
			return nil
		}
		return store.CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := api.AddUser(ctx, "kim", "ada"); err != nil {
		t.Fatalf("a user add by the member ada under a policy that lets her: %v", err)
	}
	if err := api.DeleteUser(ctx, "kim", "ada"); err == nil || err.Error() != "only the operator (jo) may remove a user; this is ada" {
		t.Fatalf("a user removal by the member ada, left to the core = %v", err)
	}
}
