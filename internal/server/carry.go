package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/packs"
)

// The server's carrying out of an approved apply (design yardr-bht9
// 3.5): one watcher goroutine takes each approved-and-not-carried-out
// pack ask, each loose source ask that names commands, each approved up,
// and each setup ask, and finishes it, once, as the operator. It holds
// the yard's apply lock from the plan's rebuilding through the
// completion record, as the terminal pack apply, source add and source
// rm hold it for their write phase, so the four never interleave on the
// same files and records.

// newBoot mints the boot of one server start: a claim under it is this
// server's while it runs, and dead with it (store.ResumeCarry).
func newBoot() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%x", os.Getpid(), b)
}

// embeddedPack loads the pack yardr embeds of a name, as the server's
// reading does: never a directory, a URL or an earlier import's record.
func embeddedPack(name string) (*pack.Pack, fs.FS, error) {
	if _, err := fs.Stat(packs.FS, name+"/pack.yaml"); err != nil {
		return nil, nil, fmt.Errorf("pack %s: yardr embeds no pack of that name", name)
	}
	sub, err := fs.Sub(packs.FS, name)
	if err != nil {
		return nil, nil, err
	}
	p, err := pack.Load(sub, packs.Embedded+" in yardr "+version.Describe())
	return p, sub, err
}

// carrier carries out approved applies against one store, under one
// boot. load reads the pack to apply; the watcher uses embeddedPack,
// and a test may read a pack of its own the same way. up starts yardr
// up for the home, detached; the watcher uses bringUp, and a test its
// own.
type carrier struct {
	st   *store.Store
	home string
	boot string
	log  *slog.Logger
	load func(name string) (*pack.Pack, fs.FS, error)
	up   func(home string) (int, error)
	// edgeHTTP makes the carrier's member reports; nil is the client
	// with the system's roots, and a test gives the one that trusts its
	// stand-in edge.
	edgeHTTP *http.Client
}

// runCarryWatcher is the server's one carrying-out goroutine: it takes
// up what an earlier boot left, drains what waits, and drains again on
// every event of the yard (an approval writes one) and on a slow tick,
// until ctx ends. One goroutine, so one carrying out at a time.
func runCarryWatcher(ctx context.Context, st *store.Store, home, boot string, log *slog.Logger) {
	c := &carrier{st: st, home: home, boot: boot, log: log, load: embeddedPack, up: bringUp}
	if n, err := st.ResumeCarry(ctx, boot); err != nil {
		log.Warn("carrying out: taking up unfinished applies", "err", err)
	} else if n > 0 {
		log.Info("carrying out: took up unfinished applies", "count", n)
	}
	head, err := st.Head(ctx)
	if err != nil {
		log.Warn("carrying out: the yard's head is not read; waking on the tick alone", "err", err)
	}
	var events <-chan store.Event
	if err == nil {
		if ch, err := st.Subscribe(ctx, head); err == nil {
			events = ch
		} else {
			log.Warn("carrying out: not subscribed; waking on the tick alone", "err", err)
		}
	}
	drain := func() {
		for {
			pending, err := st.PendingCarry(ctx)
			if err != nil || !pending {
				if err != nil && ctx.Err() == nil {
					log.Warn("carrying out: what waits is not read", "err", err)
				}
				return
			}
			did, err := c.carryNext(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("carrying out stopped on an error; it is taken up again", "err", err)
				}
				return
			}
			if !did {
				return
			}
		}
	}
	drain()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			drain()
		case <-tick.C:
			drain()
		}
	}
}

