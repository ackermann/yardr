package source

// Git is what git uses to reach the source's repositories: the credential
// helper git runs for the source's host. The yard copies it with the
// declaration's other command files and never runs it: a depot whose
// origin is on the host points its own git at the copy, which answers
// git's protocol from the source's env file at the moment git asks.
type Git struct {
	// Credential is the helper's program and its arguments, a list run
	// without a shell. Git runs it with get, store or erase and its
	// request on stdin; for the source's host it answers the token the
	// env file sets, for any other host or protocol it answers nothing,
	// and store and erase do nothing.
	Credential []string `json:"credential"`
}

// git reads the key that gives a depot's git the source's token: the
// credential helper git runs. Refused is what would fail at every ask: a
// command that is no list of strings or has no program. Whether the file
// names the credential it carries is held where the whole file is read,
// beside base.
func (r *reader) git(e entry) *Git {
	g := &Git{}
	for _, f := range r.entries(e.v, e.path, "credential") {
		switch f.key {
		case "credential":
			g.Credential = r.command(f, "git/credential")
		}
	}
	return g
}
