package hooks

import (
	"strings"

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

// RepoerFor is the source's Repoer, if it keeps repositories.
func RepoerFor(source string) (Repoer, bool) {
	r, ok := Adapters[source].(Repoer)
	return r, ok
}

// RemoteRepo is owner/name for a remote on the source's issue host, however
// the URL is written (store.RemoteForm); "" for one elsewhere, and for a
// URL with more or less than an owner and a name.
func (GitHub) RemoteRepo(raw string) string {
	repo, _ := strings.CutPrefix(store.RemoteForm(raw), GitHub{}.IssueHost()+"/")
	if strings.Count(repo, "/") != 1 {
		return ""
	}
	return repo
}
