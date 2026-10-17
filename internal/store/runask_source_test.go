package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// carriedOf builds what an asking command sends for the declaration
// text with the command files texts, by program.
func carriedOf(t *testing.T, text string, files map[string]string) []AskSource {
	t.Helper()
	d, err := source.Parse("test.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	var ordered []string
	done := map[string]bool{}
	for _, c := range d.Commands() {
		if c.Relative() && !done[c.Argv[0]] {
			done[c.Argv[0]] = true
			ordered = append(ordered, c.Argv[0])
		}
	}
	var out []AskFile
	for _, prog := range ordered {
		body := files[prog]
		sum := sha256.Sum256([]byte(body))
		out = append(out, AskFile{Program: prog, Text: body, SHA256: hex.EncodeToString(sum[:])})
	}
	return []AskSource{{Name: d.Name, Text: text, Files: out}}
}

const phoneDecl = `name: phone
base: https://phone.example.com/v1
credential: { env: PHONE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

const tgDecl = `name: tg
base: https://api.telegram.org
credential: { env: TG_TOKEN, in_path: true }
calls:
  message:
    kind: update
    params:
      chat: { type: string, pattern: "^[0-9]+$" }
      text: { type: string, max: 100 }
    request: { method: POST, path: "/bot{credential}/sendMessage", body: { chat_id: "{chat}", text: "hi {text}", fixed: 1 } }
`

func sourceAddAsk(carried []AskSource) Ask {
	return Ask{Question: "Add the phone source?", To: "mckean",
		Context: []string{"the yard has no source that tells a user"},
		Run:     &AskRun{Argv: []string{"source add phone.yaml"}, Carried: carried}}
}

const wiredDecl = `name: wired
base: https://wired.example.com/v1
credential: { env: WIRED_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
limits:
  command: [wired/limits, "{provider}", --json]
  kinds: { claude: claude }
thread:
  reply: [wired/thread, reply]
hook:
  verify: { scheme: secret, signature: { header: X-Wired-Secret } }
  bodies: unique
  normalize: [wired/normalize]
  keys: [team]
events:
  runs: [check_run]
  head_from: [pull_request]
  pr: [wired/events, pr]
  ci: [wired/events, ci]
`

var wiredFiles = map[string]string{
	"wired/limits":    "#!/bin/sh\n# limits\n",
	"wired/thread":    "#!/bin/sh\n# thread\n",
	"wired/normalize": "#!/bin/sh\n# normalize\n",
	"wired/events":    "#!/bin/sh\n# events\n",
}

// A source add ask is stored with the declaration it carries and the
// yard's sentences for the whole declaration, whatever the asker sent
// for them, and the texts stand whole in a terminal.
func TestSourceAddAskCard(t *testing.T) {
	y, _ := runYard(t)
	ask := sourceAddAsk(carriedOf(t, tgDecl, nil))
	ask.Run.Argv = []string{"source add tg.yaml"}
	ask.Run.Does = []string{"a small add, nothing to read here"}
	ask.Run.Seen = map[string]string{"source tg": "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run == nil || len(got.Ask.Run.Carried) != 1 || got.Ask.Run.Carried[0].Text != tgDecl {
		t.Fatalf("the stored ask carries %+v", got.Ask.Run)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"source tg: its calls go to https://api.telegram.org",
		"sent in the request path wherever the declaration puts {credential} (credential.in_path: true)",
		"call message (update): POST /bot{credential}/sendMessage",
		"chat (string, required, pattern ^[0-9]+$)",
		"text (string, required, at most 100)",
		`"chat_id":"{chat}"`, `"text":"hi {text}"`, `"fixed":1`,
		"Its answer is returned as it comes",
		"still owed after it: the credential, by a secret ask for tg.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if got.Ask.Run.TakenBack() != "the operator can take it back: yardr source rm tg" {
		t.Errorf("the card takes it back with %q", got.Ask.Run.TakenBack())
	}
	if strings.Contains(joined, "nothing to read here") {
		t.Errorf("the asker's sentences stood:\n%s", joined)
	}
	lines := strings.Join(got.Lines(), "\n")
	if !strings.Contains(lines, "      name: tg") || !strings.Contains(lines, "the declaration of source tg, whole as carried:") {
		t.Errorf("the terminal card lacks the whole text:\n%s", lines)
	}
}

// Every command key's file stands on the card with its hash whole.
func TestSourceAddCardNamesEveryCommand(t *testing.T) {
	y, _ := runYard(t)
	decl := wiredDecl
	for _, path := range []string{"wired/limits", "wired/thread", "wired/normalize", "wired/events"} {
		decl = strings.ReplaceAll(decl, path, "wired/shared")
	}
	shared := map[string]string{"wired/shared": "#!/bin/sh\n# shared\n"}
	ask := sourceAddAsk(carriedOf(t, decl, shared))
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	for _, want := range []string{
		"(limits.command, handed no credential)",
		"(thread.reply, handed the credential)",
		"(hook.normalize, handed no credential)",
		"(events.pr, handed the credential)",
		"(events.ci, handed the credential)",
		"scheme secret", "bodies are unique",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	for prog, body := range shared {
		sum := sha256.Sum256([]byte(body))
		if want := "command file " + prog + ":"; !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
		if want := "sha256 " + hex.EncodeToString(sum[:]); !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
}

// A built call's card says it builds its body with its command, run with
// the credential at each call and free to make its own requests while it
// builds, and the script's file stands with its hash whole.
func TestSourceAddCardNamesABuiltCall(t *testing.T) {
	y, _ := runYard(t)
	ask := sourceAddAsk(carriedOf(t, builtDecl, builtFiles))
	ask.Run.Argv = []string{"source add docs.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	sum := sha256.Sum256([]byte(builtFiles["docs/build-write"]))
	for _, want := range []string{
		"call write builds its body with the command [docs/build-write], run with the credential at each call and free to make its own requests with it while it builds",
		"(call.write.build, handed the credential)",
		"command file docs/build-write:",
		"sha256 " + hex.EncodeToString(sum[:]),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "call write sends no body") {
		t.Errorf("the built call reads as sending no body:\n%s", joined)
	}
}

const builtDecl = `name: docs
base: https://docs.googleapis.com
credential: { env: DOCS_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  write:
    kind: update
    params:
      docId: { type: string, max: 200 }
    request: { method: POST, path: "/v1/documents/{docId}:batchUpdate" }
    build: [docs/build-write]
`

var builtFiles = map[string]string{
	"docs/build-write": "#!/bin/sh\n# build\n",
}

func TestSourceAddCardNamesCredentialForAbsoluteCommands(t *testing.T) {
	decl := wiredDecl
	for _, path := range []string{"wired/limits", "wired/thread", "wired/normalize", "wired/events"} {
		decl = strings.ReplaceAll(decl, path, "/opt/"+path)
	}
	d, err := source.Parse("wired.yaml", []byte(decl))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(sourceDoes(d, AskSource{Name: d.Name, Text: decl}), "\n")
	for _, want := range []string{
		"(limits.command): the program is the operator's own, handed no credential",
		"(thread.reply): the program is the operator's own, handed the credential",
		"(hook.normalize): the program is the operator's own, handed no credential",
		"(events.pr): the program is the operator's own, handed the credential",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
}

func TestSourceAddCardShowsEveryCredentialPlacement(t *testing.T) {
	y, _ := runYard(t)
	decl := strings.Replace(wiredDecl,
		`credential: { env: WIRED_TOKEN, header: "Authorization: Bearer {credential}" }`,
		`credential: { env: WIRED_TOKEN, header: "Authorization: Bearer {credential}", in_path: true }`, 1)
	decl = strings.Replace(decl,
		`ping: { kind: read, request: { method: GET, path: /me } }`,
		`ping: { kind: update, request: { method: POST, path: "/bot{credential}/me", body: { token: "{credential}", fixed: 1 } } }`, 1)
	carried := carriedOf(t, decl, wiredFiles)
	ask := sourceAddAsk(carried)
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	for _, want := range []string{
		`sent in the header "Authorization: Bearer {credential}" on every call`,
		"in the request path wherever the declaration puts {credential}",
		"as a leaf of the body of call ping",
		`"token":"{credential}"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
}

func TestSourceAddCardLineBound(t *testing.T) {
	y, _ := runYard(t)
	var decl strings.Builder
	decl.WriteString("name: many\nbase: https://many.example.com\ncalls:\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&decl, "  call%d: { kind: read, request: { method: GET, path: /call%d } }\n", i, i)
	}
	carried := carriedOf(t, decl.String(), nil)
	ask := sourceAddAsk(carried)
	ask.Run.Argv = []string{"source add many.yaml"}
	_, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "card would run to") || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("AddAsk = %v, want the card line bound refusal", err)
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused ask left %d notes, %v", len(notes), err)
	}
}

