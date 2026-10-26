package store_test

import (
	"context"
	"maps"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// sessionYard is a yard whose operator is jo, with the users hal and ada
// and the herdr crew member yardmaster.
func sessionYard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"hal", "ada"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

// serveYardmaster serves users with the env files named, as crew set would.
func serveYardmaster(t *testing.T, s store.API, ctx context.Context, for_ string, envFor map[string]string) {
	t.Helper()
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	def := *r
	if def.Config == nil {
		def.Config = map[string]any{}
	}
	if for_ == "" {
		delete(def.Config, "for")
	} else {
		def.Config["for"] = for_
	}
	for k := range def.Config {
		if store.EnvForUser(k) != "" {
			delete(def.Config, k)
		}
	}
	for user, path := range envFor {
		def.Config["env_for_"+user] = path
	}
	if _, err := s.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatal(err)
	}
}

// The serving list and the per-user env keys hold what they name: for=
// takes users that exist (unset or empty: everyone), env_for_<user> a
// string on a herdr crew member's config for a user that exists, and
// anything else is refused where a crew definition is written.
func TestCrewServingKeys(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	crew := func(cfg map[string]any) error {
		_, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: cfg}, "jo")
		return err
	}
	for _, cfg := range []map[string]any{
		{"for": "hal"},
		{"for": "hal,ada"},
		{"for": ""},
		{"for": []any{"hal"}},
		{"env_for_hal": "/tmp/hal.env"},
		{"for": "hal", "env_for_hal": "/tmp/hal.env"},
	} {
		if err := crew(cfg); err != nil {
			t.Errorf("PutCrew %v = %v, want ok", cfg, err)
		}
	}
	for _, c := range []struct {
		want string
		cfg  map[string]any
	}{
		{`crew member yardmaster: for ghost: user ghost: not found`, map[string]any{"for": "ghost"}},
		{`crew member yardmaster: for ghost: user ghost: not found`, map[string]any{"for": "hal,ghost"}},
		{`crew member yardmaster: env_for_ghost: user ghost: not found`, map[string]any{"env_for_ghost": "/tmp/x.env"}},
		{`herdr runner needs config.env_for_hal as a string`, map[string]any{"env_for_hal": true}},
		{`unknown config key "env_for_"`, map[string]any{"env_for_": "/tmp/x.env"}},
		{`herdr runner needs config.for as names separated by commas, or a list`, map[string]any{"for": true}},
	} {
		if err := crew(c.cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("PutCrew %v = %v, want %s", c.cfg, err, c.want)
		}
	}
	// A group takes neither key.
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr", Config: map[string]any{"env_for_hal": "/x"}}, "jo"); err == nil ||
		!strings.Contains(err.Error(), `unknown config key "env_for_hal"`) {
		t.Errorf("PutGroup env_for_hal = %v, want unknown config key", err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr", Config: map[string]any{"for": "hal"}}, "jo"); err == nil ||
		!strings.Contains(err.Error(), `unknown config key "for"`) {
		t.Errorf("PutGroup for = %v, want unknown config key", err)
	}
	// An exec crew member's sessions start with no env of their own.
	if err := crew(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	def := *r
	def.Runner, def.Config = "exec", map[string]any{"env_for_hal": "/x"}
	if _, err := s.PutCrew(ctx, def, "jo"); err == nil || !strings.Contains(err.Error(), "it is read from a herdr one") {
		t.Errorf("PutCrew exec env_for_hal = %v, want herdr-only refusal", err)
	}
}

// The serving list and per-user login paths decide whose account a daemon
// session uses, so only the operator may change them. Unrelated crew config
// keeps its existing actor rule when those values are left alone.
func TestOnlyOperatorChangesConversationSettings(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	def := *r
	def.Config = map[string]any{"for": "hal"}
	if _, err := s.PutCrew(ctx, def, "hal"); err == nil ||
		err.Error() != "only the operator (jo) may change for on crew member yardmaster; this is hal" {
		t.Errorf("non-operator changing for = %v, want operator refusal", err)
	}

	def = *r
	def.Config = map[string]any{"for": "hal", "env_for_hal": "/tmp/hal.env"}
	if _, err := s.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatalf("operator setting conversation config: %v", err)
	}
	r, err = s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	def = *r
	def.Config["prompt"] = "/tmp/role.md"
	if _, err := s.PutCrew(ctx, def, "hal"); err != nil {
		t.Errorf("non-operator changing unrelated config = %v, want ok", err)
	}
}

// Serving is everyone where for= is unset or empty, else membership; a
// conversation is a served user with its env set.
func TestCrewServes(t *testing.T) {
	t.Parallel()
	r := store.CrewMember{}
	if !r.Serves("hal") || !r.Serves("jo") {
		t.Errorf("unset for= serves nobody: %+v", r)
	}
	r.Config = map[string]any{"for": ""}
	if !r.Serves("hal") {
		t.Errorf("empty for= serves nobody")
	}
	r.Config = map[string]any{"for": "hal, ada", "env_for_hal": "/h.env"}
	if !r.Serves("hal") || !r.Serves("ada") || r.Serves("jo") {
		t.Errorf("for=hal,ada serves wrongly")
	}
	if !r.HasConversation("hal") || r.HasConversation("ada") || r.HasConversation("jo") || r.HasConversation("") {
		t.Errorf("HasConversation wrong: %+v", r.ConversationUsers())
	}
	if got := r.ConversationUsers(); len(got) != 1 || got[0] != "hal" {
		t.Errorf("ConversationUsers = %v, want [hal]", got)
	}
	if got := r.Served(); len(got) != 2 || got[0] != "ada" || got[1] != "hal" {
		t.Errorf("Served = %v, want [ada hal]", got)
	}
}

// Mail to a crew member from a user it does not serve is refused at write,
// naming whom it serves; its own users' mail, and mail from anything that
// is not a user, still lands.
func TestMailRefusedForUnservedUser(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "hal", map[string]string{"hal": "/tmp/hal.env"})
	if err := s.Mail(ctx, "yardmaster", "from hal", "hal"); err != nil {
		t.Errorf("mail from hal = %v, want ok", err)
	}
	if err := s.Mail(ctx, "yardmaster", "from jo", "jo"); err == nil ||
		err.Error() != "mail to yardmaster from jo: yardmaster serves hal" {
		t.Errorf("mail from jo = %v, want refusal naming hal", err)
	}
	for _, from := range []string{"yard", "yardmaster"} {
		if err := s.Mail(ctx, "yardmaster", "from "+from, from); err != nil {
			t.Errorf("mail from %s = %v, want ok", from, err)
		}
	}
	serveYardmaster(t, s, ctx, "ada,hal", map[string]string{"hal": "/tmp/hal.env"})
	if err := s.Mail(ctx, "yardmaster", "from jo", "jo"); err == nil ||
		err.Error() != "mail to yardmaster from jo: yardmaster serves ada, hal" {
		t.Errorf("mail from jo = %v, want refusal naming ada, hal", err)
	}
	serveYardmaster(t, s, ctx, "", nil)
	for _, from := range []string{"hal", "jo", "ada"} {
		if err := s.Mail(ctx, "yardmaster", "from "+from, from); err != nil {
			t.Errorf("mail from %s with for= unset = %v, want ok", from, err)
		}
	}
}

