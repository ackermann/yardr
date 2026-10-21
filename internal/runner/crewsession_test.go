package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// A conversation session starts on its user's env file alone: the line's
// env is not inherited, and its file missing or open refuses the start as
// Blocked. Its agent and workspace carry the user, its pane the session,
// and its first prompt never greets.
func TestStartCrewConversation(t *testing.T) {
	t.Parallel()
	line := envFileT(t, 0o600)
	halPath := filepath.Join(t.TempDir(), "hal.env")
	if err := os.WriteFile(halPath, []byte("HAL_API_KEY=hal-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, calls, _ := envHerdr(t, "")
	r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: map[string]any{
		"env_file": line, "env_for_hal": halPath, "greet": "jo",
	}}
	handle, err := h.StartCrew(context.Background(), r, "hal")
	if err != nil {
		t.Fatal(err)
	}
	params := apiCall(t, readCalls(t, calls), "workspace.create")
	if params["label"] != "y yardmaster for hal" {
		t.Errorf("workspace label = %v, want %q", params["label"], "y yardmaster for hal")
	}
	env := envOf(params)
	if env["HAL_API_KEY"] != "hal-secret" {
		t.Errorf("pane env lacks hal's file entry: %v", env)
	}
	if _, ok := env["LINEAR_API_KEY"]; ok {
		t.Errorf("pane env inherits the line's file: %v", env)
	}
	if env["YARDR_CREW_FOR"] != "hal" {
		t.Errorf("pane env lacks the session: %v", env)
	}
	wantAgent := CrewConversationAgentName("y", "yardmaster", "hal")
	if handle["agent"] != wantAgent {
		t.Errorf("handle agent = %v, want %s", handle["agent"], wantAgent)
	}
	if handle["for"] != "hal" {
		t.Errorf("handle lacks for=hal: %v", handle)
	}
	prompt, _ := handle[firstPrompt].(string)
	for _, want := range []string{`talking with "hal"`, "hal's slice", "prime --crew yardmaster --for hal"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("first prompt lacks %q: %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "set this yard up") {
		t.Errorf("conversation session greets: %q", prompt)
	}

	// A file others can read, or none at all, refuses the start before
	// herdr is asked for anything, as Blocked.
	missing := filepath.Join(t.TempDir(), "nope.env")
	for name, path := range map[string]string{"0644": envFileT(t, 0o644), "missing": missing} {
		h, calls, _ := envHerdr(t, "")
		r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(),
			Config: map[string]any{"env_for_hal": path}}
		_, err := h.StartCrew(context.Background(), r, "hal")
		if err == nil || !dispatch.IsBlocked(err) {
			t.Errorf("%s: StartCrew = %v, want Blocked", name, err)
		} else if !strings.Contains(err.Error(), "crew member yardmaster for hal") {
			t.Errorf("%s: StartCrew error = %v, want the session named", name, err)
		}
		if c := readCalls(t, calls); strings.Contains(c, "workspace.create") {
			t.Errorf("%s: herdr was asked for a workspace:\n%s", name, c)
		}
	}
}

// LiveCrew for a conversation matches its own agent name, not the line
// session's: an agent under the line's name does not block it, and one
// under its own does not block the line.
func TestLiveCrewConversation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, _, agents := listHerdr(t)
	wantAgent := CrewConversationAgentName("y1", "yardmaster", "hal")
	setAgents(t, agents, wantAgent+" "+dir+" w3:p1")
	got, err := h.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: dir}, "hal")
	if err != nil || got == nil || got.Name != wantAgent {
		t.Fatalf("conversation: found %+v %v, want %s", got, err, wantAgent)
	}
	got, err = h.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: dir}, "")
	if err != nil || got != nil {
		t.Fatalf("line: found %+v %v, want none", got, err)
	}
	setAgents(t, agents, "y1-yardmaster "+dir+" w3:p1")
	got, err = h.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: dir}, "hal")
	if err != nil || got != nil {
		t.Fatalf("conversation with the line's agent up: found %+v %v, want none", got, err)
	}
}

// CloseLeft knows a conversation's workspace by its own label, from the
// handle: one still carrying it is closed, one carrying the line's is
// forgotten, never closed.
func TestCloseLeftConversation(t *testing.T) {
	t.Parallel()
	const shell = `{"pane_id":"w3:p1","workspace_id":"w3"}`
	handle := map[string]any{"agent": CrewConversationAgentName("y1", "yardmaster", "hal"), "workspace": "w3", "tab": "w3:t1", "pane": "w3:p1", "for": "hal"}
	h, calls := leftHerdr(t, "y1 yardmaster for hal", shell, "")
	kept, err := h.CloseLeft(context.Background(), store.CrewMember{Name: "yardmaster"}, handle)
	if err != nil || kept {
		t.Fatalf("CloseLeft own label = %v %v, want closed", kept, err)
	}
	if c := readCalls(t, calls); !strings.Contains(c, "workspace close w3\n") {
		t.Errorf("own label: workspace not closed:\n%s", c)
	}
	h, calls = leftHerdr(t, "y1 yardmaster", shell, "")
	kept, err = h.CloseLeft(context.Background(), store.CrewMember{Name: "yardmaster"}, handle)
	if err != nil || kept {
		t.Fatalf("CloseLeft line label = %v %v, want forgotten", kept, err)
	}
	if c := readCalls(t, calls); strings.Contains(c, "workspace close") {
		t.Errorf("line label: workspace closed, want left alone:\n%s", c)
	}
}
