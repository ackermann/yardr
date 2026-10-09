package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// prime --bead says what the bead's session was put on when that is the
// head of the pull request the bead tracks: the commit and the pull request,
// read from the session's handle as the runner left it. A session on the
// bead's own branch has no such line.
func TestPrimeSaysThePullRequestHeadTheWorktreeIsOn(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	workYard(t, st, depot.Name, []store.Group{{Name: "builders", Runner: "herdr", Limit: 1}}, "builders")
	ss, err := st.Sessions(t.Context(), false)
	if err != nil || len(ss) != 1 {
		t.Fatalf("sessions = %d, %v, want 1", len(ss), err)
	}
	prime := func(asJSON bool) string {
		t.Helper()
		out, err := run(t, (&app{st: st, actor: "me", json: asJSON}).primeCmd(), "--bead", ss[0].Bead)
		if err != nil {
			t.Fatalf("prime: %v\n%s", err, out)
		}
		return out
	}
	bead := prime(false)
	if strings.Contains(bead, "WORKTREE") {
		t.Fatalf("prime of a bead on its own branch:\n%s", bead)
	}

	const url, head = "https://github.com/acme/web/pull/1", "0123456789abcdef0123456789abcdef01234567"
	if err := st.SetHandle(t.Context(), ss[0].ID, map[string]any{"pull": url, "pull_head": head}); err != nil {
		t.Fatal(err)
	}
	bead = prime(false)
	if want := "\nWORKTREE: the head of the pull request " + url + " at " + head + ", detached"; !strings.Contains(bead, want) {
		t.Fatalf("prime:\n%s\nwant it to hold:%s", bead, want)
	}
	bead = prime(true)
	var v struct{ Pull struct{ URL, Head string } }
	if json.Unmarshal([]byte(bead), &v) != nil || v.Pull.URL != url || v.Pull.Head != head {
		t.Fatalf("prime --json:\n%s", bead)
	}
}
