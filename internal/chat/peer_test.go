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
	_, err = own.PeerHandOver(ctx, "north", key, out[0].Seq, map[uint64]store.Refusal{out[0].Seq: {Reason: store.RefusedNotExposed}})
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
// where the name's would and counts it. What is typed there goes to
// whoever wrote last (TestAnswerReachesWhoWroteLast). Without the grant, and
// once it is taken back, the name has its row as any other of the linked
// yard.
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
		"conversation, and what you write goes to mckean@north, who wrote last", "Write to mckean@north",
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

	// The user wrote last in this yard: the answer goes to them here, and
	// nothing to the other yard.
	must(t, st.Mail(ctx, me, "back at this one", "mckean"))
	v.stream()
	v.shows("what you write goes to mckean in this yard", "Write to mckean  ")
	v.hides("who wrote last")
	v.typed("which yard are you at?")
	v.key(tea.KeyEnter)
	if out, err := st.PeerOutbox(ctx, "north", 0); err != nil || len(out) != 0 {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	box, err := st.UserInbox(ctx, "mckean", 5, "mckean")
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
	later.shows("before the grant", "from this yard", "after the grant", "which yard are you at?", "for later",
		"what you write goes to mckean@north, who wrote last")
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

// northYard is the yard north itself, a second store: it has the user mckean
// and the yard of linkedYard as its peer yard, whose mail it takes for
// mckean and to which it sends mail. yardKey is that yard's key.
func northYard(t *testing.T) (north *store.Store, yardKey string) {
	t.Helper()
	api, north := storetest.NewOwned(t)
	ctx := context.Background()
	_, err := api.AddUser(ctx, "mckean", "mckean")
	must(t, err)
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "yard.key"))
	must(t, err)
	yardKey = yardkey.Public(k)
	_, err = api.AddPeer(ctx, store.Peer{Name: "yard", Key: yardKey, Calls: "the-yard"}, "mckean")
	must(t, err)
	_, err = api.AllowPeer(ctx, "yard", store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{"mckean"}}, "mckean")
	must(t, err)
	return north, yardKey
}

// carry hands what waits in from's outbox for the peer to over to the store
// to, as the two daemons do, and returns how many messages that was. key is
// to's key, as from knows it, and fromKey from's, as to does.
func carry(t *testing.T, from *store.Store, to, key string, into *store.Store, as, fromKey string) int {
	t.Helper()
	ctx := context.Background()
	page, err := from.PeerHandOver(ctx, to, key, 0, nil)
	must(t, err)
	if len(page.Messages) == 0 {
		return 0
	}
	taken, err := into.PeerReceive(ctx, as, fromKey, page.Messages)
	must(t, err)
	refused := map[uint64]store.Refusal{}
	for i, got := range taken {
		if got.Refused != "" {
			refused[page.Messages[i].Seq] = store.Refusal{Reason: got.Refused}
		}
	}
	if len(refused) > 0 {
		t.Fatalf("%s refused what %s sent: %v", as, to, refused)
	}
	_, err = from.PeerHandOver(ctx, to, key, page.Messages[len(page.Messages)-1].Seq, nil)
	must(t, err)
	return len(page.Messages)
}

