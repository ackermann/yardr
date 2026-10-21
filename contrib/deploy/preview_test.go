package deploy

// These tests run deploy-dokku preview the way a preview group does (see
// deploy_dokku_test.go for the yard and the stand-in for ssh), and the
// removal of previews by each kind of run.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// previewYard is newYard with bead r-2 waiting in preview, its branch
// yard/r-2 one commit ahead of main, and the group's cmd that of a preview:
// no app, and a URL that is a pattern.
func previewYard(t *testing.T) *yard {
	y := newYard(t)
	y.mode = "preview"
	delete(y.opts, "DOKKU_APP")
	y.opts["DOKKU_URL"] = "http://{app}.dokku.test"
	y.yardr("bead", "create", "a page", "--id", "r-2", "-d", "r", "-s", "preview")
	y.branch("r-2", "page.txt", "a first try\n")
	return y
}

// worktree is where the branch of bead id is checked out, made with the
// branch (from from) when it is not there.
func (y *yard) worktree(id, from string) string {
	y.t.Helper()
	wt := filepath.Join(filepath.Dir(y.repo), "wt-"+id)
	if _, err := os.Stat(wt); err != nil {
		y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, from)
	}
	return wt
}

// branch commits a file on the branch of bead id and gives the commit.
func (y *yard) branch(id, file, content string) string {
	y.t.Helper()
	return y.commit(y.worktree(id, "main"), file, content)
}

