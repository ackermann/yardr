// The pack apply row of the run-ask list (design yardr-bht9, B2): a pack
// yardr embeds, of kind source or of a depot kind whose plan asks
// nothing, proposed with no flag. The asker sends argv alone: the bytes
// the card shows are the server's own, read from its binary inside the
// write that stores the ask through the reading it registered
// (SetPackReader), and whatever carried bytes the writer sent are thrown
// away with its Does and Seen. The approval records "approved, to be
// carried out"; the server's carrying out is B3.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/udiff"
)

// PackReading is what the server's reading of one embedded pack hands
// the store: the pack's name and kind, the build label it is embedded
// in, each declared source's whole text with its command files' bytes,
// and, for a depot kind, the depot it would be re-applied to and the
// plan built against the yard. Nothing of it is the asker's.
type PackReading struct {
	Name    string
	Kind    string // pack.KindSource ("source") for a yard pack, else a depot kind
	Depot   string
	Label   string // "embedded in yardr <build>"
	Sources []AskSource
	Plan    *AskPlan
}

// PackReader reads the embedded pack of a name for a pack apply ask:
// the yard is the store's own API, so a depot plan is built from the
// yard's records as the write that stores the ask holds them. An error
// refuses the ask, storing nothing, in the reader's words.
type PackReader func(ctx context.Context, name string, yard API) (*PackReading, error)

// SetPackReader tells the store how its owning process reads the packs
// yardr embeds, as SetYard tells it what only that process knows. A
// store no server owns has none, and a pack ask of it is refused naming
// the server.
func (s *Store) SetPackReader(r PackReader) { s.packReader = r }

// packReaderOf is the reading registered on the store this write
// belongs to, nil where there is none.
func (w *wtx) packReaderOf() PackReader {
	if w.store == nil {
		return nil
	}
	return w.store.packReader
}

