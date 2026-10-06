// Package assembly holds the reference assembly scripts (see README.md) and
// their tests. merge-local is embedded, so a yardr that was installed
// without a checkout can still set up an assembly: yardr setup writes it to
// <home>/scripts/.
package assembly

import _ "embed"

// MergeLocal is the merge-local script as it was when this yardr was built.
//
//go:embed merge-local
var MergeLocal []byte
