package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// linkedYard is yard with the peer north, whose record carries the roster
// north said: its yardmaster and its operator. own is the store as the
// daemon and the receiver use it, and key north's.
func linkedYard(t *testing.T) (st store.API, own *store.Store, key string) {
	t.Helper()
	st, own = storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, me, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "chief", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	must(t, err)
	key = yardkey.Public(k)
	_, err = st.AddPeer(ctx, store.Peer{Name: "north", Key: key, Calls: "the-north"}, "jo")
	must(t, err)
	said(t, own, key, true, "mckean", "yardmaster")
	return st, own, key
}

// said has north say its roster: names, each taking mail or not.
func said(t *testing.T, own *store.Store, key string, mail bool, names ...string) {
	t.Helper()
	says := store.Says("v1")
	for _, n := range names {
		e := store.PeerRosterEntry{Name: n, Kind: store.RosterCrew, Role: "Shapes the beads", Mail: mail}
		if n == "mckean" {
			e.Kind, e.Role = store.RosterUser, "operator"
		}
		says.Roster = append(says.Roster, e)
	}
	must(t, own.PeerHeard(context.Background(), "north", key, says))
}

func allow(t *testing.T, st store.API, ch store.PeerAllow) {
	t.Helper()
	_, err := st.AllowPeer(context.Background(), "north", ch, "jo")
	must(t, err)
}

// fromNorth is mail north's yardmaster wrote to ada, as it comes in.
func fromNorth(t *testing.T, own *store.Store, key, id, text string) {
	t.Helper()
	northMail(t, own, key, id, "yardmaster", text)
}

// northMail is mail from of north wrote to ada, as it comes in.
func northMail(t *testing.T, own *store.Store, key, id, from, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"to": me, "from": from, "text": text})
	taken, err := own.PeerReceive(context.Background(), "north", key, []store.PeerMessage{{ID: id, Kind: store.PeerMail, Body: body}})
	if err != nil || len(taken) != 1 || taken[0].Refused != "" {
		t.Fatalf("north's mail was not taken: %+v, %v", taken, err)
	}
}

// Those a linked yard exposes are rows after the crew, <name>@<peer>, in
// the wide view and the folded one, and tab reaches them. A row says who
// that is there, and what keeps the mail or its answer from arriving.
func TestLinkedRows(t *testing.T) {
	st, own, key := linkedYard(t)
	v := open(t, st, "", 120, 24)
	if got := v.m.Linked(); !slices.Equal(got, []string{"mckean@north", "yardmaster@north"}) {
		t.Fatalf("the rows of the linked yard = %q", got)
	}
	v.shows("> chief", "  mckean@north", "  yardmaster@north")
	v.key(tea.KeyTab)
	v.shows("> mckean@north", "a person of the linked yard north: operator", "Write to mckean@north",
		"! this yard's link sends no mail to north. The operator allows it:", "  yardr peer allow north --send mail")
	v.key(tea.KeyTab)
	v.shows("> yardmaster@north", "a crew member of the linked yard north: Shapes the beads", "No mail between you and yardmaster@north yet.")
	v.key(tea.KeyTab)
	if !v.m.OnYard() {
		t.Fatalf("tab past the last of the linked yard selected %q", v.m.Selected())
	}
	v.key(tea.KeyShiftTab)
	if v.m.Selected() != "yardmaster@north" {
		t.Fatalf("shift+tab from the yard's row selected %q", v.m.Selected())
	}
	// It has no pane in this yard.
	v.send(tea.KeyMsg{Type: tea.KeyCtrlG})
	v.shows("yardmaster@north is of the linked yard north, and has no pane in this one")
	if len(v.focused) != 0 {
		t.Fatalf("a pane was asked to the front: %v", v.focused)
	}

	// The link sends mail now, and north cannot answer ada until the link
	// exposes her; then nothing is amiss.
	allow(t, st, store.PeerAllow{Send: []string{"mail"}})
	v.stream()
	v.shows("! yardmaster cannot answer you: this yard's link does not let north write to you.", "  yardr peer allow north --receive mail --expose ada")
	v.hides("sends no mail")
	allow(t, st, store.PeerAllow{Receive: []string{"mail"}, Expose: []string{me}})
	v.stream()
	v.hides("cannot answer you", "! ")
	// North's own link takes none.
	said(t, own, key, false, "mckean", "yardmaster")
	allow(t, st, store.PeerAllow{Send: []string{"ping"}}) // any event of a link has the roster read again
	v.stream()
	v.shows("! north takes no mail from this yard yet, so what you write is refused there")

	// Folded into a line, the address is the item.
	narrow := open(t, st, "yardmaster@north", 80, 24)
	narrow.shows("chief not running  mckean@north  [yardmaster@north]")

	// One north exposes no more is gone from the list, and the selection
	// with it.
	said(t, own, key, true, "mckean")
	allow(t, st, store.PeerAllow{NoSend: []string{"ping"}})
	v.stream()
	if got := v.m.Linked(); !slices.Equal(got, []string{"mckean@north"}) || v.m.Selected() != "chief" {
		t.Fatalf("after north exposed the yardmaster no more: rows %q, selected %q", got, v.m.Selected())
	}
	// A yard whose peers expose nobody has the crew alone.
	said(t, own, key, true)
	if s, err := Load(context.Background(), st, me); err != nil || len(store.PeerParties(s.Peers)) != 0 {
		t.Fatalf("a peer that exposes nobody adds %+v, %v", s, err)
	}
}

