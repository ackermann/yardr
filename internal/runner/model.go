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

	"yardr.dev/yardr/internal/agents"
)

// Model is the model the session's agent itself reports, "" while it has
// not answered yet or when its kind offers no source. An agent may run
// another model than the --model it was started with (an outdated agent
// that does not know the name falls back to one it does), and only the
// agent can say which.
//
// The source is the agent's own session file, found through the session id
// herdr reports for the pane (agent get: agent_session), not the pane's
// status line, which shows a display name in a layout that changes. Where
// the file is and which line names the model is in the kind's harness file
// (`model`, agents.ModelSource):
//
//	claude  ~/.claude/projects/<dir>/<id>.jsonl: the model of the first
//	        assistant message, which is what the API answered with
//	codex   ~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<id>.jsonl: the
//	        model of the first turn_context
//	muse    ~/.local/share/muse/sessions/<y>/<m>/<d>/<id>/session.jsonl:
//	        the model_id of the first run.model.configured line, which is
//	        what the run was started or reconfigured with
//
// A kind whose file names no source reports nothing, as does one herdr
// reports no session id for (muse, until herdr learns its sessions).
func (h *Herdr) Model(ctx context.Context, handle map[string]any) (string, error) {
	kind, id, err := h.sessionKind(ctx, handle)
	if err != nil || id == "" {
		return "", err
	}
	if kind.Model == nil {
		return "", nil
	}
	return firstModel(kind.Model.Pattern(h.agentHome(kind), id), kind.Model.ModelIn)
}

// sessionKind is the harness kind of the agent handle names and its
// session id, by which the agent's own session file is found. id is ""
// when herdr names no agent or no session id for it, or one that is no
// plain id (it goes into a glob, where one finds nothing).
func (h *Herdr) sessionKind(ctx context.Context, handle map[string]any) (agents.Harness, string, error) {
	name, _ := handle["agent"].(string)
	if name == "" {
		return agents.Harness{}, "", nil
	}
	raw, err := h.run(ctx, "agent", "get", name)
	if err != nil {
		var herr *herdrError
		if errors.As(err, &herr) && herr.Code == "agent_not_found" {
			return agents.Harness{}, "", nil
		}
		return agents.Harness{}, "", err
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
		return agents.Harness{}, "", err
	}
	id := info.Agent.Session.Value
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return agents.Harness{}, "", nil
	}
	kind, _ := agents.Lookup(info.Agent.Kind)
	return kind, id, nil
}

// agentHome is the own directory of an agent of kind, whose file names a
// model source: set (tests), else the one the agent's environment variable
// names, else its directory in the user's home.
func (h *Herdr) agentHome(kind agents.Harness) string {
	if set := h.AgentHomes[kind.Kind]; set != "" {
		return set
	}
	if p := os.Getenv(kind.Model.HomeEnv); kind.Model.HomeEnv != "" && p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, kind.Model.Home)
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
