package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// The daemon's pass fires the timers that are due by its clock. One for a
// resident is in its inbox and the resident is nudged in the same pass;
// one a resident set for a user is mail, and nudges nobody.
func TestPassFiresTimers(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)

	if _, err := s.AddTimer(ctx, store.NewTimer{To: "jo", Reason: "web-1 is stuck", Due: now.Add(10 * time.Minute)}, "mayor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTimer(ctx, store.NewTimer{Reason: "look at the deploy", Due: now.Add(20 * time.Minute)}, "mayor"); err != nil {
		t.Fatal(err)
	}
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 0 {
		t.Fatalf("nudged before anything was due: %v", f.nudges)
	}

	// The user's is due: mail for jo, and the resident that set it is left
	// alone.
	now = now.Add(10 * time.Minute)
	must(t, d.Reconcile(ctx))
	box, err := s.UserInbox(ctx, "jo", 0)
	if err != nil || box.Unread != 1 || box.Mail[0].From != "mayor" || !strings.HasPrefix(box.Mail[0].Text, "timer: web-1 is stuck") {
		t.Fatalf("jo's inbox = %+v, %v", box, err)
	}
	if len(f.nudges) != 0 {
		t.Fatalf("a timer for a user nudged a resident: %v", f.nudges)
	}

	// The resident's own is due: in its inbox, and it is nudged for it.
	now = now.Add(10 * time.Minute)
	must(t, d.Reconcile(ctx))
	page, err := s.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 1 || page.Events[0].Kind != "timer" {
		t.Fatalf("mayor's inbox = %+v, %v", page, err)
	}
	if len(f.nudges) != 1 || !strings.HasPrefix(f.nudges[0], agent+": yardr: 1 new in your inbox (timer)") {
		t.Fatalf("nudges = %v", f.nudges)
	}
	must(t, d.Reconcile(ctx))
	if page, _ := s.Inbox(ctx, "mayor", 0); page.Total != 1 || len(f.nudges) != 1 {
		t.Fatalf("fired or nudged again: %d in the inbox, nudges %v", page.Total, f.nudges)
	}
}
