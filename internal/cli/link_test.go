package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// linkYard is a yard with train yard-e and beads yard-1 and yard-2.
func linkYard(t *testing.T) (*app, store.API, *store.Store) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "train", Type: store.TypeTrain, Depot: "yard"},
		{ID: "yard-1", Title: "one", Depot: "yard"},
		{ID: "yard-2", Title: "two", Depot: "yard"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return &app{st: st, actor: "me"}, st, own
}

// link runs yardr link with args on a fresh command, flags and all.
func link(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	return run(t, a.linkCmd(), args...)
}

// The trial of the bead, as commands: a URL with its title slug and the
// same issue without it are one issue; a second tracker is refused, naming
// the first; link move hands the issue to a train; a bead tracks one Linear
// and one GitHub issue, never two of one.
func TestLinkCommands(t *testing.T) {
	a, st, _ := linkYard(t)
	ctx := context.Background()
	const lin, gh = "https://linear.app/acme/issue/YRD-1", "https://github.com/acme/webapp/issues/7"

	out, err := link(t, a, "add", "yard-1", lin+"/web-view-shows-links")
	if want := "yard-1 -tracks-> " + lin + " (linear)\n"; err != nil || out != want {
		t.Fatalf("link add: %q, %v; want %q", out, err, want)
	}
	_, err = link(t, a, "add", "yard-2", lin)
	if err == nil || !strings.Contains(err.Error(), lin+" is tracked by yard-1") ||
		!strings.Contains(err.Error(), "yardr link move "+lin+" --to yard-2") || !strings.Contains(err.Error(), "--relates") {
		t.Fatalf("second tracker: %v", err)
	}
	out, err = link(t, a, "add", "yard-2", lin+"/renamed-since?x=1", "--relates")
	if want := "yard-2 -relates-> " + lin + " (linear)\n"; err != nil || out != want {
		t.Fatalf("link add --relates: %q, %v", out, err)
	}

	out, err = link(t, a, "move", lin+"/web-view", "--to", "yard-e")
	if want := "yard-e -tracks-> " + lin + " (linear), moved from yard-1, which keeps a relates link\n"; err != nil || out != want {
		t.Fatalf("link move: %q, %v; want %q", out, err, want)
	}
	if _, err := link(t, a, "move", lin); err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("link move without --to: %v", err)
	}
	out, err = link(t, a, "list", "--issue", lin)
	// The tracker first, then oldest first: yard-1's relates link is the move's.
	want := "yard-e  tracks  " + lin + " (linear)\nyard-2  relates " + lin + " (linear)\nyard-1  relates " + lin + " (linear)\n"
	if err != nil || out != want {
		t.Fatalf("link list --issue:\n%s%v\nwant\n%s", out, err, want)
	}

	// One Linear and one GitHub issue; not a second Linear one.
	if _, err := link(t, a, "add", "yard-e", gh+"#issuecomment-3"); err != nil {
		t.Fatal(err)
	}
	_, err = link(t, a, "add", "yard-e", "https://linear.app/acme/issue/YRD-2")
	if err == nil || !strings.Contains(err.Error(), "yard-e already tracks "+lin) {
		t.Fatalf("second linear tracks: %v", err)
	}
	out, err = link(t, a, "list", "yard-e")
	if want := "tracks  " + lin + " (linear)\ntracks  " + gh + " (github)\n"; err != nil || out != want {
		t.Fatalf("link list: %q, %v", out, err)
	}
	if _, err := link(t, a, "list"); err == nil {
		t.Fatal("link list with neither a bead nor --issue ran")
	}
	if _, err := link(t, a, "list", "yard-e", "--issue", lin); err == nil {
		t.Fatal("link list with a bead and --issue ran")
	}

	// The other kind replaces the link, and says so.
	out, err = link(t, a, "add", "yard-e", gh, "--relates")
	if want := "yard-e -relates-> " + gh + " (github), replacing its tracks link\n"; err != nil || out != want {
		t.Fatalf("replace: %q, %v", out, err)
	}

	// What is no issue is refused before the store is asked.
	head, _ := st.Head(ctx)
	for arg, why := range map[string]string{
		"https://github.com/acme/webapp/pull/7": "is a pull request, not an issue",
		"https://gitlab.com/acme/x/-/issues/1":  "where yardr knows no issues",
		"YRD-1":                                 "is not the URL of an issue",
	} {
		for _, args := range [][]string{{"add", "yard-2", arg}, {"rm", "yard-2", arg}, {"move", arg, "--to", "yard-2"}, {"list", "--issue", arg}} {
			if _, err := link(t, a, args...); err == nil || !strings.Contains(err.Error(), why) {
				t.Fatalf("link %v: %v; want %q", args, err, why)
			}
		}
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatal("a refused URL logged an event")
	}

	// rm takes the issue in any form of its URL; a link not there is said.
	if _, err := link(t, a, "rm", "yard-2", lin+"/any-slug"); err != nil {
		t.Fatal(err)
	}
	if _, err := link(t, a, "rm", "yard-2", lin); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "yard-2 has no link to "+lin) {
		t.Fatalf("rm of no link: %v", err)
	}
	if ls, _ := st.Links(ctx, "yard-2"); len(ls) != 0 {
		t.Fatalf("links after rm = %+v", ls)
	}
}

