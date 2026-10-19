package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// userYard is a yard with the operator jo, a second user sam, the crew
// member yardmaster and jo's device phone: its store, the handler of its
// listener, and the device's token.
func userYard(t *testing.T) (*store.Store, *UserServer, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
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
	_, err = st.CreateBead(ctx, store.NewBead{Title: "Paint the shed", ID: "yd-shed"}, "yardmaster")
	must(err)
	d, err := st.AddDevice(ctx, "jo", "phone", "jo")
	must(err)
	return st, NewUserServer(st, func(context.Context) string { return "south" }, nil), d.Token
}

// unread fails the test when the handler reads the request's body.
type unread struct{ t *testing.T }

func (b unread) Read([]byte) (int, error) {
	b.t.Error("the body of a refused request was read")
	return 0, io.EOF
}

// ask makes one request of the handler as it comes from the address
// 203.0.113.9, with the credential when there is one.
func ask(h http.Handler, method, path, credential string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = "203.0.113.9:40000"
	if credential != "" {
		r.Header.Set("Authorization", "Bearer "+credential)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// call makes the call as the device and answers the reply's body.
func call(t *testing.T, h http.Handler, token, method, body string) string {
	t.Helper()
	w := ask(h, http.MethodPost, "/user/call/"+method, token, strings.NewReader(body))
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s: status %d: %s", method, body, w.Code, w.Body)
	}
	return strings.TrimSpace(w.Body.String())
}

func eventsOfKind(t *testing.T, st *store.Store, kind string) []store.Event {
	t.Helper()
	evs, err := st.EventsOfKind(context.Background(), kind, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// The listener serves a device exactly the calls of Calls, by name: every
// other method of the store's API is answered as no method at all, whatever
// the form of its arguments, and the array form is no way to make a call
// that is served. What the socket serves besides (wake, ping) is not there.
//
// Two calls more are served under a condition, GiveSecret and ApproveAsk to
// a device of the yard's operator: to sam's device each is one of the
// methods that are not there, and to jo's they are there, and are the whole
// of OperatorCalls.
func TestUserServesTheCallsAndNoOther(t *testing.T) {
	st, u, token := userYard(t)
	h := u.Handler()
	tablet, err := st.AddDevice(context.Background(), "sam", "tablet", "jo")
	if err != nil {
		t.Fatal(err)
	}
	api := reflect.TypeOf((*store.API)(nil)).Elem()
	var served, operators []string
	for i := 0; i < api.NumMethod(); i++ {
		name := api.Method(i).Name
		none, _ := json.Marshal(noSuchMethod(name))
		byName, byPlace := call(t, h, tablet.Token, name, `{}`), call(t, h, tablet.Token, name, `[]`)
		if _, ok := Calls[name]; !ok {
			if asOperator := call(t, h, token, name, `{}`); asOperator != string(none) {
				operators = append(operators, name)
			}
			if byName != string(none) || byPlace != string(none) {
				t.Errorf("%s is no call of the doc and is answered %s and %s, want %s", name, byName, byPlace, none)
			}
			continue
		}
		served = append(served, name)
		if byName == string(none) {
			t.Errorf("%s is a call of the doc and is answered %s", name, byName)
		}
		if want := "takes them as a JSON object, by name"; !strings.Contains(byPlace, want) {
			t.Errorf("%s with an array of arguments is answered %s, want a refusal saying %q", name, byPlace, want)
		}
	}
	if len(served) != len(Calls) || len(served) != 25 {
		t.Errorf("%d calls are served and Calls has %d, want 25 of each: %v", len(served), len(Calls), served)
	}
	if !slices.Equal(operators, []string{"ApproveAsk", "ApproveSetup", "GiveSecret"}) || len(OperatorCalls) != 3 {
		t.Errorf("a device of the operator is served %v besides, and OperatorCalls is %v: want ApproveAsk, ApproveSetup, GiveSecret and no other", operators, OperatorCalls)
	}
	for _, name := range []string{"Sing", "Subscribe", "Close"} {
		none, _ := json.Marshal(noSuchMethod(name))
		if got := call(t, h, token, name, `{}`); got != string(none) {
			t.Errorf("%s is answered %s, want %s", name, got, none)
		}
	}
	for _, path := range []string{"/user/wake", "/user/ping", "/user/", "/user/call/Head/more"} {
		if w := ask(h, http.MethodGet, path, token, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s as the device = %d, want 404", path, w.Code)
		}
	}
	if w := ask(h, http.MethodGet, "/user/call/Head", token, nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /user/call/Head as the device = %d, want 405", w.Code)
	}
}

// Every argument of a call that names a party is one the handler holds to
// the caller: a call added to Calls with a `name` or an `actor` must be in
// selfArg, or say here why that argument is nobody's identity.
func TestUserCallsNameTheirParty(t *testing.T) {
	notAParty := map[string]string{"GetCrew": "name"} // a crew member that is read, not the reader
	api := reflect.TypeOf((*store.API)(nil)).Elem()
	// Of both lists: what a device of the operator is served besides Calls
	// is made as its user too.
	calls := maps.Clone(Calls)
	maps.Copy(calls, OperatorCalls)
	for method, names := range calls {
		for i, arg := range names {
			if arg != "name" && arg != "actor" || notAParty[method] == arg {
				continue
			}
			if selfArg[method] != arg {
				t.Errorf("%s takes %q and selfArg does not hold it to the caller", method, arg)
			}
			if m, _ := api.MethodByName(method); m.Type.In(i+1).Kind() != reflect.String {
				t.Errorf("%s: %q is a %s, and asUser reads it as a string", method, arg, m.Type.In(i+1))
			}
		}
	}
	for method, arg := range selfArg {
		if !slices.Contains(calls[method], arg) {
			t.Errorf("selfArg names %q of %s, which Calls does not have", arg, method)
		}
	}
	if !slices.Equal(Calls["Conversation"], []string{"a", "b", "after", "limit", "before"}) {
		t.Errorf("Conversation takes %v: asUser holds one of a and b to the caller", Calls["Conversation"])
	}
}

// Every call is made as the device's user: a name or actor left out is
// filled in, one that names anyone else is refused and nothing is written.
func TestUserCallsAreMadeAsTheUser(t *testing.T) {
	st, u, token := userYard(t)
	ctx := context.Background()
	h := u.Handler()
	if err := st.Mail(ctx, "jo", "for jo", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "sam", "for sam", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, body, want string }{
		// Left out, null and empty are the user.
		{"UserInbox", `{}`, `"text":"for jo"`},
		{"UserInbox", `{"name":null,"limit":1}`, `"text":"for jo"`},
		{"UserInbox", `{"name":""}`, `"text":"for jo"`},
		{"UserInbox", `{"name":"jo"}`, `"text":"for jo"`},
		{"GetUser", `{}`, `"name":"jo"`},
		{"UserAsks", `{}`, `"open":`},
		{"UserNotifications", `{}`, `"notifications":`},
		{"Conversation", `{"b":"yardmaster"}`, `"text":"for jo"`},
		{"Conversation", `{"a":"yardmaster","b":"jo"}`, `"text":"for jo"`},
		// Anyone else is refused: a user, a crew member, a name of no one.
		{"UserInbox", `{"name":"sam"}`, `this device calls as jo: \"name\" is jo or left out, not sam`},
		{"GetUser", `{"name":"sam"}`, `this device calls as jo`},
		{"UserAsks", `{"name":"sam"}`, `this device calls as jo`},
		{"UserNotifications", `{"name":"sam"}`, `this device calls as jo`},
		{"AckUserInbox", fmt.Sprintf(`{"name":"sam","through":%d}`, head), `this device calls as jo`},
		{"AckUserNotifications", fmt.Sprintf(`{"name":"sam","through":%d}`, head), `this device calls as jo`},
		{"Mail", `{"to":"sam","text":"as the yardmaster","actor":"yardmaster"}`, `this device calls as jo: \"actor\" is jo or left out, not yardmaster`},
		{"MailPeer", `{"to":"clerk","peer":"north","text":"hello","actor":"sam"}`, `this device calls as jo`},
		{"AddNote", `{"bead":"yd-shed","text":"as sam","actor":"sam"}`, `this device calls as jo`},
		{"AnswerAsk", `{"bead":"yd-shed","ask":1,"option":1,"actor":"sam"}`, `this device calls as jo`},
		{"DeferAsk", `{"bead":"yd-shed","ask":1,"actor":"sam"}`, `this device calls as jo`},
		{"DeclineAsk", `{"bead":"yd-shed","ask":1,"words":"no","actor":"sam"}`, `this device calls as jo`},
		{"Conversation", `{"a":"sam","b":"yardmaster"}`, `this device calls as jo: one of \"a\" and \"b\" is jo`},
		// Another type is refused as on the socket, and is no way around.
		{"UserInbox", `{"name":["sam"]}`, `argument \"name\": json: cannot unmarshal`},
		{"Mail", `{"to":"sam","text":"x","actor":{"n":"sam"}}`, `argument \"actor\": json: cannot unmarshal`},
		{"Conversation", `{"a":7,"b":"sam"}`, `argument \"a\": json: cannot unmarshal`},
		// A name of another case is another argument, which no call has.
		{"Mail", `{"to":"sam","text":"x","Actor":"sam"}`, `argument \"Actor\": Mail has none of that name`},
	} {
		if got := call(t, h, token, c.method, c.body); !strings.Contains(got, c.want) {
			t.Errorf("%s %s = %s, want %s", c.method, c.body, got, c.want)
		}
	}
	if now, _ := st.Head(ctx); now != head {
		t.Errorf("the refused calls wrote %d events", now-head)
	}
	if box, _ := st.UserInbox(ctx, "sam", 0); box.Unread != 1 {
		t.Errorf("sam has %d unread, want the 1 a device of jo could not acknowledge", box.Unread)
	}

	// The writes, with the actor left out: they are jo's.
	if got := call(t, h, token, "Mail", `{"to":"yardmaster","text":"land it"}`); got != `{}` {
		t.Fatalf("Mail without an actor = %s", got)
	}
	if got := call(t, h, token, "AddNote", `{"bead":"yd-shed","text":"looks right"}`); got != `{}` {
		t.Fatalf("AddNote without an actor = %s", got)
	}
	if got := call(t, h, token, "AckUserInbox", fmt.Sprintf(`{"through":%d}`, head)); got != `{}` {
		t.Fatalf("AckUserInbox without a name = %s", got)
	}
	mail := eventsOfKind(t, st, "mail")
	if last := mail[len(mail)-1]; last.Actor != "jo" || last.Data["to"] != "yardmaster" {
		t.Errorf("the mail is %+v, want jo's to the yardmaster", last)
	}
	if box, _ := st.UserInbox(ctx, "jo", 0); box.Unread != 0 {
		t.Errorf("jo has %d unread after the device acknowledged", box.Unread)
	}
}

// A write made through the handler says how it came: its event carries the
// device as `via`, and so do the letter and the note that are read back.
// What is written on the yard's own machine carries none.
func TestUserWritesCarryVia(t *testing.T) {
	st, u, token := userYard(t)
	ctx := context.Background()
	h := u.Handler()
	if err := st.Mail(ctx, "jo", "for jo", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)
	call(t, h, token, "Mail", `{"to":"yardmaster","text":"land it","actor":"jo"}`)
	call(t, h, token, "AddNote", `{"bead":"yd-shed","text":"looks right","actor":"jo"}`)
	call(t, h, token, "AckUserInbox", fmt.Sprintf(`{"name":"jo","through":%d}`, head))
	if err := st.Mail(ctx, "yardmaster", "from the terminal", "jo"); err != nil {
		t.Fatal(err)
	}
	evs, err := st.EventsSince(ctx, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+" via "+store.Via(e))
	}
	if want := []string{"mail via phone", "noted via phone", "mail_read via phone", "mail via "}; !slices.Equal(got, want) {
		t.Errorf("the events are %q, want %q", got, want)
	}
	if from := store.MailFrom(evs[0]); from != "jo (via phone)" {
		t.Errorf("an inbox names the sender %q, want jo (via phone)", from)
	}
	if conv := call(t, h, token, "Conversation", `{"a":"jo","b":"yardmaster"}`); strings.Count(conv, `"via":"phone"`) != 1 {
		t.Errorf("the conversation marks other than the one letter of the device: %s", conv)
	}
	if bead := call(t, h, token, "Bead", `{"bead":"yd-shed"}`); !strings.Contains(bead, `"text":"looks right"`) || !strings.Contains(bead, `"via":"phone"`) {
		t.Errorf("the bead's note does not say how it came: %s", bead)
	}
	notes, err := st.Notes(ctx, "yd-shed")
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(store.NoteBlocks(notes, time.Now(), ""), "\n"); !strings.Contains(text, "jo (via phone)]\n  looks right") {
		t.Errorf("the notes as they are printed do not say how one came:\n%s", text)
	}
}

// /user/me says who the credential is: the user, what says so, the yard.
func TestUserMe(t *testing.T) {
	_, u, token := userYard(t)
	w := ask(u.Handler(), http.MethodGet, "/user/me", token, nil)
	if got := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK || got != `{"user":"jo","said_by":"phone","yard":"south"}` {
		t.Errorf("GET /user/me = %d %s", w.Code, got)
	}
}

// userPaths are requests of every kind the handler serves, and of none.
var userPaths = [][2]string{
	{http.MethodGet, "/user/me"},
	{http.MethodPost, "/user/call/Head"},
	{http.MethodPost, "/user/call/Mail"},
	{http.MethodPost, "/user/call/AddUser"},
	{http.MethodGet, "/user/events?after=0"},
	{http.MethodGet, "/user/blobs/0000"},
	{http.MethodGet, "/user/"},
	{http.MethodGet, "/user/nothing/here"},
	{http.MethodDelete, "/user/me"},
}

// Without a credential, with a wrong one and with one of a device since
// removed every path is 401, the same answer each time, given before the
// body is read. A yard with no device has no such paths: 404.
func TestUserRefusedWithoutACredential(t *testing.T) {
	st, u, token := userYard(t)
	ctx := context.Background()
	h := u.Handler()
	other, err := st.AddDevice(ctx, "sam", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RemoveDevice(ctx, "sam", "laptop", "jo"); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, send func(r *http.Request)) {
		t.Helper()
		for _, p := range userPaths {
			r := httptest.NewRequest(p[0], p[1], unread{t})
			send(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || w.Body.String() != "Unauthorized\n" || w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s: %s %s = %d %q, want 401 Unauthorized", what, p[0], p[1], w.Code, w.Body)
			}
		}
	}
	refused("no credential", func(*http.Request) {})
	refused("a wrong token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+store.DeviceTokenPrefix+"wrong") })
	refused("a removed device", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+other.Token) })
	refused("no device token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer some-session") })
	refused("another scheme", func(r *http.Request) { r.Header.Set("Authorization", "Basic "+token) })
	refused("the token in the URL's place", func(r *http.Request) { r.Header.Set("Authorization", token) })
	refused("two credentials", func(r *http.Request) {
		r.Header.Add("Authorization", "Bearer "+token)
		r.Header.Add("Authorization", "Bearer "+token)
	})
	refused("a header a proxy would set", func(r *http.Request) {
		r.Header.Set("X-User", "jo")
		r.Header.Set("X-Forwarded-User", "jo")
	})
	want := map[string]int{refusedNone: 5 * len(userPaths), refusedUnknown: 3 * len(userPaths)}
	if got := u.Rejected(); !reflect.DeepEqual(got, want) {
		t.Errorf("the refusals are counted %v, want %v", got, want)
	}
	if w := ask(h, http.MethodGet, "/user/me", token, nil); w.Code != http.StatusOK {
		t.Errorf("the device is answered %d after them", w.Code)
	}

	// The last device goes, and with it the paths.
	if _, err := st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, p := range userPaths {
		for _, credential := range []string{"", token} {
			r := httptest.NewRequest(p[0], p[1], unread{t})
			if credential != "" {
				r.Header.Set("Authorization", "Bearer "+credential)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Errorf("with no device in the yard %s %s = %d, want 404", p[0], p[1], w.Code)
			}
		}
	}
	if got := u.Rejected(); !reflect.DeepEqual(got, want) {
		t.Errorf("a yard with no device counted refusals: %v", got)
	}
}

