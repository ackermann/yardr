package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/storetest"
)

// Doctor notes Kimi's config when no StopFailure hook tells herdr the agent
// is idle, with the file and the block that tells it, and when the herdr
// hooks stand twice; a config with the hook once says nothing (yardr-xq9z:
// herdr's Kimi integration maps Stop and Interrupt to idle but hears no
// StopFailure, so a turn that ends on a provider error stays working).
func TestDoctorKimiHooks(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "kimi": toolAnswers})
	st := storetest.New(t)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "kimi"); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	stub(t, &userHomeDir, func() (string, error) { return home, nil })
	file := filepath.Join(home, ".kimi-code", "config.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := func(event, command string) string {
		return "[[hooks]]\nevent = \"" + event + "\"\ncommand = \"" + command + "\"\ntimeout = 10\n\n"
	}
	herdr := func(state string) string {
		return `bash '/Users/jo/.kimi-code/hooks/herdr-agent-state.sh' ` + state
	}
	moshi := `'/opt/homebrew/bin/moshi-hook' kimi-hook`
	single := hook("SessionStart", herdr("session")) + hook("Stop", herdr("idle")) + hook("Interrupt", herdr("idle"))
	withStopFailure := single + hook("StopFailure", herdr("idle")) + hook("StopFailure", moshi)
	withoutStopFailure := single + hook("StopFailure", moshi) + hook("StopFailure", "echo '"+herdrAgentState+"' idle")
	write := func(config string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := func() []string {
		t.Helper()
		rep, _ := doctorJSON(t, st)
		for _, f := range rep.Faults {
			if strings.Contains(f, "StopFailure") || strings.Contains(f, "herdr hook") {
				t.Errorf("Kimi's hooks are a fault: %s", f)
			}
		}
		return rep.State.KimiHooks
	}

	// No config at all: nothing to say.
	if got := notes(); len(got) != 0 {
		t.Fatalf("no config: notes = %q", got)
	}

	// The hook, once: nothing.
	write(withStopFailure)
	if got := notes(); len(got) != 0 {
		t.Fatalf("with the hook: notes = %q", got)
	}
	write(single + hook("StopFailure", "bash '/Users/Jo Doe/.kimi-code/hooks/"+herdrAgentState+"' 'idle'"))
	if got := notes(); len(got) != 0 {
		t.Fatalf("with a quoted idle argument: notes = %q", got)
	}

	// Without it: one note, with the file and the block to append, home
	// expanded. Neither moshi's hook nor a command that only mentions the
	// script counts: neither runs herdr's hook.
	write(withoutStopFailure)
	got := notes()
	if len(got) != 1 {
		t.Fatalf("without the hook: notes = %q", got)
	}
	block := "```toml\n[[hooks]]\nevent = \"StopFailure\"\ncommand = \"bash '" + home + "/.kimi-code/hooks/herdr-agent-state.sh' idle\"\ntimeout = 10\n```"
	wantText(t, got[0], file, "has no StopFailure hook", "stays working to herdr", "Append to "+file+":\n"+block)
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "note   kimi", file, block)

	// The herdr hooks twice, the StopFailure one once: one note, about the
	// duplication alone.
	write(single + hook("Stop", herdr("idle")) + hook("Interrupt", herdr("idle")) + hook("StopFailure", herdr("idle")))
	if got := notes(); len(got) != 1 || !strings.Contains(got[0], "lists the herdr hooks for Stop, Interrupt twice") {
		t.Fatalf("doubled: notes = %q", got)
	}

	// Twice and without it: both, the missing hook first.
	write(withoutStopFailure + single)
	got = notes()
	if len(got) != 2 || !strings.Contains(got[0], "has no StopFailure hook") || !strings.Contains(got[1], "twice") {
		t.Fatalf("doubled and without: notes = %q", got)
	}

	// A config that cannot be read is said, once.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := notes(); len(got) != 1 || !strings.HasPrefix(got[0], "whether Kimi tells herdr when a turn fails is not known: ") {
		t.Fatalf("unreadable config: notes = %q", got)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	// A yard that runs on no kimi: Kimi's config is not looked at.
	claude := storetest.New(t)
	if _, err := run(t, (&app{st: claude, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	write(withoutStopFailure)
	rep, _ := doctorJSON(t, claude)
	if len(rep.State.KimiHooks) != 0 {
		t.Fatalf("no kimi in use: notes = %q", rep.State.KimiHooks)
	}
}
