package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const crmFile = `name: crm
base: https://crm.example.com/api
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

const pairFile = `name: pair
base: https://pair.example.com/v1
credential: { env: PAIR_TOKEN, update_env: PAIR_UPDATE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
  put: { kind: update, request: { method: POST, path: /it } }
`

// secretYard is yard with ada as its operator, the sources crm (one
// variable) and pair (two), and a bead for the asks.
func secretYard(t *testing.T) (store.API, string) {
	t.Helper()
	return secretSetup(t, storetest.New(t))
}

// shortHome is a fresh empty directory whose path is short and fixed in
// length, whatever TMPDIR is: ten characters under /tmp, four hex digits
// keeping concurrent runs apart. A test whose view shows a file under the
// yard's home wraps where that home's length says; t.TempDir carries the
// test's name and a random number of varying length, so such a view wraps
// differently from run to run. The directory is removed when the test ends.
func shortHome(t *testing.T) string {
	t.Helper()
	for range 10 {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		dir := "/tmp/y" + hex.EncodeToString(b[:])
		if err := os.Mkdir(dir, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	}
	t.Fatal("no fresh directory under /tmp after 10 tries")
	return ""
}

// shortYard is secretYard with a home of short fixed length (shortHome),
// for the tests whose view shows the file a secret was given to: with a
// home of any length the gave line wraps where that length says, and the
// assertions below hold for this one. With YARDR_TEST_RPC=1 the store is
// served over a unix socket, as storetest.New serves its own.
func shortYard(t *testing.T) (store.API, string) {
	t.Helper()
	home := shortHome(t)
	s := storetest.Open(t, filepath.Join(home, "yard.db"))
	var st store.API = s
	if os.Getenv("YARDR_TEST_RPC") != "" {
		t.Cleanup(storetest.HoldDaemon(t, home))
		dir, err := os.MkdirTemp("", "ys")
		if err != nil {
			t.Fatal(err)
		}
		sock := filepath.Join(dir, "s")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: rpc.Handler(s, nil), ConnContext: rpc.ConnContext}
		go srv.Serve(ln)
		c := rpc.NewClient(sock)
		t.Cleanup(func() {
			_ = c.Close(context.Background())
			_ = srv.Close()
			os.RemoveAll(dir)
		})
		st = c
	}
	return secretSetup(t, st)
}

func secretSetup(t *testing.T, st store.API) (store.API, string) {
	t.Helper()
	st = yardOn(t, st)
	ctx := context.Background()
	must(t, st.SetSetting(ctx, store.OperatorSetting, me, me))
	for file, text := range map[string]string{"crm.yaml": crmFile, "pair.yaml": pairFile} {
		if _, err := st.PutSource(ctx, file, text, me); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the crm"}, "chief")
	must(t, err)
	return st, b.ID
}

// secretAsk is chief asking ada for the credential of source on the bead.
func secretAsk(t *testing.T, st store.API, bead, source string) uint64 {
	t.Helper()
	seq, err := st.AddAsk(context.Background(), bead, store.Ask{Question: "The API key for the CRM", To: me,
		Context: []string{"Create one at https://crm.example.com/account/api (read only)."},
		Secret:  &store.AskSecret{Source: source}}, "chief")
	must(t, err)
	return seq
}

// gives is a store whose GiveSecret is the test's: it counts the calls,
// keeps what the last was given, and answers with fail.
type gives struct {
	store.API
	calls int
	got   map[string]string
	fail  func(values map[string]string) error
}

func (g *gives) GiveSecret(_ context.Context, _ string, _ uint64, values map[string]string, _ string) (*store.SourceKeyed, error) {
	g.calls++
	g.got = values
	return nil, g.fail(values)
}

// frames are every screen the view had, as the terminal is sent them:
// with their styling.
type frames struct {
	v   *view
	all []string
}

// send gives the model a message, keeps the screen it leaves, runs what
// it asked for and keeps the screen after that too.
func (f *frames) send(msg tea.Msg) {
	f.v.t.Helper()
	_, cmd := f.v.m.Update(msg)
	f.all = append(f.all, f.v.m.View())
	f.v.do(cmd)
	f.all = append(f.all, f.v.m.View())
}

// lack fails when a frame holds one of what.
func (f *frames) lack(what ...string) {
	f.v.t.Helper()
	for i, frame := range f.all {
		for _, w := range what {
			if strings.Contains(frame, w) {
				f.v.t.Fatalf("frame %d of %d holds %q:\n%s", i+1, len(f.all), w, frame)
			}
		}
	}
}

func ctrlO() tea.Msg         { return tea.KeyMsg{Type: tea.KeyCtrlO} }
func runes(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func pasted(s string) tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

// The card of a secret ask ends in the key that types the value here and
// the command that does in a terminal.
func TestSecretCardNamesTheKeyAndTheCommand(t *testing.T) {
	st, bead := secretYard(t)
	seq := secretAsk(t, st, bead, "crm")
	v := withAsks(t, st, "chief", 120, 40)
	v.shows("a secret: CRM_TOKEN, for the source crm (https://crm.example.com)",
		"it goes to a file only the yard's server reads; no agent is shown it",
		fmt.Sprintf("  ctrl+o types it here · or in a terminal: yardr bead answer %s %d --secret", bead, seq))
	v.hides("answer with:")
	if n := len(strings.Split(v.screen(), "\n")); n != 40 {
		t.Fatalf("the view is %d lines", n)
	}
}

// ctrl+o says there is nothing to give where the conversation shows no
// secret ask: one with a question of another kind, another party's, and
// the yard's row. No field opens: what is typed next is the line's.
func TestNothingToGive(t *testing.T) {
	st, bead := secretYard(t)
	secretAsk(t, st, bead, "crm")
	askBead(t, st, "recorder")
	v := withAsks(t, st, "recorder", 120, 40)
	v.send(ctrlO())
	v.shows("nothing to give: recorder asks you for no secret")
	v.typed("hello")
	if got := v.m.input.Value(); got != "hello" || v.m.secret != nil {
		t.Fatalf("the line holds %q, the field is %v", got, v.m.secret)
	}
	v.m.input.Reset()

	v.key(tea.KeyShiftTab) // chief
	v.key(tea.KeyShiftTab) // the yard
	if !v.m.OnYard() {
		t.Fatalf("selected %q", v.m.Selected())
	}
	v.send(ctrlO())
	v.shows("nothing to give here")
	if v.m.secret != nil {
		t.Fatal("a field on the yard's row")
	}

	// A view that was given no way to give leaves the card's last line the
	// terminal's, and the key says so.
	v = open(t, st, "chief", 120, 40, func(o *Options) {
		o.Secrets = nil
		o.Asks = func(ctx context.Context) ([]store.ResolvedAsk, error) { return AsksFor(ctx, st, me) }
	})
	v.do(v.m.loadAsks())
	v.shows("answer with: yardr bead answer " + bead + " 1 --secret")
	v.send(ctrlO())
	v.shows("a secret is given in a terminal here: yardr bead answer " + bead + " 1 --secret")
	if v.m.secret != nil {
		t.Fatal("a field in a view that cannot give")
	}
}

// What is typed into the field is in no frame, not one character of it:
// typed key by key, pasted, left with esc, sent, and after the yard's
// answer. The terminal is sent dots. Esc sends nothing; enter gives the
// value to the yard, which writes it to the source's file, and the view
// shows the yard's line.
func TestTypedSecretIsInNoFrame(t *testing.T) {
	st, bead := shortYard(t)
	ctx := context.Background()
	seq := secretAsk(t, st, bead, "crm")
	v := withAsks(t, st, "chief", 120, 40)
	f := &frames{v: v}

	// Letters the view has nowhere else, so that one of them in a frame
	// is one that was typed.
	const odd = "ẞŦĦŊŁØ"
	for _, r := range odd {
		if strings.ContainsRune(v.m.View(), r) {
			t.Fatalf("the view has %q before anything is typed", r)
		}
	}
	f.send(ctrlO())
	v.shows("CRM_TOKEN for crm", "enter sends it to the yard · esc leaves, and nothing is sent",
		"enter send it to the yard  esc leave, nothing is sent  ^c quit")
	v.fits(120, 40)
	for i, r := range []rune(odd) {
		f.send(runes(string(r)))
		if !strings.Contains(v.screen(), "CRM_TOKEN for crm  "+strings.Repeat("•", i+1)) {
			t.Fatalf("after %d characters the field shows no %d dots:\n%s", i+1, i+1, v.screen())
		}
	}
	// Keys that are another's elsewhere are the field's here: no row is
	// selected, and nothing reaches the line for mail.
	f.send(tea.KeyMsg{Type: tea.KeyTab})
	if v.m.Selected() != "chief" || v.m.input.Value() != "" {
		t.Fatalf("selected %q, the line holds %q", v.m.Selected(), v.m.input.Value())
	}
	f.send(tea.KeyMsg{Type: tea.KeyEsc})
	v.shows("nothing was sent")
	v.hides("CRM_TOKEN for crm  ")
	if src, err := st.GetSource(ctx, "crm"); err != nil || src.EnvFile != "" {
		t.Fatalf("after esc the source's env_file is %q (%v)", src.EnvFile, err)
	}
	notes, err := st.Notes(ctx, bead)
	must(t, err)
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.State != store.AskOpen {
			t.Fatalf("after esc ask %d is %v", r.Note.Seq, r.State)
		}
	}

	// Pasted, with the line break it was copied with, and sent.
	const value = "sk-live-4f9a0c17e2b84d6f"
	f.send(ctrlO())
	f.send(pasted(value + "\n"))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	if v.m.secret != nil || v.m.input.Value() != "" {
		t.Fatalf("after enter the field is %v and the line holds %q", v.m.secret, v.m.input.Value())
	}
	src, err := st.GetSource(ctx, "crm")
	must(t, err)
	if text, err := os.ReadFile(src.EnvFile); err != nil || string(text) != "CRM_TOKEN="+value+"\n" {
		t.Fatalf("the file %q holds %q (%v)", src.EnvFile, text, err)
	}
	// Whole on one line: the home's ten characters keep the gave line
	// within the conversation's 85 columns.
	v.shows("ada gave CRM_TOKEN · it is set in", "sources/crm.env · yardr source check crm")
	f.all = append(f.all, v.m.View())
	v.stream()
	f.all = append(f.all, v.m.View())
	v.shows(fmt.Sprintf("ask %d · answered", seq), "ada gave CRM_TOKEN")

	// Given again, which replaces: the card of an answered ask still
	// names the key.
	const second = "sk-live-0000second"
	f.send(ctrlO())
	f.send(runes(second))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	if text, _ := os.ReadFile(src.EnvFile); string(text) != "CRM_TOKEN="+second+"\n" {
		t.Fatalf("given again, the file holds %q", text)
	}

	what := []string{value, value[:8], second}
	for _, r := range odd {
		what = append(what, string(r))
	}
	f.lack(what...)
}

// An ask for two names asks for one after the other and sends them
// together; an empty one is not sent.
func TestSecretOfTwoNames(t *testing.T) {
	st, bead := secretYard(t)
	secretAsk(t, st, bead, "pair")
	v := withAsks(t, st, "chief", 120, 40)
	f := &frames{v: v}
	f.send(ctrlO())
	v.shows("PAIR_TOKEN (1 of 2) for pair")
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	v.shows("nothing typed", "PAIR_TOKEN (1 of 2) for pair")
	f.send(runes("first-value"))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	v.shows("PAIR_UPDATE_TOKEN (2 of 2) for pair")
	v.hides("•")
	f.send(runes("second-value"))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	src, err := st.GetSource(context.Background(), "pair")
	must(t, err)
	text, err := os.ReadFile(src.EnvFile)
	must(t, err)
	if string(text) != "PAIR_TOKEN=first-value\nPAIR_UPDATE_TOKEN=second-value\n" {
		t.Fatalf("the file holds %q", text)
	}
	v.shows("ada gave PAIR_TOKEN and PAIR_UPDATE_TOKEN")
	f.lack("first-value", "second-value")
}

// A value the source marks as no secret is typed in the clear: the field
// shows the characters, not dots. Giving still writes the file and says the
// names, never the value.
func TestUnconcealedSecretIsTypedInTheClear(t *testing.T) {
	st, bead := secretYard(t)
	const gogFile = `name: gog
base: https://gog.example.com
credential:
  env: GOG_TOKEN
  header: "Authorization: Bearer {credential}"
  mint:
    - name: GOG_ACCOUNT
      secret: false
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	if _, err := st.PutSource(context.Background(), "gog.yaml", gogFile, me); err != nil {
		t.Fatal(err)
	}
	secretAsk(t, st, bead, "gog")
	v := withAsks(t, st, "chief", 120, 40)
	f := &frames{v: v}
	const value = "op@gog.example.com"
	f.send(ctrlO())
	v.shows("GOG_ACCOUNT for gog")
	f.send(runes(value))
	if !strings.Contains(v.screen(), "GOG_ACCOUNT for gog  "+value) {
		t.Fatalf("the field shows no %q:\n%s", value, v.screen())
	}
	if strings.Contains(v.screen(), strings.Repeat("•", len(value))) {
		t.Fatalf("the field shows dots for a value asked in the clear:\n%s", v.screen())
	}
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	src, err := st.GetSource(context.Background(), "gog")
	must(t, err)
	if text, err := os.ReadFile(src.EnvFile); err != nil || string(text) != "GOG_ACCOUNT="+value+"\n" {
		t.Fatalf("the file %q holds %q (%v)", src.EnvFile, text, err)
	}
	v.shows("ada gave GOG_ACCOUNT")
}

