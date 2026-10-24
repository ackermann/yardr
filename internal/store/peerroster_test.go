package store_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// rosterYard is a yard with the peer north, the operator jo and the user
// ada, and the crew members yardmaster, whose role file dir holds, and
// clerk, who has none.
func rosterYard(t *testing.T) (y *peerYard, dir string) {
	t.Helper()
	y = newPeerYard(t, "http://north.test")
	if err := y.api.SetSetting(y.ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	role := "\n# Yardmaster of the test yard\n\nYou run on claude-opus, from /home/jo/roles.\n"
	if err := os.WriteFile(filepath.Join(dir, "yardmaster.md"), []byte(role), 0o600); err != nil {
		t.Fatal(err)
	}
	master := store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: dir, Config: map[string]any{"prompt": "yardmaster.md", "model": "opus"}}
	if _, err := y.api.PutCrew(y.ctx, master, "jo"); err != nil {
		t.Fatal(err)
	}
	y.member(t, "clerk", "")
	return y, dir
}

// A link exposes nobody until the operator names who: crew members and
// users of the yard, by name, and nothing else. The names are kept sorted,
// each once, and the roster the peer is told says of each what it is, one
// line of what it does and whether the link takes mail.
func TestPeerAllowExpose(t *testing.T) {
	t.Parallel()
	y, dir := rosterYard(t)
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Expose != nil || p.Roster != nil {
		t.Fatalf("a new link exposes %+v, says %+v, %v", p.Expose, p.Roster, err)
	}
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nobody", "builders", ""} {
		_, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Expose: []string{"clerk", name}}, "jo")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q is neither a crew member nor a user of this yard", name)) {
			t.Errorf("exposing %q = %v", name, err)
		}
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Expose != nil {
		t.Fatalf("a refused change exposed %+v", p.Expose)
	}
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Expose: []string{"clerk"}}, "yardmaster"); err == nil ||
		err.Error() != "only the operator (jo) may expose someone of the yard to a peer; this is yardmaster" {
		t.Fatalf("exposing as a crew member = %v", err)
	}

	p := y.allow(t, store.PeerAllow{Expose: []string{"yardmaster", "ada", "yardmaster"}})
	want := []store.PeerRosterEntry{
		{Name: "ada", Kind: store.RosterUser, Role: "user"},
		{Name: "yardmaster", Kind: store.RosterCrew, Role: "Yardmaster of the test yard"},
	}
	if !reflect.DeepEqual(p.Expose, []string{"ada", "yardmaster"}) || !reflect.DeepEqual(p.Roster, want) {
		t.Fatalf("exposed %+v, the roster %+v", p.Expose, p.Roster)
	}
	allowed := peerEvents(t, y.api, "peer_allowed")
	if got := fmt.Sprint(allowed[len(allowed)-1].Data["expose"]); got != "[ada yardmaster]" {
		t.Fatalf("peer_allowed says expose = %s", got)
	}
	// The other grant: with mail taken, every entry says so. One exposed
	// with no role file has no line, and the operator is said as one.
	p = y.allow(t, store.PeerAllow{Receive: []string{"mail"}, Expose: []string{"clerk", "jo"}})
	want = []store.PeerRosterEntry{
		{Name: "ada", Kind: store.RosterUser, Role: "user", Mail: true},
		{Name: "clerk", Kind: store.RosterCrew, Mail: true},
		{Name: "jo", Kind: store.RosterUser, Role: "operator", Mail: true},
		{Name: "yardmaster", Kind: store.RosterCrew, Role: "Yardmaster of the test yard", Mail: true},
	}
	if !reflect.DeepEqual(p.Roster, want) {
		t.Fatalf("the roster = %+v", p.Roster)
	}
	// As the daemon reads it, and as the receiver does.
	if ps, err := y.api.Peers(y.ctx); err != nil || len(ps) != 1 || !reflect.DeepEqual(ps[0].Roster, want) {
		t.Fatalf("Peers: the roster = %+v, %v", ps, err)
	}
	if got, err := y.own.PeerByKey(y.ctx, y.key); err != nil || !reflect.DeepEqual(got.Roster, want) {
		t.Fatalf("PeerByKey: the roster = %+v, %v", got, err)
	}

	// A role is one line that can be printed, and no longer than a line is.
	long := "## " + strings.Repeat("word ", 40) + "\x1b[31m\nsecond line"
	if err := os.WriteFile(filepath.Join(dir, "yardmaster.md"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ = y.api.GetPeer(y.ctx, "north")
	if role := p.Roster[3].Role; len([]rune(role)) != 80 || !strings.HasPrefix(role, "word word") || !strings.HasSuffix(role, "…") || strings.ContainsAny(role, "#\x1b\n") {
		t.Fatalf("a long role = %q", role)
	}
	if err := os.Remove(filepath.Join(dir, "yardmaster.md")); err != nil {
		t.Fatal(err)
	}
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Roster[3].Role != "" {
		t.Fatalf("with no role file: %+v, %v", p.Roster, err)
	}

	// To stop is anyone's, and the name may be of somebody who is gone.
	if p, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{NoExpose: []string{"ada", "nobody"}}, "yardmaster"); err != nil ||
		!reflect.DeepEqual(p.Expose, []string{"clerk", "jo", "yardmaster"}) || len(p.Roster) != 3 {
		t.Fatalf("stopped by a crew member: %+v, %v", p, err)
	}
	p = y.allow(t, store.PeerAllow{NoExpose: []string{"clerk", "jo", "yardmaster"}})
	if p.Expose != nil || p.Roster != nil {
		t.Fatalf("with all stopped: %+v, %+v", p.Expose, p.Roster)
	}

	// No more names than a roster holds.
	var many []string
	for i := 0; i <= store.MaxPeerExpose; i++ {
		many = append(many, fmt.Sprintf("crew-%02d", i))
		y.member(t, many[i], "")
	}
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Expose: many}, "jo"); err == nil || !strings.Contains(err.Error(), "a link exposes at most 32 names, and this would make it 33") {
		t.Fatalf("exposing %d = %v", len(many), err)
	}
	if p := y.allow(t, store.PeerAllow{Expose: many[1:]}); len(p.Roster) != store.MaxPeerExpose {
		t.Fatalf("exposing %d: a roster of %d", len(many)-1, len(p.Roster))
	}
}

