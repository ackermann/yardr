package store_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// failureSub is the subscription the operator holds for the automatic
// install's failures: yardr scope all on exactly install_failed and
// rolled_back.
func failureSub() store.Subscription {
	return store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.JoinWhere([]string{store.InstallFailed, store.RolledBack})}}
}

// operatorWithNotify names jo the operator and tells them through a
// declared pushover source, as yardr pack apply pushover --notify would.
func operatorWithNotify(t *testing.T, st store.API, name string) {
	t.Helper()
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, name, name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", sourceFile, name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, name, map[string]string{store.UserNotify: "pushover"}, name); err != nil {
		t.Fatal(err)
	}
}

func userSubs(t *testing.T, st store.API, name string) []store.Subscription {
	t.Helper()
	u, err := st.GetUser(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.Subscriptions
}

// sameSub reports whether sub is the subscription want: source, scope and
// filter all equal.
func sameSub(sub, want store.Subscription) bool {
	return sub.Source == want.Source && sub.Scope == want.Scope && maps.Equal(sub.Where, want.Where)
}

// Setting upgrade_from is refused while the operator names no notify
// source, naming the packs that set one; set with one, it subscribes the
// operator to exactly the two failure kinds in the same write, even where
// the subscription was removed before, and never twice.
func TestUpgradeFromSetting(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "me"); err == nil ||
		!strings.Contains(err.Error(), "names no operator") {
		t.Fatalf("with no operator: %v", err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err == nil ||
		!strings.Contains(err.Error(), "names no notify source") ||
		!strings.Contains(err.Error(), "pushover") || !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("with no notify source: %v", err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "jo", map[string]string{store.UserNotify: "pushover"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Removed before: setting it subscribes again.
	if n, err := st.RemoveSubscriptions(ctx, "jo", store.SourceYardr, "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("remove = %d, %v", n, err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); got[store.UpgradeFromSetting] != "yardr" {
		t.Fatalf("settings = %v", got)
	}
	subs := userSubs(t, st, "jo")
	if len(subs) != 1 || !sameSub(subs[0], failureSub()) {
		t.Fatalf("operator subscriptions = %+v", subs)
	}
	// Set again: still one.
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
		t.Fatal(err)
	}
	if subs := userSubs(t, st, "jo"); len(subs) != 1 {
		t.Fatalf("after a second set: %+v", subs)
	}
	// A depot name, and unset is off.
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "Bad Name", "jo"); err == nil ||
		!strings.Contains(err.Error(), "depot name") {
		t.Fatalf("a bad depot name: %v", err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); len(got) != 2 || got[store.OperatorSetting] != "jo" || got[store.FilesMaxSetting] != "64" {
		t.Fatalf("after unset = %v", got)
	}
}

// Setting upgrade_from to a depot the yard has not got is refused, naming
// the depot and the depot-add line: whoever created a depot under the name
// would otherwise choose the install source. The stored value is unchanged
// and nothing is logged; a depot of the yard, and clearing, still pass.
func TestUpgradeFromSettingRefusesUnknownDepot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
		t.Fatal(err)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = st.SetSetting(ctx, store.UpgradeFromSetting, "ghost", "jo")
	if err == nil || !strings.Contains(err.Error(), "ghost") ||
		!strings.Contains(err.Error(), "yardr depot add") {
		t.Fatalf("an unknown depot: %v", err)
	}
	if got, _ := st.Settings(ctx); got[store.UpgradeFromSetting] != "yardr" {
		t.Fatalf("the stored value moved: %v", got)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("the refusal logged %d events", now-head)
	}
	// Clearing still passes, and setting it again after.
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
		t.Fatal(err)
	}
}

// Removing the depot upgrade_from names is refused, naming the setting and
// the clear-first line: a depot removed under the name would let whoever
// creates one under it choose the install source. Nothing is deleted and no
// depot_removed is logged, while after clearing it the removal passes.
func TestDeleteDepotRefusesUpgradeFromDepot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	depot := "yard"
	route, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders", Depot: &depot}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	putNamed(t, st, "code", yardFlow)
	if err := st.SetFlowPointer(ctx, "yard", "", "code", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yard", "jo"); err != nil {
		t.Fatal(err)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DeleteDepot(ctx, "yard", "jo")
	if err == nil || !strings.Contains(err.Error(), store.UpgradeFromSetting) ||
		!strings.Contains(err.Error(), `yardr settings set upgrade_from ""`) {
		t.Fatalf("the named depot: %v", err)
	}
	if _, err := st.GetDepot(ctx, "yard"); err != nil {
		t.Fatalf("the depot is gone: %v", err)
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].ID != route.ID {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	ptrs, err := st.FlowPointers(ctx)
	if err != nil || len(ptrs) != 1 || ptrs[0].Depot != "yard" {
		t.Fatalf("flow pointers = %+v, %v", ptrs, err)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("the refusal logged %d events", now-head)
	}
	// After clearing, the removal passes with all three.
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeleteDepot(ctx, "yard", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routes) != 1 || got.Routes[0] != route.ID || len(got.FlowPointers) != 1 {
		t.Fatalf("removal = %+v", got)
	}
}

// The open-beads refusal still wins where both it and the upgrade_from
// refusal apply: the depot names its beads, not the setting.
func TestDeleteDepotOpenBeadsRefusalWinsOverUpgradeFrom(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yard", "jo"); err != nil {
		t.Fatal(err)
	}
	open, err := st.CreateBead(ctx, store.NewBead{Title: "some work", Depot: "yard"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DeleteDepot(ctx, "yard", "jo")
	if err == nil || !strings.Contains(err.Error(), "bead "+open.ID) ||
		!strings.Contains(err.Error(), "close the beads") ||
		strings.Contains(err.Error(), store.UpgradeFromSetting) {
		t.Fatalf("with an open bead: %v", err)
	}
}

// A store from before the refusals whose upgrade_from names no depot has
// the setting cleared once on open, told as the setting's own change from
// yard; one whose depot exists keeps it, with nothing logged.
func TestSchema24StoreClearsDanglingUpgradeFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	aged := func(t *testing.T, dangle bool) (string, uint64) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "yard.db")
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		operatorWithNotify(t, s, "jo")
		if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, store.UpgradeFromSetting, "yard", "jo"); err != nil {
			t.Fatal(err)
		}
		// The settings migration is not this test's: mark the yard set up so
		// the reopen logs nothing but what the upgrade_from migration would.
		if err := s.SetSetting(ctx, "default_kind", "claude", "jo"); err != nil {
			t.Fatal(err)
		}
		head, _ := s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		// Age it: a store from before the refusals, under that build's
		// stamp, with the depot gone where it dangles.
		db, err := bolt.Open(path, 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			if dangle {
				if err := tx.Bucket([]byte("depots")).Delete([]byte("yard")); err != nil {
					return err
				}
			}
			return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 24))
		})
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		return path, head
	}
	dangling, danglingHead := aged(t, true)
	kept, keptHead := aged(t, false)
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err := store.Open(dangling)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		got, err := s.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got[store.UpgradeFromSetting] != "" {
			t.Fatalf("%s: upgrade_from = %q, want unset", pass, got[store.UpgradeFromSetting])
		}
		headAfter, _ := s.Head(ctx)
		var told []store.Event
		if pass == "the upgrade" {
			told, err = s.Events(ctx, "", 1)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, dangling); got != 34 {
			t.Fatalf("%s: schema = %d; want 34", pass, got)
		}
		if pass == "the upgrade" {
			if headAfter != danglingHead+1 {
				t.Fatalf("%s logged %d events, want 1", pass, headAfter-danglingHead)
			}
			if len(told) != 1 || told[0].Kind != "setting_changed" || told[0].Actor != "yard" ||
				told[0].Data["key"] != store.UpgradeFromSetting || told[0].Data["value"] != "" ||
				told[0].Data["reason"] != "cleared: named depot yard, which the yard does not have" {
				t.Fatalf("%s told %+v", pass, told)
			}
		} else if headAfter != danglingHead+1 {
			t.Fatalf("%s logged %d events", pass, headAfter-danglingHead-1)
		}
		s, err = store.Open(kept)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		got, err = s.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got[store.UpgradeFromSetting] != "yard" {
			t.Fatalf("%s: upgrade_from = %q, want yard", pass, got[store.UpgradeFromSetting])
		}
		keptAfter, _ := s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, kept); got != 34 {
			t.Fatalf("%s: schema = %d; want 34", pass, got)
		}
		if keptAfter != keptHead {
			t.Fatalf("%s logged %d events", pass, keptAfter-keptHead)
		}
	}
}