// parsePackApply reads the words of pack apply <name>: one bare name
// and no flag, since a flag shapes an import and a card answers none.
func parsePackApply(rest []string) ([]string, error) {
	const use = "yardr pack apply <name>"
	for _, word := range rest {
		if strings.HasPrefix(word, "-") {
			return nil, fmt.Errorf("--run takes %s, and pack apply with %s shapes the import, which a card answers none of: it is the operator's to type in a terminal (yardr pack check %s shows its plan there)", use, word, "<name>")
		}
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one pack's name, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// checkPackApply is applying a pack's own check as the asker: it is the
// operator's, proposable where the server registered its reading, and
// refused naming the server where none did.
func checkPackApply(ctx context.Context, w *wtx, run AskRun, actor string) error {
	if w.packReaderOf() == nil {
		return fmt.Errorf("pack %s: no server has registered its reading of the packs yardr embeds on this store, so no card can be written from the binary that runs the yard: a pack is proposed from a yard its server runs", run.Argv[2])
	}
	return w.operatorOnly(ctx, actor, "apply a pack", Askable)
}

// inventoryOf is every file under <home>/sources/<name>/ as the yard
// holds it now: each by its path there, with its SHA-256 and its mode.
// It covers each relative program a declaration names and every entry
// neither declaration names alike, so a copy added, removed, changed
// or chmodded is seen. Empty where the yard has no copies.
func (w *wtx) inventoryOf(name string, carryContents bool) ([]AskInventory, map[string][]byte, error) {
	if w.store == nil || w.store.home == "" {
		return nil, nil, nil
	}
	root := filepath.Join(w.store.home, "sources", name)
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("source %s: cannot inspect its command copy directory: %w", name, err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("source %s: its command copy destination is not a directory: pack apply cannot card what it would remove", name)
	}
	var out []AskInventory
	contents := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("source %s: cannot inventory command copy %s: %w", name, OneLine(filepath.ToSlash(rel)), walkErr)
		}
		if path == root || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("source %s: cannot inspect command copy %s: %w", name, OneLine(path), err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		var data []byte
		symlink := info.Mode()&os.ModeSymlink != 0
		switch {
		case symlink:
			// Hash the link itself, not the file it points to. Reading a
			// link target could disclose a file outside the source copies.
			var target string
			target, err = os.Readlink(path)
			if err == nil {
				var current fs.FileInfo
				current, err = os.Lstat(path)
				if err == nil && !os.SameFile(info, current) {
					err = fmt.Errorf("entry changed while it was being inventoried")
				}
			}
			if err != nil {
				return fmt.Errorf("source %s: cannot inventory symbolic link %s: %w", name, OneLine(rel), err)
			}
			data = []byte(target)
		case info.Mode().IsRegular():
			data, err = readInventoryFile(path, info)
			if err != nil {
				return fmt.Errorf("source %s: cannot inventory command copy %s: %w", name, OneLine(rel), err)
			}
			if carryContents {
				contents[rel] = data
			}
		default:
			return fmt.Errorf("source %s: command copy %s is not a regular file or symbolic link: pack apply cannot card what it would remove", name, OneLine(rel))
		}
		sum := sha256.Sum256(data)
		out = append(out, AskInventory{Path: rel, SHA256: hex.EncodeToString(sum[:]),
			Mode: fmt.Sprintf("%04o", info.Mode().Perm()), Size: int64(len(data)), Symlink: symlink})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, contents, nil
}

// readInventoryFile reads only the regular file the directory walk saw.
// Opening through a symlink, or racing a replacement while reading, makes
// the inventory incomplete and refuses the ask.
func readInventoryFile(path string, want fs.FileInfo) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(want, opened) || opened.Mode().Perm() != want.Mode().Perm() {
		_ = f.Close()
		return nil, fmt.Errorf("entry changed while it was being inventoried")
	}
	data, readErr := io.ReadAll(f)
	after, statErr := f.Stat()
	closeErr := f.Close()
	if readErr != nil {
		return nil, readErr
	}
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(want, pathInfo) || after.Size() != opened.Size() || pathInfo.Size() != after.Size() ||
		!after.ModTime().Equal(opened.ModTime()) || !pathInfo.ModTime().Equal(after.ModTime()) ||
		after.Mode().Perm() != opened.Mode().Perm() || pathInfo.Mode().Perm() != after.Mode().Perm() || int64(len(data)) != after.Size() {
		return nil, fmt.Errorf("entry changed while it was being inventoried")
	}
	return data, nil
}

// packBytesSeen fingerprints a pack reading: its sources' texts, each
// command file's SHA-256, and a depot plan whole. The build label is
// held apart (Seen["pack <name>"]), so a stale approval can say whether
// the label alone moved or the bytes did.
func packBytesSeen(r *PackReading) string {
	var parts []any
	for _, s := range r.Sources {
		parts = append(parts, s.Name, s.Text)
		for _, f := range s.Files {
			parts = append(parts, f.Program, f.SHA256)
		}
	}
	if r.Plan != nil {
		raw, _ := json.Marshal(r.Plan)
		parts = append(parts, string(raw))
	}
	return seen(parts...)
}

// validatePackSource parses a pack source's carried text in the store,
// as the loose row's does, and orders its files as the declaration
// names them. A pack may name an absolute program, the operator's own;
// a loose file may not.
func validatePackSource(carried AskSource, packName string) (*source.Declaration, AskSource, error) {
	d, err := source.Parse("pack source "+carried.Name, []byte(carried.Text))
	if err != nil {
		return nil, AskSource{}, fmt.Errorf("%s", OneLine(err.Error()))
	}
	if carried.Name != d.Name {
		return nil, AskSource{}, fmt.Errorf("the pack carries the source %s, and its declaration names %s: the two must be one", OneLine(carried.Name), d.Name)
	}
	byProgram := map[string]AskFile{}
	for _, f := range carried.Files {
		if _, twice := byProgram[f.Program]; twice {
			return nil, AskSource{}, fmt.Errorf("source %s: the pack carries the command file %s twice", d.Name, OneLine(f.Program))
		}
		if !utf8.ValidString(f.Text) {
			return nil, AskSource{}, fmt.Errorf("source %s: the command file %s is not valid UTF-8 text: a card carries text only, so this pack is the operator's to apply in a terminal (yardr pack check %s)", d.Name, OneLine(f.Program), packName)
		}
		sum := sha256.Sum256([]byte(f.Text))
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			return nil, AskSource{}, fmt.Errorf("source %s: the pack carries %s with sha256 %s, and its bytes hash to %s: nothing was stored", d.Name, OneLine(f.Program), OneLine(f.SHA256), got)
		}
		byProgram[f.Program] = f
	}
	var programs []string
	seenProg := map[string]bool{}
	for _, c := range d.Commands() {
		if c.Relative() && !seenProg[c.Argv[0]] {
			seenProg[c.Argv[0]] = true
			programs = append(programs, c.Argv[0])
		}
	}
	for _, prog := range programs {
		if _, ok := byProgram[prog]; !ok {
			return nil, AskSource{}, fmt.Errorf("source %s: its declaration names the command file %s, and the pack carries no bytes for it", d.Name, OneLine(prog))
		}
	}
	for prog := range byProgram {
		if !seenProg[prog] {
			return nil, AskSource{}, fmt.Errorf("source %s: the pack carries %s, and its declaration names no such command file", d.Name, OneLine(prog))
		}
	}
	files := make([]AskFile, 0, len(programs))
	for _, prog := range programs {
		files = append(files, byProgram[prog])
	}
	carried.Files = files
	return d, carried, nil
}

