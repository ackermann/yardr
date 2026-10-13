package dispatch_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
)

// The tests of code that crosses with a bead (docs/peers.md, "Code that
// comes back"): the yards are real stores and daemons over HTTP, as in
// peerbeads_test.go, and what they share is a bare repository, of which
// each has a clone of its own as its depot web.

// gitIn runs git in dir, off the developer's configuration and with a name
// to commit as.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitIn(t *testing.T, dir, file, text string) string {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644))
	gitIn(t, dir, "add", file)
	gitIn(t, dir, "commit", "-q", "-m", file)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// sharedRemote is a bare repository with one commit on main.
func sharedRemote(t *testing.T) string {
	t.Helper()
	// What the yards' own git calls read is the developer's otherwise.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	gitIn(t, dir, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(dir, "seed")
	gitIn(t, dir, "clone", "-q", remote, seed)
	gitIn(t, seed, "checkout", "-q", "-b", "main")
	commitIn(t, seed, "a.txt", "one\n")
	gitIn(t, seed, "push", "-q", "origin", "main")
	return remote
}

// clones makes y's depot web a clone of remote, with the base main.
func (y *peerYard) clones(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "web")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	must(t, y.api.PutDepot(context.Background(), store.Depot{Name: "web", Path: dir, Kind: store.DepotGit, Base: "main"}, "jo"))
	return dir
}

// sendsFrom makes y a yard whose stage build of its depot web, a clone of
// remote, is worked by the peer to.
func (y *peerYard) sendsFrom(t *testing.T, remote, to string, set map[string]any) string {
	t.Helper()
	dir := y.clones(t, remote)
	putFlow(t, y, "sending", sending)
	must(t, y.api.SetFlowPointer(context.Background(), "web", "", "sending", "jo"))
	y.sends(t, to, set)
	return dir
}

// builds is the work of b's builder and of return-branch on the bead: a
// commit on the bead's branch, made from where the bead says its work
// starts, pushed to origin and written on the bead.
func (y *peerYard) builds(t *testing.T, repo string, b *store.Bead) string {
	t.Helper()
	fam, err := y.api.Family(context.Background(), b.ID)
	must(t, err)
	branch := store.Branch(b.ID)
	gitIn(t, repo, "checkout", "-q", "-b", branch, fam.Base)
	commit := commitIn(t, repo, "export.txt", "the export of "+b.ID+"\n")
	gitIn(t, repo, "checkout", "-q", "main")
	gitIn(t, repo, "push", "-q", "origin", branch)
	y.result(t, b.ID, branch, commit, gitIn(t, repo, "remote", "get-url", "origin"))
	return commit
}

func (y *peerYard) result(t *testing.T, id, branch, commit, remote string) {
	t.Helper()
	_, err := y.api.UpdateBead(context.Background(), id, store.BeadPatch{Metadata: map[string]any{
		store.MetaResultBranch: branch, store.MetaResultCommit: commit, store.MetaResultRemote: remote}}, store.Expect{}, "assembly")
	must(t, err)
}

func (y *peerYard) done(t *testing.T, id string) {
	t.Helper()
	_, err := y.api.Done(context.Background(), id, "done", "", store.Expect{}, "builder")
	must(t, err)
}

