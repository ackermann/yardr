package store_test

import (
	"context"
	"encoding/binary"
	"errors"
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

var indexBuckets = []string{"inbox", "events_by_kind"}

// indexKeys lists every key of each index bucket in the file at path (nil
// for a bucket that is not there).
func indexKeys(t *testing.T, path string) map[string][]string {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string][]string{}
	_ = db.View(func(tx *bolt.Tx) error {
		for _, name := range indexBuckets {
			b := tx.Bucket([]byte(name))
			if b == nil {
				continue
			}
			var keys []string
			_ = b.ForEach(func(k, _ []byte) error {
				keys = append(keys, string(k))
				return nil
			})
			out[name] = keys
		}
		return nil
	})
	return out
}

func schemaOf(t *testing.T, path string) uint64 {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v uint64
	_ = db.View(func(tx *bolt.Tx) error {
		v = binary.BigEndian.Uint64(tx.Bucket([]byte("meta")).Get([]byte("schema")))
		return nil
	})
	return v
}

// copyFile copies the store file at src to dst, so a migration runs on a
// copy and the fixture stays as it was.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A store stamped with schema 2 comes out of Open at the current schema with its
// inbox and kind indexes built, whether a build before the indexes wrote
// it (no index buckets) or a schema-2 build with the indexes did and an
// older one appended to it since (index buckets missing entries).
func TestSchema2StoreMigratesTo3(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.db")
	s, err := store.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A set-up yard, so reopening it migrates nothing but the indexes.
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := s.Mail(ctx, "yardmaster", text, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, fixture); got != 12 {
		t.Fatalf("a new store is stamped %d; want 12", got)
	}
	want := indexKeys(t, fixture)
	if len(want["inbox"]) != 2 || len(want["events_by_kind"]) == 0 {
		t.Fatalf("fixture indexes = %v", want)
	}

	cases := map[string]func(tx *bolt.Tx) error{
		"no indexes": func(tx *bolt.Tx) error {
			for _, name := range indexBuckets {
				if err := tx.DeleteBucket([]byte(name)); err != nil {
					return err
				}
			}
			return nil
		},
		"indexes missing entries": func(tx *bolt.Tx) error {
			for _, name := range indexBuckets {
				b := tx.Bucket([]byte(name))
				k, _ := b.Cursor().Last()
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for name, age := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "yard.db")
			copyFile(t, fixture, path)
			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				if err := age(tx); err != nil {
					return err
				}
				return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 2))
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got := indexKeys(t, path); slices.Equal(got["inbox"], want["inbox"]) {
				t.Fatal("the aged fixture still has its inbox index")
			}

			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.Inbox(ctx, "yardmaster", 0)
			if err != nil || page.Total != 2 {
				t.Fatalf("inbox after migration = %+v %v", page, err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if got := schemaOf(t, path); got != 12 {
				t.Fatalf("schema after Open = %d; want 12", got)
			}
			got := indexKeys(t, path)
			for _, name := range indexBuckets {
				if !slices.Equal(got[name], want[name]) {
					t.Errorf("%s after migration = %v\nwant %v", name, got[name], want[name])
				}
			}
			if got := schemaOf(t, fixture); got != 12 || !slices.Equal(indexKeys(t, fixture)["inbox"], want["inbox"]) {
				t.Fatal("the fixture changed; the migration ran on the copy")
			}
		})
	}
}

// bucketDump is every key and value of the named buckets of the store file
// at path, as bucket\x00key -> value.
func bucketDump(t *testing.T, path string, buckets ...string) map[string]string {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]string{}
	_ = db.View(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			_ = tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error {
				out[name+"\x00"+string(k)] = string(v)
				return nil
			})
		}
		return nil
	})
	return out
}

