// Package packs carries the packs yardr ships inside the binary, so one can
// be named where a directory would be given: `yardr pack check starter`
// works with no checkout beside the binary. A pack here is data, read as
// any other (internal/pack) and installed under the yard's home by an
// import; the installed copies are what a yard reads.
package packs

import (
	"embed"
	"io/fs"
)

// FS holds each embedded pack as a directory of its name: starter/pack.yaml
// and the files it names. starter is written for a depot that is a git
// repository, dir for one that is a plain directory. contributors is for a
// git depot too, beside the flow the depot follows: what comes to its
// repository from outside, an issue or a pull request, as a bead. design is
// beside the depot's flow too: the lane a bead of type design takes, from a
// drafted document to a decision. Of kind
// source, for the yard itself: linear and github, each service's
// declaration and the commands it names, and one pack per service that
// tells a user outside the yard, a declaration with the notify key that
// sends and no commands.
//
//go:embed starter dir contributors design linear github pushover telegram
var FS embed.FS

// Starter and Dir are the embedded packs by the kind of depot each is
// written for: a git depot's beads are built on branches and landed on a
// base, a dir depot's are worked in the directory itself and land nowhere,
// so neither pack's roles and flows fit the other kind.
const (
	Starter = "starter"
	Dir     = "dir"
)

// Contributors is the embedded pack for what comes to a git depot's
// repository from outside. It is no depot's default: it has flows for the
// types issue and pr alone, and is imported beside the pack the depot
// follows for its own work.
const Contributors = "contributors"

// Design is the embedded pack for a git depot's designs. It is no depot's
// default either: it has one flow, for the type design, and is imported
// beside the pack the depot follows for its own work.
const Design = "design"

// Linear is the embedded pack for Linear itself, of kind source: it is
// imported into the yard, and into no depot.
const Linear = "linear"

// GitHub is the embedded pack for GitHub itself, of kind source: it is
// imported into the yard, and into no depot.
const GitHub = "github"

// Embedded is how the record of an import begins when the pack came from
// here and not from a directory; the yardr build follows it.
const Embedded = "embedded"

// Carries reports whether the binary carries a pack of that name: a
// yard without its source is told `yardr pack apply <name>` brings it.
func Carries(name string) bool {
	_, err := fs.Stat(FS, name+"/pack.yaml")
	return err == nil
}