// branchAt is the commit of branch in repo, "" when it has none.
func branchAt(t *testing.T, repo, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// The whole way: a's bead says where its work starts, b fetches that
// commit and starts there though its own base is elsewhere, pushes its
// branch and names it in the return, and a has the commit as its bead's
// branch before the outcome moves the bead.
func TestPeerCodeComesBack(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	// The base has moved since b last fetched, and b's own main moved on
	// by itself: neither is where the work starts.
	base := commitIn(t, aRepo, "b.txt", "two\n")
	gitIn(t, aRepo, "push", "-q", "origin", "main")
	commitIn(t, bRepo, "local.txt", "b's own\n")

	mine := a.bead(t, "Add the export")
	a.pass(t)
	form := store.RemoteForm(gitIn(t, aRepo, "remote", "get-url", "origin"))
	if sent := a.beadEvents(t, mine.ID, "peer_bead_sent"); len(sent) != 1 || sent[0].Data["base"] != "main" || sent[0].Data["base_commit"] != base ||
		!strings.HasPrefix(form, "file:///") || !strings.HasSuffix(sent[0].Data["remote"].(string), "/remote") {
		t.Fatalf("a's peer_bead_sent = %+v (form %s)", sent, form)
	}
	theirs := b.from(t, "a")
	fam, err := b.api.Family(context.Background(), theirs.ID)
	must(t, err)
	if g := theirs.Peer.Git; g == nil || g.Base != "main" || g.BaseCommit != base || fam.Base != base ||
		!strings.Contains(theirs.Peer.CodeLine(theirs.ID), "its work starts from main at "+base[:12]) {
		t.Fatalf("b's bead starts from %+v, family %+v, %q", theirs.Peer.Git, fam, theirs.Peer.CodeLine(theirs.ID))
	}
	if arrived := b.beadEvents(t, theirs.ID, "peer_bead_arrived"); len(arrived) != 1 || arrived[0].Data["base_commit"] != base {
		t.Fatalf("b's peer_bead_arrived = %+v", arrived)
	}
	commit := b.builds(t, bRepo, theirs)
	if gitIn(t, bRepo, "rev-parse", commit+"^") != base {
		t.Fatal("b's work does not start from the commit a named")
	}
	b.done(t, theirs.ID)
	if ret := b.beadEvents(t, theirs.ID, "peer_bead_returned"); len(ret) != 1 || ret[0].Data["branch"] != store.Branch(theirs.ID) || ret[0].Data["commit"] != commit {
		t.Fatalf("b's peer_bead_returned = %+v", ret)
	}
	b.pass(t)
	a.pass(t)

	got := a.get(t, mine.ID)
	if got.Stage != "review" || got.Hold {
		t.Fatalf("a's bead after the return = %+v, %+v", got, got.Away())
	}
	if at := branchAt(t, aRepo, store.Branch(mine.ID)); at != commit {
		t.Fatalf("a's %s is at %q, want b's commit %s", store.Branch(mine.ID), at, commit)
	}
	if left := gitIn(t, aRepo, "for-each-ref", "refs/yardr"); left != "" {
		t.Fatalf("a keeps a ref of the fetch: %s", left)
	}
	fetched, adv := a.beadEvents(t, mine.ID, "peer_bead_fetched"), a.beadEvents(t, mine.ID, "advanced")
	if len(fetched) != 1 || fetched[0].Data["commit"] != commit || fetched[0].Data["into"] != store.Branch(mine.ID) || fetched[0].Data["branch"] != store.Branch(theirs.ID) ||
		len(adv) != 1 || adv[0].Seq < fetched[0].Seq || adv[0].Actor != "peer:b" {
		t.Fatalf("a's peer_bead_fetched = %+v, advanced = %+v", fetched, adv)
	}
	away := got.Away()
	line := away.CodeLine(mine.ID)
	if away.FetchedAt == nil || away.Git.Commit != commit ||
		!strings.HasPrefix(line, "branch "+store.Branch(mine.ID)+" came from b: its "+store.Branch(theirs.ID)+" at "+commit[:12]+", fetched ") ||
		!strings.HasSuffix(line, "This yard has not tested it") {
		t.Fatalf("what a's bead says of its branch = %+v, %q", away, line)
	}
	ns := a.notes(t, mine.ID)
	if len(ns) != 1 || ns[0].Author != "yard" || !strings.Contains(ns[0].Text, "this yard has not tested it") || !strings.Contains(ns[0].Text, store.Branch(theirs.ID)) {
		t.Fatalf("a's notes = %+v", ns)
	}
	// Nothing is fetched or moved a second time.
	a.pass(t)
	if len(a.beadEvents(t, mine.ID, "peer_bead_fetched")) != 1 || len(a.beadEvents(t, mine.ID, "advanced")) != 1 {
		t.Fatal("a further pass took the branch again")
	}
}

// A bead whose work b cannot start where a's starts is refused as
// base_unknown: b's depot is of another repository, or the base commit is on
// no remote. Nothing of it is kept, and a's stage ends as refused.
func TestPeerBeadBaseUnknown(t *testing.T) {
	for name, tc := range map[string]struct {
		otherRemote, unpushed bool
		detail                string
	}{
		"another remote":      {otherRemote: true, detail: "and the bead starts from file://"},
		"a commit not pushed": {unpushed: true, detail: "could not be fetched from origin"},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			remote := sharedRemote(t)
			bRemote := remote
			if tc.otherRemote {
				bRemote = sharedRemote(t)
			}
			b.clones(t, bRemote)
			b.takes(t, "a", "web")
			aRepo := a.sendsFrom(t, remote, "b", nil)
			if tc.unpushed {
				commitIn(t, aRepo, "b.txt", "not pushed\n")
			}
			mine := a.bead(t, "Add the export")
			a.pass(t)
			refused := b.events(t, "peer_message_refused")
			if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedBaseUnknown || !strings.Contains(refused[0].Data["detail"].(string), tc.detail) {
				t.Fatalf("b's peer_message_refused = %+v", refused)
			}
			if bs, _ := b.api.ListBeads(context.Background(), store.BeadFilter{}); len(bs) != 0 {
				t.Fatalf("b kept %d beads of a refused one", len(bs))
			}
			got := a.get(t, mine.ID)
			if got.Stage != "decide" || got.Away().Outcome != store.OutcomeRefused || !strings.Contains(got.Away().Detail, "where this yard's bead starts") {
				t.Fatalf("a's bead = %+v, %+v", got, got.Away())
			}
		})
	}
}

