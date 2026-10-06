package rigcheck

// What is left of a check that did not pass: its whole output in a file
// under the yard's home (Keep), and the lines of it that say what failed
// (Matching), picked by a pattern the depot gives. The tail of a long run is
// mostly what came after the failure: with `go test ./...` the failing
// test's lines are followed by the `ok` of every package after it.

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// FailPatternPath is where a depot says which lines of its gate's output name
// a failure: regular expressions (Go's syntax), one per line, a line matching
// when any of them does; blank lines and lines starting with # are skipped,
// and space around a pattern is dropped. It is read where the depot's
// repository gate is: on the base for a git depot, in the directory for a dir
// depot. It serves the light gate too. It only chooses what is shown first:
// it cannot make a check pass or fail.
const FailPatternPath = ".yardr/check-fail-pattern"

const (
	// MatchBefore and MatchAfter are the lines shown around a matching one.
	// More after than before: what go prints about a failure (a test's own
	// lines, a panic's or a race's stack) follows the line that names it.
	MatchBefore = 2
	MatchAfter  = 8
	// MatchMax bounds the lines Matching returns, so that a run in which
	// everything fails still leaves a note somebody reads.
	MatchMax = 150

	// GatesDir is the directory of kept outputs, in the yard's home, one
	// directory per depot.
	GatesDir = "gates"
	// KeepPerDir is how many kept outputs outlive a new one per depot and
	// directory the check ran in (a bead's worktree is named after the
	// bead): the count is per directory so that a builder running a red gate
	// over and over does not push out the output another bead's note names.
	KeepPerDir = 5
	// KeepFor is how long a kept output stays, whatever the count.
	KeepFor = 14 * 24 * time.Hour
)

// failPattern reads FailPatternPath for depot: at sha for a git depot, in the
// directory for a dir depot. A depot without one, or whose file cannot be read,
// has none: what is shown first must never be why a gate cannot run.
func failPattern(ctx context.Context, depot store.Depot, sha string) []byte {
	if depot.IsDir() {
		b, _ := os.ReadFile(filepath.Join(depot.Path, filepath.FromSlash(FailPatternPath)))
		return b
	}
	if sha == "" {
		return nil
	}
	if out, err := git(ctx, depot.Path, "ls-tree", "--name-only", sha, "--", FailPatternPath); err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	out, _ := git(ctx, depot.Path, "show", sha+":"+FailPatternPath)
	return []byte(out)
}

// ParsePatterns reads the content of a FailPatternPath. A line that is not a
// regular expression is named in notes and skipped; the others still count.
func ParsePatterns(text []byte) (pats []*regexp.Regexp, notes []string) {
	for i, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s line %d is not a regular expression, skipped: %v", FailPatternPath, i+1, err))
			continue
		}
		pats = append(pats, re)
	}
	return pats, notes
}

