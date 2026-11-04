package source

import (
	"embed"
	"io/fs"
	"slices"
)

// knownFS holds the sources yardr carries, each a directory of its name:
// <name>.yaml, the declaration, and beside it the files its commands name,
// as they would stand beside a declaration in a directory of the operator's
// own. They are no packs: a pack may not bring a limits key, because the
// yard runs the command it names, so adding one is the operator's, and what
// runs is the program the operator installed, never one carried here.
//
//go:embed known
var knownFS embed.FS

// Aiquokka is the name of the source for aiquokka, which reads the limits of
// the subscriptions the agents' CLIs are logged in to.
const Aiquokka = "aiquokka"

// known maps a name that yardr source add takes in place of a file to the
// directory of knownFS that holds the source. There is one entry. Another is
// a directory under known/ and a line here; the tests walk the table, so they
// hold it to the same checks.
var known = map[string]string{Aiquokka: "known/" + Aiquokka}

// Known is the source yardr carries under name: the text of its declaration,
// and the directory its command files stand in (CopyCommandFilesFS reads
// them there, with "." for the declaration's directory). ok is false for a
// name yardr carries nothing under.
func Known(name string) (text []byte, files fs.FS, ok bool) {
	dir, ok := known[name]
	if !ok {
		return nil, nil, false
	}
	files, err := fs.Sub(knownFS, dir)
	if err != nil {
		return nil, nil, false
	}
	text, err = fs.ReadFile(files, name+".yaml")
	if err != nil {
		return nil, nil, false
	}
	return text, files, true
}

// KnownNames are the names yardr carries a source under, in order.
func KnownNames() []string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
