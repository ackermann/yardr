package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// secretYard is a yard whose operator jo is asked for the credential of the
// source crm, with a second user sam and a device of each.
type secretYard struct {
	st   *store.Store
	u    *UserServer
	h    http.Handler
	log  *bytes.Buffer // what the listener's handler logged
	db   string        // the store's file
	file string        // where the source's credential is written
	ask  uint64
	// closed is set by a test that closed the store itself.
	closed bool
	jo     string // the token of jo's device phone
	sam    string // the token of sam's device tablet
}

const secretDecl = "name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"

func newSecretYard(t *testing.T) *secretYard {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	y := &secretYard{log: &bytes.Buffer{}, db: filepath.Join(dir, "yard.db"), file: filepath.Join(dir, "sources", "crm.env")}
	st, err := store.Open(y.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A test that reads the store's file has closed it already.
		if !y.closed {
			st.Close(context.Background())
		}
	})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.AddUser(ctx, "jo", "jo")
	must(err)
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err = st.AddUser(ctx, "sam", "jo")
	must(err)
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo")
	must(err)
	_, err = st.PutSource(ctx, "crm.yaml", secretDecl, "jo")
	must(err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Read the CRM", ID: "yd-crm"}, "yardmaster")
	must(err)
	y.ask, err = st.AddAsk(ctx, b.ID, store.Ask{Question: "The API key for the CRM", To: "jo",
		Context: []string{"Create one in the CRM's settings."}, Secret: &store.AskSecret{Source: "crm"}}, "yardmaster")
	must(err)
	phone, err := st.AddDevice(ctx, "jo", "phone", "jo")
	must(err)
	tablet, err := st.AddDevice(ctx, "sam", "tablet", "jo")
	must(err)
	y.st, y.jo, y.sam = st, phone.Token, tablet.Token
	y.u = NewUserServer(st, func(context.Context) string { return "south" },
		slog.New(slog.NewTextHandler(y.log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	y.h = y.u.Handler()
	return y
}

// onTheMachine is the address of a request that reached the listener from
// the yard's own machine: the proxy that ended TLS there. elsewhere is one
// that came from another machine, in plain HTTP as the listener speaks it.
const (
	onTheMachine = "127.0.0.1:40000"
	elsewhere    = "203.0.113.9:40000"
)

// give asks the listener for GiveSecret from the address from, with the
// bearer credential and the headers given as name, value pairs.
func (y *secretYard) give(from, credential string, body io.Reader, header ...string) *httptest.ResponseRecorder {
	return y.post("GiveSecret", from, credential, body, header...)
}

// post asks the listener for the call method, as give does for GiveSecret.
func (y *secretYard) post(method, from, credential string, body io.Reader, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/user/call/"+method, body)
	r.RemoteAddr = from
	if credential != "" {
		r.Header.Set("Authorization", "Bearer "+credential)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	y.h.ServeHTTP(w, r)
	return w
}

// args is the body of a giving of values to the yard's ask.
func (y *secretYard) args(values any) string {
	b, _ := json.Marshal(map[string]any{"bead": "yd-crm", "ask": y.ask, "values": values, "weighed": true})
	return string(b)
}

// askFor puts a secret ask for sec on the bead, of to, and returns the body
// of a giving of value for it under name.
func (y *secretYard) askFor(t *testing.T, to string, sec *store.AskSecret, name, value string) string {
	t.Helper()
	seq, err := y.st.AddAsk(context.Background(), "yd-crm", store.Ask{Question: "A secret?", To: to,
		Context: []string{"It is shown where the service makes it."}, Secret: sec}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"bead": "yd-crm", "ask": seq, "values": map[string]string{name: value}, "weighed": true})
	return string(b)
}

// untouched fails the test when anything was given: the env file is not
// there and the ask is open, with no answer.
func (y *secretYard) untouched(t *testing.T, after string) {
	t.Helper()
	if _, err := os.Stat(filepath.Dir(y.file)); !os.IsNotExist(err) {
		t.Errorf("%s: there is a file for the source's credential (%v)", after, err)
	}
	q, err := y.st.UserAsks(context.Background(), "jo", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 || q.Open[0].Secret == nil {
		t.Errorf("%s: jo's open asks are %+v, want the one secret ask", after, q.Open)
	}
}

// answers is the notes of the bead that answer an ask, each as who wrote
// it and the device it came through: a giving is one, and giving again is
// one more.
func (y *secretYard) answers(t *testing.T) []string {
	t.Helper()
	notes, err := y.st.Notes(context.Background(), "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range notes {
		if n.Kind == store.NoteAnswer {
			got = append(got, n.Author+" via "+n.Via)
		}
	}
	return got
}

// A device of the yard's operator gives a secret over the listener: the env
// file holds it, the ask is answered with the names, and the note and the
// events of the giving say how it came, as the device's other writes do.
func TestUserGivesASecret(t *testing.T) {
	const value = "s3cret-0f-the-yard"
	y := newSecretYard(t)
	ctx := context.Background()
	head, _ := y.st.Head(ctx)
	// The actor is left out: the listener fills in the token's user.
	w := y.give(onTheMachine, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})), "X-Forwarded-Proto", "https")
	var got struct {
		Result *store.SourceKeyed `json:"result"`
		Error  *wireError         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error != nil || got.Result == nil {
		t.Fatalf("the giving is answered %d %s (%v)", w.Code, w.Body, err)
	}
	if got.Result.File != y.file || got.Result.State != "CRM_TOKEN is set in "+y.file {
		t.Errorf("the answer is %+v", got.Result)
	}
	if b, _ := os.ReadFile(y.file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Errorf("the env file holds %q", b)
	}
	evs, err := y.st.EventsSince(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, fmt.Sprintf("%s by %s via %s", e.Kind, e.Actor, store.Via(e)))
	}
	if want := "noted by jo via phone, source_set by jo via phone"; strings.Join(kinds, ", ") != want {
		t.Errorf("the events of the giving are %q, want %q", kinds, want)
	}
	if bead := call(t, y.h, y.jo, "Bead", `{"bead":"yd-crm"}`); !strings.Contains(bead, `"given":["CRM_TOKEN"]`) {
		t.Errorf("the bead does not say what was given: %s", bead)
	}
	notes, err := y.st.Notes(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(store.NoteBlocks(notes, time.Now(), ""), "\n"); !strings.Contains(text, "jo (via phone) gave CRM_TOKEN") {
		t.Errorf("the notes as they are printed do not say how the giving came:\n%s", text)
	}
	if q, err := y.st.UserAsks(ctx, "jo", "jo"); err != nil || len(q.Open) != 0 {
		t.Errorf("jo's open asks after the giving: %+v, %v", q, err)
	}
	// Given on the yard's own machine, it says no device.
	if _, err := y.st.GiveSecret(ctx, "yd-crm", y.ask, map[string]string{"CRM_TOKEN": "typed-again"}, "jo", true); err != nil {
		t.Fatal(err)
	}
	if evs, _ = y.st.EventsSince(ctx, evs[len(evs)-1].Seq, 100); len(evs) != 1 || store.Via(evs[0]) != "" {
		t.Errorf("a giving at the yard's terminal wrote %+v, want one event with no via", evs)
	}

	// A second operator's device gives the secret asked of the first: a
	// credential of the yard is any operator's to set.
	t.Run("a second operator gives the secret", func(t *testing.T) {
		y := newSecretYard(t)
		if _, err := y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo"); err != nil {
			t.Fatal(err)
		}
		w := y.give(onTheMachine, y.sam, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": "s3cret-0f-the-yard"})))
		if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("sam's giving is answered %d %s", w.Code, w.Body)
		}
		if b, _ := os.ReadFile(y.file); string(b) != "CRM_TOKEN=s3cret-0f-the-yard\n" {
			t.Errorf("the env file holds %q", b)
		}
	})
}

// A device of the yard's operator gives a waiting login's input over the
// listener: the waiter is handed the value, the ask is answered with the
// names, and the value is in no note. It does so from the yard's own machine
// and down the link from the edge, where this is the one secret taken unless
// the yard is hosted (TestUserGiveSecretIsNotTakenOverTheLink/hosted), and
// the answer says how it came. A device of another user is answered as for a
// method the yard does not have.
func TestUserGivesALoginInput(t *testing.T) {
	for name, c := range map[string]struct {
		from   string
		header []string
		via    string
		hosted bool
	}{
		"from the yard's machine":                       {onTheMachine, []string{"X-Forwarded-Proto", "https"}, "jo via phone", false},
		"down the link from the edge":                   {edgelink.RemoteAddr, nil, "jo via phone via edge", false},
		"down the link from the edge, on a hosted yard": {edgelink.RemoteAddr, nil, "jo via phone via edge", true},
	} {
		t.Run(name, func(t *testing.T) { givesALoginInput(t, c.from, c.header, c.via, c.hosted) })
	}
}

// givesALoginInput is TestUserGivesALoginInput from the address from, the
// answer on the bead being by via, on a hosted yard when hosted.
func givesALoginInput(t *testing.T, from string, header []string, via string, hosted bool) {
	const value = "code-fr0m-the-device"
	y := newSecretYard(t)
	ctx := context.Background()
	if hosted {
		if err := y.st.SetSetting(ctx, store.HostedSetting, "true", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	seq, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "The code the sign-in page showed", To: "jo",
		Context: []string{"The login waits for what the sign-in page showed."},
		Secret:  &store.AskSecret{Login: "claude-a1b2c3d4"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	// The wait registers in its own goroutine: the giving below starts once
	// a probe is refused as a duplicate. A wait refused as a duplicate
	// tries again: a probe may hold the reference for the moment it checks.
	waited := make(chan string, 1)
	go func() {
		for {
			got, err := y.st.LoginWait(context.Background(), "claude-a1b2c3d4", "")
			if err == nil {
				waited <- got
				return
			}
			if !strings.Contains(err.Error(), "already waits") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, leave := context.WithCancel(context.Background())
		leave()
		_, err := y.st.LoginWait(probe, "claude-a1b2c3d4", "")
		if err != nil && strings.Contains(err.Error(), "already waits") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wait never registered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	body, _ := json.Marshal(map[string]any{"bead": "yd-crm", "ask": seq, "values": map[string]string{"INPUT": value}, "weighed": true})
	// The actor is left out: the listener fills in the token's user.
	w := y.give(from, y.jo, bytes.NewReader(body), header...)
	var got struct {
		Result *store.SourceKeyed `json:"result"`
		Error  *wireError         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error != nil || got.Result == nil {
		t.Fatalf("the giving is answered %d %s (%v)", w.Code, w.Body, err)
	}
	if got.Result.Login != "claude-a1b2c3d4" || got.Result.File != "" {
		t.Errorf("the answer is %+v", got.Result)
	}
	if handed := <-waited; handed != value {
		t.Fatalf("the waiter was handed %q", handed)
	}
	if answers := y.answers(t); !slices.Equal(answers, []string{via}) {
		t.Errorf("the giving is answered on the bead by %q, want %q", answers, via)
	}
	notes, err := y.st.Notes(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, value) {
			t.Errorf("note %d holds the value: %q", n.Seq, n.Text)
		}
	}
	if bead := call(t, y.h, y.jo, "Bead", `{"bead":"yd-crm"}`); !strings.Contains(bead, `"given":["INPUT"]`) {
		t.Errorf("the bead does not say what was given: %s", bead)
	}
	// A device of another user names no ask of its own: not there, wherever
	// it calls from.
	none, _ := json.Marshal(noSuchMethod("GiveSecret"))
	w = y.give(from, y.sam, bytes.NewReader(body), header...)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != string(none) {
		t.Errorf("sam's device is answered %d %s, want no such method", w.Code, w.Body)
	}
}

// addressDecl is a source that tells people: PUSH_USER is a user's own
// address in it.
const addressDecl = `name: push
base: https://push.example.com/1
credential: { env: PUSH_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string } }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PUSH_USER }, text: text }
`

// A secret ask for a user's own address in a source is that user's to
// answer from a device of their own, as it is on the socket: the value goes
// to a file of theirs, the ask is answered with the name, and the events
// say how it came. Their device may give again, which replaces the value
// and is a note more, and the operator's device gives it for them. A device
// of a third user is answered as for a method the yard does not have, and
// nothing that was sent is in a refusal or the listener's log.
func TestUserGivesTheirOwnAddress(t *testing.T) {
	const value = "u-0f-sam-s3cret"
	y := newSecretYard(t)
	ctx := context.Background()
	if _, err := y.st.PutSource(ctx, "push.yaml", addressDecl, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddUser(ctx, "kim", "jo"); err != nil {
		t.Fatal(err)
	}
	laptop, err := y.st.AddDevice(ctx, "kim", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Your id at push", To: "sam",
		Context: []string{"It is on the first page after login."}, Secret: &store.AskSecret{Source: "push", User: "sam"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(y.db), "users", "sam.env")
	args := fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_USER":%q},"weighed":true}`, seq, value)
	none, _ := json.Marshal(noSuchMethod("GiveSecret"))

	var said []string
	// Not theirs: a third user's device, for sam's ask.
	w := y.give(onTheMachine, laptop.Token, strings.NewReader(args))
	if strings.TrimSpace(w.Body.String()) != string(none) {
		t.Errorf("kim's device is answered %s, want what a method the yard does not have answers", w.Body)
	}
	said = append(said, w.Body.String())
	// Theirs, and not in the clear; nor as anyone else.
	w = y.give(elsewhere, y.sam, strings.NewReader(args))
	if !strings.Contains(w.Body.String(), "a secret is taken over https only") {
		t.Errorf("sam's device from another machine in plain HTTP is answered %s", w.Body)
	}
	said = append(said, w.Body.String())
	w = y.give(onTheMachine, y.sam, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_USER":"x"},"actor":%q}`, seq, value)))
	if !strings.Contains(w.Body.String(), "this device calls as sam") {
		t.Errorf("sam's device as another actor is answered %s", w.Body)
	}
	said = append(said, w.Body.String())
	w = y.give(onTheMachine, y.sam, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_TOKEN":%q}}`, seq, value)))
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("the source's own key was taken for sam's ask: %s", w.Body)
	}
	said = append(said, w.Body.String())
	// A value the file cannot hold is refused before the store looks for the
	// file, as the write refuses it after the file's name: a device that is not
	// the operator's is told which value, and not the path it would have gone
	// to. The operator's device is told the file, as before.
	for _, bad := range []struct{ value, why string }{
		{"", "is empty"}, {"one\ntwo", "has a line break in it"}, {" padded", "begins or ends with white space"}} {
		refused := fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_USER":%q},"weighed":true}`, seq, bad.value)
		w = y.give(onTheMachine, y.sam, strings.NewReader(refused))
		if !strings.Contains(w.Body.String(), "the value for PUSH_USER "+bad.why) || strings.Contains(w.Body.String(), file) || strings.Contains(w.Body.String(), "env_file") {
			t.Errorf("sam's device sending %q is answered %s, want a refusal that names the variable and no file", bad.value, w.Body)
		}
		said = append(said, w.Body.String())
		w = y.give(onTheMachine, y.jo, strings.NewReader(refused))
		if !strings.Contains(w.Body.String(), "env_file "+file+": the value for PUSH_USER "+bad.why) {
			t.Errorf("the operator's device sending %q for sam is answered %s, want the refusal that names the file", bad.value, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatalf("a refused giving made a file of sam's (%v)", err)
	}

	head, _ := y.st.Head(ctx)
	w = y.give(onTheMachine, y.sam, strings.NewReader(args), "X-Forwarded-Proto", "https")
	var got struct {
		Result *store.SourceKeyed `json:"result"`
		Error  *wireError         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error != nil || got.Result == nil {
		t.Fatalf("the giving is answered %d %s (%v)", w.Code, w.Body, err)
	}
	// Where the value went is a path on the yard's machine, of no use to a
	// device that is not the operator's: the file, the words that say where the
	// address is set, and the source's record are held (giveSecret).
	if got.Result.File != "" || got.Result.State != "" || got.Result.Missing != "" || got.Result.Source != nil ||
		got.Result.User != "sam" || !slices.Equal(got.Result.Given, []string{"PUSH_USER"}) ||
		got.Result.Next != "yardr user set sam notify=push tells sam through it" {
		t.Errorf("the answer is %+v", got.Result)
	}
	if strings.Contains(w.Body.String(), file) || strings.Contains(w.Body.String(), "env_file") {
		t.Errorf("sam's device is told where its address went: %s", w.Body)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSH_USER="+value+"\n" {
		t.Errorf("sam's file holds %q", b)
	}
	evs, err := y.st.EventsSince(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, fmt.Sprintf("%s by %s via %s", e.Kind, e.Actor, store.Via(e)))
	}
	if want := "noted by sam via tablet, user_set by sam via tablet"; strings.Join(kinds, ", ") != want {
		t.Errorf("the events of the giving are %q, want %q", kinds, want)
	}
	if u, err := y.st.GetUser(ctx, "sam"); err != nil || u.EnvFile != file || u.Notify != "" {
		t.Errorf("sam = %+v, %v: want the env_file set and no notify", u, err)
	}
	// Their device again: what was given is replaced, and the giving is a
	// note more.
	const again = "u-0f-sam-again"
	w = y.give(onTheMachine, y.sam, strings.NewReader(strings.Replace(args, value, again, 1)))
	if !strings.Contains(w.Body.String(), `"given":["PUSH_USER"]`) {
		t.Errorf("sam's device giving again is answered %s", w.Body)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSH_USER="+again+"\n" {
		t.Errorf("sam's file holds %q after the second giving", b)
	}
	if got, want := strings.Join(y.answers(t), ", "), "sam via tablet, sam via tablet"; got != want {
		t.Errorf("the answers on the bead are %q, want %q", got, want)
	}
	// The operator's device, for them.
	w = y.give(onTheMachine, y.jo, strings.NewReader(strings.Replace(args, value, "typed-again", 1)))
	if !strings.Contains(w.Body.String(), `"given":["PUSH_USER"]`) {
		t.Errorf("the operator's device giving sam's address is answered %s", w.Body)
	}
	// The operator's device is told where it went, as the socket is.
	var told struct {
		Result *store.SourceKeyed `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &told); err != nil || told.Result == nil ||
		told.Result.File != file || told.Result.State != "PUSH_USER is set in "+file {
		t.Errorf("the operator's device giving sam's address is told %+v (%v), want the file %s", told.Result, err, file)
	}
	if b, _ := os.ReadFile(file); string(b) != "PUSH_USER=typed-again\n" {
		t.Errorf("sam's file holds %q after the operator's giving", b)
	}
	// The yard's own credential is as closed to sam's device as it was.
	y.untouched(t, "sam's own address")

	kept := []string{w.Body.String(), call(t, y.h, y.sam, "Bead", `{"bead":"yd-crm"}`), call(t, y.h, y.sam, "UserAsks", `{}`)}
	events, _ := json.Marshal(evs)
	y.u.Flush(true)
	for where, held := range map[string][]byte{
		"the refusals":          []byte(strings.Join(said, "\n")),
		"what the calls answer": []byte(strings.Join(kept, "\n")),
		"the events":            events,
		"the listener's log":    y.log.Bytes(),
	} {
		if bytes.Contains(held, []byte(value)) {
			t.Errorf("%s hold the value", where)
		}
	}
}

// A secret ask for a user's own credential in a source is that user's to
// answer from a device of theirs (ownSecret): the listener serves the
// giving to them and to the operator's device, and to nobody else. Their
// device may give again, which replaces the value and is a note more. The
// source needs no notify key.
func TestUserGivesTheirOwnCredential(t *testing.T) {
	const value = "tok-0f-sam-s3cret"
	y := newSecretYard(t)
	ctx := context.Background()
	if _, err := y.st.AddUser(ctx, "kim", "jo"); err != nil {
		t.Fatal(err)
	}
	laptop, err := y.st.AddDevice(ctx, "kim", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Your CRM token", To: "sam",
		Context: []string{"Create one in the CRM's settings."}, Secret: &store.AskSecret{Source: "crm", As: "sam"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(y.db), "users", "sam.env")
	args := fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":%q},"weighed":true}`, seq, value)
	none, _ := json.Marshal(noSuchMethod("GiveSecret"))

	var said []string
	// Not theirs: a third user's device, for sam's ask.
	w := y.give(onTheMachine, laptop.Token, strings.NewReader(args))
	if strings.TrimSpace(w.Body.String()) != string(none) {
		t.Errorf("kim's device is answered %s, want what a method the yard does not have answers", w.Body)
	}
	said = append(said, w.Body.String())
	// Theirs, and not in the clear; nor as anyone else.
	w = y.give(elsewhere, y.sam, strings.NewReader(args))
	if !strings.Contains(w.Body.String(), "a secret is taken over https only") {
		t.Errorf("sam's device from another machine in plain HTTP is answered %s", w.Body)
	}
	said = append(said, w.Body.String())
	w = y.give(onTheMachine, y.sam, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"x"},"actor":%q}`, seq, value)))
	if !strings.Contains(w.Body.String(), "this device calls as sam") {
		t.Errorf("sam's device as another actor is answered %s", w.Body)
	}
	said = append(said, w.Body.String())
	w = y.give(onTheMachine, y.sam, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"CRM_PASSWORD":%q}}`, seq, value)))
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("a name the ask did not ask for was taken for sam's ask: %s", w.Body)
	}
	said = append(said, w.Body.String())
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatalf("a refused giving made a file of sam's (%v)", err)
	}

	head, _ := y.st.Head(ctx)
	w = y.give(onTheMachine, y.sam, strings.NewReader(args), "X-Forwarded-Proto", "https")
	var got struct {
		Result *store.SourceKeyed `json:"result"`
		Error  *wireError         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error != nil || got.Result == nil {
		t.Fatalf("the giving is answered %d %s (%v)", w.Code, w.Body, err)
	}
	if got.Result.File != "" || got.Result.State != "" || got.Result.Missing != "" || got.Result.Source != nil ||
		got.Result.As != "sam" || got.Result.User != "" || !slices.Equal(got.Result.Given, []string{"CRM_TOKEN"}) ||
		got.Result.Next != "yardr source check crm --as sam" {
		t.Errorf("the answer is %+v", got.Result)
	}
	if strings.Contains(w.Body.String(), file) || strings.Contains(w.Body.String(), "env_file") {
		t.Errorf("sam's device is told where its credential went: %s", w.Body)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Errorf("sam's file holds %q", b)
	}
	evs, err := y.st.EventsSince(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, fmt.Sprintf("%s by %s via %s", e.Kind, e.Actor, store.Via(e)))
	}
	if want := "noted by sam via tablet, user_set by sam via tablet"; strings.Join(kinds, ", ") != want {
		t.Errorf("the events of the giving are %q, want %q", kinds, want)
	}
	if u, err := y.st.GetUser(ctx, "sam"); err != nil || u.EnvFile != file {
		t.Errorf("sam = %+v, %v: want the env_file set", u, err)
	}
	// Their device again: what was given is replaced, and the giving is a
	// note more.
	const again = "tok-0f-sam-again"
	w = y.give(onTheMachine, y.sam, strings.NewReader(strings.Replace(args, value, again, 1)))
	if !strings.Contains(w.Body.String(), `"given":["CRM_TOKEN"]`) {
		t.Errorf("sam's device giving again is answered %s", w.Body)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+again+"\n" {
		t.Errorf("sam's file holds %q after the second giving", b)
	}
	if got, want := strings.Join(y.answers(t), ", "), "sam via tablet, sam via tablet"; got != want {
		t.Errorf("the answers on the bead are %q, want %q", got, want)
	}
	// The operator's device, for them.
	w = y.give(onTheMachine, y.jo, strings.NewReader(strings.Replace(args, value, "typed-again", 1)))
	if !strings.Contains(w.Body.String(), `"given":["CRM_TOKEN"]`) {
		t.Errorf("the operator's device giving sam's credential is answered %s", w.Body)
	}
	var told struct {
		Result *store.SourceKeyed `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &told); err != nil || told.Result == nil ||
		told.Result.File != file || told.Result.State != "CRM_TOKEN is set in "+file {
		t.Errorf("the operator's device giving sam's credential is told %+v (%v), want the file %s", told.Result, err, file)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=typed-again\n" {
		t.Errorf("sam's file holds %q after the operator's giving", b)
	}
	// The yard's own credential is as closed to sam's device as it was.
	y.untouched(t, "sam's own credential")

	kept := []string{w.Body.String(), call(t, y.h, y.sam, "Bead", `{"bead":"yd-crm"}`), call(t, y.h, y.sam, "UserAsks", `{}`)}
	events, _ := json.Marshal(evs)
	y.u.Flush(true)
	for where, held := range map[string][]byte{
		"the refusals":          []byte(strings.Join(said, "\n")),
		"what the calls answer": []byte(strings.Join(kept, "\n")),
		"the events":            events,
		"the listener's log":    y.log.Bytes(),
	} {
		if bytes.Contains(held, []byte(value)) {
			t.Errorf("%s hold the value", where)
		}
	}
}

// GiveSecret for a source's credential is on the listener for a device of
// the yard's operator and for nobody else. A device of another user, a
// credential that resolves to the operator and is no device token of the
// yard, and the device of an operator made a member are answered as for a
// method the yard does not have; a device that was removed, a peer's key
// and no credential at all are not let in. Nothing that was sent is said
// back, and nothing is given. Each but a device of a user who is not the
// operator is answered before the request's body is read: that one is read
// for the ask it names, which may be for the user's own address or
// credential (TestUserGivesTheirOwnAddress).
func TestUserGiveSecretIsForTheOperatorsDeviceAlone(t *testing.T) {
	ctx := context.Background()
	none, _ := json.Marshal(noSuchMethod("GiveSecret"))
	notThere := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != string(none) {
			t.Errorf("answered %d %s, want what a method the yard does not have answers: %s", w.Code, w.Body, none)
		}
	}
	notLetIn := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusUnauthorized || strings.TrimSpace(w.Body.String()) != http.StatusText(http.StatusUnauthorized) {
			t.Errorf("answered %d %s, want 401 and no more", w.Code, w.Body)
		}
	}
	// A device of another user is asked which ask it names, since one for
	// that user's own address or credential is theirs to answer
	// (ownSecret): its body is read, and for the operator's ask it is
	// answered as before.
	t.Run("a device of another user", func(t *testing.T) {
		y := newSecretYard(t)
		for _, body := range []string{
			y.args(map[string]string{"CRM_TOKEN": "s3cret-0f-the-yard"}),
			`{"bead":"yd-crm","ask":999999,"values":{"CRM_TOKEN":"s3cret-0f-the-yard"}}`,
			`{"bead":"yd-none","ask":1}`,
			`["yd-crm", 1, {"CRM_TOKEN":"s3cret-0f-the-yard"}, "sam"]`,
			``,
		} {
			notThere(t, y.give(onTheMachine, y.sam, strings.NewReader(body)))
		}
		y.untouched(t, "sam's device")
	})
	t.Run("a device removed", func(t *testing.T) {
		y := newSecretYard(t)
		if _, err := y.st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
			t.Fatal(err)
		}
		notLetIn(t, y.give(onTheMachine, y.jo, unread{t}))
		y.untouched(t, "a removed device")
	})
	t.Run("a peer's key", func(t *testing.T) {
		y := newSecretYard(t)
		key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(key), URL: "http://north.test"}, "jo"); err != nil {
			t.Fatal(err)
		}
		// As a bearer, and as a linked yard names its key in a request of
		// its own: neither is a way in here.
		notLetIn(t, y.give(onTheMachine, yardkey.Public(key), unread{t}))
		notLetIn(t, y.give(onTheMachine, "", unread{t}, "Yardr-Peer-Key", yardkey.Public(key)))
		y.untouched(t, "a peer's key")
	})
	t.Run("the operator, said by something that is no device token", func(t *testing.T) {
		y := newSecretYard(t)
		// What a resolver other than the yard's own record of a device
		// answers: an identity source that vouches for a bearer.
		y.u.resolve = func(context.Context, string, string) (*Caller, error) {
			return &Caller{User: "jo", SaidBy: "kratos"}, nil
		}
		notThere(t, y.give(onTheMachine, "a-session-of-the-service", unread{t}))
		y.untouched(t, "a credential of an identity source")
	})
	t.Run("an operator made a member", func(t *testing.T) {
		y := newSecretYard(t)
		if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
			t.Fatal(err)
		}
		// A member's device: read as another user's is, for an ask of its
		// own.
		notThere(t, y.give(onTheMachine, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": "s3cret-0f-the-yard"}))))
		y.untouched(t, "an operator made a member")
	})
	t.Run("the array of yardr's own builds", func(t *testing.T) {
		y := newSecretYard(t)
		w := y.give(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`["yd-crm", %d, {"CRM_TOKEN":"s3cret-0f-the-yard"}, "jo"]`, y.ask)))
		if !strings.Contains(w.Body.String(), "takes them as a JSON object, by name") || strings.Contains(w.Body.String(), "s3cret") {
			t.Errorf("answered %s", w.Body)
		}
		y.untouched(t, "an array of arguments")
	})
}

// A secret is taken from a request that reached the listener from the
// yard's own machine, where TLS ends, and that no proxy says came in plain:
// what the listener can see of "https, or loopback". The refusal names the
// reason and says that what was sent may be given away; it is made before
// the body is read, and nothing is given.
func TestUserGiveSecretIsNotTakenInTheClear(t *testing.T) {
	for name, c := range map[string]struct {
		from   string
		header []string
		want   string
	}{
		"from another machine":             {elsewhere, nil, "in plain HTTP from another machine"},
		"from another machine, said https": {elsewhere, []string{"X-Forwarded-Proto", "https"}, "in plain HTTP from another machine"},
		"a proxy that says http":           {onTheMachine, []string{"X-Forwarded-Proto", "http"}, "the proxy in front of the yard says this request came to it in plain HTTP"},
		"two proxies, the first in plain":  {onTheMachine, []string{"X-Forwarded-Proto", "http, https"}, "the proxy in front of the yard says"},
	} {
		t.Run(name, func(t *testing.T) {
			y := newSecretYard(t)
			w := y.give(c.from, y.jo, unread{t}, c.header...)
			for _, want := range []string{c.want, "a secret is taken over https only, or from the yard's own machine", "make a new one at the service"} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("answered %s, want it to say %q", w.Body, want)
				}
			}
			y.untouched(t, name)
		})
	}
	// With no proxy at all, a client on the yard's machine: loopback.
	y := newSecretYard(t)
	if w := y.give(onTheMachine, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": "typed"}))); !strings.Contains(w.Body.String(), `"given":["CRM_TOKEN"]`) {
		t.Errorf("a giving from the yard's machine is answered %s", w.Body)
	}
	// Every other call is served as before, wherever it comes from.
	if w := y.give(elsewhere, y.jo, nil); !strings.Contains(w.Body.String(), "a secret is taken over https only") {
		t.Errorf("answered %s", w.Body)
	}
	if got := call(t, y.h, y.jo, "Head", `{}`); !strings.Contains(got, `"result"`) {
		t.Errorf("Head from another machine = %s", got)
	}
}

// The link's address, as the user server reads it, is the link's own:
// the two packages cannot import each other (the link's tests serve the
// user server), so a test holds them equal.
func TestLinkRemoteAddrMatchesTheLink(t *testing.T) {
	if linkRemoteAddr != edgelink.RemoteAddr {
		t.Fatalf("linkRemoteAddr = %q, edgelink.RemoteAddr = %q", linkRemoteAddr, edgelink.RemoteAddr)
	}
}

// hookDecl is a source with a hook, so that the signing secret of the hook
// linear is a secret ask.
const hookDecl = `name: linear
base: https://api.linear.app
credential: { env: LINEAR_API_KEY }
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery: { header: Linear-Delivery }
  bodies: unique
  normalize: [linear/normalize]
calls:
  viewer: { kind: read, request: { method: GET, path: /viewer } }
`

// Down the link from the edge a secret is refused with the link's own
// reason, whatever the ask: the edge saw what this one carried, and the fix
// is to answer on the yard's own machine. The one giving taken there is the
// daemon's ask for a harness login's code (TestUserGivesALoginInput), so the
// yard reads which ask a call names before it refuses, and says nothing of
// what it found. Nothing is given, and a code the edge says came in plain
// HTTP is refused as an approval is. This is the yard that is not hosted,
// which is every yard until its hosted setting says otherwise
// (TestUserGiveSecretIsNotTakenOverTheLink/hosted).
func TestUserGiveSecretIsNotTakenOverTheLink(t *testing.T) {
	const value = "s3cret-0f-the-yard"
	ctx := context.Background()
	y := newSecretYard(t)
	for file, decl := range map[string]string{"push.yaml": addressDecl, "linear.yaml": hookDecl} {
		if _, err := y.st.PutSource(ctx, file, decl, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	// ask puts an ask for sec on the bead, of to, and answers the body of a
	// giving of value for it under name.
	ask := func(to string, sec *store.AskSecret, name string) string {
		t.Helper()
		return y.askFor(t, to, sec, name, value)
	}
	userLogin := ask("sam", &store.AskSecret{Login: "claude-sam", As: "sam"}, "INPUT")
	operatorLogin := ask("jo", &store.AskSecret{Login: "claude-a1b2c3d4"}, "INPUT")
	changeLoginBody := func(change func(map[string]any)) string {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal([]byte(operatorLogin), &body); err != nil {
			t.Fatal(err)
		}
		change(body)
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	refused := []struct{ name, token, body string }{
		{"a source's credential", y.jo, y.args(map[string]string{"CRM_TOKEN": value})},
		{"a harness's API key", y.jo, ask("jo", &store.AskSecret{Harness: "claude", Crew: "yardmaster"}, "ANTHROPIC_API_KEY")},
		{"a hook's signing secret", y.jo, ask("jo", &store.AskSecret{Source: "linear", Hook: "linear"}, "LINEAR_SIGNING_SECRET")},
		// A user's own login code is the operator's to give for them and the
		// user's own: the exception is the operator's login alone.
		{"a user's login code, from the operator's device", y.jo, userLogin},
		{"a user's login code, from their device", y.sam, userLogin},
		{"a user's own address", y.sam, ask("sam", &store.AskSecret{Source: "push", User: "sam"}, "PUSH_USER")},
		// Arguments that name no ask of the daemon's are refused the same.
		{"an ask that asks for no secret", y.jo, ask("jo", nil, "INPUT")},
		{"an ask of no bead", y.jo, `{"bead":"yd-none","ask":1,"values":{"INPUT":"` + value + `"}}`},
		{"no ask of the bead", y.jo, `{"bead":"yd-crm","ask":999999,"values":{"INPUT":"` + value + `"}}`},
		{"no arguments", y.jo, `{}`},
		{"the array of yardr's own builds", y.jo, `["yd-crm", 1, {"INPUT":"` + value + `"}, "jo"]`},
		{"no body", y.jo, ``},
		{"a case-folded bead argument on a login ask", y.jo, strings.Replace(operatorLogin, `"bead":`, `"Bead":`, 1)},
		{"an unknown argument on a login ask", y.jo, changeLoginBody(func(body map[string]any) { body["extra"] = value })},
		{"an extra value name on a login ask", y.jo, changeLoginBody(func(body map[string]any) { body["values"].(map[string]any)["EXTRA_TOKEN"] = value })},
		{"a different actor on a login ask", y.jo, changeLoginBody(func(body map[string]any) { body["actor"] = value })},
		{"a non-boolean weight on a login ask", y.jo, changeLoginBody(func(body map[string]any) { body["weighed"] = value })},
		{"an unweighed login ask", y.jo, changeLoginBody(func(body map[string]any) { body["weighed"] = false })},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			w := y.give(edgelink.RemoteAddr, c.token, strings.NewReader(c.body))
			for _, want := range []string{
				"a secret never crosses the edge",
				"this request came down the link from the edge",
				"Answer on the yard's machine",
				"The edge saw what was sent with it",
				"make a new one at the service",
			} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("answered %s, want it to say %q", w.Body, want)
				}
			}
			for _, not := range []string{"in plain HTTP from another machine", "the yard's listener", "Put the proxy or tunnel", value} {
				if strings.Contains(w.Body.String(), not) {
					t.Errorf("answered %s, want none of %q", w.Body, not)
				}
			}
		})
	}
	// The daemon's own ask, from an edge that says the device used plain
	// HTTP: refused as an approval is, and the body names the ask.
	t.Run("the login's code, said plain by the edge", func(t *testing.T) {
		w := y.give(edgelink.RemoteAddr, y.jo, strings.NewReader(operatorLogin), "X-Forwarded-Proto", "http")
		for _, want := range []string{
			"a secret is taken over https only, or from the yard's own machine",
			"the proxy in front of the yard says this request came to it in plain HTTP",
		} {
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("answered %s, want it to say %q", w.Body, want)
			}
		}
		if strings.Contains(w.Body.String(), "a secret never crosses the edge") {
			t.Errorf("answered %s, want the plain-HTTP refusal and not the link's", w.Body)
		}
	})
	// None of it gave anything: no answer on the bead, and none of the
	// files a giving writes.
	if got := y.answers(t); len(got) != 0 {
		t.Errorf("the bead is answered %q after the refusals", got)
	}
	keyFile, err := store.HarnessKeyFile(filepath.Dir(y.db), "claude", "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{y.file, keyFile, store.HookSecretFile(filepath.Dir(y.db), "linear"), filepath.Join(filepath.Dir(y.db), "users", "sam.env")} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("a refused giving left %s (%v)", file, err)
		}
	}
	t.Run("hosted", testUserGiveSecretIsTakenDownTheLinkOfAHostedYard)
}

// On a hosted yard every secret that an operator's device gives is taken down
// the link from the edge, whatever the ask: a source's credential, a
// harness's API key, a hook's signing secret and a user's own address are
// written to their files as they are from the yard's own machine, and the
// answer on the bead says the giving came by the edge. (The login's code is
// taken on any yard: TestUserGivesALoginInput.) Only the operator's devices
// give: a member's device is refused its own ask with the link's words and
// the operator's as a method the yard does not have, and a giving the edge
// says came in plain HTTP is refused as an approval is. A yard whose hosted
// setting is false is the yard that is not hosted
// (TestUserGiveSecretIsNotTakenOverTheLink).
func testUserGiveSecretIsTakenDownTheLinkOfAHostedYard(t *testing.T) {
	const value = "s3cret-0f-the-hosted-yard"
	ctx := context.Background()
	hostedYard := func(t *testing.T) *secretYard {
		t.Helper()
		y := newSecretYard(t)
		for file, decl := range map[string]string{"push.yaml": addressDecl, "linear.yaml": hookDecl} {
			if _, err := y.st.PutSource(ctx, file, decl, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		if err := y.st.SetSetting(ctx, store.HostedSetting, "true", "jo"); err != nil {
			t.Fatal(err)
		}
		return y
	}
	for _, c := range []struct {
		name string
		to   string
		sec  *store.AskSecret
		env  string                   // the variable the ask asks for
		file func(home string) string // where its value is written
	}{
		{"a source's credential", "jo", &store.AskSecret{Source: "crm"}, "CRM_TOKEN",
			func(home string) string { return filepath.Join(home, "sources", "crm.env") }},
		{"a harness's API key", "jo", &store.AskSecret{Harness: "claude", Crew: "yardmaster"}, "ANTHROPIC_API_KEY",
			func(home string) string {
				file, err := store.HarnessKeyFile(home, "claude", "jo")
				if err != nil {
					t.Fatal(err)
				}
				return file
			}},
		{"a hook's signing secret", "jo", &store.AskSecret{Source: "linear", Hook: "linear"}, "LINEAR_SIGNING_SECRET",
			func(home string) string { return store.HookSecretFile(home, "linear") }},
		{"a user's own address, given for them", "sam", &store.AskSecret{Source: "push", User: "sam"}, "PUSH_USER",
			func(home string) string { return filepath.Join(home, "users", "sam.env") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := hostedYard(t)
			body := y.askFor(t, c.to, c.sec, c.env, value)
			w := y.give(edgelink.RemoteAddr, y.jo, strings.NewReader(body))
			if !strings.Contains(w.Body.String(), `"given":["`+c.env+`"]`) {
				t.Fatalf("answered %s", w.Body)
			}
			if b, err := os.ReadFile(c.file(filepath.Dir(y.db))); err != nil || !strings.Contains(string(b), value) {
				t.Errorf("the file holds %q (%v), want the value", b, err)
			}
			if got, want := y.answers(t), []string{"jo via phone via edge"}; !slices.Equal(got, want) {
				t.Errorf("the giving is answered on the bead by %q, want %q", got, want)
			}
		})
	}
	t.Run("a member's device", func(t *testing.T) {
		y := hostedYard(t)
		own := y.askFor(t, "sam", &store.AskSecret{Source: "push", User: "sam"}, "PUSH_USER", value)
		var refused struct {
			Error *wireError `json:"error"`
		}
		w := y.give(edgelink.RemoteAddr, y.sam, strings.NewReader(own))
		if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || refused.Error == nil {
			t.Fatalf("sam's own ask is answered %s (%v), want a refusal", w.Body, err)
		}
		const hostedMember = "a secret never crosses the edge from a member's device: this request came down the link from the edge. " +
			"Ask the operator to give it from their paired device." +
			" The edge saw what was sent with it: if it was a real key, make a new one at the service."
		if refused.Error.Message != hostedMember {
			t.Errorf("sam's own ask is told %q, want %q", refused.Error.Message, hostedMember)
		}
		if strings.Contains(w.Body.String(), value) {
			t.Errorf("sam's own ask is answered %s, with the value", w.Body)
		}
		// On a yard that is not hosted the same ask is refused with the
		// yard's-machine sentence, word for word.
		if err := y.st.SetSetting(ctx, store.HostedSetting, "false", "jo"); err != nil {
			t.Fatal(err)
		}
		w = y.give(edgelink.RemoteAddr, y.sam, strings.NewReader(own))
		refused.Error = nil
		if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || refused.Error == nil {
			t.Fatalf("sam's own ask, unhosted, is answered %s (%v), want a refusal", w.Body, err)
		}
		const connectedMember = "a secret never crosses the edge: this request came down the link from the edge. " +
			"Answer on the yard's machine (ssh -t <host> yardr bead answer <bead> <ask> --secret)." +
			" The edge saw what was sent with it: if it was a real key, make a new one at the service."
		if refused.Error.Message != connectedMember {
			t.Errorf("sam's own ask, unhosted, is told %q, want %q", refused.Error.Message, connectedMember)
		}
		none, _ := json.Marshal(noSuchMethod("GiveSecret"))
		w = y.give(edgelink.RemoteAddr, y.sam, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})))
		if strings.TrimSpace(w.Body.String()) != string(none) {
			t.Errorf("the operator's ask from sam's device is answered %s, want no such method", w.Body)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(y.db), "users", "sam.env")); !os.IsNotExist(err) {
			t.Errorf("a refused giving left a file of sam's (%v)", err)
		}
		y.untouched(t, "a member's device")
	})
	t.Run("said plain by the edge", func(t *testing.T) {
		y := hostedYard(t)
		w := y.give(edgelink.RemoteAddr, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})), "X-Forwarded-Proto", "http")
		for _, want := range []string{
			"a secret is taken over https only, or from the yard's own machine",
			"the proxy in front of the yard says this request came to it in plain HTTP",
		} {
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("answered %s, want it to say %q", w.Body, want)
			}
		}
		if strings.Contains(w.Body.String(), "a secret never crosses the edge") || strings.Contains(w.Body.String(), value) {
			t.Errorf("answered %s, want the plain-HTTP refusal and not the link's, without the value", w.Body)
		}
		y.untouched(t, "a request said plain")
	})
	t.Run("hosted set to false", func(t *testing.T) {
		y := hostedYard(t)
		if err := y.st.SetSetting(ctx, store.HostedSetting, "false", "jo"); err != nil {
			t.Fatal(err)
		}
		w := y.give(edgelink.RemoteAddr, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})))
		if !strings.Contains(w.Body.String(), "a secret never crosses the edge") {
			t.Errorf("answered %s, want the link's refusal", w.Body)
		}
		y.untouched(t, "hosted set to false")
	})
}

// A hosted yard takes a secret from its router: the peer the router_addr
// setting names, saying the request came to it over https. The value lands
// in the file, for the operator's ask and for a user's own address. From
// that peer without the header, from anywhere else, and with the setting
// unset, it is refused with today's sentence, before the body is read, and
// nothing is given.
func TestUserGivesASecretThroughTheRouter(t *testing.T) {
	const router = "172.18.0.2"
	ctx := context.Background()
	routed := func(t *testing.T) *secretYard {
		t.Helper()
		y := newSecretYard(t)
		if err := y.st.SetSetting(ctx, store.RouterAddrSetting, router, "jo"); err != nil {
			t.Fatal(err)
		}
		return y
	}
	t.Run("the operator's ask, from the router said https", func(t *testing.T) {
		const value = "s3cret-fr0m-the-edge"
		y := routed(t)
		w := y.give(router+":40000", y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})), "X-Forwarded-Proto", "https")
		if !strings.Contains(w.Body.String(), `"given":["CRM_TOKEN"]`) {
			t.Fatalf("answered %s", w.Body)
		}
		if b, _ := os.ReadFile(y.file); string(b) != "CRM_TOKEN="+value+"\n" {
			t.Errorf("the env file holds %q", b)
		}
	})
	t.Run("a user's own address, from the router said https", func(t *testing.T) {
		const value = "u-0f-sam-fr0m-the-edge"
		y := routed(t)
		if _, err := y.st.PutSource(ctx, "push.yaml", addressDecl, "jo"); err != nil {
			t.Fatal(err)
		}
		seq, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Your id at push", To: "sam",
			Context: []string{"It is on the first page after login."}, Secret: &store.AskSecret{Source: "push", User: "sam"}}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		args := fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_USER":%q},"weighed":true}`, seq, value)
		w := y.give(router+":40000", y.sam, strings.NewReader(args), "X-Forwarded-Proto", "https")
		if !strings.Contains(w.Body.String(), `"given":["PUSH_USER"]`) {
			t.Fatalf("answered %s", w.Body)
		}
		file := filepath.Join(filepath.Dir(y.db), "users", "sam.env")
		if b, _ := os.ReadFile(file); string(b) != "PUSH_USER="+value+"\n" {
			t.Errorf("sam's file holds %q", b)
		}
	})
	for name, c := range map[string]struct {
		from   string
		header []string
	}{
		"the router, no header":          {router + ":40000", nil},
		"the router, said http":          {router + ":40000", []string{"X-Forwarded-Proto", "http"}},
		"the router, one proxy in plain": {router + ":40000", []string{"X-Forwarded-Proto", "https, http"}},
		"another peer, said https":       {elsewhere, []string{"X-Forwarded-Proto", "https"}},
		"another peer, no header":        {elsewhere, nil},
	} {
		t.Run(name, func(t *testing.T) {
			y := routed(t)
			w := y.give(c.from, y.jo, unread{t}, c.header...)
			for _, want := range []string{"in plain HTTP from another machine", "a secret is taken over https only, or from the yard's own machine", "make a new one at the service"} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("answered %s, want it to say %q", w.Body, want)
				}
			}
			y.untouched(t, name)
		})
	}
	// Unset, the router's peer is another machine.
	y := newSecretYard(t)
	if w := y.give(router+":40000", y.jo, unread{t}, "X-Forwarded-Proto", "https"); !strings.Contains(w.Body.String(), "in plain HTTP from another machine") {
		t.Errorf("answered %s", w.Body)
	}
	y.untouched(t, "an unset router_addr")
}

// A value given over the listener is in the source's env file and in
// nothing else: not the store's file, not an event, a note or an answer of
// any call, not a refusal, not the listener's log, and not the stream
// another device reads. It is the test of the socket's two calls
// (TestAGivenValueIsInTheFileAlone), run through the listener.
func TestAValueGivenOverTheListenerIsInTheFileAlone(t *testing.T) {
	const value = "s3cret-0f-the-yard"
	y := newSecretYard(t)
	ctx := context.Background()
	base := listen(t, y.u, 0)
	lines, resp := follow(t, base, y.sam, 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sam's stream: %s", resp.Status)
	}

	var said []string
	for _, body := range []string{
		y.args(map[string]string{"CRM_TOKEN": value + "\nMORE=1"}),
		y.args(map[string]string{"OTHER": value}),
		y.args(map[string]string{value: "x"}),
		// The value where something else goes: as the values, as the actor,
		// and as the name of an argument.
		y.args(value),
		fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"x"},"actor":%q}`, y.ask, value),
		fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"x"},%q:true}`, y.ask, value),
	} {
		w := y.give(onTheMachine, y.jo, strings.NewReader(body))
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s was taken: %s", body, w.Body)
		}
		said = append(said, w.Body.String())
	}
	// As a client that does not know the field would send it.
	for method, body := range map[string]string{
		"AnswerAsk":  fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"words":%q}`, y.ask, value),
		"DeclineAsk": fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"words":%q}`, y.ask, value),
	} {
		got := call(t, y.h, y.jo, method, body)
		if !strings.Contains(got, `"error"`) {
			t.Fatalf("%s %s was taken: %s", method, body, got)
		}
		said = append(said, got)
	}
	// Refused where the value has crossed already: the refusal has none of it.
	said = append(said, y.give(elsewhere, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value}))).Body.String())
	said = append(said, y.give(onTheMachine, y.sam, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value}))).Body.String())
	y.untouched(t, "the refused givings")

	given := y.give(onTheMachine, y.jo, strings.NewReader(y.args(map[string]string{"CRM_TOKEN": value})))
	if !strings.Contains(given.Body.String(), `"given":["CRM_TOKEN"]`) {
		t.Fatalf("the giving is answered %s", given.Body)
	}
	if b, _ := os.ReadFile(y.file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Fatalf("the env file holds %q", b)
	}

	// What sam's device read meanwhile, through the event of the giving.
	var stream []string
	for {
		line := next(t, lines)
		if line == "" {
			t.Fatal("sam's stream ended before the giving")
		}
		stream = append(stream, line)
		if strings.Contains(line, `"source_set"`) {
			break
		}
	}
	kept := []string{given.Body.String()}
	for _, token := range []string{y.jo, y.sam} {
		kept = append(kept, call(t, y.h, token, "Bead", `{"bead":"yd-crm"}`), call(t, y.h, token, "UserAsks", `{}`))
	}
	evs, err := y.st.EventsSince(ctx, 0, 1000)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	events, _ := json.Marshal(evs)
	y.u.Flush(true)
	y.closed = true
	if err := y.st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(y.db)
	if err != nil {
		t.Fatal(err)
	}
	for where, held := range map[string][]byte{
		"the refusals":            []byte(strings.Join(said, "\n")),
		"what the calls answer":   []byte(strings.Join(kept, "\n")),
		"the events":              events,
		"another device's stream": []byte(strings.Join(stream, "\n")),
		"the store's file":        db,
		"the listener's log":      y.log.Bytes(),
	} {
		if bytes.Contains(held, []byte(value)) {
			t.Errorf("%s holds the value", where)
		}
	}
	// Nothing beside the env file either: no second file, no copy.
	if left, err := os.ReadDir(filepath.Dir(y.file)); err != nil || len(left) != 1 {
		t.Fatalf("beside the env file: %v, %v", left, err)
	}
	if all := strings.Join(said, "\n"); !strings.Contains(all, "has a line break in it") || !strings.Contains(all, "was given a name it did not ask for") {
		t.Fatalf("the refusals: %q", said)
	}
}