// A remote that is not there when a fetches: the fetch is tried again
// after the daemon's wait, and after the group's tries the bead is held
// with a note, its outcome unapplied and its stay not ended. Lifting the
// hold once the remote is back takes the branch and moves the bead.
func TestPeerCodeRemoteDown(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", map[string]any{store.GroupTries: float64(2)})
	mine := a.bead(t, "Add the export")
	a.pass(t)
	theirs := b.from(t, "a")
	commit := b.builds(t, bRepo, theirs)
	b.done(t, theirs.ID)
	must(t, os.Rename(remote, remote+".away"))
	b.pass(t)
	a.pass(t)

	got := a.get(t, mine.ID)
	away := got.Away()
	if got.Stage != "build" || got.Hold || away.Outcome != "done" || away.FetchTries != 1 || away.FetchRetryAt == nil || away.FetchedAt != nil ||
		!strings.Contains(away.CodeLine(mine.ID), "is not fetched yet, and its outcome done waits for it: 1 try so far, the next at ") {
		t.Fatalf("a's bead after the first fetch failed = %+v, %+v, %q", got, away, away.CodeLine(mine.ID))
	}
	// Not before its wait is over.
	a.pass(t)
	if a.get(t, mine.ID).Away().FetchTries != 1 {
		t.Fatal("the fetch was tried again at once")
	}
	clock.now = clock.now.Add(time.Minute)
	a.pass(t)
	got = a.get(t, mine.ID)
	away = got.Away()
	if got.Stage != "build" || !got.Hold || away.FetchGaveUp == nil || away.FetchTries != 2 || len(a.live(t)) != 1 ||
		!strings.Contains(away.CodeLine(mine.ID), "given up after 2 tries") || !strings.Contains(away.CodeLine(mine.ID), "yardr bead unhold "+mine.ID+" tries again") {
		t.Fatalf("a's bead after its tries = %+v, %+v, sessions %+v", got, away, a.live(t))
	}
	ns := a.notes(t, mine.ID)
	if len(ns) != 1 || !strings.Contains(ns[0].Text, "could not take the branch (2 tries)") || !strings.Contains(ns[0].Text, "yardr bead unhold "+mine.ID) {
		t.Fatalf("a's note of the hold = %+v", ns)
	}
	if held := a.beadEvents(t, mine.ID, "held"); len(held) != 1 || held[0].Actor != "yard" {
		t.Fatalf("a's held = %+v", held)
	}
	// Held, nothing is tried, the remote back or not; and the bead is not
	// sent out again from under its answer.
	must(t, os.Rename(remote+".away", remote))
	clock.now = clock.now.Add(time.Hour)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "build" || got.Away().FetchTries != 2 || branchAt(t, aRepo, store.Branch(mine.ID)) != "" {
		t.Fatalf("a's held bead after a pass = %+v", got)
	}
	_, err := a.api.SetHold(context.Background(), mine.ID, false, store.Expect{}, "jo")
	must(t, err)
	a.pass(t)
	got = a.get(t, mine.ID)
	if got.Stage != "review" || got.Hold || branchAt(t, aRepo, store.Branch(mine.ID)) != commit || len(a.beadEvents(t, mine.ID, "peer_bead_sent")) != 1 {
		t.Fatalf("a's bead once the remote was back and it was unheld = %+v, %+v", got, got.Away())
	}
	if bs, _ := b.api.ListBeads(context.Background(), store.BeadFilter{Label: "from-a"}); len(bs) != 0 {
		t.Fatalf("b has %d open beads of a: the bead was sent again", len(bs))
	}
}