// packApplyCard is the card of pack apply <name>: the server's own pack
// in the yard's sentences, for a kind-source pack the whole rendering
// of sourceDoes under a pack line, for a depot pack the plan's objects
// (packDepotDoes). Asking refuses what the row refuses; an approval
// reads it again and stale says what moved.
func (w *wtx) packApplyCard(run AskRun, asking bool) (AskRun, error) {
	name := run.Argv[2]
	read := w.packReaderOf()
	if read == nil {
		return AskRun{}, fmt.Errorf("pack %s: no server has registered its reading of the packs yardr embeds on this store, so no card can be written from the binary that runs the yard: a pack is proposed from a yard its server runs", name)
	}
	reading, err := read(context.Background(), name, w.store)
	if err != nil {
		return AskRun{}, err
	}
	if reading == nil || reading.Name != name {
		return AskRun{}, fmt.Errorf("pack %s: the server's reading named another pack: nothing was stored", name)
	}
	declTotal, payload := 0, 0
	carried := make([]AskSource, 0, len(reading.Sources))
	decls := map[string]*source.Declaration{}
	for _, s := range reading.Sources {
		d, norm, err := validatePackSource(s, name)
		if err != nil {
			return AskRun{}, err
		}
		declTotal += len(s.Text)
		decls[d.Name] = d
		carried = append(carried, norm)
	}
	if declTotal > MaxRunPackDeclarations {
		return AskRun{}, fmt.Errorf("pack %s: its declarations run to %d bytes together: a card holds at most %d, so this is the operator's to type in a terminal (yardr pack apply %s)", name, declTotal, MaxRunPackDeclarations, name)
	}
	// What the yard's copies of a replaced source hold, read by the
	// store itself through its home, with each file's diff.
	for i := range carried {
		d := decls[carried[i].Name]
		pay, err := w.packReplacement(&carried[i], d, name)
		if err != nil {
			return AskRun{}, err
		}
		payload += pay
		for _, f := range carried[i].Files {
			payload += len(f.Text)
		}
	}
	seenMap := map[string]string{
		"pack " + name:            reading.Label,
		"pack " + name + " bytes": packBytesSeen(&PackReading{Sources: carried, Plan: reading.Plan}),
	}
	for i := range carried {
		key := "source " + carried[i].Name
		if src, _, err := sourceIn(w.Tx, carried[i].Name); err == nil {
			fingerprint, err := w.sourceSeen(src)
			if err != nil {
				return AskRun{}, err
			}
			seenMap[key] = fingerprint
		} else if errors.Is(err, ErrNotFound) {
			if len(carried[i].Inventory) == 0 {
				seenMap[key] = ""
			} else {
				// A previous removal can leave command copies behind if the
				// process stopped between deleting the source and its files.
				// Applying this pack removes that directory too, so hold the
				// ask to what it contains even though no source record stands.
				seenMap[key] = seen("no source", carried[i].Inventory)
			}
		} else {
			return AskRun{}, err
		}
	}
	card := AskRun{Argv: run.Argv, Seen: seenMap, Carried: carried,
		Pack: &AskPack{Name: name, Kind: reading.Kind, Depot: reading.Depot, Label: reading.Label}}
	if reading.Kind == "source" {
		does, err := w.packSourceDoes(name, reading, carried, decls)
		if err != nil {
			return AskRun{}, err
		}
		card.Does = does
		card.Description = "Import a pack into the yard: set the yard to what the pack says"
		if len(carried) > 0 {
			card.Back = runCommandLine([]string{"source", "rm", carried[0].Name})
		} else {
			card.Back = runCommandLine([]string{"pack", "check", name})
		}
		card.BackBy = "the operator"
	} else {
		if reading.Depot == "" || reading.Plan == nil {
			return AskRun{}, fmt.Errorf("pack %s: it has no single depot to re-apply to: a first install, and one of several depots, is the operator's to type in a terminal (yardr pack check %s --depot <depot> shows its plan there)", name, name)
		}
		for _, group := range [][]string{reading.Plan.Missing, reading.Plan.Asks, reading.Plan.Refused, reading.Plan.Edited, reading.Plan.NonText} {
			if len(group) > 0 {
				return AskRun{}, fmt.Errorf("pack %s: its plan asks or refuses, so no card can apply it: %s: it is the operator's to type in a terminal (yardr pack check %s --depot %s shows its plan there)", name, strings.Join(group, "; "), name, reading.Depot)
			}
		}
		for _, o := range reading.Plan.Objects {
			payload += len(o.Text) + len(o.Diff)
		}
		does, err := w.packDepotDoes(name, reading, carried, decls)
		if err != nil {
			return AskRun{}, err
		}
		card.Does = does
		card.Pack.Plan = reading.Plan
		card.Description = fmt.Sprintf("Import a pack into depot %s: set the yard to what the pack says", reading.Depot)
		card.Back = fmt.Sprintf("no one command takes it back: read the standing state with yardr pack check %s --depot %s in a terminal", name, reading.Depot)
		card.BackBy = "the operator"
	}
	for i := range card.Does {
		card.Does[i] = safeSourceCardLine(card.Does[i])
	}
	if payload > MaxRunPackFiles {
		return AskRun{}, fmt.Errorf("pack %s: its carried file texts and diffs run to %d bytes: a card holds at most %d together, so this is the operator's to type in a terminal (yardr pack check %s shows it there)", name, payload, MaxRunPackFiles, name)
	}
	if len(card.Does) > MaxRunSourceCardLines {
		return AskRun{}, fmt.Errorf("pack %s: its card would run to %d lines: a card is read whole, so this is the operator's to type in a terminal (yardr pack check %s shows it there)", name, len(card.Does), name)
	}
	return card, nil
}

