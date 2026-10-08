package pack_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
)

// A pack is read from any fs.FS, and what makes a copy executable is that a
// run: group names the file, never its mode in the pack: here the script
// has none and a role is executable, as a file system without modes and a
// careless checkout would have them.
func TestLoadFromAFileSystemWithoutModes(t *testing.T) {
	fsys := fstest.MapFS{}
	for name, text := range smallFiles(small) {
		fsys[name] = &fstest.MapFile{Data: []byte(text), Mode: 0o444}
	}
	fsys["roles/builder.md"].Mode = 0o755
	p, err := pack.Load(fsys, "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != "elsewhere" {
		t.Errorf("Dir = %q", p.Dir)
	}
	st, o := yard(t)
	plan := build(t, st, p, o)
	if f := find(t, plan, "file", "scripts/land").File; f.Mode != "0755" || f.From != filepath.Join("elsewhere", "scripts", "land") {
		t.Errorf("scripts/land = %+v", f)
	}
	if f := find(t, plan, "file", "roles/builder.md").File; f.Mode != "0644" {
		t.Errorf("roles/builder.md = %+v", f)
	}
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = q.Default
	}
	a["crew.yardmaster.crew"] = "yardmaster"
	a["group.web-builders.kind"], a["crew.yardmaster.kind"], a["crew.yardmaster.dir"] = "claude", "claude", t.TempDir()
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	install := config.At(o.Home).Pack("small")
	for name, mode := range map[string]os.FileMode{"scripts/land": 0o755, "roles/builder.md": 0o644} {
		if info, err := os.Stat(filepath.Join(install, filepath.FromSlash(name))); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, mode %v, want %v", name, err, info, mode)
		}
	}
	// No directory, so no commit: the source is what Load was told.
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 || recs[0].Source != "elsewhere" || recs[0].Commit != "" {
		t.Fatalf("Packs = %+v, %v", recs, err)
	}
	if plan = build(t, st, p, o); len(actions(plan, pack.Create))+len(actions(plan, pack.Change)) != 0 {
		t.Errorf("after the import the plan would create %v and change %v", actions(plan, pack.Create), actions(plan, pack.Change))
	}
}
