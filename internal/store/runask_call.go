// The call row of the run-ask list: one call of a declared source,
// made when the operator approves. Making a call is network I/O, which
// no store write does, so the approval is phased as a source check is
// (approveSourceCheck in runask_check.go): the approval is held to the
// card in one write, the call is made with no write open through
// CallSource as the approver, and the answer is recorded in another. A
// call that fails leaves the ask open, and approving again retries it.
//
// The card shows what the call sends, never a secret it carries: a
// file parameter by its path, size and SHA-256 digest (a changed file
// is a new card, held by Seen), and a secret-file parameter by its
// path alone, the secret's value in no card, note or record.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/source"
)

// sourceCallUse is how a refusal names the call a run ask takes.
const sourceCallUse = "yardr call <source> <call> [param=value ...] [--as <user>]"

func init() {
	runList = append(runList, runCommand{
		words:       []string{"call"},
		use:         sourceCallUse,
		description: "Make a call of a declared source, and print its answer",
		parse:       parseSourceCall,
		check:       checkSourceCall,
		card:        (*wtx).sourceCallCard,
		stale:       (*wtx).sourceCallStale,
		weigh:       sourceCallWeight,
		run:         runSourceCall,
	})
}

// parseSourceCall reads the words of call <source> <call>
// [param=value ...] [--as <user>] into the yard's stored form: the
// parameters sorted by name, with --as last.
func parseSourceCall(rest []string) ([]string, error) {
	args, values, err := runWords(sourceCallUse, "call", "the operator's", rest, map[string]bool{"--as": true})
	if err != nil {
		return nil, err
	}
	if len(values["--as"]) > 1 {
		return nil, fmt.Errorf("--run takes %s: --as once, with the user the call is made as", sourceCallUse)
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("--run takes %s: it names a source and a call", sourceCallUse)
	}
	params := args[2:]
	seenParam := map[string]bool{}
	for _, kv := range params {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--run takes %s: %q is no param=value", sourceCallUse, kv)
		}
		if seenParam[k] {
			return nil, fmt.Errorf("--run takes %s: parameter %s is given twice", sourceCallUse, k)
		}
		seenParam[k] = true
	}
	slices.Sort(params)
	out := []string{args[0], args[1]}
	out = append(out, params...)
	if len(values["--as"]) == 1 {
		out = append(out, "--as", values["--as"][0])
	}
	return out, nil
}

// sourceCallOf is the source, the call, its parameters and the user a
// parsed call is made as, of a parsed call command.
func sourceCallOf(argv []string) (srcName, callName string, params map[string]string, as string) {
	params = map[string]string{}
	if len(argv) > 1 {
		srcName = argv[1]
	}
	if len(argv) > 2 {
		callName = argv[2]
	}
	for i := 3; i < len(argv); i++ {
		if argv[i] == "--as" && i+1 < len(argv) {
			as = argv[i+1]
			i++
			continue
		}
		if k, v, ok := strings.Cut(argv[i], "="); ok {
			params[k] = v
		}
	}
	return srcName, callName, params, as
}

// checkSourceCall is a call's own check as the asker: the operator, and
// a crew member the call is granted to, may make it themselves; anyone
// else proposes it for the operator to approve.
func checkSourceCall(ctx context.Context, w *wtx, argv []string, actor string) error {
	srcName, callName, _, as := sourceCallOf(argv)
	d, err := declared(w.Tx, srcName)
	if err != nil {
		return err
	}
	call, ok := d.Call(callName)
	if !ok {
		return fmt.Errorf("source %s has no call %q (it has: %s)", d.Name, callName, or(strings.Join(callNames(d), ", "), "none"))
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return err
	}
	if ops.is(actor) {
		return nil
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		var g SourceGrant
		if _, err := get(w.Tx, bSourceGrants, grantKey(srcName, Grantee{Crew: actor}, as), &g); err != nil {
			return err
		}
		if g.Covers(call, as) {
			return nil
		}
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("%s may not call %s of %s: it is the operator's to approve on a card", actor, callName, srcName), ask: Askable}
}

