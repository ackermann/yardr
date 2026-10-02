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