// packReplacement fills carried with what the yard holds of the source
// it would replace: each command file's old and new SHA-256 with its
// diff, the whole destination inventory, and each entry neither
// declaration names as removed. It answers the diff and removed bytes
// the ask carries, for the payload bound. If no source record stands,
// it still cards any leftover files the copy step would replace.
func (w *wtx) packReplacement(carried *AskSource, d *source.Declaration, packName string) (int, error) {
	_, was, sourceErr := sourceIn(w.Tx, carried.Name)
	sourceMissing := errors.Is(sourceErr, ErrNotFound)
	if sourceErr != nil && !sourceMissing {
		return 0, sourceErr
	}
	inv, contents, err := w.inventoryOf(carried.Name, true)
	if err != nil {
		return 0, err
	}
	if sourceMissing && len(inv) == 0 {
		return 0, nil // no source or leftover copies: an adding, held by its absence
	}
	carried.Inventory = inv
	byPath := map[string]AskInventory{}
	for _, e := range inv {
		byPath[e.Path] = e
	}
	newByProg := map[string]AskFile{}
	for _, f := range carried.Files {
		newByProg[f.Program] = f
	}
	union := map[string]bool{}
	var progs []string
	for _, decl := range []*source.Declaration{was, d} {
		if decl == nil {
			continue
		}
		for _, c := range decl.Commands() {
			if c.Relative() && !union[c.Argv[0]] {
				union[c.Argv[0]] = true
				progs = append(progs, c.Argv[0])
			}
		}
	}
	sort.Strings(progs)
	payload := 0
	for _, prog := range progs {
		oldE, hasOld := byPath[prog]
		newF, hasNew := newByProg[prog]
		oldSHA, newSHA := "", ""
		if hasOld {
			oldSHA = oldE.SHA256
		}
		if hasNew {
			newSHA = newF.SHA256
		}
		if sourceMissing && !hasOld {
			continue // a new command file is already shown by sourceDoes
		}
		if hasOld && oldE.Symlink {
			return 0, fmt.Errorf("source %s: the copy of %s is a symbolic link, not a regular text file: a card cannot show what it points to, so this pack is the operator's to apply in a terminal (yardr pack check %s)", carried.Name, OneLine(prog), packName)
		}
		if oldSHA == newSHA {
			continue
		}
		var oldText, newText string
		if hasOld {
			data := contents[prog]
			if !utf8.Valid(data) {
				return 0, fmt.Errorf("source %s: the copy of %s that stands is not valid UTF-8 text: a card carries text only, so this pack is the operator's to apply in a terminal (yardr pack check %s)", carried.Name, prog, packName)
			}
			oldText = string(data)
		}
		if hasNew {
			newText = newF.Text // validated text in validatePackSource
		}
		diff := udiff.Unified(oldText, newText)
		payload += len(diff)
		carried.Old = append(carried.Old, AskFileDiff{Program: prog, OldSHA256: oldSHA, NewSHA256: newSHA, Diff: diff})
	}
	for _, e := range inv {
		if union[e.Path] {
			continue
		}
		rem := AskRemoved{Path: e.Path, SHA256: e.SHA256, Mode: e.Mode, Size: e.Size, Symlink: e.Symlink}
		if data, ok := contents[e.Path]; ok && utf8.Valid(data) {
			rem.Text = string(data)
			payload += len(rem.Text)
		}
		carried.Removed = append(carried.Removed, rem)
	}
	return payload, nil
}

