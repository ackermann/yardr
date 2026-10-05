package store_test

import (
	"context"
	"errors"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// resident creates a resident with a linear identity (id "<name>-id", key
// "<name>"; none when noID) and the given subscriptions.
func resident(t *testing.T, s store.API, name string, noID bool, subs ...store.Subscription) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.PutResident(ctx, store.Resident{Name: name, Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if !noID {
		if err := s.SetIdentity(ctx, name, "linear", store.Identity{ID: name + "-id", Key: name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range subs {
		if err := s.AddSubscription(ctx, name, sub, "me"); err != nil {
			t.Fatal(err)
		}
	}
}

// record records e as the receiver does: on the store itself, since
// RecordHook is not on the API.
func record(t *testing.T, own *store.Store, e hooks.Envelope) uint64 {
	t.Helper()
	seq, dup, err := own.RecordHook(context.Background(), "linear", e.Map(), []byte("{}"), nil)
	if err != nil || dup {
		t.Fatalf("record: %v (duplicate %v)", err, dup)
	}
	return seq
}

// deliveries records e as a hook delivery and returns who it reached, why,
// reading the event back through s.
func deliveries(t *testing.T, s store.API, own *store.Store, e hooks.Envelope) map[string]string {
	t.Helper()
	seq := record(t, own, e)
	evs, err := s.EventsSince(context.Background(), seq-1, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("event %d: %v %v", seq, evs, err)
	}
	out := map[string]string{}
	for _, r := range []string{"all", "noid", "ment", "inv", "noid-inv"} {
		if why := store.Reason(evs[0], r); why != "" {
			out[r] = why
		}
	}
	return out
}

func same(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestHookScopes(t *testing.T) {
	s, own := storetest.NewOwned(t)
	sub := func(scope string) store.Subscription { return store.Subscription{Source: "linear", Scope: scope} }
	resident(t, s, "all", false, sub(store.ScopeAll))
	resident(t, s, "noid", true, sub(store.ScopeAll))
	resident(t, s, "ment", false, sub(store.ScopeMentions))
	resident(t, s, "inv", false, sub(store.ScopeInvolved))
	resident(t, s, "noid-inv", true, sub(store.ScopeInvolved), sub(store.ScopeMentions))
	jane := hooks.Party{ID: "jane-id", Name: "Jane"}

	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"nobody named", hooks.Envelope{Source: "linear", Type: "comment", Actor: jane},
			map[string]string{"all": "all", "noid": "all"}},
		{"mentioned by key and by id", hooks.Envelope{Source: "linear", Type: "comment", Actor: jane,
			Mentions: []string{"ment", "inv-id", "all", "noid-inv"}},
			map[string]string{"all": "mentioned", "noid": "all", "ment": "mentioned", "inv": "mentioned"}},
		{"keys match in any case, ids exactly", hooks.Envelope{Source: "linear", Type: "comment", Actor: jane,
			Mentions: []string{"MENT", "INV-ID"}},
			map[string]string{"all": "all", "noid": "all", "ment": "mentioned"}},
		{"assigned", hooks.Envelope{Source: "linear", Type: "issue", Actor: jane, Assignee: "inv-id"},
			map[string]string{"all": "all", "noid": "all", "inv": "assigned"}},
		{"mentions scope ignores assignment", hooks.Envelope{Source: "linear", Type: "issue", Actor: jane,
			Assignee: "ment-id"},
			map[string]string{"all": "all", "noid": "all"}},
		{"delegated counts as assigned", hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
			Actor: jane, Assignee: "jane-id", Delegate: "inv-id"},
			map[string]string{"all": "all", "noid": "all", "inv": "assigned"}},
		{"delegated to someone else", hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
			Actor: jane, Assignee: "jane-id", Delegate: "other-id"},
			map[string]string{"all": "all", "noid": "all"}},
		{"a delegate set by an update", hooks.Envelope{Source: "linear", Type: "issue", Action: "update",
			Actor: jane, Assignee: "jane-id", Delegate: "inv-id", Text: "changed delegateId"},
			map[string]string{"all": "all", "noid": "all", "inv": "assigned"}},
		{"mentions scope ignores delegation", hooks.Envelope{Source: "linear", Type: "issue", Actor: jane,
			Delegate: "ment-id"},
			map[string]string{"all": "all", "noid": "all"}},
		{"its own delegation is not delivered", hooks.Envelope{Source: "linear", Type: "issue",
			Actor: hooks.Party{ID: "inv-id"}, Delegate: "inv-id"},
			map[string]string{"all": "all", "noid": "all"}},
		{"assignment needs the id, not the key", hooks.Envelope{Source: "linear", Type: "issue", Actor: jane,
			Assignee: ""},
			map[string]string{"all": "all", "noid": "all"}},
		{"own actions are never delivered", hooks.Envelope{Source: "linear", Type: "comment",
			Actor: hooks.Party{ID: "inv-id"}, Mentions: []string{"inv", "all"}, Assignee: "inv-id"},
			map[string]string{"all": "mentioned", "noid": "all"}},
		{"self-filter on scope all", hooks.Envelope{Source: "linear", Type: "comment",
			Actor: hooks.Party{ID: "all-id"}},
			map[string]string{"noid": "all"}},
		{"other source", hooks.Envelope{Source: "token", Type: "message", Actor: jane, Mentions: []string{"ment"}},
			map[string]string{}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHookWhere(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	resident(t, s, "all", false, store.Subscription{Source: "linear", Scope: store.ScopeAll,
		Where: map[string]string{"type": "comment", "team": "ENG"}})
	resident(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved,
		Where: map[string]string{"label": "Bug", "action": "create"}})
	for _, bad := range []map[string]string{{"state": "Todo"}, {"team": ""}} {
		err := s.AddSubscription(ctx, "all", store.Subscription{Source: "linear", Scope: "all", Where: bad}, "me")
		if err == nil {
			t.Errorf("accepted --where %v", bad)
		}
	}
	eng := map[string][]string{"team": {"ENG", "Engineering"}, "label": {"Feature", "Bug"}}
	ops := map[string][]string{"team": {"OPS", "Operations"}}

	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"comment on ENG", hooks.Envelope{Source: "linear", Type: "comment", Action: "create", Fields: eng},
			map[string]string{"all": "all"}},
		{"team by name", hooks.Envelope{Source: "linear", Type: "comment", Fields: map[string][]string{"team": {"Engineering"}}},
			map[string]string{}},
		{"comment on OPS", hooks.Envelope{Source: "linear", Type: "comment", Fields: ops},
			map[string]string{}},
		{"issue on ENG", hooks.Envelope{Source: "linear", Type: "issue", Fields: eng},
			map[string]string{}},
		{"no fields", hooks.Envelope{Source: "linear", Type: "comment"},
			map[string]string{}},
		{"involved, any label matches", hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
			Assignee: "inv-id", Fields: eng},
			map[string]string{"inv": "assigned"}},
		{"involved, other action", hooks.Envelope{Source: "linear", Type: "issue", Action: "update",
			Assignee: "inv-id", Fields: eng},
			map[string]string{}},
		{"involved, mentioned but no label", hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
			Mentions: []string{"inv"}, Fields: ops},
			map[string]string{}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// github subscriptions filter on the fields its adapter sets, state among
// them; other hook sources still refuse those keys, and yardr still takes
// only state=done.
func TestGitHubWhereKeys(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	resident(t, s, "all", false, store.Subscription{Source: "github", Scope: store.ScopeAll,
		Where: map[string]string{"epic": "yard-sv6b", "state": "approved"}})
	for _, bad := range []store.Subscription{
		{Source: "linear", Scope: "all", Where: map[string]string{"epic": "yard-sv6b"}},
		{Source: "github", Scope: "all", Where: map[string]string{"stage": "review"}},
		{Source: store.SourceYardr, Scope: "all", Where: map[string]string{"state": "approved"}},
	} {
		if err := s.AddSubscription(ctx, "all", bad, "me"); err == nil {
			t.Errorf("accepted %s --where %v", bad.Source, bad.Where)
		}
	}
	review := func(epic, state string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "pull_request_review", Action: "submitted",
			Fields: map[string][]string{"epic": {epic}, "state": {state}}}
	}
	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"approved on the epic", review("yard-sv6b", "approved"), map[string]string{"all": "all"}},
		{"changes requested", review("yard-sv6b", "changes_requested"), map[string]string{}},
		{"other epic", review("yard-xxxx", "approved"), map[string]string{}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHookDeliveryInInbox(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	// "hook" in On must not widen delivery past the subscriptions.
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", On: []string{"hook"}}, "me"); err != nil {
		t.Fatal(err)
	}
	resident(t, s, "other", false, store.Subscription{Source: "linear", Scope: store.ScopeAll})
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{ID: "mayr-id", Key: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "mayor", store.Subscription{Source: "linear", Scope: store.ScopeMentions}, "me"); err != nil {
		t.Fatal(err)
	}
	record(t, own, hooks.Envelope{Source: "linear", Type: "comment", Text: "unrelated"})
	seq := record(t, own, hooks.Envelope{Source: "linear", Type: "comment",
		Mentions: []string{"mayor"}, Text: "can you look?"})
	page, err := s.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 1 || page.Events[0].Seq != seq || page.Last != seq {
		t.Fatalf("inbox: %+v %v", page, err)
	}
	if why := store.Reason(page.Events[0], "mayor"); why != store.ReasonMentioned {
		t.Fatalf("reason %q", why)
	}
	if page, _ := s.Inbox(ctx, "other", 0); page.Total != 2 {
		t.Fatalf("other's inbox: %+v", page)
	}

	// A subscription applies from when it is made: earlier deliveries were
	// decided at ingest.
	if err := s.AddSubscription(ctx, "mayor", store.Subscription{Source: "linear", Scope: store.ScopeAll}, "me"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "mayor", 0); page.Total != 1 {
		t.Fatalf("subscription applied to history: %+v", page)
	}
}

func TestIdentitiesAndSubscriptions(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mayor(t, s)
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{ID: "u1"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{ID: "u2", Key: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "ghost", "linear", store.Identity{ID: "u"}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident: %v", err)
	}
	all := store.Subscription{Source: "linear", Scope: store.ScopeAll}
	eng := store.Subscription{Source: "linear", Scope: store.ScopeAll, Where: map[string]string{"team": "ENG"}}
	for _, sub := range []store.Subscription{all, all, eng, {Source: "token", Scope: store.ScopeMentions}} {
		if err := s.AddSubscription(ctx, "mayor", sub, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []store.Subscription{{Source: "linear", Scope: "some"}, {Scope: "all"}} {
		if err := s.AddSubscription(ctx, "mayor", bad, "me"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// Redefining the resident keeps who it is and what it hears.
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetResident(ctx, "mayor")
	if r.Identities["linear"] != (store.Identity{ID: "u2", Key: "mayor"}) || len(r.Subscriptions) != 3 {
		t.Fatalf("after redefine: %+v %+v", r.Identities, r.Subscriptions)
	}

	if n, err := s.RemoveSubscriptions(ctx, "mayor", "linear", "", map[string]string{"team": "ENG"}, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe where: %d %v", n, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "mayor", "linear", "mentions", nil, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsubscribe other scope: %d %v", n, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "mayor", "linear", "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe source: %d %v", n, err)
	}
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	r, _ = s.GetResident(ctx, "mayor")
	if len(r.Identities) != 0 || len(r.Subscriptions) != 1 || r.Subscriptions[0].Source != "token" {
		t.Fatalf("after removal: %+v %+v", r.Identities, r.Subscriptions)
	}
}
