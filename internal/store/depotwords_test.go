package store_test

import (
	"context"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A store of schema 11, from when a depot was a rig, comes out of Open with
// the word renamed in every place the store holds it: the bucket of the
// records, the field that names the depot in a bead, a route, a flow
// pointer, the record of an imported pack (its rig_roles too) and a peer's
// intake, the key and a kind in the filter of a crew member's, a user's, a
// group's and a pack's subscription, a kind in --on, and the path of a
// clone under <home>/rigs, which is <home>/depots with a link of the old
// name. A field this build does not know stays in its record. The events
// already logged are not rewritten, and one of an old kind is still in the
// inbox of whoever hears of the kind.
func TestSchema11StoreRenamesRigs(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	path := filepath.Join(home, "yard.db")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	clone, elsewhere := filepath.Join(home, "depots", "web"), t.TempDir()
	s, err := store.Open(path)
	must(err)
	must(s.SetSetting(ctx, "default_kind", "claude", "me"))
	must(s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: clone}, "me"))
	must(s.PutDepot(ctx, store.Depot{Name: "docs", Kind: store.DepotDir, Path: elsewhere}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}))
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual", On: []string{"bad_depot", "held"}}, "me")
	must(err)
	_, err = s.AddUser(ctx, "jo", "me")
	must(err)
	wheres := []map[string]string{{"depot": "web"}, {"kind": "depot_updated"}, {"type": "task"}}
	for _, who := range []struct{ name, actor string }{{"boss", "me"}, {"jo", "jo"}, {"people", "me"}} {
		for _, w := range wheres {
			must(s.AddSubscription(ctx, who.name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, who.actor))
		}
	}
	f, err := store.ParseFlow("new -> done on done\ndone terminal\n")
	must(err)
	f.Name = "web"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", "", "web", "me"))
	web := "web"
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Depot: &web, Group: "people"})
	must(err)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "web-1", Title: "a task", Depot: "web"}, "me")
	must(err)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "a bead of no depot"}, "me")
	must(err)
	_, err = s.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter", DepotRoles: true,
		Objects: []store.PackObject{
			{Kind: "subscription", CrewMember: "boss", Subscription: &store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": "web", "stage": "new"}}},
		}}, true, "me")
	must(err)
	// What a build of schema 11 logged for a bead whose rig it could not
	// work in: boss hears of the kind.
	must(s.LogEvent(ctx, "web-1", "bad_rig", "yard", map[string]any{"rig": "web", "reason": "no such directory"}))
	must(s.Close(ctx))

	// Age it: every word as a build of schema 11 stored it, in the bucket
	// and the directory it had, under that build's stamp. The peer is as
	// that build wrote one, with a field no build knows.
	oldClone := filepath.Join(home, "rigs", "web")
	old := strings.NewReplacer(
		`"depot":"web"`, `"rig":"web"`, `"depot_roles":true`, `"rig_roles":true`,
		`"depot_updated"`, `"rig_updated"`, `"bad_depot"`, `"bad_rig"`,
		clone, oldClone,
	)
	aged := map[string]string{"beads": "beads", "routes": "routes", "flow_pointers": "flow_pointers", "packs": "packs",
		"crew": "crew", "users": "users", "groups": "groups", "depots": "rigs"}
	db, err := bolt.Open(path, 0o600, nil)
	must(err)
	err = db.Update(func(tx *bolt.Tx) error {
		for name, was := range aged {
			rows := map[string]string{}
			_ = tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error {
				rows[string(k)] = string(v)
				return nil
			})
			if err := tx.DeleteBucket([]byte(name)); err != nil {
				return err
			}
			b, err := tx.CreateBucket([]byte(was))
			if err != nil {
				return err
			}
			for k, v := range rows {
				if err := b.Put([]byte(k), []byte(old.Replace(v))); err != nil {
					return err
				}
			}
		}
		err := tx.Bucket([]byte("peers")).Put([]byte("north"),
			[]byte(`{"name":"north","intake":{"rig":"web","flow":"web","auto":true,"later":7},"later":[1,2]}`))
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 11))
	})
	db.Close()
	must(err)
	must(os.MkdirAll(oldClone, 0o700))
	must(os.WriteFile(filepath.Join(oldClone, "README"), []byte("web\n"), 0o600))
	was := bucketDump(t, path, "beads", "routes", "flow_pointers", "packs", "crew", "users", "groups", "rigs", "peers")
	for _, want := range append(slices.Collect(maps.Values(aged)), "peers") {
		found := false
		for k, v := range was {
			found = found || strings.HasPrefix(k, want+"\x00") && (strings.Contains(v, `"rig`) || strings.Contains(v, `_rig"`) || strings.Contains(v, oldClone))
		}
		if !found {
			t.Fatalf("the aged store has no old word in %s", want)
		}
	}
	events := bucketDump(t, path, "events")

	var first map[string]string
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		must(err)
		depots, err := s.Depots(ctx)
		must(err)
		if len(depots) != 2 || depots[0].Name != "docs" || depots[0].Path != elsewhere || depots[1].Name != "web" || depots[1].Path != clone {
			t.Fatalf("%s: depots = %+v, want docs at %s and web at %s", pass, depots, elsewhere, clone)
		}
		boss, err := s.GetCrew(ctx, "boss")
		must(err)
		if !slices.Equal(boss.On, []string{"bad_depot", "held"}) {
			t.Fatalf("%s: boss hears of %v", pass, boss.On)
		}
		people, err := s.GetGroup(ctx, "people")
		must(err)
		users, err := s.Users(ctx)
		must(err)
		for who, list := range map[string][]store.Subscription{"boss": boss.Subscriptions, "jo": users[0].Subscriptions, "people": people.Subscriptions} {
			var got []string
			for _, sub := range list {
				got = append(got, sub.String())
			}
			slices.Sort(got)
			if want := []string{"yardr all depot=web", "yardr all kind=depot_updated", "yardr all type=task"}; !slices.Equal(got, want) {
				t.Fatalf("%s: %s's subscriptions = %q, want %q", pass, who, got, want)
			}
		}
		if b, err := s.GetBead(ctx, "web-1"); err != nil || b.Depot == nil || *b.Depot != "web" {
			t.Fatalf("%s: the bead = %+v, %v; want it in web", pass, b, err)
		}
		if b, err := s.GetBead(ctx, "yard-1"); err != nil || b.Depot != nil {
			t.Fatalf("%s: the bead of no depot = %+v, %v", pass, b, err)
		}
		if in, err := s.ListBeads(ctx, store.BeadFilter{Depot: "web"}); err != nil || len(in) != 1 || in[0].ID != "web-1" {
			t.Fatalf("%s: beads in web = %+v, %v", pass, in, err)
		}
		if routes, err := s.Routes(ctx); err != nil || len(routes) != 1 || routes[0].Depot == nil || *routes[0].Depot != "web" {
			t.Fatalf("%s: routes = %+v, %v", pass, routes, err)
		}
		if ptrs, err := s.FlowPointers(ctx); err != nil || len(ptrs) != 1 || ptrs[0] != (store.FlowPointer{Depot: "web", Flow: "web"}) {
			t.Fatalf("%s: flow pointers = %+v, %v", pass, ptrs, err)
		}
		packs, err := s.Packs(ctx)
		must(err)
		if len(packs) != 1 || packs[0].Depot != "web" || !packs[0].DepotRoles || len(packs[0].Objects) != 1 ||
			!maps.Equal(packs[0].Objects[0].Subscription.Where, map[string]string{"depot": "web", "stage": "new"}) {
			t.Fatalf("%s: the pack's record = %+v", pass, packs)
		}
		if north, err := s.GetPeer(ctx, "north"); err != nil || north.Intake == nil || north.Intake.Depot != "web" || north.Intake.Flow != "web" || !north.Intake.Auto {
			t.Fatalf("%s: the peer = %+v, %v", pass, north, err)
		}
		// What was logged before the upgrade is still in the inbox, and
		// still says which depot.
		page, err := s.Inbox(ctx, "boss", 0)
		must(err)
		if last := page.Events[len(page.Events)-1]; last.Kind != "bad_rig" || store.EventDepot(last.Data) != "web" {
			t.Fatalf("%s: the last of boss's inbox = %+v", pass, last)
		}
		must(s.Close(ctx))

		if got := schemaOf(t, path); got != 16 {
			t.Fatalf("%s: schema = %d; want 16", pass, got)
		}
		if st, err := os.Lstat(filepath.Dir(oldClone)); err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: the old directory is no link: %v, %v", pass, st, err)
		}
		for _, dir := range []string{clone, oldClone} {
			if b, err := os.ReadFile(filepath.Join(dir, "README")); err != nil || string(b) != "web\n" {
				t.Fatalf("%s: %s/README = %q, %v", pass, dir, b, err)
			}
		}
		now := bucketDump(t, path, "beads", "routes", "flow_pointers", "packs", "crew", "users", "groups", "depots", "peers")
		for k, v := range now {
			if strings.Contains(v, `"rig`) || strings.Contains(v, `_rig"`) || strings.Contains(v, oldClone) {
				t.Fatalf("%s: %q still says %s", pass, k, v)
			}
		}
		if peer := now["peers\x00north"]; !strings.Contains(peer, `"later":7`) || !strings.Contains(peer, `"later":[1,2]`) {
			t.Fatalf("%s: the peer's record lost a field: %s", pass, peer)
		}
		db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
		must(err)
		_ = db.View(func(tx *bolt.Tx) error {
			if tx.Bucket([]byte("rigs")) != nil {
				t.Errorf("%s: the old bucket is still there", pass)
			}
			return nil
		})
		db.Close()
		after := bucketDump(t, path, "events")
		var added []string
		for k, v := range after {
			if _, had := events[k]; !had {
				added = append(added, v)
			} else if events[k] != v {
				t.Fatalf("%s: an event was rewritten: %s, was %s", pass, v, events[k])
			}
		}
		if len(added) != 1 || !strings.Contains(added[0], `"kind":"depots_migrated"`) || !strings.Contains(added[0], `"from":"rigs"`) {
			t.Fatalf("%s: events added = %q, want one depots_migrated", pass, added)
		}
		for _, want := range []string{`"depots":2`, `"beads":1`, `"routes":1`, `"flow_pointers":1`, `"packs":1`, `"peers":1`, `"paths":1`,
			`"subscriptions":1`, `"user_subscriptions":1`, `"group_subscriptions":1`, `"pack_subscriptions":1`} {
			if !strings.Contains(added[0], want) {
				t.Fatalf("%s: depots_migrated = %s, want %s in it", pass, added[0], want)
			}
		}
		if first == nil {
			first = now
		} else if !maps.Equal(first, now) {
			t.Fatalf("the next start changed what the upgrade wrote")
		}
	}
}