// What a user types on the row of one of a linked yard goes out as yardr
// mail <name>@<peer> does: queued for the peer under the user's name, with
// no peers setting of theirs. Under it the view says where it stands, as
// the yard learns of it. The answer is in the same conversation, unread
// on its row until that is opened, and its text reaches the terminal as
// letters only.
func TestWriteToALinkedYard(t *testing.T) {
	st, own, key := linkedYard(t)
	ctx := context.Background()
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{me}})
	v := open(t, st, "yardmaster@north", 120, 30)

	v.typed("is the depot on Go 1.27?")
	v.key(tea.KeyEnter)
	out, err := st.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 || out[0].Kind != store.PeerMail || !strings.Contains(string(out[0].Body), `"from":"ada"`) ||
		!strings.Contains(string(out[0].Body), `"to":"yardmaster"`) {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	v.shows("ada", "is the depot on Go 1.27?", "· queued for north: the daemon delivers it")
	v.stream() // the same mail from the stream is no second one
	if n := strings.Count(v.screen(), "is the depot on Go 1.27?"); n != 1 {
		t.Fatalf("the mail shows %d times:\n%s", n, v.screen())
	}

	// North asks for what waits, and has it.
	page, err := own.PeerHandOver(ctx, "north", key, 0, nil)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("PeerHandOver = %+v, %v", page, err)
	}
	_, err = own.PeerHandOver(ctx, "north", key, out[0].Seq, nil)
	must(t, err)
	v.stream()
	v.shows("· delivered to north, which does not say when it is read")
	v.hides("queued for north")

	// The answer comes while another row is open.
	v.key(tea.KeyShiftTab)
	v.key(tea.KeyShiftTab)
	if v.m.Selected() != "chief" {
		t.Fatalf("selected %q", v.m.Selected())
	}
	fromNorth(t, own, key, "m1", "it is \x1b[31mred\x1b[0m\x07 hot")
	v.stream()
	if n := v.m.Unread("yardmaster@north"); n != 1 {
		t.Fatalf("unread from yardmaster@north = %d", n)
	}
	v.shows("yardmaster@north            1")
	v.hides("no crew member") // it has a row: no pointer to yardr inbox
	if v.cursor() != 0 {
		t.Fatalf("mail of a conversation not opened was marked read through %d", v.cursor())
	}
	v.key(tea.KeyTab)
	v.key(tea.KeyTab)
	v.shows("> yardmaster@north", "it is red hot")
	if strings.ContainsAny(v.m.View(), "\x07") || strings.Contains(v.m.View(), "\x1b[31mred") {
		t.Fatalf("north's text reached the terminal as more than letters:\n%q", v.m.View())
	}
	if v.m.Unread("yardmaster@north") != 0 || v.cursor() == 0 {
		t.Fatalf("opening the conversation left %d unread, the cursor at %d", v.m.Unread("yardmaster@north"), v.cursor())
	}

	// A view opened later has both from the store, and the unread mail of
	// a linked yard on its row.
	fromNorth(t, own, key, "m2", "and stays there")
	later := open(t, st, "chief", 120, 30)
	if n := later.m.Unread("yardmaster@north"); n != 1 {
		t.Fatalf("a view opened later counts %d unread from yardmaster@north", n)
	}
	later.key(tea.KeyTab)
	later.key(tea.KeyTab)
	later.shows("is the depot on Go 1.27?", "· delivered to north", "it is red hot", "and stays there")

	// Mail north does not take says so under it, and why is in the inbox.
	v.typed("anyone else?")
	v.key(tea.KeyEnter)
	out, err = st.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	_, err = own.PeerHandOver(ctx, "north", key, 0, nil)
	must(t, err)
	_, err = own.PeerHandOver(ctx, "north", key, out[0].Seq, map[uint64]string{out[0].Seq: store.RefusedNotExposed})
	must(t, err)
	v.stream()
	v.shows("anyone else?", "· not delivered: yardr inbox says why",
		"1 unread from yard, no crew member: yardr inbox")

	// A link that carries no mail out refuses the line, and keeps it.
	allow(t, st, store.PeerAllow{NoSend: []string{"mail"}})
	v.stream()
	v.typed("once more")
	v.key(tea.KeyEnter)
	v.shows("not sent to yardmaster@north", "once more")
}

