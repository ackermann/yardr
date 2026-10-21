// The source check row of the run-ask list: one real test message to a
// user through a declared source, sent when the user or the operator
// approves. Sending is network I/O, which no store write does, so the
// approval is phased as a hook registration is (approveHookRegister in
// runask_hook.go): the approval is held to the card in one write, the
// message is sent with no write open, and the answer is recorded in
// another. A send that fails leaves the ask open, saying the service's
// answer, and approving again retries it. Only the --user form is a
// card: a plain check and --as read files their caller can read and
// need no approval.
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// sourceCheckUse is how a refusal names the source check a run ask takes.
const sourceCheckUse = "yardr source check <source> --user <user>"

// SourceCheckMessage is the title and the text of the one real test
// message a source check --user sends, at a terminal and from an
// approved card alike: one place, so the two stay the same test.
func SourceCheckMessage(src, user string) (title, text string) {
	return "yardr: a test", fmt.Sprintf("%s is told here by the yard, through the source %s. Sent by yardr source check %s --user %s.", user, src, src, user)
}

// parseSourceCheck reads the words of source check <source> [--user
// <user> | --as <user>]. The plain form and --as are read too, and
// refused as needing no approval by the check, not here: what the asker
// may run themselves is the yard's to refuse, in its words.
func parseSourceCheck(rest []string) ([]string, error) {
	args, values, err := runWords(sourceCheckUse, "source check", "the operator's", rest, map[string]bool{"--user": true, "--as": true})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("--run takes %s: it names no source", sourceCheckUse)
		}
		return nil, fmt.Errorf("--run takes %s: one source, not %s", sourceCheckUse, args[0]+" and "+args[1])
	}
	if len(values["--user"]) > 1 {
		return nil, fmt.Errorf("--run takes %s: --user once, with the user the test message is for", sourceCheckUse)
	}
	if len(values["--as"]) > 1 {
		return nil, fmt.Errorf("--run takes %s: --as once, with the user the check call is made as", sourceCheckUse)
	}
	if len(values["--user"]) == 1 && len(values["--as"]) == 1 {
		return nil, fmt.Errorf("--run takes %s: --user tells through the source, and --as reads a user's credential: one of the two", sourceCheckUse)
	}
	out := []string{args[0]}
	for _, flag := range []string{"--user", "--as"} {
		if len(values[flag]) == 1 {
			out = append(out, flag, values[flag][0])
		}
	}
	return out, nil
}

// sourceCheckOf is the source, the user told and the user read as of a
// parsed source check, each "" where the command names none.
func sourceCheckOf(argv []string) (src, user, as string) {
	if len(argv) > 2 {
		src = argv[2]
	}
	for i := 3; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--user":
			user = argv[i+1]
		case "--as":
			as = argv[i+1]
		}
	}
	return src, user, as
}

// checkSourceCheck is a source check's own check as the asker: a plain
// check and --as read files their caller can read, so anyone may run
// them; a test message is the user's own to ask for, or the operator's
// for them (mayTell), and a crew member's proposal of one is approved
// on a card. It returns nil where the asker may run it themselves,
// which askedRun refuses as needing no approval.
func checkSourceCheck(ctx context.Context, w *wtx, argv []string, actor string) error {
	_, user, _ := sourceCheckOf(argv)
	if user == "" {
		return nil
	}
	if err := w.mayTell(ctx, user, actor); err != nil {
		// A crew member (or anyone else) may not send it, so the ask
		// stands: say whose approval it takes, in mayTell's words.
		return &OperatorOnlyError{msg: err.Error(), ask: Askable}
	}
	return nil
}

