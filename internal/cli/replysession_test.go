package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// sessionPane is the pid of the pane the tests below make up for a bead's
// session: no process has it, so the store knows the pane by its number while
// its session lives, and none of the test's own calls is taken for the
// session's, as they would be over the socket if the pane were the test's.
const sessionPane = 1 << 30

// sessionMailYard is a replyMailYard with the bead yd-1 and the session on
// it, whose agent, yd-1-new, mailed the yardmaster; the last result is the
// number of that mail. The run acts as the yardmaster.
func sessionMailYard(t *testing.T) (*app, store.API, *store.Session, string) {
	t.Helper()
	a, st, owned := replyMailYardOwned(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Stage: "new"}, "jo"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Claim(ctx, "yd-1", "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "yd-1-new", "shell_pid": sessionPane}); err != nil {
		t.Fatal(err)
	}
	// In the owning process: over the socket the caller is the test itself.
	if err := owned.Mail(store.WithCaller(ctx, sessionPane), "yardmaster", "may I narrow it to the shed?", "yd-1-new"); err != nil {
		t.Fatal(err)
	}
	yardEnv(t, st)["YARDR_CREW"] = "yardmaster"
	return a, st, sess, mailSeq(t, st)
}

// notesOf are the notes of the bead yd-1.
func notesOf(t *testing.T, st store.API) []store.Note {
	t.Helper()
	notes, err := st.Notes(context.Background(), "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

// TestReplyToSessionMail answers mail from a bead's agent, whose actor is no
// inbox: the reply is a note on its bead, written as yardr bead note writes
// one, so its live session is told of it. No mail is sent.
func TestReplyToSessionMail(t *testing.T) {
	t.Parallel()
	a, st, sess, seq := sessionMailYard(t)
	ctx := context.Background()

	out, err := run(t, a.replyCmd(), seq, "Yes: only the shed.")
	if err != nil {
		t.Fatal(err)
	}
	notes := notesOf(t, st)
	if len(notes) != 1 {
		t.Fatalf("notes after the reply = %+v", notes)
	}
	n := notes[0]
	if want := fmt.Sprintf("note %d on yd-1 (session %s will be told)\n", n.Seq, sess.ID); out != want {
		t.Fatalf("reply = %q, want %q", out, want)
	}
	if n.Author != "yardmaster" || n.Text != "Yes: only the shed." || n.Notify != sess.ID || !n.NotifyLive {
		t.Fatalf("the note = %+v, want the yardmaster's words, for the live session %s", n, sess.ID)
	}
	// The only mail is the agent's own: nobody was written to.
	if mails, err := st.EventsOfKind(ctx, "mail", 10); err != nil || len(mails) != 1 {
		t.Fatalf("mail events after the reply = %+v, %v", mails, err)
	}

	// From stdin, as JSON.
	c := a.replyCmd()
	c.SetIn(strings.NewReader("  and the fence too\n"))
	a.json = true
	out, err = run(t, c, seq, "-")
	a.json = false
	var res map[string]any
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["bead"] != "yd-1" || res["crew"] != "yardmaster" ||
		res["noted"] != "ok" || res["notify"] != sess.ID || fmt.Sprint(res["seq"]) != seq {
		t.Fatalf("json reply = %q, %v", out, err)
	}
	notes = notesOf(t, st)
	if len(notes) != 2 || notes[1].Text != "and the fence too" || fmt.Sprint(res["note"]) != fmt.Sprint(notes[1].Seq) {
		t.Fatalf("notes after the second reply = %+v, reply %v", notes, res)
	}
}

// TestReplyToEndedSessionMail answers mail from a session that has ended
// since: the reply is still a note on its bead, and says no session runs on
// it. A session that works the bead later is the one told of the next.
func TestReplyToEndedSessionMail(t *testing.T) {
	t.Parallel()
	a, st, sess, seq := sessionMailYard(t)
	ctx := context.Background()
	if err := st.EndSession(ctx, sess.ID, "died", "x"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.replyCmd(), seq, "Yes: only the shed.")
	if err != nil {
		t.Fatal(err)
	}
	notes := notesOf(t, st)
	if len(notes) != 1 {
		t.Fatalf("notes after the reply = %+v", notes)
	}
	n := notes[0]
	if want := fmt.Sprintf("note %d on yd-1 (no session runs on it now; the note waits on the bead)\n", n.Seq); out != want {
		t.Fatalf("reply = %q, want %q", out, want)
	}
	if n.Author != "yardmaster" || n.Text != "Yes: only the shed." || n.Notify != "" {
		t.Fatalf("the note = %+v, want the yardmaster's words, for no session", n)
	}

	next, err := st.Claim(ctx, "yd-1", "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.replyCmd(), seq, "And the fence too.")
	if err != nil {
		t.Fatal(err)
	}
	notes = notesOf(t, st)
	if want := fmt.Sprintf("note %d on yd-1 (session %s will be told)\n", notes[len(notes)-1].Seq, next.ID); out != want {
		t.Fatalf("reply once the bead is worked again = %q, want %q", out, want)
	}
}

// TestReplyToSessionMailOnlyByTheCrewOrOperator refuses a reply as the crew
// member from anyone else, a bead's agent included, and writes no note: the
// note would be the crew member's word on the bead.
func TestReplyToSessionMailOnlyByTheCrewOrOperator(t *testing.T) {
	t.Parallel()
	a, st, _, seq := sessionMailYard(t)
	yardEnv(t, st)["YARDR_CREW"] = ""
	a.actor = "yd-1-new"

	_, err := run(t, a.replyCmd(), seq, "Yes: all of it.", "--crew", "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster; this is yd-1-new") {
		t.Fatalf("a bead's agent replying as the yardmaster = %v", err)
	}
	if notes := notesOf(t, st); len(notes) != 0 {
		t.Fatalf("a refused reply wrote %+v", notes)
	}
}

// TestReplyToCrewMail answers a crew member's mail by mail to it, as before
// a bead's session had a bead to be answered on: it names none.
func TestReplyToCrewMail(t *testing.T) {
	t.Parallel()
	a, st := replyMailYard(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	yardEnv(t, st)["YARDR_CREW"] = "yardmaster"
	if err := st.Mail(ctx, "yardmaster", "is the shed painted?", "clerk"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.replyCmd(), mailSeq(t, st), "Painted.")
	if err != nil || out != "mail sent to clerk\n" {
		t.Fatalf("reply = %q, %v", out, err)
	}
	page, err := st.Inbox(ctx, "clerk", 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Data["text"] != "Painted." || page.Events[0].Actor != "yardmaster" {
		t.Fatalf("clerk's inbox = %+v, %v", page, err)
	}
}
