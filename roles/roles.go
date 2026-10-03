// Package roles carries the default role prompts in the binary, so
// `yardr init` can install them into a new yard's home. The installed copies
// are the operator's to edit; these are only where they start.
//
// They are generic: no person, rig or language, since every yard installs
// them. `yardr init` puts the operator's name where they say {{operator}}
// and the mayor resident's where they say {{mayor}}; a rig says how it is
// worked in its own repo, under .yardr/roles/.
package roles

import "embed"

// FS holds mayor.md, builder.md, merger.md and reviewer.md.
//
//go:embed *.md
var FS embed.FS

// Stages holds the starter stage files `yardr rig init` writes beside the
// flows it seeds, as stages/<kind>/<flow>/<stage>.md: kind is git or dir,
// flow the flow's name (default, type/epic). They say what each stage asks,
// which the role prompts above leave to them (docs/groups.md), and are as
// generic as the roles, with the same placeholders.
//
//go:embed stages
var Stages embed.FS