// A refusal of the yard is shown in its words and without the value, and
// the field is closed.
func TestSecretRefusedIsShownWithoutTheValue(t *testing.T) {
	st, bead := secretYard(t)
	ctx := context.Background()
	seq := secretAsk(t, st, bead, "crm")
	v := withAsks(t, st, "chief", 120, 40)
	f := &frames{v: v}
	const value = "sk-live-refused-91"
	f.send(ctrlO())
	f.send(runes(value))
	// Declined from a terminal while the field was open.
	must(t, st.AddAnswer(ctx, bead, store.Answer{Ask: seq, Decline: true}, me))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	v.shows(fmt.Sprintf("ask %d was declined", seq))
	if v.m.secret != nil {
		t.Fatal("the field is open after the yard answered")
	}
	if src, err := st.GetSource(ctx, "crm"); err != nil || src.EnvFile != "" {
		t.Fatalf("refused, the source's env_file is %q (%v)", src.EnvFile, err)
	}
	f.lack(value)

	// And from a server that says the value back: the view takes it out.
	g := &gives{API: st, fail: func(values map[string]string) error {
		return fmt.Errorf("CRM_TOKEN=%s is no key", values["CRM_TOKEN"])
	}}
	seq2 := secretAsk(t, st, bead, "crm")
	v = withAsks(t, g, "chief", 120, 40)
	f = &frames{v: v}
	f.send(ctrlO())
	v.shows("CRM_TOKEN for crm")
	f.send(runes(value))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	if g.calls != 1 || g.got["CRM_TOKEN"] != value {
		t.Fatalf("the yard was given %d times, last %d names (ask %d)", g.calls, len(g.got), seq2)
	}
	v.shows("CRM_TOKEN=••• is no key")
	f.lack(value)
}

