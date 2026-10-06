package assembly

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

// gates gives depot r a full gate that appends "full" to a file and a light
// one that appends "light" (none when light is false), each then running
// what it is given, and returns the file: what it holds is which gates ran.
func (y *yard) gates(full, light string, hasLight bool) string {
	y.t.Helper()
	ran := filepath.Join(y.home, "ran")
	r := store.Depot{Name: "r", Path: y.repo, Base: "main", Check: "echo full >>" + ran + "; " + full}
	if hasLight {
		r.CheckLight = "echo light >>" + ran + "; " + light
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutDepot(context.Background(), r, "test"); err != nil {
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

// A wagon of a train lands on the train's branch under the light gate alone,
// and its note says so; its sibling of another type, landing on the same
// branch, gets the full gate, as it always did.
func TestMergeLocalWagonOfATrainGetsTheLightGate(t *testing.T) {
	y := newTrainYard(t, "true")
	// The full gate is red: a wagon that passes did not run it.
	ran := y.gates("exit 1", "true", true)
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	trainWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("wagon %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if got := ranGates(t, ran); got != "light" {
		t.Fatalf("gates run for a wagon of a train = %q, want the light one only", got)
	}
	if y.git(y.repo, "rev-list", "--count", trainWas+"..yard/r-e") != "1" {
		t.Fatal("the wagon did not land on the train's branch")
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "under the light gate; the full gate and the review are train r-e's") {
		t.Fatalf("the note does not say which gate it landed under:\n%s", notes)
	}

	// r-2 is a task under the same train.
	y.exec("merge-local", "assembly", "r-2")
	if got := ranGates(t, ran); got != "light full" {
		t.Fatalf("gates run after a task of the train = %q, want the full one for it", got)
	}
	if b := y.beadOf("r-2"); b.Stage != "new" {
		t.Fatalf("task under a red full gate is in %s, want back in new", b.Stage)
	}
}

// A red light gate sends the wagon back like a red gate, named for what it is.
func TestMergeLocalWagonRedLightGate(t *testing.T) {
	y := newTrainYard(t, "true")
	ran := y.gates("true", "echo light says no; exit 1", true)
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	trainWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "new" || b.Hold {
		t.Fatalf("wagon %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-e"); got != trainWas || ranGates(t, ran) != "light" {
		t.Fatalf("train branch moved (%s, was %s) or the wrong gate ran (%q)", got, trainWas, ranGates(t, ran))
	}
	notes := y.yardr("prime", "--bead", "r-1")
	for _, want := range []string{"the light gate failed on yard/r-1", "light says no"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
}

// A wagon with no train lands on the depot's base, where no full gate would
// follow: it gets the full gate. (yardr moves no such wagon out of the stage
// its flow starts in, and the dispatcher starts no script for one; this
// one's type was changed after it was approved, and the script is run
// directly. Landed, it is closed: the move to merged is refused too.)
func TestMergeLocalWagonWithoutATrainGetsTheFullGate(t *testing.T) {
	y := newYard(t)
	ran := y.gates("true", "true", true)
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Status != "closed" || y.git(y.repo, "rev-parse", "main") != y.git(y.wt, "rev-parse", "HEAD") {
		t.Fatalf("wagon without a train is %s in %s, want landed on main under the full gate and closed", b.Status, b.Stage)
	}
	if got := ranGates(t, ran); got != "full" {
		t.Fatalf("gates run for a wagon with no train = %q, want the full one only", got)
	}
	if notes := y.notes(); strings.Contains(notes, "light gate") {
		t.Fatalf("the note names the light gate:\n%s", notes)
	}
}

// A depot with no light gate: a wagon of a train runs the full one, and the
// assembly's note carries depot check's line saying so.
func TestMergeLocalWagonInADepotWithoutALightGate(t *testing.T) {
	y := newTrainYard(t, "true")
	ran := y.gates("exit 1", "", false)
	y.yardr("bead", "update", "r-1", "--type", "wagon")

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "new" {
		t.Fatalf("wagon is in %s, want back in new: the full gate is red", b.Stage)
	}
	if got := ranGates(t, ran); got != "full" {
		t.Fatalf("gates run = %q, want the full one", got)
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "depot r has no light gate") ||
		!strings.Contains(notes, "running the full gate") {
		t.Fatalf("the note does not say the full gate ran for want of a light one:\n%s", notes)
	}
}

// A wagon's light gate is compared with its train's branch as the wagon was
// just rebased onto it, not with the depot's base: the second wagon to land is
// gated on its own change, not on the first wagon's again.
func TestMergeLocalWagonLightGateIsComparedWithTheTrain(t *testing.T) {
	y := newTrainYard(t, "true")
	seen := filepath.Join(y.home, "seen")
	// What the gate is compared with, and what changed since.
	y.gates("exit 1", `echo "$YARDR_CHECK_BASE $(git diff --name-only "$(git merge-base "$YARDR_CHECK_BASE" HEAD)" | tr '\n' ' ')" >>`+seen, true)
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	y.yardr("bead", "update", "r-2", "--type", "wagon")
	base := y.git(y.repo, "rev-parse", "main")

	y.exec("merge-local", "assembly", "r-1")
	train := y.git(y.repo, "rev-parse", "yard/r-e")
	y.exec("merge-local", "assembly", "r-2")

	for _, id := range []string{"r-1", "r-2"} {
		if b := y.beadOf(id); b.Stage != "merged" || b.Status != "closed" {
			t.Fatalf("wagon %s is %s/%s, want merged and closed", id, b.Stage, b.Status)
		}
	}
	if train == base {
		t.Fatal("the first wagon did not move the train's branch")
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	// The train's branch was at the depot's base when the first wagon was gated.
	if want := base + " child1.txt \n" + train + " child2.txt \n"; string(b) != want {
		t.Fatalf("the light gates saw (base, then what changed):\n%swant:\n%s", b, want)
	}
}

// A yardr from before the types were renamed says part and YARDR_EPIC where
// this one says wagon and YARDR_TRAIN: merge-local still takes that for a wagon
// of a train, so a session started before an upgrade lands as it would have.
func TestMergeLocalReadsTheOldWordsForAWagonOfATrain(t *testing.T) {
	y := newTrainYard(t, "true")
	ran := y.gates("exit 1", "true", true)
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	// As that yardr's runner said it: the cmd's assignments come after the
	// runner's environment. YARDR_EPIC is the runner's own, still set.
	y.opts = "YARDR_TYPE=part YARDR_TRAIN="

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("wagon %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if got := ranGates(t, ran); got != "light" {
		t.Fatalf("gates run = %q, want the light one only", got)
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "the full gate and the review are train r-e's") {
		t.Fatalf("the note does not name the train:\n%s", notes)
	}
}
