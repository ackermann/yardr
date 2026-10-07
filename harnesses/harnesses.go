// Package harnesses carries, inside the binary, what yardr knows of each
// harness kind that herdr has no field for: one file per kind, <kind>.yaml.
// Whether an agent is idle, working, blocked or done is herdr's to decide,
// from its own files (~/.local/state/herdr/agent-detection); nothing of that
// is repeated here. The files are data, read once by internal/agents, which
// says what each field means (agents.Harness); `yardr harness show <kind>`
// prints one as yardr reads it.
package harnesses

import "embed"

// FS holds each kind's file by its name: claude.yaml is the kind claude. A
// kind with no file here is one yardr knows nothing of beyond what herdr
// does.
//
//go:embed *.yaml
var FS embed.FS
