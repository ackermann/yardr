package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// block is one coverprofile block: a contiguous run of statements the
// coverage tool counts together. Two runs cover the same block when every
// field matches.
type block struct {
	file                                          string
	startLine, startCol, endLine, endCol, numStmt int
}

// profile is one parsed coverprofile: every block the run could cover,
// with its execution count.
type profile struct {
	mode   string
	counts map[block]int
}

// parseCoverProfile reads a Go coverprofile: a mode line, then one block
// per line. A profile written by a failing run still parses: the test
// binary writes what it covered before it failed.
func parseCoverProfile(path string) (profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return profile{}, err
	}
	defer f.Close()
	p := profile{counts: map[block]int{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !sc.Scan() {
		return profile{}, fmt.Errorf("parse %s: empty profile", path)
	}
	mode, ok := strings.CutPrefix(sc.Text(), "mode: ")
	if !ok {
		return profile{}, fmt.Errorf("parse %s: first line %q is not a mode line", path, sc.Text())
	}
	p.mode = mode
	for sc.Scan() {
		line := sc.Text()
		b, count, err := parseBlockLine(line)
		if err != nil {
			return profile{}, fmt.Errorf("parse %s: %w", path, err)
		}
		p.counts[b] = count
	}
	if err := sc.Err(); err != nil {
		return profile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, nil
}

// parseBlockLine reads one profile line,
// name.go:startLine.startCol,endLine.endCol numStmt count. The file is cut
// at ".go:" rather than split on colons, since a path may hold one.
func parseBlockLine(line string) (block, int, error) {
	var b block
	i := strings.Index(line, ".go:")
	if i < 0 {
		return b, 0, fmt.Errorf("line %q has no file", line)
	}
	b.file = line[:i+3]
	var count int
	_, err := fmt.Sscanf(line[i+4:], "%d.%d,%d.%d %d %d",
		&b.startLine, &b.startCol, &b.endLine, &b.endCol, &b.numStmt, &count)
	if err != nil {
		return b, 0, fmt.Errorf("line %q: %w", line, err)
	}
	return b, count, nil
}

// coveredSet is the blocks a profile ran at least once.
func coveredSet(p profile) map[block]bool {
	set := make(map[block]bool, len(p.counts))
	for b, n := range p.counts {
		if n > 0 {
			set[b] = true
		}
	}
	return set
}

// statements sums the statements of a block set.
func statements(set map[block]bool) int {
	var n int
	for b := range set {
		n += b.numStmt
	}
	return n
}

// totalStatements sums every block a profile knows, covered or not.
func totalStatements(p profile) int {
	var n int
	for b := range p.counts {
		n += b.numStmt
	}
	return n
}

// uniqueCounts counts, for each test, the statements in its blocks that no
// other test of the package covers.
func uniqueCounts(sets map[string]map[block]bool) map[string]int {
	owners := map[block]string{}
	shared := map[block]bool{}
	for _, name := range sortedKeys(sets) {
		for b := range sets[name] {
			if o, ok := owners[b]; ok && o != name {
				shared[b] = true
			} else {
				owners[b] = name
			}
		}
	}
	out := map[string]int{}
	for name := range sets {
		out[name] = 0
	}
	for b, name := range owners {
		if !shared[b] {
			out[name] += b.numStmt
		}
	}
	return out
}

// greedyRemoval computes the removal set one test at a time: the most
// expensive test with no unique block goes, the counts are recomputed, and
// the next goes, until every remaining test owns a block. Removing one
// candidate changes the others' unique counts, so every zero-unique test
// at once would over-remove: two tests covering exactly the same blocks
// are both zero-unique, but dropping both loses what only they ran.
func greedyRemoval(sets map[string]map[block]bool, seconds map[string]float64) []string {
	rest := map[string]map[block]bool{}
	for name, set := range sets {
		rest[name] = set
	}
	var removed []string
	for {
		unique := uniqueCounts(rest)
		best := ""
		for name := range rest {
			if unique[name] != 0 {
				continue
			}
			if best == "" || seconds[name] > seconds[best] ||
				(seconds[name] == seconds[best] && name < best) {
				best = name
			}
		}
		if best == "" {
			return removed
		}
		removed = append(removed, best)
		delete(rest, best)
	}
}

// pair is two tests whose covered sets are nearly the same: a candidate
// to merge into one table-driven test.
type pair struct {
	a, b       string
	similarity float64
	aStmts     int
	bStmts     int
	shared     int
}

// nearDuplicates finds every pair of tests whose covered sets are at
// least cutoff the same, by Jaccard similarity over blocks. Tests that
// cover nothing are left out: they pair with everything at 0/0.
func nearDuplicates(sets map[string]map[block]bool, cutoff float64) []pair {
	names := sortedKeys(sets)
	var out []pair
	for i, a := range names {
		if len(sets[a]) == 0 {
			continue
		}
		for _, b := range names[i+1:] {
			if len(sets[b]) == 0 {
				continue
			}
			shared := 0
			for blk := range sets[a] {
				if sets[b][blk] {
					shared++
				}
			}
			union := len(sets[a]) + len(sets[b]) - shared
			if float64(shared)/float64(union) >= cutoff {
				out = append(out, pair{
					a:          a,
					b:          b,
					similarity: float64(shared) / float64(union),
					aStmts:     statements(sets[a]),
					bStmts:     statements(sets[b]),
					shared:     shared,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].similarity != out[j].similarity {
			return out[i].similarity > out[j].similarity
		}
		if out[i].a != out[j].a {
			return out[i].a < out[j].a
		}
		return out[i].b < out[j].b
	})
	return out
}

// unionOf merges the covered sets of the named tests.
func unionOf(sets map[string]map[block]bool, names []string) map[block]bool {
	out := map[block]bool{}
	for _, name := range names {
		for b := range sets[name] {
			out[b] = true
		}
	}
	return out
}

func sortedKeys(sets map[string]map[block]bool) []string {
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
