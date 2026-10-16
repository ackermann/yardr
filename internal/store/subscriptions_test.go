package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// crew member creates a crew member with a linear identity (id "<name>-id", key
// "<name>"; none when noID) and the given subscriptions.
func member(t *testing.T, s store.API, name string, noID bool, subs ...store.Subscription) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if !noID {
		if err := s.SetIdentity(ctx, name, "linear", store.Identity{ID: name + "-id", Key: name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range subs {
		if err := s.AddSubscription(ctx, name, sub, name); err != nil {
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
	t.Parallel()
	s, own := storetest.NewOwned(t)
	sub := func(scope string) store.Subscription { return store.Subscription{Source: "linear", Scope: scope} }
	member(t, s, "all", false, sub(store.ScopeAll))
	member(t, s, "noid", true, sub(store.ScopeAll))
	member(t, s, "ment", false, sub(store.ScopeMentions))
	member(t, s, "inv", false, sub(store.ScopeInvolved))
	member(t, s, "noid-inv", true, sub(store.ScopeInvolved), sub(store.ScopeMentions))
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
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	member(t, s, "all", false, store.Subscription{Source: "linear", Scope: store.ScopeAll,
		Where: map[string]string{"type": "comment", "team": "ENG"}})
	member(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved,
		Where: map[string]string{"label": "Bug", "action": "create"}})
	for _, bad := range []map[string]string{{"state": "Todo"}, {"team": ""}} {
		err := s.AddSubscription(ctx, "all", store.Subscription{Source: "linear", Scope: "all", Where: bad}, "all")
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

// github subscriptions filter on the fields its hook.keys names, state
// among them; other hook sources still refuse those keys, and yardr still
// takes only state=done.
func TestGitHubWhereKeys(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	declareGitHubKeys(t, s, "me")
	ctx := context.Background()
	member(t, s, "all", false, store.Subscription{Source: "github", Scope: store.ScopeAll,
		Where: map[string]string{"train": store.JoinWhere([]string{"yard-other", "yard-sv6b"}), "state": "approved"}})
	for _, bad := range []store.Subscription{
		{Source: "linear", Scope: "all", Where: map[string]string{"train": "yard-sv6b"}},
		{Source: "github", Scope: "all", Where: map[string]string{"stage": "review"}},
		{Source: store.SourceYardr, Scope: "all", Where: map[string]string{"state": "approved"}},
	} {
		if err := s.AddSubscription(ctx, "all", bad, "all"); err == nil {
			t.Errorf("accepted %s --where %v", bad.Source, bad.Where)
		}
	}
	review := func(train, state string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "pull_request_review", Action: "submitted",
			Fields: map[string][]string{"train": {train}, "state": {state}}}
	}
	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"approved on the train", review("yard-sv6b", "approved"), map[string]string{"all": "all"}},
		{"approved on another listed train", review("yard-other", "approved"), map[string]string{"all": "all"}},
		{"changes requested", review("yard-sv6b", "changes_requested"), map[string]string{}},
		{"other train", review("yard-xxxx", "approved"), map[string]string{}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// head_repo is a github where key like the other fields its hook.keys
// names: it is what leaves out a fork's pull request, whose head branch may
// have the name of the yard's own.
func TestGitHubWhereHeadRepo(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	declareGitHubKeys(t, s, "me")
	member(t, s, "all", false, store.Subscription{Source: "github", Scope: store.ScopeAll,
		Where: map[string]string{"head_repo": "acme/webapp"}})
	if err := s.AddSubscription(context.Background(), "all", store.Subscription{Source: "linear", Scope: "all",
		Where: map[string]string{"head_repo": "acme/webapp"}}, "all"); err == nil {
		t.Error("accepted linear --where head_repo")
	}
	opened := func(headRepo string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "pull_request", Action: "opened",
			Fields: map[string][]string{"repo": {"acme/webapp"}, "head_repo": {headRepo},
				"head_branch": {"yard/yard-sv6b"}, "train": {"yard-sv6b"}}}
	}
	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"the yard's own", opened("acme/webapp"), map[string]string{"all": "all"}},
		{"a fork's, same head branch", opened("mallory/webapp"), map[string]string{}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHookDeliveryInInbox(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	// "hook" in On must not widen delivery past the subscriptions.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", On: []string{"hook"}}, "me"); err != nil {
		t.Fatal(err)
	}
	member(t, s, "other", false, store.Subscription{Source: "linear", Scope: store.ScopeAll})
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "mayr-id", Key: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "linear", Scope: store.ScopeMentions}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	record(t, own, hooks.Envelope{Source: "linear", Type: "comment", Text: "unrelated"})
	seq := record(t, own, hooks.Envelope{Source: "linear", Type: "comment",
		Mentions: []string{"yardmaster"}, Text: "can you look?"})
	page, err := s.Inbox(ctx, "yardmaster", 0)
	if err != nil || page.Total != 1 || page.Events[0].Seq != seq || page.Last != seq {
		t.Fatalf("inbox: %+v %v", page, err)
	}
	if why := store.Reason(page.Events[0], "yardmaster"); why != store.ReasonMentioned {
		t.Fatalf("reason %q", why)
	}
	if page, _ := s.Inbox(ctx, "other", 0); page.Total != 2 {
		t.Fatalf("other's inbox: %+v", page)
	}

	// A subscription applies from when it is made: earlier deliveries were
	// decided at ingest.
	if err := s.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "linear", Scope: store.ScopeAll}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "yardmaster", 0); page.Total != 1 {
		t.Fatalf("subscription applied to history: %+v", page)
	}
}

// A crew member that speaks as a GitHub App has the bot's login as its key:
// yardr[bot]. Its own comments, whose actor is that login, stay out of its
// inbox, and a mention of the App, which people write as @yardr, reaches
// it. The account named yardr is not the bot: what it does is delivered.
func TestHookDeliveryToAGitHubApp(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "ment"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "ment", "github", store.Identity{Key: "yardr[bot]"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "ment", store.Subscription{Source: "github", Scope: store.ScopeAll}, "ment"); err != nil {
		t.Fatal(err)
	}
	comment := func(actor string, mentions ...string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "issue_comment", Actor: hooks.Party{ID: actor, Name: actor},
			Thread: hooks.Thread{ID: "acme/webapp#7"}, Mentions: mentions, Text: "words"}
	}
	for _, c := range []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"its own comment", comment("yardr[bot]"), map[string]string{}},
		{"its own comment, as GitHub cases the login", comment("Yardr[bot]", "yardr"), map[string]string{}},
		{"mentioned by its slug", comment("jane", "yardr"), map[string]string{"ment": store.ReasonMentioned}},
		// Mentioned once, it takes part in the thread from then on.
		{"another name", comment("jane", "yardr-dev"), map[string]string{"ment": store.ReasonParticipant}},
		{"the account of the slug's name acts", comment("yardr"), map[string]string{"ment": store.ReasonParticipant}},
	} {
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIdentitiesAndSubscriptions(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u1"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u2", Key: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "ghost", "linear", store.Identity{ID: "u"}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown crew member: %v", err)
	}
	all := store.Subscription{Source: "linear", Scope: store.ScopeAll}
	eng := store.Subscription{Source: "linear", Scope: store.ScopeAll, Where: map[string]string{"team": "ENG"}}
	for _, sub := range []store.Subscription{all, all, eng, {Source: "token", Scope: store.ScopeMentions}} {
		if err := s.AddSubscription(ctx, "yardmaster", sub, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []store.Subscription{{Source: "linear", Scope: "some"}, {Scope: "all"}} {
		if err := s.AddSubscription(ctx, "yardmaster", bad, "yardmaster"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// Redefining the crew member keeps who it is and what it hears.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.Identities["linear"] != (store.Identity{ID: "u2", Key: "yardmaster"}) || len(r.Subscriptions) != 3 {
		t.Fatalf("after redefine: %+v %+v", r.Identities, r.Subscriptions)
	}

	if n, err := s.RemoveSubscriptions(ctx, "yardmaster", "linear", "", map[string]string{"team": "ENG"}, "yardmaster"); err != nil || n != 1 {
		t.Fatalf("unsubscribe where: %d %v", n, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "yardmaster", "linear", "mentions", nil, "yardmaster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsubscribe other scope: %d %v", n, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "yardmaster", "linear", "", nil, "yardmaster"); err != nil || n != 1 {
		t.Fatalf("unsubscribe source: %d %v", n, err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	r, _ = s.GetCrew(ctx, "yardmaster")
	if len(r.Identities) != 0 || len(r.Subscriptions) != 1 || r.Subscriptions[0].Source != "token" {
		t.Fatalf("after removal: %+v %+v", r.Identities, r.Subscriptions)
	}
}

// A user has an identity in a source as a crew member has, set and
// removed by the same call: the name says which of the two it is.
func TestUserIdentity(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear", store.Identity{ID: "u-jo", Key: "jo"}, "jo"); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUser(ctx, "jo")
	if err != nil || u.Identities["linear"] != (store.Identity{ID: "u-jo", Key: "jo"}) {
		t.Fatalf("identities = %+v, %v", u.Identities, err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear", store.Identity{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if u, err = s.GetUser(ctx, "jo"); err != nil || len(u.Identities) != 0 {
		t.Fatalf("after removal: %+v, %v", u.Identities, err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear", store.Identity{}, "jo"); !errors.Is(err, store.ErrNotFound) ||
		!strings.Contains(err.Error(), "user jo has no linear identity") {
		t.Fatalf("removing none: %v", err)
	}
	// Who a user is there is theirs and the operator's to say: a thread
	// assigned to them goes to whom it names.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	whose := "who jo is in a source is said by jo: a thread assigned to them goes there; "
	for actor, want := range map[string]string{"yardmaster": whose + "yardmaster is a crew member", "ada": whose + "this is ada", "": whose + "this is nobody"} {
		if err := s.SetIdentity(ctx, "jo", "linear", store.Identity{ID: "u-other"}, actor); err == nil || err.Error() != want {
			t.Fatalf("%q sets jo's identity: %v, want %q", actor, err, want)
		}
	}
	if u, err = s.GetUser(ctx, "jo"); err != nil || len(u.Identities) != 0 {
		t.Fatalf("after refusals: %+v, %v", u.Identities, err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear", store.Identity{ID: "u-jo"}, "ada"); err != nil {
		t.Fatalf("the operator sets jo's identity: %v", err)
	}
	if err := s.SetIdentity(ctx, "nobody", "linear", store.Identity{ID: "x"}, "me"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (ada) may say who a crew member is in a source") {
		t.Fatalf("a name that is no party as a user: %v", err)
	}
	if err := s.SetIdentity(ctx, "nobody", "linear", store.Identity{ID: "x"}, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a name that is no party: %v", err)
	}
}

// A manual group and a crew member may share a name. Unsubscribing such a
// name removes the matching subscriptions from whichever of the two has
// them; when both do, it refuses naming both (yardr-f0ez).
func TestUnsubscribeSameNameGroupAndCrew(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	// The shared names' subscriptions are the operator's to change here,
	// as the groups have no members: the test is the shared-name
	// mechanics, not the gates.
	if err := s.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	sub := store.Subscription{Source: "acme", Scope: store.ScopeAll}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	// The group's subscription, made before the crew member exists.
	if err := s.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "reviewers"}, "me"); err != nil {
		t.Fatal(err)
	}
	// Only the group has a matching subscription: it is removed there.
	if n, err := s.RemoveSubscriptions(ctx, "reviewers", "acme", "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe the group's only: %d, %v", n, err)
	}
	if g, _ := s.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions after unsubscribe = %v", g.Subscriptions)
	}
	// Subscribing the shared name still targets the crew member.
	if err := s.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetCrew(ctx, "reviewers"); len(r.Subscriptions) != 1 {
		t.Fatalf("crew's subscriptions after subscribe = %v", r.Subscriptions)
	}
	if g, _ := s.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions after subscribe = %v", g.Subscriptions)
	}
	// Only the crew member has a matching subscription: it is removed there.
	if n, err := s.RemoveSubscriptions(ctx, "reviewers", "acme", "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe the crew member's only: %d, %v", n, err)
	}
	// Both have a matching subscription: refused, naming both. The group's
	// is made before the crew member exists, the crew member's after.
	if err := s.PutGroup(ctx, store.Group{Name: "both", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "both", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "both"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "both", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "both", "acme", "", nil, "me"); err == nil ||
		!strings.Contains(err.Error(), "both") || !strings.Contains(err.Error(), "--group") {
		t.Fatalf("unsubscribe both = %d, %v; want a refusal naming both and --group", n, err)
	}
	// Stating the kind removes from that one alone.
	if n, err := s.RemoveSubscriptionsKind(ctx, "both", "group", "acme", "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe both as group = %d, %v", n, err)
	}
	if g, _ := s.GetGroup(ctx, "both"); len(g.Subscriptions) != 0 {
		t.Fatalf("group both after kind unsubscribe = %v", g.Subscriptions)
	}
	if r, _ := s.GetCrew(ctx, "both"); len(r.Subscriptions) != 1 {
		t.Fatalf("crew both after group unsubscribe = %v", r.Subscriptions)
	}
	if n, err := s.RemoveSubscriptionsKind(ctx, "both", "crew", "acme", "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe both as crew = %d, %v", n, err)
	}
	// Subscribing the group under a shared name takes the kind.
	if err := s.AddSubscriptionKind(ctx, "reviewers", "group", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 1 {
		t.Fatalf("group reviewers after kind subscribe = %v", g.Subscriptions)
	}
	if err := s.AddSubscriptionKind(ctx, "reviewers", "user", sub, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("subscribe reviewers as user = %v, want not found", err)
	}
}

// A failed removal on either side of a shared name must not be hidden by a
// no-match on the other side or by removing that other side.
func TestUnsubscribeSharedNamePropagatesRemovalErrors(t *testing.T) {
	t.Parallel()
	blocked := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.InstallFailed}}
	allowed := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.KindAttention}}

	for _, tc := range []struct {
		name  string
		crew  []store.Subscription
		group []store.Subscription
	}{
		{name: "group guard is not hidden by crew no-match", group: []store.Subscription{blocked}},
		{name: "crew guard does not allow group removal", crew: []store.Subscription{blocked}, group: []store.Subscription{allowed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.New(t)
			ctx := context.Background()
			operatorWithNotify(t, st, "jo")
			if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Base: "main"}, "jo"); err != nil {
				t.Fatal(err)
			}
			if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
				t.Fatal(err)
			}
			if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "jo"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.PutCrew(ctx, store.CrewMember{Name: "reviewers", Runner: "herdr"}, "jo"); err != nil {
				t.Fatal(err)
			}
			for _, sub := range tc.crew {
				if err := st.AddSubscriptionKind(ctx, "reviewers", "crew", sub, "jo"); err != nil {
					t.Fatal(err)
				}
			}
			for _, sub := range tc.group {
				if err := st.AddSubscriptionKind(ctx, "reviewers", "group", sub, "jo"); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := st.RemoveSubscriptions(ctx, "reviewers", store.SourceYardr, "", nil, "jo"); err == nil ||
				!strings.Contains(err.Error(), store.UpgradeFromSetting) {
				t.Fatalf("unsubscribe shared name = %v; want the %s guard", err, store.UpgradeFromSetting)
			}
			r, err := st.GetCrew(ctx, "reviewers")
			if err != nil || len(r.Subscriptions) != len(tc.crew) {
				t.Fatalf("crew subscriptions = %v, %v; want %v", r, err, tc.crew)
			}
			g, err := st.GetGroup(ctx, "reviewers")
			if err != nil || len(g.Subscriptions) != len(tc.group) {
				t.Fatalf("group subscriptions = %v, %v; want %v", g, err, tc.group)
			}
		})
	}
}

