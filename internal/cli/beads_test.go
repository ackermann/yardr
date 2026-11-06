package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// run executes c with args, returning its stdout and error. A command left
// on the process's streams writes to buffers of its own: what a test drives
// never reaches the process's stdout or stderr. A writer the test preset
// (SetOut or SetErr before run) is left alone, so a test that asserts
// warnings on its own error buffer keeps working, and run returns no
// stdout then.
func run(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	// Never nil: with no arguments set, cobra runs the binary's own
	// (os.Args), and what a command does must not depend on how the test
	// binary was started.
	c.SetArgs(append([]string{}, args...))
	c.SilenceUsage, c.SilenceErrors = true, true
	var outBuf, errBuf bytes.Buffer
	if out, ok := c.OutOrStdout().(*os.File); ok && out == os.Stdout {
		c.SetOut(&outBuf)
	}
	if errStream, ok := c.ErrOrStderr().(*os.File); ok && errStream == os.Stderr {
		c.SetErr(&errBuf)
	}
	err := c.Execute()
	return outBuf.String(), err
}

// executeOut runs args through execute, the entry of the whole program, on
// a with its stdout in a buffer of its own, and returns what was printed
// and the exit code. A stdout the test preset on a is left alone, as run
// leaves a command's, and nothing is returned then.
func executeOut(a *app, args ...string) (string, int) {
	var out bytes.Buffer
	if a.stdout == nil {
		a.stdout = &out
	}
	code := execute(a, args)
	return out.String(), code
}

type runWriterFunc func([]byte) (int, error)

