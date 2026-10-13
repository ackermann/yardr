package hooks

import (
	"strings"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Repoer is what an Adapter whose source keeps repositories adds: the bead
// an event makes goes in the depot whose repository the event is in, and a
// depot's origin remote says which that is (dispatch).
type Repoer interface {
	// RemoteRepo is the repository of the source the git remote raw points
	// at, as owner/name; "" when raw is no remote of the source's or names
	// no repository.
	RemoteRepo(raw string) string
}

// RepoerFor is the source's Repoer, if it keeps repositories: the one
// built from d, the yard's declared source of that name, when its link
// has a repos pattern, else the builtin one of that name. No name is
// both, so at most one applies. d is nil when the yard declares no such
// source.
func RepoerFor(source string, d *source.Declaration) (Repoer, bool) {
	if dec, ok := declared(source, d, "", ""); ok {
		if dec.D.Link != nil && dec.D.Link.Repos != "" {
			return dec, true
		}
		return nil, false
	}
	r, ok := Adapters[source].(Repoer)
	return r, ok
}

// RemoteRepo is the repository the git remote raw points at: its path
// past "host/", however the URL is written (store.RemoteForm), when the
// link's repos pattern matches it. "" for a remote elsewhere, and for
// one that names no repository.
func (d Declared) RemoteRepo(raw string) string {
	form := store.RemoteForm(raw)
	host, path, _ := strings.Cut(form, "/")
	if !strings.EqualFold(host, d.D.Link.Host) {
		return ""
	}
	repo, _ := d.D.Link.RepoOf(path)
	return repo
}
