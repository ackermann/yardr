package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// tokenHandle is a session handle carrying the usage record RecordUsage
// keeps: the five counts, with partial when the reading falls short.
func tokenHandle(fresh, out, read, write int, partial bool) map[string]any {
	rec := map[string]any{"input": fresh + read, "fresh_input": fresh,
		"output": out, "cache_read": read, "cache_write": write}
	if partial {
		rec["partial"] = true
	}
	return map[string]any{"usage": rec}
}

// The bead's tokens line sums its agent sessions in start order: the
// new total and the cache reads over the counted sessions, then one
// entry per session. A total with a partial, missing or still unread
// session takes ≥; a session with no kind never has a count due and
// takes no part, and with no agent session there is no line.
func TestSummarizeBeadTokens(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)
	muse := tokenHandle(800, 20, 31000, 7, false)                  // 827 new
	codex := tokenHandle(600000, 30000, 12000000, 10000, false)    // 640k new
	sec := tokenHandle(70000000, 500000, 900000000, 100000, false) // 70.6M new
	sess := func(id, stage, kind string, off time.Duration, handle map[string]any) store.Session {
		return store.Session{ID: id, Stage: stage, Kind: kind,
			StartedAt: at.Add(off), Handle: handle}
	}
	for _, tc := range []struct {
		name     string
		sessions []store.Session
		want     string // "" wants no line
	}{
		{name: "none", want: ""},
		{name: "kindless only",
			sessions: []store.Session{sess("m1", "decide", "", 0, nil)},
			want:     ""},
		{name: "three counted",
			sessions: []store.Session{
				sess("s1", "new", "muse", 0, muse),
				sess("s2", "review", "codex", time.Minute, codex),
				sess("s3", "review-security", "muse", 2*time.Minute, sec),
			},
			want: "tokens 71.2M new, 912M cache read · new muse 827 · review codex 640k · review-security muse 70.6M"},
		{name: "start order, not given order",
			sessions: []store.Session{
				sess("s3", "review-security", "muse", 2*time.Minute, sec),
				sess("s1", "new", "muse", 0, muse),
			},
			want: "tokens 70.6M new, 900M cache read · new muse 827 · review-security muse 70.6M"},
		{name: "counted and missing",
			sessions: []store.Session{
				sess("s1", "new", "muse", 0, muse),
				sess("s2", "review", "codex", time.Minute,
					map[string]any{"usage": map[string]any{"missing": "no file"}}),
			},
			want: "tokens ≥827 new, 31k cache read · new muse 827 · review codex missing (no file)"},
		{name: "counted and partial",
			sessions: []store.Session{
				sess("s2", "review", "codex", time.Minute, codex),
				sess("s3", "review-security", "muse", 2*time.Minute,
					tokenHandle(70000000, 500000, 900000000, 100000, true)),
			},
			want: "tokens ≥71.2M new, 912M cache read · review codex 640k · review-security muse 70.6M partial"},
		{name: "counted and pending",
			sessions: []store.Session{
				sess("s1", "new", "muse", 0, muse),
				sess("s2", "review", "codex", time.Minute, nil),
			},
			want: "tokens ≥827 new, 31k cache read · new muse 827 · review codex PENDING"},
		{name: "pending only",
			sessions: []store.Session{
				sess("s1", "new", "muse", 0, nil),
			},
			want: "tokens ≥0 new, 0 cache read · new muse PENDING"},
		{name: "kindless takes no part",
			sessions: []store.Session{
				sess("s1", "new", "muse", 0, muse),
				sess("m1", "decide", "", time.Minute, nil),
			},
			want: "tokens 827 new, 31k cache read · new muse 827"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toks := summarizeBeadTokens(tc.sessions)
			if tc.want == "" {
				if toks != nil {
					t.Fatalf("summarize = %+v, want none", toks)
				}
				return
			}
			if toks == nil {
				t.Fatalf("summarize = none, want %q", tc.want)
			}
			if got := toks.line(); got != tc.want {
				t.Errorf("line = %q, want %q", got, tc.want)
			}
		})
	}
}