// A thousand wrong credentials from one address cost a device at that same
// address nothing: it is answered as before, no request is answered 429,
// and the handler keeps nothing for the address or for the credentials: a
// count for each reason, said in one event when the first is refused and
// one more for the rest.
func TestUserRefusalsKeepNothingForTheAddress(t *testing.T) {
	st, u, token := userYard(t)
	h := u.Handler()
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	u.now = func() time.Time { return at }
	before := ask(h, http.MethodGet, "/user/me", token, nil)
	for i := 0; i < 1000; i++ {
		credential := fmt.Sprintf("%swrong-%d", store.DeviceTokenPrefix, i)
		if i%2 == 1 {
			credential = fmt.Sprintf("session-%d", i)
		}
		if w := ask(h, http.MethodPost, "/user/call/Head", credential, unread{t}); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong credential %d = %d, want 401", i, w.Code)
		}
		at = at.Add(time.Millisecond)
	}
	after := ask(h, http.MethodGet, "/user/me", token, nil)
	if after.Code != http.StatusOK || after.Body.String() != before.Body.String() {
		t.Errorf("the device at the address of the wrong credentials = %d %s, before them %d %s", after.Code, after.Body, before.Code, before.Body)
	}
	if got := call(t, h, token, "Head", `{}`); !strings.Contains(got, `"result"`) {
		t.Errorf("the device's call after them = %s", got)
	}
	if got := u.Rejected(); !reflect.DeepEqual(got, map[string]int{refusedUnknown: 1000}) {
		t.Errorf("the refusals are counted %v, want 1000 for one reason", got)
	}
	if len(u.refused) != 1 || len(u.streams) != 0 {
		t.Errorf("the handler keeps %d counts and %d streams, want one count for the one reason", len(u.refused), len(u.streams))
	}
	// One event at once, and the rest in a sum when the window has ended.
	u.Flush(false)
	if evs := eventsOfKind(t, st, UserRefused); len(evs) != 1 || evs[0].Data["count"] != float64(1) || evs[0].Data["reason"] != refusedUnknown {
		t.Fatalf("within the window the events are %+v, want one of the first refusal", evs)
	}
	at = at.Add(RefuseWindow)
	u.Flush(false)
	evs := eventsOfKind(t, st, UserRefused)
	if len(evs) != 2 || evs[1].Data["count"] != float64(999) || evs[1].Data["status"] != float64(401) {
		t.Fatalf("after the window the events are %+v, want a second of 999", evs)
	}
	for _, e := range evs {
		if len(e.Data) != 4 || strings.Contains(fmt.Sprint(e.Data), "203.0.113.9") || strings.Contains(fmt.Sprint(e.Data), "wrong") {
			t.Errorf("the event carries more than reason, status, count and last: %v", e.Data)
		}
	}
	// A device's last use is its own, not the strangers'.
	devices, err := st.Devices(context.Background(), "jo")
	if err != nil || len(devices) != 1 || devices[0].LastFrom != "203.0.113.9" {
		t.Errorf("the device's last use = %+v, %v", devices, err)
	}
}

