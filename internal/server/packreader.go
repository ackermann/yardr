package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/packs"
)

// packReaderFor is the reading of the packs yardr embeds that the
// server registers on its store (store.SetPackReader): a pack apply run
// ask is carded from the binary this server runs, never from a
// directory, a URL or an earlier import's record. home is the yard's,
// where a depot plan reads the installed files it would change.
func packReaderFor(home string) store.PackReader {
	return func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
		return readEmbeddedPack(ctx, name, depot, mappings, yard, home)
	}
}

// presetReaderFor is the reading of one preset of one source that the
// server registers on its store (store.SetPresetReader): a preset grant
// run ask is carded from the binary this server runs, never from a
// directory or an earlier import's record. The preset is resolved by
// source: the one embedded pack of kind source declaring it.
func presetReaderFor() store.PresetReader {
	return func(source, preset string) (*store.PresetReading, error) {
		name, calls, err := pack.PresetOfSource(packs.FS, source, preset)
		if err != nil {
			return nil, err
		}
		return &store.PresetReading{Pack: name, Preset: preset, Label: packs.Embedded + " in yardr " + version.Describe(), Build: version.Describe(), Calls: calls}, nil
	}
}

// readEmbeddedPack reads the embedded pack of a name: its kind, each
// declared source's whole text with each command file's bytes, and for
// a depot kind the plan built against the named depot with the named
// mappings, or, naming neither, against the depot that already holds
// the pack, as a re-apply always has.
func readEmbeddedPack(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API, home string) (*store.PackReading, error) {
	if _, err := fs.Stat(packs.FS, name+"/pack.yaml"); err != nil {
		return nil, fmt.Errorf("pack %s: yardr embeds no pack of that name: a pack that is a directory, a URL or an earlier import's record is the operator's to apply in a terminal (yardr pack check %s shows its plan there)", name, name)
	}
	sub, err := fs.Sub(packs.FS, name)
	if err != nil {
		return nil, err
	}
	label := packs.Embedded + " in yardr " + version.Describe()
	p, err := pack.Load(sub, label)
	if err != nil {
		return nil, err
	}
	reading := &store.PackReading{Name: p.Name, Kind: p.Kind, Mappings: mappings, Label: label, Build: version.Describe()}
	for _, d := range p.Declared {
		src := store.AskSource{Name: d.Name, Text: d.Text}
		seenProg := map[string]bool{}
		for _, c := range d.Declaration.Commands() {
			if !c.Relative() || seenProg[c.Argv[0]] {
				continue
			}
			seenProg[c.Argv[0]] = true
			data, err := fs.ReadFile(sub, path.Join(declDirOf(d.File), c.Argv[0]))
			if err != nil {
				return nil, fmt.Errorf("declaration %s names the command file %s: %v", d.File, c.Argv[0], err)
			}
			sum := sha256.Sum256(data)
			src.Files = append(src.Files, store.AskFile{Program: c.Argv[0], Text: string(data), SHA256: hex.EncodeToString(sum[:])})
		}
		reading.Sources = append(reading.Sources, src)
	}
	for _, pr := range p.Presets {
		var grants []store.AskPresetGrant
		for _, g := range pr.Grants {
			grants = append(grants, store.AskPresetGrant{Source: g.Source, Calls: append([]string{}, g.Calls...)})
		}
		reading.Presets = append(reading.Presets, store.AskPreset{Name: pr.Name, Grants: grants})
	}
	if p.Kind == pack.KindSource {
		return reading, nil
	}
	target := depot
	if target == "" {
		recs, err := yard.Packs(ctx)
		if err != nil {
			return nil, err
		}
		var depots []string
		for _, r := range recs {
			if r.Pack == p.Name && !contains(depots, r.Depot) {
				depots = append(depots, r.Depot)
			}
		}
		switch {
		case len(depots) == 0:
			return nil, fmt.Errorf("pack %s: this yard has no standing import of it, so there is nothing to re-apply: a first install is asked with its depot named and the mappings that answer its plan (--run \"pack apply %s --depot <depot>\"); a plan that still asks is the operator's to type in a terminal (yardr pack check %s --depot <depot> shows its plan there)", name, name, name)
		case len(depots) > 1:
			return nil, fmt.Errorf("pack %s: this yard holds it in depots %s, so with no depot named there is no single depot to re-apply to: the ask names which (--run \"pack apply %s --depot <depot>\"; yardr pack check %s --depot <depot> shows its plan)", name, strings.Join(depots, ", "), name, name)
		}
		target = depots[0]
	}
	plan, err := pack.Build(ctx, yard, p, packOptions(target, home, mappings))
	if err != nil {
		return nil, err
	}
	// No mappings read exactly as a re-apply always has, byte for byte;
	// mappings answer what they name, and leave the rest still asking.
	answers, open, calls := pack.Answers{}, map[string]map[string]bool(nil), map[string]map[string]bool(nil)
	if !mappings.IsZero() {
		if answers, open, calls, err = mappingAnswers(ctx, yard, plan, mappings, home); err != nil {
			return nil, err
		}
	}
	reading.Depot = target
	reading.Plan = askPlanOf(sub, plan, answers, open, calls)
	reading.Digest, reading.Programs = plan.Digest, plan.Programs
	return reading, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// declDirOf is a declaration file's directory in its pack, "" at its root.
func declDirOf(file string) string {
	if dir := path.Dir(file); dir != "." {
		return dir
	}
	return ""
}

// askPlanOf is a depot plan as a card holds it, with each created or
// changed file's text as the import would install it (filled) and its
// unified diff against the installed text, computed here from the two
// texts the file's hashes already name. What the mappings already
// answer, in answers, open and calls, is no part of what it still asks.
func askPlanOf(sub fs.FS, plan *pack.Plan, answers pack.Answers, open, calls map[string]map[string]bool) *store.AskPlan {
	out := &store.AskPlan{}
	for _, q := range plan.Missing(answers) {
		out.Missing = append(out.Missing, q.ID+": "+q.Ask)
	}
	for _, ot := range plan.OpenThreads {
		for _, a := range ot.Asks {
			if _, done := open[ot.CrewMember][a]; !done {
				out.Asks = append(out.Asks, fmt.Sprintf("let %s open threads in %s", ot.CrewMember, a))
			}
		}
	}
	for _, c := range plan.Calls {
		for _, a := range c.Asks {
			if _, done := calls[c.Grantee().String()][pack.CallKey(c.Source, a)]; !done {
				out.Asks = append(out.Asks, fmt.Sprintf("let %s make the call %s of the source %s", c.Grantee(), a, c.Source))
			}
		}
	}
	for _, e := range plan.Expose {
		for _, a := range e.Asks {
			out.Asks = append(out.Asks, fmt.Sprintf("expose %s to the peer %s", e.CrewMember, a))
		}
	}
	out.Refused = plan.Refused
	out.Edited = plan.EditedFiles()
	for _, o := range plan.Objects {
		ao := store.AskPlanObject{Kind: o.Kind, Name: o.Name, Action: o.Action, Keeps: o.Keeps, Shadows: o.Shadows, Env: o.Env}
		for _, ch := range o.Changes {
			ao.Changes = append(ao.Changes, store.AskChange{Field: ch.Field, Old: planValue(ch.Old), New: planValue(ch.New)})
		}
		if o.Kind == "file" && o.File != nil && (o.Action == pack.Create || o.Action == pack.Change) {
			ao.FileNewSHA = o.File.SHA256
			oldText, hasOld := "", false
			if data, err := os.ReadFile(o.File.To); err == nil {
				hasOld, oldText = true, string(data)
				sum := sha256.Sum256(data)
				ao.FileOldSHA = hex.EncodeToString(sum[:])
			}
			if raw, err := fs.ReadFile(sub, o.Name); err == nil {
				if o.File.Fill {
					raw = pack.Fill(raw, plan.Fill)
				}
				ao.Text = string(raw)
			}
			if !utf8.ValidString(ao.Text) || hasOld && !utf8.ValidString(oldText) {
				out.NonText = append(out.NonText, o.Name)
			}
			ao.Diff = store.Unified(oldText, ao.Text)
		}
		out.Objects = append(out.Objects, ao)
	}
	for _, g := range plan.Governs {
		out.Governs = append(out.Governs, store.AskGovern{Type: g.Type, Flow: g.Flow, Now: g.Now})
	}
	return out
}

// planValue is a plan change's value as a card prints it.
func planValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
