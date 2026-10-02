// Package storetest opens throwaway stores for tests.
//
// With YARDR_TEST_RPC=1, New returns an rpc.Client talking to the store over
// a real unix socket, so every suite that uses New also tests the wire.
package storetest

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// Main runs the package's tests (kept so test packages need no changes).
func Main(m *testing.M) { os.Exit(m.Run()) }

// New returns a store API on a fresh file in the test's temp dir.
func New(t testing.TB) store.API {
	t.Helper()
	s := Open(t, filepath.Join(t.TempDir(), "yard.db"))
	if config.Getenv("TEST_RPC") == "" {
		return s
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
	srv := &http.Server{Handler: rpc.Handler(s, nil)}
	go srv.Serve(ln)
	c := rpc.NewClient(sock)
	t.Cleanup(func() {
		_ = c.Close(context.Background())
		_ = srv.Close()
		os.RemoveAll(dir)
	})
	return c
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
