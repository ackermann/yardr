package yards_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yards"
)

// Each yard tells the other whom of it the other may see and write to,
// with everything it says: b, which is called, in its answers, and a,
// which calls, in its questions and posts. Mail reaches the names on the
// roster, a user as a crew member, and no other name; and a name taken off
// it is gone from the other yard with the next exchange.
func TestYardsSayWhomTheyExpose(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	mail := []string{"mail"}
	toB := a.add(t, "b", b, true, mail, nil) // a writes, and takes no mail
	b.add(t, "a", a, false, nil, mail)

	if err := b.api.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "yardmaster.md"), []byte("# Yardmaster: shapes the beads\n\nRuns as claude.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.CrewMember{
		{Name: "yardmaster", Runner: "herdr", Dir: dir, Config: map[string]any{"prompt": "yardmaster.md", "model": "opus"}},
		{Name: "clerk", Runner: "herdr"},
	} {
		if _, err := b.api.PutCrew(ctx, r, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Expose: []string{"yardmaster", "mckean"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.api.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	toB = a.expose(t, "b", "scout")

	// a asks: its question says its roster, b's answer says b's.
	pull := func() {
		t.Helper()
		p, err := a.api.GetPeer(ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		batch, err := a.caller().Pull(ctx, *p, p.Pulled, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.api.PeerPulled(ctx, "b", batch); err != nil {
			t.Fatal(err)
		}
	}
	pull()
	bs := []store.PeerRosterEntry{
		{Name: "mckean", Kind: store.RosterUser, Role: "operator", Mail: true},
		{Name: "yardmaster", Kind: store.RosterCrew, Role: "Yardmaster: shapes the beads", Mail: true},
	}
	if got := said(t, a, "b"); got == nil || !reflect.DeepEqual(got.Roster, bs) {
		t.Fatalf("a, after it asked: b exposes %+v", got)
	}
	as := []store.PeerRosterEntry{{Name: "scout", Kind: store.RosterCrew}} // no role file, and a takes no mail
	if got := said(t, b, "a"); got == nil || !reflect.DeepEqual(got.Roster, as) {
		t.Fatalf("b, after a asked: a exposes %+v", got)
	}
	// What crosses is the entry's four fields: nothing of what the crew
	// member runs as, nor where its files are.
	resp, body := signed(a, b, "GET", yards.Path+"?after=0", nil).send(t, b)
	var page struct {
		Says struct {
			Roster []map[string]any `json:"roster"`
		} `json:"says"`
	}
	if err := json.Unmarshal(body, &page); err != nil || resp.StatusCode != http.StatusOK || len(page.Says.Roster) != 2 {
		t.Fatalf("b's answer = %d %s, %v", resp.StatusCode, body, err)
	}
	if want := map[string]any{"name": "yardmaster", "kind": "crew", "role": "Yardmaster: shapes the beads", "mail": true}; !reflect.DeepEqual(page.Says.Roster[1], want) ||
		strings.Contains(string(body), "opus") || strings.Contains(string(body), "claude") || strings.Contains(string(body), dir) {
		t.Fatalf("b's answer says %s", body)
	}
	// That question was an older yardr's, which says nothing: nothing is
	// kept of a yard that has not said.
	if got := said(t, b, "a"); got != nil {
		t.Fatalf("b, after a question that says nothing: %+v", got)
	}

	// Mail from a: to the user and the crew member b exposes, and to
	// nobody else of b.
	caller := a.caller()
	caller.Heard = func(ctx context.Context, p store.Peer, answer store.PeerAnswer) {
		if err := a.api.PeerPostAnswered(ctx, p.Name, answer); err != nil {
			t.Errorf("PeerPostAnswered: %v", err)
		}
	}
	post := func(id, to string) error {
		body, _ := json.Marshal(map[string]string{"to": to, "from": "scout", "text": "for " + to})
		return caller.Push(ctx, toB, store.PeerMessage{ID: id, Kind: "mail", Body: body})
	}
	for id, to := range map[string]string{"m1": "mckean", "m2": "yardmaster"} {
		if err := post(id, to); err != nil {
			t.Fatalf("mail to %s, whom b exposes = %v", to, err)
		}
	}
	box, err := b.api.UserInbox(ctx, "mckean", 0, "mckean")
	if err != nil || len(box.Mail) != 1 || box.Mail[0].From != "scout@a" || box.Mail[0].Text != "for mckean" || box.Mail[0].Read {
		t.Fatalf("mckean's inbox = %+v, %v", box, err)
	}
	if in, err := b.api.Inbox(ctx, "yardmaster", 0); err != nil || len(in.Events) != 1 || in.Events[0].Actor != "scout@a" {
		t.Fatalf("yardmaster's inbox = %+v, %v", in, err)
	}
	// The post said a's roster again, as its answer said b's.
	if got := said(t, b, "a"); got == nil || !reflect.DeepEqual(got.Roster, as) {
		t.Fatalf("b, after a posted: a exposes %+v", got)
	}
	for id, to := range map[string]string{"r1": "clerk", "r2": "nobody"} {
		if err := post(id, to); !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "it exposes nobody of that name to this yard (yardr peer show b lists whom it does)") {
			t.Errorf("mail to %s, whom b does not expose = %v", to, err)
		}
	}
	resp, body = signed(a, b, "POST", yards.Path, []byte(`{"id":"r3","kind":"mail","body":{"to":"clerk","from":"scout","text":"hi"}}`)).send(t, b)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), `"refused":"not_exposed"`) {
		t.Fatalf("mail to the clerk = %d %s", resp.StatusCode, body)
	}
	refused := b.events(t, "peer_message_refused")
	if len(refused) != 3 || refused[2].Data["reason"] != "not_exposed" || refused[2].Data["to"] != "clerk" || refused[2].Data["direction"] != "in" {
		t.Fatalf("b's peer_message_refused = %+v", refused)
	}
	if in, err := b.api.Inbox(ctx, "clerk", 0); err != nil || len(in.Events) != 0 {
		t.Fatalf("clerk's inbox = %+v, %v", in, err)
	}

	// b exposes its operator no more: a hears of it with the next answer,
	// to a post as to a question, and the mail is refused.
	if _, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{NoExpose: []string{"mckean"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := post("m3", "mckean"); !errors.Is(err, yards.ErrRefused) {
		t.Fatalf("mail to one b exposes no more = %v", err)
	}
	if got := said(t, a, "b"); got == nil || !reflect.DeepEqual(got.Roster, bs[1:]) {
		t.Fatalf("a, after the answer to its post: b exposes %+v", got)
	}
	if _, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{NoExpose: []string{"yardmaster"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	pull()
	if got := said(t, a, "b"); got == nil || got.Roster != nil {
		t.Fatalf("a, after b exposes nobody: %+v", got)
	}
}
