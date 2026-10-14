package cli

import (
	"fmt"
	"net"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// hookNow is the time hook listings judge quiet at; tests set it to put
// deliveries in the past.
var hookNow = time.Now

// hookRow is one hook as yardr hook list carries it: the hook, and how
// quiet it is, for one that delivered nothing for longer than its usual
// gap.
type hookRow struct {
	store.Hook
	Quiet *hookQuietJSON `json:"quiet,omitempty"`
}

// hookQuietJSON is a quiet hook's silence and usual gap, in words (span).
type hookQuietJSON struct {
	Silent string `json:"silent"`
	Gap    string `json:"gap"`
}

// span is a silence or a usual gap in words: whole hours under two days,
// whole days past that, rounded to the nearest. Both are over an hour by
// construction (store.QuietGap floors the gap at an hour, and a quiet hook
// is silent for longer).
func span(d time.Duration) string {
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int((d + 30*time.Minute).Hours()))
	}
	return fmt.Sprintf("%dd", int((d+12*time.Hour).Hours()/24))
}

// quietNote is what doctor says of a hook that delivered nothing for longer
// than its usual gap: when it last accepted one, how long that is, and the
// gap. Where the server has an address for webhooks the note probes the
// yard's own listener and says what that means: one that answers leaves the
// break between the source and the yard (the webhook, or the proxy in
// front of the yard), and one that does not answer is the break. Without
// an address the note is the quiet alone: the hooks line beside it says
// what the server takes.
func quietNote(h store.Hook, gap time.Duration, now time.Time, l hooks.Listener) string {
	last := *h.Accepted
	note := fmt.Sprintf("hook %s quiet: last accepted a delivery %s (nothing for %s, usual gap %s)",
		h.Name, since(last), span(now.Sub(last)), span(gap))
	if l.Addr == "" {
		return note
	}
	if l.Error != "" {
		return note + fmt.Sprintf("; the server could not listen on %s: %s", l.Addr, l.Error)
	}
	if answers(loopback(l.Addr)) {
		return note + fmt.Sprintf("; the yard's listener on %s answers, so deliveries stop between the source and the yard: "+
			"the webhook is off there, or the proxy in front of the yard is down", l.Addr)
	}
	return note + fmt.Sprintf("; nothing answers on %s: the yard's own listener is down", l.Addr)
}

// loopback is addr with a wildcard host put on loopback, for a probe from
// this machine: a listener on every interface answers there.
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