// What a fetched is not the commit the return names, or the branch it
// names is no bead's: not taken, with no try that could change it. The
// bead is held at once, no branch is made, and a person who moves the bead
// on by hand ends the wait.
func TestPeerCodeNotWhatTheReturnNames(t *testing.T) {
	for name, tc := range map[string]struct {
		branch string
		want   string
	}{
		"another commit": {want: "and the return names "},
		"not a bead's":   {branch: "main", want: `the branch it names, "main", is not of the form a bead's branch has`},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			remote := sharedRemote(t)
			bRepo := b.clones(t, remote)
			b.takes(t, "a", "web")
			aRepo := a.sendsFrom(t, remote, "b", nil)
			mainAt := branchAt(t, aRepo, "main")
			mine := a.bead(t, "Add the export")
			a.pass(t)
			theirs := b.from(t, "a")
			commit := b.builds(t, bRepo, theirs)
			if tc.branch != "" {
				b.result(t, theirs.ID, tc.branch, commit, remote)
			} else {
				// The branch moves on the remote after it was written down.
				gitIn(t, bRepo, "checkout", "-q", store.Branch(theirs.ID))
				commitIn(t, bRepo, "more.txt", "later\n")
				gitIn(t, bRepo, "push", "-q", "origin", store.Branch(theirs.ID))
			}
			b.done(t, theirs.ID)
			b.pass(t)
			a.pass(t)
			got := a.get(t, mine.ID)
			if got.Stage != "build" || !got.Hold || got.Away().FetchTries != 1 || !strings.Contains(got.Away().FetchError, tc.want) {
				t.Fatalf("a's bead = %+v, %+v", got, got.Away())
			}
			if branchAt(t, aRepo, store.Branch(mine.ID)) != "" || branchAt(t, aRepo, "main") != mainAt || gitIn(t, aRepo, "for-each-ref", "refs/yardr") != "" {
				t.Fatal("a took something of a branch it refused")
			}
			if ns := a.notes(t, mine.ID); len(ns) != 1 || !strings.Contains(ns[0].Text, tc.want) {
				t.Fatalf("a's notes = %+v", ns)
			}
			// Unheld, it is looked at again, and held again.
			_, err := a.api.SetHold(context.Background(), mine.ID, false, store.Expect{}, "jo")
			must(t, err)
			a.pass(t)
			if got := a.get(t, mine.ID); got.Stage != "build" || !got.Hold {
				t.Fatalf("a's bead after an unhold = %+v", got)
			}
			// Moved on by hand, without the code: the stay is over.
			_, err = a.api.Advance(context.Background(), mine.ID, "decide", store.Expect{}, "jo")
			must(t, err)
			a.pass(t)
			fs, err := a.api.PeerFetches(context.Background())
			must(t, err)
			ret := a.beadEvents(t, mine.ID, "peer_bead_returned")
			if len(fs) != 0 || len(ret) != 2 || ret[1].Data["stale"] != true || a.get(t, mine.ID).Stage != "decide" {
				t.Fatalf("after the bead was moved on by hand: fetches %+v, returned %+v", fs, ret)
			}
		})
	}
}

