package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// gates gives rig r a full gate that appends "full" to a file and a light
// one that appends "light" (none when light is false), each then running
// what it is given, and returns the file: what it holds is which gates ran.
func (y *yard) gates(full, light string, hasLight bool) string {
	y.t.Helper()
	ran := filepath.Join(y.home, "ran")
	r := store.Rig{Name: "r", Path: y.repo, Base: "main", Check: "echo full >>" + ran + "; " + full}
	if hasLight {
		r.CheckLight = "echo light >>" + ran + "; " + light
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutRig(context.Background(), r, "test"); err != nil {
		y.t.Fatal(err)
	}
	return ran
}

func ranGates(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// A part of an epic lands on the epic's branch under the light gate alone,
// and its note says so; its sibling of another type, landing on the same
// branch, gets the full gate, as it always did.
func TestMergeLocalPartOfAnEpicGetsTheLightGate(t *testing.T) {
	y := newEpicYard(t, "true")
	// The full gate is red: a part that passes did not run it.
	ran := y.gates("exit 1", "true", true)
	y.yardr("bead", "update", "r-1", "--type", "part")
	epicWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "refinery", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("part %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if got := ranGates(t, ran); got != "light" {
		t.Fatalf("gates run for a part of an epic = %q, want the light one only", got)
	}
	if y.git(y.repo, "rev-list", "--count", epicWas+"..yard/r-e") != "1" {
		t.Fatal("the part did not land on the epic's branch")
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "under the light gate; the full gate and the review are epic r-e's") {
		t.Fatalf("the note does not say which gate it landed under:\n%s", notes)
	}

	// r-2 is a task under the same epic.
	y.exec("merge-local", "refinery", "r-2")
	if got := ranGates(t, ran); got != "light full" {
		t.Fatalf("gates run after a task of the epic = %q, want the full one for it", got)
	}
	if b := y.beadOf("r-2"); b.Stage != "new" {
		t.Fatalf("task under a red full gate is in %s, want back in new", b.Stage)
	}
}

// A red light gate sends the part back like a red gate, named for what it is.
func TestMergeLocalPartRedLightGate(t *testing.T) {
	y := newEpicYard(t, "true")
	ran := y.gates("true", "echo light says no; exit 1", true)
	y.yardr("bead", "update", "r-1", "--type", "part")
	epicWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "refinery", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "new" || b.Hold {
		t.Fatalf("part %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-e"); got != epicWas || ranGates(t, ran) != "light" {
		t.Fatalf("epic branch moved (%s, was %s) or the wrong gate ran (%q)", got, epicWas, ranGates(t, ran))
	}
	notes := y.yardr("prime", "--bead", "r-1")
	for _, want := range []string{"the light gate failed on yard/r-1", "light says no"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
}

// A part with no epic lands on the rig's base, where no full gate would
// follow: it gets the full gate. (yardr moves no such part out of the stage
// its flow starts in, and the dispatcher starts no script for one; this
// one's type was changed after it was approved, and the script is run
// directly. Landed, it is closed: the move to merged is refused too.)
func TestMergeLocalPartWithoutAnEpicGetsTheFullGate(t *testing.T) {
	y := newYard(t)
	ran := y.gates("true", "true", true)
	y.yardr("bead", "update", "r-1", "--type", "part")
	y.commit(y.wt, "b.txt", "bead\n")

	y.refinery()

	if b := y.bead(); b.Status != "closed" || y.git(y.repo, "rev-parse", "main") != y.git(y.wt, "rev-parse", "HEAD") {
		t.Fatalf("part without an epic is %s in %s, want landed on main under the full gate and closed", b.Status, b.Stage)
	}
	if got := ranGates(t, ran); got != "full" {
		t.Fatalf("gates run for a part with no epic = %q, want the full one only", got)
	}
	if notes := y.notes(); strings.Contains(notes, "light gate") {
		t.Fatalf("the note names the light gate:\n%s", notes)
	}
}

// A rig with no light gate: a part of an epic runs the full one, and the
// refinery's note carries rig check's line saying so.
func TestMergeLocalPartInARigWithoutALightGate(t *testing.T) {
	y := newEpicYard(t, "true")
	ran := y.gates("exit 1", "", false)
	y.yardr("bead", "update", "r-1", "--type", "part")

	y.exec("merge-local", "refinery", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "new" {
		t.Fatalf("part is in %s, want back in new: the full gate is red", b.Stage)
	}
	if got := ranGates(t, ran); got != "full" {
		t.Fatalf("gates run = %q, want the full one", got)
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "rig r has no light gate") ||
		!strings.Contains(notes, "running the full gate") {
		t.Fatalf("the note does not say the full gate ran for want of a light one:\n%s", notes)
	}
}

// A part's light gate is compared with its epic's branch as the part was
// just rebased onto it, not with the rig's base: the second part to land is
// gated on its own change, not on the first part's again.
func TestMergeLocalPartLightGateIsComparedWithTheEpic(t *testing.T) {
	y := newEpicYard(t, "true")
	seen := filepath.Join(y.home, "seen")
	// What the gate is compared with, and what changed since.
	y.gates("exit 1", `echo "$YARDR_CHECK_BASE $(git diff --name-only "$(git merge-base "$YARDR_CHECK_BASE" HEAD)" | tr '\n' ' ')" >>`+seen, true)
	y.yardr("bead", "update", "r-1", "--type", "part")
	y.yardr("bead", "update", "r-2", "--type", "part")
	base := y.git(y.repo, "rev-parse", "main")

	y.exec("merge-local", "refinery", "r-1")
	epic := y.git(y.repo, "rev-parse", "yard/r-e")
	y.exec("merge-local", "refinery", "r-2")

	for _, id := range []string{"r-1", "r-2"} {
		if b := y.beadOf(id); b.Stage != "merged" || b.Status != "closed" {
			t.Fatalf("part %s is %s/%s, want merged and closed", id, b.Stage, b.Status)
		}
	}
	if epic == base {
		t.Fatal("the first part did not move the epic's branch")
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	// The epic's branch was at the rig's base when the first part was gated.
	if want := base + " child1.txt \n" + epic + " child2.txt \n"; string(b) != want {
		t.Fatalf("the light gates saw (base, then what changed):\n%swant:\n%s", b, want)
	}
}
