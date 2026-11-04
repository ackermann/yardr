package preview

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// newEdge serves a stand-in edge over plain http on loopback, which the
// supervisor's default client needs no certificate for.
func newEdge(t *testing.T) (*standin.Edge, *httptest.Server) {
	t.Helper()
	edge, err := standin.NewEdge()
	must(t, err)
	srv := httptest.NewServer(edge)
	t.Cleanup(srv.Close)
	return edge, srv
}

// pointAt sets the edge setting as yardr edge connect leaves it.
func (y *yard) pointAt(t *testing.T, edge *standin.Edge, srv *httptest.Server) {
	t.Helper()
	value := store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean", Name: "edgeyard"}.String()
	must(t, y.st.SetSetting(context.Background(), store.EdgeSetting, value, "ada"))
}

// connect has the yard keep the key it signs with and be connected to a new
// stand-in edge.
func (y *yard) connect(t *testing.T) *standin.Edge {
	t.Helper()
	_, _, err := yardkey.Ensure(config.At(y.home).PeerKey())
	must(t, err)
	edge, srv := newEdge(t)
	y.pointAt(t, edge, srv)
	return edge
}

// told lists what the edge has been told, in order: the app, its kind where it
// has one, the state and the port.
func told(edge *standin.Edge) []string {
	var out []string
	for _, r := range edge.State().PreviewReports {
		line := r.App
		if r.Kind != "" {
			line += " " + r.Kind
		}
		out = append(out, fmt.Sprintf("%s %s %d", line, r.State, r.Port))
	}
	return out
}

// update changes the row of app as the supervisor or the store does.
func (y *yard) update(t *testing.T, app string, upd store.PreviewUpdate) {
	t.Helper()
	_, err := y.st.UpdatePreview(context.Background(), app, upd)
	must(t, err)
}

// An app is reported as its row changes: approved, running, stopped, and
// forgotten once the registry has dropped it, each once, with the port it
// binds. The host the edge answers with is kept on the row, and the page told
// of it. A proposal waiting for approval and a crashed preview are not
// reported, and the edge's last word stands.
func TestReportPassTellsTheEdgeOfAnAppAsItsRowChanges(t *testing.T) {
	y := newYard(t)
	edge := y.connect(t)
	ctx := context.Background()
	step := func(what string, want ...string) {
		t.Helper()
		y.sup.reportPass(ctx)
		if got := told(edge); !slices.Equal(got, want) {
			t.Fatalf("%s: the edge was told %q, want %q", what, got, want)
		}
	}
	const host = "p-1." + standin.PreviewBaseDefault

	y.propose(t, "demo", "serve", 8080, false)
	step("a proposal waiting for approval")
	_, err := y.st.ApprovePreview(ctx, "demo", "ada")
	must(t, err)
	step("approved", "demo approved 8080")
	if got := y.state("demo").Host; got != host {
		t.Fatalf("the host kept on the row = %q, want the edge's, %q", got, host)
	}
	step("nothing changed", "demo approved 8080")

	y.update(t, "demo", store.PreviewUpdate{ExpectedState: store.PreviewApproved, State: store.PreviewRunning})
	step("started", "demo approved 8080", "demo running 8080")
	_, err = y.st.PreviewStop(ctx, "demo", "ada")
	must(t, err)
	step("stopped", "demo approved 8080", "demo running 8080", "demo stopped 8080")

	crash := "exit status 1"
	y.update(t, "demo", store.PreviewUpdate{State: store.PreviewCrashed, StateDetail: &crash})
	step("crashed", "demo approved 8080", "demo running 8080", "demo stopped 8080")
	if got := y.state("demo"); got.State != store.PreviewCrashed || got.StateDetail != crash || got.Host != host {
		t.Fatalf("a crashed preview = %+v, want it as it was, with its host", got)
	}

	_, err = y.st.ForgetPreview(ctx, "demo", "ada")
	must(t, err)
	step("forgotten", "demo approved 8080", "demo running 8080", "demo stopped 8080", "demo forgotten 8080")
	step("forgotten, a pass later", "demo approved 8080", "demo running 8080", "demo stopped 8080", "demo forgotten 8080")

	// The page redraws on an event, and the host is kept once.
	evs, err := y.st.EventsOfKind(ctx, "preview_addressed", 10)
	must(t, err)
	if len(evs) != 1 || evs[0].Data["app"] != "demo" || evs[0].Data["detail"] != host {
		t.Fatalf("the events of an address kept = %+v, want one, naming the app and its host", evs)
	}
}