// Mail from a user with a conversation reaches only its slice; acking one
// slice leaves the other unread; a user with no conversation is refused
// its slice, and reads the line's.
func TestInboxSlices(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	// The daemon's first touch makes the slice's row, starting at the
	// line cursor; until then hal's mail falls back to the line.
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "yardmaster", "from hal", "hal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "yardmaster", "from jo", "jo"); err != nil {
		t.Fatal(err)
	}
	line, err := s.Inbox(ctx, "yardmaster", 10)
	if err != nil {
		t.Fatal(err)
	}
	if line.Total != 1 || line.Events[0].Actor != "jo" {
		t.Fatalf("line inbox = %+v, want jo's mail alone", line.Events)
	}
	hal, err := s.InboxFor(ctx, "yardmaster", "hal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hal.Total != 1 || hal.Events[0].Actor != "hal" {
		t.Fatalf("hal slice = %+v, want hal's mail alone", hal.Events)
	}
	if _, err := s.InboxFor(ctx, "yardmaster", "ada", 10); err == nil ||
		!strings.Contains(err.Error(), "has no conversation session for ada") {
		t.Errorf("InboxFor ada = %v, want no-conversation refusal", err)
	}
	// Acking the line's slice leaves the conversation's unread, and back.
	if err := s.AckInbox(ctx, "yardmaster", line.Events[0].Seq); err != nil {
		t.Fatal(err)
	}
	if hal, err = s.InboxFor(ctx, "yardmaster", "hal", 10); err != nil || hal.Total != 1 {
		t.Fatalf("hal slice after line ack = %+v, %v; want 1 unread", hal, err)
	}
	if err := s.AckInboxFor(ctx, "yardmaster", "hal", hal.Events[0].Seq); err != nil {
		t.Fatal(err)
	}
	if hal, err = s.InboxFor(ctx, "yardmaster", "hal", 10); err != nil || hal.Total != 0 {
		t.Fatalf("hal slice after its ack = %+v, %v; want empty", hal, err)
	}
	if line, err = s.Inbox(ctx, "yardmaster", 10); err != nil || line.Total != 0 {
		t.Fatalf("line inbox = %+v, %v; want empty", line, err)
	}
}