// carryNext claims the next waiting apply or setup and carries it out,
// holding the apply lock from the plan's rebuilding through the
// completion record. It answers false where nothing waited. A failure of
// the carrying out itself is recorded on the ask (FailCarry); an error is
// the machinery's (the lock, the claim, the completion), which leaves
// the claim standing for the next drain or the next start.
func (c *carrier) carryNext(ctx context.Context) (bool, error) {
	rec, err := c.st.ClaimCarry(ctx, c.boot)
	if err != nil || rec == nil {
		return false, err
	}
	if len(rec.Argv) >= 2 && rec.Argv[0] == "setup" {
		setup, picked, err := c.askSetup(ctx, rec)
		if err != nil {
			return true, err
		}
		return c.carryClaimed(ctx, rec, "an approved setup", "setup "+setup.Pack, func() (string, string) {
			return c.carrySetup(ctx, rec, setup, picked)
		})
	}
	if isDepotPushCarry(rec.Argv) {
		return c.carryDepotPushClaimed(ctx, rec)
	}
	run, err := c.askRun(ctx, rec)
	if err != nil {
		return true, err
	}
	if len(run.Argv) == 1 && run.Argv[0] == "up" {
		return c.carryClaimed(ctx, rec, "an approved up", run.Command(), func() (string, string) {
			return c.carryUp(ctx)
		})
	}
	if isTeammateCarry(run.Argv) {
		return c.carryTeammateClaimed(ctx, rec, run)
	}
	return c.carryClaimed(ctx, rec, "an approved apply", run.Command(), func() (string, string) {
		if len(run.Argv) >= 2 && run.Argv[0] == "pack" {
			return c.carryPack(ctx, rec, run)
		}
		return c.carryLoose(ctx, rec, run)
	})
}

// carryClaimed runs a claimed carrying out under the apply lock, from the
// plan's rebuilding through the completion record: a setup's apply step is
// the pack row's, so the two never interleave on the same files and
// records. what and command word the log line.
func (c *carrier) carryClaimed(ctx context.Context, rec *store.Carry, what, command string, carry func() (string, string)) (bool, error) {
	release, err := config.AcquireApplyLock(c.home)
	if err != nil {
		return true, fmt.Errorf("carrying out ask %d on %s: the apply lock: %w", rec.Ask, rec.Bead, err)
	}
	defer release()
	result, failure := carry()
	if failure != "" {
		if err := c.st.FailCarry(ctx, rec.Bead, rec.Ask, c.boot, failure); err != nil {
			return true, err
		}
		c.log.Warn("carrying out failed", "bead", rec.Bead, "ask", rec.Ask, "failure", failure)
		return true, nil
	}
	if err := c.st.CompleteCarry(ctx, rec.Bead, rec.Ask, c.boot, result); err != nil {
		return true, err
	}
	c.log.Info("carried out "+what, "bead", rec.Bead, "ask", rec.Ask, "command", command)
	return true, nil
}

// askRun is the run ask of the carrying record rec, as it stands on its
// bead.
func (c *carrier) askRun(ctx context.Context, rec *store.Carry) (*store.AskRun, error) {
	notes, err := c.st.Notes(ctx, rec.Bead)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == rec.Ask && r.Ask.Run != nil {
			return r.Ask.Run, nil
		}
	}
	return nil, fmt.Errorf("ask %d on %s holds no run ask to carry out", rec.Ask, rec.Bead)
}

// failWords is a carrying-out failure in words: what happened, what it
// leaves, and the terminal command that finishes or undoes it.
func failWords(why, terminal string) string {
	return why + " Finish it with " + terminal + " in a terminal, or ask again if it is still wanted."
}

// staleWords is a holding failure (3.4): the yard no longer holds what
// the approval was held to, nothing further is applied, and the ask is
// asked again if it is still wanted.
func staleWords(why, terminal string) string {
	return why + ": nothing further was applied. Ask again if it is still wanted, or finish it with " + terminal + " in a terminal."
}