// fileDigest is the SHA-256 of the file at path and its size: what a
// card shows of a file it sends, so the bytes approved are the bytes
// that go.
func fileDigest(path string, max int) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a regular file")
	}
	if max > 0 && st.Size() > int64(max) {
		return "", 0, fmt.Errorf("larger than the parameter's bound")
	}
	h := sha256.New()
	var r io.Reader = f
	if max > 0 && max < int(^uint(0)>>1) {
		r = io.LimitReader(f, int64(max)+1)
	}
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	if max > 0 && n > int64(max) {
		return "", 0, fmt.Errorf("larger than the parameter's bound")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// secretFileStamp is what a card keeps of a secret file: when it was
// last written and its length, read under the secret-file rule. A digest
// of the value would stand in the store for anyone who reads the card to
// test guesses against, and a secret may be short enough to guess.
func secretFileStamp(path string) (string, error) {
	if _, _, err := ReadSecretFile(path); err != nil {
		return "", err
	}
	p, err := ExpandPath(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d %d", st.ModTime().UnixNano(), st.Size()), nil
}

// sourceCallCard is the card of call <source> <call>: the request it
// makes and what it sends, by parameter, with a file shown by digest
// and a secret-file by its path alone. Asking refuses a source or call
// the yard has not, and parameters the call does not take or whose file
// cannot be read: what the terminal command would refuse, the card
// refuses. An approval reads the card again without that, and says in
// its own words what changed (stale).
func (w *wtx) sourceCallCard(argv []string, asking bool) (AskRun, error) {
	srcName, callName, params, as := sourceCallOf(argv)
	src, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return AskRun{}, err
	}
	call, ok := d.Call(callName)
	if !ok {
		return AskRun{}, fmt.Errorf("source %s has no call %q (it has: %s)", d.Name, callName, or(strings.Join(callNames(d), ", "), "none"))
	}
	d = d.WithBase(src.Base)
	params, err = call.ParamsWithDefaults(params)
	if err != nil {
		return AskRun{}, err
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	seenMap := map[string]string{"source " + srcName: fingerprint}
	does := []string{fmt.Sprintf("it calls %s of the source %s (%s), %s, as %s.", callName, srcName, sourceHost(d), call.Request.Line(), or(as, "the source's own credential"))}
	var unreadable string
	for _, p := range call.Params {
		v, given := params[p.Name]
		if !given {
			continue
		}
		switch p.Type {
		case "file":
			digest, size, err := fileDigest(v, p.Max)
			if err != nil {
				unreadable = p.Name
				continue
			}
			does = append(does, fmt.Sprintf("the file %s is %s: sha256 %s (%d bytes); those bytes are what is sent.", p.Name, v, digest, size))
			seenMap["file "+p.Name] = seen(v, digest)
		case "secret-file":
			stamp, err := secretFileStamp(v)
			if err != nil {
				unreadable = p.Name
				continue
			}
			does = append(does, fmt.Sprintf("the secret %s is read from the file %s; its value stands in no card, note or record.", p.Name, v))
			seenMap["secret-file "+p.Name] = seen(v, stamp)
		default:
			does = append(does, fmt.Sprintf("%s is %s.", p.Name, OneLine(v)))
		}
	}
	if unreadable != "" {
		if asking {
			return AskRun{}, fmt.Errorf("parameter %s is no file that can be read", unreadable)
		}
		return AskRun{Argv: argv, Does: []string{fmt.Sprintf("the file of parameter %s cannot be read now.", unreadable)},
			Back: "a call that was made cannot be unmade", BackBy: "nobody", Seen: seenMap}, nil
	}
	if asking {
		if err := source.ValidateCallParams(d, callName, params, ReadSecretFile); err != nil {
			return AskRun{}, err
		}
	}
	return AskRun{Argv: argv, Does: does, Back: "a call that was made cannot be unmade", BackBy: "nobody", Seen: seenMap}, nil
}

// sourceCallStale says why the ask seq for a call is approved no more:
// the source was replaced, or a file the card showed by digest or by
// name is not the file that stands now.
func (w *wtx) sourceCallStale(seq uint64, was, now AskRun) error {
	srcName, callName, params, _ := sourceCallOf(was.Argv)
	if err := w.staleSource(seq, srcName, was, now); err != nil {
		return err
	}
	if d, err := declared(w.Tx, srcName); err == nil {
		if call, ok := d.Call(callName); ok {
			params, _ = call.ParamsWithDefaults(params)
		}
	}
	for key, wasSeen := range was.Seen {
		name, isFile := strings.CutPrefix(key, "file ")
		if isFile && now.Seen[key] != wasSeen {
			return fmt.Errorf("ask %d was written for another %s file (%s): it changed since, and a changed file is a new card. %s", seq, name, params[name], nothingRan)
		}
		if name, isSecret := strings.CutPrefix(key, "secret-file "); isSecret && now.Seen[key] != wasSeen {
			return fmt.Errorf("ask %d was written for another secret in the file of %s (%s): it changed since. %s", seq, name, params[name], nothingRan)
		}
	}
	return staleCard(seq, was, now)
}

// sourceCallWeight weighs a call card: outside where the call changes
// something in the service, and code beside it where the call sends a
// file, a program the service will run.
func sourceCallWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	srcName, callName, params, as := sourceCallOf(run.Argv)
	d, err := declared(w.Tx, srcName)
	if err != nil {
		return nil, err
	}
	call, ok := d.Call(callName)
	if !ok {
		return nil, fmt.Errorf("source %s has no call %q (it has: %s)", d.Name, callName, or(strings.Join(callNames(d), ", "), "none"))
	}
	params, err = call.ParamsWithDefaults(params)
	if err != nil {
		return nil, err
	}
	var out []AskWeight
	// A read made as a user spends their credential as an update does.
	if as != "" {
		out = append(out, weighs(WeightAsUser, fmt.Sprintf("it calls %s of %s as %s, with %s's credential", callName, srcName, as, as)))
	}
	if call.Kind != source.Update {
		return out, nil
	}
	out = append(out, weighs(WeightOutside, fmt.Sprintf("it calls %s of %s (an update): %s", callName, srcName, call.Request.Line())))
	for _, p := range call.Params {
		v, given := params[p.Name]
		if !given || p.Type != "file" {
			continue
		}
		digest, _, err := fileDigest(v, p.Max)
		if err != nil {
			continue
		}
		out = append(out, weighs(WeightCode, fmt.Sprintf("the file %s (%s, sha256 %s) is sent to %s as %s, a program it will run", p.Name, v, digest, srcName, callName)))
	}
	return out, nil
}

// runSourceCall refuses: a call is network I/O, which no store write
// does, so its approval runs in phases outside one (see
// approveSourceCall), as a source check's does.
func runSourceCall(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	srcName, callName, _, _ := sourceCallOf(argv)
	return "", fmt.Errorf("call %s %s: the yard makes it itself, and its approval is not this write", srcName, callName)
}

// sourceCallTarget reports whether ask seq on bead id is a call, for
// ApproveAsk to make in phases. A bead or ask that is not there, and
// any other command, is the plain approval's to refuse or run.
func (s *Store) sourceCallTarget(id string, seq uint64) bool {
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
			if _, argv, err := parseRun(r.Ask.Run.Argv); err == nil && len(argv) >= 3 && argv[0] == "call" {
				found = true
			}
		}
		return nil
	})
	return found
}