// Code that is not this yard's to take is no error: the outcome is
// applied, with a note that says why the branch was not taken. A bead of a
// dir depot, and a peer group whose remote is another repository than the
// branch was pushed to.
func TestPeerCodeNotTaken(t *testing.T) {
	for name, tc := range map[string]struct {
		dir  bool
		want string
	}{
		"a dir depot":    {dir: true, want: "depot web is a directory, which has no branches"},
		"another remote": {want: "and the remote upstream of depot web is file://"},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			remote := sharedRemote(t)
			bRepo := b.clones(t, remote)
			b.takes(t, "a", "web")
			ctx := context.Background()
			aRepo := ""
			if tc.dir {
				must(t, a.api.PutDepot(ctx, store.Depot{Name: "web", Path: t.TempDir(), Kind: store.DepotDir}, "jo"))
				putFlow(t, a, "sending", sending)
				must(t, a.api.SetFlowPointer(ctx, "web", "", "sending", "jo"))
				a.sends(t, "b", nil)
			} else {
				aRepo = a.sendsFrom(t, remote, "b", map[string]any{store.GroupRemote: "upstream"})
				gitIn(t, aRepo, "remote", "add", "upstream", sharedRemote(t))
			}
			mine := a.bead(t, "Add the export")
			a.pass(t)
			theirs := b.from(t, "a")
			if tc.dir && theirs.Peer.Git != nil {
				t.Fatalf("a dir depot's bead came with a base: %+v", theirs.Peer.Git)
			}
			b.builds(t, bRepo, theirs)
			b.done(t, theirs.ID)
			b.pass(t)
			a.pass(t)
			got := a.get(t, mine.ID)
			away := got.Away()
			if got.Stage != "review" || got.Hold || away.FetchedAt != nil || !strings.Contains(away.NotTaken, tc.want) ||
				!strings.Contains(away.CodeLine(mine.ID), "was not taken: ") {
				t.Fatalf("a's bead = %+v, %+v", got, away)
			}
			if ns := a.notes(t, mine.ID); len(ns) != 1 || !strings.Contains(ns[0].Text, "that this yard did not take: ") || !strings.Contains(ns[0].Text, tc.want) {
				t.Fatalf("a's notes = %+v", ns)
			}
			if not := a.beadEvents(t, mine.ID, "peer_bead_not_fetched"); len(not) != 1 || not[0].Data["held"] != nil {
				t.Fatalf("a's peer_bead_not_fetched = %+v", not)
			}
			if aRepo != "" && branchAt(t, aRepo, store.Branch(mine.ID)) != "" {
				t.Fatal("a made a branch of code it did not take")
			}
		})
	}
}

// A yard whose intake's depot is a directory takes a bead that names its
// base with its words only, as a bead that names none, and returns words.
func TestPeerCodeIntoADirDepot(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	must(t, b.api.PutDepot(context.Background(), store.Depot{Name: "docs", Path: t.TempDir(), Kind: store.DepotDir}, "jo"))
	b.takes(t, "a", "docs")
	a.sendsFrom(t, remote, "b", nil)
	mine := a.bead(t, "Write it up")
	a.pass(t)
	if sent := a.beadEvents(t, mine.ID, "peer_bead_sent"); len(sent) != 1 || sent[0].Data["base_commit"] == nil {
		t.Fatalf("a's peer_bead_sent = %+v", sent)
	}
	theirs := b.from(t, "a")
	fam, err := b.api.Family(context.Background(), theirs.ID)
	must(t, err)
	if theirs.Peer.Git != nil || fam.Base != "" {
		t.Fatalf("b's bead in a dir depot has a base: %+v, %+v", theirs.Peer.Git, fam)
	}
	b.done(t, theirs.ID)
	b.pass(t)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "review" || got.Away().Git != nil || len(a.notes(t, mine.ID)) != 0 {
		t.Fatalf("a's bead after words came back = %+v, %+v", got, got.Away())
	}
}

