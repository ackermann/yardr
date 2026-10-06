// Package packs carries the packs yardr ships inside the binary, so one can
// be named where a directory would be given: `yardr pack check starter`
// works with no checkout beside the binary. A pack here is data, read as
// any other (internal/pack) and installed under the yard's home by an
// import; the installed copies are what a yard reads.
package packs

import "embed"

// FS holds each embedded pack as a directory of its name: starter/pack.yaml
// and the files it names.
//
//go:embed starter
var FS embed.FS

// Embedded is how the record of an import begins when the pack came from
// here and not from a directory; the yardr build follows it.
const Embedded = "embedded"