// A pre-existing yard can have its operator name on a crew member. Such a
// yard cannot tell the operator outside the store, so upgrade_from refuses
// even though the legacy setting passes the operator-only check.
func TestUpgradeFromSettingRefusesLegacyOperatorCrew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "jo", Runner: "manual"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// This is legacy state: current settings cannot make a crew member the
	// operator, but an older yard may already have one.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := json.Marshal("jo")
	if err != nil {
		t.Fatal(err)
	}
	// Its users are of before roles too: mckean holds none.
	err = db.Update(func(tx *bolt.Tx) error {
		users := tx.Bucket([]byte("users"))
		var u map[string]any
		if err := json.Unmarshal(users.Get([]byte("mckean")), &u); err != nil {
			return err
		}
		delete(u, "role")
		raw, err := json.Marshal(u)
		if err != nil {
			return err
		}
		if err := users.Put([]byte("mckean"), raw); err != nil {
			return err
		}
		return tx.Bucket([]byte("settings")).Put([]byte(store.OperatorSetting), operator)
	})
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)
	err = st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo")
	if err == nil || !strings.Contains(err.Error(), "the operator jo is a crew member's name") {
		t.Fatalf("legacy operator crew: %v", err)
	}
	if got, _ := st.Settings(ctx); got[store.UpgradeFromSetting] != "" {
		t.Fatalf("failed setting was stored: %v", got)
	}
}

