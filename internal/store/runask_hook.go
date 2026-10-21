// The hook add row of the run-ask list: a new webhook endpoint for a
// declared source, whose signing secret is given on the hook's own
// secret card (secretask.go) into the file the yard owns for the hook.
// The card names no secret file of the asker's and takes no map or
// acting crew member: those stay the operator's in a terminal, as a
// replacement does.
package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// hookAddUse is how a refusal names the hook add a run ask takes.
const hookAddUse = "yardr hook add <name> --source <declared source> [--unknown unknown|allow|ignore]"

// parseHookAdd reads the words of hook add <name> --source <source>
// [--unknown <word>]. The secret file is the yard's to name, so no file
// flag is read: one that is given, and --map and --as, are refused as
// the operator's to type in a terminal by runWords. --unknown is read
// by name, each flag at most once, and its word held to the three the
// hook takes; that it names a source hearing by identity is the
// card's to refuse, from the declaration it reads.
func parseHookAdd(rest []string) ([]string, error) {
	args, values, err := runWords(hookAddUse, "hook add", "the operator's", rest, map[string]bool{"--source": true, "--unknown": true})
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
	if len(values["--unknown"]) > 1 {
		return nil, fmt.Errorf("--run takes %s: --unknown once, with the word the hook stores for an unmapped sender", hookAddUse)
	}
	out := []string{args[0], "--source", values["--source"][0]}
	if len(values["--unknown"]) == 1 {
		word := values["--unknown"][0]
		if !ValidHookUnknown(word) {
			return nil, fmt.Errorf("--run takes %s: --unknown %q: want %s", hookAddUse, word, strings.Join(HookUnknowns, ", "))
		}
		out = append(out, "--unknown", word)
	}
	return out, nil
}

// hookAddOf is the hook and the source of a parsed hook add, read by
// flag name rather than by position, so a stored command carrying
// --unknown is read as one without it is.
func hookAddOf(argv []string) (name, source string) {
	name, source, _ = hookAddParts(argv)
	return name, source
}

// hookAddParts is the hook, the source and the --unknown word of a
// parsed hook add, the word "" when the command names none.
func hookAddParts(argv []string) (name, source, unknown string) {
	if len(argv) > 2 {
		name = argv[2]
	}
	for i := 3; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--source":
			source = argv[i+1]
		case "--unknown":
			unknown = argv[i+1]
		}
	}
	return name, source, unknown
}

// hookUnknownSays is a card's sentence for the --unknown word a hook
// add will store, effective being the word itself: what an unmapped
// sender is under it.
func hookUnknownSays(effective string) string {
	switch effective {
	case HookUnknownAllow:
		return fmt.Sprintf("it stores --unknown %s for an unmapped sender: a sender no user maps is heard as %s (speaker: %s), as information, not instructions.", effective, SpeakerOutside, SpeakerOutside)
	case HookUnknownIgnore:
		return fmt.Sprintf("it stores --unknown %s for an unmapped sender: a sender no user maps is dropped, recorded nowhere, and offered no card.", effective)
	default:
		return fmt.Sprintf("it stores --unknown %s for an unmapped sender: a sender no user maps is dropped until the operator maps them, and a mapping card is offered for them.", HookUnknownUnknown)
	}
}

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