// A conversation ack's positional cursor can pass another user's mail.
// Its mail_read event and the conversation's read state must stay scoped
// to the session that acknowledged it.
func TestMailReadReceiptStaysInSessionSlice(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "yardmaster", "from jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "yardmaster", "from hal", "hal"); err != nil {
		t.Fatal(err)
	}
	through, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AckInboxFor(ctx, "yardmaster", "hal", through); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsSince(ctx, through, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "mail_read" || events[0].Data["session"] != "hal" {
		t.Fatalf("ack receipt = %+v, want one mail_read for hal", events)
	}
	conversation, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation) != 1 || conversation[0].Read {
		t.Errorf("jo's conversation mail after hal's ack = %+v, want unread", conversation)
	}
}

// An upgrade with unread items replays none and hides none: a per-user
// slice starts at the crew cursor's position when the slice is made, and
// what the line already read stays read.
func TestInboxSliceUpgrade(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	if err := s.Mail(ctx, "yardmaster", "early from hal", "hal"); err != nil {
		t.Fatal(err)
	}
	// Before the conversation exists, hal's mail falls back to the line.
	line, err := s.Inbox(ctx, "yardmaster", 10)
	if err != nil || line.Total != 1 {
		t.Fatalf("line inbox before upgrade = %+v, %v; want hal's mail", line, err)
	}
	if err := s.Mail(ctx, "yardmaster", "late from hal", "hal"); err != nil {
		t.Fatal(err)
	}
	if err := s.AckInbox(ctx, "yardmaster", line.Events[0].Seq); err != nil {
		t.Fatal(err)
	}
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	// The daemon makes the slice on its next pass, starting at the line
	// cursor: the unread late mail moves to it, the read early one stays read.
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal"}); err != nil {
		t.Fatal(err)
	}
	hal, err := s.InboxFor(ctx, "yardmaster", "hal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hal.Total != 1 || hal.Events[0].Data["text"] != "late from hal" {
		t.Fatalf("hal slice after upgrade = %+v, want the late mail alone", hal.Events)
	}
	if line, err = s.Inbox(ctx, "yardmaster", 10); err != nil || line.Total != 0 {
		t.Fatalf("line inbox after upgrade = %+v, %v; want empty", line, err)
	}
}

// A conversation's row is made when the daemon first keeps it, starting
// its cursors at the line session's; a restarted daemon resumes each.
func TestConversationRowStartsAtLineCursor(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	if err := s.Mail(ctx, "yardmaster", "from hal", "hal"); err != nil {
		t.Fatal(err)
	}
	page, err := s.Inbox(ctx, "yardmaster", 10)
	if err != nil {
		t.Fatal(err)
	}
	// The line acks past hal's mail positionally without seeing it.
	if err := s.AckInbox(ctx, "yardmaster", page.Last); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conversations) != 0 {
		t.Fatalf("conversations before any start = %v, want none", r.Conversations)
	}
	handle := map[string]any{"agent": "ym-hal", "pane": "p1"}
	if err := s.CrewStartingFor(ctx, "yardmaster", "hal", handle); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", handle); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	cs := r.Conversations["hal"]
	if cs.Cursor != r.Cursor {
		t.Errorf("hal cursor = %d, line cursor = %d; want the slice starting at the line's", cs.Cursor, r.Cursor)
	}
	if !cs.Live() {
		t.Errorf("hal session state = %q, want running", cs.State)
	}
	if err := s.CrewStartedFor(ctx, "yardmaster", "ada", handle); err == nil ||
		!strings.Contains(err.Error(), "has no conversation session for ada") {
		t.Errorf("CrewStartedFor ada = %v, want no-conversation refusal", err)
	}
}

// paneCtx is a context calling from the test process itself, recorded as
// the shell of the session handle: what a command run in a crew pane
// looks like to the store.
func paneCtx(t *testing.T, ctx context.Context) (context.Context, map[string]any) {
	t.Helper()
	me := os.Getpid()
	desc, err := peer.Describe(me)
	if err != nil {
		t.Fatal(err)
	}
	return store.WithCaller(ctx, me), map[string]any{"shell_pid": float64(me), "shell_start": float64(desc.Start)}
}