// A name of a linked yard that the link trusts as a user of this yard has
// no row: its mail is in the conversation with that user, whose row stands
// where the name's would and counts it. What is typed there goes to the
// user in this yard. Without the grant, and once it is taken back, the name
// has its row as any other of the linked yard.
func TestTrustedNameIsTheUsersConversation(t *testing.T) {
	st, own, key := linkedYard(t)
	ctx := context.Background()
	_, err := st.AddUser(ctx, "mckean", "jo")
	must(t, err)
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{me}})

	// Without the grant: a row of the address, and the user has none.
	v := open(t, st, "chief", 120, 30)
	northMail(t, own, key, "m1", "mckean", "before the grant")
	v.stream()
	if got := v.m.Linked(); !slices.Equal(got, []string{"mckean@north", "yardmaster@north"}) || v.m.Unread("mckean@north") != 1 || v.m.Unread("mckean") != 0 {
		t.Fatalf("without a grant: rows %q, %d unread from mckean@north, %d from mckean", got, v.m.Unread("mckean@north"), v.m.Unread("mckean"))
	}
	// Mail the user of this yard wrote here has no row to be on.
	must(t, st.Mail(ctx, me, "from this yard", "mckean"))
	v.stream()
	v.shows("1 unread from mckean, no crew member: yardr inbox")

	// With it, while the view is open: one row, the user's, with both.
	allow(t, st, store.PeerAllow{Trust: map[string]string{"mckean": "mckean"}})
	v.stream()
	if got := v.m.Linked(); !slices.Equal(got, []string{"mckean", "yardmaster@north"}) || v.m.Unread("mckean") != 2 {
		t.Fatalf("with the grant: rows %q, %d unread from mckean", got, v.m.Unread("mckean"))
	}
	v.shows("  mckean                      2")
	v.hides("mckean@north", "no crew member")
	northMail(t, own, key, "m2", "mckean", "after the grant")
	v.stream()
	if n := v.m.Unread("mckean"); n != 3 {
		t.Fatalf("unread on mckean's row = %d", n)
	}
	if v.cursor() != 0 {
		t.Fatalf("mail of a conversation not opened was marked read through %d", v.cursor())
	}
	v.key(tea.KeyTab)
	v.shows("> mckean", "a person of this yard, also trusted as mckean@north: mail from that name is in this",
		"conversation, and what you write goes to mckean in this yard", "Write to mckean",
		"mckean@north\n", "before the grant", "from this yard", "after the grant")
	if v.m.Unread("mckean") != 0 || v.cursor() == 0 {
		t.Fatalf("opening the conversation left %d unread, the cursor at %d", v.m.Unread("mckean"), v.cursor())
	}
	// In the open conversation mail of the name is seen as it arrives.
	northMail(t, own, key, "m3", "mckean", "while it is open")
	v.stream()
	v.shows("while it is open")
	if n := v.m.Unread("mckean"); n != 0 {
		t.Fatalf("mail that arrived in the open conversation counts %d unread", n)
	}

	// The answer goes to the user here, and nothing to the other yard.
	v.typed("which yard are you at?")
	v.key(tea.KeyEnter)
	if out, err := st.PeerOutbox(ctx, "north", 0); err != nil || len(out) != 0 {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	box, err := st.UserInbox(ctx, "mckean", 5)
	if err != nil || len(box.Mail) != 1 || box.Mail[0].From != me || box.Mail[0].Text != "which yard are you at?" {
		t.Fatalf("mckean's inbox = %+v, %v", box, err)
	}
	v.shows("which yard are you at?", "· sent")
	v.stream() // the same mail from the stream is no second one
	if n := strings.Count(v.screen(), "which yard are you at?"); n != 1 {
		t.Fatalf("the mail shows %d times:\n%s", n, v.screen())
	}
	v.send(tea.KeyMsg{Type: tea.KeyCtrlG})
	v.shows("mckean is a person of this yard, and has no pane")

	// A view opened later has all of it from the store, on the one row,
	// and the address opens it.
	northMail(t, own, key, "m4", "mckean", "for later")
	later := open(t, st, "mckean@north", 120, 30)
	if got := later.m.Linked(); !slices.Equal(got, []string{"mckean", "yardmaster@north"}) || later.m.Selected() != "mckean" {
		t.Fatalf("a view opened later: rows %q, selected %q", got, later.m.Selected())
	}
	later.shows("before the grant", "from this yard", "after the grant", "which yard are you at?", "for later")
	narrow := open(t, st, "chief", 80, 24)
	narrow.shows("[chief not running]  mckean  yardmaster@north")

	// The grant taken back, the name has its row again and its mail there;
	// what the user wrote here has none.
	allow(t, st, store.PeerAllow{NoTrust: []string{"mckean"}})
	v.stream()
	northMail(t, own, key, "m5", "mckean", "one more")
	v.stream()
	if got := v.m.Linked(); !slices.Equal(got, []string{"mckean@north", "yardmaster@north"}) || v.m.Selected() != "chief" || v.m.Unread("mckean@north") != 1 {
		t.Fatalf("the grant taken back: rows %q, selected %q, %d unread from mckean@north", got, v.m.Selected(), v.m.Unread("mckean@north"))
	}
	v.key(tea.KeyTab)
	v.shows("> mckean@north", "a person of the linked yard north: operator", "before the grant", "for later", "one more")
	v.hides("from this yard", "which yard are you at?")
}
