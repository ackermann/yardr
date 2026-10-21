package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// mailYard is userYard with sam's device too: two users, jo the operator
// and sam, a device each.
func mailYard(t *testing.T) (*store.Store, *UserServer, string, string) {
	t.Helper()
	st, u, jo := userYard(t)
	sam, err := st.AddDevice(context.Background(), "sam", "tablet", "jo")
	if err != nil {
		t.Fatal(err)
	}
	return st, u, jo, sam.Token
}

func mustHead(t *testing.T, st *store.Store) uint64 {
	t.Helper()
	head, err := st.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// A device that subscribes from the first event is sent its own mail and
// none between two others: the mail, read receipts and reactions it is a
// party to arrive, and what two others wrote, read or reacted to does
// not. A bead's events and a hook delivery still arrive: those are the
// yard's own news, not mail.
func TestUserStreamShowsOnlyOwnMail(t *testing.T) {
	ctx := context.Background()
	st, u, joToken, samToken := mailYard(t)
	base := listen(t, u, time.Minute)
	since := mustHead(t, st)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Mail(ctx, "yardmaster", "crew business", "jo"))
	crewMail := mustHead(t, st)
	must(st.Mail(ctx, "sam", "for sam", "jo"))
	toSam := mustHead(t, st)
	must(st.Mail(ctx, "jo", "for jo", "sam"))
	fromSam := mustHead(t, st)
	must(st.AckUserInbox(ctx, "jo", fromSam))
	joRead := mustHead(t, st)
	must(st.AckUserInbox(ctx, "sam", toSam))
	samRead := mustHead(t, st)
	must(st.React(ctx, crewMail, "👍", "jo"))
	crewReact := mustHead(t, st)
	must(st.React(ctx, toSam, "👀", "sam"))
	samReact := mustHead(t, st)
	must(st.AddNote(ctx, "yd-shed", "painted", "jo"))
	noted := mustHead(t, st)
	hooked, _, err := st.RecordHook(ctx, "linear", map[string]any{"type": "note", "text": "hello"}, []byte("{}"), nil)
	must(err)
	head := mustHead(t, st)
	if hooked != head {
		t.Fatalf("the hook delivery is seq %d, want the head %d", hooked, head)
	}

	// read is the events after since on token's stream from the first
	// event, by seq.
	read := func(token string) map[uint64]store.Event {
		t.Helper()
		lines, resp := follow(t, base, token, 0)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the stream is status %d", resp.StatusCode)
		}
		got := map[uint64]store.Event{}
		for {
			var e store.Event
			if err := json.Unmarshal([]byte(next(t, lines)), &e); err != nil {
				t.Fatal(err)
			}
			if e.Seq > since {
				got[e.Seq] = e
			}
			if e.Seq == head {
				return got
			}
		}
	}

	sams := read(samToken)
	for _, tc := range []struct {
		name string
		seq  uint64
		want bool
	}{
		{"mail between two others", crewMail, false},
		{"mail to sam", toSam, true},
		{"mail from sam", fromSam, true},
		{"jo's read receipt", joRead, false},
		{"sam's read receipt", samRead, true},
		{"reaction between two others", crewReact, false},
		{"sam's reaction", samReact, true},
		{"a bead's note", noted, true},
		{"a hook delivery", hooked, true},
	} {
		_, has := sams[tc.seq]
		if has != tc.want {
			t.Errorf("sam's stream carries %s: %v, want %v", tc.name, has, tc.want)
		}
	}
	// What sam is sent of its own mail is the mail itself, body and all.
	if e := sams[toSam]; e.Data["text"] != "for sam" {
		t.Errorf("sam's mail arrives as %v, want its text", e.Data)
	}

	jos := read(joToken)
	for _, tc := range []struct {
		name string
		seq  uint64
		want bool
	}{
		{"jo's mail to the crew", crewMail, true},
		{"jo's mail to sam", toSam, true},
		{"sam's mail to jo", fromSam, true},
		{"jo's read receipt", joRead, true},
		{"sam's read receipt", samRead, false},
		{"jo's reaction", crewReact, true},
		{"sam's reaction", samReact, true},
	} {
		_, has := jos[tc.seq]
		if has != tc.want {
			t.Errorf("jo's stream carries %s: %v, want %v", tc.name, has, tc.want)
		}
	}
}