// Bead show prints the tokens line after labels: three counted
// sessions, the ≥ line for one counted and one missing, a PENDING
// entry for a claimed session, and no line for a bead with none or
// with only a manual session; --json carries the same.
func TestBeadShowTokens(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "muse"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "codex"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "sec", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "muse"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"))
	count := func(fresh, out, read, write int) map[string]any {
		return map[string]any{"input": fresh + read, "fresh_input": fresh,
			"output": out, "cache_read": read, "cache_write": write}
	}
	work := func(bead, group, stage string, usage map[string]any) {
		t.Helper()
		s, err := st.Claim(ctx, bead, group, stage, "me")
		must(err)
		if usage != nil {
			must(st.RecordUsage(ctx, s.ID, usage))
		}
	}
	adv := func(bead, stage string) {
		t.Helper()
		_, err := st.Advance(ctx, bead, stage, store.Expect{}, "me")
		must(err)
	}
	mkbead := func(id string) {
		t.Helper()
		_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "yard", Labels: []string{"difficulty:1"}}, "me")
		must(err)
	}
	mkbead("yard-a")
	work("yard-a", "builders", "new", count(500000, 80000, 23500000, 12000))
	adv("yard-a", "review")
	work("yard-a", "reviewers", "review", count(200000, 30000, 5200000, 5000))
	adv("yard-a", "review-security")
	work("yard-a", "sec", "review-security", count(190000, 35000, 4300000, 6000))
	mkbead("yard-b")
	work("yard-b", "builders", "new", count(500000, 80000, 23500000, 12000))
	adv("yard-b", "review")
	work("yard-b", "reviewers", "review", map[string]any{"missing": "no file"})
	mkbead("yard-c")
	work("yard-c", "builders", "new", nil)
	mkbead("yard-d")
	mkbead("yard-e")
	work("yard-e", "people", "new", nil)

	a := &app{st: st, actor: "me"}
	for bead, want := range map[string]string{
		"yard-a": "  tokens 1.1M new, 33M cache read · new muse 592k · review codex 235k · review-security muse 231k",
		"yard-b": "  tokens ≥592k new, 23.5M cache read · new muse 592k · review codex missing (no file)",
		"yard-c": "  tokens ≥0 new, 0 cache read · new muse PENDING",
	} {
		out, err := run(t, a.beadCmd(), "show", bead)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, want+"\n") {
			t.Errorf("bead show %s lacks %q:\n%s", bead, want, out)
		}
		if li, ti := strings.Index(out, "  labels "), strings.Index(out, "  tokens "); li < 0 || ti < li {
			t.Errorf("bead show %s: tokens line not after labels:\n%s", bead, out)
		}
	}
	for _, bead := range []string{"yard-d", "yard-e"} {
		out, err := run(t, a.beadCmd(), "show", bead)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "  tokens ") {
			t.Errorf("bead show %s prints a tokens line, want none:\n%s", bead, out)
		}
	}

	j := &app{st: st, actor: "me", json: true}
	shown := func(bead string) *beadTokens {
		t.Helper()
		out, err := run(t, j.beadCmd(), "show", bead)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tokens *beadTokens `json:"tokens"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("bead show --json %s = %q: %v", bead, out, err)
		}
		return got.Tokens
	}
	toks := shown("yard-a")
	if toks == nil {
		t.Fatalf("bead show --json yard-a has no tokens")
	}
	if toks.Tokens != 1058000 {
		t.Errorf("--json tokens = %d, want 1058000", toks.Tokens)
	}
	if u := toks.Usage; u.Input != 33890000 || u.FreshInput != 890000 || u.Output != 145000 || u.CacheRead != 33000000 || u.CacheWrite != 23000 {
		t.Errorf("--json usage = %+v, want the counts summed over the three sessions", u)
	}
	if toks.Partial || toks.Uncounted != 0 || toks.Pending != 0 {
		t.Errorf("--json flags = partial %v uncounted %d pending %d, want false 0 0", toks.Partial, toks.Uncounted, toks.Pending)
	}
	if len(toks.Sessions) != 3 {
		t.Fatalf("--json sessions = %+v, want three", toks.Sessions)
	}
	for i, w := range []beadSessionTokens{
		{Stage: "new", Kind: "muse", Tokens: 592000},
		{Stage: "review", Kind: "codex", Tokens: 235000},
		{Stage: "review-security", Kind: "muse", Tokens: 231000},
	} {
		g := toks.Sessions[i]
		if g.Stage != w.Stage || g.Kind != w.Kind || g.Tokens != w.Tokens || g.Session == "" {
			t.Errorf("--json sessions[%d] = %+v, want %s %s %d", i, g, w.Stage, w.Kind, w.Tokens)
		}
	}
	toks = shown("yard-b")
	if toks == nil || toks.Tokens != 592000 || toks.Uncounted != 1 || toks.Pending != 0 {
		t.Errorf("--json yard-b = %+v, want 592000 tokens with 1 uncounted", toks)
	}
	if len(toks.Sessions) != 2 || toks.Sessions[1].Missing != "no file" {
		t.Errorf("--json yard-b sessions = %+v, want the missing reason on the second", toks.Sessions)
	}
	toks = shown("yard-c")
	if toks == nil || toks.Tokens != 0 || toks.Pending != 1 || len(toks.Sessions) != 1 || !toks.Sessions[0].Pending {
		t.Errorf("--json yard-c = %+v, want 0 tokens with 1 pending", toks)
	}
	for _, bead := range []string{"yard-d", "yard-e"} {
		if toks := shown(bead); toks != nil {
			t.Errorf("--json %s tokens = %+v, want absent", bead, toks)
		}
	}

	help, err := run(t, a.beadCmd(), "show", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tokens", "PENDING", "missing"} {
		if !strings.Contains(help, want) {
			t.Errorf("bead show help lacks %q:\n%s", want, help)
		}
	}
}
