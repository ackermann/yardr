package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Applying the github pack folds what the yard recorded under
// github-declared into github: hooks, identity, grants, subscriptions,
// participation and links. Running it twice changes nothing.
func TestPackApplyGithubFoldsGithubDeclared(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	if _, err := a.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	id := store.Identity{ID: "u-yardmaster", Key: "yardmaster"}
	if err := a.st.SetIdentity(ctx, "yardmaster", "github", id, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetIdentity(ctx, "yardmaster", "github-declared", id, "me"); err != nil {
		t.Fatal(err)
	}
	r, _ := a.st.GetCrew(ctx, "yardmaster")
	r.Config[store.OpenThreads] = "github,github-declared"
	r.Config[store.SetState] = "github-declared"
	if _, err := a.st.PutCrew(ctx, *r, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/s"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.PutHook(ctx, store.Hook{Name: "gh-declared", Source: "github-declared", SecretFile: "/s"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "github-declared", Scope: store.ScopeInvolved}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.FollowThread(ctx, "yardmaster", "github-declared", "acme/yardr#23", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateBead(ctx, store.NewBead{ID: "web-1", Title: "one", Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddLink(ctx, "web-1", store.NewLink{Kind: store.LinkTracks, Source: "github-declared", URL: "https://github.com/acme/yardr/issues/23"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "apply", "github", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source github is declared") {
		t.Errorf("the source was not added:\n%s", out)
	}
	if !strings.Contains(out, "folded github-declared into github: 1 link, 1 hook, 1 identity, 1 grant, 1 subscription, 1 thread") {
		t.Errorf("the fold is not said:\n%s", out)
	}
	if h, _ := a.st.GetHook(ctx, "gh-declared"); h.Source != "github" {
		t.Errorf("hook source = %q", h.Source)
	}
	if r, _ := a.st.GetCrew(ctx, "yardmaster"); len(r.Identities) != 1 || r.Identities["github"] != id {
		t.Errorf("identities = %+v", r.Identities)
	} else if got := strings.Join(r.MayOpen(), ","); got != "github" {
		t.Errorf("open_threads = %q", got)
	} else if got := strings.Join(r.MaySetState(), ","); got != "github" {
		t.Errorf("set_state = %q", got)
	}
	if ls, _ := a.st.Links(ctx, "web-1"); len(ls) != 1 || ls[0].Source != "github" {
		t.Errorf("links = %+v", ls)
	}
	// Twice changes nothing: the source is there, and nothing is under
	// the old name.
	out, err = run(t, a.packCmd(), "apply", "github", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "folded github-declared into github") {
		t.Errorf("second apply folded again:\n%s", out)
	}
}

// A yard with no github-declared records: the apply adds the source and
// migrates nothing.
func TestPackApplyGithubWithNoRecordsMigratesNothing(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	out, err := run(t, a.packCmd(), "apply", "github", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source github is declared") {
		t.Errorf("the source was not added:\n%s", out)
	}
	if strings.Contains(out, "folded github-declared into github") {
		t.Errorf("nothing was under the old name, yet:\n%s", out)
	}
}
