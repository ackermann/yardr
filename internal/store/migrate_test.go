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
	if _, err := s.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
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
	if got := schemaOf(t, fixture); got != 10 {
		t.Fatalf("a new store is stamped %d; want 10", got)
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
			if got := schemaOf(t, path); got != 10 {
				t.Fatalf("schema after Open = %d; want 10", got)
			}
			got := indexKeys(t, path)
			for _, name := range indexBuckets {
				if !slices.Equal(got[name], want[name]) {
					t.Errorf("%s after migration = %v\nwant %v", name, got[name], want[name])
				}
			}
			if got := schemaOf(t, fixture); got != 10 || !slices.Equal(indexKeys(t, fixture)["inbox"], want["inbox"]) {
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

// A store of schema 8, from when the yard's coordinating resident was its
// mayor, comes out of Open with the setting that names it under yardmaster
// and nothing else changed: the resident keeps its name and its prompt's
// path, the record of a pack that called its resident mayor stays as that
// import wrote it, and the events already logged are not rewritten. A yard
// from before the setting whose resident is called mayor gets the setting,
// since the default name is no longer that one.
func TestSchema8StoreRenamesTheMayorSetting(t *testing.T) {
	ctx := context.Background()
	kept := []string{"residents", "packs", "rigs", "users"}
	cases := []struct {
		name     string
		resident string
		setting  string // the mayor setting of the old store, "" for none
		want     string // the yardmaster setting after Open, "" for none
	}{
		{"the setting names a resident of another name", "boss", "boss", "boss"},
		{"the setting names the resident mayor", "mayor", "mayor", "mayor"},
		{"no setting, and a resident mayor", "mayor", "", "mayor"},
		{"no setting, and no resident mayor", "lead", "", ""},
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
			if err := s.PutRig(ctx, store.Rig{Name: "web", Path: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PutResident(ctx, store.Resident{Name: tc.resident, Runner: "herdr",
				Config: map[string]any{"kind": "claude", "prompt": "/home/jo/.yardr/roles/mayor.md"}}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.AddSubscription(ctx, tc.resident, store.Subscription{Source: "linear", Scope: "involved"}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordPack(ctx, store.PackImport{Pack: "starter", Rig: "web", Source: "/packs/starter",
				Groups: map[string]string{"builders": "web-builders"}, Residents: map[string]string{"mayor": tc.resident},
				Objects: []store.PackObject{{Kind: "file", Name: "/home/jo/.yardr/packs/starter/roles/mayor.md"}}}, true, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.Mail(ctx, tc.resident, "the mayor setting is next", "me"); err != nil {
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
				if page, err := s.Inbox(ctx, tc.resident, 0); err != nil || page.Total != 1 {
					t.Fatalf("%s: %s's inbox = %+v %v", pass, tc.resident, page, err)
				}
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if got := schemaOf(t, path); got != 10 {
				t.Fatalf("schema after Open = %d; want 10", got)
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
// routes, flow pointers, a hook's stage map, the filters of a resident's,
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
	must(s.PutRig(ctx, store.Rig{Name: "web"}, "me"))
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
		{ID: "web-e", Title: "the feature", Type: store.TypeTrain, Rig: "web", Labels: []string{"epic"}},
		{ID: "web-p", Title: "a piece of it", Type: store.TypeCar, Rig: "web"},
		{ID: "web-t", Title: "a task", Rig: "web"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(err)
	}
	must(s.AddDep(ctx, "web-e", "web-p", store.DepParent, "me"))
	typ := store.TypeTrain
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &typ, Group: "integrators"})
	must(err)
	_, err = s.PutResident(ctx, store.Resident{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
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
	_, err = s.RecordPack(ctx, store.PackImport{Pack: "starter", Rig: "web", Source: "/packs/starter",
		Objects: []store.PackObject{
			{Kind: "route", Stage: "open", Type: "train", Group: "integrators"},
			{Kind: "pointer", Type: "train", Flow: "web.epic"},
			{Kind: "map", Name: "linear", Resident: "boss", Map: map[string]string{"train:open": "started"}},
			{Kind: "subscription", Resident: "boss", Subscription: &store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"train": "web-e"}}},
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
	aged := []string{"beads", "routes", "flow_pointers", "hooks", "residents", "users", "groups", "packs"}
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
	for _, want := range []string{"beads", "routes", "flow_pointers", "hooks", "residents", "users", "groups", "packs"} {
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
		if err != nil || len(ptrs) != 1 || ptrs[0] != (store.FlowPointer{Rig: "web", Type: store.TypeTrain, Flow: "web.epic"}) {
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
		boss, err := s.GetResident(ctx, "boss")
		if err != nil || !slices.Equal(subs(boss.Subscriptions), append([]string{"github all train=web-e"}, want...)) {
			t.Fatalf("%s: the resident's subscriptions = %v, %v", pass, subs(boss.Subscriptions), err)
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
	if got := schemaOf(t, path); got != 10 {
		t.Fatalf("schema after Open = %d; want 10", got)
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
	must(s.PutRig(ctx, store.Rig{Name: "web"}, "me"))
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
	train, err := s.CreateBead(ctx, store.NewBead{ID: "web-e", Title: "the feature", Type: "epic", Rig: "web"}, "me")
	if err != nil || train.Type != store.TypeTrain {
		t.Fatalf("created as epic: %+v, %v", train, err)
	}
	car, err := s.CreateBead(ctx, store.NewBead{ID: "web-p", Title: "a piece", Rig: "web"}, "me")
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
	_, err = s.PutResident(ctx, store.Resident{Name: "boss", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(err)
	for _, w := range []map[string]string{{"epic": "web-e"}, {"type": "part"}, {"kind": "part_without_epic"}} {
		must(s.AddSubscription(ctx, "boss", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: w}, "me"))
	}
	boss, err := s.GetResident(ctx, "boss")
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
