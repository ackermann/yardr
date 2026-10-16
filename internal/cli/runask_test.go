package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	if _, err := y.st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddTimer(ctx, store.NewTimer{To: "jo", Reason: "stretch", Every: time.Hour}, "jo"); err != nil {
		t.Fatal(err)
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
		{"source add crm.yaml", nil, "no such file"},
		{"peer allow airy --mail-limit 300; true", nil, "is none of their characters"},
		{"peer allow north --mail-limit 300", nil, "peer north: not found"},
		{"peer allow airy --mail-limit 30", nil, "needs no approval: run it yourself"},
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
		"  what the command does: Set which messages a peer link carries and its mail limit",
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
		"peer airy: takes 300 mails and asks an hour, and 19200 files\n"
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
	return map[string]*cobra.Command{"peer": a.peerCmd(), "source": a.sourceCmd(), "subscribe": a.subscribeCmd(),
		"user": a.userCmd(), "bead": a.beadCmd(), "crew": a.crewCmd(), "settings": a.settingsCmd(), "timer": a.timerCmd()}
}

// The store's list of commands a run ask may name is held to the CLI: each
// command the yard stores is run through the real command of internal/cli
// in a second yard that began the same, as the operator would type it, and
// the two yards end in the same state, the approval's result being what
// the command printed. So the table cannot drift from the commands it
// stands for, and a row added to it is added here.
func TestRunListIsTheCLIs(t *testing.T) {
	key := airyKey(t)
	addedFile := filepath.Join(t.TempDir(), "added.yaml")
	if err := os.WriteFile(addedFile, []byte("name: added\nbase: https://added.example.com/v1\ncredential: { env: ADDED_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asks := map[string][]string{
		"yardr peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>": {
			"peer allow airy --mail-limit 300",
			"peer allow airy --send mail --receive mail,ping",
		},
		"yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>":            {"source set crm updates_per_hour=3"},
		"yardr source grant <source> read|<call> --group <group>|--crew <crew member>": {"source grant crm note --crew yardmaster --group builders"},
		"yardr source add <file>": {"source add " + addedFile},
		"yardr subscribe <user> yardr [--done] [--where <key>=<value>]": {"subscribe jo yardr --where label=ui --done --where label=web"},
		"yardr user set <user> tell=<parts>":                            {"user set jo tell=asks,notifications"},
		"yardr user add <name>":                                         {"user add eve"},
		"yardr user rm <name>":                                          {"user rm ada"},
		"yardr crew set <name> open_threads=<sources> | set_state=<sources> | peers=<peers>": {
			"crew set yardmaster peers=airy",
		},
		"yardr settings set <key> <value>":             {"settings set max_sessions 20"},
		"yardr peer rename <old> <new>":                {"peer rename airy north"},
		"yardr bead update <bead> --originator <user>": {"bead update yardmaster-journal --originator ada"},
		"yardr timer rm <id>":                          {"timer rm t1"},
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
		commands, ok := asks[use]
		if !ok {
			t.Errorf("%s is on the store's list and this test runs no ask for it", use)
			continue
		}
		if !strings.Contains(askHelp.Long, strings.TrimPrefix(use, "yardr ")) {
			t.Errorf("yardr bead ask --help does not name %s", use)
		}
		for _, command := range commands {
			approved, ab := runYardCLI(t, "approved", key)
			typed, _ := runYardCLI(t, "typed", key)
			ctx := context.Background()
			// Where the two yards' logs stand after the same setup: only
			// what the command logs past this is the row's changes.
			lastSeq := func(y *cliYard) uint64 {
				t.Helper()
				evs, err := y.st.Events(ctx, "", 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(evs) == 0 {
					return 0
				}
				return evs[0].Seq
			}
			markApproved, markTyped := lastSeq(approved), lastSeq(typed)
			runAsk := &store.AskRun{Argv: []string{command}}
			if carried, err := sourceAddCarried(command); err != nil {
				t.Fatal(err)
			} else {
				runAsk.Carried = carried
			}
			seq, err := approved.st.AddAsk(ctx, ab.ID, store.Ask{Question: "Run it?", To: "jo", Context: []string{"why"},
				Run: runAsk}, "yardmaster")
			if err != nil {
				t.Fatalf("%s: %v", use, err)
			}
			root := runCLI(typed.a)[strings.Fields(use)[1]]
			helpCmd, _, err := root.Find(strings.Fields(command)[1:])
			if err != nil {
				t.Fatalf("%s: find help for %q: %v", use, command, err)
			}
			notes, err := approved.st.Notes(ctx, ab.ID)
			if err != nil {
				t.Fatal(err)
			}
			var description string
			for _, asked := range store.ResolveAsks(notes, time.Now()) {
				if asked.Note.Seq == seq && asked.Ask.Run != nil {
					description = asked.Ask.Run.Description
					break
				}
			}
			if description != helpCmd.Short {
				t.Errorf("%s card description %q, want the command help's %q", use, description, helpCmd.Short)
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
			state := func(y *cliYard, mark uint64) string {
				t.Helper()
				peers, err := y.st.Peers(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for i := range peers {
					peers[i].AddedAt, peers[i].Since = time.Time{}, 0
					// Until is the log position of the peer_renamed event,
					// and the ask's own writes push it further along its own
					// log: both logs are right, so only that it names an
					// event is compared.
					for j := range peers[i].Was {
						if peers[i].Was[j].Until == 0 {
							t.Errorf("peer %s: was %s names no event", peers[i].Name, peers[i].Was[j].Name)
						}
						peers[i].Was[j].Until, peers[i].Was[j].At = 0, time.Time{}
					}
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
				users := map[string]any{}
				for _, name := range []string{"eve", "ada"} {
					u, err := y.st.GetUser(ctx, name)
					if err != nil {
						users[name] = "gone"
						continue
					}
					users[name] = map[string]any{"name": u.Name, "subscriptions": u.Subscriptions, "tell": u.Tell, "notify": u.Notify}
				}
				crew, err := y.st.GetCrew(ctx, "yardmaster")
				if err != nil {
					t.Fatal(err)
				}
				settings, err := y.st.Settings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				timers, err := y.st.Timers(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				var timerIDs []map[string]any
				for _, tm := range timers {
					timerIDs = append(timerIDs, map[string]any{"id": tm.ID, "by": tm.By, "to": tm.To, "reason": tm.Reason, "every": tm.Every})
				}
				journal, err := y.st.GetBead(ctx, "yardmaster-journal")
				if err != nil {
					t.Fatal(err)
				}
				var changes []map[string]any
				for _, kind := range []string{"peer_allowed", "source_set", "source_granted", "subscribed", "user_set",
					"peer_renamed", "user_added", "user_removed", "crew_defined", "setting_changed", "timer_removed", "originator_changed", "source_added"} {
					evs, err := y.st.EventsOfKind(ctx, kind, 10)
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range evs {
						if e.Seq <= mark {
							continue
						}
						// The one difference: the approval names its ask.
						delete(e.Data, "approved")
						changes = append(changes, map[string]any{"kind": e.Kind, "actor": e.Actor, "data": e.Data})
					}
				}
				if len(changes) != strings.Count(done.Result, "\n")+1 {
					t.Errorf("%s: %d changes were logged for %q", use, len(changes), done.Result)
				}
				text, err := json.Marshal(map[string]any{"peers": peers, "sources": sources, "grants": grants,
					"subscriptions": jo.Subscriptions, "tell": jo.Tell, "changes": changes, "users": users,
					"crew":     map[string]any{"name": crew.Name, "runner": crew.Runner, "journal": crew.Journal, "config": crew.Config},
					"settings": settings, "timers": timerIDs, "journal": journal.Originator})
				if err != nil {
					t.Fatal(err)
				}
				return string(text)
			}
			if a, b := state(approved, markApproved), state(typed, markTyped); a != b {
				t.Errorf("%s: approved, the yard holds\n%s\nand typed at a terminal\n%s", use, a, b)
			}
		}
	}
}

// bead ask --run "source add <file>" reads the declaration and its
// command files once and carries them on the ask; a commands-free
// approval adds the source, and one naming commands is recorded to be
// carried out.
func TestBeadAskRunSourceAdd(t *testing.T) {
	key := airyKey(t)
	y, b := runYardCLI(t, "mac", key)
	dir := t.TempDir()
	phone := filepath.Join(dir, "phone.yaml")
	if err := os.WriteFile(phone, []byte("name: phone\nbase: https://phone.example.com/v1\ncredential: { env: PHONE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := y.as(t, "yardmaster", "ask", b.ID, "Add the phone source?",
		"--context", "the yard has no source that tells a user", "--run", "source add "+phone)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + b.ID + " for jo: to approve, yardr source add " + phone + "\n"; out != want {
		t.Fatalf("bead ask --run source add printed %q, want %q", out, want)
	}
	shown, err := run(t, y.a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"the yard says: source phone: its calls go to https://phone.example.com/v1",
		"the declaration of source phone, whole as carried:",
		"the operator can take it back: yardr source rm phone",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("prime lacks %q:\n%s", want, shown)
		}
	}
	out, err = y.as(t, "jo", "answer", b.ID, "1", "--approve")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "source phone saved: 1 call under https://phone.example.com/v1") {
		t.Fatalf("the approval printed:\n%s", out)
	}
	if _, err := y.st.GetSource(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}

	wired := filepath.Join(dir, "wired.yaml")
	thread := filepath.Join(dir, "wired-thread")
	if err := os.WriteFile(thread, []byte("#!/bin/sh\n# thread\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decl := "name: wired\nbase: https://wired.example.com/v1\ncredential: { env: WIRED_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\nthread:\n  reply: [wired-thread, reply]\n"
	// The program stands beside the declaration as a bare name.
	decl = strings.Replace(decl, "[wired-thread, reply]", "[wired-thread, reply]", 1)
	if err := os.WriteFile(wired, []byte(decl), 0o600); err != nil {
		t.Fatal(err)
	}
	// Beside wired.yaml the program is read as wired-thread.
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Add the wired source?",
		"--context", "it speaks in threads", "--run", "source add "+wired); err != nil {
		t.Fatal(err)
	}
	out, err = y.as(t, "jo", "answer", b.ID, "3", "--approve")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "approved, to be carried out") {
		t.Fatalf("the approval of one naming commands printed:\n%s", out)
	}
	if _, err := y.st.GetSource(context.Background(), "wired"); err != nil {
		t.Fatal(err)
	}
}
