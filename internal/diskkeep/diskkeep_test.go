package diskkeep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeGo is go as the tests see it: GOCACHE and GOTMPDIR by value, every
// `go env -w` recorded. With sticky set, `go env -w` changes nothing, as
// when GOCACHE comes from the environment and wins over the go env file.
type fakeGo struct {
	gocache string
	gotmp   string
	sets    []string
	sticky  bool
	gets    int
}

func (f *fakeGo) env(names ...string) (string, error) {
	f.gets++
	if len(names) != 1 {
		panic("goEnv takes one name")
	}
	switch names[0] {
	case "GOCACHE":
		return f.gocache, nil
	case "GOTMPDIR":
		return f.gotmp, nil
	}
	panic("goEnv for " + names[0])
}

func (f *fakeGo) set(kv string) error {
	f.sets = append(f.sets, kv)
	if !f.sticky {
		f.gocache = strings.TrimPrefix(kv, "GOCACHE=")
	}
	return nil
}

// keeping is a Keep with faked go, disk and clock over a throwaway store.
type keeping struct {
	t     *testing.T
	st    store.API
	fake  *fakeGo
	free  []uint64
	frees int
	now   time.Time
}

func newKeeping(t *testing.T, settings map[string]string) (*keeping, map[string]string) {
	t.Helper()
	k := &keeping{t: t, st: storetest.New(t), fake: &fakeGo{}, now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	saveFree, saveEnv, saveSet, saveHas, saveNow := freeDisk, goEnv, goEnvSet, hasGo, now
	t.Cleanup(func() { freeDisk, goEnv, goEnvSet, hasGo, now = saveFree, saveEnv, saveSet, saveHas, saveNow })
	freeDisk = func(string) (uint64, error) {
		if k.frees >= len(k.free) {
			t.Fatalf("free space read %d times, scripted %d", k.frees+1, len(k.free))
		}
		free := k.free[k.frees]
		k.frees++
		return free, nil
	}
	goEnv = k.fake.env
	goEnvSet = k.fake.set
	hasGo = func() bool { return true }
	now = func() time.Time { return k.now }
	if settings == nil {
		settings = map[string]string{}
	}
	return k, settings
}

func (k *keeping) run(settings map[string]string) *Report {
	k.t.Helper()
	rep, err := Keep(context.Background(), k.st, settings)
	if err != nil {
		k.t.Fatal(err)
	}
	return rep
}

func (k *keeping) events() []store.Event {
	k.t.Helper()
	evs, err := k.st.EventsOfKind(context.Background(), store.DiskKept, 10)
	if err != nil {
		k.t.Fatal(err)
	}
	return evs
}

func (k *keeping) olds() []store.GOCACHEDir {
	k.t.Helper()
	olds, err := k.st.GOCACHEOldDirs(context.Background())
	if err != nil {
		k.t.Fatal(err)
	}
	return olds
}

// mkdir writes dir with a file in it, so a removal proves recursive.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKeepIdleOverThreshold(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	k.free = []uint64{31 * GB}

	rep := k.run(settings)

	if rep.Acted {
		t.Errorf("Acted = true over the threshold")
	}
	if k.frees != 1 {
		t.Errorf("free space read %d times, want 1", k.frees)
	}
	if len(k.fake.sets) > 0 {
		t.Errorf("go env -w ran %v over the threshold", k.fake.sets)
	}
	if evs := k.events(); len(evs) != 0 {
		t.Errorf("%d disk_kept events, want none", len(evs))
	}
	if olds := k.olds(); len(olds) != 0 {
		t.Errorf("%d recorded directories, want none", len(olds))
	}
	if lines := rep.Lines(); len(lines) != 0 {
		t.Errorf("Lines = %v, want none", lines)
	}
}

func TestKeepOff(t *testing.T) {
	k, settings := newKeeping(t, map[string]string{store.DiskKeepGBSetting: "0"})
	k.free = []uint64{GB}

	rep := k.run(settings)

	if rep.Acted {
		t.Errorf("Acted = true with disk_keep_gb=0")
	}
	if k.frees != 0 || k.fake.gets != 0 {
		t.Errorf("the disk and go were consulted with the keeping off (%d reads, %d go env)", k.frees, k.fake.gets)
	}
	if evs := k.events(); len(evs) != 0 {
		t.Errorf("%d disk_kept events, want none", len(evs))
	}
}

