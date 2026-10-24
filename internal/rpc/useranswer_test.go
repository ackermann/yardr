package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// gitRepo is a git checkout on main holding files, for a depot whose flows
// are read from its base branch.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "files")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// An answer that would move the bead, made where the depot's repository
// cannot be read, fails on the yard's own file: a device that is no
// operator's is told that it failed and not on which file, and the full
// words are logged for the operator. A device of the operator is told the
// error, which names the repository. Nothing is recorded: the answer's
// write rolls back with the move.
func TestUserAnswerAskHeldWhenTheDepotCannotBeRead(t *testing.T) {
	ctx := context.Background()
	const flow = "new -> review\nreview -> review | approved on done | new on changes\napproved -> merged\nmerged terminal\n"
	const review = `Review the diff.

## Ask
kind: choice
question: Where should this bead go next?
context:
- The builder asked for a decision; the question is in their note.
- The branch is ready to review either way.
recommend: 1
why: The question is small and the branch is green.
done: The bead moves to approved with your words as the note.
changes: The bead goes back for changes.
`
	repo := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":      flow,
		".yardr/flows/default/review.md": review,
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.AddUser(ctx, "jo", "jo")
	must(err)
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err = st.AddUser(ctx, "ada", "jo")
	must(err)
	must(st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "jo"}}, "jo"))
	_, err = st.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo")
	must(err)
	must(st.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotGit, Path: repo, Base: "main"}, "jo"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Paint the shed", Depot: "web", Stage: "new"}, "jo")
	must(err)
	if _, err = st.Advance(ctx, b.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	phone, err := st.AddDevice(ctx, "jo", "phone", "jo")
	must(err)
	tablet, err := st.AddDevice(ctx, "ada", "tablet", "jo")
	must(err)
	asks := map[string]uint64{}
	notes, err := st.Notes(ctx, b.ID)
	must(err)
	for _, n := range notes {
		if n.Kind != store.NoteAsk {
			continue
		}
		a, err := store.ParseAsk(n)
		must(err)
		asks[a.To] = n.Seq
	}
	if len(asks) != 2 {
		t.Fatalf("the bead's asks are for %v, want one each for ada and jo", asks)
	}
	// The checkout goes away after the bead entered review: the answer's
	// move cannot read the depot's repository.
	must(os.Rename(repo, repo+"-gone"))

	var log bytes.Buffer
	u := NewUserServer(st, func(context.Context) string { return "south" },
		slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h := u.Handler()
	answer := func(token, to string) string {
		t.Helper()
		return call(t, h, token, "AnswerAsk",
			fmt.Sprintf(`{"bead":%q,"ask":%d,"option":1,"words":"ship it"}`, b.ID, asks[to]))
	}

	raw := answer(tablet.Token, "ada")
	var ada struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &ada); err != nil || ada.Error == nil {
		t.Fatalf("ada's device is answered %s (%v), want an error", raw, err)
	}
	if ada.Error.Message != callHeld {
		t.Errorf("ada's device is told %q, want %q", ada.Error.Message, callHeld)
	}
	if strings.Contains(ada.Error.Message, repo) {
		t.Errorf("ada's device is told the repository: %q", ada.Error.Message)
	}
	jo := answer(phone.Token, "jo")
	if !strings.Contains(jo, "resolving base main in "+repo) {
		t.Errorf("the operator's device is answered %s, want the error that names the repository", jo)
	}
	if !strings.Contains(log.String(), "a device's request failed on the yard's file") ||
		!strings.Contains(log.String(), "resolving base main in "+repo) {
		t.Errorf("the listener's log does not hold the full words:\n%s", log.String())
	}
	if cur, err := st.GetBead(ctx, b.ID); err != nil || cur.Stage != "review" {
		t.Errorf("the bead is in %v, %v: want review, unmoved", cur, err)
	}
	notes, err = st.Notes(ctx, b.ID)
	must(err)
	for _, n := range notes {
		if n.Kind == store.NoteAnswer {
			t.Errorf("an answer was recorded: %+v", n)
		}
	}
}