// A watch is reported with its kind and no port, as an app is: running while
// it serves, stopped when it ends, running again at the host it had when it is
// started again, and forgotten when the registry drops it.
func TestReportPassTellsTheEdgeOfAWatchWithNoPort(t *testing.T) {
	y := newYard(t)
	edge := y.connect(t)
	ctx := context.Background()
	p, b, _ := y.watching(t, map[string]string{"index.html": "<body>one</body>"})
	const host = "p-1." + standin.PreviewBaseDefault
	var want []string
	step := func(what, state string) {
		t.Helper()
		y.sup.reportPass(ctx)
		want = append(want, p.App+" watch "+state+" 0")
		if got := told(edge); !slices.Equal(got, want) {
			t.Fatalf("%s: the edge was told %q, want %q", what, got, want)
		}
	}

	step("watched", "running")
	if got := y.state(p.App).Host; got != host {
		t.Fatalf("the host kept on the watch = %q, want %q", got, host)
	}
	_, err := y.st.PreviewStop(ctx, p.App, "ada")
	must(t, err)
	step("ended", "stopped")

	again, err := y.st.WatchPreview(ctx, b.ID, "", "ada")
	must(t, err)
	if again.Host != host {
		t.Fatalf("a watch started again has host %q at once, want the one it had, %q", again.Host, host)
	}
	step("started again", "running")
	if got := y.state(p.App).Host; got != host {
		t.Fatalf("the watch started again has host %q, want the same address, %q", got, host)
	}

	_, err = y.st.PreviewStop(ctx, p.App, "ada")
	must(t, err)
	step("ended again", "stopped")
	_, err = y.st.ForgetPreview(ctx, p.App, "ada")
	must(t, err)
	step("forgotten", "forgotten")
}

// An edge that refuses a report is kept in the state detail, and nothing else
// of the preview changes: the detail is written only where it is empty, so the
// reason a stopped preview holds and the restart that finishRestart waits for
// stand, and what the edge refused is told once, not on every pass. When the
// edge takes the report later the refusal goes and the host comes.
func TestReportPassKeepsARefusalInTheStateDetailAndChangesNothingElse(t *testing.T) {
	y := newYard(t)
	edge := y.connect(t)
	ctx := context.Background()
	const reason = "previews need a yard on its own server: this yard is docker"
	refusal := "the edge refused the report: " + reason + " (403)"
	edge.Behave(func(b *standin.EdgeBehaviour) { b.PreviewRefuse, b.PreviewReason = http.StatusForbidden, reason })
	calls := func() int { return len(edge.State().PreviewReports) }

	y.propose(t, "demo", "serve", 8080, true)
	y.update(t, "demo", store.PreviewUpdate{ExpectedState: store.PreviewApproved, State: store.PreviewRunning})
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.State != store.PreviewRunning || got.StateDetail != refusal || got.Host != "" {
		t.Fatalf("a running preview the edge refused = %s (%s) host %q, want it running, with the refusal and no host", got.State, got.StateDetail, got.Host)
	}
	y.sup.reportPass(ctx)
	if n := calls(); n != 1 {
		t.Fatalf("the edge was told %d times, want once: a refusal waiting does not cure is not asked again", n)
	}

	idle := "idle"
	y.update(t, "demo", store.PreviewUpdate{State: store.PreviewStopped, StateDetail: &idle})
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.State != store.PreviewStopped || got.StateDetail != idle {
		t.Fatalf("a stopped preview the edge refused = %s (%s), want its reason, idle, kept", got.State, got.StateDetail)
	}
	if n := calls(); n != 2 {
		t.Fatalf("the edge was told %d times, want the stop told as well", n)
	}

	restart := store.PreviewRestartDetail
	y.update(t, "demo", store.PreviewUpdate{StateDetail: &restart})
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.StateDetail != restart {
		t.Fatalf("a restart the edge refused to hear of has detail %q, want %q kept for finishRestart", got.StateDetail, restart)
	}
	y.sup.finishRestart(ctx, "demo")
	if got := y.state("demo"); got.State != store.PreviewApproved || got.StateDetail != "" {
		t.Fatalf("after finishRestart = %s (%s), want approved: the refusal changed nothing it waits on", got.State, got.StateDetail)
	}
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.State != store.PreviewApproved || got.StateDetail != refusal {
		t.Fatalf("an approved preview the edge refused = %s (%s), want the refusal", got.State, got.StateDetail)
	}

	// The wipe of a detail by a restart is put back by the next pass, which
	// finds the edge still has the report.
	none := ""
	y.update(t, "demo", store.PreviewUpdate{StateDetail: &none})
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.StateDetail != refusal {
		t.Fatalf("a refusal the row lost = %q, want it put back", got.StateDetail)
	}

	// The link comes up and the edge takes the report.
	edge.Behave(func(b *standin.EdgeBehaviour) { b.PreviewRefuse = 0 })
	y.sup.LinkUp()
	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.State != store.PreviewApproved || got.StateDetail != "" || got.Host != "p-1."+standin.PreviewBaseDefault {
		t.Fatalf("after the edge took the report = %s (%s) host %q, want no refusal and the host", got.State, got.StateDetail, got.Host)
	}
}