// sourceCheckCard is the card of source check <source> --user <user>:
// the one real test message it sends, with its title and its text
// whole, and the variable of the user's env file it goes to, set but
// never shown: the address itself stands in no card, note, event or
// answer. A sent message cannot be unsent, so nobody takes it back.
// The approval is held to the source as declared, to the user, and to
// the address their file sets now (Seen). Asking refuses a user the
// yard has not, a source it does not declare or one with no notify
// key, a credential that is not set up, and an address that is none:
// what the terminal command would refuse, the card refuses. An
// approval reads the card again without that, and says in its own
// words what changed (stale).
func (w *wtx) sourceCheckCard(argv []string, asking bool) (AskRun, error) {
	srcName, user, _ := sourceCheckOf(argv)
	if user == "" {
		return AskRun{}, fmt.Errorf("--run takes %s: source check without --user needs no approval: run it yourself", sourceCheckUse)
	}
	u, err := getUser(w.Tx, user)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AskRun{}, fmt.Errorf("a run ask sends a test message to a user of the yard (yardr user list), and %s is none", user)
		}
		return AskRun{}, err
	}
	src, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return AskRun{}, err
	}
	if d.Notify == nil {
		return AskRun{}, fmt.Errorf("the source %s has no notify key, so its file names no call that tells a person", srcName)
	}
	env := d.Notify.AddressEnv
	if _, err := d.CredentialState(src.EnvFile, d.MintArgv(w.home, src.MintCommand)); err != nil {
		return AskRun{}, fmt.Errorf("source %s: %w", srcName, err)
	}
	current, path, err := w.sourceCheckAddress(u, srcName, env)
	if err != nil {
		if !asking {
			return w.sourceCheckMissing(argv, src, u, user, env)
		}
		return AskRun{}, err
	}
	if current == "" {
		if !asking {
			return w.sourceCheckMissing(argv, src, u, user, env)
		}
		return AskRun{}, fmt.Errorf("user %s: env_file %s does not set %s", user, path, env)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	title, text := SourceCheckMessage(srcName, user)
	return AskRun{
		Argv: argv,
		Does: []string{
			fmt.Sprintf("it sends one real test message to %s through the source %s (%s).", user, srcName, sourceHost(d)),
			fmt.Sprintf("the message is titled %q and reads %q.", title, text),
			fmt.Sprintf("it goes to the %s of %s's env file (%s); the address itself stands in no card, note, event or answer.", env, user, path),
		},
		Back:   "a sent test message cannot be unsent",
		BackBy: "nobody",
		Seen: map[string]string{
			"source " + srcName:        fingerprint,
			"user " + user:             seen(u.EnvFile, u.CreatedAt),
			"user " + user + " " + env: seen(current),
		},
	}, nil
}

// sourceCheckAddress is the address the user's env file sets under env,
// with the file's path. A user with no env file, and a file that cannot
// be read, are refused in the words telling refuses them with, naming
// the command that gives the address; an unset name reads as "".
func (w *wtx) sourceCheckAddress(u *User, srcName, env string) (current, path string, err error) {
	if u.EnvFile == "" {
		return "", "", fmt.Errorf("user %s has no %s: yardr user key %s %s gives %s", u.Name, UserEnvFile, u.Name, srcName, env)
	}
	f, err := config.ReadEnvFile(u.EnvFile)
	if err != nil {
		return "", "", fmt.Errorf("user %s: %w", u.Name, err)
	}
	return source.Lookup(f.Env, env), f.Path, nil
}

// sourceCheckMissing is the card sourceCheckCard writes at approve time
// when the address it would send to is none: one sentence saying so, so
// the approval is held to it as stale. The source and the user are as
// the card read them.
func (w *wtx) sourceCheckMissing(argv []string, src *Source, u *User, user, env string) (AskRun, error) {
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{fmt.Sprintf("no %s is set for %s now.", env, user)},
		Back:   "a sent test message cannot be unsent",
		BackBy: "nobody",
		Seen: map[string]string{
			"source " + src.Name:       fingerprint,
			"user " + user:             seen(u.EnvFile, u.CreatedAt),
			"user " + user + " " + env: seen(""),
		},
	}, nil
}

// sourceCheckStale says why the ask seq for source check is approved no
// more: the source was replaced, the user was set under their name, or
// the address their file sets is not the one the card was written from.
func (w *wtx) sourceCheckStale(seq uint64, was, now AskRun) error {
	srcName, user, _ := sourceCheckOf(was.Argv)
	if err := w.staleSource(seq, srcName, was, now); err != nil {
		return err
	}
	if key := "user " + user; was.Seen[key] != now.Seen[key] {
		return fmt.Errorf("ask %d was written when the user %s was another: they were set since. %s", seq, user, nothingRan)
	}
	_, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return staleCard(seq, was, now)
	}
	env := ""
	if d.Notify != nil {
		env = d.Notify.AddressEnv
	}
	if key := "user " + user + " " + env; was.Seen[key] != now.Seen[key] {
		u, err := getUser(w.Tx, user)
		if err != nil {
			return staleCard(seq, was, now)
		}
		current, _, _ := w.sourceCheckAddress(u, srcName, env)
		if current == "" {
			return fmt.Errorf("ask %d was written while %s's %s for %s was set: none is set now. %s", seq, user, env, srcName, nothingRan)
		}
		return fmt.Errorf("ask %d was written while %s's %s for %s was another: it was set since. %s", seq, user, env, srcName, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runSourceCheck refuses: the test message is network I/O, which no
// store write does, so its approval runs in phases outside one (see
// approveSourceCheck), as a hook registration's does.
func runSourceCheck(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	srcName, user, _ := sourceCheckOf(argv)
	return "", fmt.Errorf("source check %s --user %s: the yard sends it itself, and its approval is not this write", srcName, user)
}

// sourceCheckTarget reports whether ask seq on bead id is a source
// check --user, for ApproveAsk to send in phases. A bead or ask that is
// not there, and any other command, is the plain approval's to refuse
// or run.
func (s *Store) sourceCheckTarget(id string, seq uint64) bool {
	found := false
	_ = s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, id)
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
			if err != nil {
				continue
			}
			if _, user, _ := sourceCheckOf(argv); len(argv) >= 5 && argv[0] == "source" && argv[1] == "check" && user != "" {
				found = true
			}
		}
		return nil
	})
	return found
}

