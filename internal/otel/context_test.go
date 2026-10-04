package otel

import (
	"regexp"
	"testing"
)

// What shasum -a 256 gives for "yardr bead yardr-n63q", "yardr session s1"
// and "yardr stay yardr-n63q 7", cut to 16 and 8 bytes.
const (
	pinnedTrace   = "0fe518fa23d11c584a608a4329de5a17"
	pinnedSession = "c430dd77738443eb"
	pinnedStay    = "18228cd22f9a7dc0"
)

// The ids are part of what yardr promises: a bead's trace is the same on
// every run and machine, so the values are pinned here, not only compared
// with themselves.
func TestIDsAreDerivedFromWhatTheyName(t *testing.T) {
	for got, want := range map[string]string{
		TraceID("yardr-n63q"):       pinnedTrace,
		SessionSpanID("s1"):         pinnedSession,
		StaySpanID("yardr-n63q", 7): pinnedStay,
	} {
		if got != want {
			t.Errorf("derived id = %q, pinned %q", got, want)
		}
	}
	if TraceID("yardr-n63q") == TraceID("yardr-n63r") || StaySpanID("a", 1) == StaySpanID("a", 2) {
		t.Error("different names gave one id")
	}
	tp := Traceparent("yardr-n63q", "s1")
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(tp) {
		t.Errorf("Traceparent = %q", tp)
	}
	if tp != "00-"+pinnedTrace+"-"+pinnedSession+"-01" {
		t.Errorf("Traceparent = %q does not name the bead's trace and the session's span", tp)
	}
}

func TestResourceAttributes(t *testing.T) {
	got := ResourceAttributes(" team=core, ", Attr{"yardr.bead", "web-3"}, Attr{"yardr.group", "web/builders, a=b"}, Attr{"yardr.rig", ""})
	want := "team=core,yardr.bead=web-3,yardr.group=web%2Fbuilders%2C%20a%3Db"
	if got != want {
		t.Errorf("ResourceAttributes = %q, want %q", got, want)
	}
}

func TestSignalURLAndHeaders(t *testing.T) {
	for _, c := range []struct{ base, own, want string }{
		{"http://c:4318", "", "http://c:4318/v1/traces"},
		{"http://c:4318/", "", "http://c:4318/v1/traces"},
		{"http://c:4318", "http://tempo/x", "http://tempo/x"},
		{"", "", ""},
	} {
		if got := SignalURL(c.base, c.own, "traces"); got != c.want {
			t.Errorf("SignalURL(%q, %q) = %q, want %q", c.base, c.own, got, c.want)
		}
	}
	h, err := ParseHeaders("Authorization=Bearer%20abc, X-Scope-OrgID=1")
	if err != nil || h["Authorization"] != "Bearer abc" || h["X-Scope-OrgID"] != "1" {
		t.Errorf("ParseHeaders = %v, %v", h, err)
	}
	if _, err := ParseHeaders("novalue"); err == nil {
		t.Error("a header with no value was accepted")
	}
}
