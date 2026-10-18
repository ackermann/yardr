// Preset grants (design yardr-7d4e, B7): `yardr source grant <source>
// --preset <name>` grants one preset's calls of one source, in a terminal
// and as a run ask. The preset is resolved against the packs yardr embeds:
// the one pack of kind source declaring the source, and its preset of that
// name with the calls it grants of the source.
package pack

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// PresetOfSource resolves the preset of the source against the packs in
// fsys: the one pack of kind source declaring the source, and its preset
// of that name. It answers the pack's name and the preset's calls of the
// source, in file order. A source no pack declares, two packs declaring
// one source, an unknown preset, and a preset granting nothing of the
// source are each refused with the terminal grant by name, which is what
// is left where no preset names the calls.
func PresetOfSource(fsys fs.FS, source, preset string) (string, []string, error) {
	byName := func(what string) string {
		return fmt.Sprintf("yardr source grant %s %s --group <group> | --crew <crew member>", source, what)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return "", nil, err
	}
	var declaring []string
	presets := map[string][]Preset{}
	kinds := map[string]string{}
	for _, e := range entries {
		if _, err := fs.Stat(fsys, e.Name()+"/"+File); err != nil {
			continue
		}
		sub, err := fs.Sub(fsys, e.Name())
		if err != nil {
			continue
		}
		p, err := Load(sub, e.Name())
		if err != nil {
			return "", nil, err
		}
		kinds[p.Name] = p.Kind
		if slices.ContainsFunc(p.Declared, func(d Declared) bool { return d.Name == source }) {
			declaring = append(declaring, p.Name)
			presets[p.Name] = p.Presets
		}
	}
	slices.Sort(declaring)
	switch {
	case len(declaring) == 0:
		return "", nil, fmt.Errorf("source %s: no embedded pack declares it, so no preset names its calls: grant by name in a terminal (%s)", source, byName("read|<call>"))
	case len(declaring) > 1:
		return "", nil, fmt.Errorf("the source %s is declared by packs %s: a preset grant names one pack's preset, so grant by name in a terminal (%s)", source, strings.Join(declaring, ", "), byName("read|<call>"))
	}
	name := declaring[0]
	if kinds[name] != KindSource {
		return "", nil, fmt.Errorf("pack %s declares the source %s and is of kind %s: only a pack of kind source carries presets, so grant by name in a terminal (%s)", name, source, orKind(kinds[name]), byName("read|<call>"))
	}
	i := slices.IndexFunc(presets[name], func(pr Preset) bool { return pr.Name == preset })
	if i < 0 {
		return "", nil, fmt.Errorf("pack %s has no preset %s (yardr pack show %s prints its presets numbered): grant by name in a terminal (%s)", name, preset, name, byName("read|<call>"))
	}
	j := slices.IndexFunc(presets[name][i].Grants, func(g CallsAsked) bool { return g.Source == source })
	if j < 0 {
		return "", nil, fmt.Errorf("the preset %s of pack %s grants nothing of the source %s: grant by name in a terminal (%s)", preset, name, source, byName("read|<call>"))
	}
	return name, slices.Clone(presets[name][i].Grants[j].Calls), nil
}

// orKind says a pack's kind where the sentence needs one for a pack that
// says none.
func orKind(kind string) string {
	if kind == "" {
		return "of no kind"
	}
	return kind
}