// What the edge does not answer is owed: asked again when the retry has
// passed, or the link has come up, and not on every pass meanwhile.
func TestReportPassOwesWhatTheEdgeDoesNotAnswer(t *testing.T) {
	y := newYard(t)
	y.sup.ReportRetry = time.Hour
	edge := y.connect(t)
	ctx := context.Background()
	edge.Behave(func(b *standin.EdgeBehaviour) {
		b.PreviewRefuse, b.PreviewReason = http.StatusServiceUnavailable, "ask again shortly"
	})
	calls := func() int { return len(edge.State().PreviewReports) }
	y.propose(t, "demo", "serve", 8080, true)

	y.sup.reportPass(ctx)
	if got := y.state("demo"); got.StateDetail != "" || got.Host != "" || calls() != 1 {
		t.Fatalf("a report the edge could not take: %d calls, row %+v, want one call and the row as it was", calls(), got)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.PreviewRefuse = 0 })
	y.sup.reportPass(ctx)
	if calls() != 1 {
		t.Fatalf("the edge was asked again at once (%d calls), want it left until the retry has passed", calls())
	}
	y.sup.report.mu.Lock()
	y.sup.report.retryAt = time.Now().Add(-time.Second)
	y.sup.report.mu.Unlock()
	y.sup.reportPass(ctx)
	if got := y.state("demo"); calls() != 2 || got.Host == "" {
		t.Fatalf("after the retry: %d calls, host %q, want the report made again and the host kept", calls(), got.Host)
	}

	// An edge that cannot be reached is owed the same way.
	y2 := newYard(t)
	y2.sup.ReportRetry = time.Hour
	_, _, err := yardkey.Ensure(config.At(y2.home).PeerKey())
	must(t, err)
	gone, srv := newEdge(t)
	srv.Close()
	y2.pointAt(t, gone, srv)
	y2.propose(t, "demo", "serve", 8080, true)
	y2.sup.reportPass(ctx)
	y2.sup.report.mu.Lock()
	owed := y2.sup.report.retryAt.After(time.Now())
	y2.sup.report.mu.Unlock()
	if got := y2.state("demo"); !owed || got.Host != "" || got.StateDetail != "" {
		t.Fatalf("an unreachable edge: owed %v, row %+v, want the report owed and the row as it was", owed, got)
	}
}

// A yard connected to no edge reports nothing, and what an edge it has left
// gave its previews goes: a host nobody answers at, and the refusal of a
// report. The reason a stopped preview holds stays.
func TestReportPassWithNoEdgeReportsNothingAndDropsWhatAnEdgeGave(t *testing.T) {
	y := newYard(t)
	edge, _ := newEdge(t)
	_, _, err := yardkey.Ensure(config.At(y.home).PeerKey())
	must(t, err)
	ctx := context.Background()
	y.propose(t, "demo", "serve", 8080, true)
	y.propose(t, "other", "serve", 8081, true)
	host, refusal, idle := "p-9.preview.example.net", "the edge refused the report: no (403)", "idle"
	y.update(t, "demo", store.PreviewUpdate{Host: &host, StateDetail: &refusal})
	y.update(t, "other", store.PreviewUpdate{Host: &host, State: store.PreviewStopped, StateDetail: &idle})

	y.sup.reportPass(ctx)
	if got := told(edge); len(got) != 0 {
		t.Fatalf("a yard connected to no edge told one: %q", got)
	}
	if got := y.state("demo"); got.Host != "" || got.StateDetail != "" || got.State != store.PreviewApproved {
		t.Fatalf("a preview left by an edge = %s (%s) host %q, want the host and the refusal gone", got.State, got.StateDetail, got.Host)
	}
	if got := y.state("other"); got.Host != "" || got.StateDetail != idle {
		t.Fatalf("a stopped preview left by an edge = (%s) host %q, want its reason, idle, kept and the host gone", got.StateDetail, got.Host)
	}
}

