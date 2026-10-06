package packs_test

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/packs"
	"yardr.dev/yardr/roles"
)

// Every embedded pack is a valid pack under the name of its directory, which
// is the name it is found by, and they are the two yardr has one for. A file the
// embed left out (a role, a script, a stage file) fails here: the pack names
// it and Load looks for it.
func TestEmbeddedPacksAreValid(t *testing.T) {
	entries, err := fs.ReadDir(packs.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		sub, err := fs.Sub(packs.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		p, err := pack.Load(sub, packs.Embedded)
		if err != nil {
			t.Errorf("pack %s: %v", e.Name(), err)
			continue
		}
		if p.Name != e.Name() {
			t.Errorf("the pack embedded as %s calls itself %s", e.Name(), p.Name)
		}
	}
	if !slices.Equal(names, []string{packs.Dir, packs.Starter}) {
		t.Errorf("embedded packs = %v, want dir and starter", names)
	}
}

// The dir pack's stage files are the ones depot init writes into a dir
// depot (roles/stages/dir/), placeholders included: until depot init reads
// them from the pack, a change to either is made to both.
func TestDirPackStageFilesAreDepotInits(t *testing.T) {
	n := 0
	for in, source := range map[string]string{"dir/flows/default": "stages/dir/default", "dir/flows/train": "stages/dir/type/train"} {
		entries, err := fs.ReadDir(roles.Stages, source)
		if err != nil || len(entries) == 0 {
			t.Fatalf("roles/%s: %v, %v", source, entries, err)
		}
		for _, e := range entries {
			n++
			want, _ := fs.ReadFile(roles.Stages, source+"/"+e.Name())
			if got, err := fs.ReadFile(packs.FS, in+"/"+e.Name()); err != nil || string(got) != string(want) {
				t.Errorf("packs/%s/%s differs from roles/%s/%s: change both (%v)", in, e.Name(), source, e.Name(), err)
			}
		}
		if have, _ := fs.ReadDir(packs.FS, in); len(have) != len(entries) {
			t.Errorf("packs/%s has %d stage files, roles/%s has %d", in, len(have), source, len(entries))
		}
	}
	if n != 8 {
		t.Errorf("%d stage files compared, want 8", n)
	}
}

// What is about working in a directory is said in the dir pack alone: the
// two packs hold no paragraph in common that names one, so a depot's kind
// never has two texts to keep the same. The starter's builder and reviewer
// roles do not speak of a directory depot at all: its yardmaster's alone
// does, who may have one among the yard's depots.
func TestPacksShareNoParagraphAboutDirectories(t *testing.T) {
	about := regexp.MustCompile(`(?i)\bdirector(y|ies)\b|\bdir depot`)
	paragraphs := func(name string) map[string]string {
		out := map[string]string{}
		err := fs.WalkDir(packs.FS, name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := fs.ReadFile(packs.FS, p)
			for _, para := range strings.Split(string(body), "\n\n") {
				// One paragraph however it is wrapped or indented.
				if key := strings.Join(strings.Fields(para), " "); key != "" {
					out[key] = p
				}
			}
			return err
		})
		if err != nil || len(out) == 0 {
			t.Fatalf("pack %s: %d paragraphs, %v", name, len(out), err)
		}
		return out
	}
	starter, dir := paragraphs(packs.Starter), paragraphs(packs.Dir)
	shared := 0
	for para, in := range dir {
		other, ok := starter[para]
		if !ok {
			continue
		}
		shared++
		if about.MatchString(para) {
			t.Errorf("packs/%s and packs/%s hold the same paragraph about a directory: %s", in, other, para)
		}
	}
	// The stage files for backlog and decide are the same in both, and say
	// nothing of where the work is done: the test compares something.
	if shared == 0 {
		t.Error("the packs share no paragraph at all: the comparison found nothing to compare")
	}
	for _, role := range []string{"starter/roles/builder.md", "starter/roles/reviewer.md"} {
		body, err := fs.ReadFile(packs.FS, role)
		if err != nil {
			t.Fatal(err)
		}
		if m := regexp.MustCompile(`(?i)directory depot|dir depot|depot's directory`).FindString(string(body)); m != "" {
			t.Errorf("packs/%s speaks of %q: what a directory depot asks of a role is the dir pack's to say", role, m)
		}
	}
}
