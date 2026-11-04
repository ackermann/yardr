package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// strongAsk puts the run ask that adds the user name on the bead, addressed
// to the operator to. Adding a user is a trust card, so a strong one.
func (y *runYard) strongAsk(t *testing.T, to, name string) uint64 {
	t.Helper()
	seq, err := y.st.AddAsk(context.Background(), "yd-crm", store.Ask{Question: "Add " + name + "?", To: to, Context: []string{name + " joins on Monday."},
		Run: &store.AskRun{Argv: []string{"user", "add", name}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// strongArgs is the body of an approval of the strong ask seq, with the
// word that its person saw the card's weight.
func strongArgs(seq uint64) string {
	return fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"weighed":true}`, seq)
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

	// The ask above is routine and asked for no word. A weighty one that
	// is not strong is approved by a call that says its person saw the
	// weight (weighed), as anywhere: without it the yard refuses in its
	// words, the listener passes them on, and nothing changes; with it
	// the same ask is approved.
	weighty, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the CRM make updates?", To: "jo", Context: []string{"the import writes notes back."},
		Run: &store.AskRun{Argv: []string{"source", "set", "crm", "updates_per_hour=20"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`ask %d is weighty (outside): approve it where its weight is shown; a client sends \"weighed\": true once its person has passed its gate`, weighty)
	w = y.approve(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d}`, weighty)))
	if !strings.Contains(w.Body.String(), want) {
		t.Fatalf("a weighty approval without the word is answered %s, want %s", w.Body, want)
	}
	if src, err := y.st.GetSource(ctx, "crm"); err != nil || src.UpdatesPerHour != 0 {
		t.Fatalf("the refused approval set updates to %+v, %v", src, err)
	}
	w = y.approve(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"weighed":true}`, weighty)))
	if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("a weighty approval with the word is answered %s", w.Body)
	}
	if src, err := y.st.GetSource(ctx, "crm"); err != nil || src.UpdatesPerHour != 20 {
		t.Fatalf("the approval with the word left %+v, %v", src, err)
	}

	// A strong card is approved the same way: without the word the
	// listener passes the yard's words on and nothing changes; with it the
	// command runs, and the answer's event and the change's own name the
	// device and claim no witness, as a terminal's or the hosted web's would.
	kim := y.strongAsk(t, "jo", "kim")
	wantWeighed := fmt.Sprintf(`ask %d is weighty (trust): approve it where its weight is shown; a client sends \"weighed\": true once its person has passed its gate`, kim)
	w = y.approve(onTheMachine, y.jo, strings.NewReader(fmt.Sprintf(`{"bead":"yd-crm","ask":%d}`, kim)))
	if !strings.Contains(w.Body.String(), wantWeighed) {
		t.Fatalf("a strong approval without the word is answered %s, want %s", w.Body, wantWeighed)
	}
	if _, err := y.st.GetUser(ctx, "kim"); err == nil {
		t.Fatal("the refused approval added kim")
	}
	w = y.approve(onTheMachine, y.jo, strings.NewReader(strongArgs(kim)))
	if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("a strong approval with the word is answered %s", w.Body)
	}
	if _, err := y.st.GetUser(ctx, "kim"); err != nil {
		t.Fatalf("the approval did not add kim: %v", err)
	}
	for _, kind := range []string{"user_added", "noted"} {
		evs := eventsOfKind(t, y.st, kind)
		if last := evs[len(evs)-1]; last.Actor != "jo" || store.Via(last) != "phone" || last.Data["witness"] != nil {
			t.Errorf("the last %s event is %+v, want jo's via phone with no witness", kind, last)
		}
	}

	// A second operator's device approves a run ask put to the first: any
	// operator may, so an approval waits on no one person.
	t.Run("a second operator approves an ask to the first", func(t *testing.T) {
		y := newRunYard(t)
		if _, err := y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo"); err != nil {
			t.Fatal(err)
		}
		w := y.approve(onTheMachine, y.sam, strings.NewReader(y.args()))
		if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("sam's approval is answered %d %s", w.Code, w.Body)
		}
		if src, err := y.st.GetSource(ctx, "crm"); err != nil || src.CallsPerHour != 120 {
			t.Fatalf("the source is %+v, %v: want 120 calls an hour", src, err)
		}
		if set := eventsOfKind(t, y.st, "source_set"); len(set) != 1 || set[0].Actor != "sam" || store.Via(set[0]) != "tablet" {
			t.Fatalf("source_set events: %+v, want one by sam via tablet", set)
		}
	})

	// The header a witness crosses with is dropped from a device's
	// request before the call is made, so the yard judges the approval
	// as the device's own: one the fresh gate would refuse if it read
	// it is approved, and the answer's event names the device and claims
	// no witness.
	t.Run("a device that sends a fresh witness is witnessed by none", func(t *testing.T) {
		y := newRunYard(t)
		if err := y.st.SetSetting(ctx, store.WebJWTPrivilegedSessionMaxAgeSetting, "15m", "jo"); err != nil {
			t.Fatal(err)
		}
		now := strconv.FormatInt(time.Now().Unix(), 10)
		// Each header is one the yard would refuse if it read it: made
		// as another user, or naming no user and time.
		for i, witness := range []string{"sam " + now, "bogus"} {
			name := fmt.Sprintf("lee%d", i)
			seq := y.strongAsk(t, "jo", name)
			w := y.approve(onTheMachine, y.jo, strings.NewReader(strongArgs(seq)), FreshWitnessHeader, witness)
			if !strings.Contains(w.Body.String(), `"result"`) || strings.Contains(w.Body.String(), `"error"`) {
				t.Fatalf("the approval carrying %q is answered %s", witness, w.Body)
			}
			if _, err := y.st.GetUser(ctx, name); err != nil {
				t.Fatalf("the approval carrying %q added no user: %v", witness, err)
			}
			noted := eventsOfKind(t, y.st, "noted")
			if last := noted[len(noted)-1]; last.Actor != "jo" || store.Via(last) != "phone" || last.Data["witness"] != nil {
				t.Errorf("the answer's event carrying %q is %+v, want jo's via phone with no witness", witness, last)
			}
		}
	})
}

