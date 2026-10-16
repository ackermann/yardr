package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Model reads the model from the agent's own session file, found by the
// session id herdr reports. The lines are trimmed from real files of
// Claude Code 2.1, Codex and Muse Code 1.4.9.
func TestHerdrModelReadsTheAgentsSessionFile(t *testing.T) {
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
	h := &Herdr{AgentHomes: map[string]string{"claude": claude, "codex": codex, "muse": muse}, Bin: fakeHerdrBin(t, `agent() { echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"'"$1"'","agent_session":{"kind":"id","value":"'"$2"'"},"agent_status":"idle"}}}'; }
case "$3" in
  answered) agent claude c1 ;;
  silent)   agent claude c2 ;;
  nofile)   agent claude c3 ;;
  glob)     agent claude '*' ;;
  codex)    agent codex x1 ;;
  muse)     agent muse m1 ;;
  kimi)     agent kimi session_1 ;;
  starting) echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"claude","agent_status":"idle"}}}' ;;
  gone)     echo '{"error":{"code":"agent_not_found","message":"agent target gone not found"},"id":"x"}' >&2; exit 1 ;;
  *)        echo '{"error":{"code":"server_not_running","message":"no herdr server"},"id":"x"}' >&2; exit 1 ;;
esac`)}
	for agent, want := range map[string]string{
		"answered": "claude-fable-5-1", // the first model that answered
		"silent":   "",                 // prompted, no reply yet
		"nofile":   "",                 // no session file yet
		"glob":     "",                 // an id that is no id finds nothing
		"codex":    "gpt-6-luna",
		"muse":     "muse-spark-1.3-contributor",
		"kimi":     "", // a kind with no source
		"starting": "", // herdr has no session id for it yet
		"gone":     "",
	} {
		got, err := h.Model(ctx, map[string]any{"agent": agent})
		if err != nil || got != want {
			t.Errorf("%s: model = %q, %v; want %q", agent, got, err, want)
		}
	}
	if _, err := h.Model(ctx, map[string]any{"agent": "down"}); err == nil {
		t.Error("herdr down must be an error, not a model")
	}
}
