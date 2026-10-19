// The carrying out of an approved apply (design yardr-bht9 3.5): an
// approval of a pack apply, of a source add that names commands, or of a
// setup ask is recorded first (ApproveAsk and ApproveSetup, with the
// answer holding CarryPending) and carried out straight after by the
// yard's server, because applying a pack writes files and makes many
// store calls and cannot be one write of the store, and neither can
// writing a loose file's command files or a setup's account and grants.
//
// The store keeps, beside the ask, a carrying record the approval's
// write makes and only the server's methods move: approved and not yet
// taken, then claimed, with the claiming server's boot — a value the
// server mints at each start — and the time; then done, with the
// import's output, or failed, with its words. Claiming is one write, a
// claim of the claiming boot is never taken by another boot while the
// yard's one server runs, and a claim of an earlier boot is dead with
// that boot: the server that starts takes those up (ResumeCarry) before
// it serves, since the yard's socket is held by one server at a time.
// Completing and failing name the claiming boot and are refused under
// another, so a server that lost its claim — which means it stopped —
// cannot complete an ask its successor has taken. Done and failed are
// ends: neither is taken up again.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// bCarry holds the carrying records, <bead>\x00<ask seq>: one per
// approved apply that waits for, or has had, the server's carrying out.
var bCarry = []byte("carry")

func init() { allBuckets = append(allBuckets, bCarry) }

// The states a carrying record stands in.
const (
	CarryApproved = "approved"
	CarryClaimed  = "claimed"
	CarryDone     = "done"
	CarryFailed   = "failed"
	// CarryStored is no record state: it is what CheckCarrySource
	// answers for a source the yard holds with the pack's text already
	// stored, to be finished forward (3.4).
	CarryStored = "stored"
)

// Carry is one approved apply's or setup's carrying record.
type Carry struct {
	Bead string `json:"bead"`
	Ask  uint64 `json:"ask"`
	// Argv is the approved command as the ask stored it, kept so the
	// server need not resolve the bead to know what it carries out.
	Argv  []string `json:"argv"`
	State string   `json:"state"`
	// Boot is the claiming server's boot, and ClaimedAt when it claimed.
	Boot      string    `json:"boot,omitempty"`
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
	// Actor is the operator who approved: the carrying out runs as them.
	Actor string `json:"actor"`
	// Result is what the import printed (done) or the failure in words
	// (failed), as the completed answer holds it.
	Result string `json:"result,omitempty"`
	// Sources are what each source the command names stood with when
	// the approval was held to it: where its credential comes from,
	// which a half-applied source must still stand with (3.4).
	Sources []CarrySource `json:"sources,omitempty"`
	// AccountWritten is set in the same store write that writes a setup's
	// account file, so a resumed setup can distinguish that write from an
	// older file that already held the same variable names.
	AccountWritten bool `json:"account_written,omitempty"`
	// Account is the candidate a setup approval took: the picked source's
	// name, "" for a typed account. The carrying out points the pack's
	// sources at its file instead of writing a typed value.
	Account string `json:"account,omitempty"`
	// Keep is a setup approval that keeps the standing account: no typed
	// values, no picked candidate. The carrying out verifies the file
	// still holds every name and goes on to the grants.
	Keep bool `json:"keep,omitempty"`
}

// CarrySource is one source's credential standing at approval, by name.
type CarrySource struct {
	Name        string `json:"name"`
	EnvFile     string `json:"env_file,omitempty"`
	MintCommand string `json:"mint_command,omitempty"`
}

func carryKey(bead string, ask uint64) string { return join(bead, seqKey(ask)) }

// needsCarry says whether approving the run ask was records it to be
// carried out: a pack apply always, and a source add whose declaration
// names command files the approval's write cannot write.
func needsCarry(was AskRun) bool {
	if len(was.Argv) >= 2 && was.Argv[0] == "pack" && was.Argv[1] == "apply" {
		return true
	}
	return len(was.Argv) >= 2 && was.Argv[0] == "source" && was.Argv[1] == "add" &&
		len(was.Carried) == 1 && len(was.Carried[0].Files) > 0
}