// A call that ends without the yard's answer says that the value may be
// written and is to be given again, and never that it was sent. A value
// that is written and not recorded is said in the yard's words, whole, in
// a terminal narrower than they are.
func TestSecretWithoutAnAnswer(t *testing.T) {
	st, bead := secretYard(t)
	secretAsk(t, st, bead, "crm")
	g := &gives{API: st, fail: func(map[string]string) error {
		return fmt.Errorf("yardr server: unexpected EOF: %w", store.ErrNoAnswer)
	}}
	v := withAsks(t, g, "chief", 80, 30)
	f := &frames{v: v}
	const value = "sk-live-lost-55"
	f.send(ctrlO())
	f.send(runes(value))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	v.shows("the yard did not answer. The value may be written. Give it again.")
	v.hides("sent", "EOF")
	v.fits(80, 30)
	if v.m.secret != nil {
		t.Fatal("the field is open after the call ended")
	}
	// The next key that does something takes the line away.
	v.typed("x")
	v.hides("did not answer")
	v.m.input.Reset()

	lost := &store.KeyNotRecorded{Names: []string{"CRM_TOKEN"}, File: "/home/ada/.yardr/sources/crm.env", Err: errors.New("no space left on device")}
	g.fail = func(map[string]string) error { return lost }
	f.send(ctrlO())
	f.send(runes(value))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	v.shows("CRM_TOKEN is written to /home/ada/.yardr/sources/crm.env, but the yard could not",
		"record it: no space left on device. Give it again.")
	v.hides("ada gave")
	v.fits(80, 30)
	if g.calls != 2 {
		t.Fatalf("the yard was called %d times", g.calls)
	}
	f.lack(value)
}

