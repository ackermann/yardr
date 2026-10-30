// What is the operator's alone, as a run ask is refused it (askedRun):
// commands the yard refuses anyone but the operator that no card may
// propose. Alone are what defines what the yard runs or whom it trusts,
// takes a file path, or reaches another yard. What a linked yard may do
// through a link that is made already is not: what the link carries, where
// its beads go and who its crew is here are the rows of runList, and the
// approval of one that came through that peer's own reach is refused
// (runCommand.link). A link's address is alone; adding and removing one
// are rows of runList.
package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// aloneRefusal renders an operator-only refusal with the alone mark as a
// run ask's refusal: what it is, is the check's own words, and that it is
// never proposed is the yard's.
func aloneRefusal(err error) error {
	return fmt.Errorf("%s; it is the operator's alone, typed in a terminal and never proposed as a run ask", err.Error())
}

// needsNoApproval refuses a proposed command the asker may run
// themselves, as the yard's own check said so.
func needsNoApproval(argv []string) error {
	return fmt.Errorf("%s needs no approval: run it yourself", runCommandLine(argv))
}

// aloneCommand is a command the yard recognizes to refuse as a run ask:
// the operator's alone, or the asker's own to run. It is never stored and
// never runs from a card.
type aloneCommand struct {
	// words begin the command.
	words []string
	// parse reads the words after words: handled says they are this
	// command, and argv is them as the refusal names them. It reads no
	// record of the yard.
	parse func(rest []string) (argv []string, handled bool, err error)
	// check is the command's own permission check, the same the store
	// function refuses with, run as the asker: nil says the asker may
	// run it themselves, an operator-only error with the alone mark
	// says so, and anything else is refused as the reason the command
	// would not run.
	check func(ctx context.Context, w *wtx, argv []string, actor string) error
}

// aloneList are the commands askedRun refuses with whose they are, past
// the rows of the list. Each check is the store function's own, so the
// refusal is the yard's and cannot drift from it; the two that consult
// none (pack apply, group set with a command) say what the command does
// instead, and name where their check will be.
var aloneList = []aloneCommand{{
	words: []string{"source", "add"},
	parse: aloneArgs("yardr source add <file>", 1, 1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		return w.may(ctx, actor, ActAddSource, Object{Kind: KindSource})
	},
}, {
	words: []string{"source", "key"},
	parse: aloneArgs("yardr source key <name> [NAME...]", 1, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		return w.may(ctx, actor, ActSetSourceKey, Object{Kind: KindSource, Name: argv[2]})
	},
}, {
	words: []string{"source", "rm"},
	parse: aloneArgs("yardr source rm <name>", 1, 1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		return w.may(ctx, actor, ActRemoveSource, Object{Kind: KindSource, Name: argv[2]})
	},
}, {
	words: []string{"peer", "set"},
	parse: aloneArgs("yardr peer set <name> --url <url>", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		return w.may(ctx, actor, ActChangePeerAddress, Object{Kind: KindPeer, Name: argv[2]})
	},
}, {
	words: []string{"user", "device", "add"},
	parse: aloneArgs("yardr user device add <user> <device>", 2, 2),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		if err := w.may(ctx, actor, ActAddDevice, Object{Kind: KindUser, Name: argv[3]}); err != nil {
			return err
		}
		return deviceFree(w.Tx, argv[3], argv[4])
	},
}, {
	words: []string{"user", "device", "pair"},
	parse: aloneArgs("yardr user device pair <user> <device>", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		for _, word := range argv[3:] {
			if word == "--end" || strings.HasPrefix(word, "--end=") {
				return w.may(ctx, actor, ActEndDevicePairing, Object{Kind: KindDevice})
			}
		}
		if len(argv) != 5 {
			return fmt.Errorf("yardr user device pair <user> <device>: two names, not %d words", len(argv)-3)
		}
		if err := w.may(ctx, actor, ActAddDevice, Object{Kind: KindUser, Name: argv[3]}); err != nil {
			return err
		}
		return deviceFree(w.Tx, argv[3], argv[4])
	},
}, {
	words: []string{"pair"},
	parse: aloneArgs("yardr pair [<address> <code>]", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		return w.may(ctx, actor, ActPair, Object{Kind: KindPeer})
	},
}, {
	words: []string{"pack", "apply"},
	parse: aloneArgs("yardr pack apply <pack>", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		// No one check: applying a pack is many changes at once, each
		// with its own check, and the pack is read from the machine it
		// is applied on.
		return fmt.Errorf("applying a pack changes many things at once, from files of this machine: it is the operator's alone, typed in a terminal and never proposed as a run ask")
	},
}, {
	words: []string{"crew", "add"},
	parse: aloneArgs("yardr crew add <name> --set <key>=<value>", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		// Without a grant the definition is the asker's to type, if it
		// is theirs to make at all; with one on a member that is there
		// the grants are proposed apart, as crew set.
		sets, name := crewAddSets(argv[2:])
		if name == "" {
			return errNotRow
		}
		parsed, err := ParseSets(sets)
		if err != nil {
			return err
		}
		config, err := MergeConfig(name, map[string]any{}, parsed, nil)
		if err != nil {
			return err
		}
		added := crewGrantsAdded(CrewMember{}, CrewMember{Name: name, Config: config})
		if len(added) == 0 {
			return errNotRow
		}
		var r CrewMember
		if ok, err := get(w.Tx, bCrew, name, &r); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("crew add defines the member whole: to change one's grants propose %s apart", runCommandLine([]string{"crew", "set", name, grantSetOf(sets)}))
		}
		return w.may(ctx, actor, ActGrantCrew, Object{Kind: KindCrew, Name: name, Does: added[0]})
	},
}, {
	words: []string{"group", "add"},
	parse: aloneArgs("yardr group add <name> --runner <runner> [--set cmd=<command>]", 1, -1),
	check: func(_ context.Context, _ *wtx, argv []string, _ string) error {
		for i := 3; i < len(argv); i++ {
			word := argv[i]
			switch {
			case word == "--runner" && i+1 < len(argv):
				i++
				if argv[i] == "exec" {
					return fmt.Errorf("group add with runner exec defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask")
				}
			case word == "--runner=exec":
				return fmt.Errorf("group add with runner exec defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask")
			case word == "--set" && i+1 < len(argv):
				i++
				if key, _, ok := strings.Cut(argv[i], "="); ok && key == "cmd" {
					return fmt.Errorf("group add with cmd defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask")
				}
			case strings.HasPrefix(word, "--set="):
				if key, _, ok := strings.Cut(strings.TrimPrefix(word, "--set="), "="); ok && key == "cmd" {
					return fmt.Errorf("group add with cmd defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask")
				}
			}
		}
		return errNotRow
	},
}, {
	words: []string{"group", "set"},
	parse: aloneArgs("yardr group set <name> [key=value ...]", 0, -1),
	check: func(ctx context.Context, w *wtx, argv []string, actor string) error {
		// No check of the store's yet: group set is the asker's to run
		// whole, and the gates on it are left out (yardr-bbb3, B).
		// What defines what the yard runs is refused as alone
		// meanwhile, in these words and no check's.
		for i, word := range argv[2:] {
			if word == "cmd" && i > 0 && argv[1+i] == "--unset" {
				return fmt.Errorf("group set with --unset cmd defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask")
			}
			key, value, ok := strings.Cut(word, "=")
			if !ok {
				continue
			}
			if key == "cmd" || key == "--unset" && value == "cmd" || key == "runner" && value == "exec" {
				return fmt.Errorf("group set with %s defines what the yard runs: it is the operator's alone, typed in a terminal and never proposed as a run ask", word)
			}
		}
		return errNotRow
	},
}}

