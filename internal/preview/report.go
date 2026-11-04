package preview

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
)

// A yard connected to an edge tells it of its previews, apps and watches, on
// the signed call the edge reads at /preview/report (edgelink.PreviewReport),
// and keeps the host the edge answers with on the preview's row, which is the
// address a person opens it at. The edge's cache of them is disposable and
// this registry is the source, so the supervisor does not report as a preview
// changes, which it could not do for the changes it does not make (approve,
// stop and forget arrive on the socket and write the row themselves): it
// compares the registry with what the edge was last told, and reports the
// difference. A row that is gone is reported forgotten.
//
// The calls are made by a goroutine of their own, woken after every
// reconcile: one call to a slow edge takes seconds, and the reaper and the
// starts must not wait on it.
//
// Only an approved, running or stopped preview is reported: the edge keeps an
// address for those. A pending, refused, crashed or failed one is not, and the
// edge's last word of a preview that comes to be one stands.

const (
	// defaultReportRetry is how long a report the edge did not answer waits
	// before it is made again, where the link coming up does not make it
	// sooner.
	defaultReportRetry = time.Minute

	// refusedDetail begins the state detail that keeps a refusal of the edge's
	// (refusalOf), which is how a person is told why a preview has no address.
	refusedDetail = "the edge refused the report"
)

// answered is what the edge was last told of a preview, and what it answered:
// the host it gave, or the words it refused with.
type answered struct {
	report  edgelink.PreviewReport
	host    string
	refusal string
}

// reporting is what the supervisor holds of its reports to the edge.
type reporting struct {
	mu sync.Mutex
	// told is the last report of each app that the edge took or refused, with
	// its answer. An app is in it until the edge has been told it is forgotten.
	told map[string]answered
	// edge is the value of the edge setting the told reports went to: another
	// edge has been told nothing.
	edge string
	// sweeps counts the times told was thrown away, so that a call that was in
	// flight then is not taken for one the edge has heard since.
	sweeps int
	// retryAt is when the calls the edge did not answer are made again.
	retryAt time.Time
	// warned is the failure last logged, so that an edge which stays away is
	// warned of once and not on every retry.
	warned string

	wake chan struct{}
}

func newReporting() reporting {
	return reporting{told: map[string]answered{}, wake: make(chan struct{}, 1)}
}

// forget throws away what was told, to tell it afresh.
func (r *reporting) forget(edge string) {
	clear(r.told)
	r.edge = edge
	r.sweeps++
	r.retryAt = time.Time{}
}

func (s *Supervisor) reportRetry() time.Duration {
	if s.ReportRetry > 0 {
		return s.ReportRetry
	}
	return defaultReportRetry
}

// LinkUp is told that the preview link came up. The edge may have lost what it
// was told while the link was down, or started afresh, so every preview is
// reported again. It makes no call: the reporter does.
func (s *Supervisor) LinkUp() {
	s.report.mu.Lock()
	s.report.forget(s.report.edge)
	s.report.mu.Unlock()
	s.wakeReport()
}

// wakeReport asks the reporter for a pass; one already asked for will do.
func (s *Supervisor) wakeReport() {
	select {
	case s.report.wake <- struct{}{}:
	default:
	}
}

// keepReported makes a pass for every wake until ctx ends, and one first, since
// a server that has just started has told the edge of nothing.
func (s *Supervisor) keepReported(ctx context.Context) {
	for {
		s.reportPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.report.wake:
		}
	}
}

// reportOf is p as the edge is told of it, and whether the edge is told of its
// state at all. A watch binds no port, so its row has none to report.
func reportOf(p store.Preview) (edgelink.PreviewReport, bool) {
	switch p.State {
	case store.PreviewApproved, store.PreviewRunning, store.PreviewStopped:
		return edgelink.PreviewReport{App: p.App, Port: p.Port, Kind: p.Kind, State: p.State}, true
	}
	return edgelink.PreviewReport{}, false
}

// reportPass brings the edge in line with the registry: each preview whose
// report differs from the one the edge was last told is reported, and each
// the registry no longer holds is reported forgotten. A yard connected to no
// edge reports nothing. What the edge did not answer waits for ReportRetry, or
// for the link to come up. It makes the calls one after another and stops at
// the first the edge did not answer, since the others would not be either.
func (s *Supervisor) reportPass(ctx context.Context) {
	settings, err := s.Store.Settings(ctx)
	if err != nil {
		return
	}
	edge, connected, err := store.EdgeOf(settings)
	if err != nil {
		return // an edge this build does not read: KeepEdge says so
	}
	rows, err := s.Store.Previews(ctx)
	if err != nil {
		return
	}
	r := &s.report
	r.mu.Lock()
	if value := settings[store.EdgeSetting]; r.edge != value {
		r.forget(value)
	}
	sweeps, retryAt := r.sweeps, r.retryAt
	told := maps.Clone(r.told)
	r.mu.Unlock()
	if !connected {
		s.dropAddresses(ctx, rows)
		return
	}

	var work []edgelink.PreviewReport
	seen := make(map[string]bool, len(rows))
	for _, p := range rows {
		seen[p.App] = true
		want, ok := reportOf(p)
		switch a, was := told[p.App]; {
		case !ok:
		case was && a.report == want:
			// The edge has it. The row may have lost what it answered, to a
			// restart that wrote the state detail afresh.
			s.keepAnswer(ctx, p, a)
		default:
			work = append(work, want)
		}
	}
	for app, a := range told {
		if !seen[app] {
			a.report.State = edgelink.PreviewForgotten
			work = append(work, a.report)
		}
	}
	if len(work) == 0 || time.Now().Before(retryAt) {
		return
	}
	slices.SortFunc(work, func(a, b edgelink.PreviewReport) int { return strings.Compare(a.App, b.App) })

	key, err := store.Load(config.At(s.Home).PeerKey())
	if err != nil {
		s.owe(fmt.Errorf("the yard's key is not read: %w", err))
		return
	}
	client := &edgelink.Client{URL: edge.URL, HTTP: s.EdgeHTTP}
	for _, w := range work {
		answer, err := client.PreviewReport(ctx, edge.Key, key, w)
		var refused *edgelink.Refused
		switch {
		case ctx.Err() != nil:
			return
		case err == nil:
			s.answered(ctx, sweeps, answered{report: w, host: answer.Host})
		case errors.As(err, &refused) && refused.Status != http.StatusTooManyRequests && refused.Status < http.StatusInternalServerError:
			// The edge's word that it takes no such report: waiting does not
			// cure it, so the report is told, and asked again only when the
			// preview changes or the link comes up.
			s.answered(ctx, sweeps, answered{report: w, refusal: refusalOf(refused)})
		default:
			s.owe(err)
			return
		}
	}
}

