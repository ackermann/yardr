package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/agents"
)

// Kimi's config, ~/.kimi-code/config.toml, is where herdr's Kimi integration
// keeps the hooks that tell herdr what the agent does: one [[hooks]] table
// per event, each running herdr-agent-state.sh with the agent's state.
const kimiKind = "kimi"

// kimiConfig is the file Kimi reads its hooks from, as this command finds
// it: the daemon's home is its own.
func kimiConfig() string {
	home, _ := userHomeDir()
	return filepath.Join(home, ".kimi-code", "config.toml")
}

// herdrAgentState is the script herdr's Kimi integration runs from its
// hooks, with the agent's state (session, working, blocked, idle).
const herdrAgentState = "herdr-agent-state.sh"

var herdrStateCommand = regexp.MustCompile(`^(?:(?:[^\s]*/)?(?:bash|sh)\s+)?(?:'(?:[^']*/)?herdr-agent-state\.sh'|"(?:[^"]*/)?herdr-agent-state\.sh"|(?:[^\s]*/)?herdr-agent-state\.sh)\s+("idle"|'idle'|idle)(?:\s|$)`)

// kimiHookNotes are what yardr doctor notes about Kimi's hooks, when the
// yard runs on kimi: one line when no StopFailure hook tells herdr the agent
// is idle, with the block that tells it, and one when the herdr hooks stand
// twice. Notes, not faults: the yard runs, and doctor reads the config as
// this command finds it. Nothing is written: the file is the operator's to
// change.
func kimiHookNotes(uses []harnessUse) []string {
	if !slices.ContainsFunc(uses, func(u harnessUse) bool { return u.kind == kimiKind }) {
		return nil
	}
	file := kimiConfig()
	config, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []string{fmt.Sprintf("whether Kimi tells herdr when a turn fails is not known: %v", err)}
	}
	hooks := agents.ParseKimiHooks(string(config))
	var herdr []agents.KimiHook
	idle := false
	for _, h := range hooks {
		state, ok := herdrHookState(h.Command)
		if !ok {
			continue
		}
		herdr = append(herdr, h)
		if h.Event == "StopFailure" && state == "idle" {
			idle = true
		}
	}
	var notes []string
	if !idle {
		home, _ := userHomeDir()
		notes = append(notes, fmt.Sprintf("%s has no StopFailure hook that tells herdr the agent is idle, so a Kimi turn "+
			"that ends on a provider error stays working to herdr: herdr's Kimi integration maps Stop and Interrupt to idle "+
			"but hears no StopFailure. Append to %s:\n```toml\n[[hooks]]\nevent = \"StopFailure\"\ncommand = \"bash '%s/.kimi-code/hooks/herdr-agent-state.sh' idle\"\ntimeout = 10\n```",
			file, file, home))
	}
	if doubled := doubledHerdrHooks(herdr); len(doubled) > 0 {
		what := "the herdr hook for " + doubled[0]
		if len(doubled) > 1 {
			what = "the herdr hooks for " + strings.Join(doubled, ", ")
		}
		notes = append(notes, fmt.Sprintf("%s lists %s twice: each hook runs twice. Remove the doubled [[hooks]] tables so "+
			"each stands once (a second herdr integration install kimi wrote them again)", file, what))
	}
	return notes
}

// herdrHookState returns the state argument when a command invokes the
// herdr hook directly or through bash/sh. Kimi's hook command is a shell
// command, so honor its quotes when finding the script and argument.
func herdrHookState(command string) (string, bool) {
	m := herdrStateCommand.FindStringSubmatch(command)
	if m == nil {
		return "", false
	}
	return strings.Trim(m[1], `"'`), true
}

// doubledHerdrHooks are the events whose herdr hook stands twice, in the
// order they first stand twice: the same event with the same command more
// than once.
func doubledHerdrHooks(herdr []agents.KimiHook) []string {
	seen := map[agents.KimiHook]int{}
	var doubled []string
	for _, h := range herdr {
		seen[h]++
		if seen[h] == 2 && !slices.Contains(doubled, h.Event) {
			doubled = append(doubled, h.Event)
		}
	}
	return doubled
}