// recordCarry makes the carrying record of the ask bead/seq in the
// approval's own write (approveAsk): approved and not yet taken, with
// what each source it names stands with now, which the stale comparison
// has just held to the card.
func (w *wtx) recordCarry(bead string, seq uint64, was AskRun, actor string) error {
	rec := Carry{Bead: bead, Ask: seq, Argv: was.Argv, State: CarryApproved, Actor: actor}
	for _, carried := range was.Carried {
		snap := CarrySource{Name: carried.Name}
		if src, _, err := sourceIn(w.Tx, carried.Name); err == nil {
			snap.EnvFile, snap.MintCommand = src.EnvFile, src.MintCommand
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		rec.Sources = append(rec.Sources, snap)
	}
	return put(w.Tx, bCarry, carryKey(bead, seq), rec)
}

// CarryOf reads the carrying record of the ask bead/seq, nil where the
// approval needed none (it ran in its own write).
func (s *Store) CarryOf(ctx context.Context, bead string, ask uint64) (*Carry, error) {
	var out *Carry
	err := s.read(func(tx *bolt.Tx) error {
		rec := new(Carry)
		ok, err := get(tx, bCarry, carryKey(bead, ask), rec)
		if err == nil && ok {
			out = rec
		}
		return err
	})
	return out, err
}

// PendingCarry reports whether a carrying kind this build handles waits:
// approved and not yet taken, or claimed and not finished, which a
// starting server takes up. Later kinds stay approved for the build that
// knows them. The watcher reads it on every wake so an idle yard costs no
// write.
func (s *Store) PendingCarry(ctx context.Context) (bool, error) {
	pending := false
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bCarry, "", func(_ []byte, rec *Carry) error {
			if carryHandledHere(rec) && (rec.State == CarryApproved || rec.State == CarryClaimed) {
				pending = true
			}
			return nil
		})
	})
	return pending, err
}

// carryHandledHere says whether this server's carrying-out watcher knows
// how to carry rec. A record for a later carrying kind stays approved until
// the build that knows it takes it up.
func carryHandledHere(rec *Carry) bool {
	if len(rec.Argv) < 2 {
		return false
	}
	// A setup's carrying record names the pack alone: the ask on its
	// bead holds the carded account and the preset the approval picked.
	if rec.Argv[0] == "setup" {
		return len(rec.Argv) == 2
	}
	if len(rec.Argv) < 3 {
		return false
	}
	return rec.Argv[0] == "pack" && rec.Argv[1] == "apply" ||
		rec.Argv[0] == "source" && rec.Argv[1] == "add"
}

// KeySourceForSetup writes a setup's account through the secret path and
// records, in the same store write, that this carry wrote it. The receipt
// holds no value; it lets a resumed setup tell its account write from an
// older file that happened to hold the same names.
func (s *Store) KeySourceForSetup(ctx context.Context, bead string, ask uint64, boot, name string, values map[string]string, actor string) (*SourceKeyed, error) {
	var k sourceKey
	err := s.write(func(w *wtx) error {
		if _, err := w.claimedSetupCarry(bead, ask, boot); err != nil {
			return err
		}
		if err := w.keySource(ctx, s.home, name, values, actor, &k); err != nil {
			return err
		}
		f, err := config.ReadEnvFile(k.file)
		if err != nil {
			return fmt.Errorf("source %s: account file after writing: %w", name, err)
		}
		for n, want := range values {
			if got := source.Lookup(f.Env, n); got != want {
				return fmt.Errorf("source %s: %s does not stand in %s after writing", name, n, k.file)
			}
		}
		return w.markCarryAccountWritten(bead, ask, boot)
	})
	if err != nil {
		return nil, k.failed(err)
	}
	return k.keyed(s.home), nil
}

// markCarryAccountWritten marks the claimed setup whose account was just
// written in this transaction. A different boot cannot leave a receipt for
// the claim it no longer owns.
func (w *wtx) markCarryAccountWritten(bead string, ask uint64, boot string) error {
	rec, err := w.claimedSetupCarry(bead, ask, boot)
	if err != nil {
		return err
	}
	rec.AccountWritten = true
	return put(w.Tx, bCarry, carryKey(bead, ask), rec)
}

