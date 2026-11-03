package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/testload"
)

// A want registered as the refresher's loop leaves still runs (yardr-4n4z):
// the loop clears its flag under the decision's lock, so the registration
// starts a new loop instead of standing with none to run it. The hook runs
// in the old exit gap, where without the fix the want would strand.
func TestLimitsWantRegisteredAsLoopLeavesStillRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	dir := t.TempDir()
	asked, script := filepath.Join(dir, "asked"), filepath.Join(dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$1\" >>"+asked+"\necho '{\"provider\":\"Kimi\",\"windows\":[]}'\n")
	if _, err := s.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\", --json]\n  kinds: { kimi: moonshot }\n", "me"); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{Store: s, Home: dir}
	t.Cleanup(d.stopLimitsRefresher)
	as, err := limits.Answerers(ctx, s, d.Home)
	if err != nil {
		t.Fatal(err)
	}
	a, name, ok := limits.Resolve(as, "kimi", "")
	if !ok {
		t.Fatal("no source answers for kimi")
	}
	want := limitWant{
		key: a.Source + "\x00" + name + "\x00" + limitsEnvScope(nil),
		age: time.Minute, answerer: a, kind: "kimi", name: name,
	}
	hooked := make(chan struct{})
	d.limitsMu.Lock()
	var once bool
	d.limitsIdleHook = func() {
		d.limitsMu.Lock()
		if once {
			d.limitsMu.Unlock()
			return
		}
		once = true
		d.limitsMu.Unlock()
		// Warm, so the loop exits once the run lands and the test leaks
		// no goroutine.
		d.registerLimitWant(want, true)
		close(hooked)
	}
	d.limitsMu.Unlock()
	d.ensureLimitsRefresher(context.Background(), false, nil)
	fuse := testload.Scale(WaitFuse)
	select {
	case <-hooked:
	case <-time.After(fuse):
		t.Fatalf("the loop did not leave within %s", fuse)
	}
	for end := time.Now().Add(fuse); ; time.Sleep(5 * time.Millisecond) {
		b, _ := os.ReadFile(asked)
		if got := strings.Fields(string(b)); len(got) == 1 {
			break
		}
		if time.Now().After(end) {
			d.limitsMu.Lock()
			wants, loop, running := len(d.limitsWants), d.limitsLoop, 0
			for _, w := range d.limitsWants {
				if w.running {
					running++
				}
			}
			d.limitsMu.Unlock()
			b, _ := os.ReadFile(asked)
			t.Fatalf("the provider was run for %q, want once: the want registered as the loop left did not run (wants=%d running=%d loop=%v)",
				strings.Fields(string(b)), wants, running, loop)
		}
	}
	for end := time.Now().Add(fuse); ; time.Sleep(5 * time.Millisecond) {
		d.limitsMu.Lock()
		idle := !d.limitsLoop && len(d.limitsWants) == 0
		d.limitsMu.Unlock()
		if idle {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the refresher did not go idle within %s", fuse)
		}
	}
	d.limitsMu.Lock()
	_, kept := d.limitsAsked[want.key]
	d.limitsMu.Unlock()
	if !kept {
		t.Fatalf("the cache keeps no answer for %q after the run", want.key)
	}
}
