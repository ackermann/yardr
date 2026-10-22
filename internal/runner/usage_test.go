package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
)

// Usage reads what a session used from its agent's own session file, which the
// id kept on the handle names, and asks herdr nothing. The lines are trimmed
// from real files: the builder of yardr-mnc8 (Claude Code 2.1.288), its review
// (codex-cli 0.160.1) and the review-security of yardr-58xw (Muse Code 1.4.11).
// The figures are the ones internal/agents reads off the same lines.
func TestHerdrUsageReadsTheSessionsCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	claude, codex, muse := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path string, lines ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Three messages: the first has one line, the second two and the third
	// three, and every line repeats the usage of its message.
	const brief = `{"type":"user","timestamp":"2026-10-21T19:34:43.883Z","message":{"role":"user","content":"(the brief)"}}`
	messages := []string{
		`{"type":"assistant","timestamp":"2026-10-21T19:34:48.246Z","message":{"id":"msg_011CgFweBRXKGoR2hEx6yoiJ","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":19126,"cache_read_input_tokens":27018,"output_tokens":107,"output_tokens_details":{"thinking_tokens":0}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:52.398Z","message":{"id":"msg_011CgFweY4kkJxVFnA6eEYRm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":13583,"cache_read_input_tokens":46144,"output_tokens":168,"output_tokens_details":{"thinking_tokens":19}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:53.257Z","message":{"id":"msg_011CgFweY4kkJxVFnA6eEYRm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":13583,"cache_read_input_tokens":46144,"output_tokens":168,"output_tokens_details":{"thinking_tokens":19}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:55.873Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:56.499Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:57.579Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
	}
	// The last line of that file, whole: Claude Code's own totals as it exited.
	const costState = `{"type":"cost-state","sessionId":"4b176be6-9160-4463-9056-17f3f0c8a34f","totalCostUSD":8.198233400000003,"totalAPIDuration":901193,"totalAPIDurationWithoutRetries":898403,"totalToolDuration":332809,"totalLinesAdded":474,"totalLinesRemoved":0,"totalDuration":2155171,"startTime":1792611279681,"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":960,"outputTokens":23,"thinkingTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0.001075},"claude-opus-5-5":{"inputTokens":752,"outputTokens":74978,"thinkingTokens":17031,"cacheReadInputTokens":23562912,"cacheCreationInputTokens":247751,"webSearchRequests":0,"costUSD":8.197158400000003}},"hasUnknownModelCost":false}`
	// A message with no time to place it by.
	const untimed = `{"type":"assistant","message":{"id":"msg_untimed","model":"claude-opus-5-5","usage":{"input_tokens":1,"cache_creation_input_tokens":5,"cache_read_input_tokens":100,"output_tokens":10}}}`
	// c1 ended and wrote its total, c2 is a session whose process never did,
	// c3 has not answered, and c5 holds a message with no time.
	write(filepath.Join(claude, "projects", "-tmp-w", "c1.jsonl"), append(append([]string{brief}, messages...), costState)...)
	write(filepath.Join(claude, "projects", "-tmp-w", "c2.jsonl"), append([]string{brief}, messages...)...)
	write(filepath.Join(claude, "projects", "-tmp-w", "c3.jsonl"), brief)
	write(filepath.Join(claude, "projects", "-tmp-w", "c5.jsonl"), brief, messages[0], untimed)
	// c6 is a file that opens and cannot be read.
	if err := os.MkdirAll(filepath.Join(claude, "projects", "-tmp-w", "c6.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}

	// The review: lines that are no count, then the session's first, 27th and
	// last token_count, each the running total of the session.
	taskStarted := `{"timestamp":"2026-10-21T20:10:45.638Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started"}}`
	turnContext := `{"timestamp":"2026-10-21T20:10:48.457Z","ordinal":7,"type":"turn_context","payload":{"model":"gpt-6-luna"}}`
	tokenCounts := []string{
		`{"timestamp":"2026-10-21T20:10:55.310Z","ordinal":21,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":18104,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":368,"reasoning_output_tokens":62,"total_tokens":18472},"last_token_usage":{"input_tokens":18104,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":368,"reasoning_output_tokens":62,"total_tokens":18472},"model_context_window":258400}}}`,
		`{"timestamp":"2026-10-21T20:14:55.079Z","ordinal":291,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2879571,"cached_input_tokens":2709248,"cache_write_input_tokens":0,"output_tokens":25036,"reasoning_output_tokens":17421,"total_tokens":2904607},"last_token_usage":{"input_tokens":147789,"cached_input_tokens":146688,"cache_write_input_tokens":0,"output_tokens":450,"reasoning_output_tokens":211,"total_tokens":148239},"model_context_window":258400}}}`,
		`{"timestamp":"2026-10-21T20:34:17.164Z","ordinal":681,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":11714314,"cached_input_tokens":11487872,"cache_write_input_tokens":0,"output_tokens":45824,"reasoning_output_tokens":33352,"total_tokens":11760138},"last_token_usage":{"input_tokens":194899,"cached_input_tokens":193408,"cache_write_input_tokens":0,"output_tokens":114,"reasoning_output_tokens":33,"total_tokens":195013},"model_context_window":258400}}}`,
	}
	write(filepath.Join(codex, "sessions", "2026", "10", "21", "rollout-2026-10-21T20-10-45-x1.jsonl"),
		append([]string{taskStarted, turnContext}, tokenCounts...)...)
	write(filepath.Join(codex, "sessions", "2026", "10", "21", "rollout-2026-10-21T20-10-46-x2.jsonl"), taskStarted, turnContext)
	// A file an agent left running is still writing to can end in the middle
	// of a line (x4), or in a line whose newline is not written yet (x3).
	sessions := filepath.Join(codex, "sessions", "2026", "10", "21")
	written := strings.Join(append([]string{taskStarted, turnContext}, tokenCounts...), "\n")
	for name, body := range map[string]string{
		"rollout-2026-10-21T20-10-47-x3.jsonl": written,
		"rollout-2026-10-21T20-10-48-x4.jsonl": written + "\n" + `{"timestamp":"2026-10-21T20:40:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":99`,
	} {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Records of Muse's session, in the order of its file: calls of the main
	// agent and of a side session (reminder), a tool record that holds zeros,
	// and a model_completed line that carries the first call's tokens a second
	// time and is no reading. Times are in microseconds (recordedAt).
	museLines := []string{
		`{"sequence":62,"recorded_at":1792611402569623,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"provider","quantity":{"unit":"tokens","reported":true,"input_tokens":27979,"output_tokens":111,"cached_tokens":0,"reasoning_tokens":6,"main_llm_steps":1},"owner":{"requester_kind":"main"}}}}}`,
		`{"sequence":63,"recorded_at":1792611402574676,"payload":{"event":{"kind":"model_completed","usage":{"input_tokens":27979,"output_tokens":111,"cached_tokens":0,"cache_write_tokens":0,"cache_read_tokens":0,"reasoning_tokens":6},"finish_reason":"tool_calls","model":"muse-spark-1.3-contributor","estimated_prompt_tokens":28257}}}`,
		`{"sequence":76,"recorded_at":1792611402731623,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"tool","quantity":{"unit":"tokens","reported":false,"input_tokens":0,"output_tokens":0,"cached_tokens":0,"reasoning_tokens":0,"main_llm_steps":0},"owner":{"requester_kind":"main"}}}}}`,
		`{"sequence":94,"recorded_at":1792611406610256,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"provider","quantity":{"unit":"tokens","reported":true,"input_tokens":36689,"output_tokens":182,"cached_tokens":27633,"reasoning_tokens":54,"main_llm_steps":1},"owner":{"requester_kind":"main"}}}}}`,
		`{"sequence":151,"recorded_at":1792611410214922,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"provider","quantity":{"unit":"tokens","reported":true,"input_tokens":15467,"output_tokens":461,"cached_tokens":0,"reasoning_tokens":285,"main_llm_steps":0},"owner":{"requester_kind":"reminder"}}}}}`,
		`{"sequence":174,"recorded_at":1792611414888354,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"provider","quantity":{"unit":"tokens","reported":true,"input_tokens":44852,"output_tokens":89,"cached_tokens":44273,"reasoning_tokens":0,"main_llm_steps":1},"owner":{"requester_kind":"main"}}}}}`,
	}
	recordedAt := func(us int64) string { return time.UnixMicro(us).UTC().Format(time.RFC3339Nano) }
	write(filepath.Join(muse, "sessions", "2026", "10", "21", "m1", "session.jsonl"), museLines...)

	h := &Herdr{AgentHomes: map[string]string{"claude": claude, "codex": codex, "muse": muse}}
	handle := func(kind, id, until string) map[string]any {
		hd := map[string]any{"kind": kind, "agent_session": id}
		if until != "" {
			hd["count_due"] = map[string]any{"stopped_at": "2026-10-21T20:34:17Z", "until": until}
		}
		return hd
	}
	// A cutoff of any value, as a writer's bug might leave one.
	cutoff := func(until any) map[string]any {
		hd := handle("codex", "x1", "")
		hd["count_due"] = map[string]any{"until": until}
		return hd
	}

	var (
		// Claude Code's totals, Haiku's side calls added to the main model's.
		costStateUsage = agents.Usage{Input: 752 + 960, FreshInput: 752 + 960, Output: 74978 + 23, CacheRead: 23562912, CacheWrite: 247751}
		// The three messages once each, not the six lines.
		allMessages = agents.Usage{Input: 6, FreshInput: 6, Output: 614, CacheRead: 132889, CacheWrite: 34511}
		// The first two, the second by its first line: all that is at or
		// before 19:34:53.
		firstTwo  = agents.Usage{Input: 4, FreshInput: 4, Output: 275, CacheRead: 73162, CacheWrite: 32709}
		lastCount = agents.Usage{Input: 11714314, FreshInput: 11714314 - 11487872, Output: 45824, CacheRead: 11487872}
		midCount  = agents.Usage{Input: 2879571, FreshInput: 2879571 - 2709248, Output: 25036, CacheRead: 2709248}
		// Muse reports its input with the cache in it; the side session's
		// call is counted, the tool record and the model_completed line add
		// nothing.
		allRecords    = agents.Usage{Input: 27979 + 36689 + 15467 + 44852, FreshInput: 27979 + (36689 - 27633) + 15467 + (44852 - 44273), Output: 111 + 182 + 461 + 89, CacheRead: 27633 + 44273}
		throughSecond = agents.Usage{Input: 27979 + 36689, FreshInput: 27979 + (36689 - 27633), Output: 111 + 182, CacheRead: 27633}
		firstOnly     = agents.Usage{Input: 27979, FreshInput: 27979, Output: 111}
	)
	// The words the record keeps and the report prints.
	const (
		noID     = "no session id"
		noFile   = "no file"
		noLine   = "no usage line"
		noSource = "kind declares no usage"
	)
	for _, c := range []struct {
		name     string
		handle   map[string]any
		fallback bool
		usage    agents.Usage
		partial  bool
		missing  string // why no count is there, "" for a count
		notYet   bool   // ErrUsageNotYet, for the caller to ask again
		fails    bool   // any other error, for the caller to try again too
	}{
		// Claude: the total Claude Code wrote as it exited is the reading,
		// whether or not the caller allows the fallback.
		{name: "claude total", handle: handle("claude", "c1", ""), usage: costStateUsage},
		{name: "claude total, fallback allowed", handle: handle("claude", "c1", ""), fallback: true, usage: costStateUsage},
		// No total yet: the caller waits, and after that allows the lines of
		// its messages, each counted once, which fall short of the session.
		{name: "claude running", handle: handle("claude", "c2", ""), notYet: true},
		{name: "claude running, fallback allowed", handle: handle("claude", "c2", ""), fallback: true, usage: allMessages, partial: true},
		{name: "claude silent", handle: handle("claude", "c3", ""), notYet: true},
		{name: "claude silent, fallback allowed", handle: handle("claude", "c3", ""), fallback: true, missing: noLine},
		{name: "claude without a file", handle: handle("claude", "c4", ""), missing: noFile},
		{name: "claude without a file, fallback allowed", handle: handle("claude", "c4", ""), fallback: true, missing: noFile},
		// A file that cannot be read says nothing about the count: the caller
		// tries again, it does not record that there is none.
		{name: "claude file that cannot be read", handle: handle("claude", "c6", ""), fails: true},
		{name: "claude file that cannot be read, fallback allowed", handle: handle("claude", "c6", ""), fallback: true, fails: true},

		// Codex: the last token_count is the total of the session so far.
		{name: "codex total", handle: handle("codex", "x1", ""), usage: lastCount},
		{name: "codex before its first token_count", handle: handle("codex", "x2", ""), notYet: true},
		{name: "codex has none, fallback allowed", handle: handle("codex", "x2", ""), fallback: true, missing: noLine},
		{name: "codex, last line without its newline", handle: handle("codex", "x3", ""), usage: lastCount},
		{name: "codex, half a line after the total", handle: handle("codex", "x4", ""), usage: lastCount},

		// Muse: the sum of the records of its main agent and its side sessions.
		{name: "muse records", handle: handle("muse", "m1", ""), usage: allRecords},

		// An agent left running is counted up to a time: lines at or before
		// it, the line at it included, and none after it.
		{name: "codex until the last line", handle: handle("codex", "x1", "2026-10-21T20:34:17.164Z"), usage: lastCount},
		{name: "codex until just before it", handle: handle("codex", "x1", "2026-10-21T20:34:17.163Z"), usage: midCount},
		{name: "codex until before its first", handle: handle("codex", "x1", "2026-10-21T20:10:55.309Z"), notYet: true},
		{name: "muse until a record", handle: handle("muse", "m1", recordedAt(1792611406610256)), usage: throughSecond},
		{name: "muse until just before it", handle: handle("muse", "m1", recordedAt(1792611406610255)), usage: firstOnly},
		{name: "muse until before its first", handle: handle("muse", "m1", recordedAt(1792611402569622)), notYet: true},
		// Claude's total has no time and was written however long after, so a
		// kept session reads the lines of its messages up to the time, at
		// once, and they fall short of the session.
		{name: "claude until, total in the file", handle: handle("claude", "c1", "2026-10-21T19:34:53Z"), usage: firstTwo, partial: true},
		{name: "claude until, nothing yet", handle: handle("claude", "c1", "2026-10-21T19:34:48.245Z"), notYet: true},
		{name: "claude until, nothing, fallback allowed", handle: handle("claude", "c1", "2026-10-21T19:34:48.245Z"), fallback: true, missing: noLine},
		// A line whose time does not read cannot be placed before the cutoff.
		{name: "claude with an untimed message", handle: handle("claude", "c5", ""), fallback: true, usage: agents.Usage{Input: 3, FreshInput: 3, Output: 117, CacheRead: 27118, CacheWrite: 19131}, partial: true},
		{name: "claude until, untimed message left out", handle: handle("claude", "c5", "2026-10-21T19:35:00Z"), usage: agents.Usage{Input: 2, FreshInput: 2, Output: 107, CacheRead: 27018, CacheWrite: 19126}, partial: true},
		// An entry in count_due that is not a cutoff changes nothing. A cutoff
		// that does not read is an error for the caller to see, and never a count
		// without one: that would count what a person did in a kept pane after the
		// session ended.
		{name: "a stop with no cutoff", handle: map[string]any{"kind": "codex", "agent_session": "x1", "count_due": map[string]any{"stopped_at": "2026-10-21T20:34:17Z"}}, usage: lastCount},
		{name: "a cutoff that is not a time", handle: cutoff("soon"), fails: true},
		{name: "a cutoff that is empty", handle: cutoff(""), fails: true},
		{name: "a cutoff that is null", handle: cutoff(nil), fails: true},
		{name: "a cutoff that is a number", handle: cutoff(20261021), fails: true},

		// What cannot be counted, and says why: kind before id, since an id
		// cannot make a kind count.
		{name: "no session id", handle: map[string]any{"kind": "claude"}, missing: noID},
		{name: "an id that is a pattern", handle: handle("claude", "*", ""), missing: noID},
		{name: "an id that is a path", handle: handle("claude", "../c1", ""), missing: noID},
		{name: "kind with no usage", handle: handle("kimi", "session_1", ""), missing: noSource},
		{name: "kind with no usage and no id", handle: map[string]any{"kind": "kimi"}, missing: noSource},
		{name: "no kind", handle: map[string]any{"agent_session": "c1"}, missing: noSource},
		{name: "unknown kind", handle: handle("nosuch", "c1", ""), missing: noSource},
	} {
		u, partial, missing, err := h.Usage(ctx, c.handle, c.fallback)
		switch {
		case c.fails:
			if err == nil || errors.Is(err, ErrUsageNotYet) {
				t.Errorf("%s: read %+v, partial %v, missing %q, error %v; want an error", c.name, u, partial, missing, err)
			}
		case c.notYet:
			if !errors.Is(err, ErrUsageNotYet) {
				t.Errorf("%s: read %+v, partial %v, missing %q, error %v; want ErrUsageNotYet", c.name, u, partial, missing, err)
			}
		case err != nil:
			t.Errorf("%s: error %v", c.name, err)
		case u != c.usage || partial != c.partial || missing != c.missing:
			t.Errorf("%s: read %+v, partial %v, missing %q; want %+v, %v, %q", c.name, u, partial, missing, c.usage, c.partial, c.missing)
		}
	}

	// A context that is done reads nothing.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := h.Usage(done, handle("claude", "c1", ""), false); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled context: error %v", err)
	}
}
