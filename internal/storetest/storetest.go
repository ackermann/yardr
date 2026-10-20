// Package storetest opens throwaway stores for tests.
//
// With YARDR_TEST_RPC=1, New returns an rpc.Client talking to the store over
// a real unix socket, so every suite that uses New also tests the wire.
package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// A test's store is thrown away with the test, so no commit to it waits for
// the disk (store.NoSyncEnv): every test binary that imports this package
// opens its stores so, and hands the variable on to the yardr it starts.
// YARDR_STORE_NOSYNC=0 in the environment of `go test` keeps the sync.
func init() {
	if config.Getenv(store.NoSyncEnv) == "" {
		os.Setenv("YARDR_"+store.NoSyncEnv, "1")
	}
}

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
	return newIn(t, t.TempDir())
}

// NewIn is New on a fresh file in home, for a test whose other parts read
// the yard's home too (the daemon's backups, which the store writes only
// in that home's directory of them).
func NewIn(t testing.TB, home string) store.API {
	t.Helper()
	api, _ := newIn(t, home)
	return api
}

func newIn(t testing.TB, home string) (store.API, *store.Store) {
	t.Helper()
	s := Open(t, filepath.Join(home, "yard.db"))
	if config.Getenv("TEST_RPC") == "" {
		return s, s
	}
	// Over the wire every call has a caller, so the daemon's own writes
	// would be refused without a holder: the test process holds the
	// lock, as the daemon does.
	t.Cleanup(HoldDaemon(t, home))
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

// HoldDaemon holds home's daemon lock as the test process, as the daemon
// holds it, and returns its release: over the socket every call has a
// caller, so without a holder the daemon's own writes are refused. A
// test holds it only while it plays the daemon; a yard under test must
// otherwise see no daemon, as the upgrade's health check does.
func HoldDaemon(t testing.TB, home string) (release func()) {
	t.Helper()
	me := os.Getpid()
	var start uint64
	if st, err := peer.Describe(me); err == nil {
		start = st.Start
	}
	return HoldDaemonAs(t, home, me, start)
}

// HoldDaemonAs is HoldDaemon recording pid with start as the holder, for a
// test where another process holds the lock.
func HoldDaemonAs(t testing.TB, home string, pid int, start uint64) (release func()) {
	t.Helper()
	lock := config.At(home).DaemonLock()
	f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatalf("hold %s: %v", lock, err)
	}
	id := strconv.Itoa(pid) + " " + strconv.FormatUint(start, 10) + "\n"
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(id), 0)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// TestOwnerEnv is the YARDR_TEST_OWNER assignment for the yardr a test
// starts: this test process's pid and the start time the OS reports for it
// (the daemon-lock idiom), so that the server and the daemon stop by
// themselves when the test binary is killed and runs no cleanup. The suites
// that start a real yardr add it to their commands' environment.
func TestOwnerEnv(t testing.TB) string {
	t.Helper()
	var start uint64
	if st, err := peer.Describe(os.Getpid()); err == nil {
		start = st.Start
	}
	return config.Env(config.TestOwnerEnv, strconv.Itoa(os.Getpid())+" "+strconv.FormatUint(start, 10))[0]
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

// OldConfig sets key to value in the config of the group or crew member
// name (bucket "groups" or "crew"), or with bucket "settings" stores the
// setting key (name is not read), in the store file at path, which no
// process may have open: as a store from before PutGroup, PutCrew and
// SetSetting refused a key nothing reads holds one, with none of their
// checks.
func OldConfig(t testing.TB, path, bucket, name, key string, value any) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if bucket == "settings" {
			v, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return b.Put([]byte(key), v)
		}
		var row map[string]any
		if err := json.Unmarshal(b.Get([]byte(name)), &row); err != nil {
			return fmt.Errorf("%s %s: %w", bucket, name, err)
		}
		cfg, _ := row["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfg[key], row["config"] = value, cfg
		v, err := json.Marshal(row)
		if err != nil {
			return err
		}
		return b.Put([]byte(name), v)
	})
	if err != nil {
		t.Fatalf("write old config: %v", err)
	}
}