// remove runs deploy-dokku remove <depot> as a person does, by hand: with
// the options in its environment and no bead. It gives what it printed and
// whether it ended well.
func (y *yard) remove(depot string) (string, bool) {
	y.t.Helper()
	script, err := filepath.Abs("deploy-dokku")
	if err != nil {
		y.t.Fatal(err)
	}
	cmd := exec.Command(script, "remove", depot)
	cmd.Dir = y.home
	cmd.Env = append(append([]string{}, y.env...), "PATH="+y.bin+":"+os.Getenv("PATH"),
		"YARDR_BIN="+yardrBin,
		// The session that runs it may be working a bead of its own.
		"YARDR_BEAD=r-1", "YARDR_DEPOT=other")
	for k, v := range y.opts {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	y.t.Logf("remove %s:\n%s", depot, out)
	return string(out), err == nil
}

// The bead's branch is pushed to an app of the bead's own, made on the host
// over the pinned ssh, and the bead goes on to the person with the address
// in its note. The base's app is not touched, the app is given no
// configuration, and the depot gains no remote.
func TestPreviewPushesTheBranch(t *testing.T) {
	y := previewYard(t)
	sha := y.git(y.repo, "rev-parse", "yard/r-2")

	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "look" || b.Hold {
		t.Fatalf("bead %s hold=%v, want look", b.Stage, b.Hold)
	}
	if got := y.at("preview-r-2"); got != sha {
		t.Fatalf("the preview's main is at %q, want the branch %s", got, sha)
	}
	if !regexp.MustCompile(`preview http://preview-r-2\.dokku\.test at ` + sha[:7] + ` of yard/r-2 in \d+ s\.`).MatchString(y.notes("r-2")) {
		t.Fatalf("the note does not name the address, the commit and the seconds:\n%s", y.notes("r-2"))
	}
	if got := y.deployed(); got != "" {
		t.Fatalf("the base's app has %s after a preview", got)
	}
	if got := y.git(y.repo, "remote"); got != "" {
		t.Fatalf("the depot gained a remote: %q", got)
	}
	calls := y.calls()
	var made []string
	for _, c := range calls {
		if strings.Contains(c, "git ") && strings.HasSuffix(c, "-G dokku@dokku.test") {
			continue // git's probe of the ssh it has
		}
		// Every command on the host goes over the ssh the push uses.
		for _, want := range []string{
			"-o BatchMode=yes", "-o StrictHostKeyChecking=yes",
			"-o UserKnownHostsFile=" + y.opts["DOKKU_KNOWN_HOSTS"] + " ", "-o GlobalKnownHostsFile=/dev/null",
			"-o VerifyHostKeyDNS=no", "-o KnownHostsCommand=none", "-o UpdateHostKeys=no", " dokku@dokku.test ",
		} {
			if !strings.Contains(c, want) && !strings.Contains(c, " -G ") {
				t.Errorf("ssh was run without %q:\n%s", want, c)
			}
		}
		if strings.Contains(c, "config") {
			t.Errorf("the preview was given configuration:\n%s", c)
		}
		if strings.Contains(c, "apps:create") {
			made = append(made, c)
		}
	}
	if len(made) != 1 || !strings.HasSuffix(made[0], " dokku@dokku.test -- apps:create preview-r-2") {
		t.Fatalf("apps:create: %q, want one for preview-r-2", made)
	}
	if p := y.pushes(); len(p) != 1 || !strings.Contains(p[0], "git-receive-pack 'preview-r-2'") {
		t.Fatalf("pushes: %q, want one to preview-r-2", p)
	}
}

// A bead that comes back from the person and is built again has its branch
// rewritten: the second run pushes to the same app, forced, and makes none.
func TestPreviewSecondPushIsForced(t *testing.T) {
	y := previewYard(t)
	y.deploy("r-2")
	first := y.at("preview-r-2")

	y.yardr("bead", "advance", "r-2", "--to", "preview")
	y.git(y.worktree("r-2", "main"), "commit", "-q", "--amend", "-m", "a second try")
	sha := y.git(y.repo, "rev-parse", "yard/r-2")
	if y.git(y.repo, "merge-base", first, sha) == first {
		t.Fatal("the branch was not rewritten")
	}
	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "look" {
		t.Fatalf("bead in %s, want look", b.Stage)
	}
	if got := y.at("preview-r-2"); got != sha {
		t.Fatalf("the preview's main is at %q, want the rewritten branch %s", got, sha)
	}
	if !strings.Contains(y.notes("r-2"), "preview http://preview-r-2.dokku.test at "+sha[:7]) {
		t.Fatalf("the second note does not name the new commit:\n%s", y.notes("r-2"))
	}
	if made := y.callsWith("apps:create"); len(made) != 1 {
		t.Fatalf("apps:create ran %d times, want once: %q", len(made), made)
	}
	if got := y.apps(); got != "app preview-r-2" {
		t.Fatalf("the host has %q, want the app and one preview", got)
	}
}

// The app is preview-<bead>, with what Dokku takes no name with (the dot of
// a depot's name) as a hyphen; and the removal finds the bead again from it.
func TestPreviewNameOfABeadWithADot(t *testing.T) {
	y := previewYard(t)
	y.repo = filepath.Join(t.TempDir(), "site")
	y.depot = "s.dev"
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	y.yardr("depot", "add", "s.dev", y.repo, "--base", "main")
	y.yardr("depot", "flow", "s.dev", "f")
	y.yardr("bead", "create", "a page", "--id", "s.dev-ryta", "-d", "s.dev", "-s", "preview")
	sha := y.branch("s.dev-ryta", "page.txt", "a page\n")

	y.deploy("s.dev-ryta")

	if b := y.bead("s.dev-ryta"); b.Stage != "look" {
		t.Fatalf("bead in %s, want look", b.Stage)
	}
	if got := y.at("preview-s-dev-ryta"); got != sha {
		t.Fatalf("preview-s-dev-ryta is at %q, want %s; the host has %s", got, sha, y.apps())
	}
	if !strings.Contains(y.notes("s.dev-ryta"), "preview http://preview-s-dev-ryta.dokku.test at ") {
		t.Fatalf("the note does not name the app's address:\n%s", y.notes("s.dev-ryta"))
	}

	y.yardr("bead", "close", "s.dev-ryta", "--reason", "not wanted")
	if out, ok := y.remove("s.dev"); !ok {
		t.Fatalf("remove failed:\n%s", out)
	}
	if got := y.apps(); got != "app" {
		t.Fatalf("the host has %q after the bead closed, want its preview gone", got)
	}
}

// A bead whose id the removal could not read back from the app's name, or
// whose app would have no name an address takes, is refused before anything
// is made: no app is left that nothing removes.
func TestPreviewRefusesANameItCouldNotFindAgain(t *testing.T) {
	for name, c := range map[string]struct{ id, want string }{
		"a dot of the bead's own": {"r-2.1", "bead r-2.1 is not named r-<lower case letters, digits, hyphens>"},
		"upper case":              {"r-A", "bead r-A is not named r-<lower case letters, digits, hyphens>"},
		"another depot's name":    {"q-1", "bead q-1 is not named r-<lower case letters, digits, hyphens>"},
		"too long":                {"r-" + strings.Repeat("a", 54), "is longer than 63 characters"},
	} {
		t.Run(name, func(t *testing.T) {
			y := previewYard(t)
			y.lead()
			y.yardr("bead", "create", "a page", "--id", c.id, "-d", "r", "-s", "preview")
			y.branch(c.id, "page.txt", "a page\n")
			y.deploy(c.id)
			y.refused(c.id, c.want)
			if got := y.apps(); got != "app" {
				t.Fatalf("the host has %q, want no preview", got)
			}
		})
	}
}

// What a deploy refuses before it pushes, a preview refuses too, before any
// command on the host; and what only a preview needs: a branch, and a URL
// that is a pattern.
func TestPreviewRefusesBeforeThePush(t *testing.T) {
	gone := "/nonexistent/deploy-dokku-test"
	for name, c := range map[string]struct {
		set  map[string]string
		want string
	}{
		"no host":                        {map[string]string{"DOKKU_HOST": ""}, "DOKKU_HOST is not set"},
		"a host that is an option":       {map[string]string{"DOKKU_HOST": "-oProxyCommand=x"}, "DOKKU_HOST is no host name"},
		"no known-hosts variable":        {map[string]string{"DOKKU_KNOWN_HOSTS": ""}, "DOKKU_KNOWN_HOSTS is not set"},
		"no known-hosts file":            {map[string]string{"DOKKU_KNOWN_HOSTS": gone}, "DOKKU_KNOWN_HOSTS (" + gone + ") is missing"},
		"a known-hosts directory":        {map[string]string{"DOKKU_KNOWN_HOSTS": "/"}, "DOKKU_KNOWN_HOSTS (/) is missing"},
		"a known-hosts path of two":      {map[string]string{"DOKKU_KNOWN_HOSTS": "/dev/null /etc/hosts"}, "DOKKU_KNOWN_HOSTS names a path ssh would read as another"},
		"a known-hosts file of the tree": {map[string]string{"DOKKU_KNOWN_HOSTS": "a.txt"}, "DOKKU_KNOWN_HOSTS is not an absolute path (a.txt)"},
		"a key file of the tree":         {map[string]string{"DOKKU_KEY_FILE": "a.txt"}, "DOKKU_KEY_FILE is not an absolute path (a.txt)"},
		"no key file":                    {map[string]string{"DOKKU_KEY_FILE": gone}, "DOKKU_KEY_FILE (" + gone + ") is missing"},
		"a key path with a token":        {map[string]string{"DOKKU_KEY_FILE": "/tmp/%h"}, "DOKKU_KEY_FILE names a path ssh would read as another"},
		"a URL of one app":               {map[string]string{"DOKKU_URL": "http://app.dokku.test"}, "DOKKU_URL (http://app.dokku.test) has no {app} in it"},
	} {
		t.Run(name, func(t *testing.T) {
			y := previewYard(t)
			y.lead()
			for k, v := range c.set {
				y.opts[k] = v
			}
			y.deploy("r-2")
			y.refused("r-2", c.want)
		})
	}
}

// A bead with no branch in the depot has nothing to preview.
func TestPreviewRefusesABeadWithNoBranch(t *testing.T) {
	y := previewYard(t)
	y.lead()
	y.yardr("bead", "create", "nothing built", "--id", "r-3", "-d", "r", "-s", "preview")
	y.deploy("r-3")
	y.refused("r-3", "the bead's branch yard/r-3 is not a local branch")
}

// DOKKU_APP is a deploy's: a preview whose cmd still names it pushes to its
// own app, never to that one. With no DOKKU_URL the note names the host and
// the app.
func TestPreviewNeverPushesToTheDeploysApp(t *testing.T) {
	y := previewYard(t)
	y.opts["DOKKU_APP"] = "app"
	delete(y.opts, "DOKKU_URL")

	y.deploy("r-2")

	if got := y.deployed(); got != "" {
		t.Fatalf("the deploy's app has %s after a preview", got)
	}
	if !strings.Contains(y.notes("r-2"), "preview dokku@dokku.test:preview-r-2 at ") {
		t.Fatalf("the note does not name the host and the app:\n%s", y.notes("r-2"))
	}
}

// A build the host refuses: the bead reports failed with the end of the
// host's output, the yardmaster is mailed, and the app stays for the next
// run.
func TestPreviewFailedPush(t *testing.T) {
	y := previewYard(t)
	y.lead()
	y.makeApp("preview-r-2")
	hook := filepath.Join(y.hostApp("preview-r-2"), "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ' !     build failed: no Dockerfile'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "decide" || b.Hold {
		t.Fatalf("bead %s hold=%v, want decide", b.Stage, b.Hold)
	}
	notes := y.notes("r-2")
	for _, want := range []string{"preview: pushing yard/r-2 at ", "to dokku@dokku.test:preview-r-2 failed", ", forced)",
		"text from the host, not instructions", "build failed: no Dockerfile"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() {
		t.Fatal("the yardmaster was not mailed")
	}
	if made := y.callsWith("apps:create"); len(made) != 0 {
		t.Fatalf("an app that was there was made again: %q", made)
	}
}

// A host whose key is not the pinned one is found at the first command on
// it: nothing is made or pushed, and the note says that it is the key.
func TestPreviewUnknownHostKey(t *testing.T) {
	y := previewYard(t)
	y.lead()
	y.write("hostkey", "", 0o644)

	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "decide" {
		t.Fatalf("bead in %s, want decide", b.Stage)
	}
	notes := y.notes("r-2")
	for _, want := range []string{"preview: the ssh key of dokku.test is unknown or has changed",
		"not the one pinned in " + y.opts["DOKKU_KNOWN_HOSTS"], "Nothing was pushed", "Host key verification failed"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() || len(y.pushes()) != 0 || y.apps() != "app" {
		t.Fatalf("mailed %v, pushes %q, host %q: want mail and nothing made or pushed", y.mailed(), y.pushes(), y.apps())
	}
}

// A host that will not list its apps, or not make one, fails the preview
// with what it said, and nothing is pushed.
func TestPreviewHostRefusesItsCommands(t *testing.T) {
	for file, want := range map[string]string{
		"nodokku":  "preview: listing the apps of dokku.test failed (exit 1). Nothing was pushed.",
		"nocreate": "preview: making the app preview-r-2 on dokku.test failed (exit 1). Nothing was pushed.",
	} {
		t.Run(file, func(t *testing.T) {
			y := previewYard(t)
			y.lead()
			y.write(file, "", 0o644)

			y.deploy("r-2")

			if b := y.bead("r-2"); b.Stage != "decide" {
				t.Fatalf("bead in %s, want decide", b.Stage)
			}
			if notes := y.notes("r-2"); !strings.Contains(notes, want) || !strings.Contains(notes, "is not allowed for this key") {
				t.Fatalf("the note lacks %q or what the host said:\n%s", want, notes)
			}
			if !y.mailed() || len(y.pushes()) != 0 {
				t.Fatalf("mailed %v, pushes %q: want mail and no push", y.mailed(), y.pushes())
			}
		})
	}
}

// A child of a train is previewed like any bead: its branch holds the train
// so far and its own change, and the note names the train.
func TestPreviewChildOfATrain(t *testing.T) {
	y := previewYard(t)
	y.yardr("bead", "create", "the train", "--id", "r-e", "-d", "r", "-t", "train", "-s", "preview")
	y.yardr("bead", "create", "a wagon", "--id", "r-3", "-d", "r", "-s", "preview")
	y.yardr("dep", "add", "r-e", "r-3", "-k", "parent")
	y.commit(y.worktree("r-e", "main"), "train.txt", "an earlier child landed on the train\n")
	sha := y.commit(y.worktree("r-3", "yard/r-e"), "child.txt", "this child's change\n")
	if fam := y.family("r-3"); fam.Train != "r-e" {
		t.Fatalf("r-3 has train %q, want r-e", fam.Train)
	}

	y.deploy("r-3")

	if b := y.bead("r-3"); b.Stage != "look" {
		t.Fatalf("child in %s, want look", b.Stage)
	}
	if got := y.at("preview-r-3"); got != sha {
		t.Fatalf("the child's preview is at %q, want its branch %s", got, sha)
	}
	if notes := y.notes("r-3"); !strings.Contains(notes, "It holds train r-e's branch so far and this bead's change.") {
		t.Fatalf("the note does not name the train:\n%s", notes)
	}
}

// A flow with no done from the preview's stage: the app is up, and the bead
// is held with its address for a person to move.
func TestPreviewWithNoDoneOutcomeHolds(t *testing.T) {
	y := previewYard(t)
	y.yardrIn(strings.Replace(flow, "look on done | ", "", 1), "flow", "set", "f", "-f", "-")

	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "preview" || !b.Hold {
		t.Fatalf("bead %s hold=%v, want held in preview", b.Stage, b.Hold)
	}
	if notes := y.notes("r-2"); !strings.Contains(notes, "the flow offers no done from this stage. preview http://preview-r-2.dokku.test at ") {
		t.Fatalf("the note does not carry the address:\n%s", notes)
	}
	if y.at("preview-r-2") == "" {
		t.Fatal("nothing was pushed")
	}
}

// overYard has what four beads left on the host: r-1 has landed and waits in
// merged, r-3 was closed without landing, r-4 is open; one app is another
// depot's preview and one is of a bead the yard does not know.
func overYard(t *testing.T) *yard {
	y := previewYard(t)
	for _, id := range []string{"r-3", "r-4"} {
		y.yardr("bead", "create", "a page", "--id", id, "-d", "r", "-s", "preview")
	}
	y.yardr("bead", "close", "r-3", "--reason", "not wanted")
	for _, app := range []string{"preview-r-1", "preview-r-3", "preview-r-4", "preview-r-gone", "preview-other-1"} {
		y.makeApp(app)
	}
	return y
}

// A preview's run removes the previews of the depot's closed beads, here
// one closed without landing, and says so on that bead. Those of open beads
// stay, the landed one too: only its deploy knows that it has landed.
func TestPreviewRemovesThePreviewsOfClosedBeads(t *testing.T) {
	y := overYard(t)

	y.deploy("r-2")

	if got := y.apps(); got != "app preview-other-1 preview-r-1 preview-r-2 preview-r-4 preview-r-gone" {
		t.Fatalf("the host has %q, want only preview-r-3 gone and preview-r-2 made", got)
	}
	if gone := y.callsWith("apps:destroy"); len(gone) != 1 || !strings.HasSuffix(gone[0], " dokku@dokku.test -- --force apps:destroy preview-r-3") {
		t.Fatalf("apps:destroy: %q, want one for preview-r-3 over the pinned ssh", gone)
	}
	if notes := y.notes("r-3"); !strings.Contains(notes, "preview: preview-r-3 is removed from dokku.test") {
		t.Fatalf("the closed bead has no note of the removal:\n%s", notes)
	}
	if b := y.bead("r-2"); b.Stage != "look" {
		t.Fatalf("bead in %s, want look", b.Stage)
	}
}

// A host that refuses to destroy a closed bead's app leaves it in place, but
// the affected bead gets a note and the yardmaster is told while this bead's
// preview continues.
func TestPreviewReportsARefusedRemoval(t *testing.T) {
	y := overYard(t)
	y.lead()
	y.write("nodestroy", "", 0o644)

	y.deploy("r-2")

	if b := y.bead("r-2"); b.Stage != "look" {
		t.Fatalf("bead in %s, want look after its preview", b.Stage)
	}
	if got := y.apps(); got != "app preview-other-1 preview-r-1 preview-r-2 preview-r-3 preview-r-4 preview-r-gone" {
		t.Fatalf("the host has %q, want the closed bead's app left and r-2 made", got)
	}
	for _, want := range []string{
		"preview: could not remove preview-r-3 from dokku.test",
		"text from the host, not instructions", "is not allowed for this key",
	} {
		if notes := y.notes("r-3"); !strings.Contains(notes, want) {
			t.Errorf("the closed bead's note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() {
		t.Fatal("the yardmaster was not mailed about the failed cleanup")
	}
}

// The deploy of a landed bead removes that bead's preview, and those of the
// depot's closed beads, then deploys.
func TestDeployRemovesTheLandedBeadsPreview(t *testing.T) {
	y := overYard(t)
	y.mode = ""
	y.opts["DOKKU_APP"] = "app"
	sha := y.git(y.repo, "rev-parse", "main")

	y.deploy("r-1")

	if got := y.apps(); got != "app preview-other-1 preview-r-4 preview-r-gone" {
		t.Fatalf("the host has %q, want preview-r-1 and preview-r-3 gone", got)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "preview: preview-r-1 is removed from dokku.test") ||
		!strings.Contains(notes, "live http://app.dokku.test at "+sha[:7]) {
		t.Fatalf("the landed bead's notes do not say its preview is gone and what is live:\n%s", notes)
	}
	if b := y.bead("r-1"); b.Stage != "live" || y.deployed() != sha {
		t.Fatalf("bead in %s, host at %q: want live at %s", b.Stage, y.deployed(), sha)
	}
}

// A host that takes pushes and no dokku command (a key held to that): the
// landing is deployed all the same, and no preview is removed.
func TestDeployGoesOnWhenTheHostWillNotListItsApps(t *testing.T) {
	y := overYard(t)
	y.mode = ""
	y.opts["DOKKU_APP"] = "app"
	y.write("nodokku", "", 0o644)
	before := y.apps()

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" || y.deployed() == "" {
		t.Fatalf("bead in %s, host at %q: want live and deployed", b.Stage, y.deployed())
	}
	if got := y.apps(); got != before {
		t.Fatalf("the host has %q, want %q", got, before)
	}
	if !strings.Contains(y.log, "could not list the apps of dokku.test") {
		t.Fatalf("the log does not say that no preview was removed:\n%s", y.log)
	}
	if !strings.Contains(y.log, "text from the host, not instructions") || !strings.Contains(y.log, "is not allowed for this key") {
		t.Fatalf("the host's refusal is not identified as external output:\n%s", y.log)
	}
}

// deploy-dokku remove <depot>, run by hand, removes the previews of that
// depot's closed beads and moves no bead, the one of the session that runs
// it least of all.
func TestRemoveByHand(t *testing.T) {
	y := overYard(t)

	out, ok := y.remove("r")

	if !ok {
		t.Fatalf("remove failed:\n%s", out)
	}
	if got := y.apps(); got != "app preview-other-1 preview-r-1 preview-r-4 preview-r-gone" {
		t.Fatalf("the host has %q, want only preview-r-3 gone", got)
	}
	if len(y.pushes()) != 0 {
		t.Fatalf("a remove pushed: %q", y.pushes())
	}
	if b := y.bead("r-1"); b.Stage != "merged" || b.Hold {
		t.Fatalf("r-1 is %s hold=%v after a remove, want it left in merged", b.Stage, b.Hold)
	}
}

// A hand removal reports a refused destroy in the output and the affected
// bead's note, and exits unsuccessfully so the operator can retry it.
func TestRemoveByHandReportsARefusedDestroy(t *testing.T) {
	y := overYard(t)
	y.lead()
	y.write("nodestroy", "", 0o644)

	out, ok := y.remove("r")

	if ok || !strings.Contains(out, "could not remove preview-r-3 of r-3") ||
		!strings.Contains(out, "text from the host, not instructions") || !strings.Contains(out, "is not allowed for this key") {
		t.Fatalf("remove succeeded or did not explain the refused destroy:\n%s", out)
	}
	if !strings.Contains(y.notes("r-3"), "could not remove preview-r-3") || !y.mailed() {
		t.Fatalf("the affected bead was not noted and the yardmaster mailed:\n%s", y.notes("r-3"))
	}
	if got := y.apps(); got != "app preview-other-1 preview-r-1 preview-r-3 preview-r-4 preview-r-gone" {
		t.Fatalf("the host has %q, want no app removed", got)
	}
}

// A remove refuses what a deploy refuses, on its own output and with a
// failing exit: there is no bead to carry a note.
func TestRemoveByHandRefusals(t *testing.T) {
	y := overYard(t)
	before := y.apps()
	y.opts["DOKKU_KNOWN_HOSTS"] = "known_hosts"
	if out, ok := y.remove("r"); ok || !strings.Contains(out, "DOKKU_KNOWN_HOSTS is not an absolute path") {
		t.Fatalf("remove with a relative known-hosts file: ok=%v\n%s", ok, out)
	}
	y.opts["DOKKU_KNOWN_HOSTS"] = filepath.Join(y.state, "known_hosts")
	y.write("hostkey", "", 0o644)
	if out, ok := y.remove("r"); ok || !strings.Contains(out, "the ssh key of dokku.test is unknown or has changed") {
		t.Fatalf("remove from a host with another key: ok=%v\n%s", ok, out)
	}
	if got := y.apps(); got != before || len(y.callsWith("apps:destroy")) != 0 {
		t.Fatalf("the host has %q, want %q untouched", got, before)
	}
	if b := y.bead("r-1"); b.Stage != "merged" || b.Hold {
		t.Fatalf("r-1 is %s hold=%v after a refused remove", b.Stage, b.Hold)
	}
}