// packSourceDoes is the card of a kind-source pack: the pack line, then
// each source as sourceDoes renders it, with what a replacement changes.
func (w *wtx) packSourceDoes(name string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration) ([]string, error) {
	adds, replaces, files, commands := 0, 0, 0, 0
	fileSet := map[string]bool{}
	for i := range carried {
		if _, _, err := sourceIn(w.Tx, carried[i].Name); err == nil {
			replaces++
		} else if errors.Is(err, ErrNotFound) {
			adds++
		} else {
			return nil, err
		}
		commands += len(decls[carried[i].Name].Commands())
		for _, f := range carried[i].Files {
			if !fileSet[f.Program] {
				fileSet[f.Program] = true
				files++
			}
		}
	}
	var clauses []string
	if adds > 0 {
		clauses = append(clauses, "adds "+countNoun(adds, "source"))
	}
	if replaces > 0 {
		clauses = append(clauses, "replaces "+countNoun(replaces, "source"))
	}
	if files > 0 {
		clauses = append(clauses, fmt.Sprintf("copies %s (%s)", countNoun(files, "command file"), countNoun(commands, "command")))
	} else {
		clauses = append(clauses, "copies no command files")
	}
	does := []string{fmt.Sprintf("pack %s (%s), of kind source, into the yard: it %s, and changes nothing else. It grants no call, makes no hook, and sets no credential's file.",
		name, reading.Label, strings.Join(clauses, " and "))}
	for i := range carried {
		lines, err := w.packOneSourceDoes(carried[i], decls[carried[i].Name])
		if err != nil {
			return nil, err
		}
		does = append(does, lines...)
	}
	return does, nil
}

