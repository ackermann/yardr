package assembly

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// landings gives depot r a test command that appends "landing" to a file
// when it is run with YARDR_LANDING and "wagon" when it is not, and then runs
// what it is given for that case. It returns the file: what it holds is how
// the command was run, each time.
func (y *yard) landings(landing, wagon string) string {
	y.t.Helper()
	ran := filepath.Join(y.home, "ran")
	r := store.Depot{Name: "r", Path: y.repo, Base: "main",
		Test: `if [ -n "$YARDR_LANDING" ]; then echo landing >>` + ran + "; " + landing + "; else echo wagon >>" + ran + "; " + wagon + "; fi"}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutDepot(context.Background(), r, "test"); err != nil {
		y.t.Fatal(err)
	}
	return ran
}

func ranAs(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// A wagon of a train lands on the train's branch with the test command run
// without YARDR_LANDING, and its note says so; its sibling of another type,
// landing on the same branch, is a landing, as it always was.
func TestMergeLocalWagonOfATrainIsTestedWithoutLanding(t *testing.T) {
	y := newTrainYard(t, "true")
	// A landing is red: a wagon that passes was not run as one.
	ran := y.landings("exit 1", "true")
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	trainWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("wagon %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if got := ranAs(t, ran); got != "wagon" {
		t.Fatalf("the test command ran as %q for a wagon of a train, want without YARDR_LANDING, once", got)
	}
	if y.git(y.repo, "rev-list", "--count", trainWas+"..yard/r-e") != "1" {
		t.Fatal("the wagon did not land on the train's branch")
	}
	if notes := y.yardr("prime", "--bead", "r-1"); !strings.Contains(notes, "on yard/r-e, tested with the command") ||
		!regexp.MustCompile(`without YARDR_LANDING in \d+ s; the landing run and the review are train r-e's`).MatchString(notes) {
		t.Fatalf("the note does not say how it was tested:\n%s", notes)
	}

	// r-2 is a task under the same train.
	y.exec("merge-local", "assembly", "r-2")
	if got := ranAs(t, ran); got != "wagon landing" {
		t.Fatalf("the test command ran as %q after a task of the train, want a landing for it", got)
	}
	if b := y.beadOf("r-2"); b.Stage != "new" {
		t.Fatalf("task under a red landing is in %s, want back in new", b.Stage)
	}
}

// A wagon's red run sends it back like any red run, named for what it is.
func TestMergeLocalWagonRedTests(t *testing.T) {
	y := newTrainYard(t, "true")
	ran := y.landings("true", "echo wagon says no; exit 1")
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	trainWas := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "assembly", "r-1")

	if b := y.beadOf("r-1"); b.Stage != "new" || b.Hold {
		t.Fatalf("wagon %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-e"); got != trainWas || ranAs(t, ran) != "wagon" {
		t.Fatalf("train branch moved (%s, was %s) or the command ran as %q", got, trainWas, ranAs(t, ran))
	}
	notes := y.yardr("prime", "--bead", "r-1")
	for _, want := range []string{"the wagon's tests failed on yard/r-1", "wagon says no"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
}

// A wagon with no train lands on the depot's base, where no run for a train
// would follow: it is a landing. (yardr moves no such wagon out of the stage
// its flow starts in, and the dispatcher starts no script for one; this
// one's type was changed after it was approved, and the script is run
// directly. Landed, it is closed: the move to merged is refused too.)
func TestMergeLocalWagonWithoutATrainIsALanding(t *testing.T) {
	y := newYard(t)
	ran := y.landings("true", "true")
	y.yardr("bead", "update", "r-1", "--type", "wagon")
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Status != "closed" || y.git(y.repo, "rev-parse", "main") != y.git(y.wt, "rev-parse", "HEAD") {
		t.Fatalf("wagon without a train is %s in %s, want landed on main and closed", b.Status, b.Stage)
	}
	if got := ranAs(t, ran); got != "landing" {
		t.Fatalf("the test command ran as %q for a wagon with no train, want a landing", got)
	}
	if notes := y.notes(); strings.Contains(notes, "without YARDR_LANDING") {
		t.Fatalf("the note says it was no landing:\n%s", notes)
	}
}

// A wagon's test command is told its train's branch as the base, as the wagon
// was just rebased onto it, not the depot's base: the second wagon to land is
// tested on its own change, not on the first wagon's again.
func TestMergeLocalWagonIsComparedWithTheTrain(t *testing.T) {
	y := newTrainYard(t, "true")
	seen := filepath.Join(y.home, "seen")
	// What the run is compared with, and what changed since.
	y.landings("exit 1", `echo "$YARDR_BASE $(git rev-parse "$YARDR_BASE") $(git diff --name-only "$(git merge-base "$YARDR_BASE" HEAD)" | tr '\n' ' ')" >>`+seen)
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
	// The train's branch was at the depot's base when the first wagon was tested.
	if want := "yard/r-e " + base + " child1.txt \nyard/r-e " + train + " child2.txt \n"; string(b) != want {
		t.Fatalf("the wagons' runs saw (base, where it was, what changed):\n%swant:\n%s", b, want)
	}
}
