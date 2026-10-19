package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// userYard is a yard as yardr init leaves it, with operator jo and the
// yardmaster, and nothing of the test's own session in the environment.
func userYard(t *testing.T) store.API {
	t.Helper()
	return userYardOn(t, storetest.New(t))
}

// userYardOn is userYard on the store st.
func userYardOn(t *testing.T, st store.API) store.API {
	t.Helper()
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	// Not in a bead's worktree, whose session a command there acts as.
	t.Chdir(t.TempDir())
	if out, err := as(t, st, "jo", "init", "--harness", "claude", "--operator", "jo"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return st
}

// as runs yardr with args as actor. The actor is given as the flag: the
// root command's --actor is what a run of yardr goes by.
func as(t *testing.T, st store.API, actor string, args ...string) (string, error) {
	t.Helper()
	return run(t, (&app{st: st}).rootCmd(), append(args, "--actor", actor)...)
}

func mustAs(t *testing.T, st store.API, actor string, args ...string) string {
	t.Helper()
	out, err := as(t, st, actor, args...)
	if err != nil {
		t.Fatalf("%s: yardr %s: %v\n%s", actor, strings.Join(args, " "), err, out)
	}
	return out
}

// mustAsErr is mustAs with the run's error stream in errs.
func mustAsErr(t *testing.T, st store.API, actor string, errs io.Writer, args ...string) string {
	t.Helper()
	out, err := run(t, (&app{st: st, stderr: errs}).rootCmd(), append(args, "--actor", actor)...)
	if err != nil {
		t.Fatalf("%s: yardr %s: %v\n%s", actor, strings.Join(args, " "), err, out)
	}
	return out
}

func refusedAs(t *testing.T, st store.API, actor, want string, args ...string) {
	t.Helper()
	if out, err := as(t, st, actor, args...); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: yardr %s = %v, want %q\n%s", actor, strings.Join(args, " "), err, want, out)
	}
}