// A person who answers in the conversation with a user reaches the one who
// wrote (yardr-g9xx: mckean, answering at one yard what he wrote from the
// other, wrote to himself where he sat). When a name trusted as the user
// wrote last, enter sends one mail to that name at its yard, as yardr reply
// does, and none to the user here; when the user wrote last in this yard,
// one to them here and none away. The first line and the empty input say
// which before anything is typed. Mail the link does not carry is refused
// in the store's words and written nowhere.
func TestAnswerReachesWhoWroteLast(t *testing.T) {
	st, own, key := linkedYard(t)
	north, yardKey := northYard(t)
	ctx := context.Background()
	_, err := st.AddUser(ctx, "mckean", "jo")
	must(t, err)
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{me},
		Trust: map[string]string{"mckean": "mckean"}})
	local := func() []store.Letter {
		t.Helper()
		box, err := st.UserInbox(ctx, "mckean", 0, "mckean")
		must(t, err)
		return box.Mail
	}
	queued := func() []store.PeerOut {
		t.Helper()
		out, err := st.PeerOutbox(ctx, "north", 0)
		must(t, err)
		return out
	}

	// Nobody wrote yet: it is the user in this yard.
	v := open(t, st, "mckean", 120, 30)
	v.shows("> mckean", "what you write goes to mckean in this yard", "Write to mckean  ")

	// mckean writes from north, and the view says where an answer goes
	// before anything is typed.
	_, err = north.MailPeer(ctx, me, "yard", "does chat work from here?", "mckean")
	must(t, err)
	if n := carry(t, north, "yard", yardKey, own, "north", key); n != 1 {
		t.Fatalf("north handed over %d messages", n)
	}
	v.stream()
	v.shows("does chat work from here?", "what you write goes to mckean@north, who wrote last", "Write to mckean@north")
	v.hides("in this yard,", "goes to mckean in this yard")

	v.typed("works")
	v.key(tea.KeyEnter)
	out := queued()
	if len(out) != 1 || out[0].Kind != store.PeerMail || !strings.Contains(string(out[0].Body), `"to":"mckean"`) ||
		!strings.Contains(string(out[0].Body), `"from":"ada"`) || !strings.Contains(string(out[0].Body), `"text":"works"`) {
		t.Fatalf("the outbox for north = %+v", out)
	}
	if got := local(); len(got) != 0 {
		t.Fatalf("the answer was written to mckean in this yard as well: %+v", got)
	}
	v.shows("ada to mckean@north", "works", "· queued for north: the daemon delivers it")
	v.stream() // the same mail from the stream is no second one
	if n := strings.Count(v.screen(), "works"); n != 1 {
		t.Fatalf("the answer shows %d times:\n%s", n, v.screen())
	}
	// His own answer leaves where the next goes as it was.
	v.shows("what you write goes to mckean@north, who wrote last")

	// It is in mckean's inbox at north, once, and this yard knows.
	if n := carry(t, own, "north", key, north, "yard", yardKey); n != 1 {
		t.Fatalf("this yard handed over %d messages", n)
	}
	box, err := north.UserInbox(ctx, "mckean", 0, "mckean")
	if err != nil || len(box.Mail) != 1 || box.Mail[0].From != "ada@yard" || box.Mail[0].Text != "works" {
		t.Fatalf("mckean's inbox at north = %+v, %v", box, err)
	}
	v.stream()
	v.shows("· delivered to north, which does not say when it is read")

	// A view opened later goes by the same mail, from the store.
	later := open(t, st, "mckean", 120, 30)
	later.shows("ada to mckean@north", "works", "what you write goes to mckean@north, who wrote last", "Write to mckean@north")

	// mckean writes in this yard: the next answer is written here, and
	// nothing is queued.
	must(t, st.Mail(ctx, me, "at the Mac now", "mckean"))
	v.stream()
	v.shows("what you write goes to mckean in this yard", "Write to mckean  ")
	v.hides("who wrote last")
	v.typed("welcome back")
	v.key(tea.KeyEnter)
	if got := local(); len(got) != 1 || got[0].From != me || got[0].Text != "welcome back" {
		t.Fatalf("mckean's inbox in this yard = %+v", got)
	}
	if out := queued(); len(out) != 0 {
		t.Fatalf("an answer to mckean in this yard queued %+v for north", out)
	}
	v.shows("welcome back", "· sent")
	v.hides("ada to mckean\n")

	// From north again, and the link carries no mail out now: the refusal
	// is the store's, the line is kept, and nothing is written to mckean
	// here instead.
	_, err = north.MailPeer(ctx, me, "yard", "and from here again?", "mckean")
	must(t, err)
	carry(t, north, "yard", yardKey, own, "north", key)
	allow(t, st, store.PeerAllow{NoSend: []string{"mail"}})
	v.stream()
	v.shows("what you write goes to mckean@north, who wrote last")
	v.typed("still works")
	v.key(tea.KeyEnter)
	v.shows("not sent to mckean@north: peer north: the link does not carry mail out", "> still works")
	if got := local(); len(got) != 1 {
		t.Fatalf("a refused answer was written to mckean in this yard: %+v", got)
	}
	if out := queued(); len(out) != 0 {
		t.Fatalf("a refused answer queued %+v", out)
	}
}

