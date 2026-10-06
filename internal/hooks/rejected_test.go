package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// rejections are the hook_rejected events as "hook reason status count".
func (r *depot) rejections(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, e := range r.eventsOfKind(t, store.HookRejected) {
		out = append(out, fmt.Sprint(e.Data["hook"], " ", e.Data["reason"], " ", e.Data["status"], " ", e.Data["count"]))
	}
	return out
}

// token posts to the token hook adhoc with the bearer token given.
func (r *depot) token(t *testing.T, bearer, delivery string) int {
	t.Helper()
	hdr := map[string]string{"Yard-Delivery": delivery}
	if bearer != "" {
		hdr["Authorization"] = "Bearer " + bearer
	}
	return r.post(t, "/hooks/adhoc", []byte("backup ok"), hdr)
}

// A delivery the receiver refuses leaves a hook_rejected event, so a hook
// whose source is refused can be told from one whose source sends nothing.
// One name and reason log one event per window: what is refused meanwhile
// is counted and said when the window ends. The event names the hook, the
// reason and the status, and nothing the sender sent.
func TestRefusedDeliveryIsLogged(t *testing.T) {
	r := newDepot(t)
	start := time.Now()
	r.now = start
	const marker = "MARKER-8wkd-must-not-be-logged"
	bad := func() int {
		body := []byte(`{"type":"Comment","note":"` + marker + `"}`)
		return r.post(t, "/hooks/linear", body, map[string]string{
			"Linear-Signature": sign("wrong-"+marker, body), "Linear-Delivery": marker, "X-Note": marker})
	}
	if code := bad(); code != http.StatusUnauthorized {
		t.Fatalf("bad signature answered %d", code)
	}
	want := []string{"linear unauthorized 401 1"}
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after one refusal: %v, want %v", got, want)
	}
	for i := 0; i < 50; i++ {
		r.now = r.now.Add(time.Second)
		bad()
	}
	last := r.now
	r.rc.Flush(false) // the window has not ended
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("50 more within the window logged: %v", got)
	}
	r.now = start.Add(hooks.RejectWindow)
	r.rc.Flush(false)
	want = append(want, "linear unauthorized 401 50")
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after the window: %v, want %v", got, want)
	}
	evs := r.eventsOfKind(t, store.HookRejected)
	if at, _ := time.Parse(time.RFC3339, fmt.Sprint(evs[1].Data["last"])); !at.Equal(last.UTC().Truncate(time.Second)) {
		t.Fatalf("last = %v, want the last refusal at %v", evs[1].Data["last"], last.UTC())
	}
	// Without a flush in between, the refusal that comes after the window
	// says those before it, and itself.
	for i := 0; i < 3; i++ {
		r.now = r.now.Add(time.Second)
		bad()
	}
	r.now = r.now.Add(hooks.RejectWindow)
	bad()
	want = append(want, "linear unauthorized 401 4")
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("a refusal after the window: %v, want %v", got, want)
	}
	// Another reason on the same hook has its own window.
	if code := r.post(t, "/hooks/linear", []byte(strings.Repeat("x", store.MaxHookPayload+1)), nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize answered %d", code)
	}
	want = append(want, "linear too_large 413 1")
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("another reason: %v, want %v", got, want)
	}
	for _, e := range r.eventsOfKind(t, store.HookRejected) {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), marker) || strings.Contains(string(raw), secret) {
			t.Fatalf("hook_rejected carries what was sent, or the secret: %s", raw)
		}
		if e.Actor != "hook:linear" {
			t.Fatalf("actor %q", e.Actor)
		}
	}
}

// What was counted and not yet said is logged when the receiver stops.
func TestRefusalsCountedAreLoggedAtStop(t *testing.T) {
	r := newDepot(t)
	r.now = time.Now()
	for i := 0; i < 3; i++ {
		r.token(t, "", "")
	}
	r.rc.Flush(true)
	want := []string{"adhoc unauthorized 401 1", "adhoc unauthorized 401 2"}
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	r.rc.Flush(true)
	if got := r.rejections(t); len(got) != 2 {
		t.Fatalf("a flush with nothing counted logged: %v", got)
	}
}