// yardr init makes the operator a user. Only the operator adds and
// removes users, never their own, and crew members and users share their
// names.
func TestUserCommands(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	if out := mustAs(t, st, "jo", "user", "list"); !strings.Contains(out, "jo") || !strings.Contains(out, "operator") {
		t.Fatalf("init made no user of the operator:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "user", "add", "ada"); out != "user ada added\n" {
		t.Fatalf("add: %q", out)
	}
	var views []userView
	if err := json.Unmarshal([]byte(mustAs(t, st, "ada", "user", "list", "--json")), &views); err != nil ||
		len(views) != 2 || views[0].Name != "ada" || views[0].Operator || views[1].Name != "jo" || !views[1].Operator {
		t.Fatalf("list --json = %+v, %v", views, err)
	}

	// A crew member's session, another user and a bead's agent: none of them
	// says who is a party of the yard.
	for _, who := range []string{"yardmaster", "ada", "yard-10-new"} {
		refusedAs(t, st, who, "only the operator (jo) may add a user; this is "+who, "user", "add", "grace")
		refusedAs(t, st, who, "only the operator (jo) may remove a user; this is "+who, "user", "rm", "ada")
	}
	if _, err := st.GetUser(ctx, "grace"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused user was added: %v", err)
	}
	refusedAs(t, st, "jo", "is the yard's operator and is not removed", "user", "rm", "jo")

	// One name, one party, both ways.
	refusedAs(t, st, "jo", "that is a crew member's name", "user", "add", "yardmaster")
	refusedAs(t, st, "jo", "crew member ada: that is a user's name", "crew", "add", "ada", "--dir", t.TempDir(), "--set", "kind=claude")
	refusedAs(t, st, "jo", "that is a crew member's name", "settings", "set", "operator", "yardmaster")

	if err := st.Mail(ctx, "ada", "hello", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if out := mustAs(t, st, "jo", "user", "show", "ada"); !strings.Contains(out, "inbox: 1 unread from yardmaster") || strings.Contains(out, "operator") {
		t.Fatalf("show ada:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "user", "show", "jo"); !strings.Contains(out, "the yard's operator") || !strings.Contains(out, "inbox: nothing unread") {
		t.Fatalf("show jo:\n%s", out)
	}
	if _, err := as(t, st, "jo", "user", "show", "nobody"); exitCode(err) != 3 {
		t.Fatalf("show of no user: %v", err)
	}

	// Another operator gets their user with the setting; the one before
	// can then be removed, by the new operator.
	mustAs(t, st, "jo", "settings", "set", "operator", "sam")
	if _, err := st.GetUser(ctx, "sam"); err != nil {
		t.Fatalf("settings set operator added no user: %v", err)
	}
	refusedAs(t, st, "jo", "only the operator (sam) may remove a user", "user", "rm", "ada")
	if out := mustAs(t, st, "sam", "user", "rm", "jo"); out != "user jo removed\n" {
		t.Fatalf("rm: %q", out)
	}
}

// A crew member's session and a user mail a user; a bead's agent does not.
// Each user reads their own mail with yardr inbox, and --ack <seq> marks it read.
func TestMailAUser(t *testing.T) {
	st := userYard(t)
	mustAs(t, st, "jo", "user", "add", "ada")

	mustAs(t, st, "jo", "mail", "yardmaster", "deploys are frozen")
	t.Setenv("YARDR_CREW", "yardmaster")
	if out := mustAs(t, st, "yardmaster", "mail", "jo", "noted, until when?"); out != "mail sent to jo\n" {
		t.Fatalf("mail to a user: %q", out)
	}
	mustAs(t, st, "yardmaster", "mail", "ada", "welcome")
	// In the yardmaster's session yardr inbox is the yardmaster's, as before.
	if out := mustAs(t, st, "yardmaster", "inbox"); !strings.Contains(out, "mail from jo: deploys are frozen") || strings.Contains(out, "welcome") {
		t.Fatalf("the yardmaster's inbox:\n%s", out)
	}
	t.Setenv("YARDR_CREW", "")
	mustAs(t, st, "ada", "mail", "jo", "from one user to another")

	// A bead's agent is told where its word to a person goes. Its mail to
	// a crew member is taken as before.
	t.Setenv("YARDR_BEAD", "yard-10")
	t.Setenv("YARDR_SESSION", "s1")
	for _, want := range []string{"an agent working a bead does not mail a user", "yardr bead note yard-10", "yardr bead done yard-10 --outcome question"} {
		refusedAs(t, st, "yard-10-new", want, "mail", "jo", "may I?")
	}
	mustAs(t, st, "yard-10-new", "mail", "yardmaster", "yard-10: the tests are red on main")
	t.Setenv("YARDR_BEAD", "")
	t.Setenv("YARDR_SESSION", "")
	refusedAs(t, st, "jo", "not found", "mail", "nobody", "hi")

	// Each user's inbox holds their own mail only.
	jo := mustAs(t, st, "jo", "inbox")
	if !strings.Contains(jo, "mail from yardmaster: noted, until when?") || !strings.Contains(jo, "mail from ada: from one user to another") ||
		strings.Contains(jo, "welcome") || strings.Contains(jo, "may I?") || strings.Contains(jo, "(read)") {
		t.Fatalf("jo's inbox:\n%s", jo)
	}
	if ada := mustAs(t, st, "ada", "inbox"); !strings.Contains(ada, "mail from yardmaster: welcome") || strings.Contains(ada, "noted") {
		t.Fatalf("ada's inbox:\n%s", ada)
	}
	refusedAs(t, st, "pat", "pat is no user", "inbox")
	// Named with --crew it is that crew member's, whoever asks.
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "mail from jo: deploys are frozen") {
		t.Fatalf("inbox --crew yardmaster:\n%s", out)
	}

	// --ack through the first of two, then the rest.
	mailBox, err := st.UserInbox(context.Background(), "jo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if out := mustAs(t, st, "jo", "inbox", "-n", "1", "--ack", fmt.Sprint(mailBox.Mail[0].Seq)); !strings.Contains(out, "noted, until when?") ||
		!strings.Contains(out, "… 1 more unread") || !strings.Contains(out, "acknowledged 1") {
		t.Fatalf("inbox -n 1 --ack:\n%s", out)
	}
	// Unread first, then what was read.
	out := mustAs(t, st, "jo", "inbox")
	unread, read := strings.Index(out, "mail from ada: from one user"), strings.Index(out, "mail from yardmaster (read): noted")
	if unread < 0 || read < unread {
		t.Fatalf("unread is not first:\n%s", out)
	}
	mailBox, err = st.UserInbox(context.Background(), "jo", 1)
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--ack", fmt.Sprint(mailBox.Last))
	if out := mustAs(t, st, "jo", "inbox"); !strings.HasPrefix(out, "Mail: nothing unread; the latest read:\n") || strings.Count(out, "(read)") != 2 {
		t.Fatalf("after the ack:\n%s", out)
	}
	var box store.Mailbox
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--json")), &box); err != nil || box.Unread != 0 || len(box.Mail) != 2 || !box.Mail[0].Read {
		t.Fatalf("inbox --json = %+v, %v", box, err)
	}
	if out := mustAs(t, st, "ada", "inbox"); !strings.Contains(out, "welcome") || strings.Contains(out, "(read)") {
		t.Fatalf("jo's ack read ada's mail:\n%s", out)
	}
}

// A login name that is nobody's: with the actor unnamed (neither --actor
// nor $YARDR_ACTOR) and the login name neither a user nor a crew member, a
// command that acts as one (inbox, mail, reply, chat) refuses in one line:
// who acts and where that came from, the yard's users, and both ways to
// act as one (yardr-sang).
func TestLoginNameNobodyHas(t *testing.T) {
	st, opened := chatYard(t)
	t.Setenv("USER", "kloni")
	login := func(args ...string) (string, error) {
		t.Helper()
		return run(t, (&app{st: st}).rootCmd(), args...)
	}
	line := "kloni (from $USER) is neither a user nor a crew member of this yard; its users are jo. " +
		"Act as one: --actor jo or export YARDR_ACTOR=jo"
	for _, args := range [][]string{
		{"inbox"},
		{"mail", "jo", "hello"},
		{"mail", "--with", "jo"},
		{"reply", "1", "hello"},
		{"chat"},
	} {
		out, err := login(args...)
		if err == nil || err.Error() != line {
			t.Errorf("USER=kloni yardr %s = %q, %v, want the line", strings.Join(args, " "), out, err)
		} else if c := exitCode(err); c == 0 {
			t.Errorf("USER=kloni yardr %s exits %d, want non-zero", strings.Join(args, " "), c)
		}
	}
	if len(*opened) != 0 {
		t.Errorf("the chat opened for nobody: %v", *opened)
	}
	// With several users the line lists them all.
	mustAs(t, st, "jo", "user", "add", "ada")
	two := "kloni (from $USER) is neither a user nor a crew member of this yard; its users are ada, jo. " +
		"Act as one: --actor ada or export YARDR_ACTOR=ada"
	if out, err := login("inbox"); err == nil || err.Error() != two {
		t.Errorf("two users: %q, %v, want %q", out, err, two)
	}
	// A named actor behaves as before, known or not: the line is for the
	// unnamed actor only.
	if out := mustAs(t, st, "jo", "inbox"); !strings.Contains(out, "Mail:") {
		t.Fatalf("jo's inbox:\n%s", out)
	}
	refusedAs(t, st, "pat", "pat is no user", "inbox")
	t.Setenv("USER", "yardmaster")
	if out, err := login("inbox"); err == nil || !strings.Contains(err.Error(), "yardmaster is no user") ||
		strings.Contains(err.Error(), "from $USER") {
		t.Errorf("a crew member's login: %q, %v", out, err)
	}
	t.Setenv("USER", "jo")
	if out, err := login("inbox"); err != nil || !strings.Contains(out, "Mail:") {
		t.Errorf("a user's login: %q, %v", out, err)
	}
	t.Setenv("USER", "kloni")
	t.Setenv("YARDR_ACTOR", "kloni")
	if out, err := login("inbox"); err == nil || !strings.Contains(err.Error(), "kloni is no user") ||
		strings.Contains(err.Error(), "from $USER") {
		t.Errorf("YARDR_ACTOR=kloni: %q, %v", out, err)
	}
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "")
	if err := (&app{}).unknownLogin(context.Background(), st); err != nil {
		t.Errorf("without USER, OS user fallback: %v, want no login-specific message", err)
	}
}

// yardr mail --with prints a conversation from either side, oldest first,
// with the read state; --json and --after are what a view reads it with.
func TestMailWith(t *testing.T) {
	st := userYard(t)
	for _, m := range [][3]string{{"jo", "yardmaster", "deploys are frozen"}, {"yardmaster", "jo", "until when?"}, {"jo", "yardmaster", "friday"}} {
		mustAs(t, st, m[0], "mail", m[1], m[2])
	}
	// The yardmaster acknowledges the first mail, jo theirs.
	t.Setenv("YARDR_CREW", "yardmaster")
	crewBox, err := st.InboxFor(context.Background(), "yardmaster", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "yardmaster", "inbox", "-n", "1", "--ack", fmt.Sprint(crewBox.Events[0].Seq))
	t.Setenv("YARDR_CREW", "")
	mailBox, err := st.UserInbox(context.Background(), "jo", 1)
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--ack", fmt.Sprint(mailBox.Last))

	out := mustAs(t, st, "jo", "mail", "--with", "yardmaster")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("mail --with yardmaster:\n%s", out)
	}
	for i, want := range []string{"read    jo: deploys are frozen", "read    yardmaster: until when?", "unread  jo: friday"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end in %q", i, lines[i], want)
		}
	}
	// The yardmaster's session asks with the user's name and reads the same.
	if theirs := mustAs(t, st, "yardmaster", "mail", "--with", "jo"); theirs != out {
		t.Fatalf("from the yardmaster's side:\n%s\nfrom jo's:\n%s", theirs, out)
	}

	var raw []map[string]any
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--json")), &raw); err != nil || len(raw) != 3 {
		t.Fatalf("--json: %v, %v", raw, err)
	}
	for _, k := range []string{"seq", "at", "from", "to", "text", "read"} {
		if _, ok := raw[1][k]; !ok {
			t.Errorf("--json lacks %q: %v", k, raw[1])
		}
	}
	if len(raw[1]) != 6 || raw[1]["from"] != "yardmaster" || raw[1]["to"] != "jo" || raw[1]["read"] != true || raw[2]["read"] != false {
		t.Fatalf("--json = %v", raw)
	}
	seq := fmt.Sprint(uint64(raw[1]["seq"].(float64)))
	after := mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--after", seq)
	if !strings.Contains(after, "jo: friday") || strings.Contains(after, "until when?") {
		t.Fatalf("--after %v:\n%s", seq, after)
	}
	var none []store.Letter
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--after", "99999", "--json")), &none); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("--after the last, --json = %v, %v", none, err)
	}
	if out := mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--after", "99999"); !strings.Contains(out, "no mail between jo and yardmaster after 99999") {
		t.Fatalf("nothing newer: %q", out)
	}

	if _, err := as(t, st, "jo", "mail", "--with", "nobody"); exitCode(err) != 3 || !strings.Contains(err.Error(), "no crew member and no user of that name") {
		t.Fatalf("--with nobody: %v", err)
	}
	// A crew member of another yard is read back by its link (peerrename_test.go),
	// and a yard this one is not linked with has none.
	if _, err := as(t, st, "jo", "mail", "--with", "yardmaster@north"); exitCode(err) != 3 || !strings.Contains(err.Error(), "--with yardmaster@north: peer north: not found") {
		t.Fatalf("--with a crew member of no peer: %v", err)
	}
	if _, err := as(t, st, "jo", "mail", "--with", "yardmaster", "and", "text"); err == nil {
		t.Fatal("--with took a text to send")
	}
	if _, err := as(t, st, "jo", "mail", "yardmaster"); err == nil {
		t.Fatal("mail without a text and without --with was taken")
	}
}