// A bead sent on: each pair of yards passes the code between themselves.
// c starts from the commit a named, b takes c's branch as its own bead's
// and pushes that, and a takes b's.
func TestPeerCodeSentOn(t *testing.T) {
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, true, true)
	link(t, b, c, true, true)
	ctx := context.Background()
	remote := sharedRemote(t)
	aRepo := a.sendsFrom(t, remote, "b", nil)
	base := branchAt(t, aRepo, "main")
	bRepo := b.clones(t, remote)
	putFlow(t, b, "relay", "build -> push on done | returned on failed\npush -> returned on done\nreturned terminal\n")
	_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Receive: []string{"bead"}}, "jo")
	must(t, err)
	_, err = b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Depot: "web", Flow: "relay"}, "jo")
	must(t, err)
	_, err = b.api.AllowPeer(ctx, "c", store.PeerAllow{Send: []string{"bead"}}, "jo")
	must(t, err)
	must(t, b.api.PutGroup(ctx, store.Group{Name: "at-c", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "c"}}, "me"))
	_, err = b.api.AddRoute(ctx, store.Route{Stage: "build", Group: "at-c"}, "me")
	must(t, err)
	cRepo := c.clones(t, remote)
	c.takes(t, "b", "web")

	mine := a.bead(t, "Add the export")
	a.pass(t)
	b.pass(t)
	atB, atC := b.from(t, "a"), c.from(t, "b")
	if g := atC.Peer.Git; g == nil || g.BaseCommit != base || g.Base != "main" {
		t.Fatalf("c's bead starts from %+v, want a's base %s", g, base)
	}
	commit := c.builds(t, cRepo, atC)
	c.done(t, atC.ID)
	c.pass(t)
	b.pass(t)
	if got := b.get(t, atB.ID); got.Stage != "push" || branchAt(t, bRepo, store.Branch(atB.ID)) != commit {
		t.Fatalf("b's bead after c's return = %+v, its branch at %q", got, branchAt(t, bRepo, store.Branch(atB.ID)))
	}
	// b's return-branch: its own bead's branch, never c's name for it.
	gitIn(t, bRepo, "push", "-q", "origin", store.Branch(atB.ID))
	b.result(t, atB.ID, store.Branch(atB.ID), commit, remote)
	b.done(t, atB.ID)
	b.pass(t)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "review" || branchAt(t, aRepo, store.Branch(mine.ID)) != commit || got.Away().Git.Branch != store.Branch(atB.ID) {
		t.Fatalf("a's bead after the chain unwound = %+v, %+v", got, got.Away())
	}
}

