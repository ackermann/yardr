package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// yardWithoutServer is a set-up yard in a fresh home with no server running
// on it, as after `yardr server stop`.
func yardWithoutServer(t *testing.T) *config.Config {
	t.Helper()
	newHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// captureStderr is the buffer a test hands a command as its error stream:
// an app preset (stderr) for a run through the root, SetErr for a direct
// subcommand, warn for a helper called directly.
func captureStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	return &bytes.Buffer{}
}

// store compact on a yard with no server running compacts the file and
// starts no server: the first-run check, which connects, is skipped for it.
func TestCompactWithoutAServer(t *testing.T) {
	cfg := yardWithoutServer(t)
	requireNonInteractive(t)
	errs := captureStderr(t)
	out, code := executeOut(&app{stderr: errs, env: testEnv(t, cfg.Home)}, "store", "compact")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out, cfg.DBPath()+": ") || !strings.Contains(out, " bytes") {
		t.Fatalf("output = %q", out)
	}
	for _, left := range []string{cfg.PIDFile(), cfg.Socket(), cfg.LogFile()} {
		if _, err := os.Stat(left); err == nil {
			t.Errorf("%s exists: compact started a server", left)
		}
	}
	if _, err := os.Stat(cfg.DBPath() + ".compact"); err == nil {
		t.Error("the compaction's temporary file was left behind")
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("the compacted store: %v", err)
	}
	defer st.Close(context.Background())
	if beads, err := st.ListBeads(context.Background(), store.BeadFilter{All: true}); err != nil || len(beads) != 1 {
		t.Fatalf("beads after compact = %v %v", beads, err)
	}
}

// store compact on a yard whose server runs refuses, saying what to stop,
// and leaves the store as it is.
func TestCompactRefusesWhileTheServerRuns(t *testing.T) {
	cfg := yardWithoutServer(t)
	requireNonInteractive(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); !server.Running(ctx, cfg); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("server did not come up")
		}
	}

	errs := captureStderr(t)
	_, code := executeOut(&app{stderr: errs, env: testEnv(t, cfg.Home)}, "store", "compact")
	if code != 1 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if got := errs.String(); !strings.Contains(got, "the yardr server is running") ||
		!strings.Contains(got, "stop the daemon, then `yardr server stop`") {
		t.Fatalf("stderr = %q", got)
	}
	if _, err := os.Stat(cfg.DBPath() + ".compact"); err == nil {
		t.Error("a refused compaction left its temporary file")
	}
	select {
	case err := <-done:
		t.Fatalf("the server stopped: %v", err)
	default:
	}
	if _, err := store.Compact(cfg.DBPath()); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("Compact under the server = %v; want ErrLocked", err)
	}
}

// prunedStore is a store whose prune removed res.
type prunedStore struct {
	store.API
	res store.PruneResult
}

func (p prunedStore) Prune(context.Context, store.Retention, string) (*store.PruneResult, error) {
	return &p.res, nil
}

// yardr store prune says what went of each link, as counts.
func TestStorePrunePrintsWhatWentOfEachPeer(t *testing.T) {
	t.Parallel()
	a := &app{actor: "jo", st: prunedStore{res: store.PruneResult{Events: 4, Peers: map[string]store.PeerPruned{
		"north": {Seen: 12, Failed: 1},
		"alps":  {Seen: 3},
	}}}}
	out, err := run(t, a.storeCmd(), "prune")
	if err != nil {
		t.Fatal(err)
	}
	want := "pruned 4 events, 0 sessions, 0 webhook bodies\n" +
		"peer alps: 3 ids of messages taken from it forgotten, 0 messages given up removed\n" +
		"peer north: 12 ids of messages taken from it forgotten, 1 messages given up removed\n"
	if out != want {
		t.Fatalf("store prune printed:\n%s\nwant:\n%s", out, want)
	}
}
