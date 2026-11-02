package store_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
)

// A store of schema 35 records no host for a source's credential. Opened by
// this build, each source with an env_file is paired with the host its calls
// go to under the base it is set to, so that it keeps calling with no giving
// of its credential again; a source with no env_file stays unpaired, and so
// does one whose declaration this build cannot read, which does not stop the
// store opening. No event is logged, and the next start changes nothing.
func TestSchema35StorePairsSourcesWithTheirHost(t *testing.T) {
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
	home, _ := crmHost(t, "home")
	eu, euHits := crmHost(t, "eu")
	text := strings.Replace(crmCalls, "BASE", home.URL, 1)
	text = strings.Replace(text, "base: "+home.URL+"/api\n", "base: "+home.URL+"/api\nbases:\n  eu: "+eu.URL+"/eu\n", 1)
	if _, err := s.PutSource(ctx, "crm.yaml", text, "jo"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "crm.env")
	if err := os.WriteFile(env, []byte("CRM_TOKEN="+crmToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{store.SourceEnvFile: env, store.SourceBase: "eu"} {
		if _, err := s.SetSource(ctx, "crm", key, value, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutSource(ctx, "bare.yaml", strings.Replace(text, "name: crm\n", "name: bare\n", 1), "jo"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age it: the sources as a build of schema 35 stored them, with no
	// pairing, and one a later build wrote that this build cannot read.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		sources := tx.Bucket([]byte("sources"))
		rows := map[string]map[string]any{}
		if err := sources.ForEach(func(k, raw []byte) error {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			rows[string(k)] = row
			return nil
		}); err != nil {
			return err
		}
		if rows["crm"]["keyed_host"] != eu.URL {
			return fmt.Errorf("the fixture's crm is paired with %v, want %s", rows["crm"]["keyed_host"], eu.URL)
		}
		rows["future"] = map[string]any{"name": "future", "declaration": "name: future\nbase: [", "env_file": "/x/future.env", "added_by": "jo"}
		for name, row := range rows {
			delete(row, "keyed_host")
			v, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if err := sources.Put([]byte(name), v); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 35))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i, pass := range []string{"the upgrade", "the next start"} {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		for name, want := range map[string]string{"crm": eu.URL, "bare": "", "future": ""} {
			if src, err := s.GetSource(ctx, name); err != nil || src.KeyedHost != want {
				t.Errorf("%s: %s is paired with %q, %v; want %q", pass, name, src.KeyedHost, err, want)
			}
		}
		if after, _ := s.Head(ctx); after != head {
			t.Errorf("%s logged %d events, want none", pass, after-head)
		}
		// Still calling, to the host the base it is set to names, with
		// nothing given again.
		res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", Actor: "jo"})
		if err != nil || string(res.Answer) != `{"id":"eu"}` {
			t.Errorf("%s: the call answered %+v, %v; want the answer of the eu host", pass, res, err)
		}
		if n := euHits.Load(); n != int32(i+1) {
			t.Errorf("%s: %d requests reached the eu host, want %d", pass, n, i+1)
		}
		// The call logged its own event; the next start is judged from here.
		head, _ = s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, path); got != 36 {
			t.Fatalf("%s: schema = %d; want 36", pass, got)
		}
	}
}
