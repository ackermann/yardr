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
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
)

// Model is the model the session's agent itself reports, "" while it has
// not answered yet or when its kind offers no source. An agent may run
// another model than the --model it was started with (an outdated agent
// that does not know the name falls back to one it does), and only the
// agent can say which.
//
// The source is the agent's own session file, found through the session id
// herdr reports for the pane (agent get: agent_session), or, when herdr
// names no id and the kind declares a workspace, through the session's
// worktree (matchWorkspace); not the pane's status line, which shows a
// display name in a layout that changes. Where the file is and which line
// names the model is in the kind's harness file (`model`,
// agents.ModelSource):
//
//	claude  ~/.claude/projects/<dir>/<id>.jsonl: the model of the first
//	        assistant message, which is what the API answered with
//	codex   ~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<id>.jsonl: the
//	        model of the first turn_context
//	muse    ~/.local/share/muse/sessions/<y>/<m>/<d>/<id>/session.jsonl:
//	        the model_id of the first run.model.configured line, which is
//	        what the run was started or reconfigured with
//
// A kind whose file names no source reports nothing, as does one with no
// session id and no worktree match for it.
func (h *Herdr) Model(ctx context.Context, handle map[string]any, startedAt time.Time) (string, error) {
	kind, id, err := h.sessionKind(ctx, handle)
	if err != nil {
		return "", err
	}
	if id == "" {
		kind, id, err = h.matchSession(kind, handle, startedAt)
		if err != nil || id == "" {
			return "", err
		}
	}
	if kind.Model == nil {
		return "", nil
	}
	return firstModel(kind.Model.Pattern(h.agentHome(kind), id), kind.Model.ModelIn)
}

// SessionID is the session id of the agent handle names, by which its own
// session file is found: what herdr reports for the pane, or, when herdr
// names no id and the kind declares a workspace, the worktree match's
// (matchWorkspace). "" while there is none: the agent has not started its
// session, is gone, or its file matches nothing yet. It is where Model and
// Context start, without their reading of the file, so an agent that has
// not answered yet, or whose kind names no model, has its id all the same.
func (h *Herdr) SessionID(ctx context.Context, handle map[string]any, startedAt time.Time) (string, error) {
	kind, id, err := h.sessionKind(ctx, handle)
	if err != nil || id != "" {
		return id, err
	}
	_, id, err = h.matchSession(kind, handle, startedAt)
	return id, err
}

// matchSession is the kind and session id found for handle when herdr names
// no id: kind is herdr's when it named an agent, else the handle's own, and
// id is the worktree match's, "" when no file qualifies or two or more do.
// An ambiguous match reads as no id here; the count says why (Usage).
func (h *Herdr) matchSession(kind agents.Harness, handle map[string]any, startedAt time.Time) (agents.Harness, string, error) {
	if kind.Kind == "" {
		name, _ := handle[harnessKind].(string)
		kind, _ = agents.Lookup(name)
	}
	worktree, _ := handle["worktree"].(string)
	id, _, err := h.matchWorkspace(kind, worktree, startedAt)
	return kind, id, err
}

// matchWorkspace is the id of the session file the worktree match finds for
// a session herdr reports no id for (a Muse pane): the one file whose
// workspace line names worktree and whose start, the first of its lines
// with a time that reads, was written at or after startedAt and within
// ModelWindow of it. ambiguous is true when two or more files qualify: an
// ambiguous match is never the earliest of them, so no model or count is
// read from a retry or a manual run. A kind that declares no workspace, or
// no worktree, matches nothing.
func (h *Herdr) matchWorkspace(kind agents.Harness, worktree string, startedAt time.Time) (id string, ambiguous bool, err error) {
	if kind.Model == nil || kind.Model.Workspace == nil || worktree == "" {
		return "", false, nil
	}
	dir := h.agentHome(kind)
	files, err := filepath.Glob(kind.Model.Pattern(dir, "*"))
	if err != nil {
		return "", false, err
	}
	end := startedAt.Add(dispatch.ModelWindow)
	var found string
	for _, path := range files {
		// The start first: most files are left after their first lines,
		// and only a file that started in the session's window is read
		// for its worktree.
		start, ok, err := fileStart(kind.Model, path)
		if err != nil {
			return "", false, err
		}
		if !ok || start.Before(startedAt) || start.After(end) {
			continue
		}
		names, err := fileNamesWorktree(kind.Model, path, worktree)
		if err != nil {
			return "", false, err
		}
		id, okID := kind.Model.SessionIn(dir, path)
		// A file that names no id it can be read by is no candidate: it
		// can be neither the match nor what makes one ambiguous.
		if !names || !okID || !plainSessionID(id) {
			continue
		}
		if found != "" {
			return "", true, nil
		}
		found = id
	}
	return found, false, nil
}

// fileStart is when the session file at path started: the time of its first
// line with one that reads. ok is false when no line has one.
func fileStart(src *agents.ModelSource, path string) (start time.Time, ok bool, err error) {
	err = eachLine(path, func(line []byte) bool {
		l, has := src.WorkspaceIn(line)
		if !has || l.At.IsZero() {
			return false
		}
		start = l.At
		return true
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return start, !start.IsZero(), nil
}

// fileNamesWorktree reports whether one of the session file's lines names
// worktree.
func fileNamesWorktree(src *agents.ModelSource, path, worktree string) (bool, error) {
	var names bool
	err := eachLine(path, func(line []byte) bool {
		l, has := src.WorkspaceIn(line)
		names = has && l.Workspace == worktree
		return names
	})
	return names, err
}

// sessionKind is the harness kind of the agent handle names and its
// session id, by which the agent's own session file is found. id is ""
// when herdr names no agent or no session id for it, or one that is no
// plain id (it goes into a glob, where one finds nothing); kind is still
// herdr's when it named an agent, so the worktree match knows what matched
// nothing.
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
	kind, _ := agents.Lookup(info.Agent.Kind)
	if id := info.Agent.Session.Value; plainSessionID(id) {
		return kind, id, nil
	}
	return kind, "", nil
}

// plainSessionID reports whether id can stand for a session in the glob of
// its file: one with a separator or a pattern character in it would find
// another session's file, or none.
func plainSessionID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\*?[`)
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

// sessionFile is the session file matching pattern; "" when there is none
// yet, which is no error.
func sessionFile(pattern string) (string, error) {
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return "", err
	}
	return files[0], nil
}

// eachLine calls each with the lines of the file at path in turn, the last
// one too when it has no newline yet, until each answers true.
func eachLine(path string, each func(line []byte) (stop bool)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// A line holds a whole message, tool output included: no fixed buffer.
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if each(line) || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// firstModel reads the session file matching pattern line by line and
// returns the first model pick finds; "" when there is no file yet or no
// line names one.
func firstModel(pattern string, pick func(line []byte) string) (model string, err error) {
	path, err := sessionFile(pattern)
	if path == "" || err != nil {
		return "", err
	}
	err = eachLine(path, func(line []byte) bool {
		model = pick(line)
		return model != ""
	})
	return model, err
}