// claimedSetupCarry reads the setup carrying record and holds it to the
// boot that owns its current claim.
func (w *wtx) claimedSetupCarry(bead string, ask uint64, boot string) (*Carry, error) {
	var rec Carry
	ok, err := get(w.Tx, bCarry, carryKey(bead, ask), &rec)
	if err != nil {
		return nil, err
	}
	if !ok || rec.State != CarryClaimed || rec.Boot != boot || len(rec.Argv) != 2 || rec.Argv[0] != "setup" {
		return nil, fmt.Errorf("setup ask %d on %s is not claimed by this server", ask, bead)
	}
	return &rec, nil
}

// ClaimCarry claims the oldest supported carrying record that waits, or
// one this boot claimed and did not finish (its carrying out stopped on an
// error, and the one watcher of this boot takes it up again), in one
// write: two claims cannot both win. It answers nil where nothing waits. A
// later carrying kind stays approved for the build that knows it. A record
// claimed under another boot is never claimed here: that boot is the yard's
// server, running, until ResumeCarry at a start says it is dead.
func (s *Store) ClaimCarry(ctx context.Context, boot string) (*Carry, error) {
	var out *Carry
	err := s.write(func(w *wtx) error {
		return each(w.Tx, bCarry, "", func(_ []byte, rec *Carry) error {
			if out != nil {
				return nil
			}
			if carryHandledHere(rec) && (rec.State == CarryApproved || rec.State == CarryClaimed && rec.Boot == boot) {
				rec.State, rec.Boot, rec.ClaimedAt = CarryClaimed, boot, now()
				if err := put(w.Tx, bCarry, carryKey(rec.Bead, rec.Ask), rec); err != nil {
					return err
				}
				out = rec
			}
			return nil
		})
	})
	return out, err
}

