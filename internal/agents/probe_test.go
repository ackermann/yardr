package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgent puts a script named name first on PATH.
func fakeAgent(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A probe is one one-shot request: the agent's exit says whether it took
// the model, and its last lines say why not. The fakes answer as claude
// 2.1 and codex did for a model that does not exist.
func TestProbeModel(t *testing.T) {
	fakeAgent(t, "claude", `[ "$1 $2" = "-p --model" ] || { echo "usage" >&2; exit 2; }
if [ "$3" = claude-nope-9 ]; then
  echo "There's an issue with the selected model ($3). It may not exist or you may not have access to it."
  exit 1
fi
echo ok`)
	fakeAgent(t, "codex", `[ "$1 $2" = "exec -m" ] || { echo "usage" >&2; exit 2; }
if [ "$3" = gpt-7-luna ]; then
  echo "warning: Model metadata for $3 not found. Defaulting to fallback metadata" >&2
  echo >&2
  echo "ERROR 400 The '$3' model is not supported when using Codex with a ChatGPT account" >&2
  exit 1
fi
echo ok`)
	ctx := context.Background()

	if p := ProbeModel(ctx, "claude", "opus"); !p.Checked || !p.OK || p.Cmd != "claude -p --model opus" {
		t.Fatalf("accepted model: %+v", p)
	}
	p := ProbeModel(ctx, "claude", "claude-nope-9")
	if !p.Checked || p.OK || !strings.Contains(p.Detail, "issue with the selected model (claude-nope-9)") {
		t.Fatalf("refused model: %+v", p)
	}
	p = ProbeModel(ctx, "codex", "gpt-7-luna")
	if !p.Checked || p.OK || !strings.HasSuffix(p.Detail, "model is not supported when using Codex with a ChatGPT account") ||
		strings.Contains(p.Detail, "\n\n") {
		t.Fatalf("refused codex model: %+v", p)
	}
	if p := ProbeModel(ctx, "codex", "gpt-6-luna"); !p.OK {
		t.Fatalf("accepted codex model: %+v", p)
	}
	// A kind with no one-shot mode is not probed at all.
	if p := ProbeModel(ctx, "pi", "x"); p != (Probe{}) {
		t.Fatalf("pi: %+v", p)
	}
}

// An agent that is not installed where the probe runs says nothing about
// the model.
func TestProbeModelWithoutTheAgent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p := ProbeModel(context.Background(), "claude", "opus")
	if p.Checked || p.OK || p.Detail == "" || p.Cmd == "" {
		t.Fatalf("missing agent: %+v", p)
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a  \n\n \nb\nc\t\n\n", 2); got != "b\nc" {
		t.Fatalf("tail = %q", got)
	}
}
