package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Model reads the model from the agent's own session file, found by the
// session id herdr reports, or, when herdr names no id and the kind
// declares a workspace, by the session's worktree. The lines are trimmed
// from real files of Claude Code 2.1, Codex and Muse Code 1.4.9-1.4.12.
func TestHerdrModelReadsTheAgentsSessionFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	claude, codex, muse := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A reply Claude Code made up for an API error names no model.
	write(filepath.Join(claude, "projects", "-tmp-w", "c1.jsonl"), `{"type":"user","message":{"role":"user","content":"Read the brief."}}
{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}]}}
{"type":"assistant","message":{"model":"claude-fable-5-1","role":"assistant","content":[]}}
{"type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant","content":[]}}
`)
	write(filepath.Join(claude, "projects", "-tmp-w", "c2.jsonl"), `{"type":"user","message":{"role":"user","content":"Read the brief."}}
`)
	write(filepath.Join(codex, "sessions", "2026", "10", "03", "rollout-2026-10-03T12-40-08-x1.jsonl"),
		`{"type":"session_meta","payload":{"base_instructions":{"provenance":{"model":"other"}}}}
{"type":"turn_context","payload":{"model":"gpt-6-luna","collaboration_mode":{"settings":{"model":"gpt-6-luna"}}}}
`)
	// The startup metadata names the startup model; the configured line
	// names the running one.
	write(filepath.Join(muse, "sessions", "2026", "10", "11", "m1", "session.jsonl"),
		`{"payload_type":"runtime.session.metadata","payload":{"record":{"model_id":"muse-spark-1.3"}}}
{"payload_type":"run.model.configured","payload":{"record":{"model_id":"muse-spark-1.3-contributor","source":"startup"}}}
`)
	// Two sessions in one worktree, as yardr-58xw's were: each file starts
	// seconds after its session's claim. Two more files of the same
	// worktree fall outside the first session's window, and two of another
	// worktree share one session's window. Every file opens with the
	// retained_frame wrapper, whose nested time is not the file's start.
	wtA, wtQ := "/work/wt-a", "/work/wt-q"
	claimA := time.Date(2026, 10, 21, 18, 46, 13, 0, time.UTC)
	claimB := time.Date(2026, 10, 21, 19, 36, 32, 0, time.UTC)
	claimQ := time.Date(2026, 10, 21, 18, 45, 0, 0, time.UTC)
	museFile := func(id, wt, model string, start time.Time) {
		t.Helper()
		us := start.UnixMicro()
		write(filepath.Join(muse, "sessions", "2026", "10", "21", id, "session.jsonl"),
			fmt.Sprintf("{\"retained_frame\":\"session_permission_transaction\",\"frame_schema_version\":1,\"outer_log_ordinal\":1,\"transaction_id\":\"t1\",\"children\":[{\"child_index\":0,\"record_json\":\"{\\\"sequence\\\":1,\\\"recorded_at\\\":%d}\"}],\"content_sha256\":\"x\"}\n", us-56000)+
				fmt.Sprintf("{\"sequence\":3,\"recorded_at\":%d,\"payload_type\":\"runtime.session.metadata\",\"payload\":{\"record\":{\"workspace_root\":%q,\"model_id\":\"muse-spark-1.3\"}}}\n", us, wt)+
				fmt.Sprintf("{\"sequence\":9,\"recorded_at\":%d,\"payload_type\":\"run.model.configured\",\"payload\":{\"record\":{\"model_id\":%q}}}\n", us+1000000, model))
	}
	museFile("ma", wtA, "muse-spark-1.3-contributor", claimA.Add(4*time.Second))
	museFile("mb", wtA, "muse-wonder-1.0", claimB.Add(3*time.Second))
	museFile("me", wtA, "muse-spark-1.3-contributor", claimA.Add(-time.Second))
	museFile("ml", wtA, "muse-spark-1.3-contributor", claimA.Add(15*time.Minute+4*time.Second))
	museFile("mq1", wtQ, "muse-spark-1.3-contributor", time.Date(2026, 10, 21, 18, 50, 0, 0, time.UTC))
	museFile("mq2", wtQ, "muse-wonder-1.0", time.Date(2026, 10, 21, 18, 55, 0, 0, time.UTC))
	h := &Herdr{AgentHomes: map[string]string{"claude": claude, "codex": codex, "muse": muse}, Bin: fakeHerdrBin(t, `agent() { echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"'"$1"'","agent_session":{"kind":"id","value":"'"$2"'"},"agent_status":"idle"}}}'; }
case "$3" in
  answered) agent claude c1 ;;
  silent)   agent claude c2 ;;
  nofile)   agent claude c3 ;;
  glob)     agent claude '*' ;;
  codex)    agent codex x1 ;;
  muse)     agent muse m1 ;;
  museprec) agent muse m1 ;;
  musenoid) echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"muse","agent_status":"working"}}}' ;;
  kimi)     agent kimi session_1 ;;
  starting) echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"claude","agent_status":"idle"}}}' ;;
  gone)     echo '{"error":{"code":"agent_not_found","message":"agent target gone not found"},"id":"x"}' >&2; exit 1 ;;
  *)        echo '{"error":{"code":"server_not_running","message":"no herdr server"},"id":"x"}' >&2; exit 1 ;;
esac`)}
	// SessionID is the id alone: the agent that has not answered yet, and the
	// kind that names no model, have theirs, and an id that is no id is none.
	// A Muse session herdr names no id for is matched by its worktree.
	for _, c := range []struct {
		name      string
		handle    map[string]any
		startedAt time.Time
		model, id string
	}{
		{name: "answered", handle: map[string]any{"agent": "answered"}, model: "claude-fable-5-1", id: "c1"}, // the first model that answered
		{name: "silent", handle: map[string]any{"agent": "silent"}, id: "c2"},                                // prompted, no reply yet
		{name: "nofile", handle: map[string]any{"agent": "nofile"}, id: "c3"},                                // no session file yet
		{name: "glob", handle: map[string]any{"agent": "glob"}},                                              // an id that is no id finds nothing
		{name: "codex", handle: map[string]any{"agent": "codex"}, model: "gpt-6-luna", id: "x1"},
		{name: "muse", handle: map[string]any{"agent": "muse"}, model: "muse-spark-1.3-contributor", id: "m1"},
		{name: "kimi", handle: map[string]any{"agent": "kimi"}, id: "session_1"}, // a kind with no source
		{name: "starting", handle: map[string]any{"agent": "starting"}},          // herdr has no session id for it yet
		{name: "gone", handle: map[string]any{"agent": "gone"}},
		// Herdr's id takes precedence over the match, even an ambiguous one.
		{name: "herdr id over an ambiguous match", handle: map[string]any{"agent": "museprec", "kind": "muse", "worktree": wtQ}, startedAt: claimQ, model: "muse-spark-1.3-contributor", id: "m1"},
		// Herdr names the kind and no id: the match goes by herdr's kind,
		// whatever the handle's says.
		{name: "matched by worktree", handle: map[string]any{"agent": "musenoid", "kind": "muse", "worktree": wtA}, startedAt: claimA, model: "muse-spark-1.3-contributor", id: "ma"},
		{name: "herdr kind over the handle kind", handle: map[string]any{"agent": "musenoid", "kind": "claude", "worktree": wtA}, startedAt: claimA, model: "muse-spark-1.3-contributor", id: "ma"},
		// Herdr names no agent at all: the match goes by the handle's kind.
		{name: "matched with no agent on the handle", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: claimA, model: "muse-spark-1.3-contributor", id: "ma"},
		{name: "the second session's own file", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: claimB, model: "muse-wonder-1.0", id: "mb"},
		// Two qualifying files match nothing, never the earliest.
		{name: "ambiguous match", handle: map[string]any{"kind": "muse", "worktree": wtQ}, startedAt: claimQ},
		// A file that starts before the start or after the window is not matched.
		{name: "every file before the start", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)},
		{name: "every file after the window", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: time.Date(2026, 10, 21, 18, 0, 0, 0, time.UTC)},
		// The window holds its edges: a file that starts at the start, or at
		// the window's end, counts, and here each makes two.
		{name: "a file at the start counts", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: claimA.Add(-time.Second)},
		{name: "a file at the window's end counts", handle: map[string]any{"kind": "muse", "worktree": wtA}, startedAt: claimA.Add(4 * time.Second)},
		// No worktree, or a kind with no workspace, matches nothing.
		{name: "no worktree", handle: map[string]any{"kind": "muse"}, startedAt: claimA},
		{name: "a kind with no workspace", handle: map[string]any{"kind": "codex", "worktree": wtA}, startedAt: claimA},
	} {
		got, err := h.Model(ctx, c.handle, c.startedAt)
		if err != nil || got != c.model {
			t.Errorf("%s: model = %q, %v; want %q", c.name, got, err, c.model)
		}
		id, err := h.SessionID(ctx, c.handle, c.startedAt)
		if err != nil || id != c.id {
			t.Errorf("%s: session id = %q, %v; want %q", c.name, id, err, c.id)
		}
	}
	if _, err := h.Model(ctx, map[string]any{"agent": "down"}, time.Time{}); err == nil {
		t.Error("herdr down must be an error, not a model")
	}
	if id, err := h.SessionID(ctx, map[string]any{"agent": "down"}, time.Time{}); err == nil {
		t.Errorf("herdr down must be an error, not the session id %q", id)
	}
}