// Nothing nudges a person: yardr doctor and bare yardr say what waits for
// the user who runs them, and nothing to anyone else.
func TestUnreadMailIsSaid(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "ada")
	doctor := func(actor string) (string, doctorReport) {
		t.Helper()
		out, err := as(t, st, actor, "doctor")
		if err != nil && !errors.Is(err, errCheckFailed) {
			t.Fatal(err)
		}
		js, _ := as(t, st, actor, "doctor", "--json")
		var rep doctorReport
		if err := json.Unmarshal([]byte(js), &rep); err != nil {
			t.Fatalf("doctor --json: %v\n%s", err, js)
		}
		return out, rep
	}
	out, rep := doctor("jo")
	wantText(t, out, "ok     mail        nothing unread for jo")
	if rep.State.Mail == nil || rep.State.Mail.User != "jo" || rep.State.Mail.Unread != 0 {
		t.Fatalf("mail = %+v", rep.State.Mail)
	}
	for _, m := range [][2]string{{"yardmaster", "one"}, {"ada", "two"}, {"yardmaster", "three"}} {
		if err := st.Mail(ctx, "jo", m[1], m[0]); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := st.Head(ctx)
	out, rep = doctor("jo")
	wantText(t, out, "note   mail        3 unread from yardmaster, ada for jo: ", " inbox reads them, --ack <seq> marks them read")
	if m := rep.State.Mail; m == nil || m.Unread != 3 || strings.Join(m.From, ",") != "yardmaster,ada" {
		t.Fatalf("mail = %+v", rep.State.Mail)
	}
	// Unread mail is no fault.
	for _, f := range rep.Faults {
		if strings.Contains(f, "mail") {
			t.Errorf("unread mail is a fault: %s", f)
		}
	}
	// Another user is told of their own, and who is no user of nothing.
	if out, rep = doctor("ada"); !strings.Contains(out, "nothing unread for ada") || rep.State.Mail.User != "ada" {
		t.Fatalf("ada's doctor:\n%s", out)
	}
	if out, rep = doctor("yardmaster"); strings.Contains(out, "  mail  ") || rep.State.Mail != nil {
		t.Fatalf("the yardmaster's doctor speaks of a user's mail:\n%s", out)
	}

	bare := mustAs(t, st, "jo")
	if !strings.Contains(bare, "Usage:") || !strings.HasSuffix(bare, "\nMail for jo: 3 unread from yardmaster, ada (yardr inbox).\n") {
		t.Fatalf("bare yardr:\n%s", bare)
	}
	for _, who := range []string{"ada", "yardmaster", "pat"} {
		if out := mustAs(t, st, who); strings.Contains(out, "unread") {
			t.Fatalf("bare yardr as %s:\n%s", who, out)
		}
	}
	if after, _ := st.Head(ctx); after != before {
		t.Fatalf("reading wrote to the store: head %d, was %d", after, before)
	}

	mailBox, err := st.UserInbox(ctx, "jo", 1)
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--ack", fmt.Sprint(mailBox.Last))
	if out, _ = doctor("jo"); !strings.Contains(out, "nothing unread for jo") {
		t.Fatalf("after the ack:\n%s", out)
	}
	if bare := mustAs(t, st, "jo"); strings.Contains(bare, "unread") {
		t.Fatalf("bare yardr after the ack:\n%s", bare)
	}
}