func (f runWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestRunPreservesUncomparablePresetWriter(t *testing.T) {
	t.Parallel()
	var got bytes.Buffer
	c := &cobra.Command{RunE: func(c *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(c.OutOrStdout(), "preset")
		return err
	}}
	c.SetOut(runWriterFunc(func(p []byte) (int, error) { return got.Write(p) }))

	out, err := run(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("run captured preset output %q, want none", out)
	}
	if got.String() != "preset\n" {
		t.Errorf("preset writer got %q, want %q", got.String(), "preset\n")
	}
}

// run takes a command's output in a buffer and leaves the process's
// streams as they are: no pipe stands in for os.Stdout while the command
// runs, and what the command says on its error stream is not in what run
// returns.
func TestRunLeavesTheProcessStreamsAlone(t *testing.T) {
	t.Parallel()
	stdout, stderr := os.Stdout, os.Stderr
	c := &cobra.Command{RunE: func(c *cobra.Command, _ []string) error {
		if os.Stdout != stdout || os.Stderr != stderr {
			return errors.New("a process stream was swapped while the command ran")
		}
		fmt.Fprintln(c.OutOrStdout(), "to stdout")
		fmt.Fprintln(c.ErrOrStderr(), "to stderr")
		return nil
	}}

	out, err := run(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if out != "to stdout\n" {
		t.Errorf("run returned %q, want what the command wrote to its stdout alone", out)
	}
}

// bead update refuses a pin naming no flow, rather than store it and fail
// later, when dispatch or a move resolves it.
func TestBeadUpdateRejectsUnknownPin(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	if _, err := run(t, a.beadCmd(), "update", b.ID, "--meta", "flow=ghost"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("update --meta flow=ghost: %v", err)
	}
	if got, _ := st.GetBead(ctx, b.ID); store.PinnedFlow(got) != "" || got.Revision != b.Revision {
		t.Fatalf("refused pin changed the bead: %+v", got)
	}
	// Create refuses it too.
	if _, err := run(t, a.beadCmd(), "create", "y", "--depot", "yard", "--meta", "flow=ghost"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("create --meta flow=ghost: %v", err)
	}
	if _, err := run(t, a.beadCmd(), "update", b.ID, "--meta", "flow=code"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetBead(ctx, b.ID); store.PinnedFlow(got) != "code" {
		t.Fatalf("pin = %q, want code", store.PinnedFlow(got))
	}
}

// bead update -b - and --title - take the body and the title from the
// command's stdin. An empty read is refused and the field is left alone: it
// is a broken pipe, not an intent to clear. Clearing a body on purpose stays
// -b "". Stdin can be read once, so with both the first line is the title
// and the lines after it the body, and a title alone is one line.
func TestBeadUpdateStdin(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Body: "the body"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	update := func(id, stdin string, flags ...string) error {
		c := a.beadCmd()
		c.SetIn(strings.NewReader(stdin))
		_, err := run(t, c, append([]string{"update", id}, flags...)...)
		return err
	}
	unchanged := func() {
		t.Helper()
		if got, _ := st.GetBead(ctx, b.ID); got.Body != "the body" || got.Revision != b.Revision {
			t.Fatalf("refused update changed the bead: %+v", got)
		}
	}
	for _, stdin := range []string{"", " \n\n"} {
		if err := update(b.ID, stdin, "-b", "-"); err == nil || !strings.Contains(err.Error(), "nothing to write") ||
			!strings.Contains(err.Error(), b.ID) || !strings.Contains(err.Error(), `-b ""`) {
			t.Fatalf("update -b - with %q stdin: %v", stdin, err)
		}
		unchanged()
	}
	if err := update(b.ID, "", "-b", ""); err != nil {
		t.Fatalf("update -b \"\": %v", err)
	}
	if got, _ := st.GetBead(ctx, b.ID); got.Body != "" {
		t.Fatalf("update -b \"\" left body %q", got.Body)
	}
	if err := update(b.ID, "a new body\n", "-b", "-"); err != nil {
		t.Fatalf("update -b - with a body: %v", err)
	}
	if got, _ := st.GetBead(ctx, b.ID); got.Body != "a new body" {
		t.Fatalf("update -b - wrote body %q", got.Body)
	}

	// The title reads stdin the same way: a refusal says why, and leaves
	// the title and the body as they were.
	tb, err := st.CreateBead(ctx, store.NewBead{Title: "the title", Body: "the body"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		stdin string
		flags []string
		want  string
	}{
		{"", []string{"--title", "-"}, "stdin gave no title, so bead " + tb.ID + "'s title is unchanged"},
		{" \n\n", []string{"--title", "-"}, "stdin gave no title"},
		{"\n\nthe body\n", []string{"--title", "-", "-b", "-"}, "stdin gave no title"},
		{"one\ntwo\n", []string{"--title", "-"}, "a title is one line, and stdin gave 2"},
	} {
		if err := update(tb.ID, row.stdin, row.flags...); err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("update %v with %q stdin: %v", row.flags, row.stdin, err)
		}
		if got, _ := st.GetBead(ctx, tb.ID); got.Title != "the title" || got.Body != "the body" || got.Revision != tb.Revision {
			t.Fatalf("refused update %v changed the bead: %+v", row.flags, got)
		}
	}
	if err := update(tb.ID, "a `new` title, $(not run)\n\n", "--title", "-"); err != nil {
		t.Fatalf("update --title -: %v", err)
	}
	if got, _ := st.GetBead(ctx, tb.ID); got.Title != "a `new` title, $(not run)" || got.Body != "the body" {
		t.Fatalf("update --title - wrote %+v", got)
	}
	if err := update(tb.ID, "  spaced title \t\n", "--title", "-"); err != nil {
		t.Fatalf("update --title - with surrounding spaces: %v", err)
	}
	if got, _ := st.GetBead(ctx, tb.ID); got.Title != "  spaced title \t" {
		t.Fatalf("update --title - trimmed the title: %q", got.Title)
	}
	if err := update(tb.ID, "  both \t\n\nthe body again\nin two lines\n\n", "--title", "-", "-b", "-"); err != nil {
		t.Fatalf("update --title - -b -: %v", err)
	}
	if got, _ := st.GetBead(ctx, tb.ID); got.Title != "  both \t" || got.Body != "the body again\nin two lines" {
		t.Fatalf("update --title - -b - wrote %+v", got)
	}
}

// bead create - takes the title from the command's stdin, and with -b - the
// body too: the first line is the title and the lines after it the body.
// A title with no words, or two lines without -b -, is refused and makes no
// bead.
func TestBeadCreateStdin(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	create := func(stdin string, flags ...string) (*store.Bead, error) {
		c := a.beadCmd()
		c.SetIn(strings.NewReader(stdin))
		out, err := run(t, c, append([]string{"create"}, flags...)...)
		if err != nil {
			return nil, err
		}
		return st.GetBead(ctx, strings.TrimSpace(out))
	}
	// The body from stdin, trailing newlines trimmed as before: piped
	// input stores identical bytes.
	got, err := create("the body\nwith two lines\n\n", "x", "-b", "-")
	if err != nil {
		t.Fatalf("create x -b -: %v", err)
	}
	if got.Title != "x" || got.Body != "the body\nwith two lines" {
		t.Fatalf("create x -b - wrote %+v", got)
	}
	// The title from stdin, as typed: a code span and $( ) stay text.
	got, err = create("the `title`, $(not run)\n\n", "-")
	if err != nil {
		t.Fatalf("create -: %v", err)
	}
	if got.Title != "the `title`, $(not run)" || got.Body != "" {
		t.Fatalf("create - wrote %+v", got)
	}
	got, err = create("  spaced title \t\n\n", "-")
	if err != nil {
		t.Fatalf("create - with surrounding spaces: %v", err)
	}
	if got.Title != "  spaced title \t" || got.Body != "" {
		t.Fatalf("create - trimmed the title: %q", got.Title)
	}
	got, err = create("  the title \t\n\nthe body\nwith two lines\n\n", "-", "-b", "-")
	if err != nil {
		t.Fatalf("create - -b -: %v", err)
	}
	if got.Title != "  the title \t" || got.Body != "the body\nwith two lines" {
		t.Fatalf("create - -b - wrote %+v", got)
	}
	before, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		stdin string
		flags []string
		want  string
	}{
		{"", []string{"-"}, "nothing to create: stdin gave no title"},
		{" \n\n", []string{"-"}, "nothing to create: stdin gave no title"},
		{"\n\nthe body\n", []string{"-", "-b", "-"}, "nothing to create: stdin gave no title"},
		{"one\ntwo\n", []string{"-"}, "a title is one line, and stdin gave 2"},
	} {
		if _, err := create(row.stdin, row.flags...); err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("create %v with %q stdin: %v", row.flags, row.stdin, err)
		}
	}
	if after, err := st.ListBeads(ctx, store.BeadFilter{}); err != nil || len(after) != len(before) {
		t.Fatalf("refused creates made beads: %d before, %d after, %v", len(before), len(after), err)
	}
}

// bead close --reason - takes the reason from the command's stdin, trailing
// newlines trimmed. One with no words is refused before anything closes.
func TestBeadCloseStdinReason(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	closeWith := func(stdin string) error {
		c := a.beadCmd()
		c.SetIn(strings.NewReader(stdin))
		_, err := run(t, c, "close", b.ID, "--reason", "-")
		return err
	}
	for _, stdin := range []string{"", " \n\n"} {
		if err := closeWith(stdin); err == nil || !strings.Contains(err.Error(), "nothing to write: stdin gave no reason") ||
			!strings.Contains(err.Error(), b.ID+" is still open") {
			t.Fatalf("close --reason - with %q stdin: %v", stdin, err)
		}
		if got, _ := st.GetBead(ctx, b.ID); got.Status == "closed" || got.Revision != b.Revision {
			t.Fatalf("refused close changed the bead: %+v", got)
		}
	}
	if err := closeWith("merged as `abc123`, $(not run)\nin two lines\n\n"); err != nil {
		t.Fatalf("close --reason -: %v", err)
	}
	got, err := st.GetBead(ctx, b.ID)
	if err != nil || got.Status != "closed" || got.CloseReason == nil || *got.CloseReason != "merged as `abc123`, $(not run)\nin two lines" {
		t.Fatalf("close --reason - left %+v, %v", got, err)
	}
}

// show, prime and review still work for a bead whose flow does not
// resolve, and say why next to it.
func TestBrokenFlowStillShown(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, "")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "stuck", Depot: "yard", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The depot's pointer goes; its checkout is no git repository, so its
	// repo flows cannot be read.
	if err := st.SetFlowPointer(ctx, "yard", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "mckean"}

	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil || !strings.Contains(out, "stuck") || !strings.Contains(out, "flow error:") || !strings.Contains(out, "not a git repository") {
		t.Fatalf("bead show = %q, %v", out, err)
	}
	out, err = run(t, a.primeCmd(), "--bead", b.ID)
	if err != nil || !strings.Contains(out, "flow error:") || !strings.Contains(out, "OUTCOMES: none until the flow is fixed") {
		t.Fatalf("prime = %q, %v", out, err)
	}
	items, err := reviewQueue(ctx, st, "mckean", nil)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].FlowError, "not a git repository") {
		t.Fatalf("review queue = %+v, %v", items, err)
	}
	for _, ac := range items[0].Actions {
		if ac.Kind != actDiff && ac.Kind != actSkip {
			t.Fatalf("broken flow offers %+v", ac)
		}
	}
}