// A home with both directories is left as it is, and so are the paths: the
// operator made the new one, and yardr does not choose between the two.
func TestSchema11StoreLeavesTwoDepotDirectories(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "yard.db")
	oldClone := filepath.Join(home, "rigs", "web")
	for _, dir := range []string{oldClone, filepath.Join(home, "depots")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: oldClone}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 11))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if s, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	web, err := s.GetDepot(ctx, "web")
	if err != nil || web.Path != oldClone {
		t.Fatalf("the depot = %+v, %v; want its path as it was", web, err)
	}
	if st, err := os.Lstat(filepath.Dir(oldClone)); err != nil || !st.IsDir() {
		t.Fatalf("the old directory = %v, %v; want it left", st, err)
	}
}

// The words a depot had are still read where one is given: the key rig and
// the kinds that said rig in a filter, and those kinds in --on. What is
// stored and answered has the new ones.
func TestTheOldDepotWordsAreStillRead(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	boss, err := s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual", On: []string{"bad_rig", "bad_depot", "held"}}, "me")
	if err != nil || !slices.Equal(boss.On, []string{"bad_depot", "held"}) {
		t.Fatalf("--on that says bad_rig: %+v, %v", boss, err)
	}
	for _, w := range []map[string]string{{"rig": "web"}, {"kind": "rig_removed"}} {
		if err := s.AddSubscription(ctx, "boss", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	boss, err = s.GetCrew(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, sub := range boss.Subscriptions {
		got = append(got, sub.String())
	}
	slices.Sort(got)
	if want := []string{"yardr all depot=web", "yardr all kind=depot_removed"}; !slices.Equal(got, want) {
		t.Fatalf("subscriptions = %q, want %q", got, want)
	}
	// The old spelling names the same subscription when it is removed.
	if n, err := s.RemoveSubscriptions(ctx, "boss", store.SourceYardr, store.ScopeAll, map[string]string{"rig": "web"}, "me"); err != nil || n != 1 {
		t.Fatalf("unsubscribe by the old key = %d, %v", n, err)
	}
	for old, kind := range map[string]string{"rig_updated": "depot_updated", "rig_removed": "depot_removed", "bad_rig": "bad_depot"} {
		if got := store.EventKind(old); got != kind {
			t.Errorf("EventKind(%s) = %s, want %s", old, got, kind)
		}
	}
}