// sourceCallJob is one phased call between its writes: what the card
// held, and what the call is made with.
type sourceCallJob struct {
	asker  string
	actor  string
	argv   []string
	does   []string
	params map[string]string
	as     string
	inputs map[string][]byte
}

// approveSourceCall is ApproveAsk for a call, in three parts no one
// write holds: first the approval is held to the card, then the call
// is made with no store write open through CallSource as the approver,
// then the answer is recorded. A call that fails leaves the ask open,
// saying the service's answer, and approving again retries it.
func (s *Store) approveSourceCall(ctx context.Context, id string, seq uint64, actor string, weighed bool) (*AskApproved, error) {
	var job *sourceCallJob
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		job, err = w.prepareSourceCall(ctx, id, seq, actor, weighed)
		return err
	})
	if err != nil {
		return nil, err
	}
	srcName, callName, _, _ := sourceCallOf(job.argv)
	res, err := s.CallSource(ctx, SourceCall{Source: srcName, Call: callName, Params: job.params, As: job.as, Actor: job.actor, inputs: job.inputs})
	if err != nil {
		return nil, fmt.Errorf("calling %s of %s failed: %v. Approve again to retry", callName, srcName, err)
	}
	if res.Status < 200 || res.Status > 299 {
		return nil, fmt.Errorf("calling %s of %s failed: %s was answered %d. Approve again to retry", callName, srcName, callName, res.Status)
	}
	result := fmt.Sprintf("%s: called %s: %d in %dms", srcName, callName, res.Status, res.Ms)
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

