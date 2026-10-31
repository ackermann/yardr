// The sentences of a depot pack's card, whole or summary. The summary
// shape (design yardr-y6gp, section 3): a pack named with its depot whose
// whole card passes the bounds, and which replaces no standing source, is
// written as sentences that name the pack by its digest and each file by
// its hash, with the texts carried behind them in the ask. The row, the
// reading, the weights and the approval are the whole card's: a summary
// holds nothing the whole card does not, and what stands in for the rest
// is a hash the yard reads again.
package store

import (
	"errors"
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/source"
)

// MaxRunPackCarried stands for MaxRunPackFiles on a summary card, whose
// texts stand behind it, not on it: an ask is a note in the store and a
// payload on two calls, so it still has a bound.
const MaxRunPackCarried = 1 << 20

// packDepotDoes is the card of a depot pack: the pack line with its
// counts, one line to a created or changed object, one to a dropped
// object left where it stands, the governs, and each declared source
// as a kind-source card renders it.
func (w *wtx) packDepotDoes(name string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration) ([]string, error) {
	plan := reading.Plan
	counts := packActionCounts(plan)
	does := []string{packDepotLine(name, reading, counts) + ". It asks nothing."}
	for _, flag := range packMappingFlags(reading.Mappings) {
		does = append(does, "the plan uses mapping "+flag+".")
	}
	for _, o := range plan.Objects {
		switch o.Action {
		case "create", "change":
			if o.Kind == "file" {
				ins, del := Counts(o.Diff)
				does = append(does, fmt.Sprintf("%s %s: %s, sha256 %s -> sha256 %s (%d insertions, %d deletions).",
					o.Kind, o.Name, o.Action, or(o.FileOldSHA, "(none)"), or(o.FileNewSHA, "(none)"), ins, del))
				continue
			}
			does = append(does, packObjectLine(o))
		case "dropped":
			does = append(does, fmt.Sprintf("%s %s: dropped by the pack, left where it stands.", o.Kind, o.Name))
		}
		does = append(does, packObjectNotes(o)...)
	}
	does = append(does, packGovernsLines(reading)...)
	for i := range carried {
		lines, err := w.packOneSourceDoes(carried[i], decls[carried[i].Name], false, "")
		if err != nil {
			return nil, err
		}
		does = append(does, lines...)
	}
	return does, nil
}

// packActionCounts counts a plan's objects by their action.
func packActionCounts(plan *AskPlan) map[string]int {
	counts := map[string]int{}
	for _, o := range plan.Objects {
		counts[o.Action]++
	}
	return counts
}

// packDepotLine opens a depot pack's card, whole or summary: the pack, its
// depot and its counts, without an end, for each shape to finish.
func packDepotLine(name string, reading *PackReading, counts map[string]int) string {
	kind := reading.Kind
	if kind == "" {
		kind = "of no kind"
	} else {
		kind = "kind " + kind
	}
	return fmt.Sprintf("pack %s (%s), of %s, into depot %s: %d to create, %d to change, %d unchanged, and %d dropped and left where they stand",
		name, reading.Label, kind, reading.Depot, counts["create"], counts["change"], counts["unchanged"], counts["dropped"])
}

// packObjectLine is the line of a created or changed object that is no
// file, with each change it brings.
func packObjectLine(o AskPlanObject) string {
	line := fmt.Sprintf("%s %s: %s", o.Kind, o.Name, o.Action)
	if len(o.Changes) > 0 {
		parts := make([]string, len(o.Changes))
		for i, c := range o.Changes {
			parts[i] = fmt.Sprintf("%s: %s -> %s", c.Field, or(c.Old, "none"), or(c.New, "none"))
		}
		line += " (" + strings.Join(parts, "; ") + ")"
	}
	return line + "."
}

// packObjectNotes are the lines an object has whatever its action: what
// its cmd sets, what it keeps, and what it shadows, each a fact about what
// the yard will run.
func packObjectNotes(o AskPlanObject) []string {
	var out []string
	if len(o.Env) > 0 {
		out = append(out, fmt.Sprintf("%s %s: cmd sets %s before the script.", o.Kind, o.Name, strings.Join(o.Env, " ")))
	}
	for _, k := range o.Keeps {
		out = append(out, fmt.Sprintf("%s %s: keeps %s.", o.Kind, o.Name, k))
	}
	for _, s := range o.Shadows {
		out = append(out, fmt.Sprintf("%s %s: shadows %s.", o.Kind, o.Name, s))
	}
	return out
}