// hookAddCard is the card of hook add <name> --source <source>
// [--unknown <word>]: the public path it serves, the source it serves,
// the --unknown word it stores where the source hears by identity
// (the default unknown where the command names none), and that it
// takes the source's signing secret, given on the hook's own card into
// the file the yard owns for the hook. The approval is held to the
// source as it was, to the hook's name being free and to the word it
// stores (Seen). Asking refuses a name the yard has, a source the yard
// does not declare, one whose declaration has no hook key, and an
// --unknown word for a source with no identity hearing.
func (w *wtx) hookAddCard(argv []string, asking bool) (AskRun, error) {
	name, sourceName, unknownWord := hookAddParts(argv)
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
		if src, d, err := sourceIn(w.Tx, sourceName); err == nil {
			fingerprint, err := w.sourceSeen(src)
			if err != nil {
				return AskRun{}, err
			}
			seenMap["source "+sourceName] = fingerprint
			if d.HearsByIdentity() || was.Unknown != "" {
				seenMap["hook "+name+" unknown"] = seen(was.UnknownPolicy())
			}
		} else if was.Unknown != "" {
			seenMap["hook "+name+" unknown"] = seen(was.UnknownPolicy())
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
	if unknownWord != "" && !ValidHookUnknown(unknownWord) {
		return AskRun{}, fmt.Errorf("--run takes %s: --unknown %q: want %s", hookAddUse, unknownWord, strings.Join(HookUnknowns, ", "))
	}
	identityHearing := d.HearsByIdentity()
	if unknownWord != "" && !identityHearing {
		return AskRun{}, fmt.Errorf("source %s: --unknown %s for a source with no identity hearing: source %s hears no sender by identity, so there is no unmapped sender to name a policy for", sourceName, unknownWord, sourceName)
	}
	effectiveUnknown := unknownWord
	if effectiveUnknown == "" && identityHearing {
		effectiveUnknown = HookUnknownUnknown
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	seenMap := map[string]string{"source " + sourceName: fingerprint, "hook " + name: ""}
	if identityHearing {
		seenMap["hook "+name+" unknown"] = seen(effectiveUnknown)
	}
	if d.Hook.RegistersSecret() {
		does, publicURL, err := w.hookRegisterDoes(name, sourceName, d)
		if err != nil {
			return AskRun{}, err
		}
		if identityHearing {
			does = append([]string{does[0], hookUnknownSays(effectiveUnknown)}, does[1:]...)
		}
		seenMap["setting "+HookPublicURLSetting] = seen(publicURL)
		return AskRun{
			Argv:   argv,
			Does:   does,
			Back:   runCommandLine([]string{"hook", "rm", name}),
			BackBy: "only the operator",
			Seen:   seenMap,
		}, nil
	}
	does := []string{
		fmt.Sprintf("it adds the hook %s: POST /hooks/%s takes deliveries from the source %s (%s).", name, name, sourceName, sourceHost(d)),
		hookSays(d),
		fmt.Sprintf("the signing secret is given on the hook's own secret card for %s and kept in %s (mode 0600); the hook names that file, and the value is in no note, event or answer.", name, HookSecretFile(w.home, name)),
	}
	if identityHearing {
		does = append([]string{does[0], hookUnknownSays(effectiveUnknown)}, does[1:]...)
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine([]string{"hook", "rm", name}),
		BackBy: "only the operator",
		Seen:   seenMap,
	}, nil
}

// hookPublicURL is the public URL of hook name: the hook_public_url
// setting plus /<name>. Unset, it names the setting, as a card that
// would register at it must.
func (w *wtx) hookPublicURL(name string) (string, error) {
	var base string
	if _, err := get(w.Tx, bSettings, HookPublicURLSetting, &base); err != nil {
		return "", err
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("%s is not set: the yard has no public hook address to register hook %s at (yardr settings set %s https://<host>/hooks)", HookPublicURLSetting, name, HookPublicURLSetting)
	}
	if err := CheckHookPublicURL(base); err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + "/" + name, nil
}

// hookRegisterDoes is the card of a hook the yard registers itself:
// the hook, that the yard makes its secret (none is typed), and that
// it will call the register call at the source's base to register
// the public URL. No secret stands in it: none is made yet.
func (w *wtx) hookRegisterDoes(name, sourceName string, d *source.Declaration) ([]string, string, error) {
	publicURL, err := w.hookPublicURL(name)
	if err != nil {
		return nil, "", err
	}
	call, _ := d.Call(d.Hook.Register.Call)
	return []string{
		fmt.Sprintf("it adds the hook %s: POST /hooks/%s takes deliveries from the source %s (%s).", name, name, sourceName, sourceHost(d)),
		hookSays(d),
		fmt.Sprintf("the yard makes the signing secret for %s and keeps it in %s (mode 0600); the hook names that file, and the value is in no note, event, answer or card. No secret is typed.", name, HookSecretFile(w.home, name)),
		fmt.Sprintf("it will call %s at %s to register %s.", call.Request.Line(), d.Address(), publicURL),
	}, publicURL, nil
}

// hookAddStale says why the ask seq for hook add is approved no more:
// the source is another under its name, the hook's name was taken, or
// the --unknown word the hook stands with now is not the word the card
// would store.
func (w *wtx) hookAddStale(seq uint64, was, now AskRun) error {
	name, sourceName, unknownWord := hookAddParts(was.Argv)
	if err := w.staleSource(seq, sourceName, was, now); err != nil {
		return err
	}
	if key := "hook " + name + " unknown"; (was.Seen[key] != "" || now.Seen[key] != "") && was.Seen[key] != now.Seen[key] {
		// The word that stands now is the standing hook's, where one
		// was made since; the card's is its own effective word.
		cardWord := unknownWord
		if cardWord == "" {
			cardWord = HookUnknownUnknown
		}
		nowWord := cardWord
		var standing Hook
		if had, err := get(w.Tx, bHooks, name, &standing); err == nil && had {
			nowWord = standing.UnknownPolicy()
		}
		if nowWord != cardWord {
			return fmt.Errorf("ask %d was written when hook %s would store --unknown %s, and its word was changed meanwhile to %s: the card is from before that changed. %s", seq, name, cardWord, nowWord, nothingRan)
		}
		if was.Seen["hook "+name] == now.Seen["hook "+name] {
			return fmt.Errorf("ask %d was written when hook %s would store --unknown %s, and its word was changed meanwhile: the card is from before that changed. %s", seq, name, cardWord, nothingRan)
		}
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
	name, sourceName, unknownWord := hookAddParts(argv)
	_, d, err := sourceIn(w.Tx, sourceName)
	if err != nil {
		return "", err
	}
	if d.Hook == nil {
		return "", fmt.Errorf("source %s: its declaration has no hook key, so the yard hears nothing from it", sourceName)
	}
	if d.Hook.RegistersSecret() {
		// ApproveAsk makes this hook in its own phased approval (the
		// register call is no part of a store write); see approveHookRegister.
		return "", fmt.Errorf("hook %s: the yard registers it itself, and its approval is not this write", name)
	}
	if by == nil {
		return "", fmt.Errorf("hook %s: approval needs the answered secret ask for source %s on this bead", name, sourceName)
	}
	if err := w.hookSecretAskBefore(by.Bead, by.Ask, name, sourceName); err != nil {
		return "", err
	}
	file := HookSecretFile(w.home, name)
	secret, _, err := config.ReadSecretFile(file)
	if err != nil {
		return "", fmt.Errorf("hook %s: give its signing secret on the hook's secret card for %s first: %w", name, name, err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("hook %s: give its signing secret on the hook's secret card for %s first: the secret file %s is empty", name, name, file)
	}
	saved, err := w.putHook(ctx, Hook{Name: name, Source: sourceName, SecretFile: file, Unknown: unknownWord}, actor, by, Askable)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("hook %s saved: POST /hooks/%s (%s)", saved.Name, saved.Name, saved.Source), nil
}

// makeHookSecret is a new signing secret the yard makes for a hook it
// registers: 32 bytes of crypto randomness, in the characters a secret
// header takes as base64url (A-Z a-z 0-9 _ -)
// without padding. It is written only to the hook's file, and stands
// in no card, note, event or answer.
func makeHookSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// hookAddedBy reports whether the hook name stands from the approval
// of ask seq on bead: its hook_added event carries that approval.
func (w *wtx) hookAddedBy(bead string, seq uint64, name string) bool {
	e := lastEvent(w.Tx, "hook_added", naming("hook", name))
	if e == nil {
		return false
	}
	approved, _ := e.Data["approved"].(map[string]any)
	if approved == nil {
		return false
	}
	b, _ := approved["bead"].(string)
	a, _ := approved["ask"].(float64)
	return b == bead && uint64(a) == seq
}

// hookRegisterTarget reports whether ask seq on bead id is a hook add
// the yard registers itself (a register call with scheme secret), for
// ApproveAsk to make in phases. A bead or ask that is not there, and
// any other command, is the plain approval's to refuse or run.
func (s *Store) hookRegisterTarget(id string, seq uint64) (string, bool) {
	var name string
	_ = s.read(func(tx *bolt.Tx) error {
		w := &wtx{Tx: tx, home: s.home, store: s}
		row, err := w.loadBead(id)
		if err != nil {
			return nil
		}
		notes, err := beadNotes(tx, row.UUID)
		if err != nil {
			return nil
		}
		for _, r := range ResolveAsks(notes, now()) {
			if r.Note.Seq != seq || r.AskErr != nil || r.Ask.Run == nil {
				continue
			}
			_, argv, err := parseRun(r.Ask.Run.Argv)
			if err != nil || len(argv) < 5 || argv[0] != "hook" || argv[1] != "add" {
				continue
			}
			hookName, sourceName := hookAddOf(argv)
			_, d, err := sourceIn(tx, sourceName)
			if err == nil && d.Hook.RegistersSecret() {
				name = hookName
			}
		}
		return nil
	})
	return name, name != ""
}

// hookRegisterJob is one phased registration between its writes: the
// hook standing in the yard, and what the register call is made with.
// Secret is the hook file's value: it goes to the service alone, in
// the call's body, and to nothing the store keeps.
type hookRegisterJob struct {
	bead, asker string
	ask         uint64
	argv        []string
	does        []string
	name        string
	src         *Source
	d           *source.Declaration
	publicURL   string
	secret      string
}

// approveHookRegister is ApproveAsk for a hook the yard registers
// itself, in three parts no one write holds: first the hook and its
// made secret are committed, then the register call is made with no
// store write open, then the answer is recorded. A call that fails
// leaves the hook in place and the ask open, saying the service's
// answer, and approving again retries the call with the secret that
// stands: the hook and its secret are made once.
func (s *Store) approveHookRegister(ctx context.Context, id string, seq uint64, actor string) (*AskApproved, error) {
	var job *hookRegisterJob
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		job, err = w.prepareHookRegister(ctx, id, seq, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	reg := job.d.Hook.Register
	cred, _, err := job.d.ReadCredential(ctx, job.src.EnvFile, job.d.MintArgv(s.home, job.src.MintCommand), source.Update)
	if err != nil {
		return nil, fmt.Errorf("hook %s is in place, but registering it failed: the register call %s was not made: %v. Approve again to retry", job.name, reg.Call, err)
	}
	params := map[string]string{reg.URL: job.publicURL}
	if reg.Secret != "" {
		params[reg.Secret] = job.secret
	}
	call, _ := job.d.Call(reg.Call)
	res, err := source.Do(ctx, job.d, reg.Call, params, cred)
	if err != nil {
		var failed *source.CallError
		answer := err.Error()
		if errors.As(err, &failed) && failed.Status != 0 {
			answer = fmt.Sprintf("%s (the service answered %d)", answer, failed.Status)
		}
		return nil, fmt.Errorf("hook %s is in place, but registering it failed: %s at %s to register %s: %s. Approve again to retry", job.name, call.Request.Line(), job.d.Address(), job.publicURL, answer)
	}
	if !res.OK() {
		return nil, fmt.Errorf("hook %s is in place, but registering it failed: %s at %s to register %s was answered %d. Approve again to retry", job.name, call.Request.Line(), job.d.Address(), job.publicURL, res.Status)
	}
	result := fmt.Sprintf("hook %s saved: POST /hooks/%s (%s); registered with %s at %s to %s (%d)", job.name, job.name, job.d.Name, call.Request.Line(), job.d.Address(), job.publicURL, res.Status)
	var out *AskApproved
	err = s.writeVia(ctx, func(w *wtx) error {
		var err error
		out, err = w.finishPhasedRun(ctx, id, seq, actor, job.asker, job.argv, job.does, result)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// prepareHookRegister is the first write of approveHookRegister: as
// approveAsk checks, it holds the approval to the card, makes the
// secret where none stands and adds the hook. On a retry the hook
// this ask made is its own: the secret that stands is kept, and the
// card is read as it was written, without the hook in the way.
func (w *wtx) prepareHookRegister(ctx context.Context, id string, seq uint64, actor string) (*hookRegisterJob, error) {
	who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = who
	row, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return nil, err
	}
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", row.ID, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Run == nil:
		return nil, fmt.Errorf("ask %d proposes no command to approve: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, row.ID, seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	case r.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a command runs once for one ask", seq, r.Current.Note.Author)
	}
	was := *r.Ask.Run
	_, argv, err := parseRun(was.Argv)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w", seq, err)
	}
	if err := w.mayApprove(ctx, actor); err != nil {
		return nil, err
	}
	if !slices.Equal(argv, was.Argv) {
		return nil, fmt.Errorf("ask %d holds a command the yard did not write (%s): nothing ran", seq, was.Command())
	}
	name, sourceName, unknownWord := hookAddParts(argv)
	src, d, err := sourceIn(w.Tx, sourceName)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if !d.Hook.RegistersSecret() {
		return nil, fmt.Errorf("ask %d: source %s names no register call with scheme secret now. %s", seq, sourceName, nothingRan)
	}
	does, publicURL, err := w.hookRegisterDoes(name, sourceName, d)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return nil, err
	}
	file := HookSecretFile(w.home, name)
	var existing Hook
	had, err := get(w.Tx, bHooks, name, &existing)
	if err != nil {
		return nil, err
	}
	by := &approvedOn{Bead: row.ID, Ask: seq}
	switch {
	case had && (existing.Source != sourceName || existing.SecretFile != file || !w.hookAddedBy(row.ID, seq, name)):
		card, err := w.hookAddCard(argv, false)
		if err != nil {
			return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
		}
		return nil, w.hookAddStale(seq, was, card)
	case had:
		if was.Seen["source "+sourceName] != fingerprint || was.Seen["setting "+HookPublicURLSetting] != seen(publicURL) || !slices.Equal(was.Does, does) {
			return nil, staleCard(seq, was, AskRun{Does: does})
		}
	default:
		card, err := w.hookAddCard(argv, false)
		if err != nil {
			return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
		}
		if !sameMap(card.Seen, was.Seen) || !slices.Equal(card.Does, was.Does) || card.Back != was.Back || card.BackBy != was.BackBy {
			return nil, w.hookAddStale(seq, was, card)
		}
	}
	var secret []byte
	writeSecret := true
	if had {
		var err error
		secret, _, err = config.ReadSecretFile(file)
		switch {
		case err == nil && len(secret) > 0:
			// A retry keeps the secret belonging to the hook this ask added.
			writeSecret = false
		case err == nil || errors.Is(err, os.ErrNotExist):
			// The hook stands but its secret does not; make one for the retry.
		default:
			return nil, fmt.Errorf("hook %s: %w", name, err)
		}
	}
	if writeSecret {
		made, err := makeHookSecret()
		if err != nil {
			return nil, err
		}
		secret = []byte(made)
	}
	reg := d.Hook.Register
	params := map[string]string{reg.URL: publicURL}
	if reg.Secret != "" {
		params[reg.Secret] = string(secret)
	}
	if err := source.ValidateCallParams(d, reg.Call, params); err != nil {
		return nil, fmt.Errorf("hook %s: the register call parameters cannot be sent: %w. Nothing was registered", name, err)
	}
	if writeSecret {
		if _, _, err := writeHookSecret(w.home, name, string(secret)); err != nil {
			return nil, err
		}
	}
	if !had {
		if _, err := w.putHook(ctx, Hook{Name: name, Source: sourceName, SecretFile: file, Unknown: unknownWord}, actor, by, Askable); err != nil {
			return nil, err
		}
	}
	return &hookRegisterJob{bead: row.ID, asker: r.Note.Author, ask: seq, argv: argv, does: was.Does,
		name: name, src: src, d: d, publicURL: publicURL, secret: string(secret)}, nil
}

// hookSecretAskBefore holds a hook approval to the newest earlier secret
// ask for its name on the same bead: it must name this source and have
// been answered. The file alone cannot show which card it was given for,
// and may already hold a secret from a different setup.
func (w *wtx) hookSecretAskBefore(bead string, before uint64, name, source string) error {
	row, err := w.loadBead(bead)
	if err != nil {
		return err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	var latest *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		ask := &resolved[i]
		if ask.Note.Seq >= before || ask.AskErr != nil || ask.Ask.Secret == nil || ask.Ask.Secret.Hook != name {
			continue
		}
		if latest == nil || ask.Note.Seq > latest.Note.Seq {
			latest = ask
		}
	}
	if latest == nil || latest.Ask.Secret.Source != source || latest.State != AskAnswered {
		return fmt.Errorf("hook %s: give its signing secret on the hook's secret card for %s first; it must be an answered ask for source %s on this bead", name, name, source)
	}
	return nil
}