// packOneSourceDoes is one declared source on a pack card: sourceDoes
// without its adding line (the pack line says it), with a replacement's
// changes after its credential line.
func (w *wtx) packOneSourceDoes(carried AskSource, d *source.Declaration) ([]string, error) {
	full := sourceDoes(d, carried)
	lines := full[1:] // the pack line stands for the adding line
	_, was, err := sourceIn(w.Tx, carried.Name)
	if errors.Is(err, ErrNotFound) {
		if len(carried.Inventory) == 0 {
			return lines, nil
		}
		extra := []string{fmt.Sprintf("the source %s is not in the yard, but its command copy directory has entries this pack would replace or remove.", carried.Name)}
		for _, df := range carried.Old {
			ins, del := udiff.Counts(df.Diff)
			extra = append(extra, safeSourceCardLine(fmt.Sprintf("command file %s would change: sha256 %s -> sha256 %s (%d insertions, %d deletions).",
				df.Program, or(df.OldSHA256, "(none)"), or(df.NewSHA256, "(none)"), ins, del)))
		}
		for _, rem := range carried.Removed {
			kind := ""
			if rem.Symlink {
				kind = ", symbolic link"
			}
			extra = append(extra, safeSourceCardLine(fmt.Sprintf("the copy would remove %s, which neither declaration names: sha256 %s (mode %s%s, %d bytes).",
				rem.Path, rem.SHA256, rem.Mode, kind, rem.Size)))
		}
		out := make([]string, 0, len(lines)+len(extra))
		out = append(out, lines[0])
		out = append(out, extra...)
		out = append(out, lines[1:]...)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var extra []string
	extra = append(extra, fmt.Sprintf("it would replace the source %s the yard has under that name.", carried.Name))
	extra = append(extra, packDeclDiffLines(carried.Name, was, d)...)
	if was != nil {
		grants, err := grantsOf(w.Tx, carried.Name)
		if err != nil {
			return nil, err
		}
		if calls, who := Widened(was, d, grants); len(calls) > 0 {
			names := make([]string, len(who))
			for i, g := range who {
				names[i] = g.String()
			}
			extra = append(extra, fmt.Sprintf("a standing grant of read of source %s widens to %s for %s.", carried.Name, strings.Join(calls, ", "), strings.Join(names, ", ")))
		}
	}
	for _, df := range carried.Old {
		ins, del := udiff.Counts(df.Diff)
		extra = append(extra, safeSourceCardLine(fmt.Sprintf("command file %s would change: sha256 %s -> sha256 %s (%d insertions, %d deletions).",
			df.Program, or(df.OldSHA256, "(none)"), or(df.NewSHA256, "(none)"), ins, del)))
	}
	for _, rem := range carried.Removed {
		kind := ""
		if rem.Symlink {
			kind = ", symbolic link"
		}
		extra = append(extra, safeSourceCardLine(fmt.Sprintf("the copy would remove %s, which neither declaration names: sha256 %s (mode %s%s, %d bytes).",
			rem.Path, rem.SHA256, rem.Mode, kind, rem.Size)))
	}
	// After the credential line, which is the first of lines.
	out := make([]string, 0, len(lines)+len(extra))
	out = append(out, lines[0])
	out = append(out, extra...)
	out = append(out, lines[1:]...)
	return out, nil
}

// packDeclDiffLines is one line to a top-level key of a declaration
// that differs between the yard's and the pack's, old against new:
// every key, not only the ones a plan's declarationDiff compares.
func packDeclDiffLines(name string, was, now *source.Declaration) []string {
	if was == nil {
		return []string{fmt.Sprintf("source %s: the yard's declaration is one this yardr does not read, and the pack's replaces it.", name)}
	}
	var out []string
	for _, key := range []string{"base", "credential", "check", "idempotency", "calls", "notify", "hear", "limits", "thread", "link", "identity", "hook", "events"} {
		old, new := declarationKey(was, key), declarationKey(now, key)
		if old != new {
			out = append(out, safeSourceCardLine(fmt.Sprintf("source %s %s: %s -> %s.", name, key, or(old, "none"), or(new, "none"))))
		}
	}
	return out
}

// declarationKey is the complete value of one behavior-bearing top-level
// key, as compact JSON. A shortened summary can say old and new are alike
// while a parameter, body, hook rule or thread command changed.
func declarationKey(d *source.Declaration, key string) string {
	var v any
	switch key {
	case "base":
		v = d.Base
	case "credential":
		v = d.Credential
	case "check":
		v = d.Check
	case "idempotency":
		v = d.Idempotency
	case "calls":
		if len(d.Calls) == 0 {
			return ""
		}
		calls := append([]source.Call(nil), d.Calls...)
		for i := range calls {
			calls[i].About = "" // author's description does not change what a call does
		}
		v = calls
	case "notify":
		v = d.Notify
	case "hear":
		v = d.Hear
	case "limits":
		v = d.Limits
	case "thread":
		v = d.Thread
	case "link":
		v = d.Link
	case "identity":
		v = d.Identity
	case "hook":
		v = d.Hook
	case "events":
		v = d.Events
	default:
		return ""
	}
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unreadable %s>", key)
	}
	return string(raw)
}