// An issue is also named by the seq of a delivery from its thread, which
// gives the link the thread's id; a link made from the URL is the same
// link. A delivery that is no issue's (a pull request, a token source's)
// links nothing.
func TestLinkByDelivery(t *testing.T) {
	a, st, own := linkYard(t)
	ctx := context.Background()
	const lin = "https://linear.app/acme/issue/YRD-1"
	record := func(env hooks.Envelope) string {
		t.Helper()
		seq, _, err := own.RecordHook(ctx, env.Source, env.Map(), []byte("{}"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(seq, 10)
	}
	comment := record(hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
		Thread: hooks.Thread{ID: "issue-1", Title: "Web view", URL: lin + "/web-view"}, Message: "c1", Text: "anyone?"})
	out, err := link(t, a, "add", "yard-1", comment)
	if want := "yard-1 -tracks-> " + lin + " (linear)\n"; err != nil || out != want {
		t.Fatalf("link add <seq>: %q, %v", out, err)
	}
	ls, _ := st.Links(ctx, "yard-1")
	if len(ls) != 1 || ls[0].Thread != "issue-1" || ls[0].URL != lin {
		t.Fatalf("links = %+v", ls)
	}
	// The delivery's thread and the URL are matched the same way.
	if _, err := link(t, a, "add", "yard-2", lin+"/another-title"); err == nil || !strings.Contains(err.Error(), "is tracked by yard-1") {
		t.Fatalf("second tracker by URL: %v", err)
	}
	if out, err := link(t, a, "list", "--issue", comment); err != nil || out != "yard-1  tracks  "+lin+" (linear)\n" {
		t.Fatalf("link list --issue <seq>: %q, %v", out, err)
	}
	if _, err := link(t, a, "move", comment, "--to", "yard-e"); err != nil {
		t.Fatal(err)
	}
	if _, err := link(t, a, "rm", "yard-1", comment); err != nil {
		t.Fatal(err)
	}

	pr := record(hooks.Envelope{Source: "github", Type: "pull_request", Action: "opened",
		Thread: hooks.Thread{ID: "acme/webapp#9", URL: "https://github.com/acme/webapp/pull/9"}})
	if _, err := link(t, a, "add", "yard-2", pr); err == nil || !strings.Contains(err.Error(), "is a pull request, not an issue") {
		t.Fatalf("link add <seq of a pull request>: %v", err)
	}
	cron := record(hooks.Envelope{Source: "token", Type: "backup", Thread: hooks.Thread{ID: "nightly", URL: "https://ci.example/runs/1"}})
	if _, err := link(t, a, "add", "yard-2", cron); err == nil || !strings.Contains(err.Error(), "a bead cannot link to an issue in token") {
		t.Fatalf("link add <seq of a token delivery>: %v", err)
	}
	bare := record(hooks.Envelope{Source: "linear", Type: "issue", Action: "update", Thread: hooks.Thread{ID: "issue-2"}})
	if _, err := link(t, a, "add", "yard-2", bare); err == nil || !strings.Contains(err.Error(), "names no issue") {
		t.Fatalf("link add <seq with no thread URL>: %v", err)
	}
	// An event that is no delivery, and a seq with no event.
	if _, err := link(t, a, "add", "yard-2", "1"); err == nil || !strings.Contains(err.Error(), "not a hook delivery") {
		t.Fatalf("link add <seq of another event>: %v", err)
	}
	if _, err := link(t, a, "add", "yard-2", "99999"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link add <seq of no event>: %v", err)
	}
	if ls, _ := st.Links(ctx, "yard-2"); len(ls) != 0 {
		t.Fatalf("a refused delivery linked: %+v", ls)
	}
}

// bead create --tracks and --relates link the bead in the write that
// creates it, so a refused link creates no bead; bead show lists the links
// with kind, source and URL, and --json has them as links.
func TestBeadCreateAndShowLinks(t *testing.T) {
	a, st, _ := linkYard(t)
	ctx := context.Background()
	const lin, gh = "https://linear.app/acme/issue/YRD-1", "https://github.com/acme/webapp/issues/7"

	out, err := run(t, a.beadCmd(), "create", "three", "--depot", "yard", "--id", "yard-3", "-t", "train",
		"--tracks", lin+"/web-view", "--tracks", gh, "--relates", "https://linear.app/acme/issue/YRD-2")
	if err != nil || out != "yard-3\n" {
		t.Fatalf("create: %q, %v", out, err)
	}
	out, err = run(t, a.beadCmd(), "show", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"  tracks  " + lin + " (linear)\n", "  tracks  " + gh + " (github)\n",
		"  relates https://linear.app/acme/issue/YRD-2 (linear)\n"} {
		if !strings.Contains(out, line) {
			t.Fatalf("bead show lacks %q:\n%s", line, out)
		}
	}
	a.json = true
	out, err = run(t, a.beadCmd(), "show", "yard-3")
	a.json = false
	var shown struct {
		ID       string       `json:"id"`
		Revision string       `json:"revision"`
		Links    []store.Link `json:"links"`
	}
	if err != nil || json.Unmarshal([]byte(out), &shown) != nil {
		t.Fatalf("show --json: %v\n%s", err, out)
	}
	if shown.ID != "yard-3" || shown.Revision == "" || len(shown.Links) != 3 ||
		shown.Links[0].Kind != "tracks" || shown.Links[0].Source != "linear" || shown.Links[0].URL != lin || shown.Links[0].Bead != "yard-3" {
		t.Fatalf("show --json = %+v", shown)
	}
	// A bead with no link has an empty list, not null.
	a.json = true
	out, _ = run(t, a.beadCmd(), "show", "yard-1")
	a.json = false
	if !strings.Contains(out, `"links": []`) {
		t.Fatalf("show --json of an unlinked bead:\n%s", out)
	}

	before, _ := st.ListBeads(ctx, store.BeadFilter{All: true})
	for args, why := range map[string]string{
		"--tracks " + lin: lin + " is tracked by yard-3",
		"--tracks https://github.com/acme/webapp/pull/7": "is a pull request, not an issue",
		"--relates nonsense":                             "is not the URL of an issue",
		"--tracks https://linear.app/acme/issue/YRD-8 --tracks https://linear.app/acme/issue/YRD-9": "already tracks https://linear.app/acme/issue/YRD-8",
	} {
		_, err := run(t, a.beadCmd(), append([]string{"create", "four", "--depot", "yard"}, strings.Fields(args)...)...)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("create %s: %v; want %q", args, err, why)
		}
	}
	if after, _ := st.ListBeads(ctx, store.BeadFilter{All: true}); len(after) != len(before) {
		t.Fatalf("a refused link left a bead: %d beads, were %d", len(after), len(before))
	}
}
