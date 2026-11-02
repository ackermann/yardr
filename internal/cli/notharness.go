package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/charmbracelet/huh"
	"yardr.dev/yardr/internal/agents"
)

// A program on PATH under a harness's name is not always the harness: any
// executable can be called claude. For a kind whose file says what its
// version flag must print (agents.VersionContains), probe tells one that
// answers without it, and it counts as no harness: it is not found, as far
// as the yard goes, though it is on PATH.

// notHarness reports whether the program on PATH answered its version flag
// as another program than the harness it is named for (see probe).
func (n need) notHarness() bool {
	return n.Version != nil && n.Version.State == versionNotHarness
}

// aside is what a harness found on PATH comes to when it is not the harness:
// not found, as far as the yard goes, and what the machine lacks is the
// real one, so Get and Fetch are those of a missing harness. Path still
// names the program that answered, and Version what it said.
func (n *need) aside() {
	m := missingHarness(n.Harness, n.For)
	n.Found, n.Get, n.Fetch = false, m.Get, m.Fetch
}

// harnessesOnPath are the harnesses of kinds as this machine has them. Of a
// kind whose file says what its version flag must print, a program on PATH is
// asked (probe) and, when it is another program, comes back as aside. The
// kinds with no such text are found by name and not run, as ever: a form
// waits for this, so it runs no more than it needs to.
func harnessesOnPath(ctx context.Context, kinds []string) []need {
	out := make([]need, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		out[i] = harnessNeed(kind, "")
		n := &out[i]
		if !n.Found || agents.VersionContains(kind) == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v := probe(ctx, kind, n.Path); v.State == versionNotHarness {
				n.Version = &v
				n.aside()
			}
		}()
	}
	wg.Wait()
	return out
}

// notHarnessLine is the line of a program on PATH that answered the harness's
// version flag with something else: which program it is, what it printed and
// that it is not the harness. A fault where the yard runs on the harness, a
// note where nothing does yet. herdr starts a kind by its name on PATH, so the
// first program of that name is the one that runs; the line says so, since a
// harness installed behind it (a fetched one is put in ~/.local/bin) would
// never be started.
func (n need) notHarnessLine() docLine {
	bad := tagFault
	if n.Optional {
		bad = tagNote
	}
	label := or(n.Label, n.Harness)
	said := "nothing"
	if n.Version.Output != "" {
		said = fmt.Sprintf("%q", n.Version.Output)
	}
	text := fmt.Sprintf("%s, %s: on PATH, but it is not %s: its version flag printed %s, which does not say %q. "+
		"herdr starts the first %s on PATH, which is this one, so a %s installed behind it never runs: "+
		"take this one off PATH, or put %s's directory before %s. Get it: %s",
		n.Path, n.For, label, said, agents.VersionContains(n.Harness), n.Program, label, label, filepath.Dir(n.Path), n.Get)
	return docLine{bad, n.Program, text + n.fileNote()}
}

// harnessOptions are the kinds herdr starts, each marked with whether its
// program is on PATH and is that harness, and the default: the first installed.
func harnessOptions() ([]huh.Option[string], string) {
	var opts []huh.Option[string]
	def := ""
	harnesses := harnessesOnPath(context.Background(), agents.Kinds())
	for _, n := range harnesses {
		label := or(n.Label, n.Harness)
		state := "not on PATH"
		switch {
		case n.Found:
			state = "installed"
			if def == "" {
				def = n.Harness
			}
		case n.notHarness():
			state = fmt.Sprintf("the %s on PATH is not %s", n.Program, label)
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%-12s %s", label, state), n.Harness))
	}
	if def == "" {
		// Keep a fallback for a machine with no installed harness, but do not
		// default to a kind whose executable just answered as another program.
		for _, n := range harnesses {
			if !n.notHarness() {
				def = n.Harness
				break
			}
		}
	}
	return opts, def
}
