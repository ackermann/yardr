package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// grantEvents are the source_granted and source_revoked events, oldest
// first.
func grantEvents(t *testing.T, s store.API, ctx context.Context) []store.Event {
	t.Helper()
	evs, err := s.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == "source_granted" || e.Kind == "source_revoked" {
			out = append(out, e)
		}
	}
	return out
}

// A grant of a call made as a user is given only by that user: the
// operator and a crew member are refused, and the user the yard does not
// have is refused. It is a second record beside a plain grant of the same
// grantee, granting it twice is the one record, each covers only its own
// kind of call, and revoking by the same words takes only that one back.
// Its events carry the user under "as", a plain grant's an empty one.
func TestGrantAsAUser(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	to := store.Grantee{Crew: "yardmaster"}

	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "jo"); err == nil ||
		!strings.Contains(err.Error(), "granting a call of a source as ada is ada's to give, and nobody else's") ||
		!strings.Contains(err.Error(), "this is the operator (jo)") ||
		!strings.Contains(err.Error(), `yardr bead ask <bead> "<question>" --run "source grant crm read --crew yardmaster --as ada" --to ada --context`) {
		t.Fatalf("the operator granting as ada = %v", err)
	}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("a crew member granting as ada = %v", err)
	}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "eve", "eve"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a grant as a user the yard does not have = %v", err)
	}
	if gs, err := s.SourceGrants(ctx, "crm"); err != nil || len(gs) != 0 {
		t.Fatalf("refused grants left %+v, %v", gs, err)
	}

	g, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada")
	if err != nil || g.As != "ada" || g.By != "ada" || g.Holder() != "crew member yardmaster as ada" {
		t.Fatalf("the grant as ada = %+v, %v", g, err)
	}
	// Granting again is the one record, and a call by name joins it.
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if g, err := s.GrantSource(ctx, "crm", "customer", to, "ada", "ada"); err != nil || strings.Join(g.Calls, ",") != "customer,read" {
		t.Fatalf("granting by name as ada = %+v, %v", g, err)
	}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "", "jo"); err != nil {
		t.Fatal(err)
	}
	gs, err := s.SourceGrants(ctx, "crm")
	if err != nil || len(gs) != 2 {
		t.Fatalf("the grants = %+v, %v", gs, err)
	}
	var plain, asAda store.SourceGrant
	for _, g := range gs {
		if g.As == "" {
			plain = g
		} else {
			asAda = g
		}
	}
	src, err := s.GetSource(ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	readCall, _ := d.Call("ping")
	writeCall, _ := d.Call("write")
	for _, c := range []struct {
		g    store.SourceGrant
		as   string
		want bool
	}{
		{plain, "", true}, {plain, "ada", false},
		{asAda, "ada", true}, {asAda, "", false}, {asAda, "bia", false},
	} {
		if got := c.g.Covers(readCall, c.as); got != c.want {
			t.Errorf("%+v Covers(ping, %q) = %v, want %v", c.g, c.as, got, c.want)
		}
	}
	if asAda.Covers(writeCall, "ada") {
		t.Error("a grant of read as ada covers an update as ada")
	}
	// Nobody calls plainly on the as grant alone, and the other way
	// round: the plain grant above is yardmaster's only plain one, so a
	// group with only an as grant calls nothing plainly.
	if _, err := s.GrantSource(ctx, "crm", "read", store.Grantee{Group: "reviewers"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if may, err := s.SourceCalls(ctx, "", "yardmaster"); err != nil {
		t.Fatal(err)
	} else if len(may.Calls) == 0 {
		t.Fatal("the plain grant lists no call for yardmaster")
	}
	_, reviewer := session(t, s, "reviewers")
	if _, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", Session: reviewer.ID, Actor: "x"}); err == nil ||
		!strings.Contains(err.Error(), "is not granted it") {
		t.Fatalf("a plain call on an as grant alone = %v", err)
	}

	// Revoking as ada takes only that one back, and anyone may do it.
	if g, err := s.RevokeSource(ctx, "crm", "customer", to, "ada", "bia"); err != nil || strings.Join(g.Calls, ",") != "read" {
		t.Fatalf("revoking one call as ada = %+v, %v", g, err)
	}
	if g, err := s.RevokeSource(ctx, "crm", "read", to, "", "jo"); err != nil || len(g.Calls) != 0 {
		t.Fatalf("revoking the plain grant = %+v, %v", g, err)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 2 {
		t.Fatalf("the grants after the revokes = %+v", gs)
	}
	var grantedAs, grantedPlain, revokedAs bool
	for _, e := range grantEvents(t, s, ctx) {
		switch {
		case e.Kind == "source_granted" && e.Data["as"] == "ada" && e.Actor == "ada":
			grantedAs = true
		case e.Kind == "source_granted" && e.Data["as"] == "":
			grantedPlain = true
		case e.Kind == "source_revoked" && e.Data["as"] == "ada":
			revokedAs = true
		}
	}
	if !grantedAs || !grantedPlain || !revokedAs {
		t.Fatalf("the grant events carry no as as they should: %+v", grantEvents(t, s, ctx))
	}

	// Removing the source removes both kinds.
	text, _ := s.GetSource(ctx, "crm")
	if err := s.DeleteSource(ctx, "crm", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSource(ctx, "crm.yaml", text.Text, "jo"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.SourceGrants(ctx, ""); len(gs) != 0 {
		t.Fatalf("grants after the source was removed = %+v", gs)
	}
}

// A store holding a plain grant under its old key and old record, with
// no "as" in it, reopened on this build still shows that grant covering
// its plain call, and granting and revoking plainly still address that
// same record, while a new as grant is a second record beside it.
func TestPlainGrantInAnOldStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age the record: as a build before As wrote it, under its old key.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "crm\x00crew\x00yardmaster"
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("source_grants"))
		if b.Get([]byte(key)) == nil {
			t.Fatal("the plain grant is not under its old key")
		}
		return b.Put([]byte(key), []byte(`{"source":"crm","crew":"yardmaster","calls":["read"],"at":"2026-10-01T00:00:00Z","by":"jo"}`))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	gs, err := s.SourceGrants(ctx, "crm")
	if err != nil || len(gs) != 1 || gs[0].As != "" || strings.Join(gs[0].Calls, ",") != "read" {
		t.Fatalf("the old grant reopened = %+v, %v", gs, err)
	}
	src, err := s.GetSource(ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	ping, _ := d.Call("ping")
	if !gs[0].Covers(ping, "") || gs[0].Covers(ping, "ada") {
		t.Fatalf("the old grant covers %+v as it should not", gs[0])
	}
	// Plainly granting and revoking address that same record.
	if g, err := s.GrantSource(ctx, "crm", "customer", to, "", "jo"); err != nil || strings.Join(g.Calls, ",") != "customer,read" {
		t.Fatalf("granting plainly on the old record = %+v, %v", g, err)
	}
	if _, err := s.GrantSource(ctx, "crm", "read", to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.SourceGrants(ctx, "crm"); len(gs) != 2 {
		t.Fatalf("the grants beside the old one = %+v", gs)
	}
	if g, err := s.RevokeSource(ctx, "crm", "customer", to, "", "jo"); err != nil || strings.Join(g.Calls, ",") != "read" {
		t.Fatalf("revoking plainly on the old record = %+v, %v", g, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