// ApproveAsk is on the listener for a device of the yard's operator and for
// nobody else. A device of another user, also for a command that is that
// user's own to approve on the socket, a credential that resolves to the
// operator and is no device token of the yard, and the device of an
// operator made a member are answered as for a method the yard does not have; a
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
		// A strong card is no way round the door: the call is the operator's
		// device's, whatever the ask and whatever the word.
		kim := y.strongAsk(t, "jo", "kim")
		notThere(t, y.approve(onTheMachine, y.sam, strings.NewReader(strongArgs(kim))))
		if _, err := y.st.GetUser(ctx, "kim"); err == nil {
			t.Error("sam's device added kim")
		}
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
		if _, err := y.st.ApproveAsk(ctx, "yd-crm", own, "sam", false, nil); err != nil {
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
		if _, err := y.st.AddPeer(ctx, store.Peer{Name: "north", Key: store.Public(key), URL: "http://north.test"}, "jo"); err != nil {
			t.Fatal(err)
		}
		notLetIn(t, y.approve(onTheMachine, "", unread{t}))
		notLetIn(t, y.approve(onTheMachine, store.Public(key), unread{t}))
		notLetIn(t, y.approve(onTheMachine, "", unread{t}, "Yardr-Peer-Key", store.Public(key)))
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
	t.Run("an operator made a member", func(t *testing.T) {
		y := newRunYard(t)
		if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
			t.Fatal(err)
		}
		notThere(t, y.approve(onTheMachine, y.jo, unread{t}))
		y.nothingRan(t, "an operator made a member")
		kim := y.strongAsk(t, "sam", "kim")
		notThere(t, y.approve(onTheMachine, y.jo, strings.NewReader(strongArgs(kim))))
		if _, err := y.st.GetUser(ctx, "kim"); err == nil {
			t.Error("the device of an operator made a member added kim")
		}
	})
	// The operator's device calls as the operator and in the one form.
	t.Run("as anyone else, and the array of yardr's own builds", func(t *testing.T) {
		y := newRunYard(t)
		// A name the call does not have is refused in a sentence that does not
		// say the name back, since a secret's value could sit where a name
		// goes. footplate reads that sentence, the one message a client reads:
		// it matches the whole of it, lead included, for each of the three
		// calls, to make the call once more without weighed when a yard from
		// before that argument refuses it. So these rows are the whole
		// message, and the sentences stay byte-identical.
		stray := fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"run":"x"}`, y.run)
		for _, c := range []struct{ method, body, want string }{
			{"ApproveAsk", fmt.Sprintf(`{"bead":"yd-crm","ask":%d,"actor":"sam"}`, y.run), `this device calls as jo: \"actor\" is jo or left out`},
			{"ApproveAsk", fmt.Sprintf(`["yd-crm", %d, "jo"]`, y.run), "takes them as a JSON object, by name"},
			{"ApproveAsk", stray, `"message":"an argument: ApproveAsk has none of that name (it takes bead, ask, actor, weighed, values)"`},
			{"ApproveSetup", stray, `"message":"an argument: ApproveSetup has none of that name (it takes bead, ask, option, values, account, actor, weighed)"`},
			{"GiveSecret", stray, `"message":"an argument: GiveSecret has none of that name (it takes bead, ask, values, actor, weighed)"`},
		} {
			if w := y.post(c.method, onTheMachine, y.jo, strings.NewReader(c.body)); !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("%s %s is answered %s, want %s", c.method, c.body, w.Body, c.want)
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

// Over the link from the edge a device of the operator approves a run
// ask, and the yard runs the command the ask holds: pairing is the grant.
// What the approval writes says it came by the edge: the change's event,
// the answer on the bead and its noted event are marked "phone via edge".
func TestUserApprovesARunAskOverTheLink(t *testing.T) {
	ctx := context.Background()
	y := newRunYard(t)
	w := y.approve(edgelink.RemoteAddr, y.jo, strings.NewReader(y.args()))
	var got struct {
		Result *store.AskApproved `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Result == nil {
		t.Fatalf("answered %d %s (%v)", w.Code, w.Body, err)
	}
	if src, err := y.st.GetSource(ctx, "crm"); err != nil || src.CallsPerHour != 120 {
		t.Fatalf("the source is %+v, %v: want 120 calls an hour", src, err)
	}
	set := eventsOfKind(t, y.st, "source_set")
	if len(set) != 1 || set[0].Actor != "jo" || store.Via(set[0]) != "phone via edge" {
		t.Fatalf("source_set events: %+v, want one by jo via phone via edge", set)
	}
	notes, err := y.st.Notes(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	answer := notes[len(notes)-1]
	if answer.Kind != store.NoteAnswer || answer.Author != "jo" || answer.Via != "phone via edge" {
		t.Errorf("the answer is %+v, want jo's via phone via edge", answer)
	}
	noted := eventsOfKind(t, y.st, "noted")
	if last := noted[len(noted)-1]; last.Actor != "jo" || store.Via(last) != "phone via edge" {
		t.Errorf("the answer's event is %+v, want jo's via phone via edge", last)
	}

	t.Run("the edge says the device used plain HTTP", func(t *testing.T) {
		y := newRunYard(t)
		w := y.approve(edgelink.RemoteAddr, y.jo, unread{t}, "X-Forwarded-Proto", "http")
		for _, want := range []string{
			"the proxy in front of the yard says this request came to it in plain HTTP",
			"The device's token was sent with it",
			"yardr user device rm",
		} {
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("answered %s, want it to say %q", w.Body, want)
			}
		}
		y.nothingRan(t, "the edge reported plain HTTP")
	})
}

// From the router's peer, said https, an approval is refused with today's
// sentence, before the body is read, and nothing runs: the router's
// exception is the secret's, not an approval's.
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
