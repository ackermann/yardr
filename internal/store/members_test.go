package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

func TestGroupMembersOnlyOnManual(t *testing.T) {
	s, ctx := flowDepot(t)
	err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Members: []string{"mckean"}}, "me")
	if err == nil || !strings.Contains(err.Error(), "only for manual groups") {
		t.Fatalf("herdr group with members err = %v", err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "humans", Runner: "manual", Members: []string{" mckean", "yardmaster", "mckean", ""}}, "me"); err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGroup(ctx, "humans")
	if err != nil || strings.Join(g.Members, ",") != "mckean,yardmaster" {
		t.Fatalf("members = %v, %v", g.Members, err)
	}
}

func TestAdvanceMembers(t *testing.T) {
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", yardFlow)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Limit: 5, Members: []string{"mckean", "yardmaster"}}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual"}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "approved", Group: "approvers"}, "me")
	must(err)

	claimed := func(stage, group string) *store.Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: stage}, "me")
		must(err)
		_, err = s.Claim(ctx, b.ID, group, stage, "yard")
		must(err)
		b, err = s.GetBead(ctx, b.ID)
		must(err)
		return b
	}

	// A non-member is refused, the error names the members, and the bead is
	// left as it was.
	b := claimed("approved", "approvers")
	_, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "yard-19-new")
	if err == nil || !strings.Contains(err.Error(), "mckean, yardmaster") || !strings.Contains(err.Error(), "approvers") {
		t.Fatalf("non-member advance err = %v", err)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Revision != b.Revision || got.Group == nil {
		t.Fatalf("refused advance changed the bead: %+v", got)
	}
	// A member may.
	if got, err := s.Advance(ctx, b.ID, "new", store.Expect{}, "yardmaster"); err != nil || got.Stage != "new" {
		t.Fatalf("member advance: %v %+v", err, got)
	}

	// A bead the routes send to the group, not yet claimed (the group at its
	// limit, say), is guarded too.
	u, err := s.CreateBead(ctx, store.NewBead{Title: "waiting", Depot: "yard", Stage: "approved"}, "me")
	must(err)
	if _, err := s.Advance(ctx, u.ID, "new", store.Expect{}, "yard-19-new"); err == nil {
		t.Fatal("non-member moved an unclaimed bead out of a members stage")
	}
	if _, err := s.Advance(ctx, u.ID, "new", store.Expect{}, "mckean"); err != nil {
		t.Fatal(err)
	}

	// A group without members leaves advance as it was: anyone.
	d := claimed("decide", "deciders")
	if _, err := s.Advance(ctx, d.ID, "backlog", store.Expect{}, "anyone"); err != nil {
		t.Fatal(err)
	}
	// So does a stage with no group at all.
	if _, err := s.Advance(ctx, d.ID, "new", store.Expect{}, "anyone"); err != nil {
		t.Fatal(err)
	}
}
