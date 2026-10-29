package assembly

// These tests land through merge-local with disk keeping on: the keeping
// runs before the tests, against a go of the test's own, and the machine's
// go env file and GOCACHE are untouched.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// hermeticGo gives the script a go of its own: a GOENV file seeding
// GOCACHE under the test's home, and a GOTMPDIR there too, passed in the
// group's cmd, so the keeping the landing runs touches nothing of the
// machine's go. It returns the seeded GOCACHE and GOTMPDIR.
func (y *yard) hermeticGo() (gocache, gotmp string) {
	y.t.Helper()
	gocache = filepath.Join(y.home, "gocache")
	gotmp = filepath.Join(y.home, "gotmp")
	goenv := filepath.Join(y.home, "goenv")
	if err := os.WriteFile(goenv, []byte("GOCACHE="+gocache+"\n"), 0o644); err != nil {
		y.t.Fatal(err)
	}
	if err := os.MkdirAll(gotmp, 0o755); err != nil {
		y.t.Fatal(err)
	}
	y.opts = "GOENV=" + shellQuote(goenv) + " GOTMPDIR=" + shellQuote(gotmp) + " " + y.opts
	return gocache, gotmp
}

func (y *yard) keepGB(gb string) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.SetSetting(context.Background(), store.DiskKeepGBSetting, gb, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// A landing keeps the disk before its tests run: under the threshold it
// rotates the seeded GOCACHE, records the old directory, clears stale
// go-tmp, and logs a disk_kept event, and the bead still lands. The depot's
// test command sees the rotated GOENV, which proves the order: a keeping
// after the tests would fail it.
func TestMergeLocalKeepsDiskBeforeTests(t *testing.T) {
	y := newYard(t)
	gocache, gotmp := y.hermeticGo()
	if err := os.MkdirAll(gocache, 0o755); err != nil {
		t.Fatal(err)
	}
	standing := filepath.Join(gocache, "f")
	if err := os.WriteFile(standing, []byte("cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(gotmp, "go-build111")
	fresh := filepath.Join(gotmp, "go-build222")
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-4 * time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	// Under any machine's free space: the keeping always acts.
	y.keepGB("999999999")
	// The machine's go through a frozen copy of its GOENV file: the
	// machine's own keeping may rotate the real cache while the landing
	// runs, and the before/after reads below must not race it. Only y.run
	// sees the copy (y.env is its own); the landing inherits the test
	// process's environment instead, so what it rotates is unchanged.
	frozen := filepath.Join(t.TempDir(), "goenv")
	if b, err := os.ReadFile(y.run(y.home, "go", "", "env", "GOENV")); err == nil {
		if err := os.WriteFile(frozen, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	y.env = append(y.env, "GOENV="+frozen)
	// The machine's go before: the keeping must leave it as it is.
	realCache := y.run(y.home, "go", "", "env", "GOCACHE")
	realEnvFile := y.run(y.home, "go", "", "env", "GOENV")
	realEnv, realEnvErr := os.ReadFile(realEnvFile)
	y.tests(`grep -q 'gocache-1' "$GOENV"`)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s %s, want merged and closed", b.Stage, b.Status)
	}
	if _, err := os.Stat(standing); err != nil {
		t.Errorf("the rotated cache went with the rotation: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale go-tmp stands: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh go-tmp went: %v", err)
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	ctx := context.Background()
	evs, err := st.EventsOfKind(ctx, store.DiskKept, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d disk_kept events, want 1", len(evs))
	}
	data := evs[0].Data
	if data["rotated_from"] != gocache {
		t.Errorf("rotated_from = %v, want %s", data["rotated_from"], gocache)
	}
	if to, _ := data["rotated_to"].(string); !strings.HasSuffix(to, "gocache-1") {
		t.Errorf("rotated_to = %v, want the fresh sibling", data["rotated_to"])
	}
	if before, _ := data["free_before"].(float64); before <= 0 {
		t.Errorf("free_before = %v, want the machine's free bytes", data["free_before"])
	}
	removed, _ := data["removed"].([]any)
	if len(removed) != 1 || removed[0] != stale {
		t.Errorf("removed = %v, want [%s]", removed, stale)
	}
	olds, err := st.GOCACHEOldDirs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(olds) != 1 || olds[0].Dir != gocache {
		t.Errorf("recorded = %+v, want the rotated cache", olds)
	}
	if !strings.Contains(y.log, "disk kept: free") {
		t.Errorf("the assembly log has no kept line:\n%s", y.log)
	}
	// The machine's go is as it was: the rotation went to the seeded
	// GOENV, and no other go env file was written.
	if got := y.run(y.home, "go", "", "env", "GOCACHE"); got != realCache {
		t.Errorf("the machine's GOCACHE is %s, was %s", got, realCache)
	}
	after, afterErr := os.ReadFile(realEnvFile)
	if realEnvErr == nil {
		if afterErr != nil || string(after) != string(realEnv) {
			t.Errorf("the machine's go env file changed: %v", afterErr)
		}
	} else if afterErr == nil {
		t.Errorf("the machine grew a go env file at %s", realEnvFile)
	}
	seeding, err := os.ReadFile(filepath.Join(y.home, "goenv"))
	if err != nil || !strings.Contains(string(seeding), "gocache-1") {
		t.Errorf("the seeded GOENV has no rotation: %s, %v", seeding, err)
	}
}

// With disk keeping off, a landing logs no disk_kept event and says
// nothing of the disk: the step is a no-op.
func TestMergeLocalSkipsDiskKeepWhenOff(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s %s, want merged and closed", b.Stage, b.Status)
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	evs, err := st.EventsOfKind(context.Background(), store.DiskKept, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("%d disk_kept events, want none", len(evs))
	}
	if strings.Contains(y.log, "disk keep") {
		t.Errorf("the assembly log names the disk:\n%s", y.log)
	}
}
