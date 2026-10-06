// Package otel is yardr's OpenTelemetry: the trace context a bead's work
// runs under, and an exporter that turns the event log into OTLP traces and
// logs. It writes OTLP/HTTP JSON itself; there is no SDK behind it.
//
// A bead is a trace, each stay in a stage a span, each session in a stay a
// child span. Every id is derived from what it names (a bead's handle, a
// session's id), so the runner that starts a session and the exporter that
// later sends its span agree on them without talking to each other, and so
// does every other run and machine.
package otel

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// TraceID is the trace of bead, 32 hex digits: the same for a handle
// wherever it is asked.
func TraceID(bead string) string { return derive(16, "yardr bead "+bead) }

// SessionSpanID is the span of a session, 16 hex digits.
func SessionSpanID(session string) string { return derive(8, "yardr session "+session) }

// StaySpanID is the span of a bead's stay in a stage: the stay that began
// with the event seq.
func StaySpanID(bead string, seq uint64) string {
	return derive(8, "yardr stay "+bead+" "+strconv.FormatUint(seq, 10))
}

// BeadSpanID stands for a bead as a whole where a span id is required and
// no one span is meant: in the link from a child to its train's trace. No
// span is sent under it.
func BeadSpanID(bead string) string { return derive(8, "yardr bead span "+bead) }

// derive is the first n bytes of what's hash, in hex. An id of all zeros is
// invalid in a trace context; a hash that came out so gets its last bit set.
func derive(n int, what string) string {
	sum := sha256.Sum256([]byte(what))
	id := sum[:n]
	zero := true
	for _, b := range id {
		zero = zero && b == 0
	}
	if zero {
		id[n-1] = 1
	}
	return hex.EncodeToString(id)
}

// Traceparent is the W3C trace context of a session of bead, as a harness
// reads it from TRACEPARENT: what it sends then lands under the session's
// span. The trace is flagged sampled; whether anything is collected is the
// collector's to say.
func Traceparent(bead, session string) string {
	return "00-" + TraceID(bead) + "-" + SessionSpanID(session) + "-01"
}

// Attr is one resource attribute.
type Attr struct{ Key, Value string }

// ResourceAttributes is attrs as OTEL_RESOURCE_ATTRIBUTES carries them
// (key=value, comma separated, values percent-encoded), after base: what the
// variable already held, kept so an operator's own attributes survive.
// An attribute with no value is left out.
func ResourceAttributes(base string, attrs ...Attr) string {
	parts := []string{}
	if base = strings.Trim(strings.TrimSpace(base), ","); base != "" {
		parts = append(parts, base)
	}
	for _, a := range attrs {
		if a.Value != "" {
			parts = append(parts, a.Key+"="+escape(a.Value))
		}
	}
	return strings.Join(parts, ",")
}

// escape percent-encodes everything in v but the characters a URL leaves
// alone, so a comma, an equals sign or a space cannot end the value early.
func escape(v string) string {
	const hexdigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexdigits[c>>4])
			b.WriteByte(hexdigits[c&15])
		}
	}
	return b.String()
}

// Env is the trace context of a session as environment entries: TRACEPARENT
// and OTEL_RESOURCE_ATTRIBUTES (yardr.bead, yardr.stage, yardr.group and,
// for a bead with a rig, yardr.rig), the latter after base (see
// ResourceAttributes).
func Env(base, bead, session, stage, group, rig string) []string {
	return []string{
		"TRACEPARENT=" + Traceparent(bead, session),
		ResourceVar + "=" + ResourceAttributes(base,
			Attr{"yardr.bead", bead}, Attr{"yardr.stage", stage},
			Attr{"yardr.group", group}, Attr{"yardr.rig", rig}),
	}
}

// ResourceVar is the variable an OpenTelemetry SDK reads resource
// attributes from.
const ResourceVar = "OTEL_RESOURCE_ATTRIBUTES"