// prepareSourceCall is the first write of approveSourceCall: as
// approveAsk checks, it holds the approval to the card the yard would
// write now, weighs it again, and applies the weighed and strong
// gates. It writes nothing: the call changes no record of the yard
// until CallSource makes it.
func (w *wtx) prepareSourceCall(ctx context.Context, id string, seq uint64, actor string, weighed bool) (*sourceCallJob, error) {
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
	c, argv, err := parseRun(was.Argv)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w", seq, err)
	}
	if err := w.may(ctx, actor, ActApproveRun, Object{Kind: KindAsk, Name: row.ID, Seq: seq, To: r.Ask.To}); err != nil {
		return nil, err
	}
	if !slices.Equal(argv, was.Argv) {
		return nil, fmt.Errorf("ask %d holds a command the yard did not write (%s): nothing ran", seq, was.Command())
	}
	srcName, callName, params, as := sourceCallOf(argv)
	card, err := w.sourceCallCard(argv, false)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if !sameMap(card.Seen, was.Seen) || !slices.Equal(card.Does, was.Does) || card.Back != was.Back || card.BackBy != was.BackBy {
		return nil, w.sourceCallStale(seq, was, card)
	}
	_, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return nil, err
	}
	call, ok := d.Call(callName)
	if !ok {
		return nil, fmt.Errorf("source %s has no call %q (it has: %s)", d.Name, callName, or(strings.Join(callNames(d), ", "), "none"))
	}
	effective, err := call.ParamsWithDefaults(params)
	if err != nil {
		return nil, err
	}
	inputs, err := call.ReadInputs(effective, ReadSecretFile)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	// The bytes read now are held to the card's digest, so a file swapped
	// since the card was read again is refused. A secret has no digest to
	// hold it to: no card showed its value, so the stamp above is all an
	// approval rests on.
	for _, p := range call.Params {
		data, given := inputs[p.Name]
		if !given || p.Type != "file" {
			continue
		}
		path := effective[p.Name]
		sum := sha256.Sum256(data)
		if seen(path, hex.EncodeToString(sum[:])) != was.Seen["file "+p.Name] {
			return nil, fmt.Errorf("ask %d was written for another %s file (%s): it changed since, and a changed file is a new card. %s", seq, p.Name, path, nothingRan)
		}
	}
	weight, err := w.weighRun(c, card)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if err := weightStale(seq, r.Ask.Weight, weight); err != nil {
		return nil, err
	}
	if err := w.gateRefusal(seq, weight, r.Ask.Weight, actor, weighed); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		inputs = nil
	}
	return &sourceCallJob{asker: r.Note.Author, actor: actor, argv: argv, does: was.Does, params: params, as: as, inputs: inputs}, nil
}
