package hooks

import (
	"strings"

	"yardr.dev/yardr/internal/source"
)

// KeyFolder is what an Adapter whose source's identity keys are the same in
// any case adds: a key is kept folded, as the source's deliveries carry it,
// so one spelling is stored however it was typed (yardr crew identity).
type KeyFolder interface {
	// FoldKey is an identity key as the source's deliveries carry it.
	FoldKey(key string) string
}

// KeyFolderFor is the source's KeyFolder, if its identity keys fold case:
// the one built from d, the yard's declared source of that name, when its
// identity says so, else the builtin one of that name. No name is both, so
// at most one applies. d is nil when the yard declares no such source.
func KeyFolderFor(name string, d *source.Declaration) (KeyFolder, bool) {
	if dec, ok := declared(name, d, ""); ok && dec.D.Identity != nil && dec.D.Identity.Fold == source.FoldLower {
		return dec, true
	}
	if k, ok := Adapters[name].(KeyFolder); ok {
		return k, true
	}
	return nil, false
}

// FoldKey lowercases a login: GitHub's logins are the same in any case, and
// its deliveries carry them lowercase.
func (GitHub) FoldKey(key string) string { return strings.ToLower(key) }

// FoldKey is an identity key as the source's deliveries carry it: folded,
// as the declaration says.
func (d Declared) FoldKey(key string) string { return strings.ToLower(key) }