func TestSourceAddTerminalCardTreatsTextAsData(t *testing.T) {
	y, _ := runYard(t)
	text := strings.Replace(phoneDecl, `path: /me`, `path: /me, query: { format: "\u001b[2J" }`, 1)
	text = strings.ReplaceAll(text, "\n", "\r\n")
	text += "# file text: ◆ forged\r\n"
	seq := y.ask(t, sourceAddAsk(carriedOf(t, text, nil)), "yardmaster")
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if strings.ContainsAny(lines, "\x1b\r\u202e") {
		t.Fatalf("untrusted source text reached the terminal as a control:\n%q", lines)
	}
	if strings.Contains(lines, "◆") {
		t.Fatalf("untrusted source text can forge a file mark:\n%s", lines)
	}
	if !strings.Contains(lines, `format="\x1b[2J"`) {
		t.Fatalf("the source's query is absent from the card:\n%s", lines)
	}

	carried := carriedOf(t, wiredDecl, map[string]string{
		"wired/limits":    "#!/bin/sh\n# forged ◆\r\x1b[2J\u202e\n",
		"wired/thread":    wiredFiles["wired/thread"],
		"wired/normalize": wiredFiles["wired/normalize"],
		"wired/events":    wiredFiles["wired/events"],
	})
	wired := sourceAddAsk(carried)
	wired.Run.Argv = []string{"source add wired.yaml"}
	wseq := y.ask(t, wired, "yardmaster")
	wiredLines := strings.Join(y.asked(t, wseq).Lines(), "\n")
	if strings.ContainsAny(wiredLines, "\x1b\r\u202e") || strings.Contains(wiredLines, "◆") {
		t.Fatalf("untrusted command text reached the terminal:\n%q", wiredLines)
	}
}