// packDepotDoes is the card of a depot pack: the pack line with its
// counts, one line to a created or changed object, one to a dropped
// object left where it stands, the governs, and each declared source
// as a kind-source card renders it.
func (w *wtx) packDepotDoes(name string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration) ([]string, error) {
	plan := reading.Plan
	counts := map[string]int{}
	for _, o := range plan.Objects {
		counts[o.Action]++
	}
	kind := reading.Kind
	if kind == "" {
		kind = "of no kind"
	} else {
		kind = "kind " + kind
	}
	does := []string{fmt.Sprintf("pack %s (%s), of %s, into depot %s: %d to create, %d to change, %d unchanged, and %d dropped and left where they stand. It asks nothing.",
		name, reading.Label, kind, reading.Depot, counts["create"], counts["change"], counts["unchanged"], counts["dropped"])}
	for _, o := range plan.Objects {
		switch o.Action {
		case "create", "change":
			if o.Kind == "file" {
				ins, del := udiff.Counts(o.Diff)
				does = append(does, fmt.Sprintf("%s %s: %s, sha256 %s -> sha256 %s (%d insertions, %d deletions).",
					o.Kind, o.Name, o.Action, or(o.FileOldSHA, "(none)"), or(o.FileNewSHA, "(none)"), ins, del))
				continue
			}
			line := fmt.Sprintf("%s %s: %s", o.Kind, o.Name, o.Action)
			if len(o.Changes) > 0 {
				parts := make([]string, len(o.Changes))
				for i, c := range o.Changes {
					parts[i] = fmt.Sprintf("%s: %s -> %s", c.Field, or(c.Old, "none"), or(c.New, "none"))
				}
				line += " (" + strings.Join(parts, "; ") + ")"
			}
			does = append(does, line+".")
		case "dropped":
			does = append(does, fmt.Sprintf("%s %s: dropped by the pack, left where it stands.", o.Kind, o.Name))
		}
		for _, k := range o.Keeps {
			does = append(does, fmt.Sprintf("%s %s: keeps %s.", o.Kind, o.Name, k))
		}
		for _, s := range o.Shadows {
			does = append(does, fmt.Sprintf("%s %s: shadows %s.", o.Kind, o.Name, s))
		}
	}
	for _, g := range plan.Governs {
		if g.Now != "" && g.Now != g.Flow {
			if g.Type != "" {
				does = append(does, fmt.Sprintf("depot %s's beads of type %s would follow flow %s; they follow %s now.", reading.Depot, g.Type, g.Flow, g.Now))
			} else {
				does = append(does, fmt.Sprintf("depot %s would follow flow %s; it follows %s now.", reading.Depot, g.Flow, g.Now))
			}
		}
	}
	for i := range carried {
		lines, err := w.packOneSourceDoes(carried[i], decls[carried[i].Name])
		if err != nil {
			return nil, err
		}
		does = append(does, lines...)
	}
	return does, nil
}

// runPackApply records a pack approval as approved, to be carried out:
// the yard's server does the applying after this write (B3), and
// nothing of the pack is written here.
func runPackApply(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error) {
	return "approved, to be carried out", nil
}

