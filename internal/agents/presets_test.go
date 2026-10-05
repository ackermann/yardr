package agents

import (
	"slices"
	"testing"
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

func TestParsePiModels(t *testing.T) {
	out := `provider        model                       context  max-out  thinking  images
anthropic       claude-fable-5              1M       128K     yes       yes
anthropic       claude-haiku-4-5            200K     64K      yes       yes
`
	if got := ParsePiModels(out); !slices.Equal(got, []string{"anthropic/claude-fable-5", "anthropic/claude-haiku-4-5"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPresetsAreHerdrKinds(t *testing.T) {
	for _, p := range Presets {
		if !slices.Contains(HerdrKinds, p.Kind) {
			t.Errorf("preset %s is not a kind herdr can start", p.Kind)
		}
	}
}

func TestUnattendedArgs(t *testing.T) {
	for kind, want := range map[string][]string{
		"claude": {"--permission-mode", "auto"},
		"codex":  {"--dangerously-bypass-approvals-and-sandbox"},
		"kimi":   {"--yolo"},
		"pi":     nil,
	} {
		if got := UnattendedArgs(kind); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", kind, got, want)
		}
	}
	// Every kind is offered once, presets first.
	kinds := Kinds()
	if len(kinds) != len(HerdrKinds) || kinds[0] != "claude" || !IsKind("gemini") || IsKind("nope") {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, p := range Presets {
		if !IsKind(p.Kind) {
			t.Errorf("preset %s is not a herdr kind", p.Kind)
		}
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
	if OneShotArgs("claude", "again"); len(oneShot["claude"]) != 1 {
		t.Errorf("the form of claude was changed: %v", oneShot["claude"])
	}
	for kind := range oneShot {
		if _, ok := Lookup(kind); !ok {
			t.Errorf("%s has a one-shot form and is no preset", kind)
		}
	}
}
