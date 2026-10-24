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

// A policy that refuses see for one bead hides it from the reads: the
// listing leaves it out, the view answers not-found, and the arrivals
// board holds no row for it. Under the core every viewer sees every bead.
func TestPolicySeeHidesABead(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	hidden, err := api.CreateBead(ctx, store.NewBead{Title: "hidden", ID: "yd-hidden", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	shown, err := api.CreateBead(ctx, store.NewBead{Title: "shown", ID: "yd-shown", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []*store.Bead{hidden, shown} {
		if _, err := api.AddAsk(ctx, b.ID, store.Ask{Question: "May this bead move?", To: "jo", Context: []string{"the viewer cannot see this bead", "the queue should omit this ask"}}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(bs []store.Bead) []string {
		var out []string
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	if bs, err := api.ListBeads(ctx, store.BeadFilter{Viewer: "jo"}); err != nil || len(bs) != 2 {
		t.Fatalf("jo's listing under the core = %v, %v, want both beads", ids(bs), err)
	}
	own.SetPolicy(policyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error {
		if act == store.ActSee && obj.Kind == store.KindBead && obj.Name == hidden.ID {
			return errors.New("hidden from " + actor)
		}
		return store.CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if bs, err := api.ListBeads(ctx, store.BeadFilter{Viewer: "jo"}); err != nil || len(bs) != 1 || bs[0].ID != shown.ID {
		t.Fatalf("jo's listing with yd-hidden refused = %v, %v, want [yd-shown]", ids(bs), err)
	}
	if bs, err := api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(bs) != 1 || bs[0].ID != shown.ID {
		t.Fatalf("the yard's own listing with yd-hidden refused = %v, %v, want [yd-shown]", ids(bs), err)
	}
	if _, err := api.Bead(ctx, hidden.ID, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Bead(yd-hidden) for jo = %v, want not-found", err)
	}
	if v, err := api.Bead(ctx, shown.ID, "jo"); err != nil || v.ID != shown.ID {
		t.Fatalf("Bead(yd-shown) for jo = %+v, %v, want the view", v, err)
	}
	board, err := api.Arrivals(ctx, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range board.Rows {
		if r.Bead == hidden.ID {
			t.Fatalf("jo's arrivals board holds a row for yd-hidden: %+v", board.Rows)
		}
	}
	found := false
	for _, r := range board.Rows {
		found = found || r.Bead == shown.ID
	}
	if !found {
		t.Fatalf("jo's arrivals board holds no row for yd-shown: %+v", board.Rows)
	}
	queue, err := api.UserAsks(ctx, "jo", "jo")
	if err != nil || len(queue.Open) != 1 || queue.Open[0].Bead != shown.ID {
		t.Fatalf("jo's asks with yd-hidden refused = %+v, %v, want only yd-shown", queue, err)
	}
	writerAsks, err := api.WriterAsks(ctx, "jo", "jo")
	if err != nil || len(writerAsks) != 1 || writerAsks[0].Bead != shown.ID {
		t.Fatalf("jo's writer asks with yd-hidden refused = %+v, %v, want only yd-shown", writerAsks, err)
	}
	flows, err := api.Flows(ctx, "jo")
	if err != nil || len(flows.Unassigned) != 1 || flows.Unassigned[0].Bead != shown.ID {
		t.Fatalf("jo's flows with yd-hidden refused = %+v, %v, want only yd-shown", flows, err)
	}
	if err := own.May(ctx, "jo", store.ActSee, store.Object{Kind: store.KindBead, Name: hidden.ID}); err == nil {
		t.Fatal("May(see yd-hidden) for jo = nil, want refused")
	}
}

// The CLI-only gates ask the policy through May: the member is refused in
// the CLI's own words, the operator passes, and a policy that allows the
// member one gate decides it.
func TestMayAsksThePolicyForTheCLIGates(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	gates := []struct {
		act store.Action
		obj store.Object
	}{
		{store.ActConnectEdge, store.Object{Kind: store.KindYard}},
		{store.ActDisconnectEdge, store.Object{Kind: store.KindYard}},
		{store.ActNameUpgradeDepot, store.Object{Kind: store.KindYard}},
		{store.ActListAllTimers, store.Object{Kind: store.KindTimer}},
		{store.ActDeleteYardHome, store.Object{Kind: store.KindYard}},
		{store.ActPair, store.Object{Kind: store.KindPeer}},
	}
	for _, g := range gates {
		want := "only the operator (jo) may " + string(g.act) + "; this is ada"
		if err := api.May(ctx, "ada", g.act, g.obj); err == nil || err.Error() != want {
			t.Errorf("May(%s) for ada = %v, want %q", g.act, err, want)
		}
		if err := api.May(ctx, "jo", g.act, g.obj); err != nil {
			t.Errorf("May(%s) for jo = %v, want nil", g.act, err)
		}
	}
	own.SetPolicy(policyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error {
		if act == store.ActConnectEdge && actor == "ada" {
			return nil
		}
		return store.CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if err := api.May(ctx, "ada", store.ActConnectEdge, store.Object{Kind: store.KindYard}); err != nil {
		t.Errorf("May(connect edge) for ada under a policy that lets her = %v", err)
	}
}

// May answers a question about an object the socket carried, which holds
// only what JSON names: a rule the real gates always call with the bead
// or the group beside it refuses the question without them, instead of
// failing on what is not there.
func TestMayRefusesAnObjectWithoutItsBead(t *testing.T) {
	t.Parallel()
	api, _ := operatorYard(t)
	ctx := context.Background()
	members := &store.Group{Name: "crew", Members: []string{"ada"}}
	for _, q := range []struct {
		act store.Action
		obj store.Object
	}{
		{store.ActMoveOut, store.Object{Group: members}},
		{store.ActAnswerStageAsk, store.Object{Group: members}},
		{store.ActSubscribeGroup, store.Object{}},
	} {
		if err := api.May(ctx, "jo", q.act, q.obj); err == nil {
			t.Errorf("May(%s) for jo without the bead or group = nil, want a refusal", q.act)
		}
	}
}