// When the preview link comes up the edge may have lost what it was told, so
// every preview is reported again; and an edge the yard is newly connected to
// is told of them all, and answers its own hosts.
func TestLinkUpReportsEveryPreviewAgain(t *testing.T) {
	y := newYard(t)
	edge := y.connect(t)
	ctx := context.Background()
	y.propose(t, "alpha", "serve", 8080, true)
	y.propose(t, "beta", "serve", 8081, true)
	once := []string{"alpha approved 8080", "beta approved 8081"}

	y.sup.reportPass(ctx)
	y.sup.reportPass(ctx)
	if got := told(edge); !slices.Equal(got, once) {
		t.Fatalf("the edge was told %q, want each preview once", got)
	}
	y.sup.LinkUp()
	y.sup.reportPass(ctx)
	if got := told(edge); !slices.Equal(got, slices.Concat(once, once)) {
		t.Fatalf("after the link came up the edge was told %q, want each preview again", got)
	}
	if a, b := y.state("alpha").Host, y.state("beta").Host; a != "p-1."+standin.PreviewBaseDefault || b != "p-2."+standin.PreviewBaseDefault {
		t.Fatalf("hosts after a second telling = %q, %q, want the ones the edge issued the first time", a, b)
	}

	other, srv := newEdge(t)
	other.Behave(func(b *standin.EdgeBehaviour) { b.PreviewBase = "other.example.net" })
	y.pointAt(t, other, srv)
	y.sup.reportPass(ctx)
	if got := told(other); !slices.Equal(got, once) {
		t.Fatalf("the new edge was told %q, want every preview", got)
	}
	if a, b := y.state("alpha").Host, y.state("beta").Host; a != "p-1.other.example.net" || b != "p-2.other.example.net" {
		t.Fatalf("hosts under the new edge = %q, %q, want its own", a, b)
	}
}

// The reporter runs with the supervisor: a preview that is approved, runs and
// exits is told of, and its host kept, and an edge that refuses the reports
// leaves the preview running and ending as it does with none. Run returns
// once the reporter is done with the store.
func TestRunReportsPreviewsToTheEdgeAndLeavesTheirBehaviourAlone(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%v", refuse), func(t *testing.T) {
			y := newYard(t)
			edge := y.connect(t)
			if refuse {
				edge.Behave(func(b *standin.EdgeBehaviour) {
					b.PreviewRefuse, b.PreviewReason = http.StatusForbidden, "no previews here"
				})
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				y.sup.Run(ctx)
			}()
			defer func() {
				cancel()
				waitFor(t, "Run to return", func() bool {
					select {
					case <-done:
						return true
					default:
						return false
					}
				})
			}()

			y.propose(t, "done", "echo finished; exit 0", freePort(t), true)
			waitFor(t, "the preview to run and exit", func() bool {
				got := y.state("done")
				return got.State == store.PreviewStopped && got.StateDetail == "exited"
			})
			waitFor(t, "the edge to be told", func() bool {
				reports := edge.State().PreviewReports
				return len(reports) > 0 && reports[len(reports)-1].State == store.PreviewStopped
			})
			if refuse {
				// Told of the stop and refused: the row keeps the reason it ended with.
				waitFor(t, "the refusal of the stop to be settled", func() bool {
					y.sup.report.mu.Lock()
					defer y.sup.report.mu.Unlock()
					return y.sup.report.told["done"].report.State == store.PreviewStopped
				})
				if got := y.state("done"); got.Host != "" || got.StateDetail != "exited" {
					t.Fatalf("a refused preview that exited = (%s) host %q, want the reason it exited and no host", got.StateDetail, got.Host)
				}
				return
			}
			waitFor(t, "the host to be kept", func() bool { return strings.HasPrefix(y.state("done").Host, "p-1.") })
		})
	}
}
