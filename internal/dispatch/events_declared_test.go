package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// A fixture source with scripted look-back reads: its pr and ci commands
// are scripts the test writes, which record what they were asked and
// answer fixed. The scripts use the shell's builtins alone, so they run
// with nothing on PATH, and gh is left off it: a read that asked gh
// would fail, and the train would not move.

// liveDecl declares forge with the envelope types that are CI runs and
// that tell the pull request's head, and the scripts that read the pull
// request and its CI back, each taking the file it records its calls in.
func liveDecl(pr, prCalls, ci, ciCalls string) string {
	return `name: forge
base: https://forge.test
credential:
  env: FORGE_TOKEN
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Forge-Signature, encoding: hex }
    delivery: { header: Forge-Delivery }
  bodies: repeat
  normalize: [normalize]
  keys: [event, number, state, merged, conclusion, head_sha, head_repo, train, sender]
link:
  host: forge.test
  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'
  fixed: "https://forge.test/{1}/{2}/issues/{3}"
  thread: "{1}/{2}#{3}"
  fold: lower
  pull:
    issue: '^/([^/]+)/([^/]+)/pull/([0-9]+)'
    fixed: "https://forge.test/{1}/{2}/pull/{3}"
    thread: "{1}/{2}#{3}"
  repos: '[^/]+/[^/]+'
events:
  runs: [build_finished]
  head_from: [merge_request]
  pr: ["` + pr + `", "` + prCalls + `"]
  ci: ["` + ci + `", "` + ciCalls + `"]
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
}

// liveScript writes a look-back script answering answer: it records its
// credential, the base and its stdin in the file its first argument
// names, with the shell's builtins alone.
func liveScript(t *testing.T, path, answer string) {
	t.Helper()
	body := "#!/bin/sh\n{\necho \"token=$FORGE_TOKEN base=$YARDR_SOURCE_BASE\"\nwhile IFS= read -r line || [ -n \"$line\" ]; do echo \"$line\"; done\n} >>\"$1\"\nprintf '%s\\n' '" + answer + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// liveForge gives the yard the forge source with scripted pr and ci
// commands, answering head for pull request 17, and a hook fg acting as
// the yardmaster, whose env_file holds FORGE_TOKEN. It returns the files
// the scripts record their calls in. gh is left off PATH.
func liveForge(t *testing.T, r *ghDepot, lines ...string) (prCalls, ciCalls string) {
	t.Helper()
	ctx := context.Background()
	bin := t.TempDir()
	pr, prCalls := filepath.Join(bin, "pr"), filepath.Join(bin, "pr.calls")
	ci, ciCalls := filepath.Join(bin, "ci"), filepath.Join(bin, "ci.calls")
	liveScript(t, pr, `{"head": "`+fixtureSHA+`", "base": "main", "state": "open"}`)
	liveScript(t, ci, `{"head": "`+fixtureSHA+`", "base": "main", "state": "open", "required": ["build"], "checks": [{"name": "build", "status": "completed", "conclusion": "success", "url": "https://forge.test/acme/yardr/runs/5"}]}`)
	if _, err := r.api.PutSource(ctx, "forge.yaml", liveDecl(pr, prCalls, ci, ciCalls), "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(bin, "yardmaster.env")
	if err := os.WriteFile(env, []byte("FORGE_TOKEN=forge-yardmaster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := r.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake",
		Config: map[string]any{"env_file": env}}, "me")
	must(t, err)
	must(t, r.api.SetIdentity(ctx, "yardmaster", "forge", store.Identity{ID: "u-yardmaster"}, "me"))
	_, err = r.api.PutHook(ctx, store.Hook{Name: "fg", Source: "forge",
		SecretFile: "/nonexistent/forge.secret", As: "yardmaster", Says: says(t, lines...)}, "me")
	must(t, err)
	t.Setenv("PATH", bin)
	return prCalls, ciCalls
}

// liveTrain adds a train bound to forge's pull request 17, as
// integrate-train binds one, with no head stored for it.
func liveTrain(t *testing.T, r *ghDepot, id, stage string) {
	t.Helper()
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: id, Title: "train " + id,
		Type: store.TypeTrain, Depot: "yard", Stage: stage,
		Metadata: map[string]any{"pr_url": "https://forge.test/acme/yardr/pull/17",
			"pr_number": "17", "pr_head_repo": "acme/yardr"}}, "me")
	must(t, err)
}

// scriptAsked is what the script recorded at calls: its credential, the base
// and the input it was read with.
func scriptAsked(t *testing.T, calls string) string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("the script recorded nothing at %s: %v", calls, err)
	}
	return string(b)
}

// A run moves a train whose head no event told: the head is read live
// through the declared pr command, with the hook's crew member's
// credential, and gh is not on PATH.
func TestForgeRunMovesTrainThroughScriptedPR(t *testing.T) {
	r := newGHDepot(t)
	r.d.CI, r.d.PRHead = nil, nil
	prCalls, ciCalls := liveForge(t, r,
		"event=build_finished action=completed conclusion=failure stage=review -> changes: CI failed on the head of the PR")
	liveTrain(t, r, "yard-t", "review")
	forgeDeliver(t, r, "build_finished", "completed", "https://forge.test/acme/yardr/pull/17",
		map[string][]string{"repo": {"acme/yardr"}, "number": {"17"}, "head_repo": {"acme/yardr"},
			"head_sha": {fixtureSHA}, "train": {"yard-t"}, "conclusion": {"failure"}})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "open" {
		t.Fatalf("yard-t in %s after a failed run on its pull request's head, want open", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "changes review -> open")
	for _, want := range []string{"token=forge-yardmaster", "base=https://forge.test",
		`"repo":"acme/yardr"`, `"number":"17"`, `"url":"https://forge.test/acme/yardr/pull/17"`} {
		if got := scriptAsked(t, prCalls); !strings.Contains(got, want) {
			t.Errorf("the pr command was asked %q, want %q in it", got, want)
		}
	}
	if _, err := os.Stat(ciCalls); !os.IsNotExist(err) {
		t.Errorf("the ci command was asked for a train in review: %v", err)
	}
}

// CI read as state moves a train in in-pr: the state comes from the
// declared ci command, with the hook's crew member's credential, and gh
// is not on PATH.
func TestForgeCIMovesTrainThroughScriptedCI(t *testing.T) {
	r := newGHDepot(t)
	r.d.CI, r.d.PRHead = nil, nil
	_, ciCalls := liveForge(t, r,
		"event=ci conclusion=success stage=in-pr -> done",
		"event=ci conclusion=failure stage=in-pr -> changes")
	liveTrain(t, r, "yard-t", "review")
	r.enterInPR(t, "yard-t", fixtureSHA)
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "land" {
		t.Fatalf("yard-t in %s with its CI green, want land", st)
	}
	for _, want := range []string{"token=forge-yardmaster", `"repo":"acme/yardr"`,
		`"number":"17"`, `"sha":"` + fixtureSHA + `"`} {
		if got := scriptAsked(t, ciCalls); !strings.Contains(got, want) {
			t.Errorf("the ci command was asked %q, want %q in it", got, want)
		}
	}
	if n := r.lastNote(t, "yard-t"); n != "CI green on "+fixtureSHA+": build (the checks main requires)\n(forge ci completed)" {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.ciRules(t, "yard-t"), "ci green: in-pr -> land")
}

// CI of a source that lost its ci command is read through nothing else:
// the read fails naming the command, and is tried again after the wait,
// with no event needed.
func TestForgeCIWithoutCommandFailsNamed(t *testing.T) {
	r := newGHDepot(t)
	r.d.CI, r.d.PRHead = nil, nil
	now := time.Now()
	dispatch.SetClock(r.d, func() time.Time { return now })
	liveForge(t, r,
		"event=ci conclusion=success stage=in-pr -> done",
		"event=ci conclusion=failure stage=in-pr -> changes")
	// The source re-added as B3 declared it: the lists, no commands.
	_, err := r.api.PutSource(context.Background(), "forge.yaml", forgeDecl, "me")
	must(t, err)
	liveTrain(t, r, "yard-t", "review")
	r.enterInPR(t, "yard-t", fixtureSHA)
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "in-pr" {
		t.Fatalf("yard-t in %s with its CI unread, want in-pr", st)
	}
	want := "ci error: CI on " + fixtureSHA + " is unknown: source forge declares no ci command (events.ci)"
	wantRules(t, r.ciRules(t, "yard-t"), want)
	r.pass(t)
	wantRules(t, r.ciRules(t, "yard-t"), want)
	now = now.Add(31 * time.Second)
	r.pass(t)
	wantRules(t, r.ciRules(t, "yard-t"), want, want)
}