// Before the conversation with a user is read, who wrote last is not known:
// enter sends nothing and keeps the line, instead of writing to the user in
// this yard what is an answer to the other.
func TestNoAnswerBeforeTheConversationIsRead(t *testing.T) {
	st, own, key := linkedYard(t)
	ctx := context.Background()
	_, err := st.AddUser(ctx, "mckean", "jo")
	must(t, err)
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{me},
		Trust: map[string]string{"mckean": "mckean"}})
	northMail(t, own, key, "m1", "mckean", "from the other machine")
	state, err := Load(ctx, st, me)
	must(t, err)
	v := &view{t: t, st: st, last: state.Head}
	v.m = New(ctx, st, Options{Me: me, Select: "mckean"}, state)
	v.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	v.shows("it is being read to learn where your next answer goes", "Reading mckean")
	v.hides("goes to mckean in this yard", "Write to mckean  ")
	v.typed("works")
	v.key(tea.KeyEnter)
	v.shows("the conversation with mckean is still being read", "> works")
	if box, err := st.UserInbox(ctx, "mckean", 0, "mckean"); err != nil || len(box.Mail) != 0 {
		t.Fatalf("mckean's inbox in this yard = %+v, %v", box, err)
	}
	// Read, it goes to the one who wrote.
	v.do(v.m.open())
	v.key(tea.KeyEnter)
	if out, err := st.PeerOutbox(ctx, "north", 0); err != nil || len(out) != 1 {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	if box, err := st.UserInbox(ctx, "mckean", 0, "mckean"); err != nil || len(box.Mail) != 0 {
		t.Fatalf("mckean's inbox in this yard = %+v, %v", box, err)
	}
}

// A name trusted as the viewer is how the viewer spoke: what it wrote to a
// crew member of this yard, and what the crew member wrote to it, is in that
// crew member's row with the mail between the two here, from the store and
// from the stream alike, each under the address it went by. None of it is
// unread for the viewer, and what is typed in the row goes to the crew
// member.
func TestViewersTrustedNameIsInTheCrewMembersRow(t *testing.T) {
	st, own, key := linkedYard(t)
	ctx := context.Background()
	said(t, own, key, true, me, "yardmaster")
	_, err := st.PutCrew(ctx, store.CrewMember{Name: "chief", Runner: "herdr", Config: map[string]any{"kind": "claude", store.MailPeers: "north"}}, "jo")
	must(t, err)
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{"chief"}, Trust: map[string]string{me: me}})
	toChief := func(id, text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"to": "chief", "from": me, "text": text})
		taken, err := own.PeerReceive(ctx, "north", key, []store.PeerMessage{{ID: id, Kind: store.PeerMail, Body: body}})
		if err != nil || len(taken) != 1 || taken[0].Refused != "" {
			t.Fatalf("north's mail was not taken: %+v, %v", taken, err)
		}
	}
	must(t, st.Mail(ctx, "chief", "written here", me))
	toChief("m1", "written at north")
	_, err = st.MailPeer(ctx, me, "north", "answered there", "chief")
	must(t, err)

	v := open(t, st, "chief", 120, 30)
	v.shows("> chief", "written here", "ada@north\n", "written at north", "chief to ada@north\n", "answered there", "Write to chief")
	if v.m.Unread("chief") != 0 || v.cursor() != 0 {
		t.Fatalf("%d unread on chief's row, the cursor at %d", v.m.Unread("chief"), v.cursor())
	}
	// As it arrives, with the view open.
	toChief("m2", "more from north")
	_, err = st.MailPeer(ctx, me, "north", "and its answer", "chief")
	must(t, err)
	v.stream()
	v.shows("more from north", "and its answer")
	if n := strings.Count(v.screen(), "chief to ada@north\n"); n != 2 {
		t.Fatalf("%d letters to the name:\n%s", n, v.screen())
	}
	if v.m.Unread("chief") != 0 || v.cursor() != 0 {
		t.Fatalf("after the stream: %d unread on chief's row, the cursor at %d", v.m.Unread("chief"), v.cursor())
	}
	v.typed("and from here")
	v.key(tea.KeyEnter)
	if out, err := st.PeerOutbox(ctx, "north", 0); err != nil || len(out) != 2 {
		t.Fatalf("the outbox for north = %+v, %v", out, err)
	}
	v.shows("and from here", "· chief is not running")
}