// dep add -k parent adds the child's blocks edge to its train too, and says
// so; dep rm -k parent takes both away.
func TestDepAddParentPairsBlocks(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "train", Type: store.TypeTrain, Depot: "yard"},
		{ID: "yard-1", Title: "child", Depot: "yard"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-e", "yard-1", "-k", "parent")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-e -parent-> yard-1\nyard-1 -blocks-> yard-e\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if bs, _ := st.OpenBlockers(ctx, "yard-e"); len(bs) != 1 || bs[0] != "yard-1" {
		t.Fatalf("train blocked by %v, want yard-1", bs)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-e", "yard-1", "-k", "parent"); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.Deps(ctx, "yard-e"); len(ds) != 0 {
		t.Fatalf("edges left after dep rm -k parent: %+v", ds)
	}
}

// dep add --at stages the edge and says so; dep rm --at removes exactly
// that edge, and without it a staged edge stays. The stage must be one of
// the target's flow, and --at is refused for any other kind.
func TestDepAddAtStagesTheEdge(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-1", Title: "one", Depot: "yard", Stage: "new"},
		{ID: "yard-2", Title: "two", Depot: "yard", Stage: "new"},
		{ID: "yard-3", Title: "three", Depot: "yard", Stage: "new"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-1", "yard-2", "--at", "approved")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-2 at approved\n"; out != want {
		t.Fatalf("dep add --at printed %q, want %q", out, want)
	}
	// The plain echo is unchanged.
	out, err = run(t, a.depCmd(), "add", "yard-1", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-3\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-3", "--at", "launch"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("dep add --at launch: err = %v", err)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-3", "-k", "related", "--at", "approved"); err == nil ||
		!strings.Contains(err.Error(), "--at") {
		t.Fatalf("dep add -k related --at: err = %v", err)
	}
	// --json carries the stage.
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.depCmd(), "add", "yard-2", "yard-3", "--at", "approved")
	if err != nil {
		t.Fatal(err)
	}
	var got store.Dep
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.At != "approved" {
		t.Fatalf("dep add --json --at: %q (%v)", out, err)
	}
	// rm without --at leaves a staged edge alone; with it, removes exactly it.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2"); err != nil {
		t.Fatal(err)
	}
	staged := func() []store.Dep {
		t.Helper()
		ds, err := st.Deps(ctx, "yard-2")
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	}
	if ds := staged(); len(ds) != 1 || ds[0].At != "approved" {
		t.Fatalf("yard-1's edges after dep rm without --at: %+v", ds)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2", "--at", "approved"); err != nil {
		t.Fatal(err)
	}
	if ds := staged(); len(ds) != 0 {
		t.Fatalf("yard-1's edges after dep rm --at approved: %+v", ds)
	}
}

// dep add --until stages the edge and says so, alone and with --at; dep rm
// --until removes exactly that edge, and without it a staged edge stays.
// The stage must be one of the blocker's flow, and --until is refused for
// any other kind.
func TestDepAddUntilStagesTheEdge(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-1", Title: "one", Depot: "yard", Stage: "new"},
		{ID: "yard-2", Title: "two", Depot: "yard", Stage: "new"},
		{ID: "yard-3", Title: "three", Depot: "yard", Stage: "new"},
		{ID: "yard-4", Title: "four", Depot: "yard", Stage: "new"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-1", "yard-2", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-2 until review\n"; out != want {
		t.Fatalf("dep add --until printed %q, want %q", out, want)
	}
	out, err = run(t, a.depCmd(), "add", "yard-1", "yard-3", "--at", "approved", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-3 at approved until review\n"; out != want {
		t.Fatalf("dep add --at --until printed %q, want %q", out, want)
	}
	// The plain echo is unchanged.
	out, err = run(t, a.depCmd(), "add", "yard-2", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-2 -blocks-> yard-3\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-4", "--until", "launch"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("dep add --until launch: err = %v", err)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-4", "-k", "related", "--until", "review"); err == nil ||
		!strings.Contains(err.Error(), "--until") {
		t.Fatalf("dep add -k related --until: err = %v", err)
	}
	// --json carries the stage.
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.depCmd(), "add", "yard-2", "yard-4", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	var got store.Dep
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Until != "review" {
		t.Fatalf("dep add --json --until: %q (%v)", out, err)
	}
	// rm without --until leaves a staged edge alone; with it, removes exactly it.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2"); err != nil {
		t.Fatal(err)
	}
	staged := func() []store.Dep {
		t.Helper()
		ds, err := st.Deps(ctx, "yard-2")
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	}
	if ds := staged(); len(ds) != 1 || ds[0].Until != "review" {
		t.Fatalf("yard-1's edges after dep rm without --until: %+v", ds)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2", "--until", "review"); err != nil {
		t.Fatal(err)
	}
	if ds := staged(); len(ds) != 0 {
		t.Fatalf("yard-1's edges after dep rm --until review: %+v", ds)
	}
	// rm --at alone leaves an edge that also names until.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-3", "--at", "approved"); err != nil {
		t.Fatal(err)
	}
	ds, err := st.Deps(ctx, "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	ds = slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	if len(ds) != 1 || ds[0].At != "approved" || ds[0].Until != "review" {
		t.Fatalf("yard-1's edges after dep rm --at approved: %+v", ds)
	}
}

