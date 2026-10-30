package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

const githubCarryDecl = `name: github
base: https://api.github.com
credential: { env: GITHUB_TOKEN }
git: { credential: [github/git-credential] }
link:
  host: github.com
  issue: '^/([A-Za-z0-9](?:-?[A-Za-z0-9])*)/([A-Za-z0-9._-]+)/issues/([1-9][0-9]*)(?:/.*)?$'
  fixed: "https://github.com/{1}/{2}/issues/{3}"
  thread: "{1}/{2}#{3}"
calls:
  viewer:
    kind: read
    request: { method: GET, path: /user }
`

const githubStandinToken = "stand-in-token-9c1e"

func githubCarryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCarryDepotPushGitConfigReadDistinguishesMissingKey(t *testing.T) {
	repo := t.TempDir()
	githubCarryGit(t, repo, "init", "-q")
	if values, ok := gitConfigGetAll(repo, "credential.https://github.com.helper", true); !ok || len(values) != 0 {
		t.Fatalf("missing helper = %q, %v, want none and a readable config", values, ok)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("not a git config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitConfigGetAll(repo, "credential.https://github.com.helper", true); ok {
		t.Fatal("a malformed git config read as an empty helper list")
	}
}

// githubCarryYard is a carry yard on an edge whose depot web has an
// https github.com origin, an assembly group, a second depot, and the
// github token standing in the yard's file for the source.
func githubCarryYard(t *testing.T) (*store.Store, string, string, string) {
	t.Helper()
	// No helper of the machine's own git config answers in the yard.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	// Nor one git builds in beside the system file (Apple's git brings
	// a credential.helper of its own).
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	st, home, bead := carryYard(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := store.Edge{URL: "https://my.yardr.dev", Key: store.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := st.SetSetting(ctx, store.EdgeSetting, edge.String(), "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "github.yaml", githubCarryDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sources", "github.env"), []byte("GITHUB_TOKEN="+githubStandinToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	githubCarryGit(t, repo, "init", "-q")
	githubCarryGit(t, repo, "remote", "add", "origin", "https://github.com/ada/web.git")
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "YARDR_LAND=squash YARDR_NOTIFY=boss /srv/merge-local --fast"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	githubCarryGit(t, other, "init", "-q")
	githubCarryGit(t, other, "remote", "add", "origin", "https://github.com/ada/other.git")
	if err := st.PutDepot(ctx, store.Depot{Name: "other", Path: other, Base: "main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "other-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/srv/other-land"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	return st, home, bead, repo
}

func githubAsk(depot string, off bool) store.Ask {
	q := "Turn push on?"
	if off {
		q = "Turn push off?"
	}
	return store.Ask{Question: q, To: "mckean", Context: []string{"the depot is linked"},
		DepotPush: &store.AskDepotPush{Depot: depot, Off: off}}
}

func approveGithubAsk(t *testing.T, st *store.Store, bead string, ask store.Ask) uint64 {
	t.Helper()
	ctx := context.Background()
	seq, err := st.AddAsk(ctx, bead, ask, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveAsk(ctx, bead, seq, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	return seq
}

func carryOne(t *testing.T, st *store.Store, home string) {
	t.Helper()
	c := testCarrier(st, home, "boot-one")
	if did, err := c.carryNext(context.Background()); err != nil || !did {
		t.Fatalf("carryNext = %v, %v", did, err)
	}
}

// Approving and carrying out turns push on: both halves, this depot
// alone, the rest of the cmd and YARDR_LAND standing, and the token
// nowhere in the cmd, the config, the card, or the record.
func TestCarryDepotPushOnAndOff(t *testing.T) {
	ctx := context.Background()
	st, home, bead, repo := githubCarryYard(t)
	seq := approveGithubAsk(t, st, bead, githubAsk("web", false))
	carryOne(t, st, home)
	g, err := st.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if want := "YARDR_PUSH=origin YARDR_LAND=squash YARDR_NOTIFY=boss /srv/merge-local --fast"; g.Config["cmd"] != want {
		t.Fatalf("the cmd = %q, want %q", g.Config["cmd"], want)
	}
	helper := filepath.Join(home, "sources", "github", "github", "git-credential")
	if got := githubCarryGit(t, repo, "config", "--local", "--get", "credential.https://github.com.helper"); got != helper {
		t.Fatalf("the helper = %q, want %q", got, helper)
	}
	other, err := st.GetGroup(ctx, "other-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if other.Config["cmd"] != "/srv/other-land" {
		t.Fatalf("another depot's group changed: %q", other.Config["cmd"])
	}
	d, err := st.GetDepot(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitConfig(d.Path, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("another depot's git config gained a helper")
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	// A second drain writes neither half again: nothing pends.
	if did, err := testCarrier(st, home, "boot-one").carryNext(ctx); err != nil || did {
		t.Fatalf("a second drain carried out %v, %v", did, err)
	}
	// The token is in none of the places a person or a log reads.
	configBytes, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"cmd": g.Config["cmd"].(string), "git config": string(configBytes),
		"card": resolvedLines(t, st, bead, seq), "result": carryState(t, st, bead, seq).Result,
	} {
		if strings.Contains(text, githubStandinToken) {
			t.Errorf("the token stands in the %s", name)
		}
	}

	// The off card is the exact reverse.
	if err := os.Remove(filepath.Join(home, "sources", "github.env")); err != nil {
		t.Fatal(err)
	}
	seq2 := approveGithubAsk(t, st, bead, githubAsk("web", true))
	carryOne(t, st, home)
	g, err = st.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if want := "YARDR_LAND=squash YARDR_NOTIFY=boss /srv/merge-local --fast"; g.Config["cmd"] != want {
		t.Fatalf("after off the cmd = %q, want %q", g.Config["cmd"], want)
	}
	if _, err := gitConfig(repo, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("the helper stands after off")
	}
	if rec := carryState(t, st, bead, seq2); rec.State != store.CarryDone {
		t.Fatalf("the off record = %+v", rec)
	}
	// And off again is refused at filing: push is not on.
	if _, err := st.AddAsk(ctx, bead, githubAsk("web", true), "yardmaster"); err == nil || !strings.Contains(err.Error(), "not on") {
		t.Fatalf("filing off when not on = %v", err)
	}
}

// A half-done depot is completed by filing the same card again: each
// half already standing is written no second time.
func TestCarryDepotPushCompletesAHalfDoneDepot(t *testing.T) {
	ctx := context.Background()
	// Cmd half standing, git half missing.
	st, home, bead, repo := githubCarryYard(t)
	if _, err := st.SetGroup(ctx, "web-assembly", store.GroupEdit{Set: map[string]any{"cmd": "YARDR_PUSH=origin YARDR_LAND=squash YARDR_NOTIFY=boss /srv/merge-local --fast"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := approveGithubAsk(t, st, bead, githubAsk("web", false))
	carryOne(t, st, home)
	g, _ := st.GetGroup(ctx, "web-assembly")
	if strings.Count(g.Config["cmd"].(string), "YARDR_PUSH=") != 1 {
		t.Fatalf("the cmd half was written twice: %q", g.Config["cmd"])
	}
	if got := githubCarryGit(t, repo, "config", "--local", "--get", "credential.https://github.com.helper"); got != filepath.Join(home, "sources", "github", "github", "git-credential") {
		t.Fatalf("the git half was not completed: %q", got)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}

	// Git half standing (box-wide), cmd half missing: the carry verifies
	// the helper that answers and writes no local line.
	st2, home2, bead2, repo2 := githubCarryYard(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	githubCarryGit(t, repo2, "config", "--global", "credential.https://github.com.helper", filepath.Join(home2, "sources", "github", "github", "git-credential"))
	approveGithubAsk(t, st2, bead2, githubAsk("web", false))
	carryOne(t, st2, home2)
	if _, err := gitConfig(repo2, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("a local helper was written where a box-wide pack helper already answers")
	}
	g2, _ := st2.GetGroup(ctx, "web-assembly")
	if !strings.HasPrefix(g2.Config["cmd"].(string), "YARDR_PUSH=origin ") {
		t.Fatalf("the cmd half was not completed: %q", g2.Config["cmd"])
	}
}

// A carry that fails after approval is recorded failed with words
// that send nobody to a terminal: filing the card again is the way on.
func TestCarryDepotPushFailureNamesNoTerminal(t *testing.T) {
	ctx := context.Background()
	st, home, bead, _ := githubCarryYard(t)
	seq := approveGithubAsk(t, st, bead, githubAsk("web", false))
	// A conflicting YARDR_PUSH lands between approval and carrying out.
	if _, err := st.SetGroup(ctx, "web-assembly", store.GroupEdit{Set: map[string]any{"cmd": "YARDR_PUSH=upstream /srv/merge-local"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	carryOne(t, st, home)
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryFailed {
		t.Fatal("the lookalike-helper carry was not refused")
	}
	lines := resolvedLines(t, st, bead, seq)
	if !strings.Contains(lines, "File the card again") {
		t.Fatal("the failed card does not say to file it again")
	}
	if strings.Contains(lines, "terminal") {
		t.Fatal("the failed card names a terminal")
	}
	// The token may go away after approval. A carry checks it before
	// changing either half, so a retry cannot leave a cmd that will push
	// without credentials.
	st2, home2, bead2, repo2 := githubCarryYard(t)
	seq2 := approveGithubAsk(t, st2, bead2, githubAsk("web", false))
	if err := os.WriteFile(filepath.Join(home2, "sources", "github.env"), []byte("# no token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	carryOne(t, st2, home2)
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryFailed {
		t.Fatalf("the missing-token record = %+v, want failed", rec)
	}
	group, err := st2.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(group.Config["cmd"].(string), "YARDR_PUSH=") {
		t.Fatalf("a missing token left the cmd half on: %q", group.Config["cmd"])
	}
	if _, err := gitConfig(repo2, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("a missing token wrote the helper half")
	}
	if result := carryState(t, st2, bead2, seq2).Result; !strings.Contains(result, "File the card again") || strings.Contains(result, githubStandinToken) {
		t.Fatal("the missing-token failure is missing the retry instruction or discloses a token")
	}

	// The approval also holds the source it showed. If the declaration
	// changes before the server takes the carry, it cannot switch helpers.
	st3, home3, bead3, repo3 := githubCarryYard(t)
	seq3 := approveGithubAsk(t, st3, bead3, githubAsk("web", false))
	changed := strings.Replace(githubCarryDecl, "github/git-credential", "github/other-helper", 1)
	if _, err := st3.PutSource(ctx, "github.yaml", changed, "mckean"); err != nil {
		t.Fatal(err)
	}
	carryOne(t, st3, home3)
	if rec := carryState(t, st3, bead3, seq3); rec.State != store.CarryFailed {
		t.Fatalf("the changed-source record = %+v, want failed", rec)
	}
	group, err = st3.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(group.Config["cmd"].(string), "YARDR_PUSH=") {
		t.Fatalf("a changed source left the cmd half on: %q", group.Config["cmd"])
	}
	if _, err := gitConfig(repo3, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("a changed source wrote the helper half")
	}
	// The source can keep its declaration while its copied helper changes.
	// The card's approval holds those bytes too, and the carry rechecks them.
	st4, home4, bead4, repo4 := githubCarryYard(t)
	helperCopy := filepath.Join(home4, "sources", "github", "github", "git-credential")
	if err := os.MkdirAll(filepath.Dir(helperCopy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperCopy, []byte("#!/bin/sh\nprintf original\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	seq4 := approveGithubAsk(t, st4, bead4, githubAsk("web", false))
	if err := os.WriteFile(helperCopy, []byte("#!/bin/sh\nprintf changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	carryOne(t, st4, home4)
	if rec := carryState(t, st4, bead4, seq4); rec.State != store.CarryFailed {
		t.Fatalf("the changed-helper record = %+v, want failed", rec)
	}
	group, err = st4.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(group.Config["cmd"].(string), "YARDR_PUSH=") {
		t.Fatalf("a changed helper left the cmd half on: %q", group.Config["cmd"])
	}
	if _, err := gitConfig(repo4, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("a changed helper wrote the config half")
	}
	_ = ctx
}

// A box-wide helper that only ends like the pack's copy, planted after
// approval, fails the carry: it is not verified as the pack's, nothing
// is written over it, and the failure repeats nothing of its value.
func TestCarryDepotPushRefusesALookalikeHelper(t *testing.T) {
	st, home, bead, repo := githubCarryYard(t)
	seq := approveGithubAsk(t, st, bead, githubAsk("web", false))
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	lookalike := "!sh -c 'echo password=stand-in-token-7f3a' " + filepath.Join(t.TempDir(), "sources", "github", "github", "git-credential")
	githubCarryGit(t, repo, "config", "--global", "credential.https://github.com.helper", lookalike)
	carryOne(t, st, home)
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryFailed {
		t.Fatalf("the record = %+v, want failed", rec)
	}
	if _, err := gitConfig(repo, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("a local helper was written beside a helper that is not the pack's")
	}
	lines := resolvedLines(t, st, bead, seq)
	if !strings.Contains(lines, "not the pack's helper") {
		t.Fatal("the failed card does not name the helper conflict")
	}
	if strings.Contains(lines, "stand-in-token") || strings.Contains(lines, "sh -c") {
		t.Fatal("the failed card repeats the helper's value")
	}
	st2, home2, bead2, repo2 := githubCarryYard(t)
	seq2 := approveGithubAsk(t, st2, bead2, githubAsk("web", false))
	global2 := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global2)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	bad := "!sh -c 'echo credential=stand-in-token-8d2a'"
	githubCarryGit(t, repo2, "config", "--global", "credential.https://github.com.helper", bad)
	packHelper := filepath.Join(home2, "sources", "github", "github", "git-credential")
	githubCarryGit(t, repo2, "config", "--local", "credential.https://github.com.helper", packHelper)
	carryOne(t, st2, home2)
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryFailed {
		t.Fatal("the multiple-helper carry was not refused")
	}
	group, err := st2.GetGroup(context.Background(), "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(group.Config["cmd"].(string), "YARDR_PUSH=") {
		t.Fatalf("multiple helpers left the cmd half on: %q", group.Config["cmd"])
	}
	if got := githubCarryGit(t, repo2, "config", "--local", "--get", "credential.https://github.com.helper"); got != packHelper {
		t.Fatalf("multiple helpers changed the pre-existing local helper: %q", got)
	}
	lines = resolvedLines(t, st2, bead2, seq2)
	if strings.Contains(lines, "stand-in-token") || strings.Contains(lines, "sh -c") {
		t.Fatal("the multiple-helper failure repeats a helper value")
	}
}

// A bare credential.helper an agent writes into the depot's shared
// config after approval fails the carry: git would run it on the push
// and hand it the token, so nothing is written beside it.
func TestCarryDepotPushRefusesABareHelper(t *testing.T) {
	st, home, bead, repo := githubCarryYard(t)
	seq := approveGithubAsk(t, st, bead, githubAsk("web", false))
	githubCarryGit(t, repo, "config", "credential.helper", "!sh -c 'cat >> /tmp/stolen'")
	carryOne(t, st, home)
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryFailed {
		t.Fatalf("the record = %+v, want failed", rec)
	}
	if _, err := gitConfig(repo, "--local", "--get", "credential.https://github.com.helper"); err == nil {
		t.Fatal("the pack's helper was written beside a bare helper")
	}
	if lines := resolvedLines(t, st, bead, seq); strings.Contains(lines, "stolen") || strings.Contains(lines, "sh -c") {
		t.Fatal("the failed card repeats the helper's value")
	}
}