// spells makes git name url as the URL of origin in repo, which stays the
// clone of a directory it is: a stand-in git before the real one answers
// `remote get-url origin` there, and hands on everything else. So a depot
// reads as reached by ssh or https, and its fetches and pushes still work.
// (git's own insteadOf would not do: get-url gives the URL it rewrote.)
func spells(t *testing.T, repo, url string) {
	t.Helper()
	must(t, os.WriteFile(filepath.Join(repo, ".git", "spelled-origin"), []byte(url+"\n"), 0o644))
	if os.Getenv("YARDR_TEST_GIT") != "" {
		return
	}
	real, err := exec.LookPath("git")
	must(t, err)
	bin := t.TempDir()
	scripttest.Write(t, filepath.Join(bin, "git"), `#!/bin/sh
dir=.
[ "${1:-}" = -C ] && dir=$2 && shift 2
if [ "$*" = "remote get-url origin" ] && [ -f "$dir/.git/spelled-origin" ]; then
  exec cat "$dir/.git/spelled-origin"
fi
exec "$YARDR_TEST_GIT" -C "$dir" "$@"
`)
	t.Setenv("YARDR_TEST_GIT", real)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// One repository that the two yards reach in two ways is one remote: a
// bead with code goes out and comes back between a yard that clones by
// ssh, in either spelling, and one that clones by https. Nothing that
// crosses says how either gets in.
func TestPeerCodeOneRemoteTwoSpellings(t *testing.T) {
	const scp, ssh, https = "git@git.example.test:acme/web.git", "ssh://git@git.example.test:2222/acme/web", "https://jo:s3cret@Git.Example.test/acme/web.git/"
	for name, tc := range map[string]struct{ a, b string }{
		"scp and https": {scp, https},
		"https and ssh": {https, ssh},
		"ssh and scp":   {ssh, scp},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			remote := sharedRemote(t)
			bRepo := b.clones(t, remote)
			b.takes(t, "a", "web")
			aRepo := a.sendsFrom(t, remote, "b", nil)
			spells(t, aRepo, tc.a)
			spells(t, bRepo, tc.b)
			base := commitIn(t, aRepo, "b.txt", "two\n")
			gitIn(t, aRepo, "push", "-q", "origin", "main")

			mine := a.bead(t, "Add the export")
			a.pass(t)
			const form = "git.example.test/acme/web"
			if sent := a.beadEvents(t, mine.ID, "peer_bead_sent"); len(sent) != 1 || sent[0].Data["remote"] != form {
				t.Fatalf("a's peer_bead_sent = %+v, want the remote %s", sent, form)
			}
			if refused := b.events(t, "peer_message_refused"); len(refused) != 0 {
				t.Fatalf("b refused it: %+v", refused)
			}
			theirs := b.from(t, "a")
			if g := theirs.Peer.Git; g == nil || g.BaseCommit != base || g.Remote != form {
				t.Fatalf("b's bead starts from %+v", theirs.Peer.Git)
			}
			// What return-branch writes: the URL as b has it.
			fam, err := b.api.Family(context.Background(), theirs.ID)
			must(t, err)
			branch := store.Branch(theirs.ID)
			gitIn(t, bRepo, "checkout", "-q", "-b", branch, fam.Base)
			commit := commitIn(t, bRepo, "export.txt", "the export\n")
			gitIn(t, bRepo, "checkout", "-q", "main")
			gitIn(t, bRepo, "push", "-q", "origin", branch)
			b.result(t, theirs.ID, branch, commit, tc.b)
			b.done(t, theirs.ID)
			if ret := b.beadEvents(t, theirs.ID, "peer_bead_returned"); len(ret) != 1 || ret[0].Data["remote"] != form {
				t.Fatalf("b's peer_bead_returned = %+v, want the remote %s", ret, form)
			}
			b.pass(t)
			a.pass(t)
			got := a.get(t, mine.ID)
			if got.Stage != "review" || got.Hold || got.Away().NotTaken != "" || branchAt(t, aRepo, store.Branch(mine.ID)) != commit {
				t.Fatalf("a's bead after the return = %+v, %+v; its branch is at %q, want %s", got, got.Away(), branchAt(t, aRepo, store.Branch(mine.ID)), commit)
			}
			// What b wrote on its own bead is b's; nothing of it reached a,
			// and nothing b logs of the exchange has it.
			for _, y := range []*peerYard{a, b} {
				evs, err := y.api.Events(context.Background(), "", 500)
				must(t, err)
				for _, e := range evs {
					if said := fmt.Sprint(e.Data); (y == a || strings.HasPrefix(e.Kind, "peer_")) && (strings.Contains(said, "s3cret") || strings.Contains(said, "git@")) {
						t.Errorf("%s's %s says how a yard gets in: %s", y.name, e.Kind, said)
					}
				}
			}
			if ns := a.notes(t, mine.ID); len(ns) == 0 || strings.Contains(fmt.Sprint(ns), "s3cret") || strings.Contains(fmt.Sprint(ns), "git@") {
				t.Errorf("a's notes = %+v", ns)
			}
		})
	}
}

