package store_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/store"
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
	if got := schemaOf(t, fixture); got != 20 {
		t.Fatalf("a new store is stamped %d; want 20", got)
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
			if got := schemaOf(t, path); got != 20 {
				t.Fatalf("schema after Open = %d; want 20", got)
			}
			got := indexKeys(t, path)
			for _, name := range indexBuckets {
				if !slices.Equal(got[name], want[name]) {
					t.Errorf("%s after migration = %v\nwant %v", name, got[name], want[name])
				}
			}
			if got := schemaOf(t, fixture); got != 20 || !slices.Equal(indexKeys(t, fixture)["inbox"], want["inbox"]) {
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
			if got := schemaOf(t, path); got != 20 {
				t.Fatalf("schema after Open = %d; want 20", got)
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

// A store of schema 9, from when a train was an epic and a wagon a part,
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
		Config: map[string]any{"cmd": "/home/jo/.yardr/scripts/integrate-epic"}}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}, "me"))
	f, err := store.ParseFlow("backlog -> open\nopen -> merged on done\nmerged terminal\n")
	must(err)
	f.Name = "web.epic"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", store.TypeTrain, "web.epic", "me"))
	for _, nb := range []store.NewBead{
		{ID: "web-e", Title: "the feature", Type: store.TypeTrain, Depot: "web", Labels: []string{"epic"}},
		{ID: "web-p", Title: "a piece of it", Type: store.TypeWagon, Depot: "web"},
		{ID: "web-t", Title: "a task", Depot: "web"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(err)
	}
	must(s.AddDep(ctx, "web-e", "web-p", store.DepParent, "", "", "me"))
	typ := store.TypeTrain
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &typ, Group: "integrators"}, "me")
	must(err)
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(err)
	must(s.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u-boss"}, "me"))
	_, err = s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/home/jo/.yardr/linear.secret", As: "boss",
		Map: map[string]string{"train:open": "started", "wagon:new": "started", "task:new": "started"}}, "me")
	must(err)
	_, err = s.AddUser(ctx, "jo", "me")
	must(err)
	wheres := []map[string]string{
		{"train": "web-e"}, {"type": "wagon"}, {"kind": "train_branched"}, {"type": "task"},
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
		`"type":"train"`, `"type":"epic"`, `"type":"wagon"`, `"type":"part"`,
		`"train:open"`, `"epic:open"`, `"wagon:new"`, `"part:new"`,
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
		if wagon, err := s.GetBead(ctx, "web-p"); err != nil || wagon.Type != store.TypeWagon {
			t.Fatalf("%s: the wagon = %+v, %v", pass, wagon, err)
		}
		if fam, err := s.Family(ctx, "web-p"); err != nil || fam.Train != "web-e" || fam.Base != "yard/web-e" {
			t.Fatalf("%s: the wagon's family = %+v, %v", pass, fam, err)
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
		if err != nil || len(hooks) != 1 || !maps.Equal(hooks[0].Map, map[string]string{"train:open": "started", "wagon:new": "started", "task:new": "started"}) {
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
		want := []string{"yardr all kind=train_branched", "yardr all train=web-e", "yardr all type=task", "yardr all type=wagon"}
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
	if got := schemaOf(t, path); got != 20 {
		t.Fatalf("schema after Open = %d; want 20", got)
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

// A store of schema 10, from the hours main called a wagon a car, comes out
// of Open with wagon wherever it holds the type: beads, routes, flow
// pointers (key and record), a hook's stage map, the filters of a crew
// member's, a user's and a group's subscriptions (type=car and
// kind=car_without_train), and the record of a pack. A pointer at a flow
// named web.car finds web.wagon, and the events already logged are not
// rewritten. Afterwards car is no word for a wagon: no release had it.
func TestSchema10StoreRenamesCars(t *testing.T) {
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
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "true"}}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}, "me"))
	f, err := store.ParseFlow("backlog -> new\nnew -> merged on done\nmerged terminal\n")
	must(err)
	f.Name = "web.wagon"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", store.TypeWagon, "web.wagon", "me"))
	for _, nb := range []store.NewBead{
		{ID: "web-e", Title: "the feature", Type: store.TypeTrain, Depot: "web"},
		{ID: "web-p", Title: "a piece of it", Type: store.TypeWagon, Depot: "web", Labels: []string{"car"}},
		{ID: "web-t", Title: "a task", Depot: "web"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(err)
	}
	must(s.AddDep(ctx, "web-e", "web-p", store.DepParent, "", "", "me"))
	typ := store.TypeWagon
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Type: &typ, Group: "builders"}, "me")
	must(err)
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(err)
	must(s.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u-boss"}, "me"))
	_, err = s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/home/jo/.yardr/linear.secret", As: "boss",
		Map: map[string]string{"train:open": "started", "wagon:new": "started"}}, "me")
	must(err)
	_, err = s.AddUser(ctx, "jo", "me")
	must(err)
	wheres := []map[string]string{{"type": "wagon"}, {"kind": "wagon_without_train"}, {"type": "train"}}
	for _, who := range []struct{ name, actor string }{{"boss", "me"}, {"jo", "jo"}, {"people", "me"}} {
		for _, w := range wheres {
			must(s.AddSubscription(ctx, who.name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, who.actor))
		}
	}
	_, err = s.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter",
		Objects: []store.PackObject{
			{Kind: "route", Stage: "new", Type: "wagon", Group: "builders"},
			{Kind: "pointer", Type: "wagon", Flow: "web.wagon"},
			{Kind: "map", Name: "linear", CrewMember: "boss", Map: map[string]string{"wagon:new": "started"}},
			{Kind: "subscription", CrewMember: "boss", Subscription: &store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"type": "wagon"}}},
		}}, true, "me")
	must(err)
	// What that build logged for a car with no train.
	must(s.LogEvent(ctx, "web-t", "car_without_train", "yardr", map[string]any{"reason": "a car with no train is not started"}))
	must(s.Close(ctx))

	// Age it: every word as a build of schema 10 stored it, under that
	// build's stamp. The pointer names the flow as that build would have.
	old := strings.NewReplacer(
		`"type":"wagon"`, `"type":"car"`, `"wagon:new"`, `"car:new"`,
		`"kind":"wagon_without_train"`, `"kind":"car_without_train"`, `"flow":"web.wagon"`, `"flow":"web.car"`,
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
					k = strings.Replace(k, "\x00wagon", "\x00car", 1)
				}
				if err := b.Put([]byte(k), []byte(old.Replace(v))); err != nil {
					return err
				}
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 10))
	})
	db.Close()
	must(err)
	before := bucketDump(t, path, append(aged, "events", "named_flows")...)
	for _, want := range aged {
		found := false
		for k, v := range before {
			if strings.HasPrefix(k, want+"\x00") && (strings.Contains(v, `"car"`) || strings.Contains(v, `"car:`) || strings.Contains(v, `"car_`)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the aged store has no car in %s", want)
		}
	}
	if _, ok := before["flow_pointers\x00web\x00car"]; !ok {
		t.Fatalf("the aged store has no pointer under the key of type car")
	}

	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		must(err)
		wagon, err := s.GetBead(ctx, "web-p")
		if err != nil || wagon.Type != store.TypeWagon || !slices.Equal(wagon.Labels, []string{"car"}) {
			t.Fatalf("%s: the wagon = %+v, %v", pass, wagon, err)
		}
		if train, err := s.GetBead(ctx, "web-e"); err != nil || train.Type != store.TypeTrain {
			t.Fatalf("%s: the train = %+v, %v", pass, train, err)
		}
		if fam, err := s.Family(ctx, "web-p"); err != nil || fam.Train != "web-e" || fam.Base != "yard/web-e" {
			t.Fatalf("%s: the wagon's family = %+v, %v", pass, fam, err)
		}
		// The pointer is the type's under its new word and still says
		// web.car, which finds the flow web.wagon: it governs the wagon.
		ptrs, err := s.FlowPointers(ctx)
		if err != nil || len(ptrs) != 1 || ptrs[0] != (store.FlowPointer{Depot: "web", Type: store.TypeWagon, Flow: "web.car"}) {
			t.Fatalf("%s: pointers = %+v, %v", pass, ptrs, err)
		}
		if m, err := s.FlowMap(ctx, "web-p"); err != nil || m == nil || m.Name != "web.wagon" {
			t.Fatalf("%s: the wagon's flow = %+v, %v", pass, m, err)
		}
		routes, err := s.Routes(ctx)
		if err != nil || len(routes) != 1 || routes[0].Type == nil || *routes[0].Type != store.TypeWagon {
			t.Fatalf("%s: routes = %+v, %v", pass, routes, err)
		}
		hooks, err := s.Hooks(ctx)
		if err != nil || len(hooks) != 1 || !maps.Equal(hooks[0].Map, map[string]string{"train:open": "started", "wagon:new": "started"}) {
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
		want := []string{"yardr all kind=wagon_without_train", "yardr all type=train", "yardr all type=wagon"}
		if boss, err := s.GetCrew(ctx, "boss"); err != nil || !slices.Equal(subs(boss.Subscriptions), want) {
			t.Fatalf("%s: the crew member's subscriptions = %v, %v", pass, subs(boss.Subscriptions), err)
		}
		if jo, err := s.GetUser(ctx, "jo"); err != nil || !slices.Equal(subs(jo.Subscriptions), want) {
			t.Fatalf("%s: the user's subscriptions = %v, %v", pass, subs(jo.Subscriptions), err)
		}
		if g, err := s.GetGroup(ctx, "people"); err != nil || !slices.Equal(subs(g.Subscriptions), want) {
			t.Fatalf("%s: the group's subscriptions = %v, %v", pass, subs(g.Subscriptions), err)
		}
		packs, err := s.Packs(ctx)
		if err != nil || len(packs) != 1 {
			t.Fatalf("%s: packs = %+v, %v", pass, packs, err)
		}
		o := packs[0].Objects
		if o[0].Type != store.TypeWagon || o[1].Type != store.TypeWagon || o[1].Flow != "web.car" ||
			!maps.Equal(o[2].Map, map[string]string{"wagon:new": "started"}) || o[3].Subscription.String() != "yardr all type=wagon" {
			t.Fatalf("%s: the pack's record = %+v", pass, o)
		}
		must(s.Close(ctx))
	}
	if got := schemaOf(t, path); got != 20 {
		t.Fatalf("schema after Open = %d; want 20", got)
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
			for _, part := range []string{`"beads":1`, `"routes":1`, `"flow_pointers":1`, `"hooks":1`, `"subscriptions":1`, `"user_subscriptions":1`, `"group_subscriptions":1`, `"packs":1`, `"from":"type car"`} {
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

	// No release had the word, so it names no wagon now: a bead of type car
	// is one of a type yardr knows nothing of, like any other word.
	s, err = store.Open(path)
	must(err)
	defer s.Close(ctx)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "typed with the old word", Type: "car", Depot: "web", Stage: "new"}, "me")
	if err != nil || b.Type != "car" {
		t.Fatalf("a bead of type car = %+v, %v; want the type kept as given, as for any word", b, err)
	}
	// A store older than the car has none to rename: there a type car is
	// the operator's own word.
	old9 := filepath.Join(t.TempDir(), "yard.db")
	s9, err := store.Open(old9)
	must(err)
	must(s9.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	_, err = s9.CreateBead(ctx, store.NewBead{ID: "web-c", Title: "a rail car", Type: "car", Depot: "web"}, "me")
	must(err)
	must(s9.Close(ctx))
	db, err = bolt.Open(old9, 0o600, nil)
	must(err)
	must(db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 9))
	}))
	db.Close()
	s9, err = store.Open(old9)
	must(err)
	defer s9.Close(ctx)
	if b, err := s9.GetBead(ctx, "web-c"); err != nil || b.Type != "car" {
		t.Fatalf("a schema-9 store's bead of type car = %+v, %v; want it left", b, err)
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
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}, "me"))
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", On: []string{"crew_gave_up", "held"},
		Config: map[string]any{"kind": "claude", "env_file": envFile}}, "me")
	must(err)
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"env_file": "/etc/yard/clerk.env"}}, "me")
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
		if ms, err := s.Recall(ctx, "crew:boss", ""); err != nil || len(ms) != 1 || ms[0].Scope != "crew:boss" {
			t.Fatalf("%s: recall crew:boss = %+v, %v", pass, ms, err)
		}
		if labelled, err := s.ListBeads(ctx, store.BeadFilter{Label: "crew:boss", Type: store.TypeJournal}); err != nil || len(labelled) != 1 {
			t.Fatalf("%s: beads of the journal's label = %+v, %v", pass, labelled, err)
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
		if len(page.Events) != 1 || page.Events[0].Kind != "resident_gave_up" || page.Events[0].Data["resident"] != "clerk" {
			t.Fatalf("%s: boss's inbox = %+v", pass, page.Events)
		}
		must(s.Close(ctx))

		if got := schemaOf(t, path); got != 20 {
			t.Fatalf("%s: schema = %d; want 20", pass, got)
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
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Config: map[string]any{"env_file": oldEnv}}, "me"); err != nil {
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

// A store of schema 18, from when yardr ran a depot's check itself, comes
// out of Open without what named that check: the four fields are gone from
// each depot's record and gate_slots from the settings. A depot whose check
// was a command in the store and that had no test command is tested with
// that command now; one that had a test command keeps it. One event says
// what went, the next start adds none, and nothing else changes.
func TestSchema18StoreDropsTheChecks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []store.Depot{
		{Name: "api", Path: "/src/api", Base: "main"},
		{Name: "docs", Path: "/src/docs", Kind: store.DepotDir},
		{Name: "plain", Path: "/src/plain", Base: "origin/main", Test: "make test"},
		{Name: "web", Path: "/src/web", Base: "main", Test: "npm test"},
	} {
		if err := s.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "a bead"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age it: the records as a build of schema 18 stored them, under that
	// build's stamp. plain is a depot that never had a check in the store.
	old := map[string]string{
		"api":  `{"name":"api","path":"/src/api","kind":"git","base":"main","check":"make check","check_light":"make vet","light_slot":true}`,
		"docs": `{"name":"docs","path":"/src/docs","kind":"dir","base":"","allow_no_check":true}`,
		"web":  `{"name":"web","path":"/src/web","kind":"git","base":"main","check":"npm run ci","test":"npm test"}`,
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for name, rec := range old {
			if err := tx.Bucket([]byte("depots")).Put([]byte(name), []byte(rec)); err != nil {
				return err
			}
		}
		if err := tx.Bucket([]byte("settings")).Put([]byte("gate_slots"), []byte(`"3"`)); err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 18))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	kept := []string{"beads", "routes", "groups", "crew", "users"}
	before := bucketDump(t, path, append(kept, "events", "settings", "depots")...)

	want := map[string]store.Depot{
		"api":   {Name: "api", Path: "/src/api", Kind: store.DepotGit, Base: "main", Test: "make check"},
		"docs":  {Name: "docs", Path: "/src/docs", Kind: store.DepotDir},
		"plain": {Name: "plain", Path: "/src/plain", Kind: store.DepotGit, Base: "origin/main", Test: "make test"},
		"web":   {Name: "web", Path: "/src/web", Kind: store.DepotGit, Base: "main", Test: "npm test"},
	}
	var first map[string]string
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		depots, err := s.Depots(ctx)
		if err != nil || len(depots) != len(want) {
			t.Fatalf("%s: depots = %+v, %v", pass, depots, err)
		}
		for _, d := range depots {
			if d != want[d.Name] {
				t.Errorf("%s: depot %s = %+v, want %+v", pass, d.Name, d, want[d.Name])
			}
		}
		all, err := s.Settings(ctx)
		if _, has := all["gate_slots"]; err != nil || has || all["default_kind"] != "claude" {
			t.Fatalf("%s: settings = %v, %v; want no gate_slots", pass, all, err)
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, path); got != 20 {
			t.Fatalf("%s: schema = %d; want 20", pass, got)
		}
		now := bucketDump(t, path, append(kept, "events", "settings", "depots")...)
		var added []string
		for k, v := range now {
			was, ok := before[k]
			switch {
			case strings.HasPrefix(k, "depots\x00") && k != "depots\x00plain":
				for _, f := range []string{`"check"`, `"check_light"`, `"light_slot"`, `"allow_no_check"`} {
					if strings.Contains(v, f) {
						t.Errorf("%s: %q still holds %s: %s", pass, k, f, v)
					}
				}
			case ok && was != v:
				t.Errorf("%s: %q changed:\n was %s\n now %s", pass, k, was, v)
			case !ok && strings.HasPrefix(k, "events\x00") && strings.Contains(v, `"depots_migrated"`):
				added = append(added, v)
			case !ok:
				t.Errorf("%s: %q was added: %s", pass, k, v)
			}
		}
		for k := range before {
			if _, ok := now[k]; !ok && k != "settings\x00gate_slots" {
				t.Errorf("%s: %q is gone", pass, k)
			}
		}
		if len(added) != 1 {
			t.Fatalf("%s: %d depots_migrated events, want 1: %v", pass, len(added), added)
		}
		for _, say := range []string{`"from":"checks"`, `"gate_slots":"3"`, `"test":{"api":"make check"}`,
			`"api":{"check":"make check","check_light":"make vet","light_slot":true}`, `"docs":{"allow_no_check":true}`, `"web":{"check":"npm run ci"}`} {
			if !strings.Contains(added[0], say) {
				t.Errorf("%s: depots_migrated = %s, want %s in it", pass, added[0], say)
			}
		}
		if first == nil {
			first = now
		} else if !maps.Equal(first, now) {
			t.Fatalf("the next start changed what the upgrade wrote")
		}
	}

	// A store that held none of it is told nothing.
	fresh := filepath.Join(t.TempDir(), "yard.db")
	if s, err = store.Open(fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: "/src/web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err = bolt.Open(fresh, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 18))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	was := bucketDump(t, fresh, "events", "depots", "settings")
	if s, err = store.Open(fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if now := bucketDump(t, fresh, "events", "depots", "settings"); !maps.Equal(was, now) || schemaOf(t, fresh) != 20 {
		t.Fatalf("a store with no check changed, or is not stamped 20 (%d):\n was %v\n now %v", schemaOf(t, fresh), was, now)
	}
}

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
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}, "me"))
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
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Depot: &web, Group: "people"}, "me")
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
		// What was logged before the upgrade is still in the inbox, as it
		// was logged: under the word a depot had then.
		page, err := s.Inbox(ctx, "boss", 0)
		must(err)
		if last := page.Events[len(page.Events)-1]; last.Kind != "bad_rig" || last.Data["rig"] != "web" {
			t.Fatalf("%s: the last of boss's inbox = %+v", pass, last)
		}
		must(s.Close(ctx))

		if got := schemaOf(t, path); got != 20 {
			t.Fatalf("%s: schema = %d; want 20", pass, got)
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

// A depot's pointer at a flow named in the words of before schema 10
// (train -> web.epic, from when the repository's file was web.epic.flow) is
// rewritten when the store opens, so the depot's flows resolve: v0.7.0
// stopped looking such a name up under both words and migrated nothing
// (yardr-y0hk). A pointer at a flow the store holds under the old name
// stays, and a second open writes nothing.
func TestOpenRenamesFlowPointersAtEpicAndPart(t *testing.T) {
	ctx := context.Background()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/web.flow":        repoDefault,
		".yardr/flows/web.train.flow":  repoAlt,
		".yardr/flows/web.wagon.flow":  repoDecision,
		".yardr/flows/own.train.flow":  repoAlt,
		".yardr/flows/type/train.flow": repoAlt,
	})
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "api", "own", "kept"} {
		if err := s.PutDepot(ctx, store.Depot{Name: name, Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// kept.epic is a flow of the store, as migrateFlows named a depot's
	// flow for its epics.
	putNamed(t, s, "kept.epic", repoAlt)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// As a yard from before v0.7.0 holds them, under v0.7.0's stamp.
	old := map[string]string{
		"web\x00":       `{"depot":"web","flow":"web"}`,
		"web\x00train":  `{"depot":"web","type":"train","flow":"web.epic"}`,
		"web\x00wagon":  `{"depot":"web","type":"wagon","flow":"web.part"}`,
		"own\x00":       `{"depot":"own","flow":"own.epic"}`,
		"api\x00":       `{"depot":"api","flow":"type/epic"}`,
		"api\x00wagon":  `{"depot":"api","type":"wagon","flow":"type/part"}`,
		"kept\x00train": `{"depot":"kept","type":"train","flow":"kept.epic"}`,
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for k, rec := range old {
			if err := tx.Bucket([]byte("flow_pointers")).Put([]byte(k), []byte(rec)); err != nil {
				return err
			}
		}
		return nil
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	want := []store.FlowPointer{
		{Depot: "api", Flow: "type/train"},
		{Depot: "api", Type: "wagon", Flow: "type/wagon"},
		{Depot: "kept", Type: "train", Flow: "kept.epic"},
		{Depot: "own", Flow: "own.train"},
		{Depot: "web", Flow: "web"},
		{Depot: "web", Type: "train", Flow: "web.train"},
		{Depot: "web", Type: "wagon", Flow: "web.wagon"},
	}
	var first map[string]string
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		ptrs, err := s.FlowPointers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ptrs, want) {
			t.Fatalf("%s: pointers = %+v, want %+v", pass, ptrs, want)
		}
		ownFlows, err := s.DepotFlows(ctx, "own")
		if err != nil {
			t.Fatalf("%s: flows of own: %v", pass, err)
		}
		if len(ownFlows) != 1 || ownFlows[0].Flow.Name != "own.train" {
			t.Fatalf("%s: flows of own = %+v, want own.train", pass, ownFlows)
		}
		// The flows of the depot resolve: its own, and the two its types
		// point at, from the files under their new names.
		flows, err := s.DepotFlows(ctx, "web")
		if err != nil {
			t.Fatalf("%s: flows of web: %v", pass, err)
		}
		got := map[string]string{}
		for _, f := range flows {
			got[f.Type] = f.Flow.Name
		}
		if w := map[string]string{"": "web", "train": "web.train", "wagon": "web.wagon"}; !maps.Equal(got, w) {
			t.Fatalf("%s: flows of web = %v, want %v", pass, got, w)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a train", Depot: "web", Type: "train"}, "me")
		if err != nil {
			t.Fatalf("%s: a train of web: %v", pass, err)
		}
		if name, _ := governing(t, s, b.ID); name != "web.train" {
			t.Fatalf("%s: a train of web follows %q, want web.train", pass, name)
		}
		// The pointer at the store's kept.epic names that flow still.
		k, err := s.CreateBead(ctx, store.NewBead{Title: "a train", Depot: "kept", Type: "train"}, "me")
		if err != nil {
			t.Fatalf("%s: a train of kept: %v", pass, err)
		}
		if name, _ := governing(t, s, k.ID); name != "kept.epic" {
			t.Fatalf("%s: a train of kept follows %q, want kept.epic", pass, name)
		}
		evs, err := s.EventsOfKind(ctx, "flows_migrated", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 {
			t.Fatalf("%s: %d flows_migrated events, want 1: %+v", pass, len(evs), evs)
		}
		raw, err := json.Marshal(evs[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		data := string(raw)
		for _, part := range []string{
			`"depot web type train":{"flow":"web.train","was":"web.epic"}`,
			`"depot web type wagon":{"flow":"web.wagon","was":"web.part"}`,
			`"depot api":{"flow":"type/train","was":"type/epic"}`,
			`"depot api type wagon":{"flow":"type/wagon","was":"type/part"}`,
			`"depot own":{"flow":"own.train","was":"own.epic"}`,
			`"from":"types epic and part"`,
		} {
			if !strings.Contains(data, part) {
				t.Errorf("%s: flows_migrated = %s, want %s in it", pass, data, part)
			}
		}
		if strings.Contains(data, "kept") {
			t.Errorf("%s: flows_migrated names the pointer it left: %s", pass, data)
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		dump := bucketDump(t, path, "flow_pointers")
		if first == nil {
			first = dump
		} else if !maps.Equal(dump, first) {
			t.Fatalf("%s rewrote the pointers:\n%v\nwas\n%v", pass, dump, first)
		}
	}
}

// A bead's pin at a flow named in the words of before schema 10 (flow=
// web.epic, from when the repository's file was web.epic.flow) is rewritten
// when the store opens, as a depot's pointer is, so the bead's flow
// resolves: v0.7.0 stopped looking such a name up under both words
// (yardr-xwdm). A pin at a flow the store holds under the old name stays,
// as does a closed bead's, and a second open writes nothing.
func TestOpenRenamesBeadPinsAtEpicAndPart(t *testing.T) {
	ctx := context.Background()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/web.flow":       repoDefault,
		".yardr/flows/web.train.flow": repoAlt,
		".yardr/flows/web.wagon.flow": repoDecision,
	})
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, s, "kept.epic", repoAlt)
	// The pin each bead has in the store file: the CLI refuses a pin at a
	// flow that does not exist.
	pins := map[string]string{}
	for _, pin := range []string{"web.epic", "web.part", "kept.epic", "closed", ""} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "pinned " + pin, Depot: "web"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if pin == "closed" {
			if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
				t.Fatal(err)
			}
			pin = "web.epic"
		}
		pins[b.ID] = pin
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		recs := map[string][]byte{}
		err := tx.Bucket([]byte("beads")).ForEach(func(k, v []byte) error {
			var rec map[string]any
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			id, _ := rec["id"].(string)
			if pins[id] == "" {
				return nil
			}
			rec["metadata"] = map[string]any{"flow": pins[id], "other": "kept"}
			raw, err := json.Marshal(rec)
			recs[string(k)] = raw
			return err
		})
		if err != nil {
			return err
		}
		if len(recs) != 4 {
			return fmt.Errorf("%d bead records to pin, want 4", len(recs))
		}
		for k, raw := range recs {
			if err := tx.Bucket([]byte("beads")).Put([]byte(k), raw); err != nil {
				return err
			}
		}
		return nil
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"web.epic": "web.train", "web.part": "web.wagon", "kept.epic": "kept.epic", "": ""}
	var first map[string]string
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		var migrated []string
		for id, pin := range pins {
			b, err := s.GetBead(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			got := store.PinnedFlow(b)
			if b.Status == "closed" {
				if got != pin {
					t.Errorf("%s: the closed bead pins %q, want %q left", pass, got, pin)
				}
				continue
			}
			if got != want[pin] {
				t.Errorf("%s: the bead pinned to %q pins %q, want %q", pass, pin, got, want[pin])
			}
			if pin == "" {
				continue
			}
			if b.Metadata["other"] != "kept" {
				t.Errorf("%s: the bead pinned to %q has metadata %v, want the rest kept", pass, pin, b.Metadata)
			}
			// The pin resolves: to the repository's file under its new
			// name, or to the store's flow under the old one.
			if name, src := governing(t, s, id); name != want[pin] || !strings.HasPrefix(src, "pin") {
				t.Errorf("%s: the bead pinned to %q follows %q (%s), want %q by its pin", pass, pin, name, src, want[pin])
			}
			if want[pin] != pin {
				migrated = append(migrated, fmt.Sprintf(`%q:{"flow":%q,"was":%q}`, id, want[pin], pin))
			}
		}
		evs, err := s.EventsOfKind(ctx, "flows_migrated", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 {
			t.Fatalf("%s: %d flows_migrated events, want 1: %+v", pass, len(evs), evs)
		}
		raw, err := json.Marshal(evs[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(migrated)
		if w := `{"beads":{` + strings.Join(migrated, ",") + `},"from":"types epic and part"}`; string(raw) != w {
			t.Errorf("%s: flows_migrated = %s, want %s", pass, raw, w)
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		dump := bucketDump(t, path, "beads")
		if first == nil {
			first = dump
		} else if !maps.Equal(dump, first) {
			t.Fatalf("%s rewrote the beads:\n%v\nwas\n%v", pass, dump, first)
		}
	}
}

// A store of schema 19, from when the dispatcher's rule was the
// github_rule, comes out of Open with the rule under its own word: a crew
// member's --on and the kind= of subscriptions name the hook_rule, one
// event says what was renamed, and what the log holds under the old word
// reads as the new one. The next start changes nothing further.
func TestSchema19StoreRenamesHookRule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", On: []string{"github_rule", "created"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", On: []string{"created"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	rule := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"kind": "github_rule"}}
	for _, who := range []struct{ name, actor string }{{"boss", "me"}, {"jo", "jo"}, {"people", "me"}} {
		if err := s.AddSubscription(ctx, who.name, rule, who.actor); err != nil {
			t.Fatal(err)
		}
	}
	multi := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.JoinWhere([]string{"created", "github_rule"})}}
	if err := s.AddSubscription(ctx, "people", multi, "me"); err != nil {
		t.Fatal(err)
	}
	// What a build of schema 19 logged when a hook's event made a bead:
	// the log keeps the old word.
	if err := s.LogEvent(ctx, "", "github_rule", "yardr", map[string]any{"hook": "site", "makes": "task:new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age it: the records as a build of schema 19 stored them (the shapes
	// are unchanged, only the word is old), under that build's stamp.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 19))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	var first map[string]string
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err = store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		boss, err := s.GetCrew(ctx, "boss")
		if err != nil || !slices.Equal(boss.On, []string{"hook_rule", "created"}) {
			t.Fatalf("%s: boss on = %v, %v; want [hook_rule created]", pass, boss.On, err)
		}
		clerk, err := s.GetCrew(ctx, "clerk")
		if err != nil || !slices.Equal(clerk.On, []string{"created"}) {
			t.Fatalf("%s: clerk on = %v, %v; want [created]", pass, clerk.On, err)
		}
		jo, err := s.GetUser(ctx, "jo")
		if err != nil || len(jo.Subscriptions) != 1 || jo.Subscriptions[0].Where["kind"] != "hook_rule" {
			t.Fatalf("%s: jo subscriptions = %+v, %v", pass, jo.Subscriptions, err)
		}
		people, err := s.GetGroup(ctx, "people")
		if err != nil || len(people.Subscriptions) != 2 {
			t.Fatalf("%s: people subscriptions = %+v, %v", pass, people.Subscriptions, err)
		}
		var kinds []string
		for _, sub := range people.Subscriptions {
			kinds = append(kinds, strings.Join(store.WhereValues(sub.Where["kind"]), "+"))
		}
		slices.Sort(kinds)
		if !slices.Equal(kinds, []string{"created+hook_rule", "hook_rule"}) {
			t.Errorf("%s: people kinds = %v", pass, kinds)
		}
		// The logged rule reads as the new word under its old one.
		evs, err := s.EventsSince(ctx, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var logged, migrated int
		for _, e := range evs {
			switch {
			case e.Kind == "github_rule" && e.Data["makes"] == "task:new":
				logged++
				if store.EventKind(e.Kind) != "hook_rule" {
					t.Errorf("%s: EventKind(github_rule) = %q", pass, store.EventKind(e.Kind))
				}
			case e.Kind == "hook_rule_migrated":
				migrated++
				raw, _ := json.Marshal(e.Data)
				for _, say := range []string{`"from":"github_rule"`, `"crew":1`, `"user_subscriptions":1`, `"group_subscriptions":1`} {
					if !strings.Contains(string(raw), say) {
						t.Errorf("%s: hook_rule_migrated = %s, want %s in it", pass, raw, say)
					}
				}
			}
		}
		if logged != 1 || migrated != 1 {
			t.Errorf("%s: logged %d github_rule and %d hook_rule_migrated events, want 1 each", pass, logged, migrated)
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, path); got != 20 {
			t.Fatalf("%s: schema = %d; want 20", pass, got)
		}
		dump := bucketDump(t, path, "crew", "users", "groups", "events")
		if first == nil {
			first = dump
		} else if !maps.Equal(dump, first) {
			t.Fatalf("the next start changed what the upgrade wrote")
		}
	}

	// A store that held none of it is told nothing.
	fresh := filepath.Join(t.TempDir(), "yard.db")
	if s, err = store.Open(fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", On: []string{"created"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err = bolt.Open(fresh, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 19))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	was := bucketDump(t, fresh, "crew", "events", "settings")
	if s, err = store.Open(fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if now := bucketDump(t, fresh, "crew", "events", "settings"); !maps.Equal(was, now) || schemaOf(t, fresh) != 20 {
		t.Fatalf("a store with no github_rule changed, or is not stamped 20 (%d):\n was %v\n now %v", schemaOf(t, fresh), was, now)
	}
}
