package web_test

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/preview"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// previewFixture serves a yard with the operator ada, whose web view and
// supervisor share the yard's home, as production's do: the page reads
// the log tail where the supervisor writes it.
func previewFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	st, own := storetest.NewOwnedIn(t, home)
	if err := st.SetSetting(context.Background(), store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: st, own: own, ts: ts, home: home}
}

// supervise runs the yard's supervisor against the fixture's store, with
// shortened clocks as the supervisor's own tests run it.
func (f *fixture) supervise(t *testing.T) *preview.Supervisor {
	t.Helper()
	sup := preview.New(f.own, f.home, nil)
	sup.Poll = 10 * time.Millisecond
	sup.BindWait = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sup.Run(ctx)
	return sup
}

func previewPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitPreview(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// gitDepot is a directory with one commit, for a proposal to read its
// depot state from.
func gitDepot(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"},
		{"commit", "-q", "--allow-empty", "-m", "one"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

func python3Available() bool {
	for _, p := range []string{"/usr/local/bin/python3", "/usr/bin/python3", "/opt/homebrew/bin/python3"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	_, err := exec.LookPath("python3")
	return err == nil
}

// A pending proposal appears as one card, in JSON and HTML, showing the
// start command in full, the directory, the port, the depot commit and
// dirty mark, and the proposer. Approving it starts the preview through
// the supervisor; refusing another leaves the row refused with nothing
// started. A view that reads only draws no button and takes no write.
func TestPreviewsPage(t *testing.T) {
	t.Parallel()
	if !python3Available() {
		t.Skip("python3 not available for the stand-in app")
	}
	f := previewFixture(t)
	ctx := context.Background()
	dir, head := gitDepot(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const command = "python3 -m http.server PORT --bind 127.0.0.1"
	port := previewPort(t)
	if _, err := f.st.ProposePreview(ctx, store.NewPreview{
		App: "demo", Command: strings.Replace(command, "PORT", fmt.Sprint(port), 1),
		Dir: dir, Port: port,
	}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ProposePreview(ctx, store.NewPreview{
		App: "other", Command: "serve .", Dir: dir, Port: previewPort(t),
	}, "builder"); err != nil {
		t.Fatal(err)
	}
	acting := f.as(t, "ada")
	var got web.Previews
	acting.getJSON("/previews", &got)
	if got.As != "ada" || len(got.Previews) != 2 {
		t.Fatalf("previews = %+v", got)
	}
	card := got.Previews[0]
	if card.App != "demo" || card.Command != strings.Replace(command, "PORT", fmt.Sprint(port), 1) ||
		card.Dir != dir || card.Port != port || card.Commit != head || !card.Dirty ||
		card.Proposer != "builder" || card.State != store.PreviewPending || card.Head != head || card.Stale {
		t.Fatalf("pending card = %+v", card)
	}
	if got.Idle != "4h" || got.MaxAge != "7d" || got.Head == 0 {
		t.Fatalf("previews lifetime = %q/%q head %d", got.Idle, got.MaxAge, got.Head)
	}
	page := acting.get("/previews").body
	for _, want := range []string{
		card.Command, dir, fmt.Sprint(port), head[:12], "(dirty)", "builder",
		"idle 4h · age 7d", "/preview/demo/", `name="action" value="approve">Approve`,
		`name="action" value="refuse">Refuse`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// A view that reads only draws no button and takes no write.
	plain := f.get("/previews").body
	if strings.Contains(plain, `name="action"`) {
		t.Error("the read-only page draws a button")
	}
	if r := f.do("POST", "/previews"); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST /previews without --as: status %d, want 405", r.status)
	}
	sup := f.supervise(t)
	form := func(app, action string) url.Values { return url.Values{"app": {app}, "action": {action}} }
	if r := acting.postForm("/previews", form("demo", "approve"), "Origin", acting.ts.URL); r.status != http.StatusSeeOther || r.header.Get("Location") != "/previews" {
		t.Fatalf("approve: status %d, location %q", r.status, r.header.Get("Location"))
	}
	waitPreview(t, "approved preview running", func() bool {
		p, err := f.st.GetPreview(ctx, "demo")
		return err == nil && p.State == store.PreviewRunning
	})
	if r := acting.postForm("/previews", form("other", "refuse"), "Origin", acting.ts.URL); r.status != http.StatusSeeOther {
		t.Fatalf("refuse: status %d: %s", r.status, r.body)
	}
	if got, err := f.st.GetPreview(ctx, "other"); err != nil || got.State != store.PreviewRefused || got.Approval != nil {
		t.Fatalf("refused = %+v, %v", got, err)
	}
	if sup.Running("other") {
		t.Error("the refused preview started")
	}
	// One request through the app, so its log has a line to show: the
	// stand-in buffers its startup line, and logs each request.
	waitPreview(t, "app answering", func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	// The running card shows its log tail and its Open, Stop and Restart;
	// the refused card stays listed with no button.
	var running web.Previews
	waitPreview(t, "log tail on the card", func() bool {
		// Decoded fresh: a reused value would keep a field the
		// answer omits.
		var cur web.Previews
		acting.getJSON("/previews", &cur)
		if len(cur.Previews) == 2 && len(cur.Previews[0].Log) > 0 {
			running = cur
			return true
		}
		return false
	})
	page = acting.get("/previews").body
	if tail := template.HTMLEscapeString(running.Previews[0].Log[0]); !strings.Contains(page, tail) {
		t.Errorf("the page does not show the log tail %q", tail)
	}
	for _, want := range []string{`href="/preview/demo/"`, `value="stop">Stop`, `value="restart">Restart`} {
		if !strings.Contains(page, want) {
			t.Errorf("the running card does not show %q", want)
		}
	}
	if refused := running.Previews[1]; refused.App != "other" || refused.State != store.PreviewRefused || len(refused.Log) != 0 {
		t.Fatalf("refused card = %+v", refused)
	}
	// A view that reads only shows the cards without the start command
	// and the log tail, which are the operator's to read: the approval's
	// command with the card's.
	if running.Previews[0].Approval == nil || running.Previews[0].Approval.Command == "" {
		t.Fatalf("the running card carries no approval command: %+v", running.Previews[0])
	}
	var readOnly web.Previews
	f.getJSON("/previews", &readOnly)
	if len(readOnly.Previews) != 2 {
		t.Fatalf("read-only previews = %+v", readOnly)
	}
	for _, c := range readOnly.Previews {
		if c.Command != "" || len(c.Log) != 0 {
			t.Errorf("the read-only card shows the command or the log: %+v", c)
		}
		if c.Approval != nil && c.Approval.Command != "" {
			t.Errorf("the read-only card shows the approval's command: %+v", c.Approval)
		}
	}
	if body := f.get("/previews").body; strings.Contains(body, card.Command) {
		t.Error("the read-only page shows the start command")
	}
}

// A crew session's approval attempt is refused on the page, as the
// store's operator rule refuses it: the page comes back redrawn with
// why, and the proposal stays pending.
func TestPreviewsCrewApprovalRefused(t *testing.T) {
	t.Parallel()
	f := previewFixture(t)
	ctx := context.Background()
	if _, err := f.st.AddUser(ctx, "builder", "ada"); err != nil {
		t.Fatal(err)
	}
	port := previewPort(t)
	if _, err := f.st.ProposePreview(ctx, store.NewPreview{
		App: "demo", Command: "serve .", Dir: t.TempDir(), Port: port,
	}, "builder"); err != nil {
		t.Fatal(err)
	}
	acting := f.as(t, "builder")
	r := acting.postForm("/previews", url.Values{"app": {"demo"}, "action": {"approve"}}, "Origin", acting.ts.URL)
	if r.status != http.StatusOK {
		t.Fatalf("crew approve: status %d, want the redrawn page: %s", r.status, r.body)
	}
	if !strings.Contains(r.body, "may approve a preview") {
		t.Errorf("the redrawn page does not carry the store's refusal: %s", r.body)
	}
	if got, err := f.st.GetPreview(ctx, "demo"); err != nil || got.State != store.PreviewPending {
		t.Fatalf("after crew approve = %+v, %v", got, err)
	}
}

// After the depot HEAD moves past the running commit, the card carries
// the stale mark. Restart does not run the approved command at the new
// commit: the approval was given on the old one, so the page comes back
// with why, the card is pending again at the new commit with its process
// stopped, and the preview runs at that commit once approved anew.
func TestPreviewsStaleAndRestart(t *testing.T) {
	t.Parallel()
	if !python3Available() {
		t.Skip("python3 not available for the stand-in app")
	}
	f := previewFixture(t)
	ctx := context.Background()
	dir, first := gitDepot(t)
	port := previewPort(t)
	if _, err := f.st.ProposePreview(ctx, store.NewPreview{
		App: "demo", Command: fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port),
		Dir: dir, Port: port,
	}, "builder"); err != nil {
		t.Fatal(err)
	}
	acting := f.as(t, "ada")
	sup := f.supervise(t)
	if r := acting.postForm("/previews", url.Values{"app": {"demo"}, "action": {"approve"}}, "Origin", acting.ts.URL); r.status != http.StatusSeeOther {
		t.Fatalf("approve: status %d: %s", r.status, r.body)
	}
	waitPreview(t, "preview running", func() bool {
		p, err := f.st.GetPreview(ctx, "demo")
		return err == nil && p.State == store.PreviewRunning
	})
	if out, err := exec.Command("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", "two").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	second := strings.TrimSpace(string(out))
	var got web.Previews
	acting.getJSON("/previews", &got)
	if len(got.Previews) != 1 || !got.Previews[0].Stale || got.Previews[0].Head != second || got.Previews[0].Commit != first {
		t.Fatalf("stale card = %+v", got.Previews)
	}
	if page := acting.get("/previews").body; !strings.Contains(page, "stale") {
		t.Error("the page does not mark the stale preview")
	}
	r := acting.postForm("/previews", url.Values{"app": {"demo"}, "action": {"restart"}}, "Origin", acting.ts.URL)
	if r.status != http.StatusOK || !strings.Contains(r.body, "approval is taken back") {
		t.Fatalf("restart on the moved depot: status %d, want the page redrawn with why: %s", r.status, r.body)
	}
	if p, err := f.st.GetPreview(ctx, "demo"); err != nil || p.State != store.PreviewPending ||
		p.StateDetail != store.PreviewTreeChangedDetail || p.Approval != nil || p.Commit != second {
		t.Fatalf("after the refused restart = %+v, %v, want pending at %q with no approval", p, err, second)
	}
	// Stopped means gone from the supervisor and the port it held answering
	// no more: the approval given next starts on that port again.
	waitPreview(t, "the taken-back preview's process stopped", func() bool {
		if sup.Running("demo") {
			return false
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), time.Second)
		if err != nil {
			return true
		}
		c.Close()
		return false
	})
	// The pending card is the one the page offers Approve on.
	if page := acting.get("/previews").body; !strings.Contains(page, `value="approve">Approve`) || !strings.Contains(page, store.PreviewTreeChangedDetail) {
		t.Errorf("the page does not offer the taken-back card for approval: %s", page)
	}
	if r := acting.postForm("/previews", url.Values{"app": {"demo"}, "action": {"approve"}}, "Origin", acting.ts.URL); r.status != http.StatusSeeOther {
		t.Fatalf("approve anew: status %d: %s", r.status, r.body)
	}
	waitPreview(t, "start at the new commit", func() bool {
		p, err := f.st.GetPreview(ctx, "demo")
		return err == nil && p.State == store.PreviewRunning && p.Commit == second
	})
	// Decoded fresh: the earlier decode's stale mark must not survive
	// into this answer, which omits it.
	var after web.Previews
	acting.getJSON("/previews", &after)
	if len(after.Previews) != 1 || after.Previews[0].Stale {
		t.Fatalf("after restart = %+v, want no stale mark", after.Previews)
	}
}
