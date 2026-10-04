package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

const (
	// streamWrite bounds one write to a stream's client. A client that
	// takes nothing for this long is dropped: it holds a subscription of
	// the yard, and one that still lives reconnects where it stopped.
	streamWrite = 10 * time.Second
	// streamBeat is how often a quiet stream is written to, so a client
	// that went away without closing is found, and nothing between the
	// two takes the silence for a dead connection.
	streamBeat = 20 * time.Second
	// streamRetry is how long a stream waits before it asks the yardr
	// server again after losing it, and what it tells the browser to wait
	// before reconnecting to this one.
	streamRetry = 500 * time.Millisecond
)

// handleEvents streams the yard's events as server-sent events, one per
// event, with the event's seq as the id. It starts after the seq in
// Last-Event-ID (what a browser sends when it reconnects) or in ?after=,
// and from now without either, and from there carries every event once
// and in order, as `yardr watch --after` does.
//
// The data is an HTML fragment, the feed's entry, for htmx to put at the
// top of the feed; an event the feed leaves out is sent as its id alone,
// so the client's cursor still passes it. ?format= asks for something
// else: json (also by an Accept that prefers JSON) is the event itself,
// and none is left out; seq is the seq alone, for a page that reads
// itself again when something happens; card is seq, and before it the
// event's bead as /flows shows it, to be put in the section it now stands
// in.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	switch format {
	case "":
		format = "html"
		if wantsJSON(r.Header.Values("Accept")) {
			format = "json"
		}
	case "json", "html", "card", "seq":
	default:
		http.Error(w, "format is json, html, card or seq, not "+format, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Where the client stopped counts for more than where its URL first
	// told it to start: a reconnecting browser sends both.
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = q.Get("after")
	}
	var last uint64
	if cursor != "" {
		var err error
		if last, err = strconv.ParseUint(cursor, 10, 64); err != nil {
			http.Error(w, "after (or Last-Event-ID) is an event's seq, not "+cursor, http.StatusBadRequest)
			return
		}
	} else {
		var err error
		if last, err = s.st.Head(ctx); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	s.streams.Add(1)
	defer s.streams.Add(-1)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// A proxy in front (nginx) must hand each event on as it comes.
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// send writes one frame and reports whether the client took it.
	send := func(frame []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(s.streamWrite))
		if _, err := w.Write(frame); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(fmt.Appendf(nil, "retry: %d\n\n", streamRetry.Milliseconds())) {
		return
	}
	beat := time.NewTicker(streamBeat)
	defer beat.Stop()
	for ctx.Err() == nil {
		// From here on a failing store is not answered with an error: the
		// stream is open, and the yardr server coming back continues it.
		ch, err := s.st.Subscribe(ctx, last)
		for err == nil {
			select {
			case <-ctx.Done():
				return
			case <-beat.C:
				if !send([]byte(": still here\n\n")) {
					return
				}
				continue
			case e, ok := <-ch:
				if !ok {
					err = errStreamEnded
					continue
				}
				if !send(s.frame(ctx, e, format)) {
					return
				}
				last = e.Seq
			}
		}
		// The yardr server went away, or dropped this subscription as too
		// slow: wait for it and go on after the last event sent.
		select {
		case <-ctx.Done():
		case <-time.After(streamRetry):
		}
	}
}

var errStreamEnded = fmt.Errorf("the subscription ended")

// frame is e as server-sent events in format (html, json, card or seq, see
// handleEvents): the last of them, and only it, has e's seq as its id. An
// event the stream leaves out is its id alone: no message for the client,
// but its cursor passes it.
func (s *Server) frame(ctx context.Context, e store.Event, format string) []byte {
	var b bytes.Buffer
	switch {
	case format == "json":
		// One line: json.Marshal escapes every newline in a string.
		if data, err := json.Marshal(e); err == nil {
			return message(&b, e.Seq, "", data)
		}
	case format == "card":
		if p, ok := s.place(ctx, e); ok {
			s.placed(&b, e.Seq, p)
		}
		fallthrough
	case format == "seq":
		return message(&b, e.Seq, "", strconv.AppendUint(nil, e.Seq, 10))
	case inFeed(e):
		var html bytes.Buffer
		if err := s.partials.ExecuteTemplate(&html, "entry", s.newReader(nil).entry(ctx, e)); err != nil {
			s.log.Error("web: /events", "seq", e.Seq, "err", err)
			break
		}
		return message(&b, e.Seq, "", html.Bytes())
	}
	return message(&b, e.Seq, "", nil)
}

// message writes one server-sent event to b and returns b's bytes. id 0 is
// no id: the client's cursor stays where it was. event "" is an unnamed
// event, a browser's `message`.
func message(b *bytes.Buffer, id uint64, event string, data []byte) []byte {
	if id != 0 {
		fmt.Fprintf(b, "id: %d\n", id)
	}
	if event != "" {
		fmt.Fprintf(b, "event: %s\n", event)
	}
	if data != nil {
		// A message's data ends at a newline, so each line of a fragment
		// is a data line of its own.
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			fmt.Fprintf(b, "data: %s\n", line)
		}
	}
	b.WriteString("\n")
	return b.Bytes()
}
