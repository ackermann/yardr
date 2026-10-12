package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The daemon reads an idle agent's pane once per pass: two or three reads in
// a row make herdr scroll the pane a client shows. A pass over an idle crew
// member with nothing due, and one over an idle bead session, each take at
// most one snapshot of the pane, whatever checks read from it (yardr-2zst).
func TestIdlePassReadsEachPaneOnce(t *testing.T) {
	t.Run("crew", func(t *testing.T) {
		h, calls := droppingHerdr(t, 0)
		ctx := context.Background()
		s := storetest.New(t)
		d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil {
			t.Fatalf("crew member = %+v %v", r, err)
		}
		pane, _ := r.Handle["pane"].(string)
		before := strings.Count(calls(), "pane read "+pane)
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(calls(), "pane read "+pane) - before; got > 1 {
			t.Errorf("one pass over an idle crew member read its pane %d times, want at most 1:\n%s", got, calls())
		}
	})

	t.Run("bead", func(t *testing.T) {
		ctx := context.Background()
		repo := t.TempDir()
		gitT(t, repo, "init", "-q", "-b", "main")
		gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
		s := storetest.New(t)
		mustT(t, s.PutDepot(ctx, store.Depot{Name: "r", Path: repo, Base: "main"}, "me"))
		mustT(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"))
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
		mustT(t, err)
		b, err := s.CreateBead(ctx, store.NewBead{Title: "b0", Stage: "new", Depot: "r"}, "me")
		mustT(t, err)
		dir := t.TempDir()
		log := filepath.Join(dir, "calls")
		h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: beadPassHerdr(t, dir), WorktreeRoot: t.TempDir(), Store: s}
		d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}}
		for range 3 {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		sess, err := s.Sessions(ctx, false)
		if err != nil || len(sess) != 1 || sess[0].Bead != b.ID || sess[0].Handle[store.PendingPrompt] != nil {
			t.Fatalf("sessions = %+v %v", sess, err)
		}
		pane, _ := sess[0].Handle["pane"].(string)
		raw, _ := os.ReadFile(log)
		before := strings.Count(string(raw), "pane read "+pane)
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		raw, _ = os.ReadFile(log)
		if got := strings.Count(string(raw), "pane read "+pane) - before; got > 1 {
			t.Errorf("one pass over an idle bead session read its pane %d times, want at most 1:\n%s", got, raw)
		}
	})
}

// A styled snapshot holds the plain text once its SGR escapes are stripped,
// which is what serves the plain readers of a pass from the one snapshot.
// Only SGR goes; every other byte stays as herdr printed it.
func TestStripSGR(t *testing.T) {
	for _, c := range []struct{ styled, plain string }{
		{"────\n❯ \n────", "────\n❯ \n────"},
		{"a\r\nb\r\n", "a\r\nb\r\n"},
		{"❯ \x1b[2mTry this\x1b[0m", "❯ Try this"},
		{"\x1b[2mdim\x1b[22m plain", "dim plain"},
		{"\x1b[38;2;255;0;0mred\x1b[0m", "red"},
		{"\x1b[mreset", "reset"},
		{"\x1b[2Knot SGR, kept", "\x1b[2Knot SGR, kept"},
		{"a\x1bb", "a\x1bb"},
		{"a\x1b[2", "a\x1b[2"},
	} {
		if got := stripSGR(c.styled); got != c.plain {
			t.Errorf("stripSGR(%q) = %q, want %q", c.styled, got, c.plain)
		}
	}
}

// A shorter read from a snapshot is its end; a snapshot with fewer lines
// is read whole.
func TestSnapLines(t *testing.T) {
	for _, c := range []struct {
		snap string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb", 1, "b"},
		{"a\n", 5, "a"},
		{"", 200, ""},
	} {
		if got := snapLines(c.snap, c.n); got != c.want {
			t.Errorf("snapLines(%q, %d) = %q, want %q", c.snap, c.n, got, c.want)
		}
	}
}

// beadPassHerdr is a fake herdr for a pass over a bead session: worktree
// create really makes the worktree, as herdr does; a prompt shows in the
// pane and the agent works on it; the agent is idle from then on. Every
// call is logged to dir/calls.
func beadPassHerdr(t *testing.T, dir string) string {
	t.Helper()
	return deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "worktree create")
    shift 2
    while [ $# -gt 1 ]; do
      case "$1" in --cwd) cwd=$2 ;; --branch) br=$2 ;; --base) base=$2 ;; --path) p=$2 ;; esac
      shift 2
    done
    git -C "$cwd" worktree add -q -b "$br" "$p" "$base" >&2 || exit 1
    echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"'"$p"'"}}}' ;;
  "agent prompt")
    echo "❯ $4" >> pane
    answer working ;;
  "pane read") echo "────"; cat pane 2>/dev/null; echo "❯ " ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
}