// bead list, ready and session list show each bead's train in a TRAIN
// column after the bead, and in --json; bead list --train lists one train's
// family; bead show --json still carries the train.
func TestListingsShowTheTrain(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "train", Type: store.TypeTrain, Depot: "yard"},
		{ID: "yard-1", Title: "child", Depot: "yard"},
		{ID: "yard-2", Title: "loose", Depot: "yard"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddDep(ctx, "yard-e", "yard-1", store.DepParent, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"yard-1", "yard-2"} {
		if _, err := st.Claim(ctx, id, "builders", "new", "yard"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	// fields are the whitespace-separated cells of out's row for id.
	fields := func(out, id string) []string {
		for _, l := range strings.Split(out, "\n") {
			if f := strings.Fields(l); len(f) > 1 && slices.Contains(f[:2], id) {
				return f
			}
		}
		t.Fatalf("no row for %s in:\n%s", id, out)
		return nil
	}
	out, err := run(t, a.beadCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.Fields(out); !slices.Equal(h[:3], []string{"ID", "TRAIN", "PRI"}) {
		t.Fatalf("bead list header:\n%s", out)
	}
	if f := fields(out, "yard-1"); f[1] != "yard-e" {
		t.Fatalf("bead list child row %v, want train yard-e second", f)
	}
	if f := fields(out, "yard-2"); f[1] != "P0" {
		t.Fatalf("bead list plain row %v, want an empty train", f)
	}
	out, err = run(t, a.readyCmd())
	if err != nil {
		t.Fatal(err)
	}
	if f := fields(out, "yard-1"); f[1] != "yard-e" {
		t.Fatalf("ready child row %v", f)
	}
	out, err = run(t, a.sessionCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.Fields(out); !slices.Equal(h[:4], []string{"ID", "BEAD", "TRAIN", "STAGE"}) {
		t.Fatalf("session list header:\n%s", out)
	}
	if f := fields(out, "yard-1"); f[2] != "yard-e" {
		t.Fatalf("session list child row %v", f)
	}
	if f := fields(out, "yard-2"); f[2] != "new" {
		t.Fatalf("session list plain row %v, want an empty train", f)
	}

	out, err = run(t, a.beadCmd(), "list", "--train", "yard-e")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		ids = append(ids, strings.Fields(l)[0])
	}
	if !slices.Equal(ids, []string{"yard-e", "yard-1"}) {
		t.Fatalf("bead list --train yard-e = %v:\n%s", ids, out)
	}

	a.json = true
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
	}{{a.beadCmd(), []string{"list"}}, {a.readyCmd(), nil}, {a.sessionCmd(), []string{"list"}}} {
		out, err := run(t, c.cmd, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct{ ID, Bead, Train string }
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("%s: %v\n%s", c.args, err, out)
		}
		got := map[string]string{}
		for _, r := range rows {
			got[r.ID+r.Bead] = r.Train // sessions: ID is the session's, so key by both
		}
		n := 0
		for k, v := range got {
			if strings.HasSuffix(k, "yard-1") || k == "yard-1" {
				n++
				if v != "yard-e" {
					t.Fatalf("%s --json: yard-1 train %q:\n%s", c.args, v, out)
				}
			} else if v != "" {
				t.Fatalf("%s --json: %s has train %q", c.args, k, v)
			}
		}
		if n != 1 {
			t.Fatalf("%s --json: no yard-1 row:\n%s", c.args, out)
		}
	}
	out, err = run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct{ ID, Train, Base, Revision string }
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Train != "yard-e" || shown.Base != "yard/yard-e" || shown.Revision == "" {
		t.Fatalf("bead show --json = %+v, %v:\n%s", shown, err, out)
	}
}

// bead create --discovered-from records where a bead was found: bead show
// says so on both beads, in text and JSON, and bead list --discovered-from
// lists what a bead led to. An unknown origin creates nothing.
func TestBeadDiscoveredFrom(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "the origin", Depot: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	create := func(title string) string {
		t.Helper()
		out, err := run(t, a.beadCmd(), "create", title, "--discovered-from", "yard-1")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	open, shut := create("a loose end"), create("a fixed end")
	if _, err := st.CloseBead(ctx, shut, "fixed", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Created like any bead: no depot, labels or priority of the origin's.
	if b, _ := st.GetBead(ctx, open); b == nil || b.Depot != nil || b.Priority != 2 {
		t.Fatalf("the found bead: %+v", b)
	}

	out, err := run(t, a.beadCmd(), "show", open)
	if err != nil {
		t.Fatal(err)
	}
	if want := "  discovered from yard-1 the origin\n"; !strings.Contains(out, want) {
		t.Fatalf("bead show of the found bead lacks %q:\n%s", want, out)
	}
	out, err = run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  led to: " + open + " a loose end\n  led to: " + shut + " a fixed end (closed)\n"; !strings.Contains(out, want) {
		t.Fatalf("bead show of the origin lacks %q:\n%s", want, out)
	}

	listed := func(args ...string) []string {
		t.Helper()
		out, err := run(t, a.beadCmd(), append([]string{"list", "--discovered-from", "yard-1"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
			ids = append(ids, strings.Fields(l)[0])
		}
		return ids
	}
	if got := listed(); !slices.Equal(got, []string{open}) {
		t.Fatalf("bead list --discovered-from yard-1 = %v, want %s", got, open)
	}
	if got := listed("-a"); !slices.Equal(got, []string{open, shut}) {
		t.Fatalf("bead list -a --discovered-from yard-1 = %v, want both", got)
	}

	before, _ := st.ListBeads(ctx, store.BeadFilter{All: true})
	if _, err := run(t, a.beadCmd(), "create", "an orphan", "--discovered-from", "yard-nope"); err == nil {
		t.Fatal("an unknown origin was accepted")
	}
	if after, _ := st.ListBeads(ctx, store.BeadFilter{All: true}); len(after) != len(before) {
		t.Fatalf("a refused create left a bead: %d, were %d", len(after), len(before))
	}

	a.json = true
	type refs struct {
		ID             string
		DiscoveredFrom []store.BeadRef `json:"discovered_from"`
		LedTo          []store.BeadRef `json:"led_to"`
	}
	show := func(id string) refs {
		t.Helper()
		out, err := run(t, a.beadCmd(), "show", id)
		if err != nil {
			t.Fatal(err)
		}
		var r refs
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		// Both lists are there, empty when there is nothing to list.
		if r.ID != id || r.DiscoveredFrom == nil || r.LedTo == nil {
			t.Fatalf("bead show --json %s:\n%s", id, out)
		}
		return r
	}
	if r := show(open); !slices.Equal(r.DiscoveredFrom, []store.BeadRef{{ID: "yard-1", Title: "the origin", Status: "open"}}) || len(r.LedTo) != 0 {
		t.Fatalf("the found bead as JSON: %+v", r)
	}
	if r := show("yard-1"); len(r.DiscoveredFrom) != 0 || !slices.Equal(r.LedTo, []store.BeadRef{
		{ID: open, Title: "a loose end", Status: "open"}, {ID: shut, Title: "a fixed end", Status: "closed"}}) {
		t.Fatalf("the origin as JSON: %+v", r)
	}
}

// oldServer is a store whose server is from before discovered-from,
// NewBead.BlockedBy and NewBead.For: it knows neither Discovery,
// CheckBlockers nor CheckFor, and its CreateBead would drop the origin,
// the blockers and the user and create the bead without its edges and its
// originator.
type oldServer struct {
	store.API
	created int
}

func (o *oldServer) Discovery(context.Context, string) (*store.Discovery, error) {
	return nil, errors.New("no such method Discovery")
}

func (o *oldServer) CheckBlockers(context.Context, []string) error {
	return errors.New("no such method CheckBlockers")
}

func (o *oldServer) CheckFor(context.Context, string) error {
	return errors.New("no such method CheckFor")
}

func (o *oldServer) CreateBead(ctx context.Context, nb store.NewBead, actor string) (*store.Bead, error) {
	o.created++
	nb.DiscoveredFrom, nb.BlockedBy, nb.For = "", nil, ""
	return o.API.CreateBead(ctx, nb, actor)
}

// Against a server from before the flag, bead create --discovered-from
// creates nothing rather than a bead without its edge.
func TestDiscoveredFromRefusesAnOlderServer(t *testing.T) {
	t.Parallel()
	st := &oldServer{API: storetest.New(t)}
	ctx := context.Background()
	if _, err := st.API.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the origin"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "found", "--discovered-from", "yd-1"); err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("create against an older server: %v, want its refusal", err)
	}
	if st.created != 0 {
		t.Fatal("the bead was created without its edge")
	}
	// Without the flag an older server is asked nothing new.
	if _, err := run(t, a.beadCmd(), "create", "plain"); err != nil || st.created != 1 {
		t.Fatalf("a plain create: %v, %d created", err, st.created)
	}
}

// Against a server from before the flag, bead create --for creates
// nothing rather than a bead billed to the operator's subscription.
func TestCreateForRefusesAnOlderServer(t *testing.T) {
	t.Parallel()
	st := &oldServer{API: storetest.New(t)}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "for hal", "--for", "hal"); err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("create --for against an older server: %v, want its refusal", err)
	}
	if st.created != 0 {
		t.Fatal("the bead was created without its originator")
	}
}

// bead create --for names the user the bead is for, and bead show prints
// it beside the owner; an unknown --for is refused and creates nothing.
func TestBeadCreateFor(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if out, err := run(t, a.beadCmd(), "create", "hal's", "--id", "yd-1", "--for", "hal"); err != nil || strings.TrimSpace(out) != "yd-1" {
		t.Fatalf("create --for hal: %v\n%s", err, out)
	}
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil || !strings.Contains(out, "originator hal") {
		t.Fatalf("bead show:\n%s\n%v", out, err)
	}
	if out, err := run(t, a.beadCmd(), "create", "x", "--for", "mallory"); err == nil || !strings.Contains(err.Error(), "--for: user mallory: not found") {
		t.Fatalf("create --for mallory: %v\n%s", err, out)
	}
	if bs, _ := st.ListBeads(ctx, store.BeadFilter{All: true}); len(bs) != 1 {
		t.Fatalf("a refused --for left %+v", bs)
	}
	// A bead with no originator to show (no operator in this yard, and
	// an actor that is no user) shows no line for one.
	if _, err := run(t, a.beadCmd(), "create", "plain", "--id", "yd-2"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.beadCmd(), "show", "yd-2"); err != nil || strings.Contains(out, "originator") {
		t.Fatalf("bead show of a bead with none:\n%s\n%v", out, err)
	}
	// Without an operator, a user creating a bead is still its originator.
	hal := &app{st: st, actor: "hal"}
	if _, err := run(t, hal.beadCmd(), "create", "hal's own", "--id", "yd-3"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, hal.beadCmd(), "show", "yd-3"); err != nil || !strings.Contains(out, "originator hal") {
		t.Fatalf("bead show:\n%s\n%v", out, err)
	}
}

// bead show --json carries the originator.
func TestBeadShowOriginatorJSON(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "hal's", "--id", "yd-1", "--for", "hal"); err != nil {
		t.Fatal(err)
	}
	a.json = true
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		ID         string
		Originator string
	}
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b.ID != "yd-1" || b.Originator != "hal" {
		t.Fatalf("bead show --json:\n%s", out)
	}
}

