// The hook add row of the run-ask list: a new webhook endpoint for a
// declared source, whose signing secret is given on the hook's own
// secret card (secretask.go) into the file the yard owns for the hook.
// The card names no secret file of the asker's and takes no map or
// acting crew member: those stay the operator's in a terminal, as a
// replacement does.
package store

import (
	"context"
	"fmt"

	"yardr.dev/yardr/internal/config"
)

// hookAddUse is how a refusal names the hook add a run ask takes.
const hookAddUse = "yardr hook add <name> --source <declared source>"

// parseHookAdd reads the words of hook add <name> --source <source>.
// The secret file is the yard's to name, so no file flag is read: one
// that is given, and --map and --as, are refused as the operator's to
// type in a terminal by runWords.
func parseHookAdd(rest []string) ([]string, error) {
	args, values, err := runWords(hookAddUse, "hook add", "the operator's", rest, map[string]bool{"--source": true})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("--run takes %s: it names no hook", hookAddUse)
		}
		return nil, fmt.Errorf("--run takes %s: one hook, not %s", hookAddUse, args[0]+" and "+args[1])
	}
	if len(values["--source"]) != 1 {
		return nil, fmt.Errorf("--run takes %s: --source once, with the declared source the hook serves", hookAddUse)
	}
	return []string{args[0], "--source", values["--source"][0]}, nil
}

// hookAddOf is the hook and the source of a parsed hook add.
func hookAddOf(argv []string) (name, source string) { return argv[2], argv[4] }

// checkHookAdd is adding a hook's own check as the asker: a new hook
// may be proposed, and replacing one the yard has is the operator's
// alone, in the words putHook refuses a replacement with.
func checkHookAdd(ctx context.Context, w *wtx, argv []string, actor string) error {
	name, _ := hookAddOf(argv)
	if w.Bucket(bHooks).Get([]byte(name)) != nil {
		return w.operatorOnly(ctx, actor, "replace a hook", Alone)
	}
	return w.operatorOnly(ctx, actor, "add a hook", Askable)
}

// hookAddCard is the card of hook add <name> --source <source>: the
// public path it serves, the source it serves and that it takes the
// source's signing secret, given on the hook's own card into the file
// the yard owns for the hook. The approval is held to the source as it
// was and to the hook's name being free (Seen). Asking refuses a name
// the yard has, a source the yard does not declare and one whose
// declaration has no hook key.
func (w *wtx) hookAddCard(argv []string, asking bool) (AskRun, error) {
	name, sourceName := hookAddOf(argv)
	if !hookName.MatchString(name) {
		return AskRun{}, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", name)
	}
	var was Hook
	if had, err := get(w.Tx, bHooks, name, &was); err != nil {
		return AskRun{}, err
	} else if had {
		if asking {
			return AskRun{}, fmt.Errorf("hook %s is in the yard already: adding it again would replace it, which is the operator's to type in a terminal (yardr hook add %s --source %s --secret-file <file>)", name, name, sourceName)
		}
		// The name was taken since the card was written: say so in one
		// sentence, so the approval is held to it as stale.
		seenMap := map[string]string{"hook " + name: seen(was.Source, was.SecretFile)}
		if src, _, err := sourceIn(w.Tx, sourceName); err == nil {
			seenMap["source "+sourceName] = sourceSeen(src)
		}
		return AskRun{
			Argv:   argv,
			Does:   []string{fmt.Sprintf("hook %s is in the yard already.", name)},
			Back:   runCommandLine([]string{"hook", "rm", name}),
			BackBy: "only the operator",
			Seen:   seenMap,
		}, nil
	}
	src, d, err := sourceIn(w.Tx, sourceName)
	if err != nil {
		return AskRun{}, err
	}
	if d.Hook == nil {
		return AskRun{}, fmt.Errorf("source %s: its declaration has no hook key, so the yard hears nothing from it", sourceName)
	}
	return AskRun{
		Argv: argv,
		Does: []string{
			fmt.Sprintf("it adds the hook %s: POST /hooks/%s takes deliveries from the source %s (%s).", name, name, sourceName, sourceHost(d)),
			hookSays(d),
			fmt.Sprintf("the signing secret is given on the hook's own secret card for %s and kept in %s (mode 0600); the hook names that file, and the value is in no note, event or answer.", name, HookSecretFile(w.home, name)),
		},
		Back:   runCommandLine([]string{"hook", "rm", name}),
		BackBy: "only the operator",
		Seen:   map[string]string{"source " + sourceName: sourceSeen(src), "hook " + name: ""},
	}, nil
}

// hookAddStale says why the ask seq for hook add is approved no more:
// the source is another under its name, or the hook's name was taken.
func (w *wtx) hookAddStale(seq uint64, was, now AskRun) error {
	name, sourceName := hookAddOf(was.Argv)
	if err := w.staleSource(seq, sourceName, was, now); err != nil {
		return err
	}
	if key := "hook " + name; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "hook_added", naming("hook", name))
		return fmt.Errorf("ask %d was written when the hook %s was not in the yard: it was added since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runHookAdd adds the hook as actor through putHook, naming the secret
// file the yard owns for it, and answers what the command prints at a
// terminal. The secret must stand in that file already, given on the
// hook's secret card: without it the hook would take nothing, so the
// approval runs only after that giving.
func runHookAdd(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, sourceName := hookAddOf(argv)
	_, d, err := sourceIn(w.Tx, sourceName)
	if err != nil {
		return "", err
	}
	if d.Hook == nil {
		return "", fmt.Errorf("source %s: its declaration has no hook key, so the yard hears nothing from it", sourceName)
	}
	file := HookSecretFile(w.home, name)
	secret, _, err := config.ReadSecretFile(file)
	if err != nil {
		return "", fmt.Errorf("hook %s: give its signing secret on the hook's secret card for %s first: %w", name, name, err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("hook %s: give its signing secret on the hook's secret card for %s first: the secret file %s is empty", name, name, file)
	}
	saved, err := w.putHook(ctx, Hook{Name: name, Source: sourceName, SecretFile: file}, actor, by, Askable)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("hook %s saved: POST /hooks/%s (%s)", saved.Name, saved.Name, saved.Source), nil
}
