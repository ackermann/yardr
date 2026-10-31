package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const depotPushDecl = `name: github
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

const depotHelperKey = "credential.https://github.com.helper"

const depotPushCmd = "YARDR_LAND=squash YARDR_NOTIFY=boss /srv/merge-local --fast"

func githubGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func githubRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	githubGit(t, dir, "init", "-q")
	if origin != "" {
		githubGit(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func TestDepotPushGitConfigReadDistinguishesMissingKey(t *testing.T) {
	t.Parallel()
	repo := githubRepo(t, "https://github.com/ada/web.git")
	if values, ok := gitConfigGetAll(repo, depotHelperKey, true); !ok || len(values) != 0 {
		t.Fatalf("missing helper = %q, %v, want none and a readable config", values, ok)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("not a git config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitConfigGetAll(repo, depotHelperKey, true); ok {
		t.Fatal("a malformed git config read as an empty helper list")
	}
}

// depotPushYard is an ask yard on an edge whose depot web has an https
// github.com origin, an assembly group, and a github token standing in
// the yard's own file for the source (home/sources/github.env).
func depotPushYard(t *testing.T) *askYard {
	t.Helper()
	// No helper of the machine's own git config answers in the yard;
	// TestMain pins the global and system config for the whole package.
	y := answerYard(t)
	if err := y.SetSetting(y.ctx, OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := Edge{URL: "https://my.yardr.dev", Key: Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := y.SetSetting(y.ctx, EdgeSetting, edge.String(), "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "github.yaml", depotPushDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(y.home, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(y.home, "sources", "github.env"), []byte("GITHUB_TOKEN=stand-in-token-7f3a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := githubRepo(t, "https://github.com/ada/web.git")
	if err := y.PutDepot(y.ctx, Depot{Name: "web", Path: repo, Base: "main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": depotPushCmd}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	other := githubRepo(t, "https://github.com/ada/other.git")
	if err := y.PutDepot(y.ctx, Depot{Name: "other", Path: other, Base: "main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "other-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/srv/other-land"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func depotPushAsk(depot string, off bool) Ask {
	ask := Ask{Question: "Turn push on?", To: "mckean", Context: []string{"the depot is linked"}, DepotPush: &AskDepotPush{Depot: depot, Off: off}}
	if off {
		ask.Question = "Turn push off?"
	}
	return ask
}

func (y *askYard) depotPath(t *testing.T, name string) string {
	t.Helper()
	d, err := y.GetDepot(y.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return d.Path
}

// Approving a --depot-push card records CarryPending and writes
// nothing itself: the server carries both halves out after it.
func TestDepotPushCardAndApprovalDefersToCarrier(t *testing.T) {
	t.Parallel()
	y := depotPushYard(t)
	seq := y.ask(t, depotPushAsk("web", false), "yardmaster")
	got := y.asked(t, seq).Ask
	if got.DepotPush == nil || got.DepotPush.Depot != "web" || got.DepotPush.Off {
		t.Fatalf("the card stores %+v", got.DepotPush)
	}
	joined := strings.Join(got.DepotPush.Does, " ")
	for _, want := range []string{"turns push to github.com on for depot web", "adds YARDR_PUSH=origin at the front of web-assembly's cmd", "the pack's credential helper", "nothing here runs git push"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(got.DepotPush.Back, "--depot-push-off web") || got.DepotPush.BackBy != "only the operator" {
		t.Errorf("the card's way back = %q by %q", got.DepotPush.Back, got.DepotPush.BackBy)
	}
	if len(got.Weight) != 1 || got.Weight[0].Class != WeightOutside {
		t.Fatalf("the card's weight = %+v, want one outside", got.Weight)
	}
	if strings.Contains(joined, "stand-in-token") {
		t.Fatal("the card holds the token")
	}
	if strings.Contains(joined, "git-credential") || strings.Contains(joined, depotPushCmd) {
		t.Fatal("the card repeats a helper path or cmd")
	}
	// Unweighted, the approval is refused and leaves the ask open.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "weighty (outside)") {
		t.Fatalf("unweighted approval = %v, want a weighty refusal", err)
	}
	// Nobody but the operator approves, a crew member included.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "yardmaster", true, nil); err == nil || !strings.Contains(err.Error(), "approving a command is") {
		t.Fatalf("crew approval = %v, want the operator's refusal", err)
	}
	// Nor does anyone but the operator put the card aside or decline it.
	y.refusesStrangers(t, seq)
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the approval records %q, want CarryPending", done.Result)
	}
	ev := lastAnswer(t, y)
	if held, _ := ev.Data["answer_held"].(bool); !held {
		t.Fatalf("the approval event is not held from non-operator devices: %+v", ev.Data)
	}
	if told, held := ev.WithoutHeld(); !held || told.Data["answer"] != nil {
		t.Fatalf("a non-operator event tells the approval command: %+v", told.Data)
	}
	g, err := y.GetGroup(y.ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if g.Config["cmd"] != depotPushCmd {
		t.Fatalf("the approval itself changed the cmd: %q", g.Config["cmd"])
	}
	if vs, ok := gitConfigGetAll(y.depotPath(t, "web"), depotHelperKey, true); !ok || len(vs) != 0 {
		t.Fatalf("the approval itself wrote the helper: %q", vs)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved || len(rec.Argv) != 2 || rec.Argv[0] != "depot-push" || rec.Argv[1] != "web" {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	if pending, _ := y.PendingCarry(y.ctx, y.now); !pending {
		t.Fatal("the approved card does not pend carrying out")
	}
	// A cmd may already carry a sensitive value. The card says what changes
	// without copying any of the cmd's words into its public sentences.
	y2 := depotPushYard(t)
	const cmdSecret = "cmd-secret-2e91"
	if _, err := y2.SetGroup(y2.ctx, "web-assembly", GroupEdit{Set: map[string]any{"cmd": "TOKEN=" + cmdSecret + " /srv/merge-local"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq2 := y2.ask(t, depotPushAsk("web", false), "yardmaster")
	card := strings.Join(y2.asked(t, seq2).Ask.DepotPush.Does, " ")
	if strings.Contains(card, cmdSecret) {
		t.Fatal("the card repeats a value from the existing cmd")
	}
}

// A card written before its weight was weighed is approved no more:
// approving it weighted, from a card that showed none, is stale.
func TestDepotPushApprovalRefusesAStaleWeight(t *testing.T) {
	t.Parallel()
	y := depotPushYard(t)
	seq := y.ask(t, depotPushAsk("web", false), "yardmaster")
	// Take the weight off the stored card, as a card from before cards
	// were weighed stands.
	if err := y.write(func(w *wtx) error {
		row, err := w.loadBead(y.bead)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		for _, n := range notes {
			if n.Seq != seq || n.Kind != NoteAsk {
				continue
			}
			ask, err := ParseAsk(n)
			if err != nil {
				return err
			}
			ask.Weight = nil
			raw, err := json.Marshal(ask)
			if err != nil {
				return err
			}
			n.Text = string(raw)
			return put(w.Tx, bNotes, join(row.UUID, seqKey(n.Seq)), n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "without its weight") {
		t.Fatalf("approving an unweighted-written card = %v, want a stale-weight refusal", err)
	}
}

func TestDepotPushFilingRefusals(t *testing.T) {
	t.Parallel()
	file := func(y *askYard, depot string, off bool, actor string) error {
		_, err := y.AddAsk(y.ctx, y.bead, depotPushAsk(depot, off), actor)
		return err
	}
	notes := func(y *askYard) int {
		ns, err := y.Notes(y.ctx, y.bead)
		if err != nil {
			t.Fatal(err)
		}
		return len(ns)
	}
	for _, tc := range []struct {
		name string
		make func(t *testing.T) *askYard
		want string
	}{
		{"no edge", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			if err := y.write(func(w *wtx) error { return w.Bucket(bSettings).Delete([]byte(EdgeSetting)) }); err != nil {
				t.Fatal(err)
			}
			return y
		}, "connected to no edge"},
		{"ssh origin", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			githubGit(t, y.depotPath(t, "web"), "remote", "set-url", "origin", "git@github.com:ada/web.git")
			return y
		}, "not https on a host"},
		{"origin with a port", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			githubGit(t, y.depotPath(t, "web"), "remote", "set-url", "origin", "https://github.com:8443/ada/web.git")
			return y
		}, "not https on a host"},
		{"source helper with arguments", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			changed := strings.Replace(depotPushDecl, "credential: [github/git-credential]", "credential: [github/git-credential, fixture]", 1)
			if _, err := y.PutSource(y.ctx, "github.yaml", changed, "mckean"); err != nil {
				t.Fatal(err)
			}
			return y
		}, "not https on a host"},
		{"no token", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			if err := os.WriteFile(filepath.Join(y.home, "sources", "github.env"), []byte("# none\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return y
		}, "no GITHUB_TOKEN stands"},
		{"missing assembly", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			if err := y.PutDepot(y.ctx, Depot{Name: "bare", Path: githubRepo(t, "https://github.com/ada/bare.git"), Base: "main"}, "mckean"); err != nil {
				t.Fatal(err)
			}
			// File for bare, not web: the caller below files for web, so
			// this case is run apart (see the loop).
			return y
		}, "no group bare-assembly"},
		{"another YARDR_PUSH", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			if _, err := y.SetGroup(y.ctx, "web-assembly", GroupEdit{Set: map[string]any{"cmd": "YARDR_PUSH=upstream /srv/merge-local"}}, "mckean"); err != nil {
				t.Fatal(err)
			}
			return y
		}, "never overwrites"},
		{"duplicate YARDR_PUSH", func(t *testing.T) *askYard {
			y := depotPushYard(t)
			if _, err := y.SetGroup(y.ctx, "web-assembly", GroupEdit{Set: map[string]any{"cmd": "YARDR_PUSH=origin YARDR_PUSH=origin /srv/merge-local"}}, "mckean"); err != nil {
				t.Fatal(err)
			}
			return y
		}, "more than one YARDR_PUSH"},
		{"off when not on", func(t *testing.T) *askYard { return depotPushYard(t) }, "not on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := tc.make(t)
			depot, off := "web", false
			if tc.name == "missing assembly" {
				depot = "bare"
			}
			if tc.name == "off when not on" {
				off = true
			}
			before := notes(y)
			if err := file(y, depot, off, "yardmaster"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("filing = %v, want it to contain %q", err, tc.want)
			}
			if notes(y) != before {
				t.Fatal("a refused filing stored something")
			}
		})
	}
	// Fully on already refuses, and a non-crew, non-user filer refuses.
	y := depotPushYard(t)
	helper := filepath.Join(y.home, "sources", "github", "github", "git-credential")
	githubGit(t, y.depotPath(t, "web"), "config", depotHelperKey, helper)
	if _, err := y.SetGroup(y.ctx, "web-assembly", GroupEdit{Set: map[string]any{"cmd": "YARDR_PUSH=origin " + depotPushCmd}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := file(y, "web", false, "yardmaster"); err == nil || !strings.Contains(err.Error(), "on already") {
		t.Fatalf("filing when fully on = %v", err)
	}
	y2 := depotPushYard(t)
	if err := file(y2, "web", false, "ghost"); err == nil || !strings.Contains(err.Error(), "filed by a crew member or a user") {
		t.Fatalf("filing as nobody = %v, want the filer refusal", err)
	}
	// A crew member and a user may each file.
	if _, err := y2.AddAsk(y2.ctx, y2.bead, depotPushAsk("web", false), "ada"); err != nil {
		t.Fatalf("filing as a user: %v", err)
	}
}

// An approval is refused when the yard changed since the card: the
// edge going, or the group's other words moving, each leaves the ask
// open with nothing written.
func TestDepotPushApprovalRefusesWhenTheYardChanged(t *testing.T) {
	t.Parallel()
	y := depotPushYard(t)
	seq := y.ask(t, depotPushAsk("web", false), "yardmaster")
	if _, err := y.SetGroup(y.ctx, "web-assembly", GroupEdit{Set: map[string]any{"log": "/tmp/other.log"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "Nothing ran") {
		t.Fatalf("approving after the group changed = %v, want a stale refusal", err)
	}
	y2 := depotPushYard(t)
	seq2 := y2.ask(t, depotPushAsk("web", false), "yardmaster")
	if err := y2.write(func(w *wtx) error { return w.Bucket(bSettings).Delete([]byte(EdgeSetting)) }); err != nil {
		t.Fatal(err)
	}
	if _, err := y2.ApproveAsk(y2.ctx, y2.bead, seq2, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "connected to no edge") {
		t.Fatalf("approving with no edge = %v", err)
	}
	y3 := depotPushYard(t)
	seq3 := y3.ask(t, depotPushAsk("web", false), "yardmaster")
	changed := strings.Replace(depotPushDecl, "github/git-credential", "github/other-helper", 1)
	if _, err := y3.PutSource(y3.ctx, "github.yaml", changed, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y3.ApproveAsk(y3.ctx, y3.bead, seq3, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "push source") || !strings.Contains(err.Error(), "Nothing ran") {
		t.Fatalf("approving after the source changed = %v, want a stale refusal", err)
	}
}

// The cmd surgery itself: where the push half goes, what it never
// touches, and its exact reverse.
func TestDepotPushCmdSurgery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ cmd, on, off string }{
		{"/srv/land", "YARDR_PUSH=origin /srv/land", "/srv/land"},
		{depotPushCmd, "YARDR_PUSH=origin " + depotPushCmd, depotPushCmd},
		{"YARDR_PUSH=origin /srv/land", "YARDR_PUSH=origin /srv/land", "/srv/land"},
		{"A='x y' /srv/land", "YARDR_PUSH=origin A='x y' /srv/land", "A='x y' /srv/land"},
	} {
		on, changed, err := DepotPushCmd(tc.cmd, false)
		if err != nil || on != tc.on {
			t.Errorf("on(%q) = %q, %v, want %q", tc.cmd, on, err, tc.on)
		}
		_ = changed
		back, _, err := DepotPushCmd(on, true)
		if err != nil || back != tc.off {
			t.Errorf("off(%q) = %q, %v, want %q", on, back, err, tc.off)
		}
		if strings.Contains(on, "YARDR_LAND") != strings.Contains(tc.cmd, "YARDR_LAND") && !strings.Contains(tc.cmd, "YARDR_LAND=squash") {
			// YARDR_LAND stands exactly as it was, in the on form too.
			if strings.Count(on, "YARDR_LAND=squash") != strings.Count(tc.cmd, "YARDR_LAND=squash") {
				t.Errorf("YARDR_LAND moved in %q", on)
			}
		}
	}
	if _, _, err := DepotPushCmd("YARDR_PUSH=upstream /srv/land", false); err == nil || !strings.Contains(err.Error(), "never overwrites") {
		t.Errorf("a conflicting YARDR_PUSH = %v", err)
	}
	if _, _, err := DepotPushCmd("YARDR_PUSH=upstream /srv/land", true); err == nil || !strings.Contains(err.Error(), "never overwrites") {
		t.Errorf("turning a conflicting YARDR_PUSH off = %v", err)
	}
	const secret = "push-value-secret-8f21"
	if _, _, err := DepotPushCmd("YARDR_PUSH="+secret+" /srv/land", false); err == nil || strings.Contains(err.Error(), secret) {
		t.Error("a conflicting push value is disclosed or not refused")
	}
	if _, _, err := DepotPushCmd("YARDR_PUSH=origin YARDR_PUSH=origin /srv/land", true); err == nil || !strings.Contains(err.Error(), "more than one YARDR_PUSH") {
		t.Errorf("turning off duplicate push assignments = %v, want refusal", err)
	}
}

// A helper that only ends like the pack's copy is not the pack's: a
// shell helper ending in that path is refused, never verified, and the
// refusal repeats nothing of its value, which may carry a token.
func TestDepotPushRefusesALookalikeHelper(t *testing.T) {
	// Sequential: process environment.
	y := depotPushYard(t)
	lookalike := "!sh -c 'echo password=stand-in-token-7f3a' " + filepath.Join(t.TempDir(), "sources", "github", "github", "git-credential")
	githubGit(t, y.depotPath(t, "web"), "config", depotHelperKey, lookalike)
	_, err := y.AddAsk(y.ctx, y.bead, depotPushAsk("web", false), "yardmaster")
	if err == nil {
		t.Fatal("filing over a lookalike helper was not refused")
	}
	if !strings.Contains(err.Error(), "not the pack's helper") {
		t.Fatal("the lookalike-helper refusal did not name the conflict")
	}
	if strings.Contains(err.Error(), "stand-in-token") || strings.Contains(err.Error(), "sh -c") {
		t.Fatal("the refusal repeats the helper's value")
	}
	if !IsPackHelper(filepath.Join(y.home, "sources", "github", "github", "git-credential"), filepath.Join(y.home, "sources", "github", "github", "git-credential")) {
		t.Fatal("the pack's own helper path is not the pack's helper")
	}
	y2 := depotPushYard(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	githubGit(t, y2.depotPath(t, "web"), "config", "--global", depotHelperKey, "!sh -c 'echo credential=stand-in-token-7f3a'")
	packHelper := filepath.Join(y2.home, "sources", "github", "github", "git-credential")
	githubGit(t, y2.depotPath(t, "web"), "config", "--local", depotHelperKey, packHelper)
	_, err = y2.AddAsk(y2.ctx, y2.bead, depotPushAsk("web", false), "yardmaster")
	if err == nil {
		t.Fatal("filing with two effective helpers was not refused")
	}
	if !strings.Contains(err.Error(), "more than one credential helper") {
		t.Fatal("the multiple-helper refusal did not name the conflict")
	}
	if strings.Contains(err.Error(), "stand-in-token") || strings.Contains(err.Error(), "sh -c") {
		t.Fatal("the multiple-helper refusal repeats a helper value")
	}
}

// A bare credential.helper answers for every host, so git runs it on the
// assembly's push and hands it the token the pack's helper gave: one
// standing in the depot's shared config refuses the card, and the
// refusal repeats nothing of its value.
func TestDepotPushRefusesABareHelper(t *testing.T) {
	t.Parallel()
	y := depotPushYard(t)
	repo := y.depotPath(t, "web")
	githubGit(t, repo, "config", "credential.helper", "!sh -c 'cat >> /tmp/stolen'")
	_, err := y.AddAsk(y.ctx, y.bead, depotPushAsk("web", false), "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "not the pack's helper") {
		t.Fatalf("filing beside a bare helper = %v, want refused as not the pack's", err)
	}
	if strings.Contains(err.Error(), "stolen") || strings.Contains(err.Error(), "sh -c") {
		t.Fatal("the refusal repeats the helper's value")
	}
}

// PushHelpers reads the helpers git runs for a host: the bare key and
// that host's, in order, an empty value clearing those before it, and
// never another host's.
func TestPushHelpersAreThoseGitRuns(t *testing.T) {
	t.Parallel()
	// TestMain pins the machine's git config aside for the whole package.
	repo := githubRepo(t, "https://github.com/ada/web.git")
	githubGit(t, repo, "config", "--add", "credential.helper", "cache")
	githubGit(t, repo, "config", "--add", "credential.https://gitlab.com.helper", "other")
	githubGit(t, repo, "config", "--add", "credential.http://github.com.helper", "plain")
	githubGit(t, repo, "config", "--add", "credential.https://*.github.com.helper", "wild")
	githubGit(t, repo, "config", "--add", depotHelperKey, "/pack")
	got, ok := PushHelpers(repo, "github.com")
	if !ok || strings.Join(got, ",") != "cache,/pack" {
		t.Fatalf("helpers = %q, %v, want cache,/pack", got, ok)
	}
	if got, _ := PushHelpers(repo, "api.github.com"); strings.Join(got, ",") != "cache,wild" {
		t.Fatalf("helpers for api.github.com = %q, want cache,wild", got)
	}
	githubGit(t, repo, "config", "--add", depotHelperKey, "")
	githubGit(t, repo, "config", "--add", depotHelperKey, "/pack")
	if got, _ := PushHelpers(repo, "github.com"); strings.Join(got, ",") != "/pack" {
		t.Fatalf("helpers after a reset = %q, want /pack", got)
	}
}
