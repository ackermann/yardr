package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
)

// sayLink has cfg's home hold s as the link a running server said it is: the
// state, and a pidfile naming the process that wrote it, this test's.
func sayLink(t *testing.T, cfg *config.Config, s edgelink.State) {
	t.Helper()
	if err := edgelink.WriteState(cfg, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// yardr edge status, yardr status and yardr doctor show the link as the
// server said it is: up and since when, with how often it came up again;
// down with its reason and what to do about one that does not mend on its
// own; and that the server has said nothing when it has not.
func TestTheLinkIsShownWhereTheListenerIs(t *testing.T) {
	t.Parallel()
	st, _, srv := edgeYard(t)
	nothing := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("no network in this test")
	})}
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	cfg := config.At(yardEnv(t, st)["YARDR_HOME"])
	ctx := context.Background()

	doctorNote := func() string {
		t.Helper()
		parts, err := optionalParts(ctx, cfg, st, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			for _, n := range p.Notes {
				if p.Key == "hooks" && strings.HasPrefix(n, "the link to the edge ") {
					return n
				}
			}
		}
		t.Fatalf("doctor's hooks part says nothing of the link: %+v", parts)
		return ""
	}
	statusLines := func(s *edgelink.State) []string {
		r := &statusReport{Home: cfg.Home, Edge: &edgeStatus{URL: srv.URL, Handle: "mckean", Link: s}}
		for _, p := range r.parts() {
			if p.name == "edge" {
				return p.lines
			}
		}
		t.Fatal("status has no edge part")
		return nil
	}

	silent := "the server has not said how its link is: it dials the edge while it runs with the edge set"
	out, _ := mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out, "link      "+silent)
	if n := doctorNote(); n != "the link to the edge "+srv.URL+", as handle mckean: "+silent {
		t.Errorf("doctor before the server said = %q", n)
	}

	since := time.Date(2026, 10, 20, 9, 12, 0, 0, time.Local)
	up := edgelink.State{URL: srv.URL, Handle: "mckean", Up: true, Since: since, Ups: 4}
	sayLink(t, cfg, up)
	upLine := "up since 10-20 09:12 (3 reconnects since the server started)"
	out, _ = mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out, "link      "+upLine)
	if n := doctorNote(); !strings.HasSuffix(n, ": "+upLine) {
		t.Errorf("doctor with the link up = %q", n)
	}
	if got := statusLines(&up); len(got) != 2 || got[0] != srv.URL+" as handle mckean" || got[1] != "link "+upLine {
		t.Errorf("status with the link up = %q", got)
	}
	out, _ = mustEdge(t, st, nothing, "edge", "status", "--json")
	var view edgeView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.Link == nil || !view.Link.Up || view.Link.Ups != 4 {
		t.Errorf("edge status --json = %s (%v)", out, err)
	}

	down := edgelink.State{URL: srv.URL, Handle: "mckean", Since: since, Reason: edgelink.DownRefusedKey, Detail: "the challenge is not signed by the edge's key this yard pinned, over this link's nonces", Ups: 1}
	sayLink(t, cfg, down)
	out, _ = mustEdge(t, st, nothing, "edge", "status")
	if !strings.Contains(out, "link      down since 10-20 09:12: refused key (the challenge is not signed by the edge's key this yard pinned") ||
		!strings.Contains(out, ". If the edge's key changed on purpose, ") || !strings.Contains(out, " edge disconnect and connect again\n") {
		t.Errorf("edge status with the key refused:\n%s", out)
	}
	pings := edgelink.State{Since: since, Reason: edgelink.DownPings, Detail: "nothing heard from the edge for 1m15s"}
	if got := statusLines(&pings); got[1] != "link down since 10-20 09:12: unanswered pings (nothing heard from the edge for 1m15s); the server dials again on its own" {
		t.Errorf("status with the link down = %q", got)
	}
	if got := statusLines(nil); got[1] != "link "+silent {
		t.Errorf("status before the server said = %q", got)
	}

	// A yard with no edge has no edge part, and no link in doctor.
	mustEdge(t, st, srv.Client(), "edge", "disconnect")
	parts, err := optionalParts(ctx, cfg, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		for _, n := range p.Notes {
			if strings.Contains(n, "link to the edge") {
				t.Errorf("doctor on a disconnected yard: %q", n)
			}
		}
	}
	if all, _ := st.Settings(ctx); all[store.EdgeSetting] != "" {
		t.Fatal("still connected")
	}
}
