package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A provider of limits is slow, and may be bounded in how often it answers:
// it is asked where something stopped on a limit, or a person asks, and
// never by a pass that finds nothing stopped. With a source that says the
// limits of the agents' kind, passes that start a session, find it working
// and find it idle with nothing wrong do not run its command once.
func TestPassWithNothingStoppedAsksNoLimits(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	asked, script := filepath.Join(dir, "asked"), filepath.Join(dir, "provider")
	must(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" >>"+asked+"\necho '{\"provider\":\"Kimi\",\"windows\":[]}'\n"), 0o700))
	_, err := s.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+", \"{provider}\", --json]\n  kinds: { kimi: kimi }\n", "me")
	must(t, err)

	r := &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 2, Config: map[string]any{"kind": "kimi"}}))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	must(t, err)
	for _, title := range []string{"x", "y"} {
		_, err := s.CreateBead(ctx, store.NewBead{Title: title}, "me")
		must(t, err)
	}
	must(t, d.Reconcile(ctx)) // both start
	must(t, d.Reconcile(ctx)) // seen working
	if n := len(r.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	r.show("idle", "› Ask Kimi to do anything")
	must(t, d.Reconcile(ctx)) // idle, and stopped on nothing
	must(t, d.Reconcile(ctx))
	// A daemon that has just started, too.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	must(t, d2.Reconcile(ctx))
	if b, err := os.ReadFile(asked); err == nil {
		t.Fatalf("the limits command was run by a pass with nothing stopped, for: %q", b)
	}
}
