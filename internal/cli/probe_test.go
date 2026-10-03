package cli

import (
	"context"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeProbe replaces the agent probe with one that refuses the models in
// reject, and returns the "kind model" pairs asked.
func fakeProbe(t *testing.T, reject ...string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	old := probeModel
	probeModel = func(_ context.Context, kind, model string) agents.Probe {
		mu.Lock()
		asked = append(asked, kind+" "+model)
		mu.Unlock()
		p := agents.Probe{Cmd: kind + " -p --model " + model, Checked: true, OK: true}
		for _, r := range reject {
			if r == kind+" "+model {
				p.OK, p.Detail = false, "There's an issue with the selected model ("+model+")."
			}
		}
		return p
	}
	t.Cleanup(func() { probeModel = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// group add with a model its agent refuses warns and still stores the
// group; --no-probe asks nothing; a model the agent accepts says nothing.
func TestGroupAddWarnsOfARefusedModel(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	asked := fakeProbe(t, "claude claude-nope-9")
	errs := captureStderr(t)

	out, err := run(t, (&app{st: st}).groupCmd(), "add", "builders", "--runner", "herdr",
		"--set", "kind=claude", "--set", "model=claude-nope-9")
	if err != nil {
		t.Fatal(err)
	}
	if w := errs.String(); !strings.Contains(w, "warning: group builders: claude did not answer with model \"claude-nope-9\"") ||
		!strings.Contains(w, "There's an issue with the selected model (claude-nope-9).") {
		t.Fatalf("stderr = %q", w)
	}
	if !strings.Contains(out, "group builders") {
		t.Fatalf("stdout = %q", out)
	}
	g, err := st.GetGroup(ctx, "builders")
	if err != nil || g.Config["model"] != "claude-nope-9" {
		t.Fatalf("group not stored as given: %+v, %v", g, err)
	}
	if got := asked(); len(got) != 1 || got[0] != "claude claude-nope-9" {
		t.Fatalf("probes = %v", got)
	}

	// --no-probe: stored, nothing asked, nothing said.
	errs.Reset()
	if _, err := run(t, (&app{st: st}).groupCmd(), "add", "reviewers", "--runner", "herdr",
		"--set", "kind=claude", "--set", "model=claude-nope-9", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	if g, err := st.GetGroup(ctx, "reviewers"); err != nil || g.Config["model"] != "claude-nope-9" {
		t.Fatalf("group not stored: %+v, %v", g, err)
	}
	if got := asked(); len(got) != 1 || errs.Len() != 0 {
		t.Fatalf("--no-probe probed or warned: probes = %v, stderr = %q", got, errs.String())
	}

	// An accepted model, a group without one and a runner that has no
	// model: no warning; only the first is asked about.
	for _, args := range [][]string{
		{"add", "a", "--runner", "herdr", "--set", "kind=codex", "--set", "model=gpt-6-luna"},
		{"add", "b", "--runner", "herdr", "--set", "kind=claude"},
		{"add", "c", "--runner", "exec", "--set", "cmd=true", "--set", "model=claude-nope-9"},
	} {
		if _, err := run(t, (&app{st: st}).groupCmd(), args...); err != nil {
			t.Fatal(err)
		}
	}
	if got := asked(); len(got) != 2 || got[1] != "codex gpt-6-luna" || errs.Len() != 0 {
		t.Fatalf("probes = %v, stderr = %q", got, errs.String())
	}
}

// resident add probes its model the same way.
func TestResidentAddWarnsOfARefusedModel(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	asked := fakeProbe(t, "codex gpt-7-luna")
	errs := captureStderr(t)
	dir := t.TempDir()

	if _, err := run(t, (&app{st: st}).residentCmd(), "add", "mayor", "--dir", dir,
		"--set", "kind=codex", "--set", "model=gpt-7-luna"); err != nil {
		t.Fatal(err)
	}
	if w := errs.String(); !strings.Contains(w, "warning: resident mayor: codex did not answer with model \"gpt-7-luna\"") {
		t.Fatalf("stderr = %q", w)
	}
	rs, err := st.Residents(ctx)
	if err != nil || len(rs) != 1 || rs[0].Config["model"] != "gpt-7-luna" {
		t.Fatalf("resident not stored as given: %+v, %v", rs, err)
	}
	errs.Reset()
	if _, err := run(t, (&app{st: st}).residentCmd(), "add", "mayor", "--dir", dir,
		"--set", "kind=codex", "--set", "model=gpt-7-luna", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	if got := asked(); len(got) != 1 || errs.Len() != 0 {
		t.Fatalf("--no-probe probed or warned: probes = %v, stderr = %q", got, errs.String())
	}
}

// bead create --meta model= asks every agent kind that may work the bead:
// the model replaces each herdr group's own, so one kind's model is
// another's refusal.
func TestBeadCreateWarnsOfARefusedModel(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, g := range []store.Group{
		{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "refinery", Runner: "exec", Config: map[string]any{"cmd": "true"}},
		{Name: "elsewhere", Runner: "herdr", Config: map[string]any{"kind": "grok"}},
	} {
		if err := st.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	other := "other"
	if err := st.PutRig(ctx, store.Rig{Name: other}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Route{
		{Stage: "new", Group: "builders"}, {Stage: "review", Group: "reviewers"},
		{Stage: "approved", Group: "refinery"}, {Stage: "new", Group: "elsewhere", Rig: &other},
	} {
		if _, err := st.AddRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	asked := fakeProbe(t, "codex opus")
	errs := captureStderr(t)

	out, err := run(t, (&app{st: st}).beadCmd(), "create", "x", "--meta", "model=opus")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(out)
	if b, err := st.GetBead(ctx, id); err != nil || b.Metadata["model"] != "opus" {
		t.Fatalf("bead not created as given: %+v, %v", b, err)
	}
	if w := errs.String(); !strings.Contains(w, "warning: bead "+id+" (worked by reviewers): codex did not answer with model \"opus\"") ||
		strings.Contains(w, "builders") {
		t.Fatalf("stderr = %q", w)
	}
	got := asked()
	if len(got) != 2 || !strings.Contains(strings.Join(got, ","), "claude opus") || !strings.Contains(strings.Join(got, ","), "codex opus") {
		t.Fatalf("probes = %v, want claude and codex (not another rig's grok)", got)
	}

	errs.Reset()
	if _, err := run(t, (&app{st: st}).beadCmd(), "create", "y", "--meta", "model=opus", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, (&app{st: st}).beadCmd(), "create", "z"); err != nil {
		t.Fatal(err)
	}
	if got := asked(); len(got) != 2 || errs.Len() != 0 {
		t.Fatalf("probed without a model or with --no-probe: probes = %v, stderr = %q", got, errs.String())
	}
}

// A probe that could not run (the agent is not installed here) says so
// rather than calling the model refused; a kind with no one-shot mode says
// nothing.
func TestProbeWarningWording(t *testing.T) {
	w := probeWarning("group g", "claude", "opus", agents.Probe{Cmd: "claude -p --model opus", Detail: "exec: \"claude\": executable file not found in $PATH"})
	if !strings.Contains(w, "could not check model \"opus\" with claude") || strings.Contains(w, "did not answer") {
		t.Fatalf("warning = %q", w)
	}
	if w := probeWarning("group g", "pi", "x", agents.Probe{}); w != "" {
		t.Fatalf("warning for a kind without a probe = %q", w)
	}
}