// A store of schema 8, from when the yard's coordinating crew member was its
// mayor, comes out of Open with the setting that names it under yardmaster
// and nothing else changed: the crew member keeps its name and its prompt's
// path, the record of a pack that called its crew member mayor stays as that
// import wrote it, and the events already logged are not rewritten. A yard
// from before the setting whose crew member is called mayor gets the setting,
// since the default name is no longer that one.
func TestSchema8StoreRenamesTheMayorSetting(t *testing.T) {
	ctx := context.Background()
	kept := []string{"crew", "packs", "depots", "users"}
	cases := []struct {
		name    string
		member  string
		setting string // the mayor setting of the old store, "" for none
		want    string // the yardmaster setting after Open, "" for none
	}{
		{"the setting names a crew member of another name", "boss", "boss", "boss"},
		{"the setting names the crew member mayor", "mayor", "mayor", "mayor"},
		{"no setting, and a crew member mayor", "mayor", "", "mayor"},
		{"no setting, and no crew member mayor", "lead", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "yard.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: tc.member, Runner: "herdr",
				Config: map[string]any{"kind": "claude", "prompt": "/home/jo/.yardr/roles/mayor.md"}}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.AddSubscription(ctx, tc.member, store.Subscription{Source: "linear", Scope: "involved"}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter",
				Groups: map[string]string{"builders": "web-builders"}, Crew: map[string]string{"mayor": tc.member},
				Objects: []store.PackObject{{Kind: "file", Name: "/home/jo/.yardr/packs/starter/roles/mayor.md"}}}, true, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.Mail(ctx, tc.member, "the mayor setting is next", "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			// Age it: the setting as a build of schema 8 stored it, under
			// that build's stamp.
			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				if tc.setting != "" {
					if err := tx.Bucket([]byte("settings")).Put([]byte("mayor"), []byte(`"`+tc.setting+`"`)); err != nil {
						return err
					}
				}
				return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 8))
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			before := bucketDump(t, path, append(kept, "events", "settings")...)

			for _, pass := range []string{"the upgrade", "the next start"} {
				s, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				all, err := s.Settings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, old := all["mayor"]; old || all["yardmaster"] != tc.want || all["default_kind"] != "claude" {
					t.Fatalf("%s: settings = %v, want yardmaster %q and no mayor", pass, all, tc.want)
				}
				if page, err := s.Inbox(ctx, tc.member, 0); err != nil || page.Total != 1 {
					t.Fatalf("%s: %s's inbox = %+v %v", pass, tc.member, page, err)
				}
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if got := schemaOf(t, path); got != 12 {
				t.Fatalf("schema after Open = %d; want 12", got)
			}
			after := bucketDump(t, path, append(kept, "events", "settings")...)
			added := 0
			for k, v := range after {
				was, ok := before[k]
				switch {
				case ok && was != v:
					t.Errorf("%q changed:\n was %s\n now %s", k, was, v)
				case !ok && k == "settings\x00yardmaster":
				case !ok && strings.HasPrefix(k, "events\x00") && strings.Contains(v, `"settings_migrated"`):
					added++
				case !ok:
					t.Errorf("%q was added: %s", k, v)
				}
			}
			for k := range before {
				if _, ok := after[k]; !ok && k != "settings\x00mayor" {
					t.Errorf("%q is gone", k)
				}
			}
			// One event says what the upgrade did, and the next start adds
			// none.
			if want := map[bool]int{true: 1, false: 0}[tc.want != ""]; added != want {
				t.Errorf("%d settings_migrated events, want %d", added, want)
			}
		})
	}
}

