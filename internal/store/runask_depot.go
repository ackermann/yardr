// The depot test and depot base rows of the run-ask list: a depot's test
// command and its base, changed on a card for an operator with no
// terminal to approve. Both are the operator's to change (PutDepot), and
// both rows run through it, so the check stays one. There is no such
// command at a terminal: the card is the way in, as for a teammate
// invite. A depot's path and kind stay the operator's alone in a
// terminal: they name this machine, not the hosted yard.
package store

import (
	"context"
	"fmt"
	"strings"
)

// parseDepotTest reads the words of depot test <depot>: the depot alone.
// The command goes beside argv, in the ask's --run-test, since it needs
// the characters --run quotes nothing of.
func parseDepotTest(rest []string) ([]string, error) {
	const use = "yardr depot test <depot>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one depot, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// parseDepotBase reads the words of depot base <depot> <base>: a depot
// and one base. A base is one word, so it rides argv as typed.
func parseDepotBase(rest []string) ([]string, error) {
	const use = "yardr depot base <depot> <base>"
	if len(rest) != 2 {
		return nil, fmt.Errorf("--run takes %s: one depot and one base, not %d words", use, len(rest))
	}
	return []string{rest[0], rest[1]}, nil
}

// checkDepotTest is changing a depot's test command as the asker: the
// check PutDepot refuses with, which a run ask may propose.
func checkDepotTest(ctx context.Context, w *wtx, run AskRun, actor string) error {
	return w.may(ctx, actor, ActChangeDepotTest, Object{Kind: KindDepot, Name: run.Argv[2], Ask: Askable})
}

// checkDepotBase is changing a depot's base as the asker: the check
// PutDepot refuses with, which a run ask may propose.
func checkDepotBase(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.may(ctx, actor, ActChangeDepotBase, Object{Kind: KindDepot, Name: argv[2], Ask: Askable})
}

// depotTestCarried is the command a depot test ask carries, whole. It
// refuses one that names no command, one past what a card shows whole,
// and one with a line break or a control character in it, which no card
// shows: more than one line belongs in a script in the repository, whose
// path is then what is proposed.
func depotTestCarried(run AskRun) (string, error) {
	name := run.Argv[2]
	proposed := run.Test
	switch {
	case strings.TrimSpace(proposed) == "":
		return "", fmt.Errorf("depot test %s carries no command: the command goes beside --run, in --run-test \"<command>\"; --run takes the depot alone", name)
	case len(proposed) > MaxRunDepotTest:
		return "", fmt.Errorf("depot test %s: the command it carries is %d bytes: a card shows it whole and holds at most %d, so this is the operator's to type in a terminal", name, len(proposed), MaxRunDepotTest)
	case claimPlain(proposed) != proposed:
		return "", fmt.Errorf("depot test %s: the command it carries holds a line break or a control character, which no card shows: keep it to one line, or type it in a terminal", name)
	}
	return proposed, nil
}

// depotTestCard is the card of depot test <depot>: the depot, its test
// command now and the proposed one, each whole and verbatim. Asking
// refuses a depot the yard has not and a command that would change
// nothing; an approval reads the card again and stale says the command
// was set since. The approval is held to the command whole (Seen).
func (w *wtx) depotTestCard(run AskRun, asking bool) (AskRun, error) {
	name := run.Argv[2]
	var d Depot
	if ok, err := get(w.Tx, bDepots, name, &d); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("depot %s: %w", name, ErrNotFound)
	}
	proposed, err := depotTestCarried(run)
	if err != nil {
		return AskRun{}, err
	}
	if asking && proposed == d.Test {
		return AskRun{}, fmt.Errorf("the test command of the depot %s is %s already, so the command would change nothing", name, proposed)
	}
	now := "none set"
	if d.Test != "" {
		now = d.Test
	}
	back := "clear the depot's test command in a terminal"
	if d.Test != "" {
		back = fmt.Sprintf("yardr bead ask <bead> \"<question>\" --run \"depot test %s\" --run-test \"<the old command above>\"", name)
	}
	return AskRun{
		Argv:   run.Argv,
		Does:   []string{fmt.Sprintf("the test command of the depot %s becomes %s (now: %s).", name, proposed, now)},
		Back:   back,
		BackBy: "only the operator",
		Seen:   map[string]string{"depot " + name: seen(d.Test)},
		Test:   proposed,
	}, nil
}

