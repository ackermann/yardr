package yards_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

func said(t *testing.T, y *yard, peer string) *store.PeerSaid {
	t.Helper()
	p, err := y.api.GetPeer(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	return p.Said
}

func understands(s *store.PeerSaid) bool {
	return s != nil && reflect.DeepEqual(s.Understands, store.PeerKinds) && !s.At.IsZero()
}

// Every signed request says which kinds its yard's build understands, and
// so does every signed answer: the yard that is posted to or asked keeps
// what the request said, and the yard that asks what the answer said. Not
// once at pairing: with each.
func TestYardsSayWhatTheyUnderstand(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	toB := a.add(t, "b", b, true, ping, ping)
	b.add(t, "a", a, false, ping, ping)
	if said(t, a, "b") != nil || said(t, b, "a") != nil {
		t.Fatal("a link nothing crossed has said something")
	}
	if got := yards.Says(); !reflect.DeepEqual(got.Understands, store.PeerKinds) {
		t.Fatalf("Says = %+v", got)
	}

	// A post: b keeps what a's request said, and its answer says b's.
	if err := a.caller().Push(ctx, toB, store.PeerMessage{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got := said(t, b, "a"); !understands(got) {
		t.Fatalf("b, after a posted: a said %+v", got)
	}
	for name, r := range map[string]*request{
		"taken":     signed(a, b, "POST", yards.Path, []byte(`{"id":"m2","kind":"ping","body":{}}`)),
		"duplicate": signed(a, b, "POST", yards.Path, []byte(`{"id":"m2","kind":"ping","body":{}}`)),
		"refused":   signed(a, b, "POST", yards.Path, []byte(`{"id":"m3","kind":"mail","body":{}}`)),
		"asked":     signed(a, b, "GET", yards.Path+"?after=0", nil),
	} {
		resp, body := r.send(t, b)
		var answer struct {
			Says *store.PeerSays `json:"says"`
		}
		if err := json.Unmarshal(body, &answer); err != nil || answer.Says == nil || !reflect.DeepEqual(answer.Says.Understands, store.PeerKinds) {
			t.Errorf("%s: b answered %d %s, which does not say what it understands", name, resp.StatusCode, body)
		}
		if err := store.VerifyResponse(b.pub(), resp.StatusCode, r.sig, resp.Header.Get(yardkey.HeaderSig), body); err != nil {
			t.Errorf("%s: what b says is not under its signature: %v", name, err)
		}
	}

	// A question: a keeps what its answer said, once the store has checked
	// that the answer is b's, and b kept what the question said.
	if err := b.own.PeerHeard(ctx, "a", a.pub(), nil); err != nil {
		t.Fatal(err)
	}
	batch, err := a.caller().Pull(ctx, toB, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if said(t, a, "b") != nil {
		t.Fatal("a kept what an answer said before the store checked whose it is")
	}
	if _, err := a.api.PeerPulled(ctx, "b", batch); err != nil {
		t.Fatal(err)
	}
	if got := said(t, a, "b"); !understands(got) {
		t.Fatalf("a, after it asked b: b said %+v", got)
	}
	if got := said(t, b, "a"); !understands(got) {
		t.Fatalf("b, after a asked: a said %+v", got)
	}
}

// A signed answer to a post is kept too; a pull may fail after the post has
// succeeded, so its answer is not left for a later question to repeat.
func TestPostAnswerSaysWhatThePeerUnderstands(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	toB := a.add(t, "b", b, true, ping, ping)
	b.add(t, "a", a, false, ping, ping)
	c := a.caller()
	c.Heard = func(ctx context.Context, p store.Peer, answer store.PeerAnswer) {
		if err := a.own.PeerPostAnswered(ctx, p.Name, answer); err != nil {
			t.Errorf("keep what b said: %v", err)
		}
	}
	if err := c.Push(ctx, toB, store.PeerMessage{ID: "post-answer", Kind: "ping", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got := said(t, a, "b"); !understands(got) {
		t.Fatalf("a did not keep b's signed post answer: %+v", got)
	}
}

// A yard on this build and one on the build before it, which says nothing
// of what it understands, still exchange ping and mail. The older yard's
// requests are replayed as it signs them, without the statement: each is
// taken, and the yard is kept as one that has not said.
func TestOlderYardrIsStillHeard(t *testing.T) {
	old, b := newYard(t), newYard(t)
	ctx := context.Background()
	both := []string{"ping", "mail"}
	b.add(t, "old", old, false, both, both)
	if _, err := b.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b.expose(t, "old", "yardmaster")
	// It said once, on a newer build it has since left.
	if err := b.own.PeerHeard(ctx, "old", old.pub(), store.Says("v9")); err != nil {
		t.Fatal(err)
	}

	resp, body := signed(old, b, "POST", yards.Path, []byte(`{"id":"p1","kind":"ping","body":{}}`)).send(t, b)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"taken":true`) {
		t.Fatalf("an older yardr's ping = %d %s", resp.StatusCode, body)
	}
	if got := said(t, b, "old"); got != nil {
		t.Fatalf("a yard that says nothing is kept as having said %+v", got)
	}
	resp, body = signed(old, b, "POST", yards.Path, []byte(`{"id":"l1","kind":"mail","body":{"to":"yardmaster","from":"yardmaster","text":"hello from before"}}`)).send(t, b)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("an older yardr's mail = %d %s", resp.StatusCode, body)
	}
	if mail := b.events(t, "mail"); len(mail) != 1 || mail[0].Data["text"] != "hello from before" || mail[0].Actor != "yardmaster@old" {
		t.Fatalf("b's mail = %+v", mail)
	}
	// Its question, as it asks it: after and nothing more. It is handed the
	// pong, in a page it reads the fields it knows of.
	resp, body = signed(old, b, "GET", yards.Path+"?after=0", nil).send(t, b)
	var page struct {
		Messages []store.PeerMessage `json:"messages"`
		Push     bool                `json:"push"`
	}
	if err := json.Unmarshal(body, &page); resp.StatusCode != http.StatusOK || err != nil || len(page.Messages) != 1 || page.Messages[0].Re != "p1" {
		t.Fatalf("an older yardr's question = %d %s", resp.StatusCode, body)
	}
	if got := said(t, b, "old"); got != nil {
		t.Fatalf("after its question: %+v", got)
	}
}

// oldYard answers as a yardr from before yards said what they understand:
// it reads of a posted message the four fields it knows, of a question
// after alone, and answers with neither says.
func oldYard(t *testing.T, y *yard, from *yard, page string) (*httptest.Server, *[]store.PeerMessage) {
	t.Helper()
	var got []store.PeerMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get(yardkey.HeaderSig)
		if err := yardkey.VerifyRequest(from.pub(), y.pub(), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, y.now); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		answer := []byte(page)
		if r.Method == http.MethodPost {
			var m struct {
				ID   string          `json:"id"`
				Kind string          `json:"kind"`
				Re   string          `json:"re,omitempty"`
				Body json.RawMessage `json:"body"`
			}
			if err := json.Unmarshal(body, &m); err != nil {
				t.Errorf("an older yardr does not read %s: %v", body, err)
			}
			got = append(got, store.PeerMessage{ID: m.ID, Kind: m.Kind, Re: m.Re, Body: m.Body})
			answer = []byte(`{"taken":true}`)
		} else if r.URL.Query().Get("after") == "" {
			t.Errorf("a question without after: %s", r.URL.RequestURI())
		}
		w.Header().Set(yardkey.HeaderSig, store.SignResponse(y.key, http.StatusOK, sig, answer))
		_, _ = w.Write(answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// The other way: what this build posts to and asks of an older yardr is
// read there, the statement passed by as a field it does not know, and its
// answers, which say nothing, are taken here.
func TestOlderYardrIsStillCalled(t *testing.T) {
	a, old := newYard(t), newYard(t)
	ctx := context.Background()
	srv, got := oldYard(t, old, a, `{"messages":[{"seq":1,"id":"p9","kind":"ping","body":{}}]}`)
	both := []string{"ping", "mail"}
	if _, err := a.api.AddPeer(ctx, store.Peer{Name: "old", Key: old.pub(), URL: srv.URL}, "jo"); err != nil {
		t.Fatal(err)
	}
	toOld, err := a.api.AllowPeer(ctx, "old", store.PeerAllow{Send: both, Receive: both}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mail := json.RawMessage(`{"to":"yardmaster","from":"yardmaster","text":"hello"}`)
	for _, m := range []store.PeerMessage{{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`)}, {ID: "m2", Kind: "mail", Body: mail}} {
		if err := a.caller().Push(ctx, *toOld, m); err != nil {
			t.Fatalf("a %s posted to an older yardr = %v", m.Kind, err)
		}
	}
	if len(*got) != 2 || (*got)[0].ID != "m1" || (*got)[1].Kind != "mail" || string((*got)[1].Body) != string(mail) {
		t.Fatalf("the older yardr read %+v", *got)
	}
	batch, err := a.caller().Pull(ctx, *toOld, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.api.PeerPulled(ctx, "old", batch)
	if err != nil || len(res.Taken) != 1 || res.Taken[0].ID != "p9" || res.Taken[0].Refused != "" {
		t.Fatalf("an older yardr's page = %+v, %v", res, err)
	}
	if got := said(t, a, "old"); got != nil {
		t.Fatalf("an older yardr is kept as having said %+v", got)
	}
}

// A message of a kind the receiving yard's yardr does not know is answered
// 403 with a reason of its own, and the sender says that the peer's yardr
// does not know the kind: not that its link does not carry it, which a peer
// allow there would mend.
func TestUnknownKindIsRefusedAsThat(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	toB := a.add(t, "b", b, true, ping, ping)
	b.add(t, "a", a, false, ping, ping)
	err := a.caller().Push(ctx, toB, store.PeerMessage{ID: "h1", Kind: "handoff", Body: json.RawMessage(`{}`)})
	if !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "refused by the peer: b's yardr does not know handoff") {
		t.Fatalf("a handoff posted to a yard that knows none = %v", err)
	}
	resp, body := signed(a, b, "POST", yards.Path, []byte(`{"id":"h2","kind":"handoff","body":{},"says":{"understands":["handoff","ping"]}}`)).send(t, b)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), `"refused":"kind_unknown"`) {
		t.Fatalf("answered %d %s", resp.StatusCode, body)
	}
	ref := b.events(t, "peer_message_refused")
	if len(ref) != 2 || ref[1].Data["reason"] != "kind_unknown" || ref[1].Data["detail"] != "this yard's yardr does not know handoff" || ref[1].Data["way"] != "push" {
		t.Fatalf("b's peer_message_refused = %+v", ref)
	}
	// What it said with the refused message is kept all the same.
	if got := said(t, b, "a"); got == nil || !got.Knows("handoff") {
		t.Fatalf("b, of a: %+v", got)
	}
	// A ping after it is taken: the refusal was of the one message.
	if err := a.caller().Push(ctx, toB, store.PeerMessage{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}
