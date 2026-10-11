package hooks

import "strings"

// KeyFolder is what an Adapter whose source's identity keys are the same in
// any case adds: a key is kept folded, as the source's deliveries carry it,
// so one spelling is stored however it was typed (yardr crew identity).
type KeyFolder interface {
	// FoldKey is an identity key as the source's deliveries carry it.
	FoldKey(key string) string
}

// KeyFolderFor is the source's KeyFolder, if its identity keys fold case.
func KeyFolderFor(source string) (KeyFolder, bool) {
	k, ok := Adapters[source].(KeyFolder)
	return k, ok
}

// FoldKey lowercases a login: GitHub's logins are the same in any case, and
// its deliveries carry them lowercase.
func (GitHub) FoldKey(key string) string { return strings.ToLower(key) }
