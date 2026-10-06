// Package packs carries the packs yardr ships inside the binary, so one can
// be named where a directory would be given: `yardr pack check starter`
// works with no checkout beside the binary. A pack here is data, read as
// any other (internal/pack) and installed under the yard's home by an
// import; the installed copies are what a yard reads.
package packs

import "embed"

// FS holds each embedded pack as a directory of its name: starter/pack.yaml
// and the files it names. starter is written for a depot that is a git
// repository, dir for one that is a plain directory.
//
//go:embed starter dir
var FS embed.FS

// Starter and Dir are the embedded packs by the kind of depot each is
// written for: a git depot's beads are built on branches and landed on a
// base, a dir depot's are worked in the directory itself and land nowhere,
// so neither pack's roles and flows fit the other kind.
const (
	Starter = "starter"
	Dir     = "dir"
)

// Embedded is how the record of an import begins when the pack came from
// here and not from a directory; the yardr build follows it.
const Embedded = "embedded"
