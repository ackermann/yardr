package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Context reads the last use the agent's session file holds, found by the
// session id herdr reports. The lines are trimmed from a real transcript
// of Claude Code 2.1.
func TestHerdrContextReadsTheLastUse(t *testing.T) {
	ctx := context.Background()
	claude := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(claude, "projects", "-tmp-w", "c1.jsonl"), `{"type":"user","message":{"role":"user","content":"Read the brief."}}
{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":5,"output_tokens":7}}}
{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}]}}
{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":200,"output_tokens":3}}}
`)
	write(filepath.Join(claude, "projects", "-tmp-w", "c2.jsonl"), `{"type":"user","message":{"role":"user","content":"Read the brief."}}
`)
	h := &Herdr{AgentHomes: map[string]string{"claude": claude}, Bin: fakeHerdrBin(t, `agent() { echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"'"$1"'","agent_session":{"kind":"id","value":"'"$2"'"},"agent_status":"idle"}}}'; }
case "$3" in
  used)     agent claude c1 ;;
  silent)   agent claude c2 ;;
  nofile)   agent claude c3 ;;
  glob)     agent claude '*' ;;
  kimi)     agent kimi session_1 ;;
  starting) echo '{"id":"x","result":{"type":"agent_info","agent":{"agent":"claude","agent_status":"idle"}}}' ;;
  gone)     echo '{"error":{"code":"agent_not_found","message":"agent target gone not found"},"id":"x"}' >&2; exit 1 ;;
  *)        echo '{"error":{"code":"server_not_running","message":"no herdr server"},"id":"x"}' >&2; exit 1 ;;
esac`)}
	for agent, want := range map[string]struct {
		used int
		ok   bool
	}{
		"used":     {202, true}, // the last line's input and cache, not what it said
		"silent":   {},          // prompted, no reply yet
		"nofile":   {},          // no session file yet
		"glob":     {},          // an id that is no id finds nothing
		"kimi":     {},          // a kind with no source
		"starting": {},          // herdr has no session id for it yet
		"gone":     {},
	} {
		used, ok, err := h.Context(ctx, map[string]any{"agent": agent})
		if err != nil || used != want.used || ok != want.ok {
			t.Errorf("%s: context = %d, %v, %v; want %d, %v", agent, used, ok, err, want.used, want.ok)
		}
	}
	if _, _, err := h.Context(ctx, map[string]any{"agent": "down"}); err == nil {
		t.Error("herdr down must be an error, not a reading")
	}
}
