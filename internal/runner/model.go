package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Model is the model the session's agent itself reports, "" while it has
// not answered yet or when its kind offers no source. An agent may run
// another model than the --model it was started with (an outdated agent
// that does not know the name falls back to one it does), and only the
// agent can say which.
//
// The source is the agent's own session file, found through the session id
// herdr reports for the pane (agent get: agent_session), not the pane's
// status line, which shows a display name in a layout that changes:
//
//	claude  ~/.claude/projects/<dir>/<id>.jsonl: the model of the first
//	        assistant message, which is what the API answered with
//	codex   ~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<id>.jsonl: the
//	        model of the first turn_context
//
// Other kinds have none yardr knows, and report nothing.
func (h *Herdr) Model(ctx context.Context, handle map[string]any) (string, error) {
	name, _ := handle["agent"].(string)
	if name == "" {
		return "", nil
	}
	raw, err := h.run(ctx, "agent", "get", name)
	if err != nil {
		var herr *herdrError
		if errors.As(err, &herr) && herr.Code == "agent_not_found" {
			return "", nil
		}
		return "", err
	}
	var info struct {
		Agent struct {
			Kind    string `json:"agent"`
			Session struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	id := info.Agent.Session.Value
	// The id goes into a glob: one that is not a plain id finds nothing.
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return "", nil
	}
	switch info.Agent.Kind {
	case "claude":
		return firstModel(filepath.Join(h.agentHome("CLAUDE_CONFIG_DIR", h.ClaudeDir, ".claude"), "projects", "*", id+".jsonl"),
			func(line []byte) string {
				var e struct {
					Type    string `json:"type"`
					Message struct {
						Model string `json:"model"`
					} `json:"message"`
				}
				// "<synthetic>" is Claude Code's own message (an API error
				// shown as a reply), not a model's.
				if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || strings.HasPrefix(e.Message.Model, "<") {
					return ""
				}
				return e.Message.Model
			})
	case "codex":
		return firstModel(filepath.Join(h.agentHome("CODEX_HOME", h.CodexDir, ".codex"), "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl"),
			func(line []byte) string {
				var e struct {
					Type    string `json:"type"`
					Payload struct {
						Model string `json:"model"`
					} `json:"payload"`
				}
				if json.Unmarshal(line, &e) != nil || e.Type != "turn_context" {
					return ""
				}
				return e.Payload.Model
			})
	}
	return "", nil
}

// agentHome is an agent's own directory: set (tests), else the agent's
// environment variable for it, else dir in the user's home.
func (h *Herdr) agentHome(env, set, dir string) string {
	if set != "" {
		return set
	}
	if p := os.Getenv(env); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, dir)
}

// firstModel reads the session file matching pattern line by line and
// returns the first model pick finds; "" when there is no file yet or no
// line names one.
func firstModel(pattern string, pick func(line []byte) string) (string, error) {
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return "", err
	}
	f, err := os.Open(files[0])
	if err != nil {
		return "", err
	}
	defer f.Close()
	// A line holds a whole message, tool output included: no fixed buffer.
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if m := pick(line); m != "" {
			return m, nil
		}
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
	}
}