func TestKeepWithoutGo(t *testing.T) {
	k, settings := newKeeping(t, nil)
	hasGo = func() bool { return false }

	rep := k.run(settings)

	if rep.Acted {
		t.Errorf("Acted = true without go")
	}
	if evs := k.events(); len(evs) != 0 {
		t.Errorf("%d disk_kept events, want none", len(evs))
	}
}

func TestKeepRotates(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	if !rep.Acted {
		t.Fatalf("Acted = false under the threshold")
	}
	wantFrom := filepath.Join(base, "go-build-8")
	wantTo := filepath.Join(base, "go-build-9")
	if len(k.fake.sets) != 1 || k.fake.sets[0] != "GOCACHE="+wantTo {
		t.Fatalf("go env -w ran %v, want [GOCACHE=%s]", k.fake.sets, wantTo)
	}
	if rep.RotatedFrom != wantFrom || rep.RotatedTo != wantTo {
		t.Errorf("rotated %s -> %s, want %s -> %s", rep.RotatedFrom, rep.RotatedTo, wantFrom, wantTo)
	}
	olds := k.olds()
	if len(olds) != 1 || olds[0].Dir != filepath.Join(base, "go-build-8") || !olds[0].At.Equal(k.now) {
		t.Errorf("recorded = %+v, want the old directory at now", olds)
	}
	evs := k.events()
	if len(evs) != 1 {
		t.Fatalf("%d disk_kept events, want 1", len(evs))
	}
	data := evs[0].Data
	if data["free_before"] != float64(20*GB) || data["free_after"] != float64(40*GB) {
		t.Errorf("event free %v -> %v, want %d -> %d", data["free_before"], data["free_after"], 20*GB, 40*GB)
	}
	if data["rotated_from"] != rep.RotatedFrom || data["rotated_to"] != rep.RotatedTo {
		t.Errorf("event rotated %v -> %v", data["rotated_from"], data["rotated_to"])
	}
	if data["gave_up"] != false {
		t.Errorf("event gave_up = %v, want false", data["gave_up"])
	}
	if evs[0].Actor != Actor {
		t.Errorf("event actor = %q, want %q", evs[0].Actor, Actor)
	}
	if evs[0].Bead != nil {
		t.Errorf("event bead = %v, want none", *evs[0].Bead)
	}
	lines := rep.Lines()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "disk kept: free 20.0 GB -> 40.0 GB") {
		t.Errorf("Lines = %v", lines)
	}
}

func TestKeepRemovesOldPastGrace(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	past := filepath.Join(base, "go-build-7")
	young := filepath.Join(base, "go-build-6")
	mkdir(t, past)
	mkdir(t, young)
	ctx := context.Background()
	must(t, k.st.RecordGOCACHEOld(ctx, past, k.now.Add(-31*time.Minute)))
	must(t, k.st.RecordGOCACHEOld(ctx, young, k.now.Add(-29*time.Minute)))
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	if _, err := os.Stat(past); !os.IsNotExist(err) {
		t.Errorf("%s stands past its grace", past)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("%s went before its grace: %v", young, err)
	}
	if _, err := os.Stat(filepath.Join(base, "go-build-9")); !os.IsNotExist(err) {
		t.Errorf("the fresh sibling was touched: %v", err)
	}
	olds := k.olds()
	var dirs []string
	for _, o := range olds {
		dirs = append(dirs, o.Dir)
	}
	// The old directory past its grace is gone from the record; the young
	// one and the just-rotated one stay.
	if len(olds) != 2 || dirs[0] != filepath.Join(base, "go-build-6") || dirs[1] != filepath.Join(base, "go-build-8") {
		t.Errorf("recorded = %v", dirs)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != past {
		t.Errorf("Removed = %v, want [%s]", rep.Removed, past)
	}
}

func TestKeepNeverRemovesTheLiveCache(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	// A record pointing at what the rotation lands on: stale from a
	// pointer moved back, and past its grace. It is the live cache from
	// the rotation on, so it stays recorded and out of the removed.
	live := filepath.Join(base, "go-build-9")
	must(t, k.st.RecordGOCACHEOld(context.Background(), live, k.now.Add(-time.Hour)))
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	for _, dir := range rep.Removed {
		if dir == live {
			t.Errorf("Removed holds the live cache: %v", rep.Removed)
		}
	}
	olds := k.olds()
	var dirs []string
	for _, o := range olds {
		dirs = append(dirs, o.Dir)
	}
	if len(olds) != 2 || dirs[0] != filepath.Join(base, "go-build-8") || dirs[1] != live {
		t.Errorf("recorded = %v, want the just-rotated and the live cache", dirs)
	}
}

// TestRemoveOldKeepsAnExistingLiveCache is the race removeOld guards: a
// build that started in the new cache between the rotation and the removal
// made the live directory exist, and past-grace record or not, it stands.
func TestRemoveOldKeepsAnExistingLiveCache(t *testing.T) {
	k, _ := newKeeping(t, nil)
	live := filepath.Join(t.TempDir(), "go-build-9")
	mkdir(t, live)
	ctx := context.Background()
	must(t, k.st.RecordGOCACHEOld(ctx, live, k.now.Add(-time.Hour)))
	rep := &Report{Removed: []string{}}

	removed := removeOld(ctx, k.st, rep, live, k.now)

	if len(removed) != 0 || len(rep.Errors) != 0 {
		t.Errorf("removed = %v, errors = %v", removed, rep.Errors)
	}
	if _, err := os.Stat(filepath.Join(live, "f")); err != nil {
		t.Errorf("the live cache went: %v", err)
	}
	if olds := k.olds(); len(olds) != 1 {
		t.Errorf("recorded = %+v, want the live cache kept", olds)
	}
}

func TestKeepPrunesTmpByAge(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	k.fake.gotmp = filepath.Join(base, "go-tmp")
	stale := filepath.Join(k.fake.gotmp, "go-build111")
	fresh := filepath.Join(k.fake.gotmp, "go-build222")
	other := filepath.Join(k.fake.gotmp, "other")
	mkdir(t, stale)
	mkdir(t, fresh)
	mkdir(t, other)
	plain := filepath.Join(k.fake.gotmp, "go-build333")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := k.now.Add(-4 * time.Hour)
	for _, p := range []string{stale, other, plain} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale go-tmp stands: %v", err)
	}
	for _, p := range []string{fresh, other, plain} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went: %v", p, err)
		}
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != stale {
		t.Errorf("Removed = %v, want [%s]", rep.Removed, stale)
	}
}