// aloneArgs parses the rest of an alone command with min to max words
// (max below zero: any number), answering them whole as the refusal
// names them. The command is always handled: what it is refused as is
// the check's.
func aloneArgs(use string, min, max int) func(rest []string) ([]string, bool, error) {
	return func(rest []string) ([]string, bool, error) {
		if len(rest) < min || max >= 0 && len(rest) > max {
			return nil, true, fmt.Errorf("--run takes %s", use)
		}
		return rest, true, nil
	}
}

// crewAddSets reads a crew add's --set values and its name: the name is
// the first word that is no flag, and the sets are the values of --set,
// whole.
func crewAddSets(rest []string) (sets []string, name string) {
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		if word == "--set" && i+1 < len(rest) {
			i++
			sets = append(sets, rest[i])
		} else if value, ok := strings.CutPrefix(word, "--set="); ok {
			sets = append(sets, value)
		} else if !strings.HasPrefix(word, "-") && name == "" {
			name = word
		}
	}
	return sets, name
}

// grantSetOf is the sets that name grants, as a crew set proposes them.
func grantSetOf(sets []string) string {
	var out []string
	for _, set := range sets {
		if key, _, ok := strings.Cut(set, "="); ok {
			switch key {
			case OpenThreads, SetState, MailPeers:
				out = append(out, set)
			}
		}
	}
	return strings.Join(out, " ")
}

// checkAlone refuses the words as a run ask when they name what is the
// operator's alone or the asker's own to run, and answers errNotRow for
// none the yard runs from a card. It reads the yard's records through
// the commands' own checks, as askedRun reads them for a row.
func (w *wtx) checkAlone(ctx context.Context, sent []string, actor string) error {
	words := strings.Fields(strings.Join(sent, " "))
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	comma := strings.Contains(strings.Join(sent, " "), ",")
	for i := range aloneList {
		e := &aloneList[i]
		if len(words) < len(e.words) || !slices.Equal(words[:len(e.words)], e.words) {
			continue
		}
		rest, handled, err := e.parse(words[len(e.words):])
		if err != nil {
			return err
		}
		if !handled {
			continue
		}
		argv := append(slices.Clone(e.words), rest...)
		if err := e.check(ctx, w, argv, actor); err != nil {
			if err == errNotRow {
				continue
			}
			if ask, ok := IsOperatorOnly(err); ok && ask == Alone {
				return aloneRefusal(err)
			}
			return err
		}
		return needsNoApproval(argv)
	}
	if comma {
		return runNoneChar(',')
	}
	return unknownRun()
}