// carryLoose finishes a loose source add that names commands: the
// declaration stands from the approval's write, so it holds the stored
// text to the approved one, writes the carried bytes whole, and
// verifies each file's SHA-256 and mode against the carried ones.
func (c *carrier) carryLoose(ctx context.Context, rec *store.Carry, run *store.AskRun) (string, string) {
	terminal := "yardr source add " + run.Argv[2]
	if len(run.Carried) != 1 {
		return "", failWords(fmt.Sprintf("the ask carries %d sources, not one", len(run.Carried)), terminal)
	}
	carried := run.Carried[0]
	src, err := c.st.GetSource(ctx, carried.Name)
	if err != nil {
		return "", failWords(fmt.Sprintf("source %s is not in the yard: its declaration was stored when the ask was approved and is gone since; nothing was written", carried.Name), terminal)
	}
	if src.Text != carried.Text {
		what := "another declaration"
		if d, err := src.Declaration(); err == nil {
			what = fmt.Sprintf("another declaration (%s under %s)", plural(len(d.Calls), "call"), d.Address())
		}
		return "", failWords(fmt.Sprintf("source %s stands with %s than the one approved; nothing was written", carried.Name, what), terminal)
	}
	// A replacement card showed the command copies that stood. Hold those
	// copies again before the delayed write, so a hand that changed one after
	// approval is not silently removed by making the directory anew.
	if fingerprint := run.Seen["source "+carried.Name]; fingerprint != "" {
		var snap store.CarrySource
		found := false
		for _, source := range rec.Sources {
			if source.Name == carried.Name {
				snap, found = source, true
				break
			}
		}
		if !found {
			return "", failWords(fmt.Sprintf("the carrying record has no standing for source %s", carried.Name), terminal)
		}
		if _, err := c.st.CheckCarrySource(ctx, carried, fingerprint, snap); err != nil {
			return "", staleWords(fmt.Sprintf("ask %d: %s", rec.Ask, err), terminal)
		}
	}
	files := make([]source.FileCopy, len(carried.Files))
	want := map[string]string{}
	for i, f := range carried.Files {
		files[i] = source.FileCopy{Program: f.Program, Data: []byte(f.Text)}
		want[f.Program] = f.SHA256
	}
	if err := source.WriteCopies(c.home, carried.Name, files); err != nil {
		return "", failWords(fmt.Sprintf("source %s saved, but its commands' files were not copied: %v", carried.Name, err), terminal)
	}
	if err := source.VerifyCopies(c.home, carried.Name, want); err != nil {
		return "", failWords(err.Error(), terminal)
	}
	d, err := source.Parse(run.Argv[2], []byte(carried.Text))
	if err != nil {
		return "", failWords(err.Error(), terminal)
	}
	if len(d.Calls) == 0 {
		return fmt.Sprintf("source %s saved: no call, its limits command alone", d.Name), ""
	}
	return fmt.Sprintf("source %s saved: %s under %s", d.Name, plural(len(d.Calls), "call"), src.CalledAt(d)), ""
}

// carryUp finishes an approved up: the server starts yardr up for the
// home, detached, and the card reads that the daemon's start follows as
// a daemon_started event, which is the recovery's evidence. The pid goes
// to the server log; a spawn failure fails the carry with the terminal
// command.
func (c *carrier) carryUp(ctx context.Context) (string, string) {
	up := c.up
	if up == nil {
		up = bringUp
	}
	pid, err := up(c.home)
	if err != nil {
		return "", failWords("yardr up: "+err.Error(), "yardr up")
	}
	c.log.Info("carrying out an approved up: it runs detached", "pid", pid)
	return "up started; the daemon's start follows as a daemon_started event", ""
}

