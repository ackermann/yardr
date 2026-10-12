package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
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
	// streamMax is how many streams are open at once from other machines,
	// all together, and again how many from this one: each holds a
	// subscription of the yard and a goroutine here, and the view has no
	// authentication to say whose they are. Loopback is counted apart, so
	// a network that fills its share leaves the pages on this machine
	// their own.
	streamMax = 128
	// streamPerAddr is how many of them one address off loopback holds:
	// more than a browser opens to one host over all its tabs, and few
	// enough that it takes several machines to fill streamMax.
	streamPerAddr = 16
	// streamBusy is the Retry-After of a stream refused for want of a
	// place, in seconds.
	streamBusy = "5"
)

// loopback is the key all of this machine's own clients share in
// Server.streamsBy: 127.0.0.1, ::1 and whatever else is loopback are one.
const loopback = "loopback"

// streamKey is what a stream from remote (a request's RemoteAddr) is
// counted under: loopback, an IPv4 address, or an IPv6 address's /64, since
// one machine has as many addresses in its own /64 as it likes. A header
// that names another address (X-Forwarded-For) is not looked at: whoever
// asks writes it.
func streamKey(remote string) string {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		// No address to tell clients apart by: they share one place.
		return "unknown"
	}
	addr := ap.Addr().Unmap().WithZone("")
	switch {
	case addr.IsLoopback():
		return loopback
	case addr.Is6():
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// enter takes a place for a stream from remote, and leave gives it back.
// With no place left there is no leave, and status is the answer: 429 when
// the address has its share, 503 when the streams of all addresses, or of
// this machine, are as many as are served.
func (s *Server) enter(remote string) (leave func(), status int) {
	key := streamKey(remote)
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if key == loopback {
		if s.streamsBy[key] >= s.streamMax {
			return nil, http.StatusServiceUnavailable
		}
	} else {
		// The address before the total: a client that is told it has too
		// many open can do something about it.
		if s.streamsBy[key] >= s.streamPerAddr {
			return nil, http.StatusTooManyRequests
		}
		if s.streamsOff >= s.streamMax {
			return nil, http.StatusServiceUnavailable
		}
		s.streamsOff++
	}
	s.streamsBy[key]++
	s.streams.Add(1)
	return func() {
		s.streamMu.Lock()
		defer s.streamMu.Unlock()
		if key != loopback {
			s.streamsOff--
		}
		// An address with no stream left is forgotten, or the map would
		// grow with every address that ever asked.
		if s.streamsBy[key]--; s.streamsBy[key] == 0 {
			delete(s.streamsBy, key)
		}
		s.streams.Add(-1)
	}, 0
}

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
// in; home is the entry only for an event home's feed keeps, and the id
// alone for the rest, for the stream home hangs its feed on.
//
// The streams open at once are limited (streamMax, streamPerAddr): one
// past the limit is answered 429 or 503 with Retry-After, before the yard
// is asked anything.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	switch format {
	case "":
		format = "html"
		if wantsJSON(r.Header.Values("Accept")) {
			format = "json"
		}
	case "json", "html", "card", "seq", "home":
	default:
		http.Error(w, "format is json, html, card, seq or home, not "+format, http.StatusBadRequest)
		return
	}
	leave, status := s.enter(r.RemoteAddr)
	if leave == nil {
		why := "too many event streams are open: close a page of the yard, or try again shortly"
		if status == http.StatusTooManyRequests {
			why = "too many event streams are open from this address: close a page of the yard, or try again shortly"
		}
		w.Header().Set("Retry-After", streamBusy)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, why, status)
		return
	}
	defer leave()
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
	// From here on a failing store is not answered with an error: the
	// stream is open, and it goes on after the last event sent when the
	// yardr server is back, or has dropped this subscription as too slow.
	ch := store.Following(ctx, s.st, last, streamRetry)
	for {
		select {
		case <-beat.C:
			if !send([]byte(": still here\n\n")) {
				return
			}
		case e, ok := <-ch:
			if !ok || !send(s.frame(ctx, e, format)) {
				return
			}
		}
	}
}

// frame is e as server-sent events in format (html, json, card, seq or
// home, see handleEvents): the last of them, and only it, has e's seq as
// its id. An event the stream leaves out is its id alone: no message for
// the client, but its cursor passes it.
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
	case format == "home":
		if rd := s.newReader(nil); inFeed(e) && inHomeFeed(ctx, rd, e) {
			var html bytes.Buffer
			if err := s.partials.ExecuteTemplate(&html, "entry", rd.entry(ctx, e)); err != nil {
				s.log.Error("web: /events", "seq", e.Seq, "err", err)
				break
			}
			return message(&b, e.Seq, "", html.Bytes())
		}
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