// A giving to a file the yard cannot write fails on the yard's own file:
// a device that is no operator's is told that it failed and not on which
// file, and the full words are logged for the operator. A device of the
// operator is told the error, which names the file. A value the file
// cannot hold is still refused whole, before the file is looked at, even
// where the file is broken.
func TestUserGiveSecretHeldWhenTheWriteFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breakFile func(t *testing.T, file string)
	}{
		{"a symbolic link", func(t *testing.T, file string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			elsewhere := filepath.Join(t.TempDir(), "elsewhere.env")
			if err := os.WriteFile(elsewhere, []byte("PUSH_USER=old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, file); err != nil {
				t.Fatal(err)
			}
		}},
		{"open to others", func(t *testing.T, file string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("PUSH_USER=old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, 0o640); err != nil { // past the umask
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := newSecretYard(t)
			ctx := context.Background()
			if _, err := y.st.PutSource(ctx, "push.yaml", addressDecl, "jo"); err != nil {
				t.Fatal(err)
			}
			seq, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Your id at push", To: "sam",
				Context: []string{"It is on the first page after login."}, Secret: &store.AskSecret{Source: "push", User: "sam"}}, "yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(filepath.Dir(y.db), "users", "sam.env")
			tc.breakFile(t, file)
			args := func(value string) string {
				return fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"values":{"PUSH_USER":%q},"weighed":true}`, seq, value)
			}

			var held struct {
				Error *wireError `json:"error"`
			}
			w := y.give(onTheMachine, y.sam, strings.NewReader(args("u-0f-sam-s3cret")))
			if err := json.Unmarshal(w.Body.Bytes(), &held); err != nil || held.Error == nil {
				t.Fatalf("sam's device is answered %s (%v), want an error", w.Body, err)
			}
			if held.Error.Message != callHeld {
				t.Errorf("sam's device is told %q, want %q", held.Error.Message, callHeld)
			}
			if strings.Contains(w.Body.String(), file) || strings.Contains(w.Body.String(), "env_file") {
				t.Errorf("sam's device is told the file: %s", w.Body)
			}
			w = y.give(onTheMachine, y.jo, strings.NewReader(args("typed-by-jo")))
			if !strings.Contains(w.Body.String(), file) {
				t.Errorf("the operator's device is answered %s, want the error that names the file", w.Body)
			}
			// A refusal reads whole, before the file is looked at.
			w = y.give(onTheMachine, y.sam, strings.NewReader(args("")))
			var refused struct {
				Error *wireError `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || refused.Error == nil {
				t.Fatalf("sam's device sending nothing is answered %s (%v), want a refusal", w.Body, err)
			}
			if refused.Error.Message != "the value for PUSH_USER is empty" {
				t.Errorf("sam's device sending nothing is told %q, want the refusal whole", refused.Error.Message)
			}
			if !strings.Contains(y.log.String(), "a device's request failed on the yard's file") ||
				!strings.Contains(y.log.String(), file) {
				t.Errorf("the listener's log does not hold the full words:\n%s", y.log.String())
			}
			if q, err := y.st.UserAsks(ctx, "sam", "sam"); err != nil || len(q.Open) != 1 {
				t.Errorf("sam's open asks after the failed givings: %+v, %v", q, err)
			}
		})
	}
}