// A delivery to a name that is no hook's is logged under that name, cut to
// 64 characters. Only so many such names are told apart at a time: a
// scanner trying names fills neither the log nor the receiver, and does not
// push a hook's own refusals out of sight.
func TestRefusedUnknownHookNamesAreBounded(t *testing.T) {
	r := newDepot(t)
	start := time.Now()
	r.now = start
	long := strings.Repeat("a", 100)
	if code := r.post(t, "/hooks/"+long, []byte("{}"), nil); code != http.StatusNotFound {
		t.Fatalf("unknown hook answered %d", code)
	}
	want := []string{long[:64] + " unknown_hook 404 1"}
	for i := 1; i < hooks.MaxRejectNames; i++ {
		r.post(t, fmt.Sprintf("/hooks/nope-%d", i), []byte("{}"), nil)
		want = append(want, fmt.Sprintf("nope-%d unknown_hook 404 1", i))
	}
	// The 33rd name and those after it are one.
	for i := 0; i < 5; i++ {
		r.post(t, fmt.Sprintf("/hooks/more-%d", i), []byte("{}"), nil)
	}
	want = append(want, hooks.OtherHooks+" unknown_hook 404 1")
	// A name already told apart stays itself, and a hook's name is its own.
	r.post(t, "/hooks/nope-1", []byte("{}"), nil)
	r.token(t, "", "")
	want = append(want, "adhoc unauthorized 401 1")
	if got := r.rejections(t); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, e := range r.eventsOfKind(t, store.HookRejected) {
		if e.Data["hook"] != "adhoc" && e.Actor != "yard" {
			t.Fatalf("a name that is no hook's logged as %q", e.Actor)
		}
	}
	// When the window ends, what was counted is said and the names with
	// nothing counted are forgotten, so a new name is told apart again.
	r.now = start.Add(hooks.RejectWindow)
	r.rc.Flush(false)
	want = append(want, "nope-1 unknown_hook 404 1", hooks.OtherHooks+" unknown_hook 404 4")
	got := r.rejections(t)
	if len(got) != len(want) || !strings.Contains(fmt.Sprint(got[len(got)-2:]), want[len(want)-1]) || !strings.Contains(fmt.Sprint(got[len(got)-2:]), want[len(want)-2]) {
		t.Fatalf("after the window: %v\nwant %v", got, want)
	}
	r.post(t, "/hooks/new-name", []byte("{}"), nil)
	if got := r.rejections(t); got[len(got)-1] != "new-name unknown_hook 404 1" {
		t.Fatalf("a new name after the window: %v", got[len(got)-1])
	}
	// The name is the sender's: nothing of it reaches a terminal as sent.
	r.post(t, "/hooks/a%1b%5b2Jb", []byte("{}"), nil)
	if got := r.rejections(t); got[len(got)-1] != "a?[2Jb unknown_hook 404 1" {
		t.Fatalf("a name with an escape in it: %q", got[len(got)-1])
	}
}

// A duplicate is answered 200 and is no refusal. The hook keeps when it
// last accepted and last refused a delivery, across `hook add` of it again.
func TestHookKeepsLastAcceptedAndRefused(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	r.now = time.Now().Add(-time.Hour)
	h, err := r.st.GetHook(ctx, "adhoc")
	if err != nil || h.Accepted != nil || h.Refused != nil || h.LastRefused() {
		t.Fatalf("a hook sent nothing: %+v %v", h, err)
	}
	if code := r.token(t, "", "d0"); code != http.StatusUnauthorized {
		t.Fatalf("no token answered %d", code)
	}
	h, _ = r.st.GetHook(ctx, "adhoc")
	if h.Refused == nil || !h.Refused.Equal(r.now.UTC().Truncate(time.Microsecond)) || h.RefusedFor != "unauthorized" || !h.LastRefused() {
		t.Fatalf("after a refusal: %+v", h)
	}
	for i := 0; i < 2; i++ {
		if code := r.token(t, secret, "d1"); code != http.StatusOK {
			t.Fatalf("delivery answered %d", code)
		}
	}
	if got := r.rejections(t); len(got) != 1 {
		t.Fatalf("a duplicate was logged as refused: %v", got)
	}
	h, _ = r.st.GetHook(ctx, "adhoc")
	if h.Accepted == nil || time.Since(*h.Accepted) > time.Minute || h.LastRefused() {
		t.Fatalf("after an accepted delivery: %+v", h)
	}
	// No client sets the times, and replacing the hook keeps them.
	forged := time.Now().Add(24 * time.Hour)
	put, err := r.st.PutHook(ctx, store.Hook{Name: "adhoc", Source: "token", SecretFile: h.SecretFile, Accepted: &forged, Refused: &forged, RefusedFor: "forged"}, "me")
	if err != nil || !put.Accepted.Equal(*h.Accepted) || !put.Refused.Equal(*h.Refused) || put.RefusedFor != "unauthorized" {
		t.Fatalf("PutHook: %+v %v, want the times of %+v", put, err, h)
	}
}