// packGovernsLines say which flow the depot's beads would move to.
func packGovernsLines(reading *PackReading) []string {
	var out []string
	for _, g := range reading.Plan.Governs {
		if g.Now != "" && g.Now != g.Flow {
			if g.Type != "" {
				out = append(out, fmt.Sprintf("depot %s's beads of type %s would follow flow %s; they follow %s now.", reading.Depot, g.Type, g.Flow, g.Now))
			} else {
				out = append(out, fmt.Sprintf("depot %s would follow flow %s; it follows %s now.", reading.Depot, g.Flow, g.Now))
			}
		}
	}
	return out
}

// packSummary turns the whole card it is given into the summary shape
// where the row writes one, and says whether it did. Where it does not,
// the card stands, and the whole-card bounds refuse it or pass it. named
// is whether the command names the depot: a bare re-apply is never
// summarised, nor a card with no digest to bind it, nor a replacement,
// whose old side only a whole card's sentences show (section 3.5).
func (w *wtx) packSummary(card *AskRun, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration, named bool, payload int) (bool, error) {
	if !named || reading.Digest == "" ||
		payload <= MaxRunPackFiles && len(card.Does) <= MaxRunSourceCardLines {
		return false, nil
	}
	replaces, err := w.packReplaces(carried)
	if err != nil || replaces {
		return false, err
	}
	name := card.Pack.Name
	if payload > MaxRunPackCarried {
		return false, fmt.Errorf("pack %s: its carried file texts and diffs run to %d bytes: a summary card carries at most %d together, so this is the operator's to type in a terminal (yardr pack check %s shows it there)", name, payload, MaxRunPackCarried, name)
	}
	first, err := w.packFirstInstall(name, reading.Depot)
	if err != nil {
		return false, err
	}
	does, files := packSummaryDoes(name, reading, carried, decls, first)
	for i := range does {
		does[i] = safeSourceCardLine(does[i])
	}
	card.Does, card.Pack.Summary, card.Pack.Files = does, true, files
	return true, nil
}

// packReplaces says whether the import would replace a standing source or
// the copies one left: packReplacement has already read the copies into
// each carried source.
func (w *wtx) packReplaces(carried []AskSource) (bool, error) {
	for i := range carried {
		_, _, err := sourceIn(w.Tx, carried[i].Name)
		if err == nil || len(carried[i].Inventory) > 0 {
			return true, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return false, err
		}
	}
	return false, nil
}

// packSummaryDoes is the summary's sentences, in section 3.1's order, with
// each file's place among them. Every line but a file's is owed: a mapping,
// a value the approval sets, a dropped object or a source is a decision, so
// the files take what room of the bound those leave. Where they do not all
// fit, the last room goes to one closing line that counts the rest. Lines
// that are owed and alone pass the bound are the bound's to refuse.
func packSummaryDoes(name string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration, first bool) ([]string, []AskPackFile) {
	head := []string{packSummaryLine(name, reading)}
	if first {
		head = append(head, fmt.Sprintf("this is a first install: depot %s holds no import of pack %s.", reading.Depot, name))
	}
	for _, flag := range packMappingFlags(reading.Mappings) {
		head = append(head, "the plan uses mapping "+flag+".")
	}
	objects := packSummaryObjects(reading.Plan)
	objects = append(objects, packGovernsLines(reading)...)
	for i := range carried {
		objects = append(objects, packSummarySource(carried[i], decls[carried[i].Name]))
	}
	closing := packSummaryCounts(reading.Plan)

	var fileObjects []AskPlanObject
	for _, o := range reading.Plan.Objects {
		if packCarriesFile(o) {
			fileObjects = append(fileObjects, o)
		}
	}
	room := MaxRunSourceCardLines - len(head) - len(objects) - 2 // the closing counts and full text lines
	lined := len(fileObjects)
	if lined > room {
		lined = max(room-1, 0) // one line of the room counts the rest
	}

	does := head
	files := make([]AskPackFile, 0, len(fileObjects))
	foldedBytes := 0
	for i, o := range fileObjects {
		if i >= lined {
			files = append(files, AskPackFile{Name: o.Name, Line: -1})
			foldedBytes += packFileFullTextBytes(o)
			continue
		}
		files = append(files, AskPackFile{Name: o.Name, Line: len(does)})
		ins, del := Counts(o.Diff)
		does = append(does, fmt.Sprintf("%s %s: %s, sha256 %s -> sha256 %s, %d bytes (%d insertions, %d deletions).",
			o.Kind, o.Name, o.Action, or(o.FileOldSHA, "(none)"), or(o.FileNewSHA, "(none)"), len(o.Text), ins, del))
	}
	if folded := len(fileObjects) - lined; folded > 0 {
		does = append(does, fmt.Sprintf("%s, %d bytes, stand in the full text without a line of their own, under digest sha256:%s and each under its own sha256 there.",
			countNoun(folded, "more file"), foldedBytes, reading.Digest))
	}
	does = append(does, objects...)
	does = append(does, closing)
	count, size := packFullText(fileObjects, carried)
	does = append(does, fmt.Sprintf("the full text stands behind this card, on this bead's page: %s, %d bytes.", countNoun(count, "file"), size))
	return does, files
}