const pushFile = `name: push
base: https://push.example.com/1
credential: { env: PUSH_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string } }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PUSH_USER }, text: text }
`

// An ask for the person's own address in a source is typed in the same
// field: the card is the yard's line for it, the value goes to a file of
// the person's, and the yard's line afterwards names the command that
// tells them through the source, not the check of a source's credential.
func TestOwnAddressIsTypedInTheField(t *testing.T) {
	st, bead := shortYard(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "push.yaml", pushFile, me); err != nil {
		t.Fatal(err)
	}
	seq, err := st.AddAsk(ctx, bead, store.Ask{Question: "Your id at push", To: me,
		Context: []string{"It is on the first page after login."},
		Secret:  &store.AskSecret{Source: "push", User: me}}, "chief")
	must(t, err)
	v := withAsks(t, st, "chief", 120, 40)
	f := &frames{v: v}
	v.shows("a secret: PUSH_USER, ada's own address in the source push")

	const value = "u-4f9a0c17e2b84d6f"
	f.send(ctrlO())
	v.shows("PUSH_USER for push")
	f.send(pasted(value))
	f.send(tea.KeyMsg{Type: tea.KeyEnter})
	u, err := st.GetUser(ctx, me)
	must(t, err)
	if text, err := os.ReadFile(u.EnvFile); err != nil || string(text) != "PUSH_USER="+value+"\n" || !strings.HasSuffix(u.EnvFile, "users/ada.env") {
		t.Fatalf("the file %q holds %q (%v)", u.EnvFile, text, err)
	}
	// The gave line is longer than the conversation's 85 columns, so it
	// wraps; joined, it names the command that tells ada through the
	// source, wherever the break falls.
	v.shows("ada gave PUSH_USER · it is set in")
	v.showsUnwrapped("yardr user set ada notify=push tells ada through it")
	v.hides("source check push")
	// And whole on one row where the conversation has the columns for it.
	v.send(tea.WindowSizeMsg{Width: 200, Height: 40})
	v.shows("yardr user set ada notify=push tells ada through it")
	f.all = append(f.all, v.m.View())
	v.stream()
	f.all = append(f.all, v.m.View())
	v.shows(fmt.Sprintf("ask %d · answered", seq), "ada gave PUSH_USER")
	f.lack(value, value[:8])
}

// A login input is typed in the field with the waiting login named before
// it, as a source's credential names its source.
func TestSecretFieldNamesTheWaitingLogin(t *testing.T) {
	s := &secretField{login: "claude-a1b2c3d4", env: []string{"INPUT"}, values: map[string]string{}}
	if got := s.label(); got != "INPUT for the waiting login claude-a1b2c3d4  " {
		t.Fatalf("label = %q", got)
	}
}