// answered records the edge's answer to a report, unless what was told was
// thrown away while the call was out, and puts it on the preview's row.
func (s *Supervisor) answered(ctx context.Context, sweeps int, a answered) {
	r := &s.report
	r.mu.Lock()
	if r.sweeps == sweeps {
		if a.report.State == edgelink.PreviewForgotten {
			delete(r.told, a.report.App)
		} else {
			r.told[a.report.App] = a
		}
	}
	r.warned = ""
	r.mu.Unlock()
	if a.report.State == edgelink.PreviewForgotten {
		return // the row is gone
	}
	if p, err := s.Store.GetPreview(ctx, a.report.App); err == nil {
		s.keepAnswer(ctx, *p, a)
	}
}

// owe keeps a report the edge did not answer for ReportRetry, and says so once
// for as long as the failure stays the same.
func (s *Supervisor) owe(err error) {
	r := &s.report
	r.mu.Lock()
	r.retryAt = time.Now().Add(s.reportRetry())
	first := r.warned != err.Error()
	r.warned = err.Error()
	r.mu.Unlock()
	if first {
		s.Log.Warn("preview: the edge took no report; trying again", "err", err, "in", s.reportRetry())
	}
}

// refusalOf is the state detail that keeps the edge's refusal: its words, which
// name the page or the backend to change, and the status they came with.
func refusalOf(r *edgelink.Refused) string {
	if r.Reason == "" {
		return fmt.Sprintf("%s (%d)", refusedDetail, r.Status)
	}
	return fmt.Sprintf("%s: %s (%d)", refusedDetail, r.Reason, r.Status)
}

func isRefusal(detail string) bool { return strings.HasPrefix(detail, refusedDetail) }

// keepAnswer puts the edge's answer to a report on the row p: the host it gave,
// or, where it refused, the words in the state detail and no host, since the
// edge stands behind no address of the preview. A row that is no longer what
// was reported is left, for the answer is to another report and the next pass
// makes the one it needs. The detail is written only where it is empty or an
// earlier refusal, never over the reason a stopped preview holds or the
// restart finishRestart waits for, and an earlier refusal goes when the edge
// takes the report.
func (s *Supervisor) keepAnswer(ctx context.Context, p store.Preview, a answered) {
	if want, ok := reportOf(p); !ok || want != a.report {
		return
	}
	host, detail := p.Host, p.StateDetail
	if a.refusal != "" {
		host = ""
		if detail == "" || isRefusal(detail) {
			detail = a.refusal
		}
	} else {
		if a.host != "" {
			host = a.host
		}
		if isRefusal(detail) {
			detail = ""
		}
	}
	s.setAddress(ctx, p, host, detail)
}

// dropAddresses takes the hosts, and the refusals of a report, from rows of a
// yard that is connected to no edge: nobody answers at the one, and the other
// is about a connection that is gone.
func (s *Supervisor) dropAddresses(ctx context.Context, rows []store.Preview) {
	for _, p := range rows {
		detail := p.StateDetail
		if isRefusal(detail) {
			detail = ""
		}
		s.setAddress(ctx, p, "", detail)
	}
}

// setAddress writes the host and state detail the row p ought to hold, if they
// differ from what it does, only while it has the state it was read in, and
// logs an event so that the Previews page redraws with it. A field that is not
// changed is not written, so a change made to it since p was read stands.
func (s *Supervisor) setAddress(ctx context.Context, p store.Preview, host, detail string) {
	upd := store.PreviewUpdate{ExpectedState: p.State}
	if host != p.Host {
		upd.Host = &host
	}
	if detail != p.StateDetail {
		upd.StateDetail = &detail
	}
	if upd.Host == nil && upd.StateDetail == nil {
		return
	}
	if _, err := s.Store.UpdatePreview(ctx, p.App, upd); err != nil {
		s.Log.Debug("preview: the edge's answer is not kept", "app", p.App, "err", err)
		return
	}
	said := host
	if said == "" {
		said = detail
	}
	_ = s.Store.LogPreviewEvent(ctx, p.App, "preview_addressed", said)
}
