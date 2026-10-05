// Package storetest opens throwaway stores for tests.
//
// With YARDR_TEST_RPC=1, New returns an rpc.Client talking to the store over
// a real unix socket, so every suite that uses New also tests the wire.
package storetest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// Main runs the package's tests (kept so test packages need no changes).
func Main(m *testing.M) { os.Exit(m.Run()) }

// New returns a store API on a fresh file in the test's temp dir.
func New(t testing.TB) store.API {
	t.Helper()
	api, _ := NewOwned(t)
	return api
}

// NewOwned is New that also returns the store behind the API, for what
// only the owning process may do (record a webhook delivery, log any
// event kind) while the test reads back through the API.
func NewOwned(t testing.TB) (store.API, *store.Store) {
	t.Helper()
	s := Open(t, filepath.Join(t.TempDir(), "yard.db"))
	if config.Getenv("TEST_RPC") == "" {
		return s, s
	}
	// Unix socket paths are limited to ~108 bytes; t.TempDir is too long.
	dir, err := os.MkdirTemp("", "ys")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rpc.Handler(s, nil), ConnContext: rpc.ConnContext}
	go srv.Serve(ln)
	c := rpc.NewClient(sock)
	t.Cleanup(func() {
		_ = c.Close(context.Background())
		_ = srv.Close()
		os.RemoveAll(dir)
	})
	return c, s
}

// RepoFlow is the path of the repository's .yardr/flows/<name>.flow, for a
// test that wants a flow exactly as the repo carries it. The repository is
// found from the test's working directory, and a flow the repo no longer
// carries fails naming the ones it does, so a rename shows up as such.
func RepoFlow(t testing.TB, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for ; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".yardr", "flows")); err == nil {
			break
		}
		if dir == filepath.Dir(dir) {
			t.Fatal("no .yardr/flows above the test's working directory")
		}
	}
	path := filepath.Join(dir, ".yardr", "flows", name+".flow")
	if _, err := os.Stat(path); err != nil {
		have, _ := filepath.Glob(filepath.Join(dir, ".yardr", "flows", "*.flow"))
		for i, h := range have {
			have[i] = filepath.Base(h)
		}
		t.Fatalf("the repository carries no flow %s (%v); it has %v", name, err, have)
	}
	return path
}

// Open opens the store at path, closing it when the test ends.
func Open(t testing.TB, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// OldBlocks writes the edge from -blocks-> to (bead UUIDs) into the store
// file at path, which no process may have open, as a store from before
// AddDep refused cycles holds one: the row and its two index keys, with
// none of AddDep's checks.
func OldBlocks(t testing.TB, path, from, to string) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	id := fmt.Sprintf("old%017d", time.Now().UnixNano()%1e17)
	row := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"kind":"blocks","created_at":%q}`, id, from, to, time.Now().UTC().Format(time.RFC3339Nano))
	err = db.Update(func(tx *bolt.Tx) error {
		for _, kv := range [][3]string{{"deps", id, row}, {"dep_from", from + "\x00" + id, ""}, {"dep_to", to + "\x00" + id, ""}} {
			if err := tx.Bucket([]byte(kv[0])).Put([]byte(kv[1]), []byte(kv[2])); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write edge: %v", err)
	}
}