// A user the operator's setting makes carries the failure subscription; a
// user added any other way carries none.
func TestOperatorCarriesUpgradeSubscription(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if subs := userSubs(t, st, "jo"); len(subs) != 1 || !sameSub(subs[0], failureSub()) {
		t.Fatalf("operator subscriptions = %+v", subs)
	}
	if subs := userSubs(t, st, "ada"); len(subs) != 0 {
		t.Fatalf("other user subscriptions = %+v", subs)
	}
}

// A yard from before the subscription gains it once on open, and keeps an
// unsubscribe afterwards while upgrade_from is unset.
func TestMigrateUpgradeTell(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "jo", store.SourceYardr, "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("remove = %d, %v", n, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restamp(t, path, 19)
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUser(ctx, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Subscriptions) != 1 || !sameSub(u.Subscriptions[0], failureSub()) {
		t.Fatalf("after open at 19: %+v", u.Subscriptions)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, path); got != 34 {
		t.Fatalf("schema after Open = %d; want 34", got)
	}
	// Once: a second open adds no other.
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	u, err = s.GetUser(ctx, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Subscriptions) != 1 {
		t.Fatalf("after a second open: %+v", u.Subscriptions)
	}
	if n, err := s.RemoveSubscriptions(ctx, "jo", store.SourceYardr, "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("unsubscribe while unset = %d, %v", n, err)
	}
	if u, _ := s.GetUser(ctx, "jo"); len(u.Subscriptions) != 0 {
		t.Fatalf("after unsubscribe: %+v", u.Subscriptions)
	}
}

func restamp(t *testing.T, path string, v uint64) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, v))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
}

