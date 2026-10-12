package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Code crosses a link through a git remote both yards use, never through
// the link itself (docs/peers.md, "Code that comes back"): the bead message
// says what the work starts from, the yard that built pushes its bead's
// branch, the return names that branch, and the yard that asked fetches
// it. This file is the git of that: which remote a depot's base has, the form
// two yards' URLs for one remote are compared in, whether a base commit can
// be had here and the ref that keeps it, and the taking of a branch that
// came back. All of it runs
// outside a write.

// PeerGit is the git part of a bead message and of its return. A bead
// message says what the work starts from: Base, the branch's name, and
// BaseCommit, with Remote the URL of that branch's remote as the sender
// knows it. A return says where the result is: Branch, at Commit, on
// Remote as the receiver knows it. A Remote is in the form RemoteForm
// gives, so it never carries a credential over a link.
type PeerGit struct {
	Base       string `json:"base,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Remote     string `json:"remote,omitempty"`
}

// The metadata of a bead that says where its result was pushed, which
// contrib/peer/return-branch writes: a bead that came from a peer returns
// with them as the return's git part (returnPeerBead).
const (
	MetaResultBranch = "result.branch"
	MetaResultCommit = "result.commit"
	MetaResultRemote = "result.remote"
)

// GroupRemote is the setting of a peer group that names the remote of the
// depot a returned branch is fetched from; PeerRemote when unset.
const (
	GroupRemote = "remote"
	PeerRemote  = "origin"
)

// RefusedBaseUnknown is the refusal of a bead whose work this yard cannot
// start where the sender's starts: the intake's depot has another remote
// than the bead names, or the base commit cannot be fetched into it.
const RefusedBaseUnknown = "base_unknown"

// peerFetchTimeout bounds one fetch for a bead from a peer: the base of
// one that arrives, while the peer waits for the answer to its post.
const peerFetchTimeout = 20 * time.Second

var (
	gitCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// gitRemoteName is what the name of a remote is held to before it is
	// an argument of git: no option, no URL.
	gitRemoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// gitBranchName is what a peer's name for its base is held to before
	// this yard asks its own remote for it.
	gitBranchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	scpRemote     = regexp.MustCompile(`^(?:[^@/:]+@)?([^/:@]+):(.*)$`)
	// formRemote is what RemoteForm gives for a remote on another machine:
	// a host, then the path.
	formRemote = regexp.MustCompile(`(?i)^(\[[0-9a-f:.]+\]|[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?)/(.+)$`)
)

// IsBeadBranch reports whether name is of the form a bead's branch has
// (Branch): yard/<handle>.
func IsBeadBranch(name string) bool {
	handle, ok := strings.CutPrefix(name, "yard/")
	return ok && validID.MatchString(handle) && !strings.Contains(handle, "..") && !strings.HasSuffix(handle, ".lock")
}

// RemoteForm is the URL of a git remote in the one form two yards' URLs
// are compared in: the host and the path, as in github.com/acme/web. How
// the host is reached is each yard's own and no part of it, so the scheme,
// the login and the port are left out, with a trailing slash and .git, and
// the host is in lower case: git@host:acme/web.git, ssh://git@host/acme/web
// and https://host/acme/web are one remote, and host/acme/api is another.
// A path on this machine is file:///path, with the links in it followed.
// The form reads as itself, and is "" for what reads as none of them.
func RemoteForm(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 2048 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	var host, path string
	local := false
	form, scp := formRemote.FindStringSubmatch(s), scpRemote.FindStringSubmatch(s)
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" {
			return ""
		}
		if local = strings.EqualFold(u.Scheme, "file"); local {
			host, path = u.Host, u.Path
		} else {
			host, path = u.Hostname(), u.Path
		}
	case form != nil:
		// The form itself, as it crossed a link.
		host, path = strings.Trim(form[1], "[]"), form[2]
	case scp != nil && !strings.HasPrefix(s, ".") && len(scp[1]) > 1:
		// One letter before the colon is a drive, not a host.
		host, path = scp[1], scp[2]
	default:
		local, path = true, filepath.Clean(s)
	}
	host = strings.ToLower(host)
	if local {
		// As this machine names the directory: two yards that share one
		// are on one machine, and a link in the way to it (/tmp) is not
		// a different remote.
		path = filepath.ToSlash(realPath(path))
	}
	path = "/" + strings.TrimLeft(strings.TrimSuffix(strings.TrimRight(path, "/"), ".git"), "/")
	switch {
	case local:
		return "file://" + host + path
	case host == "" || path == "/":
		return ""
	case strings.Contains(host, ":"):
		// An address with colons of its own keeps its brackets.
		host = "[" + host + "]"
	}
	return host + path
}

// realPath is path with the links in it followed, as far as it is there:
// a repository that is away for now is still named as it was while it was
// there, since what is above it has not moved.
func realPath(path string) string {
	rest := ""
	for dir := path; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		up := filepath.Dir(dir)
		if up == dir {
			return path
		}
		rest, dir = filepath.Join(filepath.Base(dir), rest), up
	}
}

// BaseRemote names the remote and branch the depot's base tracks, or "" when
// it has none.
func BaseRemote(ctx context.Context, depot Depot) (remote, branch string) {
	if depot.IsDir() || depot.Path == "" {
		return "", ""
	}
	out, err := git(ctx, depot.Path, "remote")
	if err != nil {
		return "", ""
	}
	remotes := strings.Fields(string(out))
	has := func(r string) bool { return slices.Contains(remotes, r) }
	// A remote-tracking base: origin/main.
	if r, b, ok := strings.Cut(depot.Base, "/"); ok && has(r) {
		return r, b
	}
	// A local branch: its upstream's remote, else origin.
	if _, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+depot.Base); err != nil {
		return "", ""
	}
	if out, err := git(ctx, depot.Path, "config", "--get", "branch."+depot.Base+".remote"); err == nil {
		r := strings.TrimSpace(string(out))
		b := depot.Base
		if m, err := git(ctx, depot.Path, "config", "--get", "branch."+depot.Base+".merge"); err == nil {
			b = strings.TrimPrefix(strings.TrimSpace(string(m)), "refs/heads/")
		}
		if has(r) {
			return r, b
		}
	}
	if has("origin") {
		return "origin", depot.Base
	}
	return "", ""
}

// remoteForm is the URL the depot has for remote, in RemoteForm. A path that
// is written from the depot is made whole first.
func remoteForm(ctx context.Context, depot Depot, remote string) (string, error) {
	if !gitRemoteName.MatchString(remote) {
		return "", fmt.Errorf("%q is not the name of a remote", remote)
	}
	out, err := git(ctx, depot.Path, "remote", "get-url", remote)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(out))
	if !strings.Contains(raw, "://") && (scpRemote.FindStringSubmatch(raw) == nil || strings.HasPrefix(raw, ".")) && !filepath.IsAbs(raw) {
		raw = filepath.Join(depot.Path, raw)
	}
	form := RemoteForm(raw)
	if form == "" {
		return "", fmt.Errorf("the URL of %s does not read as one", remote)
	}
	return form, nil
}

// gitFetch runs git fetch in dir for a caller nobody watches: a remote
// that asks for credentials fails at once.
func gitFetch(ctx context.Context, dir string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "fetch", "--quiet", "--no-tags"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	cmd.Stderr = &stderr
	// git may leave a child (ssh) holding its output open after it is
	// killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("git fetch: %w", ctx.Err())
		}
		return fmt.Errorf("git fetch: %w: %s", err, gitWords(stderr.String()))
	}
	return nil
}

// gitWords is what git said on stderr as one line says it: its fatal and
// error lines when it has any, without the advice around them.
func gitWords(stderr string) string {
	var said, all []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		all = append(all, line)
		if strings.HasPrefix(line, "fatal:") || strings.HasPrefix(line, "error:") {
			said = append(said, line)
		}
	}
	if len(said) == 0 {
		said = all
	}
	return strings.Join(said, "; ")
}

func hasCommit(ctx context.Context, dir, commit string) bool {
	_, err := git(ctx, dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// baseOf is the git part of the message that sends a bead of depot whose
// branch starts from base (Family.Base): nil for a dir depot, a depot whose
// base has no remote, and a base that does not resolve. Such a bead
// crosses with its words only.
func baseOf(ctx context.Context, depot Depot, base string) *PeerGit {
	remote, branch := BaseRemote(ctx, depot)
	if remote == "" || base == "" {
		return nil
	}
	form, err := remoteForm(ctx, depot, remote)
	if err != nil {
		return nil
	}
	out, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return nil
	}
	if base != depot.Base {
		// A child of a train, or a bead that came from a yard: what it
		// starts from is no tracking ref of the depot's base.
		branch = base
	}
	return &PeerGit{Base: branch, BaseCommit: strings.TrimSpace(string(out)), Remote: form}
}

// againOf is where the work on a bead of depot starts that is sent to a yard
// again, after a return whose branch this yard took (last, as that return
// named it): on this yard's branch of the bead, which stands at the
// returned commit or behind it, so the yard that built that commit has what
// the branch has, and what it returns comes after the branch. g is what the
// bead would start from otherwise (baseOf) and is what is returned when
// there is nothing to continue: no return with code, no branch here any
// more, or a branch that holds nothing its base does not.
//
// why is set when the branch holds commits that neither the return nor the
// base has. The other yard does not have those, and nothing sends them to
// it, so the bead is not sent (PeerBeadSend).
func againOf(ctx context.Context, depot Depot, bead string, g, last *PeerGit) (from *PeerGit, why string) {
	if g == nil || last == nil || !gitCommit.MatchString(last.Commit) {
		return g, ""
	}
	out, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+Branch(bead)+"^{commit}")
	if err != nil {
		return g, ""
	}
	head := strings.TrimSpace(string(out))
	within := func(commit string) bool {
		_, err := git(ctx, depot.Path, "merge-base", "--is-ancestor", head, commit)
		return err == nil
	}
	switch {
	case within(last.Commit):
		// Named by the branch the other yard pushed, which is where a yard
		// that no longer has the commit fetches it from (checkBase).
		return &PeerGit{Base: last.Branch, BaseCommit: head, Remote: g.Remote}, ""
	case within(g.BaseCommit):
		return g, ""
	}
	return nil, fmt.Sprintf("not sent again: this yard's %s is at %s, with commits made here since the other yard returned %s at %s. That yard does not have them, and a bead's own commits are not sent to the yard that works it. Move the branch back to %s, or work the bead in this yard",
		Branch(bead), short(head), last.Branch, short(last.Commit), short(last.Commit))
}

// checkBase says why a bead whose work starts at g cannot start there in
// depot, "" when it can: the depot's base has the remote the bead names, and
// the base commit is in the depot, fetched now when it was not.
func checkBase(ctx context.Context, depot Depot, g PeerGit) string {
	ctx, cancel := context.WithTimeout(ctx, peerFetchTimeout)
	defer cancel()
	if !gitCommit.MatchString(g.BaseCommit) {
		return "the base commit it names is no commit's name"
	}
	remote, _ := BaseRemote(ctx, depot)
	if remote == "" {
		return fmt.Sprintf("the base of depot %s has no remote here", depot.Name)
	}
	own, err := remoteForm(ctx, depot, remote)
	if err != nil {
		return fmt.Sprintf("depot %s: %v", depot.Name, err)
	}
	if theirs := RemoteForm(g.Remote); theirs != own {
		return fmt.Sprintf("the remote %s of depot %s is %s here, and the bead starts from %s", remote, depot.Name, own, or(theirs, "one that does not read as a URL"))
	}
	if hasCommit(ctx, depot.Path, g.BaseCommit) {
		return ""
	}
	var ferr error
	if gitBranchName.MatchString(g.Base) && !strings.Contains(g.Base, "..") {
		ferr = gitFetch(ctx, depot.Path, remote, "+refs/heads/"+g.Base+":refs/remotes/"+remote+"/"+g.Base)
	}
	if !hasCommit(ctx, depot.Path, g.BaseCommit) {
		// Not on the branch it names (any more): by its own name, which a
		// remote may or may not hand out.
		if err := gitFetch(ctx, depot.Path, remote, g.BaseCommit); err != nil && ferr == nil {
			ferr = err
		}
	}
	if hasCommit(ctx, depot.Path, g.BaseCommit) {
		return ""
	}
	why := fmt.Sprintf("the base commit %s is not in depot %s and could not be fetched from %s", short(g.BaseCommit), depot.Name, remote)
	if ferr != nil {
		why += ": " + ferr.Error()
	}
	return why
}

// peerBaseRefs is where a depot keeps the commits that the open beads from
// peers start from: one ref a bead, named as its branch is. A base commit
// may be on no branch of the depot (fetched by its own name, or on a branch
// the remote has moved since), and git's gc drops a commit that no ref
// leads to.
const peerBaseRefs = "refs/yardr/peer-base/"

// PeerBaseRef is the ref that keeps the commit the bead starts from.
func PeerBaseRef(bead string) string { return peerBaseRefs + bead }

// PeerBase is the commit an open bead that came from a peer starts from,
// which the bead's depot keeps by PeerBaseRef until the bead is closed.
type PeerBase struct {
	Bead   string `json:"bead"`
	Depot  Depot  `json:"depot"`
	Commit string `json:"commit"`
}

// KeepPeerBases makes depot's refs for the bases of beads from peers what
// bases says, which are the depot's open ones (PeerBases): a ref for each,
// at its commit, and no other, so the ref of a bead that has closed goes.
// It runs outside any write: the store calls it when a bead has arrived,
// and the daemon when the depot's open beads have changed.
func KeepPeerBases(ctx context.Context, depot Depot, bases []PeerBase) error {
	if depot.IsDir() || depot.Path == "" {
		return nil
	}
	out, err := git(ctx, depot.Path, "for-each-ref", "--format=%(refname) %(objectname)", peerBaseRefs)
	if err != nil {
		return fmt.Errorf("depot %s: reading %s: %w", depot.Name, peerBaseRefs, err)
	}
	have := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if ref, at, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			have[ref] = at
		}
	}
	var errs []error
	for _, b := range bases {
		ref := PeerBaseRef(b.Bead)
		at, ok := have[ref]
		delete(have, ref)
		if ok && at == b.Commit {
			continue
		}
		if !validID.MatchString(b.Bead) || !gitCommit.MatchString(b.Commit) {
			continue
		}
		if _, err := git(ctx, depot.Path, "update-ref", ref, b.Commit); err != nil {
			errs = append(errs, fmt.Errorf("depot %s: keeping %s for %s: %w", depot.Name, short(b.Commit), b.Bead, err))
		}
	}
	for ref := range have {
		if _, err := git(ctx, depot.Path, "update-ref", "-d", ref); err != nil {
			errs = append(errs, fmt.Errorf("depot %s: removing %s: %w", depot.Name, ref, err))
		}
	}
	return errors.Join(errs...)
}

// short is a commit as a line says it.
func short(commit string) string { return commit[:min(len(commit), 12)] }

// finalFetch is a failure to take a returned branch that no further try
// changes: what the peer named, or pushed, is not what this yard takes.
type finalFetch struct{ error }

// FinalFetch reports whether err, of TakePeerBranch, is one that trying
// again does not change.
func FinalFetch(err error) bool {
	var f finalFetch
	return errors.As(err, &f)
}

// TakePeerBranch takes the branch a peer's return names into the depot of
// the bead it is for: fetched from the remote the bead's peer group names,
// checked to be the commit the return names, and made the bead's own
// branch (f.Into), which is created at it or, when it exists, moved to it
// by fast-forward only. The daemon calls it, outside any write.
//
// It returns why the code is not for this yard to take, when that is how
// the two yards are set up and nothing to mend: the bead's depot is a
// directory, or its remote is not the one the branch was pushed to. An
// error is a branch that was to be taken and was not: tried again, unless
// FinalFetch says no try changes it (a branch name that is no bead's, a
// commit that is not the one named, a local branch it is no fast-forward
// of).
func TakePeerBranch(ctx context.Context, f PeerFetch) (notTaken string, err error) {
	depot := f.Depot
	final := func(format string, a ...any) (string, error) { return "", finalFetch{fmt.Errorf(format, a...)} }
	if depot.IsDir() {
		return fmt.Sprintf("depot %s is a directory, which has no branches", depot.Name), nil
	}
	if depot.Path == "" {
		return fmt.Sprintf("depot %s has no repository here", depot.Name), nil
	}
	own, err := remoteForm(ctx, depot, f.Remote)
	if err != nil {
		return fmt.Sprintf("depot %s has no remote %s to fetch it from (%v)", depot.Name, f.Remote, err), nil
	}
	if theirs := RemoteForm(f.Git.Remote); theirs != own {
		return fmt.Sprintf("it was pushed to %s, and the remote %s of depot %s is %s here", or(theirs, "a remote whose URL does not read as one"), f.Remote, depot.Name, own), nil
	}
	if !IsBeadBranch(f.Git.Branch) {
		return final("the branch it names, %q, is not of the form a bead's branch has (yard/<bead>)", f.Git.Branch)
	}
	if !gitCommit.MatchString(f.Git.Commit) {
		return final("the commit it names, %q, is no commit's name", f.Git.Commit)
	}
	if !IsBeadBranch(f.Into) {
		return final("%q is no bead's branch to take it into", f.Into)
	}
	// Into a ref of its own first: what came is looked at before any branch
	// is it, and FETCH_HEAD is every fetch's in the repository.
	tmp := "refs/yardr/returned/" + strings.TrimPrefix(f.Into, "yard/")
	defer func() { _, _ = git(context.WithoutCancel(ctx), depot.Path, "update-ref", "-d", tmp) }()
	// --refmap= : and into no tracking ref beside it, which git would
	// otherwise update as it passes.
	if err := gitFetch(ctx, depot.Path, "--refmap=", f.Remote, "+refs/heads/"+f.Git.Branch+":"+tmp); err != nil {
		return "", fmt.Errorf("fetching %s from %s: %w", f.Git.Branch, f.Remote, err)
	}
	out, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", tmp+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("reading what was fetched of %s: %w", f.Git.Branch, err)
	}
	if got := strings.TrimSpace(string(out)); got != f.Git.Commit {
		return final("%s is at %s on %s, and the return names %s: not taken", f.Git.Branch, short(got), f.Remote, short(f.Git.Commit))
	}
	ref := "refs/heads/" + f.Into
	out, err = git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		// No such branch: made, and refused should one appear meanwhile.
		if _, err := git(ctx, depot.Path, "update-ref", ref, f.Git.Commit, ""); err != nil {
			return "", fmt.Errorf("making %s: %w", f.Into, err)
		}
		return "", nil
	}
	old := strings.TrimSpace(string(out))
	if old == f.Git.Commit {
		return "", nil
	}
	if _, err := git(ctx, depot.Path, "merge-base", "--is-ancestor", old, f.Git.Commit); err != nil {
		return final("this yard's %s is at %s, which %s does not come after: it is taken by fast-forward only. Move or delete the branch, then yardr bead unhold tries again",
			f.Into, short(old), short(f.Git.Commit))
	}
	// Where the branch is checked out its tree moves with it, which git
	// refuses over work that is in the way.
	if wt := worktreeOf(ctx, depot.Path, ref); wt != "" {
		if _, err := git(ctx, wt, "merge", "--ff-only", f.Git.Commit); err != nil {
			return final("this yard's %s is checked out in %s and could not be moved to %s: %v", f.Into, wt, short(f.Git.Commit), err)
		}
		return "", nil
	}
	if _, err := git(ctx, depot.Path, "update-ref", ref, f.Git.Commit, old); err != nil {
		return "", fmt.Errorf("moving %s: %w", f.Into, err)
	}
	return "", nil
}

// worktreeOf is the worktree of repo that has ref checked out, "" for none.
func worktreeOf(ctx context.Context, repo, ref string) string {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	wt := ""
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			wt = p
		} else if line == "branch "+ref {
			return wt
		}
	}
	return ""
}
