package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// runYard is secretYard with a run ask besides: the yardmaster proposes
// that the source crm may be called more often, for jo to approve.
type runYard struct {
	*secretYard
	run uint64
}

func newRunYard(t *testing.T) *runYard {
	t.Helper()
	y := &runYard{secretYard: newSecretYard(t)}
	var err error
	y.run, err = y.st.AddAsk(context.Background(), "yd-crm", store.Ask{Question: "May the CRM be called more often?", To: "jo",
		Context: []string{"The import was refused its 61st call of the hour."},
		Run:     &store.AskRun{Argv: []string{"source", "set", "crm", "calls_per_hour=120"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return y
}

// approve asks the listener for ApproveAsk of the yard's run ask.
func (y *runYard) approve(from, credential string, body io.Reader, header ...string) *httptest.ResponseRecorder {
	return y.post("ApproveAsk", from, credential, body, header...)
}

func (y *runYard) args() string {
	return fmt.Sprintf(`{"bead":"yd-crm","ask":%d}`, y.run)
}

// nothingRan fails the test when the command ran: the source has the limit
// it had, the ask is open, and no source_set event was written.
func (y *runYard) nothingRan(t *testing.T, after string) {
	t.Helper()
	ctx := context.Background()
	src, err := y.st.GetSource(ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	if src.CallsPerHour != 0 {
		t.Errorf("%s: the source may be called %d times an hour", after, src.CallsPerHour)
	}
	if evs := eventsOfKind(t, y.st, "source_set"); len(evs) != 0 {
		t.Errorf("%s: %d source_set events", after, len(evs))
	}
	b, err := y.st.Bead(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range b.Asks {
		if a.Ask == y.run && (a.State != "open" || a.Ran != nil) {
			t.Errorf("%s: the run ask is %s, ran %+v", after, a.State, a.Ran)
		}
	}
}

// A device of the operator approves a run ask over the listener, and the
// yard runs the command the ask holds: the call answers the command and
// what it printed, and the answer on the bead and the change's own event
// say that it came through the device.
func TestUserApprovesARunAsk(t *testing.T) {
	ctx := context.Background()
	y := newRunYard(t)
	w := y.approve(onTheMachine, y.jo, strings.NewReader(y.args()))
	var got struct {
		Result *store.AskApproved `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Result == nil {
		t.Fatalf("answered %d %s (%v)", w.Code, w.Body, err)
	}
	if r := got.Result; r.Bead != "yd-crm" || r.Ask != y.run || r.Asker != "yardmaster" ||
		strings.Join(r.Argv, " ") != "source set crm calls_per_hour=120" || r.Result == "" || len(r.Does) == 0 {
		t.Errorf("answered %+v", r)
	}
	if src, err := y.st.GetSource(ctx, "crm"); err != nil || src.CallsPerHour != 120 {
		t.Fatalf("the source is %+v, %v: want 120 calls an hour", src, err)
	}
	// The change's event: the operator's, through the device, and tied to
	// the ask that proposed it.
	set := eventsOfKind(t, y.st, "source_set")
	if len(set) != 1 || set[0].Actor != "jo" || store.Via(set[0]) != "phone" {
		t.Fatalf("source_set events: %+v, want one by jo via phone", set)
	}
	if on, _ := set[0].Data["approved"].(map[string]any); on["bead"] != "yd-crm" || on["ask"] != float64(y.run) {
		t.Errorf("the event's approved is %v", set[0].Data["approved"])
	}
	// The answer: jo's note, through the device, and its noted event.
	notes, err := y.st.Notes(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	answer := notes[len(notes)-1]
	if answer.Kind != store.NoteAnswer || answer.Author != "jo" || answer.Via != "phone" || !strings.Contains(answer.Text, `"ran"`) {
		t.Errorf("the answer is %+v, want jo's via phone, with what ran", answer)
	}
	noted := eventsOfKind(t, y.st, "noted")
	if last := noted[len(noted)-1]; last.Actor != "jo" || store.Via(last) != "phone" {
		t.Errorf("the answer's event is %+v, want jo's via phone", last)
	}
	// What the store refuses is refused here in its words: a command runs
	// once for one ask.
	if w := y.approve(onTheMachine, y.jo, strings.NewReader(y.args())); !strings.Contains(w.Body.String(), "was approved already, by jo") {
		t.Errorf("a second approval is answered %s", w.Body)
	}
	if set := eventsOfKind(t, y.st, "source_set"); len(set) != 1 {
		t.Errorf("%d source_set events after the second approval", len(set))
	}

	// The ask above is routine and asked for no word. A weighty one is
	// approved by a call that says its person saw the weight (weighed), as
	// anywhere: without it the yard refuses in its words, the listener
	// passes them on, and nothing changes; with it the same ask is approved.
	weighty, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Add kim?", To: "jo", Context: []string{"kim joins on Monday."},
		Run: &store.AskRun{Argv: []string{"user", "add", "kim"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`ask %d is weighty (trust): approve it where its weight is shown; a client sends \"weighed\": true once its person has passed its gate`, weighty)
	w = y.approve(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d}`, weighty)))
	if !strings.Contains(w.Body.String(), want) {
		t.Fatalf("a weighty approval without the word is answered %s, want %s", w.Body, want)
	}
	if _, err := y.st.GetUser(ctx, "kim"); err == nil {
		t.Fatal("the refused approval added kim")
	}
	w = y.approve(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"weighed":true}`, weighty)))
	if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("a weighty approval with the word is answered %s", w.Body)
	}
	if _, err := y.st.GetUser(ctx, "kim"); err != nil {
		t.Fatalf("the approval with the word did not add kim: %v", err)
	}
}

// ApproveAsk is on the listener for a device of the yard's operator and for
// nobody else. A device of another user, also for a command that is that
// user's own to approve on the socket, a credential that resolves to the
// operator and is no device token of the yard, and every device in a yard
// with no operator are answered as for a method the yard does not have; a
// removed device, a peer's key and no credential at all are not let in.
// Each is answered before the request's body is read, and nothing runs.
func TestUserApproveAskIsForTheOperatorsDeviceAlone(t *testing.T) {
	ctx := context.Background()
	none, _ := json.Marshal(noSuchMethod("ApproveAsk"))
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
	t.Run("a device of another user", func(t *testing.T) {
		y := newRunYard(t)
		notThere(t, y.approve(onTheMachine, y.sam, unread{t}))
		y.nothingRan(t, "sam's device")
	})
	// On the socket sam approves a subscription of their own. Not here: the
	// call is the operator's device's, whatever the ask.
	t.Run("a device of the user a command is the own of", func(t *testing.T) {
		y := newRunYard(t)
		own, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "Hear of what is done?", To: "sam",
			Context: []string{"You asked to be told when a bead is done."},
			Run:     &store.AskRun{Argv: []string{"subscribe", "sam", "yardr", "--done"}}}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		notThere(t, y.approve(onTheMachine, y.sam, unread{t}))
		if evs := eventsOfKind(t, y.st, "subscribed"); len(evs) != 0 {
			t.Errorf("%d subscribed events", len(evs))
		}
		if _, err := y.st.ApproveAsk(ctx, "yd-crm", own, "sam", false); err != nil {
			t.Errorf("sam's approval on the socket: %v", err)
		}
	})
	t.Run("a device removed", func(t *testing.T) {
		y := newRunYard(t)
		if _, err := y.st.RemoveDevice(ctx, "jo", "phone", "jo"); err != nil {
			t.Fatal(err)
		}
		notLetIn(t, y.approve(onTheMachine, y.jo, unread{t}))
		y.nothingRan(t, "a removed device")
	})
	t.Run("no credential, and a peer's key", func(t *testing.T) {
		y := newRunYard(t)
		key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(key), URL: "http://north.test"}, "jo"); err != nil {
			t.Fatal(err)
		}
		notLetIn(t, y.approve(onTheMachine, "", unread{t}))
		notLetIn(t, y.approve(onTheMachine, yardkey.Public(key), unread{t}))
		notLetIn(t, y.approve(onTheMachine, "", unread{t}, "Yardr-Peer-Key", yardkey.Public(key)))
		y.nothingRan(t, "no device token")
	})
	t.Run("the operator, said by something that is no device token", func(t *testing.T) {
		y := newRunYard(t)
		y.u.resolve = func(context.Context, string, string) (*Caller, error) {
			return &Caller{User: "jo", SaidBy: "kratos"}, nil
		}
		notThere(t, y.approve(onTheMachine, "a-session-of-the-service", unread{t}))
		y.nothingRan(t, "a credential of an identity source")
	})
	t.Run("a yard with no operator", func(t *testing.T) {
		y := newRunYard(t)
		if err := y.st.SetSetting(ctx, store.OperatorSetting, "", "jo"); err != nil {
			t.Fatal(err)
		}
		notThere(t, y.approve(onTheMachine, y.jo, unread{t}))
		notThere(t, y.approve(onTheMachine, y.sam, unread{t}))
		y.nothingRan(t, "a yard with no operator")
	})
	// The operator's device calls as the operator and in the one form.
	t.Run("as anyone else, and the array of yardr's own builds", func(t *testing.T) {
		y := newRunYard(t)
		for body, want := range map[string]string{
			fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"actor":"sam"}`, y.run): `this device calls as jo: \"actor\" is jo or left out`,
			fmt.Sprintf(`["yd-crm", %d, "jo"]`, y.run):                     "takes them as a JSON object, by name",
			fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"run":"x"}`, y.run):     "ApproveAsk has none of that name (it takes bead, ask, actor, weighed)",
		} {
			if w := y.approve(onTheMachine, y.jo, strings.NewReader(body)); !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s is answered %s, want %s", body, w.Body, want)
			}
		}
		y.nothingRan(t, "calls that are refused")
	})
}

// An approval is taken where a secret is: from a request that reached the
// listener from the yard's own machine, where TLS ends, and that no proxy
// says came in plain. The refusal is made before the body is read, says
// that the token crossed with it, and nothing runs.
func TestUserApproveAskIsNotTakenInTheClear(t *testing.T) {
	for name, c := range map[string]struct {
		from   string
		header []string
		want   string
	}{
		"from another machine":             {elsewhere, nil, "in plain HTTP from another machine"},
		"from another machine, said https": {elsewhere, []string{"X-Forwarded-Proto", "https"}, "in plain HTTP from another machine"},
		"a proxy that says http":           {onTheMachine, []string{"X-Forwarded-Proto", "http"}, "the proxy in front of the yard says this request came to it in plain HTTP"},
	} {
		t.Run(name, func(t *testing.T) {
			y := newRunYard(t)
			w := y.approve(c.from, y.jo, unread{t}, c.header...)
			for _, want := range []string{c.want, "an approval is taken over https only, or from the yard's own machine", "The device's token was sent with it", "yardr user device rm"} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("answered %s, want it to say %q", w.Body, want)
				}
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Errorf("answered %s: an approval holds no secret", w.Body)
			}
			y.nothingRan(t, name)
		})
	}
	// Behind a proxy on the machine that says https, it is taken.
	y := newRunYard(t)
	if w := y.approve(onTheMachine, y.jo, strings.NewReader(y.args()), "X-Forwarded-Proto", "https"); !strings.Contains(w.Body.String(), `"result":{`) {
		t.Errorf("an approval through the proxy on the yard's machine is answered %s", w.Body)
	}
}

// Over the link from the edge an approval is refused with the link's own
// reason: approvals never cross the edge, and the edge saw the token this
// one carried. The fix is to answer on the yard's own machine. The
// refusal is made before the body is read, and nothing runs.
func TestUserApproveAskIsNotTakenOverTheLink(t *testing.T) {
	y := newRunYard(t)
	w := y.approve(edgelink.RemoteAddr, y.jo, unread{t})
	for _, want := range []string{
		"an approval never crosses the edge",
		"this request came down the link from the edge",
		"Answer on the yard's machine",
		"The edge saw the device's token with it",
		"yardr user device rm",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("answered %s, want it to say %q", w.Body, want)
		}
	}
	for _, not := range []string{"in plain HTTP from another machine", "the yard's listener", "Put the proxy or tunnel"} {
		if strings.Contains(w.Body.String(), not) {
			t.Errorf("answered %s, want none of %q", w.Body, not)
		}
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("answered %s: an approval holds no secret", w.Body)
	}
	y.nothingRan(t, "an approval over the link")
}

// An approval stays loopback-only: from the router's peer, said https, it
// is refused with today's sentence, before the body is read, and nothing
// runs.
func TestUserApproveAskIsNotTakenFromTheRouter(t *testing.T) {
	y := newRunYard(t)
	if err := y.st.SetSetting(context.Background(), store.RouterAddrSetting, "172.18.0.2", "jo"); err != nil {
		t.Fatal(err)
	}
	w := y.approve("172.18.0.2:40000", y.jo, unread{t}, "X-Forwarded-Proto", "https")
	for _, want := range []string{"in plain HTTP from another machine", "an approval is taken over https only, or from the yard's own machine", "The device's token was sent with it", "yardr user device rm"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("answered %s, want it to say %q", w.Body, want)
		}
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("answered %s: an approval holds no secret", w.Body)
	}
	y.nothingRan(t, "an approval from the router")
}