// The setting's old key still sets and unsets it, under the new one.
func TestTheMayorSettingIsTheYardmasterSetting(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, "mayor", "boss", "me"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Settings(ctx); all["yardmaster"] != "boss" || len(all) != 1 {
		t.Fatalf("settings = %v, want yardmaster boss alone", all)
	}
	if err := s.SetSetting(ctx, "mayor", "", "me"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Settings(ctx); len(all) != 0 {
		t.Fatalf("settings after the unset = %v", all)
	}
}

// A store of schema 9, from when a train was an epic and a car a part,
// comes out of Open with the new types wherever it holds a type: beads,
// routes, flow pointers, a hook's stage map, the filters of a crew member's,
// a user's and a group's subscriptions, and the record of a pack. What the
// operator named stays (the flow web.epic, the label epic), a pointer at
// that flow still finds it, and the events already logged are not
// rewritten. The old words are still read afterwards.
func TestSchema9StoreRenamesEpicsAndParts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetSetting(ctx, "default_kind", "claude", "me"))
	must(s.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "integrators", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "/home/jo/.yardr/scripts/integrate-epic"}}))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}))
	f, err := store.ParseFlow("backlog -> open\nopen -> merged on done\nmerged terminal\n")
	must(err)
	f.Name = "web.epic"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", store.TypeTrain, "web.epic", "me"))
	for _, nb := range []store.NewBead{
		{ID: "web-e", Title: "the feature", Type: store.TypeTrain, Depot: "web", Labels: []string{"epic"}},
		{ID: "web-p", Title: "a piece of it", Type: store.TypeCar, Depot: "web"},
		{ID: "web-t", Title: "a task", Depot: "web"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(err)
	}
	must(s.AddDep(ctx, "web-e", "web-p", store.DepParent, "me"))
	typ := store.TypeTrain
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &typ, Group: "integrators"})
	must(err)
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(err)
	must(s.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u-boss"}, "me"))
	_, err = s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/home/jo/.yardr/linear.secret", As: "boss",
		Map: map[string]string{"train:open": "started", "car:new": "started", "task:new": "started"}}, "me")
	must(err)
	_, err = s.AddUser(ctx, "jo", "me")
	must(err)
	wheres := []map[string]string{
		{"train": "web-e"}, {"type": "car"}, {"kind": "train_branched"}, {"type": "task"},
	}
	for _, who := range []struct{ name, actor string }{{"boss", "me"}, {"jo", "jo"}, {"people", "me"}} {
		for _, w := range wheres {
			must(s.AddSubscription(ctx, who.name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, who.actor))
		}
	}
	must(s.AddSubscription(ctx, "boss", store.Subscription{Source: "github", Scope: store.ScopeAll, Where: map[string]string{"train": "web-e"}}, "me"))
	_, err = s.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter",
		Objects: []store.PackObject{
			{Kind: "route", Stage: "open", Type: "train", Group: "integrators"},
			{Kind: "pointer", Type: "train", Flow: "web.epic"},
			{Kind: "map", Name: "linear", CrewMember: "boss", Map: map[string]string{"train:open": "started"}},
			{Kind: "subscription", CrewMember: "boss", Subscription: &store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"train": "web-e"}}},
		}}, true, "me")
	must(err)
	must(s.Close(ctx))

	// Age it: every word as a build of schema 9 stored it, under that
	// build's stamp. An event of that build has the old kind.
	old := strings.NewReplacer(
		`"type":"train"`, `"type":"epic"`, `"type":"car"`, `"type":"part"`,
		`"train:open"`, `"epic:open"`, `"car:new"`, `"part:new"`,
		`"train":"web-e"`, `"epic":"web-e"`, `"kind":"train_branched"`, `"kind":"epic_branched"`,
	)
	aged := []string{"beads", "routes", "flow_pointers", "hooks", "crew", "users", "groups", "packs"}
	db, err := bolt.Open(path, 0o600, nil)
	must(err)
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range aged {
			b := tx.Bucket([]byte(name))
			rows := map[string]string{}
			_ = b.ForEach(func(k, v []byte) error {
				rows[string(k)] = string(v)
				return nil
			})
			for k, v := range rows {
				if err := b.Delete([]byte(k)); err != nil {
					return err
				}
				if name == "flow_pointers" {
					k = strings.Replace(k, "\x00train", "\x00epic", 1)
				}
				if err := b.Put([]byte(k), []byte(old.Replace(v))); err != nil {
					return err
				}
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 9))
	})
	db.Close()
	must(err)
	before := bucketDump(t, path, append(aged, "events", "named_flows")...)
	for _, want := range []string{"beads", "routes", "flow_pointers", "hooks", "crew", "users", "groups", "packs"} {
		found := false
		for k, v := range before {
			if strings.HasPrefix(k, want+"\x00") && (strings.Contains(v, "epic") || strings.Contains(v, `"part`)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the aged store has no old word in %s", want)
		}
	}

	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		must(err)
		train, err := s.GetBead(ctx, "web-e")
		if err != nil || train.Type != store.TypeTrain || !slices.Equal(train.Labels, []string{"epic"}) {
			t.Fatalf("%s: the train = %+v, %v", pass, train, err)
		}
		if car, err := s.GetBead(ctx, "web-p"); err != nil || car.Type != store.TypeCar {
			t.Fatalf("%s: the car = %+v, %v", pass, car, err)
		}
		if fam, err := s.Family(ctx, "web-p"); err != nil || fam.Train != "web-e" || fam.Base != "yard/web-e" {
			t.Fatalf("%s: the car's family = %+v, %v", pass, fam, err)
		}
		// The pointer is the type's under its new word, at the flow under
		// the name the operator gave it, and that flow governs the train.
		ptrs, err := s.FlowPointers(ctx)
		if err != nil || len(ptrs) != 1 || ptrs[0] != (store.FlowPointer{Depot: "web", Type: store.TypeTrain, Flow: "web.epic"}) {
			t.Fatalf("%s: pointers = %+v, %v", pass, ptrs, err)
		}
		if m, err := s.FlowMap(ctx, "web-e"); err != nil || m == nil || m.Name != "web.epic" {
			t.Fatalf("%s: the train's flow = %+v, %v", pass, m, err)
		}
		routes, err := s.Routes(ctx)
		if err != nil || len(routes) != 1 || routes[0].Type == nil || *routes[0].Type != store.TypeTrain {
			t.Fatalf("%s: routes = %+v, %v", pass, routes, err)
		}
		hooks, err := s.Hooks(ctx)
		if err != nil || len(hooks) != 1 || !maps.Equal(hooks[0].Map, map[string]string{"train:open": "started", "car:new": "started", "task:new": "started"}) {
			t.Fatalf("%s: hooks = %+v, %v", pass, hooks, err)
		}
		subs := func(list []store.Subscription) []string {
			var out []string
			for _, sub := range list {
				out = append(out, sub.String())
			}
			slices.Sort(out)
			return out
		}
		want := []string{"yardr all kind=train_branched", "yardr all train=web-e", "yardr all type=car", "yardr all type=task"}
		boss, err := s.GetCrew(ctx, "boss")
		if err != nil || !slices.Equal(subs(boss.Subscriptions), append([]string{"github all train=web-e"}, want...)) {
			t.Fatalf("%s: the crew member's subscriptions = %v, %v", pass, subs(boss.Subscriptions), err)
		}
		if jo, err := s.GetUser(ctx, "jo"); err != nil || !slices.Equal(subs(jo.Subscriptions), want) {
			t.Fatalf("%s: the user's subscriptions = %v, %v", pass, subs(jo.Subscriptions), err)
		}
		if g, err := s.GetGroup(ctx, "people"); err != nil || !slices.Equal(subs(g.Subscriptions), want) {
			t.Fatalf("%s: the group's subscriptions = %v, %v", pass, subs(g.Subscriptions), err)
		}
		// The group's cmd is the operator's: it still names the old script.
		if g, err := s.GetGroup(ctx, "integrators"); err != nil || g.Config["cmd"] != "/home/jo/.yardr/scripts/integrate-epic" {
			t.Fatalf("%s: the integrators = %+v, %v", pass, g, err)
		}
		packs, err := s.Packs(ctx)
		if err != nil || len(packs) != 1 {
			t.Fatalf("%s: packs = %+v, %v", pass, packs, err)
		}
		o := packs[0].Objects
		if o[0].Type != store.TypeTrain || o[1].Type != store.TypeTrain || o[1].Flow != "web.epic" ||
			!maps.Equal(o[2].Map, map[string]string{"train:open": "started"}) || o[3].Subscription.String() != "yardr all train=web-e" {
			t.Fatalf("%s: the pack's record = %+v", pass, o)
		}
		must(s.Close(ctx))
	}
	if got := schemaOf(t, path); got != 12 {
		t.Fatalf("schema after Open = %d; want 12", got)
	}
	after := bucketDump(t, path, "events", "named_flows")
	added := 0
	for k, v := range after {
		was, ok := before[k]
		switch {
		case ok && was != v:
			t.Errorf("%q changed:\n was %s\n now %s", k, was, v)
		case !ok && strings.HasPrefix(k, "events\x00") && strings.Contains(v, `"types_migrated"`):
			added++
			for _, part := range []string{`"beads":2`, `"routes":1`, `"flow_pointers":1`, `"hooks":1`, `"subscriptions":1`, `"user_subscriptions":1`, `"group_subscriptions":1`, `"packs":1`} {
				if !strings.Contains(v, part) {
					t.Errorf("types_migrated lacks %s: %s", part, v)
				}
			}
		case !ok:
			t.Errorf("%q was added: %s", k, v)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok && (strings.HasPrefix(k, "events\x00") || strings.HasPrefix(k, "named_flows\x00")) {
			t.Errorf("%q is gone", k)
		}
	}
	if added != 1 {
		t.Errorf("%d types_migrated events, want one: the next start adds none", added)
	}
}

