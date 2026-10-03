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
// so the client's cursor still passes it. With ?format=json (or an Accept
// that prefers JSON) the data is the event itself, and none is left out.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asJSON := wantsJSON(r.Header.Values("Accept"))
	switch q.Get("format") {
	case "":
	case "json":
		asJSON = true
	case "html":
		asJSON = false
	default:
		http.Error(w, "format is json or html, not "+q.Get("format"), http.StatusBadRequest)
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
				if !send(s.frame(ctx, e, asJSON)) {
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

// frame is e as one server-sent event.
func (s *Server) frame(ctx context.Context, e store.Event, asJSON bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %d\n", e.Seq)
	switch {
	case asJSON:
		// One line: json.Marshal escapes every newline in a string.
		if data, err := json.Marshal(e); err == nil {
			fmt.Fprintf(&b, "data: %s\n", data)
		}
	case inFeed(e):
		var html bytes.Buffer
		if err := s.partials.ExecuteTemplate(&html, "entry", s.newReader(nil).entry(ctx, e)); err != nil {
			s.log.Error("web: /events", "seq", e.Seq, "err", err)
			break
		}
		// A frame's data ends at a newline, so each line of the fragment
		// is a data line of its own.
		for _, line := range strings.Split(strings.TrimSpace(html.String()), "\n") {
			fmt.Fprintf(&b, "data: %s\n", line)
		}
	}
	b.WriteString("\n")
	return b.Bytes()
}