// Dropping a yardr subscription that matches either failure kind while
// upgrade_from is set is refused, naming the setting; any other drop, and
// any drop with the setting unset, works.
func TestUpgradeSubscriptionDropRefused(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	if _, err := st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	kind := func(kinds ...string) map[string]string {
		return map[string]string{"kind": store.JoinWhere(kinds)}
	}
	add := func(name string, sub store.Subscription) {
		t.Helper()
		if err := st.AddSubscription(ctx, name, sub, name); err != nil {
			t.Fatal(err)
		}
	}
	add("ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: kind(store.KindAttention)})
	add("ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: kind("noted")})
	add("ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeMentions, Where: kind(store.InstallFailed)})
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	bead, err := st.CreateBead(ctx, store.NewBead{Title: "some work", Depot: "yard"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	add("ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"bead": bead.ID}})
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "yardr", "jo"); err != nil {
		t.Fatal(err)
	}
	drop := func(name string, where map[string]string) error {
		_, err := st.RemoveSubscriptions(ctx, name, store.SourceYardr, "", where, name)
		return err
	}
	if err := drop("jo", failureSub().Where); err == nil ||
		!strings.Contains(err.Error(), store.UpgradeFromSetting) || !strings.Contains(err.Error(), "remove the setting first") {
		t.Fatalf("the operator's subscription: %v", err)
	}
	if err := drop("ada", kind(store.KindAttention)); err == nil ||
		!strings.Contains(err.Error(), store.UpgradeFromSetting) {
		t.Fatalf("kind=attention: %v", err)
	}
	// A narrower scope never matches without a bead, and a bead filter
	// needs one: neither reaches a failure event, so both drop.
	if err := drop("ada", kind(store.InstallFailed)); err != nil {
		t.Fatalf("scope mentions: %v", err)
	}
	if err := drop("ada", map[string]string{"bead": bead.ID}); err != nil {
		t.Fatalf("bead filter: %v", err)
	}
	if err := drop("ada", kind("noted")); err != nil {
		t.Fatalf("kind=noted: %v", err)
	}
	if err := st.SetSetting(ctx, store.UpgradeFromSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := drop("jo", failureSub().Where); err != nil {
		t.Fatalf("with the setting unset: %v", err)
	}
}

// A bead-less install_failed notifies a user subscribed on its kind or on
// the class, and records the crew deliveries; a subscription filtering on
// bead fields does not match it. The yardmaster's inbox holds it on its
// default --on, with no migration of its own.
func TestBeadlessFailureNotifies(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	for _, name := range []string{"ada", "bob", "cara"} {
		if _, err := st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"kind": "claude"}, On: []string{store.KindAttention}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr",
		Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	sub := func(kinds ...string) store.Subscription {
		return store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
			Where: map[string]string{"kind": store.JoinWhere(kinds)}}
	}
	if err := st.AddSubscription(ctx, "ada", sub(store.InstallFailed), "ada"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "bob", sub(store.KindAttention), "bob"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	watched, err := st.CreateBead(ctx, store.NewBead{Title: "some work", Depot: "yard"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "cara", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.InstallFailed, "bead": watched.ID}}, "cara"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "clerk", sub(store.InstallFailed), "jo"); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"revision": "1904417", "stage": "health", "reason": "the daemon never stamped"}
	if err := st.LogDispatch(ctx, "", store.InstallFailed, "yard", data); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Events(ctx, "", 1)
	if err != nil || len(evs) != 1 {
		t.Fatal(evs, err)
	}
	notified, _ := evs[0].Data["notified"].(map[string]any)
	if notified["ada"] != "all" || notified["bob"] != "all" || notified["jo"] != "all" {
		t.Fatalf("notified = %v", evs[0].Data["notified"])
	}
	if _, ok := notified["cara"]; ok {
		t.Fatalf("a bead filter matched: %v", notified)
	}
	deliveries, _ := evs[0].Data["deliveries"].(map[string]any)
	if deliveries["clerk"] != "all" {
		t.Fatalf("deliveries = %v", evs[0].Data["deliveries"])
	}
	if _, ok := deliveries["yardmaster"]; ok {
		t.Fatalf("the yardmaster has no subscription: %v", deliveries)
	}
	for _, name := range []string{"ada", "bob", "jo"} {
		page, err := st.UserNotifications(ctx, name, 10)
		if err != nil || len(page.Notifications) != 1 {
			t.Fatalf("%s notifications = %+v, %v", name, page, err)
		}
		n := page.Notifications[0]
		if n.Kind != store.InstallFailed || n.Bead != "" || !strings.Contains(n.Text, "install_failed (all) by yard") {
			t.Fatalf("%s notification = %+v", name, n)
		}
	}
	if page, _ := st.UserNotifications(ctx, "cara", 10); len(page.Notifications) != 0 {
		t.Fatalf("cara notifications = %+v", page.Notifications)
	}
	if ps := pushesOf(t, st); len(ps) != 1 || ps[0].User != "jo" {
		t.Fatalf("pushes = %+v", ps)
	}
	page, err := st.Inbox(ctx, "yardmaster", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Events {
		found = found || e.Kind == store.InstallFailed
	}
	if !found {
		t.Fatalf("the yardmaster's inbox holds %+v", page.Events)
	}
	// Nor is a person told of what they did themselves.
	if err := st.LogDispatch(ctx, "", store.RolledBack, "ada", map[string]any{"reason": "by hand"}); err != nil {
		t.Fatal(err)
	}
	if page, _ := st.UserNotifications(ctx, "ada", 10); len(page.Notifications) != 1 {
		t.Fatalf("ada hears her own: %+v", page.Notifications)
	}
}

// An event marked told writes notifications but queues no push.
func TestToldEventNotifiesWithoutPush(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	operatorWithNotify(t, st, "jo")
	if err := st.LogDispatch(ctx, "", store.InstallFailed, "yard",
		map[string]any{"revision": "1904417", "told": true}); err != nil {
		t.Fatal(err)
	}
	page, err := st.UserNotifications(ctx, "jo", 10)
	if err != nil || len(page.Notifications) != 1 || !page.Notifications[0].Told {
		t.Fatalf("notifications = %+v, %v", page, err)
	}
	if strings.Contains(page.Notifications[0].Text, "told=") {
		t.Fatalf("the row says told: %q", page.Notifications[0].Text)
	}
	if ps := pushesOf(t, st); len(ps) != 0 {
		t.Fatalf("pushes = %+v", ps)
	}
	if err := st.LogDispatch(ctx, "", store.RolledBack, "yard", map[string]any{"reason": "x"}); err != nil {
		t.Fatal(err)
	}
	if ps := pushesOf(t, st); len(ps) != 1 {
		t.Fatalf("untold pushes = %+v", ps)
	}
}