// packApplyStale says why a pack approval is refused: the build or the
// pack's bytes moved, a source it would add or replace moved (naming
// the command copy that did), what is owed changed, or a depot plan is
// not the stored one.
func (w *wtx) packApplyStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if was.Pack != nil && was.Pack.Plan != nil {
		wasRaw, _ := json.Marshal(was.Pack.Plan)
		var nowRaw []byte
		if now.Pack != nil && now.Pack.Plan != nil {
			nowRaw, _ = json.Marshal(now.Pack.Plan)
		}
		if string(wasRaw) != string(nowRaw) {
			differ := "its objects"
			if now.Pack != nil && now.Pack.Plan != nil {
				for i, o := range was.Pack.Plan.Objects {
					oRaw, _ := json.Marshal(o)
					var nRaw []byte
					if i < len(now.Pack.Plan.Objects) {
						nRaw, _ = json.Marshal(now.Pack.Plan.Objects[i])
					}
					if string(oRaw) != string(nRaw) {
						differ = fmt.Sprintf("the object %s %s", o.Kind, o.Name)
						break
					}
				}
			}
			return fmt.Errorf("ask %d was written from the plan of pack %s for depot %s, and the plan the yard builds now differs at %s: the card is from before that changed. %s", seq, name, was.Pack.Depot, differ, nothingRan)
		}
	}
	labelKey, bytesKey := "pack "+name, "pack "+name+" bytes"
	if was.Seen[labelKey] != now.Seen[labelKey] || was.Seen[bytesKey] != now.Seen[bytesKey] {
		wasLabel, nowLabel := "", ""
		if was.Pack != nil {
			wasLabel = was.Pack.Label
		}
		if now.Pack != nil {
			nowLabel = now.Pack.Label
		}
		if was.Seen[bytesKey] == now.Seen[bytesKey] {
			return fmt.Errorf("ask %d was written from pack %s as %s embeds it: the pack is unchanged file for file, but the card named the build it was carded from, and this yard runs %s. %s", seq, name, wasLabel, nowLabel, nothingRan)
		}
		return fmt.Errorf("ask %d was written from pack %s as %s embeds it: this yard runs %s, whose pack differs. %s", seq, name, wasLabel, nowLabel, nothingRan)
	}
	for i := range was.Carried {
		srcName := was.Carried[i].Name
		key := "source " + srcName
		if was.Seen[key] == now.Seen[key] {
			continue
		}
		var nowInv []AskInventory
		if i < len(now.Carried) {
			nowInv = now.Carried[i].Inventory
		}
		if moved := inventoryMoved(was.Carried[i].Inventory, nowInv); moved != "" {
			return fmt.Errorf("ask %d was written when the source %s's copies stood as carded: %s since. %s", seq, srcName, moved, nothingRan)
		}
		if was.Seen[key] == "" {
			return fmt.Errorf("ask %d was written when the source %s was not in the yard: it was added since. %s", seq, srcName, nothingRan)
		}
		if err := w.staleSource(seq, srcName, was, now); err != nil {
			return err
		}
		return fmt.Errorf("ask %d was written when the source %s was another: its declaration, its %s or its %s was changed since. %s", seq, srcName, SourceEnvFile, SourceMintCommand, nothingRan)
	}
	return staleCard(seq, was, now)
}

// inventoryMoved names the first entry that stands otherwise between
// two inventories of one source's copies, "" when they are one.
func inventoryMoved(was, now []AskInventory) string {
	nowBy := map[string]AskInventory{}
	for _, e := range now {
		nowBy[e.Path] = e
	}
	wasBy := map[string]bool{}
	for _, e := range was {
		wasBy[e.Path] = true
		if n, ok := nowBy[e.Path]; !ok {
			return fmt.Sprintf("the command copy %s was removed", e.Path)
		} else if n.Symlink != e.Symlink {
			return fmt.Sprintf("the command copy %s changed type", e.Path)
		} else if n.SHA256 != e.SHA256 {
			return fmt.Sprintf("the command copy %s changed", e.Path)
		} else if n.Mode != e.Mode {
			return fmt.Sprintf("the command copy %s changed mode %s -> %s (a chmod)", e.Path, e.Mode, n.Mode)
		} else if n.Size != e.Size {
			return fmt.Sprintf("the command copy %s changed size %d -> %d", e.Path, e.Size, n.Size)
		}
	}
	for _, e := range now {
		if !wasBy[e.Path] {
			return fmt.Sprintf("the command copy %s was added", e.Path)
		}
	}
	return ""
}