// The words a train and a car had until schema 10 are still read where a
// type, a filter or a flow's name is given, and what is stored and printed
// has the new ones.
func TestTheOldTypeWordsAreStillRead(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "integrators", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "true"}}))
	// A flow called web.train is found by a pointer that says web.epic, and
	// the other way round; the pointer is the type's under its new word.
	f, err := store.ParseFlow("backlog -> open\nopen -> merged on done\nmerged terminal\n")
	must(err)
	f.Name = "web.train"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", "epic", "web.epic", "me"))
	if ptrs, err := s.FlowPointers(ctx); err != nil || len(ptrs) != 1 || ptrs[0].Type != store.TypeTrain {
		t.Fatalf("pointers = %+v, %v", ptrs, err)
	}
	train, err := s.CreateBead(ctx, store.NewBead{ID: "web-e", Title: "the feature", Type: "epic", Depot: "web"}, "me")
	if err != nil || train.Type != store.TypeTrain {
		t.Fatalf("created as epic: %+v, %v", train, err)
	}
	car, err := s.CreateBead(ctx, store.NewBead{ID: "web-p", Title: "a piece", Depot: "web"}, "me")
	must(err)
	part := "part"
	if car, err = s.UpdateBead(ctx, car.ID, store.BeadPatch{Type: &part}, store.Expect{}, "me"); err != nil || car.Type != store.TypeCar {
		t.Fatalf("updated to part: %+v, %v", car, err)
	}
	must(s.AddDep(ctx, "web-e", "web-p", store.DepParent, "me"))
	if bs, err := s.ListBeads(ctx, store.BeadFilter{Type: "epic"}); err != nil || len(bs) != 1 || bs[0].ID != "web-e" {
		t.Fatalf("listed by type epic: %+v, %v", bs, err)
	}
	epic := "epic"
	if r, err := s.AddRoute(ctx, store.Route{Stage: "open", Type: &epic, Group: "integrators"}); err != nil || *r.Type != store.TypeTrain {
		t.Fatalf("a route for type epic: %+v, %v", r, err)
	}

	if m, err := s.FlowMap(ctx, "web-e"); err != nil || m == nil || m.Name != "web.train" {
		t.Fatalf("the train's flow = %+v, %v", m, err)
	}
	if err := s.SetFlowPointer(ctx, "web", "epic", "web.nothing", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a pointer at no flow: %v", err)
	}

	// A subscription that names the old key, types or kinds is stored, and
	// removed, as what they are now.
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(err)
	for _, w := range []map[string]string{{"epic": "web-e"}, {"type": "part"}, {"kind": "part_without_epic"}} {
		must(s.AddSubscription(ctx, "boss", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, "me"))
	}
	boss, err := s.GetCrew(ctx, "boss")
	must(err)
	var got []string
	for _, sub := range boss.Subscriptions {
		got = append(got, sub.String())
	}
	if want := []string{"yardr all train=web-e", "yardr all type=car", "yardr all kind=car_without_train"}; !slices.Equal(got, want) {
		t.Fatalf("subscriptions = %v, want %v", got, want)
	}
	if n, err := s.RemoveSubscriptions(ctx, "boss", store.SourceYardr, "", map[string]string{"epic": "web-e"}, "me"); err != nil || n != 1 {
		t.Fatalf("removed by the old key: %d, %v", n, err)
	}
	// The car's events reach the subscription that said type=part.
	must(s.AddNote(ctx, "web-p", "a note", "me"))
	if page, err := s.Inbox(ctx, "boss", 0); err != nil || page.Total == 0 {
		t.Fatalf("boss's inbox = %+v, %v", page, err)
	}

	must(s.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u-boss"}, "me"))
	h, err := s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s", As: "boss",
		Map: map[string]string{"epic:open": "started", "part:new": "started"}}, "me")
	if err != nil || !maps.Equal(h.Map, map[string]string{"train:open": "started", "car:new": "started"}) {
		t.Fatalf("a map that says epic and part: %+v, %v", h, err)
	}
}