// bead update --originator is the operator's to change: anyone else is
// refused, and the bead keeps its originator.
func TestBeadUpdateOriginator(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	op := &app{st: st, actor: "me"}
	if _, err := run(t, op.beadCmd(), "create", "x", "--id", "yd-1"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, op.beadCmd(), "update", "yd-1", "--originator", "hal")
	if err != nil || !strings.Contains(out, "originator hal") {
		t.Fatalf("update --originator hal: %v\n%s", err, out)
	}
	hal := &app{st: st, actor: "hal"}
	out, err = run(t, hal.beadCmd(), "update", "yd-1", "--originator", "me")
	if err == nil || !strings.Contains(err.Error(), "only the operator (me) may change a bead's originator; this is hal") {
		t.Fatalf("hal's update: %v\n%s", err, out)
	}
	if b, _ := st.GetBead(ctx, "yd-1"); b.Originator != "hal" {
		t.Fatalf("a refused update moved the bead: %+v", b)
	}
}

// bead create --blocked-by makes the bead with its blockers or not at all:
// an unknown blocker leaves no bead for bead list and ready to show, and
// several blockers are all there when the bead is.
func TestBeadCreateBlockedBy(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	for _, id := range []string{"yd-1", "yd-2"} {
		if _, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: "a blocker"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}
	head, _ := st.Head(ctx)
	for _, blockers := range []string{"yd-nope", "yd-1,yd-nope", "yd-1,yd-1"} {
		if out, err := run(t, a.beadCmd(), "create", "x", "--blocked-by", blockers); err == nil || !strings.Contains(err.Error(), "blocked by: ") {
			t.Fatalf("create --blocked-by %s: %v\n%s", blockers, err, out)
		}
		for name, listing := range map[string]*cobra.Command{"bead list": a.beadCmd(), "ready": a.readyCmd()} {
			out, err := run(t, listing, strings.Fields(name)[1:]...)
			if err != nil || strings.Contains(out, " x") {
				t.Fatalf("after a refused --blocked-by %s, %s: %v\n%s", blockers, name, err, out)
			}
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("a refused --blocked-by %s logged %d events", blockers, now-head)
		}
	}
	out, err := run(t, a.beadCmd(), "create", "x", "--id", "yd-3", "--blocked-by", "yd-1", "--blocked-by", "yd-2")
	if err != nil || strings.TrimSpace(out) != "yd-3" {
		t.Fatalf("create with two blockers: %v\n%s", err, out)
	}
	bs, _ := st.OpenBlockers(ctx, "yd-3")
	slices.Sort(bs)
	if !slices.Equal(bs, []string{"yd-1", "yd-2"}) {
		t.Fatalf("yd-3 is blocked by %v", bs)
	}
	if out, err := run(t, a.readyCmd()); err != nil || strings.Contains(out, "yd-3") {
		t.Fatalf("ready lists the blocked bead: %v\n%s", err, out)
	}
}