// Two repositories on one host are two remotes, however alike the yards
// reach them: on the way out the bead is refused as base_unknown, and a
// branch pushed to the other one is not taken on the way back.
func TestPeerCodeTwoRepositoriesOnOneHost(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	spells(t, aRepo, "https://git.example.test/acme/web.git")
	spells(t, bRepo, "https://git.example.test/acme/api.git")
	mine := a.bead(t, "Add the export")
	a.pass(t)
	refused := b.events(t, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedBaseUnknown ||
		!strings.Contains(refused[0].Data["detail"].(string), "is git.example.test/acme/api here, and the bead starts from git.example.test/acme/web") {
		t.Fatalf("b's peer_message_refused = %+v", refused)
	}
	if got := a.get(t, mine.ID); got.Stage != "decide" || got.Away().Outcome != store.OutcomeRefused {
		t.Fatalf("a's bead = %+v, %+v", got, got.Away())
	}

	// Back: b has the repository a has, and says it pushed to the other.
	spells(t, bRepo, "git@git.example.test:acme/web.git")
	_, err := a.api.Advance(context.Background(), mine.ID, "build", store.Expect{}, "jo")
	must(t, err)
	a.pass(t)
	theirs := b.from(t, "a")
	commit := b.builds(t, bRepo, theirs)
	b.result(t, theirs.ID, store.Branch(theirs.ID), commit, "git@git.example.test:acme/api.git")
	b.done(t, theirs.ID)
	b.pass(t)
	a.pass(t)
	got := a.get(t, mine.ID)
	if got.Stage != "review" || !strings.Contains(got.Away().NotTaken, "it was pushed to git.example.test/acme/api, and the remote origin of depot web is git.example.test/acme/web here") ||
		branchAt(t, aRepo, store.Branch(mine.ID)) != "" {
		t.Fatalf("a's bead = %+v, %+v", got, got.Away())
	}
}

// A base commit that is on no branch of the taking depot, fetched by its own
// name, is kept by the bead's ref from the moment the bead is taken: a gc
// that prunes at once leaves it, and the builder starts from it. The ref
// goes when the bead has closed.
func TestPeerBaseKeptByARef(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	// a's main is ahead of the remote's, and its commit is on the remote
	// under another name: main, which the bead names, does not bring it.
	base := commitIn(t, aRepo, "b.txt", "two\n")
	gitIn(t, aRepo, "push", "-q", "origin", "main:refs/heads/side")

	mine := a.bead(t, "Add the export")
	a.pass(t)
	theirs := b.from(t, "a")
	ref := store.PeerBaseRef(theirs.ID)
	refs := func() string {
		return gitIn(t, bRepo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/yardr")
	}
	if refs() != ref+" "+base {
		t.Fatalf("b's refs once the bead is taken = %q, want %s at %s", refs(), ref, base)
	}
	if on := gitIn(t, bRepo, "branch", "-a", "--contains", base); on != "" {
		t.Fatalf("the base commit is on a branch of b's (%s): the test shows nothing", on)
	}
	gc := func() {
		// FETCH_HEAD and the reflogs name it no longer either.
		gitIn(t, bRepo, "fetch", "-q", "origin", "main")
		gitIn(t, bRepo, "reflog", "expire", "--expire=now", "--all")
		gitIn(t, bRepo, "gc", "-q", "--prune=now")
	}
	gc()
	// A pass of b's daemon leaves the ref of an open bead.
	b.pass(t)
	gc()
	if refs() != ref+" "+base {
		t.Fatalf("b's refs after a pass = %q", refs())
	}
	commit := b.builds(t, bRepo, theirs)
	if gitIn(t, bRepo, "rev-parse", commit+"^") != base {
		t.Fatal("b's work does not start from the commit a named")
	}
	b.done(t, theirs.ID)
	b.pass(t)
	if got := b.get(t, theirs.ID); got.Status != "closed" || refs() != "" {
		t.Fatalf("b's bead is %s and its refs are %q, want it closed and none", got.Status, refs())
	}
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "review" || branchAt(t, aRepo, store.Branch(mine.ID)) != commit || gitIn(t, aRepo, "for-each-ref", "refs/yardr") != "" {
		t.Fatalf("a's bead = %+v, %+v", got, got.Away())
	}

	// A daemon that starts after a bead closed finds the ref it left.
	gitIn(t, bRepo, "update-ref", store.PeerBaseRef("web-gone"), base)
	b.pass(t)
	if refs() == "" {
		t.Fatal("a daemon that had looked at the depot looked again with nothing changed")
	}
	b.d = &dispatch.Dispatcher{Store: b.api, Runners: b.d.Runners, PeerKey: b.d.PeerKey}
	dispatch.SetClock(b.d, func() time.Time { return clock.now })
	b.pass(t)
	if refs() != "" {
		t.Fatalf("b's refs after its daemon started anew = %q", refs())
	}
}