// depotTestStale says why the ask seq for depot test is approved no
// more: the depot's test command was set since the card was written, or
// is the proposed one already, and nothing is left to change.
func (w *wtx) depotTestStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if key := "depot " + name; was.Seen[key] != now.Seen[key] {
		var d Depot
		if ok, err := get(w.Tx, bDepots, name, &d); err == nil && ok && d.Test == was.Test {
			return fmt.Errorf("ask %d proposes %s as the test command of the depot %s, and it is set already: nothing is left to change. %s", seq, was.Test, name, nothingRan)
		}
		e := lastEvent(w.Tx, "depot_updated", naming("depot", name))
		return fmt.Errorf("ask %d was written when the test command of the depot %s was another: it was changed since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runDepotTest sets the depot's test command to the carried one as actor,
// through putDepot, and answers what depot add prints for it, with the
// depot named.
func runDepotTest(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error) {
	name := run.Argv[2]
	proposed, err := depotTestCarried(run)
	if err != nil {
		return "", err
	}
	var d Depot
	if ok, err := get(w.Tx, bDepots, name, &d); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("depot %s: %w", name, ErrNotFound)
	}
	d.Test = proposed
	if err := w.putDepot(ctx, d, actor, by); err != nil {
		return "", err
	}
	return fmt.Sprintf("depot %s: test: %s", name, proposed), nil
}

// depotBaseCard is the card of depot base <depot> <base>: the depot, its
// base now and the proposed one. Asking refuses a depot the yard has
// not, a directory depot, which has no base, a base that is no branch
// name, and one that would change nothing; an approval reads the card
// again and stale says the base was set since. The approval is held to
// the base whole (Seen). The card checks the base's shape, not that it
// resolves in the depot: depot add checks that in a terminal, with git.
func (w *wtx) depotBaseCard(argv []string, asking bool) (AskRun, error) {
	name, base := argv[2], argv[3]
	var d Depot
	if ok, err := get(w.Tx, bDepots, name, &d); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("depot %s: %w", name, ErrNotFound)
	}
	if d.IsDir() {
		return AskRun{}, fmt.Errorf("depot %s is a directory depot: it has no base", name)
	}
	if !validBase(base) {
		return AskRun{}, fmt.Errorf("depot %s: base %q is not a branch name", name, base)
	}
	if asking && base == d.Base {
		return AskRun{}, fmt.Errorf("the base of the depot %s is %s already, so the command would change nothing", name, base)
	}
	now := "none set"
	if d.Base != "" {
		now = d.Base
	}
	back, backBy := "the depot had no base, and no card clears one again", "nobody"
	if d.Base != "" {
		back, backBy = runCommandLine([]string{"depot", "base", name, d.Base}), "only the operator"
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{fmt.Sprintf("the base of the depot %s becomes %s (now: %s).", name, base, now)},
		Back:   back,
		BackBy: backBy,
		Seen:   map[string]string{"depot " + name: seen(d.Base)},
	}, nil
}

// depotBaseStale says why the ask seq for depot base is approved no
// more: the depot's base was set since the card was written, or is the
// proposed one already, and nothing is left to change.
func (w *wtx) depotBaseStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if key := "depot " + name; was.Seen[key] != now.Seen[key] {
		var d Depot
		if ok, err := get(w.Tx, bDepots, name, &d); err == nil && ok && d.Base == was.Argv[3] {
			return fmt.Errorf("ask %d proposes %s as the base of the depot %s, and it is set already: nothing is left to change. %s", seq, was.Argv[3], name, nothingRan)
		}
		e := lastEvent(w.Tx, "depot_updated", naming("depot", name))
		return fmt.Errorf("ask %d was written when the base of the depot %s was another: it was changed since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runDepotBase sets the depot's base as actor, through putDepot, and
// answers the depot with its base, as depot add prints it.
func runDepotBase(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, base := argv[2], argv[3]
	var d Depot
	if ok, err := get(w.Tx, bDepots, name, &d); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("depot %s: %w", name, ErrNotFound)
	}
	if d.IsDir() {
		return "", fmt.Errorf("depot %s is a directory depot: it has no base", name)
	}
	if !validBase(base) {
		return "", fmt.Errorf("depot %s: base %q is not a branch name", name, base)
	}
	d.Base = base
	if err := w.putDepot(ctx, d, actor, by); err != nil {
		return "", err
	}
	return fmt.Sprintf("depot %s: base %s", name, base), nil
}
