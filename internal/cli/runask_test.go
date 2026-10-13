package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

// runYardCLI is a yard whose operator is the user jo, with the crew member
// yardmaster, the group builders, the source crm, a bead, and the peer airy
// under the key given: two of them with one key hold the same link, for
// what is compared between them.
func runYardCLI(t *testing.T, name, key string) (*cliYard, *store.Bead) {
	t.Helper()
	y := newCLIYard(t, name)
	ctx := context.Background()
	if _, err := y.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "airy", Key: key, Calls: "yardr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutSource(ctx, "crm.yaml", runCRM, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.GetUser(ctx, "jo"); err != nil {
		if _, err := y.st.AddUser(ctx, "jo", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := y.st.CreateBead(ctx, store.NewBead{Title: "the work"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return y, b
}

const runCRM = `name: crm
base: https://crm.example.com/v1
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
idempotency: Idempotency-Key
calls:
  find: { kind: read, request: { method: GET, path: /contacts } }
  note: { kind: update, request: { method: POST, path: /notes, body: { text: hello } } }
`

// as runs the bead command args as the actor who.
func (y *cliYard) as(t *testing.T, who string, args ...string) (string, error) {
	t.Helper()
	y.a.actor = who
	defer func() { y.a.actor = "jo" }()
	return run(t, y.a.beadCmd(), args...)
}

// airyKey is a key for the peer airy of a test.
func airyKey(t *testing.T) string {
	t.Helper()
	other := newCLIYard(t, "airy")
	t.Setenv("YARDR_HOME", other.home)
	return strings.TrimSpace(other.must(t, "key"))
}

// yardr bead ask --run stores the command in the yard's form and prints
// what an approval would run; prime's view of the bead shows the card; and
// yardr bead answer --approve runs it and prints the command, the yard's
// sentence, who approved and what the command printed.
func TestBeadAskRunAndApprove(t *testing.T) {
	key := airyKey(t)
	y, b := runYardCLI(t, "mac", key)
	ask := func(who, command string, more ...string) (string, error) {
		return y.as(t, who, append([]string{"ask", b.ID, "Take more mail from airy?",
			"--context", "airy's yard was refused 4 mails in the last hour: the link takes 30.",
			"--run", command}, more...)...)
	}
	out, err := ask("yardmaster", "yardr peer allow --mail-limit=300 airy")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + b.ID + " for jo: to approve, yardr peer allow airy --mail-limit 300\n"; out != want {
		t.Fatalf("bead ask --run printed %q, want %q", out, want)
	}
	for _, c := range []struct {
		command string
		more    []string
		want    string
	}{
		{"user add eve", nil, "--run takes a command of the list (yardr peer allow <peer> --mail-limit <n>, yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>, "},
		{"peer allow airy --mail-limit 300; true", nil, "is none of their characters"},
		{"peer allow north --mail-limit 300", nil, "peer north: not found"},
		{"peer allow airy --mail-limit 30", nil, "would change nothing"},
		{"peer allow airy --mail-limit 300", []string{"--option", "Yes: it runs"}, "a run ask takes no --option"},
		{"peer allow airy --mail-limit 300", []string{"--to", "jo@airy"}, "no approval crosses a link"},
		{"peer allow airy --mail-limit 300", []string{"--secret", "crm"}, "--run proposes a command and --secret asks for a value: one of the two"},
	} {
		if _, err := ask("yardmaster", c.command, c.more...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--run %q %v = %v, want %q", c.command, c.more, err, c.want)
		}
	}
	shown, err := run(t, y.a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  yardmaster says: airy's yard was refused 4 mails in the last hour: the link takes 30.",
		"  when you approve, the yard runs this as jo, and nothing else:",
		"      yardr peer allow airy --mail-limit 300",
		"  the yard says: the link with airy (it calls itself yardr, key " + key[:8] + "…) takes 300 mails and asks an hour from it. It takes 30 now.",
		"  anyone can take it back: yardr peer allow airy --mail-limit 30",
		"  approve with: yardr bead answer " + b.ID + " 1 --approve",
	} {
		if !strings.Contains(shown, want+"\n") {
			t.Errorf("prime lacks %q:\n%s", want, shown)
		}
	}

	if _, err := y.as(t, "yardmaster", "answer", b.ID, "1", "--approve"); err == nil || !strings.Contains(err.Error(), "approving a command is the operator's (jo)") {
		t.Fatalf("the asker's own approval = %v", err)
	}
	if _, err := y.as(t, "jo", "answer", b.ID, "1", "--approve", "--later"); err == nil || !strings.Contains(err.Error(), "--approve runs the command the ask holds and takes nothing else") {
		t.Fatalf("--approve --later = %v", err)
	}
	if _, err := y.as(t, "jo", "answer", b.ID, "1", "--words", "yes"); err == nil || !strings.Contains(err.Error(), "takes an approval, not words: yardr bead answer "+b.ID+" 1 --approve") {
		t.Fatalf("words on a run ask = %v", err)
	}
	out, err = y.as(t, "jo", "answer", b.ID, "1", "--approve")
	if err != nil {
		t.Fatal(err)
	}
	want := "ask 1, asked by yardmaster: yardr peer allow airy --mail-limit 300\n" +
		"the link with airy (it calls itself yardr, key " + key[:8] + "…) takes 300 mails and asks an hour from it. It takes 30 now.\n" +
		"jo approved it, and the yard ran it:\n" +
		"peer airy: takes 300 mails and asks an hour, and 2400 files\n"
	if out != want {
		t.Fatalf("bead answer --approve printed:\n%s\nwant:\n%s", out, want)
	}
	if p, err := y.st.GetPeer(context.Background(), "airy"); err != nil || p.MailsAnHour() != 300 {
		t.Fatalf("the link after the approval = %+v, %v", p, err)
	}
	if _, err := y.as(t, "jo", "answer", b.ID, "1", "--approve"); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}
}

// runCLI are the commands of internal/cli that the rows of the store's
// list stand for, by the word a stored command begins with.
func runCLI(a *app) map[string]*cobra.Command {
	return map[string]*cobra.Command{"peer": a.peerCmd(), "source": a.sourceCmd(), "subscribe": a.subscribeCmd()}
}

// The store's list of commands a run ask may name is held to the CLI: each
// command the yard stores is run through the real command of internal/cli
// in a second yard that began the same, as the operator would type it, and
// the two yards end in the same state, the approval's result being what
// the command printed. So the table cannot drift from the commands it
// stands for, and a row added to it is added here.
func TestRunListIsTheCLIs(t *testing.T) {
	key := airyKey(t)
	asks := map[string]string{
		"yardr peer allow <peer> --mail-limit <n>":                                     "peer allow airy --mail-limit 300",
		"yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>":            "source set crm updates_per_hour=3",
		"yardr source grant <source> read|<call> --group <group>|--crew <crew member>": "source grant crm note --crew yardmaster --group builders",
		"yardr subscribe <user> yardr [--done] [--where <key>=<value>]":                "subscribe jo yardr --where label=ui --done --where label=web",
	}
	if len(asks) != len(store.RunCommands()) {
		t.Errorf("this test has asks for %d commands, and the list has %d", len(asks), len(store.RunCommands()))
	}
	help := (&app{}).beadCmd()
	askHelp, _, err := help.Find([]string{"ask"})
	if err != nil {
		t.Fatal(err)
	}
	for _, use := range store.RunCommands() {
		command, ok := asks[use]
		if !ok {
			t.Errorf("%s is on the store's list and this test runs no ask for it", use)
			continue
		}
		if !strings.Contains(askHelp.Long, strings.TrimPrefix(use, "yardr ")) {
			t.Errorf("yardr bead ask --help does not name %s", use)
		}
		approved, ab := runYardCLI(t, "approved", key)
		typed, _ := runYardCLI(t, "typed", key)
		ctx := context.Background()
		seq, err := approved.st.AddAsk(ctx, ab.ID, store.Ask{Question: "Run it?", To: "jo", Context: []string{"why"},
			Run: &store.AskRun{Argv: []string{command}}}, "yardmaster")
		if err != nil {
			t.Fatalf("%s: %v", use, err)
		}
		done, err := approved.st.ApproveAsk(ctx, ab.ID, seq, "jo")
		if err != nil {
			t.Fatalf("%s: %v", use, err)
		}
		cmd := runCLI(typed.a)[done.Argv[0]]
		if cmd == nil {
			t.Fatalf("%s: this test has no command of internal/cli for %q", use, done.Argv[0])
		}
		out, err := run(t, cmd, done.Argv[1:]...)
		if err != nil {
			t.Fatalf("yardr %s: %v", strings.Join(done.Argv, " "), err)
		}
		// What the command prints is on its own line under its name, a
		// line for each thing it changed.
		for _, line := range strings.Split(done.Result, "\n") {
			if _, said, _ := strings.Cut(line, ": "); !strings.Contains(out, said) {
				t.Errorf("yardr %s printed %q, and the approval recorded %q", strings.Join(done.Argv, " "), out, done.Result)
			}
		}
		state := func(y *cliYard) string {
			t.Helper()
			peers, err := y.st.Peers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i := range peers {
				peers[i].AddedAt, peers[i].Since = time.Time{}, 0
			}
			sources, err := y.st.Sources(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i := range sources {
				sources[i].AddedAt = time.Time{}
			}
			grants, err := y.st.SourceGrants(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			for i := range grants {
				grants[i].At = time.Time{}
			}
			jo, err := y.st.GetUser(ctx, "jo")
			if err != nil {
				t.Fatal(err)
			}
			var changes []map[string]any
			for _, kind := range []string{"peer_allowed", "source_set", "source_granted", "subscribed"} {
				evs, err := y.st.EventsOfKind(ctx, kind, 10)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range evs {
					// The one difference: the approval names its ask.
					delete(e.Data, "approved")
					changes = append(changes, map[string]any{"kind": e.Kind, "actor": e.Actor, "data": e.Data})
				}
			}
			if len(changes) != strings.Count(done.Result, "\n")+1 {
				t.Errorf("%s: %d changes were logged for %q", use, len(changes), done.Result)
			}
			text, err := json.Marshal(map[string]any{"peers": peers, "sources": sources, "grants": grants,
				"subscriptions": jo.Subscriptions, "changes": changes})
			if err != nil {
				t.Fatal(err)
			}
			return string(text)
		}
		if a, b := state(approved), state(typed); a != b {
			t.Errorf("%s: approved, the yard holds\n%s\nand typed at a terminal\n%s", use, a, b)
		}
	}
}