// A resolver that does not return is given up: the request is refused when
// ResolveTimeout has passed, and the handler goes on answering.
func TestUserResolverThatDoesNotReturn(t *testing.T) {
	_, u, token := userYard(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	device := u.resolve
	u.resolveTimeout = 30 * time.Millisecond
	u.resolve = func(ctx context.Context, credential, from string) (*Caller, error) {
		if credential == "slow" {
			<-hang
		}
		return device(ctx, credential, from)
	}
	h := u.Handler()
	start := time.Now()
	w := ask(h, http.MethodGet, "/user/me", "slow", unread{t})
	if w.Code != http.StatusUnauthorized || time.Since(start) > 5*time.Second {
		t.Errorf("a credential whose resolver hangs = %d after %s, want 401 when the limit passed", w.Code, time.Since(start))
	}
	if got := u.Rejected(); got[refusedTimeout] != 1 {
		t.Errorf("the refusals are counted %v, want one %s", got, refusedTimeout)
	}
	// The short limit served the hanging credential; the device beside it
	// is answered under the limit the yard runs with. Under load the
	// store's lookup can outlast the short one, and the device is refused
	// for being slow, not for being unknown.
	u.resolveTimeout = ResolveTimeout
	if w := ask(h, http.MethodGet, "/user/me", token, nil); w.Code != http.StatusOK {
		t.Errorf("the device beside it = %d", w.Code)
	}
}

// listen serves the handler as the yard's listener does, with a bound on
// how long a request is read for, and answers its address.
func listen(t *testing.T, u *UserServer, readTimeout time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(UserPath, u.Handler())
	srv := &http.Server{Handler: mux, ReadTimeout: readTimeout, WriteTimeout: readTimeout}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return "http://" + ln.Addr().String()
}

// follow opens the device's stream after seq and answers its lines; the
// channel closes when the stream ends.
func follow(t *testing.T, base, token string, after uint64) (<-chan string, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/user/events?after=%d", base, after), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	lines := make(chan string, 64)
	if resp.StatusCode != http.StatusOK {
		close(lines)
		return lines, resp
	}
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines, resp
}

// next is the stream's next line, "" when it ended, and fails the test when
// neither comes.
func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("the stream neither sent a line nor ended")
		return ""
	}
}

