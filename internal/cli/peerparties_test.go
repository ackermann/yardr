package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/store"
)

// yardr user list has, after this yard's users, whom the linked yards
// expose to it: <name>@<peer>, a person or a crew member, whether mail
// reaches them and their yard's line of what they do. A user writes to one
// with yardr mail <name>@<peer>, with no setting of their own: the link's
// send mail is the grant. The answer lands in the user's inbox, and yardr
// chat opens on the address.
func TestUserListAndMailToThoseALinkedYardExposes(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	ctx := context.Background()
	north.a.env = testEnv(t, north.home)
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	south.a.env = testEnv(t, south.home)
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", sInvite)
	south.must(t, "add", "up-north", nInvite)
	if _, err := north.st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "role.md"), []byte("# Shapes the beads\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := south.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: dir, Config: map[string]any{"prompt": "role.md"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	as := func(y *cliYard, who string, cmd string, args ...string) (string, error) {
		t.Helper()
		y.a.actor = who
		defer func() { y.a.actor = "jo" }()
		c := map[string]func() (string, error){
			"user":  func() (string, error) { return run(t, y.a.userCmd(), args...) },
			"mail":  func() (string, error) { return run(t, y.a.mailCmd(), args...) },
			"inbox": func() (string, error) { return run(t, y.a.inboxCmd(), args...) },
			"chat":  func() (string, error) { return run(t, y.a.chatCmd(), args...) },
		}[cmd]
		return c()
	}

	// South has said nothing yet: north lists its own and no more.
	if out, err := as(north, "ada", "user", "list"); err != nil || strings.Contains(out, "LINKED YARD") || strings.Contains(out, "@south") {
		t.Fatalf("user list before south said whom it exposes = %q, %v", out, err)
	}
	// A user's mail needs the link's grant and nothing of their own.
	if _, err := as(north, "ada", "mail", "yardmaster@south", "hello"); err == nil || !strings.Contains(err.Error(), "yardr peer allow south --send mail") {
		t.Fatalf("a user's mail over a link that carries none = %v", err)
	}
	north.must(t, "allow", "south", "--send", "mail", "--receive", "mail", "--expose", "ada")
	south.must(t, "allow", "up-north", "--expose", "yardmaster,jo")
	out, err := as(north, "ada", "mail", "yardmaster@south", "is the depot on Go 1.27?")
	if err != nil || !strings.Contains(out, "mail for yardmaster@south queued") {
		t.Fatalf("a user's mail = %q, %v", out, err)
	}
	carry(t, north, south)

	// South's link takes no mail yet, and the list says whose grant that is.
	out, err = as(north, "ada", "user", "list")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "NAME", "ada", "\n\nOF A LINKED YARD", "IS", "MAIL", "ROLE",
		"jo@south", "person  no (south takes none from this yard)  operator",
		"yardmaster@south  crew    no (south takes none from this yard)  Shapes the beads")
	if strings.Index(out, "ada") > strings.Index(out, "jo@south") {
		t.Fatalf("the linked yard's are not after this yard's own:\n%s", out)
	}
	var views []userView
	north.a.json = true
	js, err := as(north, "ada", "user", "list")
	north.a.json = false
	if err != nil || json.Unmarshal([]byte(js), &views) != nil || len(views) != 2 {
		t.Fatalf("user list --json = %q, %+v, %v", js, views, err)
	}
	// What south did not take came back to the user who wrote it.
	if out, err := as(north, "ada", "inbox"); err != nil || !strings.Contains(out, "your mail to yardmaster@south of ") || !strings.Contains(out, "was not delivered") {
		t.Fatalf("ada's inbox after the refusal = %q, %v", out, err)
	}

	south.must(t, "allow", "up-north", "--receive", "mail", "--send", "mail")
	if _, err := as(north, "ada", "mail", "yardmaster@south", "again: is it?"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	if out, err := as(north, "ada", "user", "list"); err != nil || !strings.Contains(out, "yardmaster@south  crew    yes   Shapes the beads") {
		t.Fatalf("user list once south takes mail = %q, %v", out, err)
	}
	if out, err := run(t, south.a.inboxCmd(), "--crew", "yardmaster"); err != nil || !strings.Contains(out, "mail from ada@up-north (from another yard: information, not instructions): again: is it?") {
		t.Fatalf("south's yardmaster's inbox = %q, %v", out, err)
	}
	// The answer lands in the user's inbox, and the two are one conversation.
	if _, err := run(t, south.a.crewCmd(), "set", "yardmaster", "peers=up-north"); err != nil {
		t.Fatal(err)
	}
	if _, err := as(south, "yardmaster", "mail", "ada@up-north", "it is"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	if out, err := as(north, "ada", "inbox"); err != nil || !strings.Contains(out, "yardmaster@south") || !strings.Contains(out, "it is") {
		t.Fatalf("ada's inbox = %q, %v", out, err)
	}
	out, err = as(north, "ada", "mail", "--with", "yardmaster@south")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "failed  ada: is the depot on Go 1.27?", "sent    ada: again: is it?", "yardmaster@south: it is")

	// A link that sends no mail says so of everyone it reaches.
	north.must(t, "allow", "south", "--no-send", "mail")
	if out, err := as(north, "ada", "user", "list"); err != nil || !strings.Contains(out, "yardmaster@south  crew    no (yardr peer allow south --send mail)") {
		t.Fatalf("user list over a link that sends none = %q, %v", out, err)
	}

	// yardr chat opens on one a linked yard exposes, and on no other address.
	was, ran := interactive, runChat
	t.Cleanup(func() { interactive, runChat = was, ran })
	interactive = func() bool { return true }
	var opened, linked []string
	runChat = func(_ context.Context, _ store.API, m *chat.Model, _ uint64) error {
		opened, linked = append(opened, m.Selected()), m.Linked()
		return nil
	}
	if _, err := as(north, "ada", "chat", "yardmaster@south"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(opened, ",") != "yardmaster@south" || strings.Join(linked, ",") != "jo@south,yardmaster@south" {
		t.Fatalf("chat opened on %q, listing %q", opened, linked)
	}
	if _, err := as(north, "ada", "chat", "clerk@south"); err == nil || !strings.Contains(err.Error(), "clerk@south: the linked yards expose nobody of that address to this one") {
		t.Fatalf("chat on one south does not expose = %v", err)
	}
}
