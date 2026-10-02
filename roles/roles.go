// Package roles carries the default role prompts in the binary, so
// `yardr init` can install them into a new yard's home. The installed copies
// are the operator's to edit; these are only where they start.
//
// They are generic: no person, rig or language, since every yard installs
// them. `yardr init` puts the operator's name where they say {{operator}}; a
// rig says how it is worked in its own repo, under .yardr/roles/.
package roles

import "embed"

// FS holds mayor.md, builder.md, merger.md and reviewer.md.
//
//go:embed *.md
var FS embed.FS