// packFileFullTextBytes counts what the full-text view opens for one file:
// a changed file's diff and new text, or a created file's new text alone.
func packFileFullTextBytes(o AskPlanObject) int {
	n := len(o.Text)
	if o.Action == "change" {
		n += len(o.Diff)
	}
	return n
}

// packCarriesFile says whether a plan object is a file the import would
// write: what the card names one by one, and the unit its text is read by.
func packCarriesFile(o AskPlanObject) bool {
	return o.Kind == "file" && (o.Action == "create" || o.Action == "change")
}

// packSummaryLine is the summary's first line: the whole card's, with the
// digest in the words pack check prints it and the files that run.
func packSummaryLine(name string, reading *PackReading) string {
	line := fmt.Sprintf("%s, digest sha256:%s. It asks nothing.", packDepotLine(name, reading, packActionCounts(reading.Plan)), reading.Digest)
	if len(reading.Programs) > 0 {
		line += fmt.Sprintf(" It runs %s of the pack: %s.", countNoun(len(reading.Programs), "program"), strings.Join(reading.Programs, ", "))
	}
	return line
}

// packSummaryObjects are the lines of the objects that are no file: a kind
// and an action to a line where its objects carry nothing but their names,
// and the whole card's own line where one sets a value or is dropped.
func packSummaryObjects(plan *AskPlan) []string {
	type group struct{ kind, action string }
	var order []group
	names := map[group][]string{}
	var out []string
	for _, o := range plan.Objects {
		switch {
		case packCarriesFile(o):
		case o.Action == "dropped":
			out = append(out, fmt.Sprintf("%s %s: dropped by the pack, left where it stands.", o.Kind, o.Name))
		case o.Action != "create" && o.Action != "change":
		case len(o.Changes) > 0:
			out = append(out, packObjectLine(o))
		default:
			g := group{o.Kind, o.Action}
			if names[g] == nil {
				order = append(order, g)
			}
			names[g] = append(names[g], o.Name)
		}
		out = append(out, packObjectNotes(o)...)
	}
	grouped := make([]string, 0, len(order))
	for _, g := range order {
		kinds := g.kind
		if kinds != "crew" {
			kinds += "s"
		}
		grouped = append(grouped, fmt.Sprintf("%s to %s: %s.", kinds, g.action, strings.Join(names[g], ", ")))
	}
	return append(grouped, out...)
}

// packSummarySource names a source the import adds, with its command files
// by program and hash: no summary card replaces one, and its declaration
// stands whole in the full text.
func packSummarySource(carried AskSource, d *source.Declaration) string {
	line := fmt.Sprintf("source %s: added", carried.Name)
	if addr := d.Address(); addr != "" {
		line += ", its calls go to " + addr
	}
	if len(carried.Files) == 0 {
		return line + "."
	}
	parts := make([]string, len(carried.Files))
	for i, f := range carried.Files {
		parts[i] = fmt.Sprintf("%s sha256 %s", f.Program, f.SHA256)
	}
	return line + ", with command files " + strings.Join(parts, ", ") + "."
}

// packSummaryCounts is the closing count over the whole plan, in pack
// check's words.
func packSummaryCounts(plan *AskPlan) string {
	counts := packActionCounts(plan)
	line := fmt.Sprintf("in all: %d to create, %d to change, %d unchanged, %d not in the pack",
		counts["create"], counts["change"], counts["unchanged"], counts["not_in_pack"])
	if n := counts["dropped"]; n > 0 {
		line += fmt.Sprintf(", %d no longer in it", n)
	}
	if n := counts["takes"]; n > 0 {
		line += fmt.Sprintf(", %d of the yard's taken", n)
	}
	return line + "."
}

// packFullText counts what stands behind the card: each file the import
// would write, and each declaration with its command files.
func packFullText(files []AskPlanObject, carried []AskSource) (count, size int) {
	for _, o := range files {
		count++
		size += packFileFullTextBytes(o)
	}
	for _, c := range carried {
		count++
		size += len(c.Text)
		for _, f := range c.Files {
			count++
			size += len(f.Text)
		}
	}
	return count, size
}
