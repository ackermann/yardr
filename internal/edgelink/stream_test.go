package edgelink

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A live answer its handler flushes goes as the head, then one data frame
// per flushed piece, then end when the handler returns. The edge's end for
// the id cancels the request, which ends the handler there as a closed
// connection does on the listener.
func TestLinkStreamsAFlushedAnswerAndCancelsItOnEnd(t *testing.T) {
	t.Parallel()
	returned := make(chan struct{})
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(returned)
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			rc := http.NewResponseController(w)
			if err := rc.Flush(); err != nil {
				t.Errorf("the link's writer does not flush: %v", err)
				return
			}
			fmt.Fprintln(w, `{"seq":1}`)
			_ = rc.Flush()
			<-r.Context().Done()
		})
	}, nil)
	l := y.next(t)
	f := l.Frame(http.MethodGet, "/user/events?after=0", nil, nil, false)
	head := deliver(t, l, f)
	if head.Status != http.StatusOK || head.Header.Get("Content-Type") != "application/x-ndjson" || len(head.Body) != 0 {
		t.Fatalf("the head is %+v, want 200 ndjson with no first piece", head)
	}
	select {
	case d := <-l.DataEvents():
		if d.ID != f.ID || string(d.Body) != "{\"seq\":1}\n" {
			t.Fatalf("the piece is %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the flushed piece never arrived as data")
	}
	select {
	case <-returned:
		t.Fatal("the handler returned before the edge's end")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.SendEnd(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not return after the edge's end")
	}
	select {
	case id := <-l.EndEvents():
		if id != f.ID {
			t.Fatalf("the end is for %s, want %s", id, f.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end after the handler returned")
	}
}

// A device follows /user/events over the link: the head, then an event
// written after it opened, each as it is written. The edge's end ends the
// yard's handler there, and the yard ends the stream after it.
func TestLinkStreamsUserEventsOverTheLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, own := storetest.NewOwned(t)
	if _, err := own.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := own.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	dev, err := own.AddDevice(ctx, "jo", "footplate", "jo")
	if err != nil {
		t.Fatal(err)
	}
	u := rpc.NewUserServer(own, func(context.Context) string { return "south" }, nil)
	y := linkTo(t, func(l *Link) { l.Handler = u.Handler() }, nil)
	l := y.next(t)
	head, err := own.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := l.Frame(http.MethodGet, "/user/events?after="+strconv.FormatUint(head, 10),
		http.Header{"Authorization": {"Bearer " + dev.Token}}, nil, false)
	if got := deliver(t, l, f); got.Status != http.StatusOK {
		t.Fatalf("the stream opened %+v", got)
	}
	if err := own.LogEvent(ctx, "", "noted", "jo", map[string]any{"bead": "yardr-179d"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-l.DataEvents():
		if d.ID != f.ID || !strings.Contains(string(d.Body), `"kind":"noted"`) {
			t.Fatalf("the piece is %s of %s", d.Body, d.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the event written after the stream opened never arrived as data")
	}
	ectx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.SendEnd(ectx, f.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-l.EndEvents():
		if id != f.ID {
			t.Fatalf("the end is for %s, want %s", id, f.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end after the edge's end: the yard's handler did not return")
	}
}

// A flushed piece larger than one frame follows a response head as data
// frames that fit, instead of being mistaken for an oversized head.
func TestLinkStreamsALargeFlushedPieceAsDataFrames(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", 900<<10)
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			if _, err := w.Write([]byte(body)); err != nil {
				t.Errorf("write the stream piece: %v", err)
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush the stream piece: %v", err)
			}
		})
	}, nil)
	l := y.next(t)
	f := l.Frame(http.MethodGet, "/user/events", nil, nil, false)
	head := deliver(t, l, f)
	if head.Status != http.StatusCreated || len(head.Body) != 0 {
		t.Fatalf("the response head is %+v, want 201 with no body", head)
	}
	var got strings.Builder
	frames := 0
	for got.Len() < len(body) {
		select {
		case d := <-l.DataEvents():
			if d.ID != f.ID {
				t.Fatalf("the data is for %s, want %s", d.ID, f.ID)
			}
			got.Write(d.Body)
			frames++
		case <-time.After(10 * time.Second):
			t.Fatalf("received %d of %d bytes in %d data frames", got.Len(), len(body), frames)
		}
	}
	if got.String() != body || frames < 2 {
		t.Fatalf("received %d bytes in %d data frames, want the %d byte piece split across frames", got.Len(), frames, len(body))
	}
	select {
	case id := <-l.EndEvents():
		if id != f.ID {
			t.Fatalf("the end is for %s, want %s", id, f.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the link never ended the large flushed piece")
	}
}

// The request context ends when its handler returns, as it does on the
// listener. Canceling it also lets the parent link release the child.
func TestLinkCancelsRequestContextWhenHandlerReturns(t *testing.T) {
	t.Parallel()
	contexts := make(chan context.Context, 1)
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			contexts <- r.Context()
			w.WriteHeader(http.StatusNoContent)
		})
	}, nil)
	l := y.next(t)
	f := l.Frame(http.MethodGet, "/user/summary", nil, nil, false)
	if a := deliver(t, l, f); a.Status != http.StatusNoContent {
		t.Fatalf("the request answered %d", a.Status)
	}
	ctx := <-contexts
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the handler's request context stayed live after it returned")
	}
}