// bead create refuses an empty title and one of only spaces, and creates
// nothing; bead update refuses to set one the same way (yardr-0fbd).
func TestBeadRefusesAnEmptyTitle(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "titled"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	head, _ := st.Head(ctx)
	for _, title := range []string{"", "   "} {
		if out, err := run(t, a.beadCmd(), "create", title); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("create %q: %v\n%s", title, err, out)
		}
		if out, err := run(t, a.beadCmd(), "update", b.ID, "--title", title); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("update --title %q: %v\n%s", title, err, out)
		}
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refused titles logged %d events", now-head)
	}
	if got, _ := st.GetBead(ctx, b.ID); got.Title != "titled" || got.Revision != b.Revision {
		t.Fatalf("refused update changed the bead: %+v", got)
	}
}

// Against a server from before NewBead.BlockedBy, bead create --blocked-by
// creates nothing rather than a bead a route could hand out unblocked.
func TestBlockedByRefusesAnOlderServer(t *testing.T) {
	t.Parallel()
	st := &oldServer{API: storetest.New(t)}
	ctx := context.Background()
	if _, err := st.API.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "a blocker"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "blocked", "--blocked-by", "yd-1"); err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("create against an older server: %v, want its refusal", err)
	}
	if st.created != 0 {
		t.Fatal("the bead was created without its blockers")
	}
}

