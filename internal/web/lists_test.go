package web_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
	"yardr.dev/yardr/internal/yardkey"
)

// printed is v as a command prints it with --json, read back: what a client
// of either has.
func printed(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	must(t, err)
	var out any
	must(t, json.Unmarshal(raw, &out))
	return out
}

// listOf reads a route's answer: the list under key and the head.
func (f *fixture) listOf(target, key string, headers ...string) (list any, head uint64) {
	f.t.Helper()
	r := f.get(target, headers...)
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
		f.t.Fatalf("%s: status %d, %v: %s", target, r.status, err, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("%s: Content-Type %q", target, ct)
	}
	if got[key] == nil || got["head"] == nil {
		f.t.Fatalf("%s: no %q or no head: %s", target, key, r.body)
	}
	must(f.t, json.Unmarshal(got[key], &list))
	must(f.t, json.Unmarshal(got["head"], &head))
	return list, head
}

// The lists with no page answer what their list commands print with
// --json, under the route's name, with the yard's head: JSON whatever the
// request accepts, and an empty list for a yard that has none.
func TestLists(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	routes := map[string]func() any{
		"depots": func() any { v, err := f.st.Depots(ctx); must(t, err); return v },
		"groups": func() any { v, err := f.st.Groups(ctx); must(t, err); return v },
		"routes": func() any { v, err := f.st.Routes(ctx); must(t, err); store.SortRoutes(v); return v },
		"peers":  func() any { v, err := f.st.Peers(ctx); must(t, err); return v },
		"deps":   func() any { v, err := f.st.AllDeps(ctx); must(t, err); return v },
	}
	for key := range routes {
		if list, _ := f.listOf("/"+key, key, "Accept", "application/json"); !reflect.DeepEqual(list, []any{}) {
			t.Errorf("/%s of an empty yard: %v, want an empty list", key, list)
		}
	}

	f.populate()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "claude"}}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "me"))
	line, bug := "line", "bug"
	for _, r := range []store.Route{
		{Stage: "new", Group: "builders"},
		{Stage: "new", Depot: &line, Type: &bug, Group: "decisions", Priority: 5},
	} {
		_, err := f.st.AddRoute(ctx, r, "me")
		must(t, err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	must(t, err)
	_, err = f.st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(key), URL: "http://127.0.0.1:1"}, "me")
	must(t, err)
	beads := f.byTitle()
	must(t, f.st.AddDep(ctx, beads["one"].ID, beads["three"].ID, store.DepBlocks, "me"))
	must(t, f.st.AddDep(ctx, beads["three"].ID, beads["five"].ID, store.DepDiscoveredFrom, "me"))

	head, err := f.st.Head(ctx)
	must(t, err)
	for key, read := range routes {
		// No Accept: there is no page to answer with instead.
		for _, headers := range [][]string{nil, {"Accept", "application/json"}, {"Accept", "text/html"}} {
			list, got := f.listOf("/"+key, key, headers...)
			if want := printed(t, read()); !reflect.DeepEqual(list, want) || len(list.([]any)) == 0 {
				t.Errorf("/%s %v\n got %v\nwant %v", key, headers, list, want)
			}
			if got != head {
				t.Errorf("/%s: head %d, want %d", key, got, head)
			}
		}
	}

	// What a second front end picks from them is there by name.
	var groups web.Groups
	f.getJSON("/groups", &groups)
	if g := groups.Groups[0]; g.Name != "builders" || g.Runner != "herdr" || g.Limit != 3 || g.Config["kind"] != "claude" ||
		!slices.Equal(groups.Groups[1].Members, []string{"jo"}) {
		t.Errorf("groups: %+v", groups.Groups)
	}
	// The routes in match order: the higher priority first.
	var rs web.Routes
	f.getJSON("/routes", &rs)
	if len(rs.Routes) != 2 || rs.Routes[0].Group != "decisions" || rs.Routes[0].Priority != 5 || *rs.Routes[0].Type != "bug" {
		t.Errorf("routes: %+v", rs.Routes)
	}
	var depots web.Depots
	f.getJSON("/depots", &depots)
	if len(depots.Depots) != 2 || depots.Depots[0].Name != "line" || depots.Depots[0].Base != "origin/main" {
		t.Errorf("depots: %+v", depots.Depots)
	}
	// Every edge once, though each is in the list of both its beads.
	var deps web.Deps
	f.getJSON("/deps", &deps)
	var edges []string
	for _, d := range deps.Deps {
		edges = append(edges, d.From+" "+d.Kind+" "+d.To)
	}
	want := []string{beads["one"].ID + " blocks " + beads["three"].ID, beads["three"].ID + " discovered-from " + beads["five"].ID}
	if !slices.Equal(edges, want) {
		t.Errorf("deps: %v, want %v", edges, want)
	}
	// A list is read only, as every page.
	if r := f.do("POST", "/groups"); r.status != 405 {
		t.Errorf("POST /groups: status %d", r.status)
	}
}