// What a crew member is told is its own to decide, or the operator's for
// it: another crew member, a user, a bead's agent or nobody is refused,
// and the refusal logs no event. Under a name a crew member and a manual
// group share, the side a change lands on is gated as that side alone
// would be. From a pane the change is checked and recorded as the pane's
// actor, whatever --actor says.
func TestCrewSubscriptionIsTheMembersOrTheOperators(t *testing.T) {
	t.Parallel()
	api, _ := operatorYard(t)
	ctx := context.Background()
	sub := store.Subscription{Source: "linear", Scope: store.ScopeInvolved}
	const want = "only yardmaster or the operator (jo) changes what yardmaster is told"
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"clerk", "ada", "yard-10-new", ""} {
		this := actor
		if this == "" {
			this = "nobody"
		}
		if err := api.AddSubscription(ctx, "yardmaster", sub, actor); err == nil || err.Error() != want+"; this is "+this {
			t.Errorf("subscribe as %q = %v, want %q", actor, err, want+"; this is "+this)
		}
		if _, err := api.RemoveSubscriptions(ctx, "yardmaster", "linear", "", nil, actor); err == nil || err.Error() != want+"; this is "+this {
			t.Errorf("unsubscribe as %q = %v, want %q", actor, err, want+"; this is "+this)
		}
	}
	if now, _ := api.Head(ctx); now != head {
		t.Errorf("the refusals logged %d events", now-head)
	}
	if r, _ := api.GetCrew(ctx, "yardmaster"); len(r.Subscriptions) != 0 {
		t.Fatalf("a refused subscription was kept: %v", r.Subscriptions)
	}
	// The member itself, and the operator for it.
	if err := api.AddSubscription(ctx, "yardmaster", sub, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if n, err := api.RemoveSubscriptions(ctx, "yardmaster", "linear", "", nil, "yardmaster"); err != nil || n != 1 {
		t.Fatalf("the member's unsubscribe = %d, %v", n, err)
	}
	if err := api.AddSubscription(ctx, "yardmaster", sub, "jo"); err != nil {
		t.Fatal(err)
	}
	if n, err := api.RemoveSubscriptions(ctx, "yardmaster", "linear", "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("the operator's unsubscribe = %d, %v", n, err)
	}

	// Under a shared name the side a change lands on is gated as that
	// side alone; landing on both is refused first, to be repeated for
	// one.
	if err := api.PutGroup(ctx, store.Group{Name: "both", Runner: "manual"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "both", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.AddSubscriptionKind(ctx, "both", "crew", store.Subscription{Source: "linear", Scope: store.ScopeAll}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.AddSubscriptionKind(ctx, "both", "group", store.Subscription{Source: "linear", Scope: store.ScopeMentions}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.RemoveSubscriptions(ctx, "both", "linear", "all", nil, "clerk"); err == nil ||
		err.Error() != "only both or the operator (jo) changes what both is told; this is clerk" {
		t.Errorf("the crew side's unsubscribe as clerk = %v", err)
	}
	if _, err := api.RemoveSubscriptions(ctx, "both", "linear", "mentions", nil, "clerk"); err == nil ||
		err.Error() != "only a member of both or the operator (jo) changes what both is told; this is clerk" {
		t.Errorf("the group side's unsubscribe as clerk = %v", err)
	}
	if _, err := api.RemoveSubscriptions(ctx, "both", "linear", "", nil, "clerk"); err == nil || !strings.Contains(err.Error(), "--group") {
		t.Errorf("unsubscribe both as clerk = %v, want the refusal naming both", err)
	}

	// With no operator named, the member still changes its own, and
	// another crew member is still refused.
	plain := storetest.New(t)
	pctx := context.Background()
	for _, r := range []string{"a", "b"} {
		if _, err := plain.PutCrew(pctx, store.CrewMember{Name: r, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := plain.AddSubscription(pctx, "a", sub, "b"); err == nil ||
		err.Error() != "only a or the operator changes what a is told; this is b" {
		t.Errorf("another crew member, with no operator = %v", err)
	}
	if err := plain.AddSubscription(pctx, "a", sub, "a"); err != nil {
		t.Errorf("the member itself, with no operator: %v", err)
	}

	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	// Another crew member's pane, naming the member: refused as the
	// pane's crew member, and nothing is written.
	s, _ := operatorYard(t)
	if err := s.CrewStarted(pctx, "clerk", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(pctx, me)
	head, err = s.Head(pctx)
	if err != nil {
		t.Fatal(err)
	}
	const paneWant = "only yardmaster or the operator (jo) changes what yardmaster is told; this is clerk"
	if err := s.AddSubscription(pane, "yardmaster", sub, "yardmaster"); err == nil || err.Error() != paneWant {
		t.Errorf("subscribe faked from clerk's pane = %v, want %q", err, paneWant)
	}
	if _, err := s.RemoveSubscriptions(pane, "yardmaster", "linear", "", nil, "yardmaster"); err == nil || err.Error() != paneWant {
		t.Errorf("unsubscribe faked from clerk's pane = %v, want %q", err, paneWant)
	}
	if now, _ := s.Head(pctx); now != head {
		t.Errorf("the pane's refusals logged %d events", now-head)
	}
	// The member's own pane, naming another: the change passes and is
	// recorded as the member.
	s, _ = operatorYard(t)
	if err := s.CrewStarted(pctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane = store.WithCaller(pctx, me)
	if err := s.AddSubscription(pane, "yardmaster", sub, "yard-10-new"); err != nil {
		t.Fatalf("subscribe from yardmaster's own pane, naming another: %v", err)
	}
	if a := lastKindActor(t, s, pctx, "subscribed"); a != "yardmaster" {
		t.Errorf("subscribed event actor = %q, want the pane's actor %q", a, "yardmaster")
	}
}

// What a manual group tells its members is a member's to decide, or the
// operator's for them: whoever is neither is refused, and the refusal
// logs no event. A group with no members is the operator's alone, not
// anyone's as a move is. From a pane the change is checked and recorded
// as the pane's actor, whatever --actor says.
func TestManualGroupSubscriptionIsAMembersOrTheOperators(t *testing.T) {
	t.Parallel()
	api, _ := operatorYard(t)
	ctx := context.Background()
	if _, err := api.AddUser(ctx, "bob", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"yardmaster", "ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutGroup(ctx, store.Group{Name: "lonely", Runner: "manual"}, "jo"); err != nil {
		t.Fatal(err)
	}
	sub := store.Subscription{Source: "linear", Scope: store.ScopeAll}
	const want = "only a member of reviewers or the operator (jo) changes what reviewers is told"
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"clerk", "bob", "yard-10-new", ""} {
		this := actor
		if this == "" {
			this = "nobody"
		}
		if err := api.AddSubscription(ctx, "reviewers", sub, actor); err == nil || err.Error() != want+"; this is "+this {
			t.Errorf("subscribe as %q = %v, want %q", actor, err, want+"; this is "+this)
		}
		if _, err := api.RemoveSubscriptions(ctx, "reviewers", "linear", "", nil, actor); err == nil || err.Error() != want+"; this is "+this {
			t.Errorf("unsubscribe as %q = %v, want %q", actor, err, want+"; this is "+this)
		}
	}
	// No members is the operator's alone: a non-member is refused in the
	// same words, and the operator passes.
	if err := api.AddSubscription(ctx, "lonely", sub, "clerk"); err == nil ||
		err.Error() != "only a member of lonely or the operator (jo) changes what lonely is told; this is clerk" {
		t.Errorf("subscribe a memberless group as clerk = %v", err)
	}
	if err := api.AddSubscription(ctx, "lonely", sub, "jo"); err != nil {
		t.Errorf("subscribe a memberless group as the operator: %v", err)
	}
	if now, _ := api.Head(ctx); now != head+1 {
		t.Errorf("the refusals logged %d events besides the operator's subscribe", now-head-1)
	}
	if g, _ := api.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("a refused subscription was kept: %v", g.Subscriptions)
	}
	// A crew member that is one, a user that is one, and the operator.
	if err := api.AddSubscription(ctx, "reviewers", sub, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	other := store.Subscription{Source: "github", Scope: store.ScopeAll}
	if err := api.AddSubscription(ctx, "reviewers", other, "ada"); err != nil {
		t.Fatal(err)
	}
	if n, err := api.RemoveSubscriptions(ctx, "reviewers", "linear", "", nil, "ada"); err != nil || n != 1 {
		t.Fatalf("a member's unsubscribe = %d, %v", n, err)
	}
	if n, err := api.RemoveSubscriptions(ctx, "reviewers", "github", "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("the operator's unsubscribe = %d, %v", n, err)
	}

	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	// Another crew member's pane, naming a member: refused as the pane's
	// crew member, and nothing is written.
	s, _ := operatorYard(t)
	pctx := context.Background()
	if err := s.PutGroup(pctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"yardmaster", "ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStarted(pctx, "clerk", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(pctx, me)
	head, err = s.Head(pctx)
	if err != nil {
		t.Fatal(err)
	}
	const paneWant = "only a member of reviewers or the operator (jo) changes what reviewers is told; this is clerk"
	if err := s.AddSubscription(pane, "reviewers", sub, "ada"); err == nil || err.Error() != paneWant {
		t.Errorf("subscribe faked from clerk's pane = %v, want %q", err, paneWant)
	}
	if now, _ := s.Head(pctx); now != head {
		t.Errorf("the pane's refusal logged %d events", now-head)
	}
	// A member's own pane, naming another: the change passes and is
	// recorded as the member.
	s, _ = operatorYard(t)
	if err := s.PutGroup(pctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"yardmaster", "ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStarted(pctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane = store.WithCaller(pctx, me)
	if err := s.AddSubscription(pane, "reviewers", sub, "yard-10-new"); err != nil {
		t.Fatalf("subscribe from a member's own pane, naming another: %v", err)
	}
	if a := lastKindActor(t, s, pctx, "subscribed"); a != "yardmaster" {
		t.Errorf("subscribed event actor = %q, want the pane's actor %q", a, "yardmaster")
	}
}