// operatorNamed is a store whose operator setting names a crew member, as a
// yard from before users may have it: no command gets a yard there now.
type operatorNamed struct {
	store.API
	name string
}

func (o operatorNamed) Settings(ctx context.Context) (map[string]string, error) {
	all, err := o.API.Settings(ctx)
	if err == nil {
		all[store.OperatorSetting] = o.name
	}
	return all, err
}

// yardr doctor notes the names that are no party of the yard: a member of
// a group who is neither user nor crew member, which stays allowed, and a
// crew member who has the operator's name.
func TestDoctorNotesNames(t *testing.T) {
	st := userYard(t)
	out, err := as(t, st, "jo", "doctor")
	if !errors.Is(err, errCheckFailed) || strings.Contains(out, "  names  ") {
		t.Fatalf("a fresh yard has a note on names: %v\n%s", err, out)
	}
	// Members are taken as they are named.
	mustAs(t, st, "jo", "group", "add", "humans", "--runner", "manual", "--members", "jo,yardmaster,pat,sam")
	mustAs(t, st, "jo", "group", "add", "mergers", "--runner", "manual", "--members", "jo")
	out, _ = as(t, st, "jo", "doctor")
	wantText(t, out, "note   names       group humans: pat, sam are neither a user nor a crew member, so nobody of that name can be mailed (",
		" user add <name>)")
	if strings.Contains(out, "group mergers") {
		t.Fatalf("a group of users is noted:\n%s", out)
	}
	js, _ := as(t, st, "jo", "doctor", "--json")
	var rep doctorReport
	if err := json.Unmarshal([]byte(js), &rep); err != nil || len(rep.State.Parties) != 1 {
		t.Fatalf("parties = %+v, %v", rep.State, err)
	}
	for _, f := range rep.Faults {
		if strings.Contains(f, "names") {
			t.Errorf("a name is a fault: %s", f)
		}
	}
	mustAs(t, st, "jo", "user", "add", "pat")
	if out, _ = as(t, st, "jo", "doctor"); !strings.Contains(out, "group humans: sam is neither a user nor a crew member") {
		t.Fatalf("after adding pat:\n%s", out)
	}

	out, _ = as(t, operatorNamed{st, "yardmaster"}, "jo", "doctor")
	wantText(t, out, "note   names       crew member yardmaster has the operator's name, so the operator has no user and no inbox: mail to yardmaster is the crew member's. Rename one of the two: ",
		" --actor yardmaster settings set operator <new name> gives the operator a name of their own and adds their user (as yardmaster, since settings are the operator's to change)")
}