// A source add ask is refused, storing nothing, for what the row refuses.
func TestSourceAddRefusals(t *testing.T) {
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
		t.Fatal(err)
	}
	big := "name: big\nabout: " + strings.Repeat("a", 9000) + "\nbase: https://big.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	bigFileDecl := strings.Replace(wiredDecl, "name: wired", "name: wired2", 1)
	for _, c := range []struct {
		name    string
		carried []AskSource
		want    string
	}{
		{"does not parse", []AskSource{{Name: "x", Text: "not a declaration"}}, "want a mapping"},
		{"another name carried", func() []AskSource { c := carriedOf(t, phoneDecl, nil); c[0].Name = "other"; return c }(), "its declaration names phone"},
		{"the yard has the name", carriedOf(t, runCRM, nil), "it is the operator's alone"},
		{"an absolute program", carriedOf(t, strings.Replace(wiredDecl, "[wired/limits,", "[/bin/limits,", 1), wiredFiles), "it is the operator's alone"},
		{"bytes are not their hash", func() []AskSource {
			c := carriedOf(t, wiredDecl, wiredFiles)
			c[0].Files[0].SHA256 = strings.Repeat("0", 64)
			return c
		}(), "its bytes hash to"},
		{"a binary command file", func() []AskSource {
			c := carriedOf(t, wiredDecl, wiredFiles)
			c[0].Files[0].Text = string([]byte{0xff, 0xfe})
			s := sha256.Sum256([]byte(c[0].Files[0].Text))
			c[0].Files[0].SHA256 = hex.EncodeToString(s[:])
			return c
		}(), "yardr source add phone.yaml in a terminal"},
		{"a declaration too long", []AskSource{{Name: "big", Text: big}}, "a card holds at most 8192"},
		{"a file too long", func() []AskSource {
			f := map[string]string{}
			for k := range wiredFiles {
				f[k] = wiredFiles[k]
			}
			f["wired/limits"] = strings.Repeat("a", 9000)
			return carriedOf(t, bigFileDecl, f)
		}(), "a card holds at most 8192"},
		{"all of it too long", func() []AskSource {
			f := map[string]string{}
			for k := range wiredFiles {
				f[k] = strings.Repeat("a", 8100)
			}
			return carriedOf(t, bigFileDecl, f)
		}(), "a card holds at most 32768"},
	} {
		ask := sourceAddAsk(c.carried)
		if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want %q", c.name, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// A commands-free approval adds the source in its write; one that names
// commands stores the declaration and is left to be carried out.
func TestSourceAddApprove(t *testing.T) {
	y, _ := runYard(t)
	seq := y.ask(t, sourceAddAsk(carriedOf(t, phoneDecl, nil)), "yardmaster")
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "source phone saved: 1 call under https://phone.example.com/v1" {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}

	wired := sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	wired.Run.Argv = []string{"source add wired.yaml"}
	wseq := y.ask(t, wired, "yardmaster")
	done, err = y.ApproveAsk(y.ctx, y.bead, wseq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "approved, to be carried out" {
		t.Fatalf("the approval of one naming commands recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, wseq, "mckean"); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval of one naming commands = %v", err)
	}
}

// An approval is refused when the name was taken since the ask.
func TestSourceAddApproveStale(t *testing.T) {
	y, _ := runYard(t)
	seq := y.ask(t, sourceAddAsk(carriedOf(t, phoneDecl, nil)), "yardmaster")
	if _, err := y.PutSource(y.ctx, "phone.yaml", phoneDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "was not in the yard: it was added since") {
		t.Fatalf("ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
}