// ResumeCarry takes up, for the server boot that is starting, every
// apply an earlier boot claimed and did not finish: that boot has
// stopped, since this server holds the yard's store. They stand
// claimed under boot, to be carried by ClaimCarry as its own. It
// answers how many it took up.
func (s *Store) ResumeCarry(ctx context.Context, boot string) (int, error) {
	n := 0
	err := s.write(func(w *wtx) error {
		var recs []*Carry
		if err := each(w.Tx, bCarry, "", func(_ []byte, rec *Carry) error {
			if rec.State == CarryClaimed && rec.Boot != boot {
				recs = append(recs, rec)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, rec := range recs {
			rec.Boot, rec.ClaimedAt = boot, now()
			if err := put(w.Tx, bCarry, carryKey(rec.Bead, rec.Ask), rec); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// CompleteCarry records the carrying out of the ask bead/seq as done,
// with what it printed, in the write that completes its answer: the
// answer's Ran takes the output, and the card reads approved, ran. It
// names the boot that claimed the ask and is refused under another.
func (s *Store) CompleteCarry(ctx context.Context, bead string, ask uint64, boot, result string) error {
	return s.finishCarry(bead, ask, boot, result, false)
}

// FailCarry is CompleteCarry for a carrying out that failed: the record
// and the answer hold the failure in words, and the card reads
// approved, failed. A failed ask is asked again if it is still wanted.
func (s *Store) FailCarry(ctx context.Context, bead string, ask uint64, boot, failure string) error {
	return s.finishCarry(bead, ask, boot, failure, true)
}

func (s *Store) finishCarry(bead string, ask uint64, boot, result string, failed bool) error {
	return s.write(func(w *wtx) error {
		rec := new(Carry)
		ok, err := get(w.Tx, bCarry, carryKey(bead, ask), rec)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("ask %d on %s has no carrying out to finish", ask, bead)
		}
		if rec.State != CarryClaimed || rec.Boot != boot {
			return fmt.Errorf("ask %d on %s is claimed by another server boot: this one does not finish it", ask, bead)
		}
		rec.Result = result
		if failed {
			rec.State = CarryFailed
		} else {
			rec.State = CarryDone
		}
		if err := put(w.Tx, bCarry, carryKey(bead, ask), rec); err != nil {
			return err
		}
		return w.completeAnswer(bead, ask, result, failed)
	})
}

// completeAnswer completes the answer the approval recorded for the
// ask bead/seq with the carrying out's words: its Ran takes the output,
// or the failure with Failed set, so every view reads the carrying out
// from the answer alone (ResolveAsks).
func (w *wtx) completeAnswer(bead string, seq uint64, result string, failed bool) error {
	row, err := w.loadBead(bead)
	if err != nil {
		return err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if n.Kind != NoteAnswer {
			continue
		}
		ans, err := ParseAnswer(n)
		if err != nil || ans.Ask != seq || ans.Ran == nil {
			continue
		}
		ans.Ran.Result, ans.Ran.Failed = result, failed
		raw, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		n.Text = string(raw)
		return put(w.Tx, bNotes, join(row.UUID, seqKey(n.Seq)), n)
	}
	return fmt.Errorf("ask %d on %s has no approved answer to complete", seq, bead)
}

// CheckCarrySource holds one declared source of an approved pack apply
// to what its approval was held to (design yardr-bht9 3.4): it answers
// CarryApproved when the yard holds the source as the card recorded it
// (its sourceSeen, absence included), CarryStored when the yard holds
// the pack's declaration text already stored — with each inventoried
// copy at the card's old standing or the pack's new one, and its
// credential standing as at the approval — which a stop inside the
// source's write leaves and a carrying out finishes forward, and an
// error in the stale words, naming what stands, anywhere else: a file
// at a third hash or mode, an entry added or removed, a third text, a
// credential's file set on the half-added source.
func (s *Store) CheckCarrySource(ctx context.Context, carried AskSource, seenFingerprint string, snap CarrySource) (string, error) {
	var state string
	err := s.read(func(tx *bolt.Tx) error {
		w := &wtx{Tx: tx, store: s}
		var err error
		state, err = w.checkCarrySource(carried, seenFingerprint, snap)
		return err
	})
	return state, err
}

func (w *wtx) checkCarrySource(carried AskSource, seenFingerprint string, snap CarrySource) (string, error) {
	name := carried.Name
	src, _, srcErr := sourceIn(w.Tx, name)
	inv, _, err := w.inventoryOf(name, false)
	if err != nil {
		return "", err
	}
	if errors.Is(srcErr, ErrNotFound) {
		current := ""
		if len(inv) > 0 {
			current = seen("no source", inv)
		}
		if current == seenFingerprint {
			return CarryApproved, nil
		}
		if moved := inventoryMoved(carried.Inventory, inv); moved != "" {
			return "", fmt.Errorf("the source %s is not in the yard, and %s since the card was written", name, moved)
		}
		return "", fmt.Errorf("the source %s is not in the yard as the card recorded it", name)
	}
	if srcErr != nil {
		return "", srcErr
	}
	current, err := w.sourceSeen(src)
	if err != nil {
		return "", err
	}
	if current == seenFingerprint {
		return CarryApproved, nil
	}
	if src.Text != carried.Text {
		// Not the pack's text yet: either the card's standing, whose
		// copies a hand moved, or a third declaration under the name.
		if moved := inventoryMoved(carried.Inventory, inv); moved != "" {
			return "", fmt.Errorf("the source %s stands with another declaration than the pack's, and %s since the card was written", name, moved)
		}
		return "", fmt.Errorf("the source %s stands with another declaration than the one approved: neither the card's nor the pack's text", name)
	}
	if src.EnvFile != snap.EnvFile || src.MintCommand != snap.MintCommand {
		return "", fmt.Errorf("the source %s stands with its credential set since the approval (env_file %q, mint_command %q): neither as the card recorded it nor as the pack left it", name, src.EnvFile, src.MintCommand)
	}
	oldByPath := map[string]AskInventory{}
	for _, e := range carried.Inventory {
		oldByPath[e.Path] = e
	}
	newByProg := map[string]AskFile{}
	for _, f := range carried.Files {
		newByProg[f.Program] = f
	}
	for _, e := range inv {
		if old, ok := oldByPath[e.Path]; ok && old == e {
			continue
		}
		if f, ok := newByProg[e.Path]; ok && e.SHA256 == f.SHA256 && e.Mode == "0700" && !e.Symlink {
			continue
		}
		if _, ok := oldByPath[e.Path]; ok {
			return "", fmt.Errorf("the command copy %s of the source %s stands at sha256 %s, mode %s: neither the card's old standing nor the pack's new one", e.Path, name, e.SHA256, e.Mode)
		}
		return "", fmt.Errorf("the command copy %s of the source %s stands since the card was written: neither the card's nor the pack's", e.Path, name)
	}
	return CarryStored, nil
}