// A store of schema 10, from when the crew were the residents, comes out of
// Open with the word renamed in every place the store holds it: the bucket
// of the records, the label of a journal and the scope of a memory, a kind
// in --on and in the filter of a crew member's, a user's and a group's
// subscription, the field that names the crew member in a thread and in
// the record of an imported pack, and the path of an env_file under
// <home>/residents, which is <home>/crew with a link of the old name. The
// events already logged are not rewritten, and one of an old kind is still
// in the inbox of whoever hears of the kind.
func TestSchema10StoreRenamesResidents(t *testing.T) {
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
	envFile := filepath.Join(home, "crew", "boss.env")
	s, err := store.Open(path)
	must(err)
	must(s.SetSetting(ctx, "default_kind", "claude", "me"))
	must(s.PutDepot(ctx, store.Depot{Name: "web", Path: t.TempDir()}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}))
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", On: []string{"crew_gave_up", "held"},
		Config: map[string]any{"kind": "claude", "env_file": envFile}}, "me")
	must(err)
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "manual", Config: map[string]any{"env_file": "/etc/yard/clerk.env"}}, "me")
	must(err)
	_, err = s.AddUser(ctx, "jo", "me")
	must(err)
	wheres := []map[string]string{{"kind": "crew_started"}, {"label": "crew:boss"}, {"type": "task"}}
	for _, who := range []struct{ name, actor string }{{"boss", "me"}, {"jo", "jo"}, {"people", "me"}} {
		for _, w := range wheres {
			must(s.AddSubscription(ctx, who.name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, who.actor))
		}
	}
	_, err = s.Remember(ctx, store.NewMemory{Title: "deploys are frozen", Scope: "crew:boss"}, "boss")
	must(err)
	must(s.FollowThread(ctx, "boss", "linear", "ENG-1", "me"))
	_, err = s.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter",
		Crew: map[string]string{"yardmaster": "boss"},
		Objects: []store.PackObject{
			{Kind: "crew", Name: "boss"},
			{Kind: "map", Name: "linear", CrewMember: "boss", Map: map[string]string{"task:new": "started"}},
			{Kind: "subscription", CrewMember: "boss", Subscription: &store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"kind": "crew_started"}}},
		}}, true, "me")
	must(err)
	// What a build of schema 10 logged when a start was given up: boss
	// hears of the kind.
	must(s.LogEvent(ctx, "clerk-journal", "resident_gave_up", "yard", map[string]any{"resident": "clerk", "reason": "not logged in"}))
	must(s.Close(ctx))

	// Age it: every word as a build of schema 10 stored it, in the bucket
	// and the directory it had, under that build's stamp.
	oldEnv := filepath.Join(home, "residents", "boss.env")
	old := strings.NewReplacer(
		`"scope:crew:boss"`, `"scope:resident:boss"`, `"crew:boss"`, `"resident:boss"`,
		`"crew_started"`, `"resident_started"`, `"crew_gave_up"`, `"resident_gave_up"`,
		`"crew":{`, `"residents":{`, `"crew":"boss"`, `"resident":"boss"`, `"kind":"crew"`, `"kind":"resident"`,
		envFile, oldEnv,
	)
	aged := map[string]string{"beads": "beads", "crew": "residents", "users": "users", "groups": "groups", "packs": "packs", "threads": "threads"}
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
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 10))
	})
	db.Close()
	must(err)
	must(os.Mkdir(filepath.Dir(oldEnv), 0o700))
	must(os.WriteFile(oldEnv, []byte("LINEAR_API_KEY=k\n"), 0o600))
	was := bucketDump(t, path, "beads", "residents", "users", "groups", "packs", "threads")
	for _, want := range aged {
		found := false
		for k, v := range was {
			found = found || strings.HasPrefix(k, want+"\x00") && strings.Contains(v, "resident")
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
		boss, err := s.GetCrew(ctx, "boss")
		must(err)
		if !slices.Equal(boss.On, []string{"crew_gave_up", "held"}) || boss.Config["env_file"] != envFile {
			t.Fatalf("%s: boss hears of %v and reads %v", pass, boss.On, boss.Config["env_file"])
		}
		if clerk, err := s.GetCrew(ctx, "clerk"); err != nil || clerk.Config["env_file"] != "/etc/yard/clerk.env" {
			t.Fatalf("%s: an env_file outside the home = %+v, %v", pass, clerk, err)
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
			if want := []string{"yardr all kind=crew_started", "yardr all label=crew:boss", "yardr all type=task"}; !slices.Equal(got, want) {
				t.Fatalf("%s: %s's subscriptions = %q, want %q", pass, who, got, want)
			}
		}
		if j, err := s.GetBead(ctx, boss.Journal); err != nil || !slices.Equal(j.Labels, []string{"crew:boss"}) {
			t.Fatalf("%s: the journal = %+v, %v", pass, j, err)
		}
		for _, scope := range []string{"crew:boss", "resident:boss"} {
			ms, err := s.Recall(ctx, scope, "")
			if err != nil || len(ms) != 1 || ms[0].Scope != "crew:boss" {
				t.Fatalf("%s: recall %s = %+v, %v", pass, scope, ms, err)
			}
		}
		if labelled, err := s.ListBeads(ctx, store.BeadFilter{Label: "resident:boss", Type: store.TypeJournal}); err != nil || len(labelled) != 1 {
			t.Fatalf("%s: beads of the old label = %+v, %v", pass, labelled, err)
		}
		if ths, err := s.Threads(ctx, "boss"); err != nil || len(ths) != 1 || ths[0].CrewMember != "boss" || ths[0].Thread != "ENG-1" {
			t.Fatalf("%s: threads = %+v, %v", pass, ths, err)
		}
		packs, err := s.Packs(ctx)
		must(err)
		if len(packs) != 1 || packs[0].Crew["yardmaster"] != "boss" || len(packs[0].Objects) != 3 {
			t.Fatalf("%s: the pack's record = %+v", pass, packs)
		}
		for _, o := range packs[0].Objects {
			switch {
			case o.Kind == "crew" && o.Name == "boss":
			case o.Kind == "map" && o.CrewMember == "boss":
			case o.Kind == "subscription" && o.CrewMember == "boss" && o.Subscription.Where["kind"] == "crew_started":
			default:
				t.Fatalf("%s: the pack's object = %+v", pass, o)
			}
		}
		// The start given up before the upgrade is still in the inbox.
		page, err := s.Inbox(ctx, "boss", 0)
		must(err)
		if len(page.Events) != 1 || page.Events[0].Kind != "resident_gave_up" || store.EventCrew(page.Events[0].Data) != "clerk" {
			t.Fatalf("%s: boss's inbox = %+v", pass, page.Events)
		}
		must(s.Close(ctx))

		if got := schemaOf(t, path); got != 12 {
			t.Fatalf("%s: schema = %d; want 12", pass, got)
		}
		if st, err := os.Lstat(filepath.Dir(oldEnv)); err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: the old directory is no link: %v, %v", pass, st, err)
		}
		for _, p := range []string{envFile, oldEnv} {
			if b, err := os.ReadFile(p); err != nil || string(b) != "LINEAR_API_KEY=k\n" {
				t.Fatalf("%s: %s = %q, %v", pass, p, b, err)
			}
		}
		now := bucketDump(t, path, "beads", "crew", "users", "groups", "packs", "threads")
		for k, v := range now {
			if strings.Contains(v, "resident") {
				t.Fatalf("%s: %q still says %s", pass, k, v)
			}
		}
		db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
		must(err)
		_ = db.View(func(tx *bolt.Tx) error {
			if tx.Bucket([]byte("residents")) != nil {
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
		if len(added) != 1 || !strings.Contains(added[0], `"kind":"crew_migrated"`) || !strings.Contains(added[0], `"from":"residents"`) {
			t.Fatalf("%s: events added = %q, want one crew_migrated", pass, added)
		}
		for _, want := range []string{`"crew":1`, `"beads":2`, `"threads":1`, `"packs":1`, `"user_subscriptions":1`, `"group_subscriptions":1`} {
			if !strings.Contains(added[0], want) {
				t.Fatalf("%s: crew_migrated = %s, want %s in it", pass, added[0], want)
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
func TestSchema10StoreLeavesTwoDirectories(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "yard.db")
	oldEnv := filepath.Join(home, "residents", "boss.env")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual", Config: map[string]any{"env_file": oldEnv}}, "me"); err != nil {
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
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 10))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"residents", "crew"} {
		if err := os.Mkdir(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if s, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	boss, err := s.GetCrew(ctx, "boss")
	if err != nil || boss.Config["env_file"] != oldEnv {
		t.Fatalf("env_file = %v, %v; want it as it was", boss, err)
	}
	if st, err := os.Lstat(filepath.Dir(oldEnv)); err != nil || !st.IsDir() {
		t.Fatalf("the old directory = %v, %v; want it left", st, err)
	}
}
