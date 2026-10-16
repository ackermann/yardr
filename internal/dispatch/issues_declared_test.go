package dispatch_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// acmeMove declares acme with the keys a stage map takes: thread.state and
// thread.states, and a link.
const acmeMove = `name: acme
base: https://acme.test
credential:
  env: ACME_TOKEN
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex }
    delivery: { header: Acme-Delivery }
  bodies: repeat
  normalize: [normalize]
thread:
  reply: [reply]
  posted: [posted]
  read: [read]
  place: team
  open: [open]
  opened: [opened]
  state: [state]
  states: [started, review, done]
link:
  host: acme.test
  issue: '^/issue/([A-Z]+-[0-9]+)'
  fixed: "https://acme.test/issue/{1}"
  thread: "{1}"
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// A hook's stage map moves a declared source's issue by running its
// thread.state with forward, with the crew member's credential for the one
// variable the file names.
func TestIssueMoveDeclaredSource(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	ctx := context.Background()
	home, declDir, dir := t.TempDir(), t.TempDir(), t.TempDir()
	scripts := map[string]string{
		"normalize": "#!/bin/sh\nprintf '%s' '{\"type\":\"ping\"}'\n",
		"reply":     "#!/bin/sh\nexit 1\n",
		"posted":    "#!/bin/sh\nprintf '%s' '{}'\n",
		"read":      "#!/bin/sh\nprintf '%s' '{\"messages\":[]}'\n",
		"open":      "#!/bin/sh\nexit 1\n",
		"opened":    "#!/bin/sh\nprintf '%s' '{}'\n",
		"state": "#!/bin/sh\n{ cat; echo; } >> " + dir + "/state.stdin\n" +
			"printf '%s' \"$ACME_TOKEN\" > " + dir + "/state.token\n" +
			"printf '%s' '{\"status\":\"In Progress\",\"moved\":true}'\n",
	}
	for name, body := range scripts {
		must(t, os.WriteFile(filepath.Join(declDir, name), []byte(body), 0o755))
	}
	d, err := source.Parse("acme.yaml", []byte(acmeMove))
	must(t, err)
	must(t, d.CopyCommandFiles(declDir, home))
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_yardmaster\nGITHUB_TOKEN=gh-yardmaster\nACME_TOKEN=tok-1\n"), 0o600))
	y.yardmaster(t, "linear,github,acme")
	y.d.Home = home
	_, err = y.api.PutSource(ctx, "acme.yaml", acmeMove, "me")
	must(t, err)
	must(t, y.api.SetIdentity(ctx, "yardmaster", "acme", store.Identity{ID: "u-me"}, "me"))
	_, err = y.api.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: "/s", As: "yardmaster",
		Map: map[string]string{"task:new": "started"}}, "me")
	must(t, err)
	_, err = y.api.CreateBead(ctx, store.NewBead{ID: "yard-9", Title: "nine", Depot: "yard", Stage: "backlog",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "acme",
			URL: "https://acme.test/issue/ACME-1", Thread: "ACME-1"}}}, "me")
	must(t, err)

	y.to(t, "yard-9", "new")
	raw, err := os.ReadFile(filepath.Join(dir, "state.stdin"))
	must(t, err)
	var in map[string]any
	must(t, json.Unmarshal([]byte(strings.TrimRight(string(raw), "\n")), &in))
	if in["thread"] != "ACME-1" || in["state"] != "started" || in["forward"] != true || len(in) != 3 {
		t.Fatalf("state stdin = %v", in)
	}
	token, err := os.ReadFile(filepath.Join(dir, "state.token"))
	must(t, err)
	if string(token) != "tok-1" {
		t.Fatalf("credential handed in = %q", token)
	}
	said := y.said(t, "yard-9")
	if len(said) != 1 || !strings.HasPrefix(said[0], "started: In Progress") {
		t.Fatalf("said = %v", said)
	}
}

// A bead tracking a Linear issue, its hook with a map and --as:
// advancing the bead moves the issue through the pack's own
// thread.state, with the crew member's credential, end to end against
// the stand-in.
func TestIssueMoveLinearDeclared(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	ctx := context.Background()
	// The bead tracks under linear by its URL alone.
	_, err := y.api.CreateBead(ctx, store.NewBead{ID: "yard-9", Title: "nine", Depot: "yard", Stage: "backlog",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: issueA}}}, "me")
	must(t, err)

	y.to(t, "yard-9", "new")
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "In Progress")
	want(t, "events", y.said(t, "yard-9"), []string{"started: In Progress (was Todo)"})
	want(t, "calls", y.src.Calls(), []string{"linear read YRD-1", "linear set YRD-1 In Progress"})
	// The daemon called the state command with the crew member's
	// credential: anything else the stand-in refuses, and nothing moves.
	y.src.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Backlog"})
	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_other\n"), 0o600))
	_, err = y.api.CreateBead(ctx, store.NewBead{ID: "yard-8", Title: "eight", Depot: "yard", Stage: "backlog",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: issueB}}}, "me")
	must(t, err)
	y.to(t, "yard-8", "new")
	if got := y.src.Status("linear", "YRD-2"); got != "Backlog" {
		t.Fatalf("the issue with another credential = %q", got)
	}
	if ms := y.pending(t); len(ms) != 1 || !strings.Contains(ms[0].Error, "Authentication required") {
		t.Fatalf("moves %+v", ms)
	}
}
