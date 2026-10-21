package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DoneSafe is the outcome of a reviewer who approves a bead that needs no
// security review: the flows of depot yardr and of the starter pack send it
// straight past review-security to approved. A depot may list the paths
// whose change it never takes that road (SecurityPathsFile), and Done holds
// the outcome to the list.
const DoneSafe = "done-safe"

// SecurityPathsFile is the file of a git depot's repository that lists the
// paths a change must not touch if it is to be approved with DoneSafe: one
// git pathspec per line, read as a glob (ParseSecurityPaths). It is read at
// the tip of the depot's base, as the depot's flows are (RepoFlowDir), never
// from a bead's branch or worktree, so a bead cannot loosen the list that
// holds it: an edit applies once it has been reviewed and landed. A depot
// without the file has no list, and DoneSafe is taken as the flow says.
const SecurityPathsFile = ".yardr/security_paths"

// maxSecurityPaths bounds the list, which becomes the arguments of one git
// command.
const maxSecurityPaths = 500

// securityGitTimeout bounds the git a done-safe report waits for: a checkout
// that hangs refuses the report, and the agent that made it is told so,
// instead of its command never coming back.
const securityGitTimeout = 30 * time.Second

// ParseSecurityPaths reads the text of SecurityPathsFile into the pathspecs
// git is given: one entry a line, blank lines and lines starting with # left
// out. An entry is a glob over paths from the repository's root, as git's
// glob pathspec magic reads it: * matches within a directory, ** across
// directories, so internal/store/** is everything under that directory and
// go.mod is that file alone. An entry that starts with a colon is a git
// pathspec with magic of its own (:(exclude)docs/**) and is passed as it is.
func ParseSecurityPaths(text []byte) ([]string, error) {
	var out []string
	for n, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsRune(line, 0) {
			return nil, fmt.Errorf("line %d: a path with a NUL in it", n+1)
		}
		if len(out) == maxSecurityPaths {
			return nil, fmt.Errorf("more than %d entries", maxSecurityPaths)
		}
		if !strings.HasPrefix(line, ":") {
			line = ":(glob)" + line
		}
		out = append(out, line)
	}
	return out, nil
}

// noRevision refuses a name git would read as an option where a revision
// is meant.
func noRevision(rev string) error {
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("%q is no revision", rev)
	}
	return nil
}

// securityPaths reads the depot's SecurityPathsFile at the tip of its base.
// A depot with no such file has no list. If git cannot resolve the base, the
// list cannot be established, so the check refuses rather than treating it
// as absent. A list that cannot be read or has a line git would refuse is an
// error: the file says the depot wants the check, so it is not skipped.
func securityPaths(ctx context.Context, d Depot) ([]string, error) {
	base := or(d.Base, "origin/main")
	if err := noRevision(base); err != nil {
		return nil, fmt.Errorf("base %w", err)
	}
	sha, err := ResolveRef(ctx, d.Path, base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolving base %s in %s: %w", base, d.Path, err)
	}
	commit := strings.TrimSpace(string(sha))
	listed, err := git(ctx, d.Path, "ls-tree", "-z", "--name-only", commit, "--", SecurityPathsFile)
	if err != nil {
		return nil, fmt.Errorf("listing %s at %s: %w", SecurityPathsFile, base, err)
	}
	if len(listed) == 0 {
		return nil, nil
	}
	text, err := git(ctx, d.Path, "cat-file", "blob", commit+":"+SecurityPathsFile)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", SecurityPathsFile, base, err)
	}
	paths, err := ParseSecurityPaths(text)
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %w", SecurityPathsFile, base, err)
	}
	return paths, nil
}

// securityTouched lists the files the branch changed against base, from
// where it left it, that match one of the depot's security paths. A rename
// counts as the deletion and the addition it is: a file moved out of a
// security path is a change to it.
func securityTouched(ctx context.Context, d Depot, base, branch string, specs []string) ([]string, error) {
	if err := noRevision(base); err != nil {
		return nil, fmt.Errorf("base %w", err)
	}
	args := append([]string{"diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", base + "..." + branch, "--"}, specs...)
	out, err := git(ctx, d.Path, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// safeCheck is what Done found out about a report of DoneSafe, before its
// write: git must not run inside one. Either the diff touches security
// paths (files), or whether it does could not be told (err), which holds
// the report too: a depot that lists paths wants the check made.
type safeCheck struct {
	base  string
	files []string
	err   error
}

// checkDoneSafe reads git for a report of outcome on bead id, or nil when
// there is nothing to hold it to: another outcome, a bead the flow gives no
// such outcome (the move itself refuses that), a depot that is no git
// repository here, or one with no list. Everything the move would refuse
// anyway is left to it.
func (s *Store) checkDoneSafe(ctx context.Context, id, outcome string) *safeCheck {
	if outcome != DoneSafe {
		return nil
	}
	b, err := s.GetBead(ctx, id)
	if err != nil || b.Depot == nil {
		return nil
	}
	next, err := s.NextStages(ctx, id)
	if err != nil || next.Flow == nil {
		return nil
	}
	if _, err := next.Flow.Resolve(next.Stage, outcome); err != nil {
		return nil
	}
	d, err := s.GetDepot(ctx, *b.Depot)
	if err != nil || d.IsDir() || d.Path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, securityGitTimeout)
	defer cancel()
	specs, err := securityPaths(ctx, *d)
	if err != nil {
		return &safeCheck{err: err}
	}
	if len(specs) == 0 {
		return nil
	}
	fam, err := s.Family(ctx, id)
	if err != nil {
		return &safeCheck{err: err}
	}
	c := &safeCheck{base: or(fam.Base, "origin/main")}
	c.files, c.err = securityTouched(ctx, *d, c.base, Branch(b.ID), specs)
	if len(c.files) == 0 && c.err == nil {
		return nil
	}
	return c
}

// refusal is the error that holds a report of DoneSafe on bead id at stage
// of flow f, or nil: it says which files, or why they could not be told, and
// what to report instead.
func (c *safeCheck) refusal(f *Flow, id, stage string) error {
	if c == nil {
		return nil
	}
	instead := "report " + DefaultOutcome + " instead"
	if to, err := f.Resolve(stage, DefaultOutcome); err == nil {
		instead += ", which sends it to " + to
	}
	if c.err != nil {
		return fmt.Errorf("bead %s: %s is refused: the depot lists security paths (%s), and whether the diff against %s touches one could not be told: %w; %s",
			id, DoneSafe, SecurityPathsFile, or(c.base, "its base"), c.err, instead)
	}
	const shown = 10
	files := c.files
	more := ""
	if len(files) > shown {
		files, more = files[:shown], fmt.Sprintf(" and %d more", len(c.files)-shown)
	}
	names := make([]string, len(files))
	for i, file := range files {
		// A branch can name a file with terminal controls or line breaks; quote
		// it before returning the refusal to the session that reported done-safe.
		names[i] = strconv.QuoteToASCII(file)
	}
	return fmt.Errorf("bead %s: %s is refused: the diff against %s touches %s%s, which the depot lists in %s; %s",
		id, DoneSafe, c.base, strings.Join(names, ", "), more, SecurityPathsFile, instead)
}
