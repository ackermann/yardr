package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestWatchAppFoldsABeadIdToAPreviewName(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"yardr-e385":                    "yardr-e385",
		"yardr.dev-02ye":                "yardr-dev-02ye",
		"Yardr_Hosted--x9wd":            "yardr-hosted-x9wd",
		"-odd.-":                        "odd",
		"a-very-long-depot-prefix-ab12": "a-very-long-depot-prefix-ab12",
		"a-really-very-long-depot-prefix-name-ab12": "ry-long-depot-prefix-name-ab12",
	} {
		got := store.WatchApp(id)
		if got != want {
			t.Errorf("WatchApp(%q) = %q, want %q", id, got, want)
		}
		if err := store.CheckPreviewApp(got); err != nil {
			t.Errorf("WatchApp(%q) = %q, which no preview may be named: %v", id, got, err)
		}
	}
}

// watchYard is a yard whose operator is jo, with a bead the builders work
// (originator kim) whose herdr session recorded a worktree holding a site,
// a mockups directory, a dot directory and links, and the session's pane.
type watchYard struct {
	s    *store.Store
	bead *store.Bead
	sess *store.Session
	wt   string // the worktree, links resolved
	link string // the path the session recorded for it
}

func newWatchYard(t *testing.T) *watchYard {
	t.Helper()
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	for _, u := range []string{"jo", "kim", "lee"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yardr.dev", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yardr.dev", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "site", Depot: "yardr.dev", Stage: "new", For: "kim"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// The worktree is reached through a link, as macOS's temporary
	// directory is, so the directory a watch records is the resolved one.
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.Symlink(real, wt); err != nil {
		t.Fatal(err)
	}
	for dir, files := range map[string][]string{
		".":              {"index.html"},
		"mockups":        {"home.html"},
		".git":           {"config"},
		"mockups/.draft": {"x.html"},
	} {
		if err := os.MkdirAll(filepath.Join(real, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(real, dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	outside := t.TempDir()
	for name, target := range map[string]string{"out": outside, "in": filepath.Join(real, "mockups"), "hidden": filepath.Join(real, ".git")} {
		if err := os.Symlink(target, filepath.Join(real, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetHandle(ctx, sess.ID, map[string]any{"worktree": wt, "agent": "yardr-a-new"}); err != nil {
		t.Fatal(err)
	}
	return &watchYard{s: s, bead: b, sess: sess, wt: real, link: wt}
}

// pane makes this test process the pane of session sess, and no other
// session's: the store tells a call's origin by its records of panes.
func (y *watchYard) pane(t *testing.T, sess *store.Session, agent string) context.Context {
	t.Helper()
	ctx := context.Background()
	me := os.Getpid()
	handle := map[string]any{"agent": agent, "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if sess.ID == y.sess.ID {
		handle["worktree"] = y.link
	}
	if err := y.s.SetHandle(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	return store.WithCaller(ctx, me)
}

func TestWatchPreviewServesADirectoryOfTheWorktree(t *testing.T) {
	t.Parallel()
	y := newWatchYard(t)
	ctx := context.Background()
	real := y.wt

	p, err := y.s.WatchPreview(ctx, y.bead.ID, "", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.App, "yardr-dev-") || p.Kind != store.PreviewKindWatch || p.Bead != y.bead.ID ||
		p.Stage != "new" || p.State != store.PreviewRunning || p.Dir != real || p.Depot != "yardr.dev" ||
		p.Proposer != "jo" || p.StartedAt == nil || p.Command != "" || p.Port != 0 || p.Approval != nil {
		t.Fatalf("watch = %+v, want a running watch of %s's stage new on %s", p, y.bead.ID, real)
	}
	if got, err := y.s.GetPreview(ctx, p.App); err != nil || got.Dir != real {
		t.Fatalf("the registry holds %+v, %v", got, err)
	}

	// A directory of the worktree, by name, by a link inside it, and by its
	// own path.
	for _, dir := range []string{"mockups", "in", filepath.Join(real, "mockups")} {
		got, err := y.s.WatchPreview(ctx, y.bead.ID, dir, "jo")
		if err != nil {
			t.Fatalf("watch of %s: %v", dir, err)
		}
		if want := filepath.Join(real, "mockups"); got.Dir != want {
			t.Errorf("watch of %s serves %s, want %s", dir, got.Dir, want)
		}
	}

	// What is not a directory of the worktree, or is reached through a dot.
	for dir, want := range map[string]string{
		"../":            "not inside its worktree",
		"out":            "not inside its worktree",
		os.TempDir():     "not inside its worktree",
		".git":           "reached through .git",
		"hidden":         "reached through .git",
		"mockups/.draft": "reached through .draft",
		"index.html":     "not a directory",
		"nowhere":        "not a directory of its worktree",
	} {
		if _, err := y.s.WatchPreview(ctx, y.bead.ID, dir, "jo"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("watch of %q = %v, want a refusal saying %q", dir, err, want)
		}
	}
	if got, _ := y.s.GetPreview(ctx, p.App); got.Dir != filepath.Join(real, "mockups") {
		t.Fatalf("a refused watch changed the one standing: %+v", got)
	}
}

func TestWatchPreviewHasToHaveAWorktreeAndAnOpenBead(t *testing.T) {
	t.Parallel()
	y := newWatchYard(t)
	ctx := context.Background()
	b2, err := y.s.CreateBead(ctx, store.NewBead{Title: "no session", Depot: "yardr.dev", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.WatchPreview(ctx, b2.ID, "", "jo"); err == nil || !strings.Contains(err.Error(), "no worktree to watch") {
		t.Fatalf("watch of a bead with no session = %v", err)
	}
	// A session that recorded no worktree, a cleaned one and one whose
	// directory is gone are no worktree either.
	sess, err := y.s.Claim(ctx, b2.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.s.SetHandle(ctx, sess.ID, map[string]any{"worktree": filepath.Join(t.TempDir(), "gone")}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.WatchPreview(ctx, b2.ID, "", "jo"); err == nil || !strings.Contains(err.Error(), "no worktree to watch") {
		t.Fatalf("watch of a bead whose worktree is gone = %v", err)
	}
	// A dir depot's bead is worked in the depot's directory itself.
	if err := y.s.SetHandle(ctx, sess.ID, map[string]any{"dir": y.wt}); err != nil {
		t.Fatal(err)
	}
	if p, err := y.s.WatchPreview(ctx, b2.ID, "mockups", "jo"); err != nil || p.Dir != filepath.Join(y.wt, "mockups") {
		t.Fatalf("watch of a dir depot's bead = %+v, %v", p, err)
	}
	if _, err := y.s.CloseBead(ctx, b2.ID, "done", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.WatchPreview(ctx, b2.ID, "", "jo"); err == nil || !strings.Contains(err.Error(), "is closed") {
		t.Fatalf("watch of a closed bead = %v", err)
	}
}

func TestWatchPreviewIsForTheBeadsAgentItsOriginatorAndAnOperator(t *testing.T) {
	t.Parallel()
	y := newWatchYard(t)
	ctx := context.Background()
	refused := "only an agent working " + y.bead.ID + ", the user it is for or the operator (jo) may watch its files"

	// Nobody who is not one of the three.
	if _, err := y.s.WatchPreview(ctx, y.bead.ID, "", "lee"); err == nil || !strings.Contains(err.Error(), refused+"; this is lee") {
		t.Fatalf("watch by lee = %v, want %q", err, refused)
	}
	if got, err := y.s.Previews(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a refused watch left %+v, %v", got, err)
	}
	// The user the bead is for.
	if _, err := y.s.WatchPreview(ctx, y.bead.ID, "", "kim"); err != nil {
		t.Fatalf("watch by the originator: %v", err)
	}

	// The pane of another bead's agent is refused, whatever actor it names,
	// and the pane of this bead's agent passes.
	b2, err := y.s.CreateBead(ctx, store.NewBead{Title: "other", Depot: "yardr.dev", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	other, err := y.s.Claim(ctx, b2.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	pane := y.pane(t, other, "yardr-b-new")
	if _, err := y.s.WatchPreview(pane, y.bead.ID, "", "jo"); err == nil || !strings.Contains(err.Error(), refused+"; this is yardr-b-new") {
		t.Fatalf("watch from another bead's pane = %v, want %q", err, refused)
	}
	if err := y.s.SetHandle(ctx, other.ID, map[string]any{"agent": "yardr-b-new"}); err != nil {
		t.Fatal(err)
	}
	pane = y.pane(t, y.sess, "yardr-a-new")
	p, err := y.s.WatchPreview(pane, y.bead.ID, "mockups", "lee")
	if err != nil {
		t.Fatalf("watch from the bead's own pane: %v", err)
	}
	if p.Proposer != "yardr-a-new" {
		t.Fatalf("a pane's watch is started by %q, want its agent", p.Proposer)
	}
	// Who started a watch may stop it, and an operator may.
	if _, err := y.s.PreviewStop(pane, p.App, "lee"); err != nil {
		t.Fatalf("stop by the agent that started the watch: %v", err)
	}
	if _, err := y.s.PreviewStop(ctx, p.App, "lee"); err == nil {
		t.Fatal("lee stopped a watch they did not start")
	}
}

func TestWatchPreviewStartsAnEndedWatchInPlace(t *testing.T) {
	t.Parallel()
	y := newWatchYard(t)
	ctx := context.Background()
	first, err := y.s.WatchPreview(ctx, y.bead.ID, "", "jo")
	if err != nil {
		t.Fatal(err)
	}
	// The edge gave it a host while it ran. An update that names none leaves
	// the host, so the one that ends the watch keeps it.
	host := "p-12.preview.example.net"
	if _, err := y.s.UpdatePreview(ctx, first.App, store.PreviewUpdate{Host: &host}); err != nil {
		t.Fatal(err)
	}
	// It ends, its last request long ago; the bead is in another stage when
	// it is watched again.
	ended := "stage left"
	long := first.StartedAt.AddDate(0, 0, -3)
	if _, err := y.s.UpdatePreview(ctx, first.App, store.PreviewUpdate{State: store.PreviewStopped, StateDetail: &ended, LastRequest: &long}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.Advance(ctx, y.bead.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	again, err := y.s.WatchPreview(ctx, y.bead.ID, "mockups", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if again.App != first.App || again.State != store.PreviewRunning || again.StateDetail != "" || again.Stage != "review" ||
		again.LastRequestAt != nil || !again.StartedAt.After(*first.StartedAt) || !strings.HasSuffix(again.Dir, "mockups") {
		t.Fatalf("watched again = %+v, want the same app running from now in review", again)
	}
	if again.Host != host {
		t.Fatalf("watched again has host %q, want the %q it had: the address stands", again.Host, host)
	}
	if all, _ := y.s.Previews(ctx); len(all) != 1 {
		t.Fatalf("the yard holds %d previews, want the one watch", len(all))
	}
	// The supervisor clears a host the edge no longer stands behind.
	none := ""
	if cleared, err := y.s.UpdatePreview(ctx, again.App, store.PreviewUpdate{Host: &none}); err != nil || cleared.Host != "" {
		t.Fatalf("clearing the host = %+v, %v", cleared, err)
	}

	// A preview of the name that is an app is not overwritten, and nothing a
	// watch is can be approved.
	if _, err := y.s.ApprovePreview(ctx, again.App, "jo"); err == nil {
		t.Fatal("a watch was approved")
	}
	if _, err := y.s.RestartPreview(ctx, again.App, "jo"); err == nil {
		t.Fatal("a watch was restarted as an app")
	}
	if _, err := y.s.PreviewStop(ctx, again.App, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.ForgetPreview(ctx, again.App, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.ProposePreview(ctx, store.NewPreview{App: again.App, Command: "serve", Dir: y.wt, Port: 8080}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.s.WatchPreview(ctx, y.bead.ID, "", "jo"); err == nil || !strings.Contains(err.Error(), "already in the yard") {
		t.Fatalf("watch under the name of an app preview = %v", err)
	}
}

func TestWatchesTakeNoneOfTheAppPreviewPlaces(t *testing.T) {
	t.Parallel()
	y := newWatchYard(t)
	ctx := context.Background()
	if _, err := y.s.WatchPreview(ctx, y.bead.ID, "", "jo"); err != nil {
		t.Fatal(err)
	}
	for i := range store.PreviewLimit {
		np := store.NewPreview{App: "app" + string(rune('a'+i)), Command: "serve", Dir: y.wt, Port: 8080 + i}
		if _, err := y.s.ProposePreview(ctx, np, "builder"); err != nil {
			t.Fatalf("app preview %d with a watch standing: %v", i+1, err)
		}
	}
	np := store.NewPreview{App: "appz", Command: "serve", Dir: y.wt, Port: 8099}
	if _, err := y.s.ProposePreview(ctx, np, "builder"); err == nil || !strings.Contains(err.Error(), "the most it may") {
		t.Fatalf("a fourth app preview = %v, want the limit", err)
	}
}
