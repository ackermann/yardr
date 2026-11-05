package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// upgradeScriptSources are the scripts a yard installs by hand from the
// depot's checkout into <home>/scripts (docs/line.md, "Setup"), by their path
// in the checkout. Each keeps its base name there, as install(1) leaves it.
// The packs' copies under <home>/packs are not here: a pack import owns those.
var upgradeScriptSources = []string{
	"contrib/assembly/merge-local",
	"contrib/assembly/integrate-train",
	"contrib/assembly/pr-train",
	"contrib/assembly/pr-merge",
	"contrib/assembly/merge-pr",
	"contrib/assembly/open-pr",
	"contrib/assembly/post-review",
	"contrib/assembly/pr-ci",
	"contrib/deploy/deploy-dokku",
	"contrib/deploy/deploy-void",
	"contrib/peer/return-branch",
}

// upgradeScriptCheckWait bounds one sh -n. A syntax check takes milliseconds,
// and a shell that hangs must not hold the upgrade, with every yard's gate
// file, behind it.
const upgradeScriptCheckWait = 10 * time.Second

// upgradeScripts is what one upgrade did to the scripts of the covered yards.
type upgradeScripts struct {
	// replaced is each script this run replaced, for a rollback to put back.
	replaced []upgradeScriptSwap
	// failed is whether a script could not be refreshed, each said on warn.
	failed bool
}

// upgradeScriptSwap is one script of one yard that the run replaced.
type upgradeScriptSwap struct {
	dir, name string
}

// upgradeScriptsInstall refreshes the scripts each covered yard already has
// from the depot's checkout, so a landed fix to one is live for the next
// landing as the binary is. Only a script already in a yard's <home>/scripts
// is touched: a yard that never set a group up has no use for one, so none is
// added. A script is replaced only when its bytes differ, and only by a file
// that passes sh -n. One that cannot be refreshed is kept as it is and said
// on warn; it never fails the upgrade, whose binary is installed and verified
// whatever happens here.
func upgradeScriptsInstall(ctx context.Context, checkout string, homes []string, warn io.Writer) *upgradeScripts {
	s := &upgradeScripts{}
	for _, rel := range upgradeScriptSources {
		name := filepath.Base(rel)
		fresh, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			// A checkout without it has nothing to give.
			continue
		}
		if err != nil {
			fmt.Fprintf(warn, "warn: %s in %s could not be read, so the yards' %s is kept as it is: %v\n", rel, checkout, name, err)
			s.failed = true
			continue
		}
		for _, home := range homes {
			dir := filepath.Join(home, "scripts")
			have, err := os.ReadFile(filepath.Join(dir, name))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				fmt.Fprintf(warn, "warn: the script %s could not be read, so it is kept as it is: %v\n", filepath.Join(dir, name), err)
				s.failed = true
				continue
			}
			if bytes.Equal(have, fresh) {
				continue
			}
			if err := upgradeScriptReplace(ctx, dir, name, fresh); err != nil {
				fmt.Fprintf(warn, "warn: the script %s was not refreshed and is kept as it is: %v\n", filepath.Join(dir, name), err)
				s.failed = true
				continue
			}
			s.replaced = append(s.replaced, upgradeScriptSwap{dir: dir, name: name})
		}
	}
	return s
}

// upgradeScriptReplace puts fresh in dir/name as a new file, keeping the old
// bytes as name.previous, one generation, overwritten each time. It never
// writes in place. sh reads a script as it runs, so overwriting one would
// change what a landing that is running reads next. A rename leaves that
// landing its old file, and the next landing starts on the new one.
func upgradeScriptReplace(ctx context.Context, dir, name string, fresh []byte) (err error) {
	live := filepath.Join(dir, name)
	next := filepath.Join(dir, "."+name+".new")
	// A leftover of a run that died is replaced, not written through: it
	// could be a link to somewhere else.
	_ = os.Remove(next)
	f, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(next)
		}
	}()
	_, err = f.Write(fresh)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// The umask has had its say in OpenFile; the script runs by its name.
	if err = os.Chmod(next, 0o755); err != nil {
		return err
	}
	if err = upgradeScriptSyntax(ctx, next); err != nil {
		return err
	}
	// The recovery is kept before the replacement, so no script is ever
	// replaced without one, and after the new file is known good, so a
	// refused one costs the last generation nothing.
	if err = copyFile(live, live+".previous"); err != nil {
		return fmt.Errorf("the old one could not be kept as %s.previous: %w", name, err)
	}
	return os.Rename(next, live)
}

// upgradeScriptSyntax is why the shell refuses the script at path, nil when
// it parses. -n reads the script and runs none of it.
func upgradeScriptSyntax(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, upgradeScriptCheckWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-n", path).CombinedOutput()
	if err == nil {
		return nil
	}
	if why, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); why != "" {
		return fmt.Errorf("the new file fails sh -n: %s", why)
	}
	return fmt.Errorf("the new file could not be checked with sh -n: %w", err)
}

// names are the scripts the run replaced, once each, in the order of the
// sources.
func (s *upgradeScripts) names() []string {
	var out []string
	for _, r := range s.replaced {
		if !slices.Contains(out, r.name) {
			out = append(out, r.name)
		}
	}
	return out
}

// summary is the upgrade's one line about the scripts: the names it
// refreshed, else "current", else that they were kept (the warnings say why).
// Only the first form ends in "refreshed", which is what merge-local reads
// the names by.
func (s *upgradeScripts) summary() string {
	switch names := s.names(); {
	case len(names) > 0:
		return strings.Join(names, ", ") + " refreshed"
	case s.failed:
		return "kept as they were, see the warnings above"
	default:
		return "current"
	}
}

// putBack undoes the run's replacements, each from its name.previous, when
// the upgrade rolls back: the scripts belong with the binary they were
// refreshed for. It returns the names it put back; one it cannot is said on
// warn with where its old bytes are.
func (s *upgradeScripts) putBack(warn io.Writer) []string {
	var out []string
	for _, r := range s.replaced {
		live := filepath.Join(r.dir, r.name)
		if err := os.Rename(live+".previous", live); err != nil {
			fmt.Fprintf(warn, "warn: the script %s could not be put back: %v; the one it replaced is %s.previous\n", live, err, live)
			continue
		}
		if !slices.Contains(out, r.name) {
			out = append(out, r.name)
		}
	}
	return out
}
