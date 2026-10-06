// Package peer holds the script a yard runs to give back the code of a
// bead it worked for a linked yard (see README.md), and its tests.
// return-branch is embedded, so a yardr that was installed without a
// checkout can still set one up: yardr peer intake writes it to
// <home>/scripts/.
package peer

import _ "embed"

// ReturnBranch is the return-branch script as it was when this yardr was
// built.
//
//go:embed return-branch
var ReturnBranch []byte