// Who is removed from the yard is taken off every link that exposed them:
// whoever has the name next is exposed to nobody.
func TestRemovedIsExposedNoMore(t *testing.T) {
	t.Parallel()
	y, _ := rosterYard(t)
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "west", Key: otherKey(t)}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Expose: []string{"ada", "clerk", "yardmaster"}})
	if _, err := y.api.AllowPeer(y.ctx, "west", store.PeerAllow{Expose: []string{"clerk"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.DeleteCrew(y.ctx, "clerk", "", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.DeleteUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	north, _ := y.api.GetPeer(y.ctx, "north")
	west, _ := y.api.GetPeer(y.ctx, "west")
	if !reflect.DeepEqual(north.Expose, []string{"yardmaster"}) || west.Expose != nil {
		t.Fatalf("north exposes %+v, west %+v", north.Expose, west.Expose)
	}
	if ev := peerEvents(t, y.api, "crew_deleted"); len(ev) != 1 || fmt.Sprint(ev[0].Data["unexposed"]) != "[north west]" {
		t.Fatalf("crew_deleted = %+v", ev)
	}
	if ev := peerEvents(t, y.api, "user_removed"); len(ev) != 1 || fmt.Sprint(ev[0].Data["unexposed"]) != "[north]" {
		t.Fatalf("user_removed = %+v", ev)
	}
	// One of the name again is another, and nobody's to write to.
	y.member(t, "clerk", "")
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}})
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "clerk", "clerk", "hi")}); err != nil || got[0].Refused != store.RefusedNotExposed {
		t.Fatalf("mail to the clerk of now = %+v, %v", got, err)
	}
}

// Mail from a peer reaches whom the link exposes, a user as a crew member,
// and nobody else: a crew member not exposed is refused as a name nobody
// has is, so the answer tells nothing of who is here. Exposing is not
// taking mail: without receive mail the link carries none.
func TestPeerMailToTheExposed(t *testing.T) {
	t.Parallel()
	y, _ := rosterYard(t)
	if _, err := y.api.PutSource(y.ctx, "pushover.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.SetUser(y.ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	take := func(m store.PeerMessage) string {
		t.Helper()
		got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{m})
		if err != nil || len(got) != 1 {
			t.Fatalf("PeerReceive = %+v, %v", got, err)
		}
		return got[0].Refused
	}
	y.allow(t, store.PeerAllow{Expose: []string{"ada", "yardmaster"}})
	if got := take(mail("m0", "ada", "yardmaster", "hi")); got != store.RefusedKind {
		t.Fatalf("mail to one exposed on a link that takes none = %q", got)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}})

	if got := take(mail("m1", "ada", "yardmaster", "the build is green")); got != "" {
		t.Fatalf("mail to an exposed user = %q", got)
	}
	box, err := y.api.UserInbox(y.ctx, "ada", 0, "ada")
	if err != nil || box.Unread != 1 || len(box.Mail) != 1 || box.Mail[0].From != "yardmaster@north" || box.Mail[0].To != "ada" || box.Mail[0].Text != "the build is green" {
		t.Fatalf("ada's inbox = %+v, %v", box, err)
	}
	if ev := peerEvents(t, y.api, "mail"); len(ev) != 1 || ev[0].Data["to_user"] != true || ev[0].Data["from_peer"] != "north" || !store.FromOutside(ev[0]) {
		t.Fatalf("the mail event = %+v", ev)
	}
	// Told outside the yard as any mail to her is.
	if ps, err := y.api.Pushes(y.ctx); err != nil || len(ps) != 1 || ps[0].User != "ada" || ps[0].Mail == nil || ps[0].Mail.From != "yardmaster@north" {
		t.Fatalf("pushes = %+v, %v", ps, err)
	}
	if got := take(mail("m2", "yardmaster", "yardmaster", "and to you")); got != "" || len(y.inbox(t, "yardmaster")) != 1 {
		t.Fatalf("mail to an exposed crew member = %q, inbox %+v", got, y.inbox(t, "yardmaster"))
	}

	before := len(peerEvents(t, y.api, "peer_message_refused"))
	for id, to := range map[string]string{"r1": "clerk", "r2": "jo", "r3": "nobody"} {
		if got := take(mail(id, to, "yardmaster", "hi")); got != store.RefusedNotExposed {
			t.Errorf("mail to %s, who is not exposed = %q", to, got)
		}
	}
	ref := peerEvents(t, y.api, "peer_message_refused")[before:]
	if len(ref) != 3 || ref[0].Data["reason"] != "not_exposed" || ref[0].Data["direction"] != "in" || ref[0].Data["to"] == nil {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	if len(y.inbox(t, "clerk")) != 0 {
		t.Fatalf("clerk's inbox = %+v", y.inbox(t, "clerk"))
	}
	// Stopped, the name is reached no more.
	y.allow(t, store.PeerAllow{NoExpose: []string{"ada"}})
	if got := take(mail("m3", "ada", "yardmaster", "still there?")); got != store.RefusedNotExposed {
		t.Fatalf("mail to one no longer exposed = %q", got)
	}
}