// sourceCheckJob is one phased test message between its writes: what
// the card held, and what the send is made with. Address is the user's
// env file's value: it goes to the service alone, in the call's
// parameters, and to nothing the store keeps.
type sourceCheckJob struct {
	bead, asker string
	ask         uint64
	argv        []string
	does        []string
	src         *Source
	d           *source.Declaration
	user        string
	address     string
	notify      string
}

// approveSourceCheck is ApproveAsk for a source check --user, in three
// parts no one write holds: first the approval is held to the card,
// then the test message is sent with no store write open, then the
// answer is recorded. A send that fails leaves the ask open, saying
// the service's answer, and approving again retries it.
func (s *Store) approveSourceCheck(ctx context.Context, id string, seq uint64, actor string) (*AskApproved, error) {
	var job *sourceCheckJob
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		job, err = w.prepareSourceCheck(ctx, id, seq, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	cred, _, err := job.d.ReadCredential(ctx, job.src.EnvFile, job.d.MintArgv(s.home, job.src.MintCommand), source.Update)
	if err != nil {
		return nil, fmt.Errorf("telling %s through %s failed: the test message was not sent: %v. Approve again to retry", job.user, job.d.Name, err)
	}
	title, text := SourceCheckMessage(job.src.Name, job.user)
	call, _ := job.d.Call(job.d.Notify.Call)
	res, err := source.Do(ctx, job.d, job.d.Notify.Call, job.d.TellParams(job.address, title, text, ""), cred)
	if err != nil {
		var failed *source.CallError
		answer := err.Error()
		if errors.As(err, &failed) && failed.Status != 0 {
			answer = fmt.Sprintf("%s (the service answered %d)", answer, failed.Status)
		}
		return nil, fmt.Errorf("telling %s through %s failed: %s at %s: %s. Approve again to retry", job.user, job.d.Name, call.Request.Line(), job.d.Address(), answer)
	}
	if !res.OK() {
		return nil, fmt.Errorf("telling %s through %s failed: %s at %s was answered %d. Approve again to retry", job.user, job.d.Name, call.Request.Line(), job.d.Address(), res.Status)
	}
	result := fmt.Sprintf("%s: told %s: %d in %dms", job.d.Name, job.user, res.Status, res.Took.Milliseconds())
	if job.notify != job.src.Name {
		result += fmt.Sprintf("\nnext: yardr user set %s %s=%s", job.user, UserNotify, job.src.Name)
	}
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

// prepareSourceCheck is the first write of approveSourceCheck: as
// approveAsk checks, it holds the approval to the card and reads the
// address the send is made with. It writes nothing: the send changes no
// record of the yard, so a retry sends the message the card holds.
func (w *wtx) prepareSourceCheck(ctx context.Context, id string, seq uint64, actor string) (*sourceCheckJob, error) {
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
	srcName, user, _ := sourceCheckOf(argv)
	if err := w.mayApproveOwn(ctx, actor, r.Ask.To, user); err != nil {
		return nil, err
	}
	if !slices.Equal(argv, was.Argv) {
		return nil, fmt.Errorf("ask %d holds a command the yard did not write (%s): nothing ran", seq, was.Command())
	}
	card, err := w.sourceCheckCard(argv, false)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if !sameMap(card.Seen, was.Seen) || !slices.Equal(card.Does, was.Does) || card.Back != was.Back || card.BackBy != was.BackBy {
		return nil, w.sourceCheckStale(seq, was, card)
	}
	u, err := getUser(w.Tx, user)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	src, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	address, _, err := w.sourceCheckAddress(u, srcName, d.Notify.AddressEnv)
	if err != nil || address == "" {
		card, err := w.sourceCheckCard(argv, false)
		if err != nil {
			return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
		}
		return nil, w.sourceCheckStale(seq, was, card)
	}
	return &sourceCheckJob{bead: row.ID, asker: r.Note.Author, ask: seq, argv: argv, does: was.Does,
		src: src, d: d, user: user, address: address, notify: u.Notify}, nil
}
