// Package refinery holds the reference refinery scripts (see README.md) and
// their tests. merge-local is embedded, so a yardr that was installed
// without a checkout can still set up a refinery: yardr setup writes it to
// <home>/scripts/.
package refinery

import _ "embed"

// MergeLocal is the merge-local script as it was when this yardr was built.
//
//go:embed merge-local
var MergeLocal []byte