// Removing a device closes the streams it holds, at once and before the
// event that says so is sent; another device's stream goes on. A stream
// outlives the listener's bound on reading a request.
func TestUserStreamEndsWithItsDevice(t *testing.T) {
	st, u, token := userYard(t)
	ctx := context.Background()
	sam, err := st.AddDevice(ctx, "sam", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	base := listen(t, u, 200*time.Millisecond)
	head, _ := st.Head(ctx)
	jos, _ := follow(t, base, token, head)
	sams, _ := follow(t, base, sam.Token, head)

	// Quiet for longer than a request may be read for, and still there.
	time.Sleep(500 * time.Millisecond)
	if err := st.Mail(ctx, "jo", "still there?", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	for _, lines := range []<-chan string{jos, sams} {
		if line := next(t, lines); !strings.Contains(line, "still there?") {
			t.Fatalf("the stream after a quiet half second sent %q, want the mail", line)
		}
	}
	if _, err := st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	if line := next(t, jos); line != "" {
		t.Errorf("the removed device's stream sent %q, want it closed", line)
	}
	if line := next(t, sams); !strings.Contains(line, `"kind":"device_removed"`) {
		t.Errorf("the other device's stream sent %q, want the removal", line)
	}
	u.mu.Lock()
	open := len(u.streams)
	u.mu.Unlock()
	if open != 1 {
		t.Errorf("%d devices hold a stream, want the one that is left", open)
	}
	if _, resp := follow(t, base, token, head); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the removed device opens a stream: %s", resp.Status)
	}
}

// A device removed and added again under its name keeps the stream of its
// new token: the removal in the log it replays is of the old one.
func TestUserStreamReplaysAnOldRemoval(t *testing.T) {
	st, u, _ := userYard(t)
	ctx := context.Background()
	if _, err := st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	again, err := st.AddDevice(ctx, "jo", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)
	lines, _ := follow(t, listen(t, u, time.Minute), again.Token, 0)
	for seq := uint64(1); seq <= head; seq++ {
		if line := next(t, lines); !strings.Contains(line, fmt.Sprintf(`"seq":%d,`, seq)) {
			t.Fatalf("line %d of the stream is %q", seq, line)
		}
	}
}

// A stream's credential is resolved again as it is held, and the stream is
// closed when it no longer resolves, whatever took the way in away: here a
// resolver that stops knowing it, as a source whose session ended will.
func TestUserStreamIsCheckedAgain(t *testing.T) {
	st, u, token := userYard(t)
	var mu sync.Mutex
	revoked := false
	device := u.resolve
	u.resolve = func(ctx context.Context, credential, from string) (*Caller, error) {
		mu.Lock()
		defer mu.Unlock()
		if revoked {
			return nil, nil
		}
		return device(ctx, credential, from)
	}
	u.recheck = 20 * time.Millisecond
	head, _ := st.Head(context.Background())
	lines, _ := follow(t, listen(t, u, time.Minute), token, head)
	time.Sleep(100 * time.Millisecond) // checked a few times, and kept
	if err := st.Mail(context.Background(), "jo", "hello", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if line := next(t, lines); !strings.Contains(line, "hello") {
		t.Fatalf("the stream sent %q, want the mail", line)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	if line := next(t, lines); line != "" {
		t.Errorf("the stream of a credential that no longer resolves sent %q, want it closed", line)
	}
}

// A device holds StreamsPerCredential streams and no more: one past them is
// 503 with Retry-After, never 429, and has its place when one is closed.
// Another device's streams are counted apart.
func TestUserStreamsAreBounded(t *testing.T) {
	st, u, token := userYard(t)
	sam, err := st.AddDevice(context.Background(), "sam", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	base := listen(t, u, time.Minute)
	var held []*http.Response
	for i := 0; i < StreamsPerCredential; i++ {
		_, resp := follow(t, base, token, 0)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d = %s", i+1, resp.Status)
		}
		held = append(held, resp)
	}
	if _, resp := follow(t, base, token, 0); resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("one stream too many = %s (Retry-After %q), want 503 with one", resp.Status, resp.Header.Get("Retry-After"))
	}
	if _, resp := follow(t, base, sam.Token, 0); resp.StatusCode != http.StatusOK {
		t.Errorf("another device's stream beside them = %s", resp.Status)
	}
	held[0].Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, resp := follow(t, base, token, 0)
		if resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a stream after one was closed = %s", resp.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A call's arguments are bounded: PutBlob by the yard's cap on a blob, the
// others by maxUserCall.
func TestUserCallIsBounded(t *testing.T) {
	st, u, token := userYard(t)
	h := u.Handler()
	big := `{"bead":"yd-shed","text":"` + strings.Repeat("x", maxUserCall) + `"}`
	if w := ask(h, http.MethodPost, "/user/call/AddNote", token, strings.NewReader(big)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a note over the bound = %d, want 413", w.Code)
	}
	if err := st.SetSetting(context.Background(), store.BlobMaxSetting, "100", "jo"); err != nil {
		t.Fatal(err)
	}
	blob := `{"data":"` + strings.Repeat("A", maxUserCall+1000) + `"}`
	if w := ask(h, http.MethodPost, "/user/call/PutBlob", token, strings.NewReader(blob)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a blob over the yard's cap and the bound = %d, want 413", w.Code)
	}
	if got := call(t, h, token, "PutBlob", `{"data":"`+apiPNG+`"}`); !strings.Contains(got, apiPNGHash) {
		t.Errorf("PutBlob within the cap = %s", got)
	}
}

// A device reads a conversation in pages as the socket's client does, and
// one that asks without limit and before is answered as before.
func TestUserConversationPages(t *testing.T) {
	st, u, token := userYard(t)
	ctx := context.Background()
	h := u.Handler()
	for _, m := range [][3]string{{"yardmaster", "one", "jo"}, {"jo", "two", "yardmaster"}, {"yardmaster", "not this", "sam"}, {"yardmaster", "three", "jo"}} {
		if err := st.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	all, _, err := st.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("the conversation = %+v, %v", all, err)
	}
	reply := func(letters []store.Letter, more bool) string {
		out := map[string]any{"result": letters}
		if more {
			out["more"] = true
		}
		raw, _ := json.Marshal(out)
		return string(raw)
	}
	for _, c := range []struct{ body, want string }{
		{`{"b":"yardmaster"}`, reply(all, false)},
		{`{"a":"jo","b":"yardmaster","after":0}`, reply(all, false)},
		{`{"b":"yardmaster","limit":2}`, reply(all[1:], true)},
		{fmt.Sprintf(`{"b":"yardmaster","limit":2,"before":%d}`, all[1].Seq), reply(all[:1], false)},
		{fmt.Sprintf(`{"b":"yardmaster","before":%d}`, all[2].Seq), reply(all[:2], false)},
		{`{"b":"yardmaster","limit":3}`, reply(all, false)},
	} {
		var got, want any
		raw := call(t, h, token, "Conversation", c.body)
		if json.Unmarshal([]byte(raw), &got) != nil || json.Unmarshal([]byte(c.want), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Conversation %s answers\n%s\nwant\n%s", c.body, raw, c.want)
		}
		if strings.Contains(raw, `"more"`) != strings.Contains(c.want, `"more"`) {
			t.Errorf("Conversation %s: more is in %s", c.body, raw)
		}
	}
	// The page is the caller's as the whole is.
	if got := call(t, h, token, "Conversation", `{"a":"sam","b":"yardmaster","limit":1}`); !strings.Contains(got, "this device calls as jo") {
		t.Errorf("a page of another's conversation = %s", got)
	}
}

// A device is answered the user's conversation as the socket answers it:
// with what the user wrote under a name a link trusts as them.
func TestDeviceConversationHasTheTrustedName(t *testing.T) {
	st, token := apiStore(t)
	raw := string(post(t, overNetwork(t, st, token), "Conversation", `{"b":"yardmaster"}`))
	if !strings.Contains(raw, `"from":"josephine@north","to":"yardmaster","text":"And the fence after the shed.","read":false,"as":"jo"`) {
		t.Fatalf("jo's device is answered\n%s", raw)
	}
}
