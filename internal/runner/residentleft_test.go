package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// leftHerdr is a herdr with one workspace, w3, as the files of dir describe
// it: label (missing: no such workspace), panes and agents (the lists'
// JSON). It logs what it was asked.
func leftHerdr(t *testing.T, label, panes, agents string) (*Herdr, string) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{"label": label, "panes": panes, "agents": agents} {
		if name == "label" && text == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls")
	return &Herdr{YardHome: "/tmp/y1", Bin: deafHerdrBin(t, `d=`+dir+`
printf '%s\n' "$*" >> $d/calls
case "$1 $2" in
  "workspace get")
    if [ "$3" != w3 ] || [ ! -f $d/label ]; then
      echo '{"error":{"code":"workspace_not_found","message":"workspace '$3' not found"}}' >&2; exit 1
    fi
    printf '{"result":{"workspace":{"workspace_id":"w3","label":"%s"}}}\n' "$(cat $d/label)" ;;
  "pane list") printf '{"result":{"panes":[%s]}}\n' "$(cat $d/panes)" ;;
  "agent list") printf '{"result":{"agents":[%s]}}\n' "$(cat $d/agents)" ;;
  "workspace close") echo '{"result":{}}' ;;
  *) echo '{"error":{"code":"unexpected","message":"'"$*"'"}}' >&2; exit 1 ;;
esac
`)}, calls
}

// Found live (yardr-ybxk): the workspace of a resident's session that was
// gone stayed open, one for every such session. CloseLeft closes it by the
// id yardr recorded, and only while it is still that workspace with no
// agent in it.
func TestCloseLeft(t *testing.T) {
	const shell = `{"pane_id":"w3:p1","workspace_id":"w3"}`
	handle := map[string]any{"agent": "y1-yardmaster", "workspace": "w3", "tab": "w3:t1", "pane": "w3:p1"}
	for name, tc := range map[string]struct {
		handle                map[string]any
		label, panes, agents  string
		wantKept, wantClosed  bool
		mustNotAsk, wantError string
	}{
		"the shell the harness left": {handle: handle, label: "y1 yardmaster", panes: shell, wantClosed: true},
		"closed by hand":             {handle: handle, panes: shell, mustNotAsk: "pane list"},
		"an agent in its pane": {handle: handle, label: "y1 yardmaster",
			panes: `{"pane_id":"w3:p1","workspace_id":"w3","agent":"claude"}`, wantKept: true},
		"an agent in a pane beside it": {handle: handle, label: "y1 yardmaster",
			panes: shell + `,{"pane_id":"w3:p2","workspace_id":"w3","agent":"codex"}`, wantKept: true},
		"a named agent herdr detects no harness for": {handle: handle, label: "y1 yardmaster", panes: shell,
			agents: `{"name":"mine","workspace_id":"w3"}`, wantKept: true},
		"an agent in another workspace": {handle: handle, label: "y1 yardmaster",
			panes:  shell + `,{"pane_id":"w4:p1","workspace_id":"w4","agent":"claude"}`,
			agents: `{"name":"y1-yardmaster","workspace_id":"w4"}`, wantClosed: true},
		"relabelled since":         {handle: handle, label: "scratch", panes: shell, mustNotAsk: "pane list"},
		"another yard's label":     {handle: handle, label: "y2 yardmaster", panes: shell},
		"the id names other panes": {handle: handle, label: "y1 yardmaster", panes: `{"pane_id":"w3:p7","workspace_id":"w3"}`},
		"a handle with no workspace": {handle: map[string]any{"agent": "y1-yardmaster"}, label: "y1 yardmaster", panes: shell,
			mustNotAsk: "workspace get"},
		"a handle with no pane": {handle: map[string]any{"workspace": "w3"}, label: "y1 yardmaster", panes: shell,
			mustNotAsk: "workspace get"},
	} {
		h, calls := leftHerdr(t, tc.label, tc.panes, tc.agents)
		kept, err := h.CloseLeft(context.Background(), store.Resident{Name: "yardmaster"}, tc.handle)
		if err != nil || kept != tc.wantKept {
			t.Errorf("%s: kept %v, %v; want kept %v", name, kept, err, tc.wantKept)
		}
		raw, _ := os.ReadFile(calls)
		asked := string(raw)
		if closed := strings.Contains(asked, "workspace close w3\n"); closed != tc.wantClosed {
			t.Errorf("%s: closed %v, want %v:\n%s", name, closed, tc.wantClosed, asked)
		}
		if strings.Count(asked, "close") > 1 || (tc.mustNotAsk != "" && strings.Contains(asked, tc.mustNotAsk)) {
			t.Errorf("%s: asked herdr more than it had to:\n%s", name, asked)
		}
	}
	// herdr down: an error, so the handle is kept, and nothing closed.
	down := &Herdr{Bin: deafHerdrBin(t, `echo '{"error":{"code":"server_not_running","message":"no herdr server"}}' >&2; exit 1`)}
	if kept, err := down.CloseLeft(context.Background(), store.Resident{Name: "yardmaster"}, handle); err == nil || kept {
		t.Errorf("herdr down: kept %v, %v; want an error", kept, err)
	}
}