// A device reads a note's file, its own mail's files, and a file put
// within the hour whose mail or note is on its way; another user's mail
// file is 404, as it is in the web view, as is anything the yard never
// held, a name that is no hash, and a file put long ago that nothing
// names.
func TestUserBlobsServeOnlyWhatTheCallerMayRead(t *testing.T) {
	ctx := context.Background()
	st, u, joToken, samToken := mailYard(t)
	h := u.Handler()

	put := func(data string) string {
		t.Helper()
		ref, err := st.PutBlob(ctx, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return ref.Hash
	}
	noteFile := put("\x89PNG\r\n\x1a\n the shed on its note")
	ownFile := put("\xff\xd8\xff\xe0 the fence by mail")
	otherFile := put("GIF89a the crew's chart")
	if err := st.AddNote(ctx, "yd-shed", "hung the picture", "jo",
		store.BlobFile{Hash: noteFile, Name: "shed.png"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "sam", "for sam", "jo",
		store.BlobFile{Hash: ownFile, Name: "fence.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "yardmaster", "crew business", "jo",
		store.BlobFile{Hash: otherFile, Name: "chart.gif"}); err != nil {
		t.Fatal(err)
	}
	fresh := put("put this moment, named by nothing yet")
	old := put("put long ago, named by nothing")
	aged := time.Now().Add(-2 * store.BlobGrace)
	if err := os.Chtimes(st.BlobPath(old), aged, aged); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		token string
		hash  string
		want  int
	}{
		{"a note's file", samToken, noteFile, http.StatusOK},
		{"own mail in", samToken, ownFile, http.StatusOK},
		{"own mail out", joToken, ownFile, http.StatusOK},
		{"another user's fresh mail file", samToken, otherFile, http.StatusNotFound},
		{"the sender reads it", joToken, otherFile, http.StatusOK},
		{"a file just put", samToken, fresh, http.StatusOK},
		{"a file just put, another device", joToken, fresh, http.StatusOK},
		{"a file put long ago, named by nothing", samToken, old, http.StatusNotFound},
		{"a hash the yard never held", samToken, strings.Repeat("4", 64), http.StatusNotFound},
		{"a name that is no hash", samToken, "nope", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ask(h, http.MethodGet, "/user/blobs/"+tc.hash, tc.token, nil)
			if w.Code != tc.want {
				t.Errorf("GET /user/blobs/%s = %d, want %d", tc.hash, w.Code, tc.want)
			}
		})
	}
	if w := ask(h, http.MethodGet, "/user/blobs/"+noteFile, samToken, nil); w.Code != http.StatusOK ||
		w.Body.String() != "\x89PNG\r\n\x1a\n the shed on its note" {
		t.Errorf("a note's file = %d, %q", w.Code, w.Body.String())
	}
}

// deviceEvent sends a mail, a read receipt and a reaction only where the
// caller is its sender or its receiver: row by row, for sam's device
// against mail of every shape the log holds.
func TestUserDeviceEventShowsOnlyPartiesMail(t *testing.T) {
	_, u, _ := userYard(t)
	ctx := context.Background()
	sam := &Caller{User: "sam", SaidBy: "tablet", device: true}
	mail := func(actor string, data map[string]any) store.Event {
		return store.Event{Kind: "mail", Actor: actor, Data: data}
	}
	for _, tc := range []struct {
		name string
		e    store.Event
		want bool
	}{
		{"mail to sam", mail("jo", map[string]any{"to": "sam", "to_user": true, "text": "hi"}), true},
		{"mail from sam", mail("sam", map[string]any{"to": "yardmaster", "text": "hi"}), true},
		{"mail from sam's pane", mail("sam", map[string]any{"to": "jo", "to_user": true, "from_bead": "yd-shed"}), true},
		{"mail between two others", mail("jo", map[string]any{"to": "yardmaster", "text": "hi"}), false},
		{"mail sam sent to a linked yard", mail("sam", map[string]any{"to": "clerk", "to_peer": "north", "text": "hi"}), true},
		{"mail another sent to a linked yard", mail("jo", map[string]any{"to": "clerk", "to_peer": "north", "text": "hi"}), false},
		{"mail in from a linked yard to sam", mail("al@north", map[string]any{"to": "sam", "to_user": true, "from": "al", "from_peer": "north"}), true},
		{"mail in from a linked yard to another", mail("al@north", map[string]any{"to": "jo", "to_user": true, "from": "al", "from_peer": "north"}), false},
		{"a timer's mail to sam", mail("jo", map[string]any{"to": "sam", "to_user": true, "timer": "t1"}), true},
		{"sam's read receipt", store.Event{Kind: "mail_read", Actor: "sam", Data: map[string]any{"reader": "sam", "through": uint64(9)}}, true},
		{"another's read receipt", store.Event{Kind: "mail_read", Actor: "jo", Data: map[string]any{"reader": "jo", "through": uint64(9)}}, false},
		{"sam's reaction", store.Event{Kind: "reaction", Actor: "sam", Data: map[string]any{"seq": uint64(3), "emoji": "👀", "to": "jo"}}, true},
		{"a reaction on sam's mail", store.Event{Kind: "reaction", Actor: "jo", Data: map[string]any{"seq": uint64(4), "emoji": "👍", "to": "sam"}}, true},
		{"a reaction between two others", store.Event{Kind: "reaction", Actor: "jo", Data: map[string]any{"seq": uint64(5), "emoji": "👍", "to": "yardmaster"}}, false},
		{"a bead's event", store.Event{Kind: "noted", Actor: "jo", Data: map[string]any{"note": "hi"}}, true},
		{"a hook delivery", store.Event{Kind: "hook", Actor: "hook:linear", Data: map[string]any{"hook": "linear", "text": "hi"}}, true},
	} {
		if _, keep := u.deviceEvent(ctx, sam, tc.e); keep != tc.want {
			t.Errorf("sam is sent %s: %v, want %v", tc.name, keep, tc.want)
		}
	}
}