// /beads has every field `yardr bead list --json` prints of a bead, and
// with ?all=1 the closed beads too, as bead list --all.
func TestBeadsAll(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	one := f.byTitle()["one"]
	_, err := f.st.UpdateBead(ctx, one.ID, store.BeadPatch{AddLabels: []string{"door"}, Priority: ptr(4)}, store.Expect{}, "me")
	must(t, err)

	for target, all := range map[string]bool{"/beads": false, "/beads?all=1": true, "/beads?all=0": false} {
		listed, err := f.st.ListBeads(ctx, store.BeadFilter{All: all})
		must(t, err)
		var got struct {
			All   bool             `json:"all"`
			Beads []map[string]any `json:"beads"`
		}
		f.getJSON(target, &got)
		if got.All != all || len(got.Beads) != len(listed) {
			t.Fatalf("%s: all %v, %d beads, want %v and %d", target, got.All, len(got.Beads), all, len(listed))
		}
		closed := 0
		for _, b := range listed {
			i := slices.IndexFunc(got.Beads, func(row map[string]any) bool { return row["id"] == b.ID })
			if i < 0 {
				t.Fatalf("%s lacks %s (%s)", target, b.ID, b.Title)
			}
			row := got.Beads[i]
			if b.Status == "closed" {
				closed++
			}
			// The card's own two fields beside the bead's.
			if row["working"] != false || (row["held"] == true) != b.Hold {
				t.Errorf("%s: %s working %v, held %v", target, b.Title, row["working"], row["held"])
			}
			delete(row, "working")
			delete(row, "held")
			if want := printed(t, b); !reflect.DeepEqual(any(row), want) {
				t.Errorf("%s: %s\n got %v\nwant %v", target, b.Title, row, want)
			}
		}
		if (closed == 1) != all {
			t.Errorf("%s: %d closed beads", target, closed)
		}
	}

	// What bead list prints and the cards did not have.
	var got web.Beads
	f.getJSON("/beads?all=1&stage=backlog", &got)
	i := slices.IndexFunc(got.Beads, func(r web.BeadRow) bool { return r.ID == one.ID })
	if i < 0 || !slices.Equal(got.Beads[i].Labels, []string{"door"}) || got.Beads[i].Priority != 4 || got.Beads[i].CreatedAt.IsZero() {
		t.Errorf("one: %+v", got.Beads)
	}
	if len(got.Beads) != 2 || got.Beads[1].Title != "two" || !got.Beads[1].Hold || !got.Beads[1].Held {
		t.Errorf("the held bead: %+v", got.Beads)
	}
	// The newest change first, the closed bead among them; a filter keeps all.
	f.getJSON("/beads?all=1&depot=line", &got)
	if want := []string{"one", "four", "two", "five", "three"}; !reflect.DeepEqual(titles(cards(got.Beads)), want) {
		t.Errorf("all of line: %v, want %v", titles(cards(got.Beads)), want)
	}
	text := squash(f.get("/beads?all=1&depot=line").body)
	for _, part := range []string{
		`hx-get="/beads?all=1&amp;depot=line"`,
		`<a href="/beads?all=1&amp;depot=line&amp;stage=new">new</a>`,
		` · closed</span></p><p class="card-title">four</p>`,
	} {
		if !strings.Contains(text, part) {
			t.Errorf("the page of all beads lacks %q", part)
		}
	}
	if text := f.get("/beads").body; strings.Contains(text, "four") || strings.Contains(text, "all=1") {
		t.Error("the page of the open beads shows the closed one, or links to all")
	}
}