// Matching is the lines of output that match one of pats, each with
// MatchBefore lines before it and MatchAfter after, in the order of the
// output, every line once: stretches that touch are one, and a matching
// line whose text was shown as a match already opens no stretch of its own
// (the same panic printed by forty tests is shown once). Each stretch is
// headed by the number of its first line in the output. At most MatchMax
// lines are returned; more is how many matching lines are in none of them.
func Matching(output string, pats []*regexp.Regexp) (text string, more int) {
	if len(pats) == 0 {
		return "", 0
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	matches := func(s string) bool {
		return slices.ContainsFunc(pats, func(re *regexp.Regexp) bool { return re.MatchString(s) })
	}
	var sb strings.Builder
	seen := map[string]bool{}
	shown, end := 0, 0 // lines written; the line after the last one written
	for i, line := range lines {
		if !matches(line) {
			continue
		}
		from := max(i-MatchBefore, 0)
		if i < end || (shown > 0 && from <= end) {
			// The match or its context touches a stretch already written.
			seen[line] = true
		} else if seen[line] {
			continue
		} else if shown+i-max(from, end) >= MatchMax {
			// No room left to reach the line itself.
			more++
			continue
		} else {
			seen[line] = true
			from = max(from, end)
			fmt.Fprintf(&sb, "-- line %d\n", from+1)
			end = from
		}
		for to := min(i+MatchAfter+1, len(lines)); end < to && shown < MatchMax; end, shown = end+1, shown+1 {
			sb.WriteString(lines[end])
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n"), more
}

// Lines is how many lines s has, a last one without a newline included.
func Lines(s string) int {
	if s = strings.TrimRight(s, "\n"); s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// kept is the name of a kept output: <dir>-<UTC time>[-<n>][.light].log.
var kept = regexp.MustCompile(`^(.+)-(\d{8}T\d{6}Z)(?:-(\d+))?(?:\.light)?\.log$`)

// unsafe is what a depot's or directory's name may hold that a file name
// should not.
var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+|^\.+`)

// Keep writes the whole output of res to a file of its own under home,
// <home>/gates/<depot>/<dir>-<time>.log (".light.log" for the light gate),
// mode 0600, and returns its path; <dir> is the last element of the
// directory the check ran in, which for a bead's worktree is the bead. It
// then removes the depot's kept outputs older than KeepFor and, of those for
// the same directory, all but the newest KeepPerDir. Without a home nothing
// is kept. The caller decides which runs are worth keeping: the ones that
// did not pass.
func Keep(home string, res *Result, now time.Time) (string, error) {
	if home == "" {
		return "", nil
	}
	dir := filepath.Join(home, GatesDir, safe(res.Depot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := safe(filepath.Base(res.Dir)) + "-" + now.UTC().Format("20060102T150405Z")
	ext := ".log"
	if res.Light {
		ext = ".light.log"
	}
	// Two runs in one second (a signalled gate run again, two shells) each
	// keep their own.
	var f *os.File
	for n := 1; ; n++ {
		path := filepath.Join(dir, name+ext)
		if n > 1 {
			path = filepath.Join(dir, name+"-"+strconv.Itoa(n)+ext)
		}
		var err error
		if f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			break
		} else if !errors.Is(err, fs.ErrExist) || n > 1000 {
			return "", err
		}
	}
	_, werr := f.Write([]byte(res.Output))
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	prune(dir, filepath.Base(f.Name()), now)
	return f.Name(), nil
}

func safe(name string) string {
	if name = unsafe.ReplaceAllString(name, "_"); name == "" {
		return "_"
	}
	return name
}

// prune removes from dir the kept outputs past KeepFor at now, and for each
// directory name all but the newest KeepPerDir, never the one just written.
// It goes by the time in the name, not the file's: a copied or touched file
// is as old as the run it holds. What it cannot remove stays: keeping an
// output must not fail over an older one.
func prune(dir, just string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name, of, at string
		n            int
	}
	var files []file
	for _, e := range entries {
		if m := kept.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
			n, _ := strconv.Atoi(m[3])
			files = append(files, file{e.Name(), m[1], m[2], n})
		}
	}
	// Newest first.
	slices.SortFunc(files, func(a, b file) int {
		return cmp.Or(cmp.Compare(b.at, a.at), cmp.Compare(b.n, a.n), cmp.Compare(b.name, a.name))
	})
	oldest := now.UTC().Add(-KeepFor).Format("20060102T150405Z")
	count := map[string]int{}
	for _, f := range files {
		count[f.of]++
		if f.name != just && (f.at < oldest || count[f.of] > KeepPerDir) {
			os.Remove(filepath.Join(dir, f.name))
		}
	}
}

// matching fills in what of res's output matches the depot's pattern.
func (c *Check) matching(res *Result) {
	if len(bytes.TrimSpace(c.FailPattern)) == 0 {
		return
	}
	pats, notes := ParsePatterns(c.FailPattern)
	res.PatternNotes = notes
	res.Matched, res.MatchedMore = Matching(res.Output, pats)
}
