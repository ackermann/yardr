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
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", depot.Path, "cat-file", "--batch")
	cmd.WaitDelay = time.Second
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", false, nil
	}
	out := bufio.NewReader(stdout)
	// ask sends one object name and reads git's answer to it: the object's
	// id, type and content, or known false for the "missing" (or
	// "ambiguous") git says of a name it cannot give an object for.
	ask := func(name string) (oid, kind string, body []byte, known bool, err error) {
		// A failed write shows as the read that follows it failing.
		io.WriteString(stdin, name+"\n")
		line, err := out.ReadString('\n')
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
		if _, err := io.ReadFull(out, body); err != nil {
			return "", "", nil, false, err
		}
		return oid, kind, body[:size], true, nil
	}
	// failed ends git and says why the answer to name could not be read:
	// the deadline, else what git said as it went. Git is ended by closing
	// its stdin and waited for, not killed: one that has answered on stdout
	// may not have said why on stderr yet, and a kill would take that with
	// it. One that does not go at the end of its input goes at the deadline.
	failed := func(name string, cause error) error {
		stdin.Close()
		werr := cmd.Wait()
		timedOut := ctx.Err()
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
		return fmt.Errorf("git cat-file --batch: %s: %w: %s", name, cause, strings.TrimSpace(errb.String()))
	}

	commit := depot.Base + "^{commit}"
	sha, _, _, known, err := ask(commit)
	if err != nil || !known {
		// A git that ends with nothing said has no base to give, as one
		// that says so: only the deadline or an answer that is none of
		// git's is a failure here.
		ferr := failed(commit, err)
		if err == nil || (errors.Is(err, io.EOF) && !errors.Is(ferr, context.DeadlineExceeded)) {
			return nil, "", false, nil
		}
		return nil, "", false, ferr
	}
	blob := sha + ":" + gitPath
	_, kind, text, known, err := ask(blob)
	if err != nil {
		return nil, "", false, failed(blob, err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, "", false, fmt.Errorf("git cat-file --batch: %s: %w: %s", blob, err, strings.TrimSpace(errb.String()))
	}
	if !known {
		return nil, sha, false, nil
	}
	if kind != "blob" {
		return nil, "", false, fmt.Errorf("%s:%s is a %s, not a file", depot.Base, gitPath, kind)
	}
	return text, sha, true, nil
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