func ptr[T any](v T) *T { return &v }

// /sessions has the sessions as `yardr session list --json` prints them
// under list, with the gate and the wait for a usage limit; with ?all=1 the
// ended ones too, with how they ended.
func TestSessionsList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b, sess := blockedYard(t, f)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	must(t, f.st.SetHandle(ctx, sess.ID, map[string]any{"agent": "door-new",
		"harness_reset_at": until.Format(time.RFC3339Nano), "harness_reset_window": "5h", "harness_reset_model": "opus"}))
	gone, err := f.st.CreateBead(ctx, store.NewBead{Title: "ended", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	ended, err := f.st.Claim(ctx, gone.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.EndSession(ctx, ended.ID, "died", "the pane closed"))
	gate, err := rigcheck.MarkRunning(f.home, rigcheck.Gate{Bead: b.ID, Depot: "line", Light: true, Since: time.Now()})
	must(t, err)
	defer gate.End()
	gates, err := rigcheck.Gates(f.home)
	must(t, err)

	for target, all := range map[string]bool{"/sessions": false, "/sessions?all=1": true} {
		ss, err := f.st.ListSessions(ctx, store.SessionFilter{All: all})
		must(t, err)
		list, _ := f.listOf(target, "list", "Accept", "application/json")
		if want := printed(t, dispatch.Listed(ss, gates)); !reflect.DeepEqual(list, want) {
			t.Errorf("%s\n got %v\nwant %v", target, list, want)
		}
		var got web.Sessions
		f.getJSON(target, &got)
		// The page's own rows are the live ones either way.
		if len(got.Sessions) != 1 || got.Sessions[0].ID != sess.ID || got.Sessions[0].Runner != "fake door-new" {
			t.Errorf("%s: sessions %+v", target, got.Sessions)
		}
		if want := map[bool]int{false: 1, true: 2}[all]; len(got.List) != want {
			t.Fatalf("%s: %d listed, want %d", target, len(got.List), want)
		}
		for _, s := range got.List {
			switch s.ID {
			case sess.ID:
				if s.Bead != b.ID || s.Group != "builders" || s.State != "running" || s.Runner != "fake" || s.EndedAt != nil ||
					s.Gate == nil || !s.Gate.Light || s.ResetWait == nil || !s.ResetWait.Until.Equal(until) || s.ResetWait.Model != "opus" {
					t.Errorf("%s: the running session: %+v (gate %+v, wait %+v)", target, s, s.Gate, s.ResetWait)
				}
			case ended.ID:
				if s.Bead != gone.ID || s.State != "died" || s.EndedAt == nil || s.StartedAt.IsZero() || s.Gate != nil {
					t.Errorf("%s: the ended session: %+v", target, s)
				}
			default:
				t.Errorf("%s lists %+v", target, s)
			}
		}
	}
}

// /crew says what a crew member's session is doing, as crew list --json.
func TestCrewStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Enabled: true, Config: map[string]any{"kind": "codex"}}, "me")
	must(t, err)
	must(t, f.st.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster"}))
	must(t, f.st.CrewStatus(ctx, "yardmaster", "idle"))
	var got web.Crew
	f.getJSON("/crew", &got)
	if len(got.Crew) != 1 || got.Crew[0].Status != "idle" || got.Crew[0].Kind != "codex" || got.Crew[0].Agent != "herdr · codex" {
		t.Errorf("crew: %+v", got.Crew)
	}
	rows, err := f.st.Crew(ctx)
	must(t, err)
	if len(got.Crew) == 1 {
		pageFields := map[string]bool{"running": true, "since": true, "up": true, "agent": true, "kind": true, "unread": true, "restart": true}
		var want, actual map[string]any
		want = printed(t, rows[0]).(map[string]any)
		actual = printed(t, got.Crew[0]).(map[string]any)
		for key := range pageFields {
			delete(actual, key)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Errorf("/crew command fields\n got %v\nwant %v", actual, want)
		}
	}
}
