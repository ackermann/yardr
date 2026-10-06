package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

var _ dispatch.CrewCloser = (*Herdr)(nil)

// CloseLeft closes the herdr workspace a crew member's session that is gone
// ran in (see dispatch.CrewCloser): the harness exited, its pane's
// shell did not, and herdr keeps the workspace until someone closes it.
//
// The workspace is the one yardr recorded in the session's handle, never
// one found by its label, and it is closed only while it still is what
// yardr opened: it carries the crew member's label and holds the handle's
// pane. An id that names something else by now is left alone and
// forgotten. A workspace with an agent in any pane is kept: someone
// started one there by hand, and closing it would end their session.
func (h *Herdr) CloseLeft(ctx context.Context, r store.CrewMember, handle map[string]any) (bool, error) {
	ws, _ := handle["workspace"].(string)
	pane, _ := handle["pane"].(string)
	if ws == "" || pane == "" {
		return false, nil
	}
	raw, err := h.run(ctx, "workspace", "get", ws)
	var herr *herdrError
	if errors.As(err, &herr) && herr.Code == "workspace_not_found" {
		return false, nil // closed by hand
	}
	if err != nil {
		return false, err
	}
	var info struct {
		Workspace struct {
			Label string `json:"label"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return false, fmt.Errorf("workspace get: %w", err)
	}
	if info.Workspace.Label != CrewLabel(h.yardName(ctx), r.Name) {
		return false, nil
	}
	if raw, err = h.run(ctx, "pane", "list"); err != nil {
		return false, err
	}
	var pl struct {
		Panes []struct {
			ID        string `json:"pane_id"`
			Workspace string `json:"workspace_id"`
			Agent     string `json:"agent"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(raw, &pl); err != nil {
		return false, fmt.Errorf("pane list: %w", err)
	}
	ours, live := false, false
	for _, p := range pl.Panes {
		if p.Workspace != ws {
			continue
		}
		ours = ours || p.ID == pane
		live = live || p.Agent != ""
	}
	if !ours {
		return false, nil
	}
	if live {
		return true, nil
	}
	// herdr's agent list is asked too: a pane's agent field is the harness
	// herdr detected in it, and an agent started under a name is listed
	// there whether or not it was.
	if raw, err = h.run(ctx, "agent", "list"); err != nil {
		return false, err
	}
	var al struct {
		Agents []struct {
			Workspace string `json:"workspace_id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &al); err != nil {
		return false, fmt.Errorf("agent list: %w", err)
	}
	for _, a := range al.Agents {
		if a.Workspace == ws {
			return true, nil
		}
	}
	_, err = h.run(ctx, "workspace", "close", ws)
	if errors.As(err, &herr) && herr.Code == "workspace_not_found" {
		return false, nil
	}
	return false, err
}
