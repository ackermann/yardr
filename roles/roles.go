// Package roles carries the default role prompts in the binary, so
// `yardr init` can install them into a new yard's home. The installed copies
// are the operator's to edit; these are only where they start.
package roles

import "embed"

// FS holds mayor.md, builder.md, merger.md and reviewer.md.
//
//go:embed *.md
var FS embed.FS