// An ask's answer goes to the session that asked: hal's conversation for
// an ask from hal's pane, the line for an ask from nowhere.
func TestAskAnswerRoutedToAsker(t *testing.T) {
	t.Parallel()
	if os.Getenv("YARDR_TEST_RPC") != "" {
		t.Skip("one RPC test process cannot represent both a conversation pane and the line session")
	}
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	pane, shell := paneCtx(t, ctx)
	if err := s.CrewStartingFor(ctx, "yardmaster", "hal", shell); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	halSeq, err := s.AddAsk(pane, b.ID, store.Ask{To: "hal", Question: "which one?", Context: []string{"line one", "line two"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	lineSeq, err := s.AddAsk(ctx, b.ID, store.Ask{To: "jo", Question: "line asks", Context: []string{"line one", "line two"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	asks, err := s.WriterAsks(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint64]string{}
	for _, a := range asks {
		got[a.Ask] = a.Session
	}
	if got[halSeq] != "hal" || got[lineSeq] != "" {
		t.Errorf("ask sessions = %v, want hal's tagged and the line's not", got)
	}
	if err := s.AnswerAsk(ctx, b.ID, halSeq, 0, "this one", "hal", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerAsk(ctx, b.ID, lineSeq, 0, "that one", "jo", false); err != nil {
		t.Fatal(err)
	}
	hal, err := s.InboxFor(ctx, "yardmaster", "hal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hal.Total != 1 || store.AskSessionOf(hal.Events[0]) != "hal" {
		t.Fatalf("hal slice = %+v, want its answer alone", hal.Events)
	}
	line, err := s.Inbox(ctx, "yardmaster", 10)
	if err != nil {
		t.Fatal(err)
	}
	if line.Total != 1 || store.AskSessionOf(line.Events[0]) != "" {
		t.Fatalf("line inbox = %+v, want the line's answer alone", line.Events)
	}
	prime, err := s.CrewPrimeFor(ctx, "yardmaster", "hal")
	if err != nil {
		t.Fatal(err)
	}
	if len(prime.Asks) != 0 {
		t.Errorf("hal prime asks = %+v, want none (both answered)", prime.Asks)
	}
}

// An ask a crew member left open on its journal or on a memory is among its
// open asks like one on a plain bead, for the yard itself and for a viewer:
// a bead listing leaves those two out unless it keeps the knowledge beads,
// and the crew prime and `bead list --asked-by` read this list. An answered
// ask on the journal is not listed, as on any bead.
func TestWriterAsksListsAsksOnJournalAndMemory(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	crew, err := s.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := s.Remember(ctx, store.NewMemory{Title: "ask before moving a release"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.CreateBead(ctx, store.NewBead{Title: "b"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(bead string) uint64 {
		t.Helper()
		seq, err := s.AddAsk(ctx, bead, store.Ask{To: "jo", Question: "which one?", Context: []string{"line one", "line two"}}, crew.Name)
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	onPlain, onJournal, answered, onMemory := ask(plain.ID), ask(crew.Journal), ask(crew.Journal), ask(mem.ID)
	if err := s.AnswerAsk(ctx, crew.Journal, answered, 0, "this one", "jo", false); err != nil {
		t.Fatal(err)
	}
	want := map[string]uint64{plain.ID: onPlain, crew.Journal: onJournal, mem.ID: onMemory}
	read := func(asks []store.WriterAsk) map[string]uint64 {
		got := map[string]uint64{}
		for _, a := range asks {
			got[a.Bead] = a.Ask
		}
		return got
	}
	for _, viewer := range [][]string{nil, {"jo"}} {
		asks, err := s.WriterAsks(ctx, crew.Name, viewer...)
		if err != nil {
			t.Fatal(err)
		}
		if got := read(asks); len(asks) != 3 || !maps.Equal(got, want) {
			t.Errorf("viewer %q: asks = %+v, want the plain bead's, the journal's open one and the memory's", viewer, asks)
		}
	}
	// What the line is primed with, which reads the same list.
	prime, err := s.CrewPrime(ctx, crew.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(prime.Asks); len(prime.Asks) != 3 || !maps.Equal(got, want) {
		t.Errorf("line prime asks = %+v, want the same three", prime.Asks)
	}
}

// The journal is shared and each entry tagged with the session that wrote
// it; the handoff is per session, read back by the session that wrote it,
// and asks a session to restart its own next one.
func TestJournalHandoffPerSession(t *testing.T) {
	t.Parallel()
	if os.Getenv("YARDR_TEST_RPC") != "" {
		t.Skip("one RPC test process cannot represent both a conversation pane and the line session")
	}
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	pane, shell := paneCtx(t, ctx)
	if err := s.CrewStartingFor(ctx, "yardmaster", "hal", shell); err != nil {
		t.Fatal(err)
	}
	if err := s.Journal(pane, "yardmaster", store.NoteJournal, "hal learned", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := s.Journal(ctx, "yardmaster", store.NoteJournal, "line learned", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := s.Journal(pane, "yardmaster", store.NoteHandoff, "hal stands here", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Conversations["hal"].RestartRequested != "handoff" {
		t.Errorf("hal restart = %q, want handoff", r.Conversations["hal"].RestartRequested)
	}
	if r.RestartRequested != "" {
		t.Errorf("line restart = %q, want none", r.RestartRequested)
	}
	prime, err := s.CrewPrimeFor(ctx, "yardmaster", "hal")
	if err != nil {
		t.Fatal(err)
	}
	if prime.Handoff == nil || !strings.Contains(prime.Handoff.Text, "hal stands") {
		t.Fatalf("hal prime handoff = %+v, want hal's", prime.Handoff)
	}
	if len(prime.Since) != 0 {
		t.Errorf("hal prime since = %d entries, want none (handoff last)", len(prime.Since))
	}
	line, err := s.CrewPrime(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if line.Handoff != nil {
		t.Errorf("line prime handoff = %+v, want none", line.Handoff)
	}
	if len(line.Since) != 3 {
		t.Errorf("line prime since = %d entries, want all 3 (shared journal)", len(line.Since))
	}
	if err := s.Journal(ctx, "yardmaster", store.NoteHandoff, "line stands here", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	prime, err = s.CrewPrimeFor(ctx, "yardmaster", "hal")
	if err != nil {
		t.Fatal(err)
	}
	if prime.Handoff == nil || !strings.Contains(prime.Handoff.Text, "hal stands") {
		t.Errorf("hal prime handoff after line handoff = %+v, want still hal's", prime.Handoff)
	}
}

// A conversation the config no longer declares stays writable, so the
// daemon can stop what still runs of it; one never declared is refused.
func TestStaleConversationWritable(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	handle := map[string]any{"agent": "ym-hal", "pane": "p1"}
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", handle); err != nil {
		t.Fatal(err)
	}
	serveYardmaster(t, s, ctx, "", nil)
	if _, err := s.InboxFor(ctx, "yardmaster", "hal", 1); err == nil {
		t.Errorf("InboxFor stale hal = ok, want no-conversation refusal")
	}
	if err := s.CrewStoppedFor(ctx, "yardmaster", "hal", "undeclared"); err != nil {
		t.Errorf("CrewStoppedFor stale hal = %v, want ok", err)
	}
	if err := s.CrewStatusFor(ctx, "yardmaster", "ada", "idle"); err == nil {
		t.Errorf("CrewStatusFor never-declared ada = ok, want refusal")
	}
}

// A context nudge is recorded with its reading and a crew_context_asked
// event; a repeated reading writes nothing, and the next session starts
// unasked and unread.
func TestContextAskedAndReadings(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContext(ctx, "yardmaster", 50, 200000); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.ContextAskedAt != nil || r.ContextPct != 50 || r.ContextWindow != 200000 || r.ContextAt == nil {
		t.Fatalf("reading = %+v", r.CrewSession)
	}
	rev := r.Revision
	if err := s.SetContext(ctx, "yardmaster", 50, 200000); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.Revision != rev {
		t.Fatal("a repeated reading wrote")
	}
	if err := s.MarkContextAsked(ctx, "yardmaster", 60, 200000); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetCrew(ctx, "yardmaster")
	if r.ContextAskedAt == nil || r.ContextPct != 60 {
		t.Fatalf("after the ask: %+v", r.CrewSession)
	}
	evs, err := s.Events(ctx, r.Journal, 100)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, e := range evs {
		if e.Kind == "crew_context_asked" {
			asked++
			if e.Data["crew"] != "yardmaster" || e.Data["pct"] != float64(60) || e.Data["window"] != float64(200000) {
				t.Fatalf("crew_context_asked = %+v", e.Data)
			}
		}
	}
	if asked != 1 {
		t.Fatalf("crew_context_asked events = %d, want 1", asked)
	}
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster-2"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextAskedAt != nil || r.ContextPct != 0 || r.ContextAt != nil {
		t.Fatalf("the new session starts asked: %+v", r.CrewSession)
	}
}