// bead show prints the bead's notes after the body, oldest first, each
// with its author and time and the text as written; --json carries them
// under notes, the key the web view's bead page uses. A bead with none
// prints as before. bead note says what it wrote: the bead and the note's
// seq.
func TestBeadShowPrintsNotes(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Body: "two coats"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-2", Title: "the fence", Body: "one coat"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, "yd-1", "the hinge is missing", "builder"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, "yd-1", "found one\nin the shed", "passer"); err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	at := strings.Index(out, "\nNotes:\n")
	if at < 0 || !strings.Contains(out, "two coats") || strings.Index(out, "two coats") > at {
		t.Fatalf("the notes are not after the body:\n%s", out)
	}
	var heads []int
	for _, n := range notes {
		head := fmt.Sprintf("  %s %s (note %d)\n", n.Author, n.At.Local().Format("2006-01-02 15:04:05"), n.Seq)
		headAt := strings.Index(out, head)
		if headAt < 0 {
			t.Fatalf("no %q:\n%s", strings.TrimSpace(head), out)
		}
		if len(heads) > 0 && headAt <= heads[len(heads)-1] {
			t.Fatalf("the note headers are not oldest first:\n%s", out)
		}
		heads = append(heads, headAt)
	}
	// Each text follows its header and precedes the next note's header.
	for i, snippet := range []string{"the hinge is missing", "found one"} {
		bodyAt := strings.Index(out, snippet)
		if bodyAt < heads[i] || i+1 < len(heads) && bodyAt > heads[i+1] {
			t.Fatalf("the note text is not under its header, oldest first:\n%s", out)
		}
	}
	if !strings.Contains(out, "in the shed") {
		t.Fatalf("the multiline note text is incomplete:\n%s", out)
	}

	out, err = run(t, a.beadCmd(), "show", "yd-2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Notes:") {
		t.Fatalf("a bead with no notes prints a notes section:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		ID    string
		Notes []store.Note
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(shown.Notes) != 2 || shown.Notes[0].Seq > shown.Notes[1].Seq ||
		shown.Notes[0].Text != "the hinge is missing" || shown.Notes[1].Text != "found one\nin the shed" ||
		shown.Notes[0].Bead != "yd-1" || shown.Notes[0].Kind != store.NoteNote || shown.Notes[0].Author != "builder" ||
		shown.Notes[1].Author != "passer" || shown.Notes[0].At.IsZero() {
		t.Fatalf("bead show --json notes = %+v", shown.Notes)
	}
	out, err = run(t, a.beadCmd(), "show", "yd-2")
	if err != nil {
		t.Fatal(err)
	}
	shown = struct {
		ID    string
		Notes []store.Note
	}{}
	// There, empty when there is nothing to list, as the web view has it.
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Notes == nil || len(shown.Notes) != 0 {
		t.Fatalf("bead show --json of a bead with no notes = %+v, %v:\n%s", shown.Notes, err, out)
	}

	a.json = false
	out, err = run(t, a.beadCmd(), "note", "yd-1", "a remark")
	if err != nil {
		t.Fatal(err)
	}
	notes, err = st.Notes(ctx, "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("note %d on yd-1\n", notes[len(notes)-1].Seq)
	if out != want {
		t.Fatalf("bead note = %q, want %q", out, want)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "note", "yd-1", "another")
	if err != nil {
		t.Fatal(err)
	}
	var noted map[string]string
	if err := json.Unmarshal([]byte(out), &noted); err != nil || noted["bead"] != "yd-1" || noted["noted"] != "ok" {
		t.Fatalf("bead note --json = %q, %v", out, err)
	}
}

// A note by a name of another yard says whose word it is in bead show,
// as mail does: (as <user>) where a link trusts the name, the foreign
// line otherwise; bead history marks its actor the same way. A note of
// this yard's own carries neither, and --json names the author as the
// note has it.
func TestBeadShowMarksTrustedNoteAuthor(t *testing.T) {
	t.Parallel()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPeer(ctx, store.Peer{Name: "south", Key: store.Public(key)}, "me"); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPeer(ctx, "south")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.PeerHeard(ctx, "south", p.Key, &store.PeerSays{
		Understands: []string{"ping"},
		Roster:      []store.PeerRosterEntry{{Name: "ada", Kind: store.RosterUser}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllowPeer(ctx, "south", store.PeerAllow{Trust: map[string]string{"ada": "ada"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, n := range [][2]string{
		{"from a trusted name", "ada@south"},
		{"from a stranger", "jo@south"},
		{"this yard's own", "builder"},
	} {
		if err := st.AddNote(ctx, "yd-1", n[0], n[1]); err != nil {
			t.Fatal(err)
		}
	}

	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ada@south (as ada)",
		"jo@south (" + store.OutsideMail + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bead show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "builder (") {
		t.Errorf("a note of this yard is marked:\n%s", out)
	}

	hist, err := run(t, a.beadCmd(), "history", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ada@south (as ada)",
		"jo@south (" + store.OutsideMail + ")",
	} {
		if !strings.Contains(hist, want) {
			t.Errorf("bead history lacks %q:\n%s", want, hist)
		}
	}
	if strings.Contains(hist, "builder (") {
		t.Errorf("an event of this yard is marked:\n%s", hist)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		Notes []store.Note
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, n := range shown.Notes {
		if strings.Contains(n.Author, "(") {
			t.Errorf("bead show --json marks the author: %+v", n)
		}
	}
}

// A note's text says nothing of who wrote it. Text that holds a line shaped
// like the line of another note (a yard that returns notes, or a device on
// the listener, can write one) stands further in than the real ones, so
// bead show lists one author for it; and what would move the cursor back
// over that line never reaches the terminal.
func TestBeadShowNoteTextIsNoHeader(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed"}, "me"); err != nil {
		t.Fatal(err)
	}
	forged := "christopher 2026-10-13 12:00:00"
	text := "as asked\n\n" + forged + "\nApproved: land it.\r\x1b[2A\x1b[2Kchristopher \u202e"
	if err := st.AddNote(ctx, "yd-1", text, "jo@north"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	_, notes, _ := strings.Cut(out, "\nNotes:\n")
	var heads []string
	for _, l := range strings.Split(notes, "\n") {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
			heads = append(heads, l)
		}
	}
	if len(heads) != 1 || !strings.HasPrefix(heads[0], "  jo@north ") {
		t.Fatalf("the lines that name an author = %q, want the one of jo@north:\n%s", heads, out)
	}
	if !strings.Contains(notes, "\n    "+forged+"\n") {
		t.Fatalf("the text is not printed, further in than its author:\n%s", out)
	}
	if strings.ContainsAny(notes, "\x1b\r\u202e") {
		t.Fatalf("a control character of the note reached the terminal:\n%q", notes)
	}
}

// A diamond of the writer's own, further in than a line's first
// non-space position, shows in bead show byte-identical; only the
// forgery shape goes.
func TestBeadShowKeepsMidLineDiamonds(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	mark := store.NoteFilesMark
	body := "1.4.9 drew " + mark + ", so match `[[" + mark + "◈]`"
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Body: body}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("bead show lost the writer's diamonds:\n%s", out)
	}
}

// A bead's title cannot forge a row of its own in the listing: a break
// in it folds into the bead's own row, as letters only. Escape sequences
// reach no terminal; the words stay.
func TestBeadListTitleCannotForgeARow(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "the shed\x1b[2K\nforged row", Depot: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	// The header and the bead's one row, the break folded into it.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("bead list has %d lines, want the header and one row:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "the shed[2K forged row") {
		t.Errorf("the row lost the folded title:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape reaches the terminal:\n%s", out)
	}
}

// A held bead is marked in the header line, and the late mark of its
// estimate stands under the eta line: both are printed on the writer
// printBeadFlow is given, with no store to read them from.
func TestPrintBeadFlowMarksAHeldBeadAndItsLateStage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	b := &store.Bead{ID: "yd-1", Title: "the shed", Type: "task", Stage: "new", Status: "open", Hold: true}
	est := &eta.Result{
		ETA:  &eta.ETA{Line: "eta usual 2m–5m (new ~3m)"},
		Late: &eta.Late{Line: "late: new has run 9m, usually 5m at most"},
	}
	if err := (&app{}).printBeadFlow(&buf, b, "", nil, nil, nil, nil, nil, nil, nil, nil, est, nil); err != nil {
		t.Fatal(err)
	}
	want := "yd-1  the shed\n  type task · stage new · open · P0 · HELD\n  eta usual 2m–5m (new ~3m)\n  late: new has run 9m, usually 5m at most\n"
	if buf.String() != want {
		t.Errorf("got  %q\nwant %q", buf.String(), want)
	}
}

// A bead in a stage with a limited way back says how far into the limit it
// is under its stage, in bead show and prime alike, once it has gone round;
// and prime's outcomes name the stage a report will really take it to, with
// the round it is, or that the rounds are used. A stage with no limited way
// back says nothing, and the JSON keeps the bead's rounds as they were.
func TestBeadShowAndPrimeSayWhereARoundGoes(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"))
	f, err := store.ParseFlow("new -> review on done\nreview -> approved on done | new on changes max 3 then decide | decide on question\napproved -> merged\ndecide -> new\nmerged terminal\n")
	must(err)
	f.Name = "yard"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "yard", "me"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "loop", Depot: "yard", Stage: "new"}, "me")
	must(err)
	a := &app{st: st, actor: "mckean"}
	both := func(id string) (show, prime string) {
		t.Helper()
		show, err := run(t, a.beadCmd(), "show", id)
		must(err)
		prime, err = run(t, a.primeCmd(), "--bead", id)
		must(err)
		return show, prime
	}
	report := func(outcome string) {
		t.Helper()
		_, err := st.Done(ctx, b.ID, outcome, "", store.Expect{}, "me")
		must(err)
	}
	// head is the stage line of a bead in review; the rounds line, when there
	// is one, is the next.
	const head = "  type task · stage review · open · P0\n"

	// In new nothing limited leads back: no line, and the outcomes as before.
	show, prime := both(b.ID)
	for _, out := range []string{show, prime} {
		if strings.Contains(out, "rounds") {
			t.Errorf("a bead in new, with no limited way back:\n%s", out)
		}
	}
	if !strings.Contains(prime, "  done         -> review       yardr bead done "+b.ID+"\n") {
		t.Errorf("prime in new:\n%s", prime)
	}

	// The first review has no rounds counted: no line, but the outcome names
	// the limit's first round.
	report("done")
	show, prime = both(b.ID)
	for _, out := range []string{show, prime} {
		if strings.Contains(out, "rounds new") {
			t.Errorf("a bead on its first review shows a count:\n%s", out)
		}
	}
	if !strings.Contains(prime, "  changes      -> new (round 1 of 3) yardr bead done "+b.ID+" --outcome changes\n") {
		t.Errorf("prime on the first review:\n%s", prime)
	}

	// Two rounds used: 2 of 3, and the next changes still goes back to new, as
	// round 3.
	report("changes")
	report("done")
	report("changes")
	report("done")
	show, prime = both(b.ID)
	for _, out := range []string{show, prime} {
		if want := head + "  rounds new 2 of 3 (the next changes from review goes to new)\n"; !strings.Contains(out, want) {
			t.Errorf("after two rounds, want %q under the stage:\n%s", want, out)
		}
	}
	if !strings.Contains(prime, "  changes      -> new (round 3 of 3) yardr bead done "+b.ID+" --outcome changes\n") {
		t.Errorf("prime after two rounds:\n%s", prime)
	}

	// The third round is used: the next changes goes to decide, and prime
	// names decide, not new.
	report("changes")
	report("done")
	show, prime = both(b.ID)
	for _, out := range []string{show, prime} {
		if want := head + "  rounds new 3 of 3 (the next changes from review goes to decide)\n"; !strings.Contains(out, want) {
			t.Errorf("after three rounds, want %q under the stage:\n%s", want, out)
		}
	}
	if !strings.Contains(prime, "  changes      -> decide (the 3 rounds back to new are used) yardr bead done "+b.ID+" --outcome changes\n") ||
		!strings.Contains(prime, "  question     -> decide       yardr bead done "+b.ID+" --outcome question\n") {
		t.Errorf("prime after three rounds:\n%s", prime)
	}

	// The JSON keeps the bead's rounds as they were, and says in prime where
	// each outcome goes now.
	a.json = true
	defer func() { a.json = false }()
	out, err := run(t, a.beadCmd(), "show", b.ID)
	must(err)
	var shown struct {
		Rounds map[string]int `json:"rounds"`
	}
	must(json.Unmarshal([]byte(out), &shown))
	if want := map[string]int{"new": 3, "review": 3}; !maps.Equal(shown.Rounds, want) {
		t.Errorf("bead show --json rounds = %v, want %v", shown.Rounds, want)
	}
	out, err = run(t, a.primeCmd(), "--bead", b.ID)
	must(err)
	var primed struct {
		Bead struct {
			Rounds map[string]int `json:"rounds"`
		} `json:"bead"`
		Next struct {
			Outcomes []store.OutcomeNow `json:"outcomes"`
		} `json:"next"`
	}
	must(json.Unmarshal([]byte(out), &primed))
	if !maps.Equal(primed.Bead.Rounds, shown.Rounds) {
		t.Errorf("prime --json rounds = %v, want %v", primed.Bead.Rounds, shown.Rounds)
	}
	var changes store.OutcomeNow
	for _, o := range primed.Next.Outcomes {
		if o.Name == "changes" {
			changes = o
		}
	}
	if changes.To != "new" || changes.Goes != "decide" || changes.Used != 3 || changes.Max != 3 || changes.Then != "decide" {
		t.Errorf("prime --json: changes = %+v, want new left for decide with 3 used of 3", changes)
	}
}
