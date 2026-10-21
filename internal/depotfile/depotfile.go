package depotfile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// DepotRules is the depot's own standing working rules, relative to the
// depot's root: how this depot is worked, for every agent session on its
// beads. A brief appends it after the role; a depot without it gets briefs
// as if it were not there. Read with ReadDepotFile, from the tip of the
// depot's base (a dir depot's from its directory).
const DepotRules = ".yardr/depot.md"

// ReadDepotFile reads rel at the tip of the depot's base (depot.Base, which a
// bead's start has just fetched when it is a remote's). A file the base
// does not have is fs.ErrNotExist, whatever the working tree holds: the
// working tree is read only when the depot has no base that resolves (a
// directory that is no git checkout) or is a dir depot. where names the
// place looked, for the not-found error, and sha the base commit read, empty
// for the working tree.
func ReadDepotFile(depot *store.Depot, rel string) (text []byte, where, sha string, err error) {
	// A base with a newline in it is no ref, and would be two requests to
	// the git that reads a line for each.
	if depot.Base != "" && !depot.IsDir() && !strings.Contains(depot.Base, "\n") {
		gitPath := filepath.ToSlash(rel)
		text, sha, found, err := readAtBase(depot, gitPath)
		if err != nil {
			return nil, "", "", err
		}
		// A depot that is not a repo, or whose base does not resolve, has no
		// base to read from: its working tree is all there is.
		if sha != "" {
			where := depot.Base + ":" + gitPath
			if !found {
				return nil, where, "", fs.ErrNotExist
			}
			return text, where, sha, nil
		}
	}
	path := filepath.Join(depot.Path, rel)
	text, err = os.ReadFile(path)
	return text, path, "", err
}

// A File is one depot file ReadDepotFiles looked for, in position: what it
// holds and where it was looked for (base:path, or the disk path), whether
// it was there, or why this one could not be read. A path the base does not
// have is simply not Found; what stops the read itself (no git, a deadline)
// fails the call.
type File struct {
	Rel   string
	Text  []byte
	Where string
	Found bool
	Err   error
}

// ReadDepotFiles reads rels the way ReadDepotFile reads one, in one git
// process where the depot has a base that resolves, else from the working
// tree or directory as it is on disk. A command that shows or plans many
// files reads them together instead of starting git per file. With no
// rels, nothing is read and no process starts.
func ReadDepotFiles(depot *store.Depot, rels ...string) (files []File, sha string, err error) {
	files = make([]File, len(rels))
	for i, rel := range rels {
		files[i].Rel = filepath.ToSlash(rel)
	}
	// No depot, or nowhere it stands: nothing is anywhere, as DepotRole
	// says of one role.
	if depot == nil || depot.Path == "" {
		return files, "", nil
	}
	if len(rels) == 0 {
		return files, "", nil
	}
	if depot.Base != "" && !depot.IsDir() && !strings.Contains(depot.Base, "\n") {
		sha, err = readAtBaseFiles(depot, files)
		if err != nil {
			return nil, "", err
		}
		// A depot that is not a repo, or whose base does not resolve, has
		// no base to read from: its working tree is all there is.
		if sha != "" {
			for i := range files {
				files[i].Where = depot.Base + ":" + files[i].Rel
			}
			return files, sha, nil
		}
	}
	for i, rel := range rels {
		p := filepath.Join(depot.Path, rel)
		files[i].Where = p
		text, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, "", err
		}
		files[i].Text, files[i].Found = text, true
	}
	return files, "", nil
}

// readAtBase reads gitPath at the commit the depot's base names, in one git
// process: a `git cat-file --batch` asked for the commit and then, with the
// sha it answered, for the file, so the file is read at the commit that was
// resolved. sha is empty when there is no base to read from: git does not
// know it, or ends before it has answered (no checkout here, no git). found
// is false for a path that commit does not have. A git that is cut off at
// GitTimeout, or fails once the base is known, is an error.
func readAtBase(depot *store.Depot, gitPath string) (text []byte, sha string, found bool, err error) {
	if strings.Contains(gitPath, "\n") {
		return nil, "", false, fmt.Errorf("%s:%q: a path with a newline in it cannot be read from the base", depot.Base, gitPath)
	}
	b, err := openBatch(depot)
	if err != nil {
		return nil, "", false, err
	}
	if b == nil {
		return nil, "", false, nil
	}
	defer b.cancel()

	commit := depot.Base + "^{commit}"
	sha, _, _, known, err := b.ask(commit)
	if err != nil || !known {
		// A git that ends with nothing said has no base to give, as one
		// that says so: only the deadline or an answer that is none of
		// git's is a failure here.
		ferr := b.failed(commit, err)
		if err == nil || (errors.Is(err, io.EOF) && !errors.Is(ferr, context.DeadlineExceeded)) {
			return nil, "", false, nil
		}
		return nil, "", false, ferr
	}
	blob := sha + ":" + gitPath
	_, kind, text, known, err := b.ask(blob)
	if err != nil {
		return nil, "", false, b.failed(blob, err)
	}
	if err := b.close(blob); err != nil {
		return nil, "", false, err
	}
	if !known {
		return nil, sha, false, nil
	}
	if kind != "blob" {
		return nil, "", false, fmt.Errorf("%s:%s is a %s, not a file", depot.Base, gitPath, kind)
	}
	return text, sha, true, nil
}