func TestKeepWithoutTmpDir(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	k.fake.gotmp = "" // GOTMPDIR unset: nothing known to clear
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	if !rep.Acted || len(rep.Errors) != 0 {
		t.Errorf("Acted = %v, Errors = %v", rep.Acted, rep.Errors)
	}
}

func TestKeepGaveUp(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "gocache")
	mkdir(t, k.fake.gocache)
	k.free = []uint64{20 * GB, 5 * GB}

	rep := k.run(settings)

	if !rep.GaveUp {
		t.Fatalf("GaveUp = false at 5 GB free after")
	}
	// No numeric suffix: the first sibling is -1.
	if want := filepath.Join(base, "gocache-1"); k.fake.gocache != want || rep.RotatedTo != want {
		t.Errorf("rotated to %s, want %s", rep.RotatedTo, want)
	}
	evs := k.events()
	if len(evs) != 1 || evs[0].Data["gave_up"] != true {
		t.Fatalf("events = %+v, want one with gave_up", evs)
	}
	lines := rep.Lines()
	if lines[len(lines)-1] != "gave up: free space still under 10 GB after keeping the disk" {
		t.Errorf("Lines = %v", lines)
	}
}

func TestKeepSkipsTakenSiblings(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "cache")
	mkdir(t, k.fake.gocache)
	mkdir(t, filepath.Join(base, "cache-1"))
	k.free = []uint64{20 * GB, 40 * GB}

	rep := k.run(settings)

	if want := filepath.Join(base, "cache-2"); rep.RotatedTo != want {
		t.Errorf("rotated to %s, want %s", rep.RotatedTo, want)
	}
}

func TestKeepRefusesAnUntakenRotation(t *testing.T) {
	k, settings := newKeeping(t, nil)
	base := t.TempDir()
	k.fake.gocache = filepath.Join(base, "go-build-8")
	mkdir(t, k.fake.gocache)
	k.fake.sticky = true // GOCACHE from the environment wins over go env -w
	k.free = []uint64{20 * GB}

	_, err := Keep(context.Background(), k.st, settings)

	if err == nil || !strings.Contains(err.Error(), "after go env -w") {
		t.Fatalf("err = %v, want the untaken rotation", err)
	}
	if evs := k.events(); len(evs) != 0 {
		t.Errorf("%d disk_kept events, want none", len(evs))
	}
	if olds := k.olds(); len(olds) != 0 {
		t.Errorf("%d recorded directories, want none", len(olds))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
