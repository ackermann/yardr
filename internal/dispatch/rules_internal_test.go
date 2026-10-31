package dispatch

import (
	"testing"

	"yardr.dev/yardr/internal/store"
)

func TestUnacceptedPullRequestHeadUsesLegacyVisited(t *testing.T) {
	const accepted = "9f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	const pushed = "c0ffee5a1e0f4b2d8a97163e5d4c3b2a1f0e9d8c"
	h := &store.Hook{Says: []store.Say{{
		Where: map[string]string{
			"event": "pull_request", "action": "synchronize", "label": "accepted",
			"stage": "review", "type": "pr",
		},
		Outcome: outcomeUnaccepted,
	}}}
	ev := ruleEvent{
		typ: "pull_request", action: "synchronize", sha: pushed, tellsHead: true,
		data: map[string]any{
			"type": "pull_request", "action": "synchronize",
			"fields": map[string]any{"head_sha": []string{pushed}},
		},
	}
	bead := &store.Bead{
		Type: "pr", Stage: "review", Visited: []string{"triage", "review"},
		Metadata: map[string]any{metaAcceptedHead: accepted},
	}
	line := unacceptedPullRequestHead(h, bead, ev)
	if line == nil || line.Outcome != outcomeUnaccepted {
		t.Fatalf("line = %+v, want the unaccepted outcome for a legacy bead that visited triage", line)
	}
}
