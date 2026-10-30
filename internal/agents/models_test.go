package agents

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Samples are trimmed from the installed tools' real output.

func TestParseClaudeHelp(t *testing.T) {
	help := `  --mcp-config <configs...>             Load MCP servers
  --model <model>                       Model for the current session. Provide
                                        an alias for the latest model (e.g.
                                        'fable', 'opus', or 'sonnet') or a
                                        model's full name (e.g. 'claude-x').
  --name <name>                         Set a display name ('n')
`
	if got := ParseClaudeHelp(help); !slices.Equal(got, []string{"fable", "opus", "sonnet", "claude-x"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseGrokModels(t *testing.T) {
	out := "You are not authenticated.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n    grok-4.7-mini\n"
	if got := ParseGrokModels(out); !slices.Equal(got, []string{"grok-4.7", "grok-4.7-mini"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseOpencodeModels(t *testing.T) {
	out := "opencode/big-pickle\nopencode/hy3-free\n\nsome banner text\n"
	if got := ParseOpencodeModels(out); !slices.Equal(got, []string{"opencode/big-pickle", "opencode/hy3-free"}) {
		t.Fatalf("got %v", got)
	}
}

// The hooks tables of Kimi's config: the event and the command of each,
// in order, in either kind of string, and nothing else.
func TestParseKimiHooks(t *testing.T) {
	config := `default_model = "kimi-code/k3"

[models."kimi-code/k3"]

[[hooks]]
event = "SessionStart"
command = "bash '/Users/jo/.kimi-code/hooks/herdr-agent-state.sh' session"
timeout = 10

[[hooks]]
event = 'Stop' # a literal string
matcher = "startup|resume"
command = "'/opt/homebrew/bin/moshi-hook' kimi-hook"

[[hooks]]
event = "Stop"
command = "bash \"/Users/jo/hooks/herdr-agent-state.sh\" idle"

[thinking]
enabled = true
`
	want := []KimiHook{
		{Event: "SessionStart", Command: "bash '/Users/jo/.kimi-code/hooks/herdr-agent-state.sh' session"},
		{Event: "Stop", Command: "'/opt/homebrew/bin/moshi-hook' kimi-hook"},
		{Event: "Stop", Command: `bash "/Users/jo/hooks/herdr-agent-state.sh" idle`},
	}
	if got := ParseKimiHooks(config); !slices.Equal(got, want) {
		t.Fatalf("hooks = %q, want %q", got, want)
	}
	if got := ParseKimiHooks(""); len(got) != 0 {
		t.Fatalf("of no config: %q", got)
	}
}

func TestParsePiModels(t *testing.T) {
	out := `provider        model                       context  max-out  thinking  images
anthropic       claude-fable-5              1M       128K     yes       yes
anthropic       claude-haiku-4-5            200K     64K      yes       yes
`
	if got := ParsePiModels(out); !slices.Equal(got, []string{"anthropic/claude-fable-5", "anthropic/claude-haiku-4-5"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseKiroModels(t *testing.T) {
	out := `{"default_model":"auto","models":[
{"model_id":"auto","model_name":"auto","description":"Models chosen by task","context_window_tokens":1000000},
{"model_id":"claude-opus-5.5","model_name":"Claude Opus 5.5"},
{"model_id":"claude-opus-5.5","model_name":"Claude Opus 5.5"},
{"model_id":"","model_name":"no id"}]}`
	if got := ParseKiroModels([]byte(out)); !slices.Equal(got, []string{"auto", "claude-opus-5.5"}) {
		t.Fatalf("got %v", got)
	}
	if got := ParseKiroModels([]byte("not json")); got != nil {
		t.Fatalf("of no list: %v", got)
	}
	if got := ParseKiroModels([]byte(`{"models":[]}`)); got != nil {
		t.Fatalf("of no models: %v", got)
	}
}

func TestParseMuseCatalog(t *testing.T) {
	out := `{"schema_version":2,"provider_id":"meta","profile_id":"tbh","source":"provider_catalog","rows":[
{"model_id":"muse-wonder-1.0","visibility":"visible","is_default":true},
{"model_id":"muse-wonder-1.0-contributor","visibility":"hidden"},
{"model_id":"muse-spark-1.3","visibility":"visible"},
{"model_id":"muse-image-1.0","visibility":"hidden"},
{"model_id":"","visibility":"visible"}]}`
	if got := ParseMuseCatalog([]byte(out)); !slices.Equal(got, []string{"muse-wonder-1.0", "muse-spark-1.3"}) {
		t.Fatalf("got %v", got)
	}
	if got := ParseMuseCatalog([]byte("not json")); got != nil {
		t.Fatalf("of no catalog: %v", got)
	}
}

func TestPresetsAreHerdrKinds(t *testing.T) {
	for _, p := range Harnesses {
		if !slices.Contains(store.HerdrKinds, p.Kind) {
			t.Errorf("preset %s is not a kind herdr can start", p.Kind)
		}
	}
}

func TestUnattendedArgs(t *testing.T) {
	for kind, want := range map[string][]string{
		"claude": {"--permission-mode", "auto"},
		"codex":  {"--dangerously-bypass-approvals-and-sandbox"},
		"kimi":   {"--yolo"},
		"kiro":   {"--trust-tools=*"},
		"muse":   {"--yolo"},
		"pi":     nil,
	} {
		if got := UnattendedArgs(kind); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", kind, got, want)
		}
	}
	// Every kind is offered once, presets first.
	kinds := Kinds()
	if len(kinds) != len(store.HerdrKinds) || kinds[0] != "claude" || !IsKind("gemini") || IsKind("nope") {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, p := range Harnesses {
		if !IsKind(p.Kind) {
			t.Errorf("preset %s is not a herdr kind", p.Kind)
		}
	}
}

// A preset is found by its label in either spelling, and a kind that
// offers none of that name (or none at all) says so: what a yard's
// permissions_preset is held to.
func TestPreset(t *testing.T) {
	for _, c := range []struct {
		kind, label string
		args        []string
	}{
		{"claude", "bypass", []string{"--dangerously-skip-permissions"}},
		{"claude", "Accept Edits", []string{"--permission-mode", "acceptEdits"}},
		{"claude", "accept-edits", []string{"--permission-mode", "acceptEdits"}},
		{"codex", "bypass", []string{"--dangerously-bypass-approvals-and-sandbox"}},
	} {
		perm, ok := Preset(c.kind, c.label)
		if !ok || !slices.Equal(perm.Args, c.args) {
			t.Errorf("Preset(%s, %q) = %+v, %v; want %v", c.kind, c.label, perm, ok, c.args)
		}
	}
	for _, kindLabel := range [][2]string{{"claude", "yolo"}, {"kimi", "bypass"}, {"pi", "bypass"}, {"nope", "bypass"}} {
		if _, ok := Preset(kindLabel[0], kindLabel[1]); ok {
			t.Errorf("Preset(%s, %q) found, want none", kindLabel[0], kindLabel[1])
		}
	}
	if got := PresetLabels("claude"); !slices.Equal(got, []string{"auto", "accept edits", "bypass", "read only"}) {
		t.Errorf("PresetLabels(claude) = %v", got)
	}
	if got := PresetLabels("pi"); len(got) != 0 {
		t.Errorf("PresetLabels(pi) = %v, want none", got)
	}
}

// The one-shot form of a kind ends in the prompt; a kind with none known
// has none, and the forms are not changed by a call.
func TestOneShotArgs(t *testing.T) {
	for kind, want := range map[string][]string{
		"claude": {"-p", "say ok"},
		"codex":  {"exec", "--skip-git-repo-check", "say ok"},
		"cursor": nil,
	} {
		if got := OneShotArgs(kind, "say ok"); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", kind, got, want)
		}
	}
	claude, _ := Lookup("claude")
	if OneShotArgs("claude", "again"); len(claude.Flags.OneShot) != 1 {
		t.Errorf("the form of claude was changed: %v", claude.Flags.OneShot)
	}
}