// bringUp starts yardr up for the home, detached: the server's own
// binary, since up leaves the running server alone and starts what is
// missing. What it prints is discarded; the daemon's start arrives as a
// daemon_started event.
func bringUp(home string) (int, error) {
	self, err := executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(self, "--home", home, "up")
	if _, err := SpawnDetached(cmd, os.DevNull); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// carryPack carries out an approved pack apply: it makes the plan
// again from the embedded pack, holds it as 3.4 holds it (the pack's
// side whole, the yard's a source at a time; 3.7 an object at a time
// for a depot pack) before anything writes, applies it as the
// operator with no answers, no prune and no overwrite, and verifies
// what it wrote.
func (c *carrier) carryPack(ctx context.Context, rec *store.Carry, run *store.AskRun) (string, string) {
	res, plan, failure := c.carryPackApply(ctx, rec, run)
	if failure != "" {
		return "", failure
	}
	return packResult(res, plan), ""
}

// carryPackApply is carryPack's holding, applying and verifying, with the
// import's result and plan for the caller to word: a setup's carrying out
// applies its pack through it and words the apply beside its account and
// grants.
func (c *carrier) carryPackApply(ctx context.Context, rec *store.Carry, run *store.AskRun) (*pack.Result, *pack.Plan, string) {
	name := run.Argv[2]
	terminal := "yardr pack apply " + name
	if run.Pack != nil && run.Pack.Depot != "" {
		terminal += " --depot " + run.Pack.Depot
	}
	if run.Pack == nil {
		return nil, nil, failWords("the ask holds no pack reading to carry out", terminal)
	}
	// The card's own depot and mappings, never a fresh reading of the
	// ask's words: what approval runs is the plan the card showed. A
	// A card written before mappings were carried has no mappings; its
	// recorded depot still names the target it was approved for.
	depot, mappings := run.Pack.Depot, run.Pack.Mappings
	reading, err := c.st.ReadPack(ctx, name, depot, mappings)
	if err != nil {
		return nil, nil, failWords(err.Error(), terminal)
	}
	// The pack's side, held whole: who embeds it and the bytes as carded.
	// The build is no part of it, as at the approval (LabelWithoutBuild):
	// the bytes are held to the card, and the refusal names the builds.
	cardedBuild := run.Seen["pack "+name+" build"]
	if reading.Name != run.Pack.Name || reading.Kind != run.Pack.Kind ||
		store.LabelWithoutBuild(reading.Label, reading.Build) != store.LabelWithoutBuild(run.Pack.Label, cardedBuild) ||
		!sameSources(reading.Sources, run.Carried) {
		return nil, nil, staleWords(store.CardedUnder(rec.Ask, cardedBuild, reading.Build)+", whose pack differs", terminal)
	}
	if reading.Depot != depot {
		return nil, nil, staleWords(fmt.Sprintf("pack %s would now be re-applied to depot %s, not %s", name, reading.Depot, depot), terminal)
	}
	p, sub, err := c.load(name)
	if err != nil {
		return nil, nil, failWords(err.Error(), terminal)
	}
	plan, err := pack.Build(ctx, c.st, p, packOptions(depot, c.home, mappings))
	if err != nil {
		return nil, nil, failWords(err.Error(), terminal)
	}
	// The pin pack apply --sha256 trusts, held to the digest the card
	// named: a pack that moved since the approval installs nothing. A card
	// from before digests has none.
	if run.Pack.Digest != "" {
		if err := plan.CheckDigest(run.Pack.Digest); err != nil {
			return nil, nil, staleWords(fmt.Sprintf("ask %d: %s", rec.Ask, err), terminal)
		}
	}
	answers, open, calls := pack.Answers{}, map[string]map[string]bool(nil), map[string]map[string]bool(nil)
	if !mappings.IsZero() {
		if answers, open, calls, err = mappingAnswers(ctx, c.st, plan, mappings, c.home); err != nil {
			return nil, nil, failWords(err.Error(), terminal)
		}
	}
	// The yard's side, a source at a time, before anything writes.
	stored := map[string]bool{}
	snaps := map[string]store.CarrySource{}
	for _, s := range rec.Sources {
		snaps[s.Name] = s
	}
	for _, carried := range run.Carried {
		state, err := c.st.CheckCarrySource(ctx, carried, run.Seen["source "+carried.Name], snaps[carried.Name])
		if err != nil {
			return nil, nil, staleWords(fmt.Sprintf("ask %d: %s", rec.Ask, err), terminal)
		}
		stored[carried.Name] = state == store.CarryStored
	}
	// A depot pack's yard side is its objects, one at a time (3.7).
	if run.Pack.Plan != nil {
		if err := holdDepotObjects(run.Pack.Plan, askPlanOf(sub, plan, answers, open, calls)); err != nil {
			return nil, nil, staleWords(fmt.Sprintf("ask %d: %s", rec.Ask, err), terminal)
		}
	}
	// A source the yard holds with the pack's text already stored is
	// not accepted as done: it is written again whole and verified
	// (3.4), so a partial copy is repaired, not resumed into.
	for _, carried := range run.Carried {
		if !stored[carried.Name] {
			continue
		}
		if _, err := c.st.PutSource(ctx, "pack "+name, carried.Text, rec.Actor); err != nil {
			return nil, nil, failWords(fmt.Sprintf("source %s: %v", carried.Name, err), terminal)
		}
		if failure := c.writeAndVerify(carried); failure != "" {
			return nil, nil, failWords(failure, terminal)
		}
	}
	res, err := pack.Apply(ctx, c.st, plan, pack.ApplyOptions{
		Answers: answers, OpenThreads: open, Calls: calls, Actor: rec.Actor,
		ApprovedBead: rec.Bead, ApprovedAsk: rec.Ask,
	})
	if err != nil {
		return nil, nil, failWords(err.Error(), terminal)
	}
	// What stands is verified, never trusted: each source's stored
	// text and each copied file, and for a depot pack every carded
	// object read unchanged with every written file hashed.
	for _, carried := range run.Carried {
		src, err := c.st.GetSource(ctx, carried.Name)
		if err != nil {
			return nil, nil, failWords(fmt.Sprintf("source %s does not stand after the import: %v", carried.Name, err), terminal)
		}
		if src.Text != carried.Text {
			return nil, nil, failWords(fmt.Sprintf("source %s stands with another declaration than the pack's after the import", carried.Name), terminal)
		}
		if failure := verifyCarried(c.home, carried); failure != "" {
			return nil, nil, failWords(failure, terminal)
		}
	}
	if run.Pack.Plan != nil {
		after, err := pack.Build(ctx, c.st, p, packOptions(depot, c.home, mappings))
		if err != nil {
			return nil, nil, failWords(err.Error(), terminal)
		}
		if err := verifyDepotObjects(run.Pack.Plan, askPlanOf(sub, after, answers, open, calls)); err != nil {
			return nil, nil, failWords(err.Error(), terminal)
		}
		for _, obj := range after.Objects {
			if obj.Kind != "file" || obj.File == nil {
				continue
			}
			data, err := os.ReadFile(obj.File.To)
			if err != nil {
				return nil, nil, failWords(fmt.Sprintf("file %s does not stand after the import: %v", obj.File.To, err), terminal)
			}
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != obj.File.SHA256 {
				return nil, nil, failWords(fmt.Sprintf("file %s hashes to %s after the import, not the planned %s", obj.File.To, hex.EncodeToString(sum[:]), obj.File.SHA256), terminal)
			}
		}
	}
	return res, plan, ""
}

// writeAndVerify writes a carried source's command files whole and
// holds each to its carried SHA-256 and the mode the card showed.
func (c *carrier) writeAndVerify(carried store.AskSource) string {
	files := make([]source.FileCopy, len(carried.Files))
	for i, f := range carried.Files {
		files[i] = source.FileCopy{Program: f.Program, Data: []byte(f.Text)}
	}
	if err := source.WriteCopies(c.home, carried.Name, files); err != nil {
		return fmt.Sprintf("source %s saved, but its commands' files were not copied: %v", carried.Name, err)
	}
	return verifyCarried(c.home, carried)
}

func verifyCarried(home string, carried store.AskSource) string {
	want := map[string]string{}
	for _, f := range carried.Files {
		want[f.Program] = f.SHA256
	}
	if err := source.VerifyCopies(home, carried.Name, want); err != nil {
		return err.Error()
	}
	return ""
}

// sameSources says whether two readings of a pack hold the same
// declared sources: each text whole, and each command file's program,
// bytes and hash.
func sameSources(a, b []store.AskSource) bool {
	if len(a) != len(b) {
		return false
	}
	byName := map[string]store.AskSource{}
	for _, s := range b {
		byName[s.Name] = s
	}
	for _, s := range a {
		o, ok := byName[s.Name]
		if !ok || o.Text != s.Text || len(o.Files) != len(s.Files) {
			return false
		}
		files := map[string]store.AskFile{}
		for _, f := range o.Files {
			files[f.Program] = f
		}
		for _, f := range s.Files {
			if files[f.Program] != f {
				return false
			}
		}
	}
	return true
}

// holdDepotObjects holds a rebuilt depot plan to the carded one, an
// object at a time (3.7): each carded create or change must rebuild
// identical or unchanged — the planner reading unchanged is what
// "already as the pack says" means — each carded unchanged must
// rebuild unchanged, and each dropped must still read dropped.
func holdDepotObjects(carded, rebuilt *store.AskPlan) error {
	for _, lines := range [][]string{rebuilt.Missing, rebuilt.Asks, rebuilt.Refused, rebuilt.Edited, rebuilt.NonText} {
		if len(lines) > 0 {
			return fmt.Errorf("the plan now asks, refuses, or needs a hand-edited or non-text file: %s", strings.Join(lines, "; "))
		}
	}
	byKey := map[string]store.AskPlanObject{}
	for _, o := range rebuilt.Objects {
		byKey[o.Kind+"\x00"+o.Name] = o
	}
	cardedKeys := make(map[string]bool, len(carded.Objects))
	for _, obj := range carded.Objects {
		key := obj.Kind + "\x00" + obj.Name
		cardedKeys[key] = true
		rb, ok := byKey[key]
		if !ok {
			return fmt.Errorf("the object %s %s is no longer in the plan", obj.Kind, obj.Name)
		}
		if !sameStrings(rb.Keeps, obj.Keeps) || !sameStrings(rb.Shadows, obj.Shadows) {
			return fmt.Errorf("the object %s %s keeps or shadows differently than the card", obj.Kind, obj.Name)
		}
		sameChange := rb.Action == obj.Action && rb.FileNewSHA == obj.FileNewSHA && rb.Text == obj.Text && sameChanges(rb.Changes, obj.Changes) && sameStrings(rb.Env, obj.Env)
		// A changed file not yet installed stands at the card's old
		// hash; at a third one a hand moved it outside the import.
		if obj.Kind == "file" && rb.Action == obj.Action && (obj.Action == "create" || obj.Action == "change") && rb.FileOldSHA != obj.FileOldSHA {
			return fmt.Errorf("the file %s stands at sha256 %s, neither the card's old %s nor its new %s", obj.Name, rb.FileOldSHA, obj.FileOldSHA, obj.FileNewSHA)
		}
		switch obj.Action {
		case "create", "change":
			if rb.Action != "unchanged" && !sameChange {
				return fmt.Errorf("the object %s %s rebuilds as %s, not as the card's %s", obj.Kind, obj.Name, rb.Action, obj.Action)
			}
		case "unchanged":
			if rb.Action != "unchanged" {
				return fmt.Errorf("the object %s %s was unchanged on the card and rebuilds as %s", obj.Kind, obj.Name, rb.Action)
			}
		case "dropped":
			if rb.Action != "dropped" {
				return fmt.Errorf("the object %s %s was dropped on the card and rebuilds as %s", obj.Kind, obj.Name, rb.Action)
			}
		default:
			if rb.Action != obj.Action {
				return fmt.Errorf("the object %s %s rebuilds as %s, not as the card's %s", obj.Kind, obj.Name, rb.Action, obj.Action)
			}
		}
	}
	for key, obj := range byKey {
		if !cardedKeys[key] {
			return fmt.Errorf("the object %s %s was not on the card", obj.Kind, obj.Name)
		}
	}
	cardedGov, _ := json.Marshal(carded.Governs)
	rebuiltGov, _ := json.Marshal(rebuilt.Governs)
	if string(cardedGov) != string(rebuiltGov) {
		return fmt.Errorf("the plan's governs rebuild differently than the card's")
	}
	return nil
}

// verifyDepotObjects holds a depot plan made after the import to the
// card: every carded object reads unchanged, and each dropped one
// still dropped (no prune was asked for).
func verifyDepotObjects(carded, after *store.AskPlan) error {
	byKey := map[string]store.AskPlanObject{}
	for _, o := range after.Objects {
		byKey[o.Kind+"\x00"+o.Name] = o
	}
	cardedKeys := make(map[string]bool, len(carded.Objects))
	for _, obj := range carded.Objects {
		key := obj.Kind + "\x00" + obj.Name
		cardedKeys[key] = true
		rb, ok := byKey[key]
		if !ok {
			return fmt.Errorf("the object %s %s is gone after the import", obj.Kind, obj.Name)
		}
		if !sameStrings(rb.Keeps, obj.Keeps) || !sameStrings(rb.Shadows, obj.Shadows) {
			return fmt.Errorf("the object %s %s keeps or shadows differently after the import", obj.Kind, obj.Name)
		}
		switch obj.Action {
		case "create", "change", "unchanged":
			if rb.Action != "unchanged" {
				return fmt.Errorf("the object %s %s reads %s after the import, not unchanged", obj.Kind, obj.Name, rb.Action)
			}
		case "dropped":
			if rb.Action != "dropped" {
				return fmt.Errorf("the object %s %s reads %s after the import, not dropped", obj.Kind, obj.Name, rb.Action)
			}
		}
	}
	for key, obj := range byKey {
		if !cardedKeys[key] {
			return fmt.Errorf("the object %s %s was not on the card", obj.Kind, obj.Name)
		}
	}
	return nil
}

func sameChanges(a, b []store.AskChange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// packResult is what a carried-out import prints on the ask: its
// counts, the sources it declared, and what it still leaves owed, as
// a terminal apply's first lines and owed lines say them.
func packResult(res *pack.Result, plan *pack.Plan) string {
	return strings.Join(packResultLines(res, plan, true), "\n")
}

// packResultLines is packResult's lines: owedCredential says whether the
// credential's owed lines stand in them. A setup's apply words leave them
// out, since its account step settles the credential next.
func packResultLines(res *pack.Result, plan *pack.Plan, owedCredential bool) []string {
	var lines []string
	if res.Wrote() {
		lines = append(lines, fmt.Sprintf("applied pack %s to %s: %d created, %d changed, %d unchanged", res.Pack, inDepot(res.Depot), res.Created, res.Changed, res.Unchanged))
	} else {
		lines = append(lines, fmt.Sprintf("pack %s is already applied to %s: nothing changed", res.Pack, inDepot(res.Depot)))
	}
	for _, d := range plan.Declared {
		lines = append(lines, fmt.Sprintf("declared source %s: %s", d.Source, d.Action))
		for _, n := range d.Needs {
			if n.Status != "ok" && (owedCredential || n.What != store.SourceEnvFile) {
				lines = append(lines, "still owed: "+n.What+" ("+n.Detail+")")
			}
		}
	}
	return lines
}

// inDepot is where a pack went: its depot, or the yard itself for one
// of kind source (internal/cli's wording).
func inDepot(depot string) string {
	if depot == "" {
		return "the yard"
	}
	return "depot " + depot
}