// readAtBaseFiles reads gitPaths at the commit the depot's base names, in
// one git process, the way readAtBase reads one: asked for the commit and
// then, with the sha it answered, for each file. sha is empty when there is
// no base to read from, and the caller reads the working tree instead. A
// path the commit does not have is simply not Found; a path that names no
// file fails its own File, since the rest are still read.
func readAtBaseFiles(depot *store.Depot, files []File) (sha string, err error) {
	for _, f := range files {
		if strings.Contains(f.Rel, "\n") {
			return "", fmt.Errorf("%s:%q: a path with a newline in it cannot be read from the base", depot.Base, f.Rel)
		}
	}
	b, err := openBatch(depot)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", nil
	}
	defer b.cancel()

	commit := depot.Base + "^{commit}"
	sha, _, _, known, err := b.ask(commit)
	if err != nil || !known {
		ferr := b.failed(commit, err)
		if err == nil || (errors.Is(err, io.EOF) && !errors.Is(ferr, context.DeadlineExceeded)) {
			return "", nil
		}
		return "", ferr
	}
	last := commit
	for i := range files {
		blob := sha + ":" + files[i].Rel
		last = blob
		_, kind, text, known, err := b.ask(blob)
		if err != nil {
			return "", b.failed(blob, err)
		}
		if !known {
			continue
		}
		if kind != "blob" {
			files[i].Err = fmt.Errorf("%s:%s is a %s, not a file", depot.Base, files[i].Rel, kind)
			continue
		}
		files[i].Text = text
		files[i].Found = true
	}
	if err := b.close(last); err != nil {
		return "", err
	}
	return sha, nil
}

// A batch is one `git cat-file --batch` asked for objects by name.
type batch struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	errb   bytes.Buffer
}

// openBatch starts git for the depot's base and files. A nil batch with no
// error is git that would not start (no git): no base to read from, as one
// that ends before it has answered.
func openBatch(depot *store.Depot) (*batch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	cmd := exec.CommandContext(ctx, "git", "-C", depot.Path, "cat-file", "--batch")
	cmd.WaitDelay = time.Second
	b := &batch{ctx: ctx, cancel: cancel, cmd: cmd}
	cmd.Stderr = &b.errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil
	}
	b.stdin, b.out = stdin, bufio.NewReader(stdout)
	return b, nil
}

// ask sends one object name and reads git's answer to it: the object's id,
// type and content, or known false for the "missing" (or "ambiguous") git
// says of a name it cannot give an object for.
func (b *batch) ask(name string) (oid, kind string, body []byte, known bool, err error) {
	// A failed write shows as the read that follows it failing.
	io.WriteString(b.stdin, name+"\n")
	line, err := b.out.ReadString('\n')
	if err != nil {
		return "", "", nil, false, err
	}
	head, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), name+" ")
	if ok && (head == "missing" || head == "ambiguous") {
		return "", "", nil, false, nil
	}
	var size int64
	if n, _ := fmt.Sscanf(line, "%s %s %d\n", &oid, &kind, &size); n != 3 || size < 0 {
		return "", "", nil, false, fmt.Errorf("unexpected answer %q", strings.TrimSpace(line))
	}
	// The content is followed by a newline of git's own.
	body = make([]byte, size+1)
	if _, err := io.ReadFull(b.out, body); err != nil {
		return "", "", nil, false, err
	}
	return oid, kind, body[:size], true, nil
}

// failed ends git and says why the answer to name could not be read: the
// deadline, else what git said as it went. Git is ended by closing its
// stdin and waited for, not killed: one that has answered on stdout may not
// have said why on stderr yet, and a kill would take that with it. One that
// does not go at the end of its input goes at the deadline.
func (b *batch) failed(name string, cause error) error {
	b.stdin.Close()
	werr := b.cmd.Wait()
	timedOut := b.ctx.Err()
	if timedOut != nil {
		return fmt.Errorf("git cat-file --batch: %s: %w", name, timedOut)
	}
	if errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
		if werr != nil {
			cause = werr
		} else {
			cause = errors.New("git ended without an answer")
		}
	}
	return fmt.Errorf("git cat-file --batch: %s: %w: %s", name, cause, strings.TrimSpace(b.errb.String()))
}

// close ends git at the end of its input: every answer read. name is the
// last asked, for what a git that does not go quietly says.
func (b *batch) close(name string) error {
	b.stdin.Close()
	if err := b.cmd.Wait(); err != nil {
		if b.ctx.Err() != nil {
			err = b.ctx.Err()
		}
		return fmt.Errorf("git cat-file --batch: %s: %w: %s", name, err, strings.TrimSpace(b.errb.String()))
	}
	return nil
}

// DepotRole reports whether the depot has its own role prompt called name
// (a file name, <role>.md): .yardr/roles/<name> where a brief looks for
// it, at the tip of the depot's base. A pack's import asks: it names the file its role
// shadows, or keeps the depot's role when told to.
func DepotRole(depot *store.Depot, name string) (bool, error) {
	if depot == nil || depot.Path == "" {
		return false, nil
	}
	_, _, _, err := ReadDepotFile(depot, filepath.Join(".yardr", "roles", name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
