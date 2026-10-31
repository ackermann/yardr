package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	t.Parallel()
	y, _ := runYard(t)
	ask := sourceAddAsk(carriedOf(t, tgDecl, nil))
	ask.Run.Argv = []string{"source add tg.yaml"}
	ask.Run.Does = []string{"a small add, nothing to read here"}
	ask.Run.Seen = map[string]string{"source tg": "forged"}
	// What the yard's copies hold is the yard's record, never the asker's.
	ask.Run.Carried[0].Old = []AskFileDiff{{Program: "forged", Diff: "+forged\n"}}
	ask.Run.Carried[0].Removed = []AskRemoved{{Path: "forged", Text: "forged\n"}}
	ask.Run.Carried[0].Inventory = []AskInventory{{Path: "forged"}}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run == nil || len(got.Ask.Run.Carried) != 1 || got.Ask.Run.Carried[0].Text != tgDecl {
		t.Fatalf("the stored ask carries %+v", got.Ask.Run)
	}
	if c := got.Ask.Run.Carried[0]; len(c.Old)+len(c.Removed)+len(c.Inventory) != 0 || strings.Contains(strings.Join(got.Lines(), "\n"), "forged") {
		t.Errorf("the stored ask kept the asker's record of the copies: %+v", c)
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
	t.Parallel()
	y, _ := runYard(t)
	decl := wiredDecl
	for _, path := range []string{"wired/limits", "wired/thread", "wired/normalize", "wired/events"} {
		decl = strings.ReplaceAll(decl, path, "wired/shared")
	}
	decl = strings.Replace(decl, "  ping: { kind: read, request: { method: GET, path: /me } }\n",
		"  ping: { kind: read, request: { method: GET, path: /me } }\n"+
			"  send:\n    kind: update\n    params: { to: { type: string }, text: { type: string } }\n"+
			"    request: { method: POST, path: /send, body: { to: \"{to}\", text: \"{text}\" } }\n", 1)
	decl += "notify:\n  call: send\n  address: { param: to, env: WIRED_USER }\n  text: text\n  image: [wired/shared]\n"
	shared := map[string]string{"wired/shared": "#!/bin/sh\n# shared\n"}
	ask := sourceAddAsk(carriedOf(t, decl, shared))
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	for _, want := range []string{
		"(limits.command, handed no credential)",
		"(thread.reply, handed the credential)",
		"(notify.image, handed the credential)",
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
	t.Parallel()
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

// crmLimits is the standing crm with a limits command whose program is
// program: a file the declaration brings when it is relative.
func crmLimits(program string) string {
	return runCRM + "limits:\n  command: [" + program + ", \"{provider}\"]\n  kinds: { claude: claude }\n"
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
	t.Parallel()
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
	t.Parallel()
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

// A source whose credential is the address (credential.url) has no base,
// and the card does not say it calls nothing: it says its calls go to the
// variable's value, that whoever sets it chooses where, and that no header,
// path or body holds it.
func TestSourceAddCardSaysTheCredentialIsTheAddress(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	const hookDecl = `name: hook
credential: { env: HOOK_URL, url: true }
calls:
  send: { kind: update, params: { text: { type: string, max: 100 } }, request: { method: POST, body: { text: "{text}" } } }
`
	ask := sourceAddAsk(carriedOf(t, hookDecl, nil))
	ask.Run.Argv = []string{"source add hook.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	for _, want := range []string{
		"source hook: its calls go to the URL in HOOK_URL.",
		"Its credential is the variable HOOK_URL (the name, never the value), sent as the address every call goes to (credential.url: true): whoever sets the variable chooses where the calls go, over https or plain http to this machine alone.",
		"call send (update): POST /",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	for _, not := range []string{"no base", "calls nothing", "no call's header, path or body holds it"} {
		if strings.Contains(joined, not) {
			t.Errorf("the card says %q:\n%s", not, joined)
		}
	}
}

func TestSourceAddCardLineBound(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	y, _ := runYard(t)
	text := strings.Replace(phoneDecl, `path: /me`, `path: /me, query: { format: "\u001b[2J" }`, 1)
	text = strings.ReplaceAll(text, "\n", "\r\n")
	text += "# file text: ◆ forged\r\n"
	seq := y.ask(t, sourceAddAsk(carriedOf(t, text, nil)), "yardmaster")
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if strings.ContainsAny(lines, "\x1b\r\u202e") {
		t.Fatalf("untrusted source text reached the terminal as a control:\n%q", lines)
	}
	// The mark in untrusted text cannot start a line of its own: every
	// carried line passes the files-mark guard. Mid-line it is data and
	// stays.
	for _, line := range strings.Split(lines, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "◆") {
			t.Fatalf("untrusted source text forged a files line:\n%s", lines)
		}
	}
	if !strings.Contains(lines, "# file text: ◆ forged") {
		t.Fatalf("the card lost the untrusted text's diamond:\n%s", lines)
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
	if strings.ContainsAny(wiredLines, "\x1b\r\u202e") {
		t.Fatalf("untrusted command text reached the terminal as a control:\n%q", wiredLines)
	}
	for _, line := range strings.Split(wiredLines, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "◆") {
			t.Fatalf("untrusted command text forged a files line:\n%s", wiredLines)
		}
	}
	if !strings.Contains(wiredLines, "# forged ◆") {
		t.Fatalf("the card lost the command text's diamond:\n%s", wiredLines)
	}
}

// A source add ask is refused, storing nothing, for what the row refuses.
func TestSourceAddRefusals(t *testing.T) {
	t.Parallel()
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
		{"an absolute program", carriedOf(t, strings.Replace(wiredDecl, "[wired/limits,", "[/bin/limits,", 1), wiredFiles), "it is the operator's alone"},
		// The yard has crm: these are refused as they are for a free name,
		// since a replacement carries its bytes in the same way.
		{"an absolute program replacing a source", carriedOf(t, crmLimits("/bin/limits"), nil), "it is the operator's alone"},
		{"a declaration too long replacing a source", []AskSource{{Name: "crm", Text: strings.Replace(runCRM, "name: crm\n", "name: crm\nabout: "+strings.Repeat("a", 9000)+"\n", 1)}}, "a card holds at most 8192"},
		{"a file too long replacing a source", carriedOf(t, crmLimits("crm/limits"), map[string]string{"crm/limits": strings.Repeat("a", 9000)}), "a card holds at most 8192"},
		{"bytes are not their hash replacing a source", func() []AskSource {
			c := carriedOf(t, crmLimits("crm/limits"), map[string]string{"crm/limits": "#!/bin/sh\n"})
			c[0].Files[0].SHA256 = strings.Repeat("0", 64)
			return c
		}(), "its bytes hash to"},
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
	t.Parallel()
	y, _ := runYard(t)
	seq := y.ask(t, sourceAddAsk(carriedOf(t, phoneDecl, nil)), "yardmaster")
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "source phone saved: 1 call under https://phone.example.com/v1" {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	const addressDecl = `name: webhook
credential: { env: WEBHOOK_URL, url: true }
calls:
  send: { kind: update, request: { method: POST } }
`
	addressSeq := y.ask(t, sourceAddAsk(carriedOf(t, addressDecl, nil)), "yardmaster")
	addressDone, err := y.ApproveAsk(y.ctx, y.bead, addressSeq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "source webhook saved: 1 call under the URL in WEBHOOK_URL"; addressDone.Result != want {
		t.Fatalf("the URL credential approval recorded %q, want %q", addressDone.Result, want)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}

	wired := sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	wired.Run.Argv = []string{"source add wired.yaml"}
	wseq := y.ask(t, wired, "yardmaster")
	done, err = y.ApproveAsk(y.ctx, y.bead, wseq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "approved, to be carried out" {
		t.Fatalf("the approval of one naming commands recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, wseq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval of one naming commands = %v", err)
	}
}

// An approval is refused when the name was taken since the ask.
func TestSourceAddApproveStale(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	seq := y.ask(t, sourceAddAsk(carriedOf(t, phoneDecl, nil)), "yardmaster")
	if _, err := y.PutSource(y.ctx, "phone.yaml", phoneDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "was not in the yard: it was added since") {
		t.Fatalf("ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}

	// An ask written for a free name is never turned into a replacement
	// because the name was taken: even where the source that took it has a
	// copy no card can show, the refusal is the stale one for a name taken.
	y, _ = runYard(t)
	free := sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	free.Run.Argv = []string{"source add wired.yaml"}
	seq = y.ask(t, free, "yardmaster")
	limits := filepath.Join(plantWired(t, y), "wired", "limits")
	if err := os.Remove(limits); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", limits); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written when the source wired was not in the yard: it was added since")
	// And the write that adds refuses a name taken, as the one that
	// replaces refuses a source gone, when its card says otherwise.
	var stored AskRun
	if err := y.write(func(w *wtx) error {
		stored = *y.asked(t, seq).Ask.Run
		_, err := runSourceAdd(y.ctx, w, stored, "mckean", nil)
		if err == nil || !strings.Contains(err.Error(), "source wired is in the yard already") {
			t.Errorf("adding over a name taken = %v", err)
		}
		stored.Seen = map[string]string{"source wired": "a fingerprint"}
		if err := w.deleteSource(y.ctx, "wired", "mckean"); err != nil {
			t.Fatal(err)
		}
		_, err = runSourceAdd(y.ctx, w, stored, "mckean", nil)
		if err == nil || !strings.Contains(err.Error(), "source wired is not in the yard any more") {
			t.Errorf("replacing a source gone = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A replacement is held to the source it was written about: its
	// declaration, the settings its card was held to, the copies that stand
	// and who a grant reaches. Any change makes it stale; nothing runs.
	next, files := nextWired()
	for _, c := range []struct {
		name   string
		change func(t *testing.T, y *askYard, dir string)
		want   string
	}{
		{"replaced since", func(t *testing.T, y *askYard, _ string) {
			if _, err := y.PutSource(y.ctx, "wired.yaml", wiredDecl+"# another\n", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "was written from another declaration of the source wired: it was replaced since"},
		{"its credential file set since", func(t *testing.T, y *askYard, _ string) {
			if _, err := y.SetSource(y.ctx, "wired", SourceEnvFile, "/keys/wired.env", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "took its credential from elsewhere: its env_file was set since"},
		{"a copy changed since", func(t *testing.T, _ *askYard, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "wired", "limits"), []byte("#!/bin/sh\n# swapped\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "the source wired's copies stood as carded: the command copy wired/limits changed since"},
		{"a copy added since", func(t *testing.T, _ *askYard, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "added.txt"), []byte("by hand\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the command copy added.txt was added since"},
		{"a grant of read given since", func(t *testing.T, y *askYard, _ string) {
			if _, err := y.GrantSource(y.ctx, "wired", GrantRead, Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "the card is from before that changed"},
		{"removed since", func(t *testing.T, y *askYard, _ string) {
			if err := y.DeleteSource(y.ctx, "wired", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, "was written when the source wired was in the yard: it was removed since"},
	} {
		y, _ := runYard(t)
		dir := plantWired(t, y)
		ask := sourceAddAsk(carriedOf(t, next, files))
		ask.Run.Argv = []string{"source add wired.yaml"}
		seq := y.ask(t, ask, "yardmaster")
		c.change(t, y, dir)
		_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasSuffix(err.Error(), nothingRan) {
			t.Errorf("%s: ApproveAsk = %v, want %q and then %q", c.name, err, c.want, nothingRan)
		}
		if src, err := y.GetSource(y.ctx, "wired"); err == nil && src.Text == next {
			t.Errorf("%s: the stale approval replaced the source", c.name)
		}
		if r := y.asked(t, seq); r.State != AskOpen {
			t.Errorf("%s: the ask is %s after a refused approval", c.name, r.State)
		}
	}

	// An add written for a free name with orphaned copies is held to
	// them: a file added to the directory after the ask makes it stale,
	// and nothing runs.
	y, _ = runYard(t)
	orphanDir := filepath.Join(y.Store.home, "sources", "wired")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "orphan.txt"), []byte("left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	free = sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	free.Run.Argv = []string{"source add wired.yaml"}
	seq = y.ask(t, free, "yardmaster")
	if err := os.WriteFile(filepath.Join(orphanDir, "added.txt"), []byte("by hand after the ask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if want := "the source wired's copies stood as carded: the command copy added.txt was added since"; err == nil || !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("an orphan copy added after the ask: ApproveAsk = %v, want %q and then %q", err, want, nothingRan)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatalf("the stale approval added the source")
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
}

// A previous source removal may leave its command-copy directory behind.
// An add for that free name cards the orphaned copies as a pack card
// does: each file the add would replace or remove is named, the approval
// leaves the add to the server's carrying out, and writing the carried
// bytes removes the orphan and nothing the card did not name.
func TestSourceAddCardsOrphanCopyDirectory(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := filepath.Join(y.Store.home, "sources", "wired")
	if err := os.MkdirAll(filepath.Join(dir, "wired"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# left by a removed source\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphan.txt"), []byte("left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask := sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	carried := got.Ask.Run.Carried[0]
	if len(carried.Inventory) != 2 || len(carried.Old) != 1 || len(carried.Removed) != 1 {
		t.Fatalf("the orphan copy inventory = %+v, old %+v, removed %+v", carried.Inventory, carried.Old, carried.Removed)
	}
	if fp := got.Ask.Run.Seen["source wired"]; fp == "" || fp != seen("no source", carried.Inventory) {
		t.Fatalf("the card holds the orphaned copies by %q", fp)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"the source wired is not in the yard, but its command copy directory has entries it would replace or remove.",
		"command file wired/thread: [wired/thread, reply] (thread.reply, handed the credential): sha256 ",
		" -> sha256 ",
		"the copy would remove orphan.txt, which neither declaration names: sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(strings.Join(got.Lines(), "\n"), "left by hand") {
		t.Fatalf("the terminal card omits the orphan file's text:\n%s", strings.Join(got.Lines(), "\n"))
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatal("the orphan directory had a source record")
	}

	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	state, err := y.CheckCarrySource(y.ctx, carried, got.Ask.Run.Seen["source wired"], rec.Sources[0])
	if err != nil || state != CarryStored {
		t.Fatalf("the hold on the carried-out add = %s, %v", state, err)
	}
	// The server's write step, with the carried bytes.
	var copies []source.FileCopy
	want := map[string]string{}
	for _, f := range carried.Files {
		copies = append(copies, source.FileCopy{Program: f.Program, Data: []byte(f.Text)})
		want[f.Program] = f.SHA256
	}
	if err := source.WriteCopies(y.Store.home, "wired", copies); err != nil {
		t.Fatal(err)
	}
	if err := source.VerifyCopies(y.Store.home, "wired", want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "orphan.txt")); !os.IsNotExist(err) {
		t.Errorf("orphan.txt stands after the carrying out: %v", err)
	}
	var stood []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			stood = append(stood, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(stood)
	if wantStood := []string{"wired/events", "wired/limits", "wired/normalize", "wired/thread"}; !slices.Equal(stood, wantStood) {
		t.Errorf("the copies after the carrying out = %v, want %v", stood, wantStood)
	}
}

// nextWired is wiredDecl as a replacement would carry it: the address
// moved, a read call added, and the thread command's file rewritten.
func nextWired() (string, map[string]string) {
	next := strings.Replace(wiredDecl, "https://wired.example.com/v1", "https://wired.example.org/v2", 1)
	next = strings.Replace(next, "  ping: { kind: read, request: { method: GET, path: /me } }\n",
		"  ping: { kind: read, request: { method: GET, path: /me } }\n  list: { kind: read, request: { method: GET, path: /lists } }\n", 1)
	files := map[string]string{}
	for prog, body := range wiredFiles {
		files[prog] = body
	}
	files["wired/thread"] = "#!/bin/sh\n# thread, rewritten\n"
	return next, files
}

// A source add ask for a name the yard has is a replacement: its card
// says what changes between the declaration that stands and the one the
// ask carries, each changed key whole old and new and each command file's
// change with its diff, who a standing grant of read now reaches, and what
// the source keeps; it takes it back with the old declaration asked again.
// The yard weighs it as code, with reads where a grant widens.
func TestSourceAddReplacementCard(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	if _, err := y.GrantSource(y.ctx, "wired", GrantRead, Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	next, files := nextWired()
	ask := sourceAddAsk(carriedOf(t, next, files))
	ask.Run.Argv = []string{"source add wired.yaml"}
	ask.Run.Does = []string{"a small change, nothing to read here"}
	ask.Run.Seen = map[string]string{"source wired": "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	run := got.Ask.Run
	oldThread := sha256.Sum256([]byte(wiredFiles["wired/thread"]))
	newThread := sha256.Sum256([]byte(files["wired/thread"]))
	joined := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"it replaces one source and copies 4 command files (5 commands), and changes nothing else.",
		"the source keeps its env_file, mint_command, region base, call limits and ignored senders, and every grant of it stands",
		"still owed after it: the credential, by a secret ask for wired.",
		"source wired: its calls go to https://wired.example.org/v2.",
		"it would replace the source wired the yard has under that name.",
		`source wired base: "https://wired.example.com/v1" -> "https://wired.example.org/v2".`,
		"call list (read): GET /lists.",
		"a standing grant of read of source wired widens to list for crew member yardmaster.",
		"command file wired/thread: [wired/thread, reply] (thread.reply, handed the credential): sha256 " + hex.EncodeToString(oldThread[:]) + " -> sha256 " + hex.EncodeToString(newThread[:]) + ", mode 0700 (1 insertions, 1 deletions).",
		"the copy would remove extra.txt, which neither declaration names: sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, `source wired calls: [{`) || !strings.Contains(joined, `/lists`) {
		t.Errorf("the card does not show the calls whole, old and new:\n%s", joined)
	}
	if strings.Contains(joined, "nothing to read here") {
		t.Errorf("the asker's sentences stood:\n%s", joined)
	}
	if got.Ask.Run.TakenBack() != `only the operator can take it back: yardr bead ask <bead> "<question>" --run "source add <a file holding the declaration that stands now>"` {
		t.Errorf("the card takes it back with %q", got.Ask.Run.TakenBack())
	}
	if want := []string{WeightCode, WeightReads}; !slices.Equal(weightClasses(got.Ask.Weight), want) {
		t.Errorf("the card weighs %v, want %v", weightClasses(got.Ask.Weight), want)
	}
	if want := "the command file wired/thread of wired (changed)"; !strings.Contains(got.Ask.Weight[0].Line, want) {
		t.Errorf("the code line is %q, want it to name %q", got.Ask.Weight[0].Line, want)
	}
	// The ask stores the copies that stand, with each file's change and
	// the entry nothing names, whole behind the card.
	carried := run.Carried[0]
	if len(carried.Inventory) != 5 || len(carried.Old) != 1 || carried.Old[0].Program != "wired/thread" || len(carried.Removed) != 1 || carried.Removed[0].Path != "extra.txt" {
		t.Fatalf("the stored inventory = %+v, old %+v, removed %+v", carried.Inventory, carried.Old, carried.Removed)
	}
	lines := strings.Join(got.Lines(), "\n")
	for _, want := range []string{"the change to command file wired/thread", "-# thread", "+# thread, rewritten", "the declaration of source wired, whole as carried:", "      base: https://wired.example.org/v2"} {
		if !strings.Contains(lines, want) {
			t.Errorf("the terminal card lacks %q:\n%s", want, lines)
		}
	}
	// Nothing is written by asking: the source stands as it did.
	if src, err := y.GetSource(y.ctx, "wired"); err != nil || src.Text != wiredDecl {
		t.Fatalf("asking changed the source: %+v, %v", src, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "wired", "thread")); err != nil || string(body) != wiredFiles["wired/thread"] {
		t.Fatalf("asking changed the copy: %q, %v", body, err)
	}
	checkReplacementCardChangedCallGrants(t, "source add")
}

// checkReplacementCardChangedCallGrants checks one of the existing
// replacement-card paths. A call that does not change, and a changed call
// held through read alone, have no by-name line; an approval is stale once a
// grant by name is given after the card.
func checkReplacementCardChangedCallGrants(t *testing.T, row string) {
	t.Helper()
	y := listYard(t)
	for _, g := range []struct {
		to   Grantee
		what string
		as   string
	}{
		{Grantee{Crew: "yardmaster"}, "note", "mckean"},
		{Grantee{Group: "builders"}, "note", ""},
		{Grantee{Group: "reviewers"}, GrantRead, ""},
		{Grantee{Group: "reviewers"}, "find", ""},
	} {
		if _, err := y.GrantSource(y.ctx, "crm", g.what, g.to, g.as, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	// runCRM with the path of each call named moved, so that it is changed.
	edit := func(calls ...string) string {
		t.Helper()
		moved := map[string][2]string{
			"find": {"{ method: GET, path: /contacts }", "{ method: GET, path: /people }"},
			"get":  {`path: "/contacts/{id}" }`, `path: "/people/{id}" }`},
			"note": {`path: "/contacts/{id}/notes"`, `path: "/contacts/{id}/remarks"`},
		}
		text := runCRM
		for _, c := range calls {
			if !strings.Contains(text, moved[c][0]) {
				t.Fatalf("runCRM has no %q to move", moved[c][0])
			}
			text = strings.Replace(text, moved[c][0], moved[c][1], 1)
		}
		return text
	}
	cardOf := func(next string) (uint64, []string) {
		t.Helper()
		var seq uint64
		if row == "source add" {
			ask := sourceAddAsk(carriedOf(t, next, nil))
			ask.Run.Argv = []string{"source add crm.yaml"}
			seq = y.ask(t, ask, "yardmaster")
		} else {
			y.Store.SetPackReader(func(context.Context, string, string, Mappings, API) (*PackReading, error) {
				return &PackReading{Name: "crm", Kind: "source", Label: "embedded in yardr test1",
					Sources: []AskSource{{Name: "crm", Text: next}}}, nil
			})
			seq = y.ask(t, Ask{Question: "Update the crm source from its pack?", To: "mckean",
				Context: []string{"the pack's crm moves a call the yard has"}, Run: &AskRun{Argv: []string{"pack apply crm"}}}, "yardmaster")
		}
		return seq, y.asked(t, seq).Ask.Run.Does
	}
	reach := func(call string) string {
		return "a standing grant of read of source crm widens to " + call + " for group reviewers."
	}
	holds := func(call, kind, who string) string {
		return "the changed call " + call + " (" + kind + ") is still granted by its name to " + who + " (yardr source revoke crm " + call + " takes it back)."
	}
	for _, c := range []struct {
		name string
		next string
		want []string // the card's lines about a standing grant, in order
	}{
		{"an update held by name, a read call held through read", edit("note", "get"), []string{
			reach("get"), holds("note", "update", "crew member yardmaster as mckean, group builders")}},
		{"a read call held by its own name", edit("find"), []string{
			reach("find"), holds("find", "read", "group reviewers")}},
		{"a changed call nobody holds by name", edit("get"), []string{reach("get")}},
		{"calls that do not change", strings.Replace(runCRM, "https://crm.example.com/v1", "https://crm.example.org/v2", 1), nil},
	} {
		_, does := cardOf(c.next)
		var got []string
		for _, line := range does {
			if strings.Contains(line, "a standing grant of read") || strings.Contains(line, "is still granted by its name") {
				got = append(got, line)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s, %s: the card's lines about a standing grant are %q, want %q:\n%s", c.name, row, got, c.want, strings.Join(does, "\n"))
		}
	}

	// The approver read who holds the call: a grant by name given since
	// makes the card another.
	seq, _ := cardOf(edit("note"))
	if _, err := y.GrantSource(y.ctx, "crm", "note", Grantee{Group: "reviewers"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err == nil || !strings.Contains(err.Error(), "the card is from before that changed") || !strings.Contains(err.Error(), "group reviewers") {
		t.Errorf("%s: approving after a grant by name given since = %v, want it stale, the card's new holder named", row, err)
	}
	if _, err := y.RevokeSource(y.ctx, "crm", "note", Grantee{Group: "reviewers"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
}

// crmRegions is runCRM with a second region the source can be set to call.
const crmRegions = runCRM + "bases:\n  eu: https://eu.crm.example.com/v1\n"

// crmMoved is crmRegions with both addresses moved: a replacement that
// changes nothing of its calls or files, only where the credential goes.
var crmMoved = strings.NewReplacer("https://crm.example.com/v1", "https://crm.example.org/v2",
	"https://eu.crm.example.com/v1", "https://eu.crm.example.org/v2").Replace(crmRegions)

// standingCRM is a yard whose source crm stands with every setting a
// replacement keeps (where its credential comes from, its mint command,
// its call limits, the region it calls, an ignored sender) and a grant of
// read of it.
func standingCRM(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "crm.yaml", crmRegions, "mckean"); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{SourceEnvFile: "/keys/crm.env", SourceMintCommand: "crm-mint --short",
		SourceCallsPerHour: "7", SourceUpdatesPerHour: "3", SourceBase: "eu"} {
		if _, err := y.SetSource(y.ctx, "crm", key, value, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	if err := y.write(func(w *wtx) error {
		var src Source
		if _, err := get(w.Tx, bSources, "crm", &src); err != nil {
			return err
		}
		src.Ignored = []string{"U1", "U2"}
		return put(w.Tx, bSources, "crm", src)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GrantSource(y.ctx, "crm", GrantRead, Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

// lastSourceAdded is the newest source_added event's data without the ask
// it was approved on.
func (y *askYard) lastSourceAdded(t *testing.T) (Event, map[string]any) {
	t.Helper()
	evs, err := y.EventsOfKind(y.ctx, "source_added", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("source_added events = %v, %v", evs, err)
	}
	data := map[string]any{}
	for k, v := range evs[0].Data {
		if k != "approved" {
			data[k] = v
		}
	}
	return evs[0], data
}

// Approving a replacement weighed replaces the source exactly as the
// terminal command does (PutSource): the new declaration, and everything
// the source had that a declaration does not say, which it keeps. Unweighed
// it is refused. A paired device's approval does the same as the socket's,
// strong as the card is. A replacement of nothing but where the credential
// goes is strong too.
func TestSourceAddReplacementApprove(t *testing.T) {
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y := standingCRM(t)
	before := y.source(t)
	grants := y.grants(t)
	ask := sourceAddAsk(carriedOf(t, crmMoved, nil))
	ask.Run.Argv = []string{"source add crm.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if classes := weightClasses(got.Ask.Weight); !slices.Equal(classes, []string{WeightCode}) {
		t.Fatalf("a replacement of nothing but the addresses weighs %v", classes)
	}
	if want := "the declaration of crm replaces the one that stands"; !strings.Contains(got.Ask.Weight[0].Line, want) {
		t.Errorf("the code line is %q, want it to say %q", got.Ask.Weight[0].Line, want)
	}
	if !strings.Contains(strings.Join(got.Ask.Run.Does, "\n"), `source crm base: "https://crm.example.com/v1" -> "https://crm.example.org/v2".`) {
		t.Errorf("the card does not show the address moving:\n%s", strings.Join(got.Ask.Run.Does, "\n"))
	}
	// crm keeps the file its credential is read from, so none is owed after.
	if strings.Contains(strings.Join(got.Ask.Run.Does, "\n"), "still owed after it") {
		t.Errorf("the card says a credential is owed for a source that keeps its file:\n%s", strings.Join(got.Ask.Run.Does, "\n"))
	}

	// Unweighed it is refused, and the ask stays open.
	want := "ask " + itoa(seq) + ` is weighty (code): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || err.Error() != want {
		t.Fatalf("approving unweighed = %v, want %q", err, want)
	}
	if now := y.source(t); !reflect.DeepEqual(now, before) {
		t.Fatalf("a refused approval changed the source: %+v, was %+v", now, before)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal", r.State)
	}

	// Through a device it is replaced, and everything below holds of the
	// approval as it did of the socket's.
	done, err := y.ApproveAsk(WithVia(y.ctx, "mckean", "phone"), y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev := lastAnswer(t, y); Via(ev) != "phone" || Witness(ev) != "" {
		t.Fatalf("the replacement's approval is via %q witnessed %q, want phone with no witness", Via(ev), Witness(ev))
	}
	if want := "source crm saved: 3 calls under https://eu.crm.example.org/v2 (base eu)"; done.Result != want {
		t.Fatalf("the approval recorded %q, want %q", done.Result, want)
	}
	if carry, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || carry != nil {
		t.Fatalf("a replacement with no files and no copies was left to carry out: %+v, %v", carry, err)
	}
	after := y.source(t)
	if after.Text != crmMoved || after.AddedBy != "mckean" {
		t.Fatalf("the source after the approval = %+v", after)
	}
	// Each setting the source had stands, and so does every grant.
	if after.EnvFile != "/keys/crm.env" || after.MintCommand != "crm-mint --short" || after.CallsPerHour != 7 ||
		after.UpdatesPerHour != 3 || after.Base != "eu" || !slices.Equal(after.Ignored, []string{"U1", "U2"}) {
		t.Fatalf("the replacement dropped a setting: %+v", after)
	}
	if now := y.grants(t); !slices.Equal(now, grants) {
		t.Fatalf("the grants after the approval = %v, were %v", now, grants)
	}
	ev, data := y.lastSourceAdded(t)
	y.approved(t, ev, seq)
	if data["replaced"] != true || data["source"] != "crm" {
		t.Fatalf("the event = %v, want it to say crm was replaced", data)
	}
	// The change's own event names the device that approved it, and is
	// the terminal's event in everything else.
	if Via(ev) != "phone" {
		t.Fatalf("the event is via %q, want phone", Via(ev))
	}
	delete(data, "via")

	// The terminal's own command, on a yard that began the same, leaves the
	// source and its event as the approval did.
	typed := standingCRM(t)
	if _, err := typed.PutSource(typed.ctx, "crm.yaml", crmMoved, "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := typed.source(t); !reflect.DeepEqual(got, after) {
		t.Errorf("typed, the source is %+v, and approved %+v", got, after)
	}
	if _, typedData := typed.lastSourceAdded(t); !reflect.DeepEqual(typedData, data) {
		t.Errorf("typed, the event says %v, and approved %v", typedData, data)
	}

	// A command run directly by the asker is refused as it always was; only
	// the card gains the road.
	if _, err := y.PutSource(y.ctx, "crm.yaml", crmRegions, "yardmaster"); err == nil || !strings.Contains(err.Error(), "adding a source is the operator's") {
		t.Fatalf("a crew member's own replacement = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}
}

// A replacement that brings command files, or drops the ones that stand,
// is carried out by the server after its approval, which makes the copies
// anew as source add does; one with nothing to copy and nothing to remove
// runs in the approval's own write.
func TestSourceAddReplacementIsCarriedOut(t *testing.T) {
	t.Parallel()
	next, files := nextWired()
	// Brings files over the copies that stand.
	y, _ := runYard(t)
	plantWired(t, y)
	ask := sourceAddAsk(carriedOf(t, next, files))
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq := y.ask(t, ask, "yardmaster")
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil || done.Result != CarryPending {
		t.Fatalf("the approval of one bringing files = %+v, %v", done, err)
	}
	if carry, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || carry == nil || carry.State != CarryApproved {
		t.Fatalf("the carrying record = %+v, %v", carry, err)
	}
	if src, err := y.GetSource(y.ctx, "wired"); err != nil || src.Text != next {
		t.Fatalf("the stored declaration = %+v, %v", src, err)
	}

	// Drops every file: nothing to copy, but the copies that stand are to go.
	y, _ = runYard(t)
	plantWired(t, y)
	bare := "name: wired\nbase: https://wired.example.com/v1\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	ask = sourceAddAsk(carriedOf(t, bare, nil))
	ask.Run.Argv = []string{"source add wired.yaml"}
	seq = y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	for _, want := range []string{
		"it replaces one source and copies no command files, and removes the command copies that stand, and changes nothing else.",
		"command file wired/thread would change: sha256 ",
		"the copy would remove extra.txt, which neither declaration names",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil || done.Result != CarryPending {
		t.Fatalf("the approval of one dropping every file = %+v, %v", done, err)
	}
	if carry, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || carry == nil {
		t.Fatalf("a replacement over copies that stand left no carrying record: %+v, %v", carry, err)
	}
}
