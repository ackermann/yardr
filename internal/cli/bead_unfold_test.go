package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// bead unfold takes a member out of its carrier and says so in a line; the
// part reopens in --to, else in the flow's first stage, with the reason as a
// note. The reason is required, and what the store refuses is told as it is.
func TestBeadUnfold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, newBead := foldYard(t)
	yard := &app{st: st, actor: "me"}

	// foldOf folds the beads waiting in the stages given and answers the
	// carrier and the members.
	foldOf := func(t *testing.T, stages ...string) (string, []string) {
		t.Helper()
		ids := make([]string, len(stages))
		for i, stage := range stages {
			ids[i] = newBead(stage)
		}
		out, err := run(t, yard.beadCmd(), append(append([]string{"fold"}, ids...), "--to", "ready")...)
		if err != nil {
			t.Fatal(err)
		}
		carrier, _, _ := strings.Cut(out, " folded from ")
		return carrier, ids
	}

	t.Run("the line names the part, where it reopens and the commit to revert", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		out, err := run(t, yard.beadCmd(), "unfold", carrier, ids[1], "--reason", "part 2 breaks the gate", "--to", "new")
		if err != nil {
			t.Fatal(err)
		}
		if want := ids[1] + " reopened in new, out of " + carrier + "; revert its commit on yard/" + carrier + "\n"; out != want {
			t.Fatalf("the line of an unfold:\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("without --to the part reopens in the flow's first stage", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		out, err := run(t, yard.beadCmd(), "unfold", carrier, ids[0], "--reason", "why it comes out")
		if err != nil {
			t.Fatal(err)
		}
		if want := ids[0] + " reopened in backlog, out of " + carrier + "; revert its commit on yard/" + carrier + "\n"; out != want {
			t.Fatalf("the line of an unfold:\n%q\nwant\n%q", out, want)
		}
		if b, err := st.GetBead(ctx, ids[0]); err != nil || b.Stage != "backlog" || b.Status != "open" {
			t.Errorf("the part after the unfold: %+v, %v", b, err)
		}
	})

	t.Run("the reason is required", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		if out, err := run(t, yard.beadCmd(), "unfold", carrier, ids[0]); err == nil || !strings.Contains(err.Error(), "--reason") {
			t.Fatalf("unfold without a reason = %q, %v", out, err)
		}
		if b, _ := st.GetBead(ctx, ids[0]); b == nil || b.Status != "closed" {
			t.Errorf("%s after the refusal: %+v", ids[0], b)
		}
	})

	t.Run("--json is the member and the carrier it came out of", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		j := &app{st: st, actor: "me", json: true}
		out, err := run(t, j.beadCmd(), "unfold", carrier, ids[0], "--reason", "why it comes out", "--to", "new")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ID      string
			Stage   string
			Status  string
			Carrier string
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got.ID != ids[0] || got.Stage != "new" || got.Status != "open" || got.Carrier != carrier {
			t.Fatalf("unfold --json:\n%s", out)
		}
	})

	t.Run("a bead typed by its uuid is named by its handle", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		bc, err := st.GetBead(ctx, carrier)
		if err != nil {
			t.Fatal(err)
		}
		bm, err := st.GetBead(ctx, ids[0])
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(t, yard.beadCmd(), "unfold", bc.UUID, bm.UUID, "--reason", "why it comes out", "--to", "new")
		if err != nil {
			t.Fatal(err)
		}
		if want := ids[0] + " reopened in new, out of " + carrier + "; revert its commit on yard/" + carrier + "\n"; out != want {
			t.Fatalf("the line of an unfold:\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("a bead that is not a member is refused", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		loose := newBead("backlog")
		out, err := run(t, yard.beadCmd(), "unfold", carrier, loose, "--reason", "why it comes out", "--to", "new")
		if err == nil || !strings.Contains(err.Error(), "is not a member of") {
			t.Fatalf("unfold of a bead in no fold = %q, %v", out, err)
		}
		// Nothing was reopened or taken out.
		for _, id := range ids {
			if b, _ := st.GetBead(ctx, id); b == nil || b.Status != "closed" {
				t.Errorf("%s after the refusal: %+v", id, b)
			}
		}
		if b, _ := st.GetBead(ctx, carrier); b == nil || b.Status != "open" {
			t.Errorf("%s after the refusal: %+v", carrier, b)
		}
	})

	t.Run("a bead the yard does not know unfolds nothing", func(t *testing.T) {
		carrier, ids := foldOf(t, "backlog", "backlog")
		if out, err := run(t, yard.beadCmd(), "unfold", carrier, "yd-nope", "--reason", "why", "--to", "new"); err == nil {
			t.Fatalf("unfold of an unknown bead = %q", out)
		}
		if b, _ := st.GetBead(ctx, ids[0]); b == nil || b.Status != "closed" {
			t.Errorf("%s after the refusal: %+v", ids[0], b)
		}
	})
}
