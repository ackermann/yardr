package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// similarityCutoff is how alike two tests' covered sets must be to read
// as near-duplicates: at least 95% the same blocks.
const similarityCutoff = 0.95

// report is the analyzed suite: every test's cost and cover, the greedy
// removal set, the near-duplicate pairs, and the coverage with and
// without the candidates.
type report struct {
	pkg            string
	importPath     string
	created        time.Time
	goVersion      string
	results        []testResult
	removal        []string
	removalSeconds float64
	testsSeconds   float64
	fullSeconds    float64
	total          int
	coveredAll     int
	unionAll       int
	coveredKept    int
	pairs          []pair
}

// analyze folds the alone runs into the report. The per-test unique
// counts are read against the full set; the removal set is greedy, so a
// test's column and its membership can differ.
func analyze(pkg, importPath string, results []testResult, total, coveredAll int, fullSeconds float64) report {
	rep := report{
		pkg:         pkg,
		importPath:  importPath,
		created:     time.Now(),
		goVersion:   goVersion(),
		results:     results,
		total:       total,
		coveredAll:  coveredAll,
		fullSeconds: fullSeconds,
	}
	sets := map[string]map[block]bool{}
	seconds := map[string]float64{}
	for _, r := range results {
		rep.testsSeconds += r.seconds
		if r.outcome == outcomeNoProfile {
			continue
		}
		sets[r.name] = r.covered
		seconds[r.name] = r.seconds
	}
	unique := uniqueCounts(sets)
	for i := range rep.results {
		rep.results[i].unique = unique[rep.results[i].name]
	}
	rep.removal = greedyRemoval(sets, seconds)
	dropped := map[string]bool{}
	for _, name := range rep.removal {
		dropped[name] = true
		rep.removalSeconds += seconds[name]
	}
	var kept []string
	for name := range sets {
		if !dropped[name] {
			kept = append(kept, name)
		}
	}
	for i := range rep.results {
		rep.results[i].removed = dropped[rep.results[i].name]
	}
	all := make([]string, 0, len(sets))
	for name := range sets {
		all = append(all, name)
	}
	rep.unionAll = statements(unionOf(sets, all))
	rep.coveredKept = statements(unionOf(sets, kept))
	rep.pairs = nearDuplicates(sets, similarityCutoff)
	sort.Slice(rep.results, func(i, j int) bool {
		if rep.results[i].seconds != rep.results[j].seconds {
			return rep.results[i].seconds > rep.results[j].seconds
		}
		return rep.results[i].name < rep.results[j].name
	})
	return rep
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

// writeReport writes the Markdown report and its JSON beside it.
func writeReport(rep report, mdPath, jsPath string) error {
	if err := os.WriteFile(mdPath, []byte(rep.markdown()), 0o644); err != nil {
		return err
	}
	js, err := json.MarshalIndent(rep.json(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(jsPath, append(js, '\n'), 0o644)
}

func (rep report) markdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# testprune: %s\n\n", rep.pkg)
	fmt.Fprintf(&sb, "%s, %s. Full suite %.1f s; %d alone runs %.1f s.\n\n",
		rep.created.Format("2 Jan 2006"), rep.goVersion, rep.fullSeconds, len(rep.results), rep.testsSeconds)
	fmt.Fprintf(&sb, "Coverage with all tests: %d of %d statements (%.1f%%).\n", rep.coveredAll, rep.total, pct(rep.coveredAll, rep.total))
	if rep.unionAll != rep.coveredAll {
		fmt.Fprintf(&sb, "The alone runs' union is %d statements: running apart covers %s than running together.\n",
			rep.unionAll, moreOrLess(rep.unionAll, rep.coveredAll))
	}
	fmt.Fprintf(&sb, "Coverage without the %d candidates: %d of %d statements (%.1f%%).\n\n",
		len(rep.removal), rep.coveredKept, rep.total, pct(rep.coveredKept, rep.total))
	fmt.Fprintf(&sb, "Candidates: %d tests, %.1f s combined.\n\n", len(rep.removal), rep.removalSeconds)

	sb.WriteString("## Candidates\n\n")
	sb.WriteString("Greedy, most expensive first: each was dropped with no unique block against what stayed. A candidate is not proven redundant: its assertions may check what the others only run.\n\n")
	sb.WriteString("| test | time (s) | statements |\n")
	sb.WriteString("| --- | ---: | ---: |\n")
	byName := map[string]testResult{}
	for _, r := range rep.results {
		byName[r.name] = r
	}
	for _, name := range rep.removal {
		r := byName[name]
		fmt.Fprintf(&sb, "| %s | %.2f | %d |\n", name, r.seconds, r.statements)
	}
	if len(rep.removal) == 0 {
		sb.WriteString("| _none_ | | |\n")
	}

	fmt.Fprintf(&sb, "\n## Near-duplicates (%d)\n\n", len(rep.pairs))
	sb.WriteString("Pairs whose covered sets are at least 95% the same: candidates to merge into one table-driven test.\n\n")
	sb.WriteString("| a | b | same | a stmts | b stmts | shared blocks |\n")
	sb.WriteString("| --- | --- | ---: | ---: | ---: | ---: |\n")
	for _, p := range rep.pairs {
		fmt.Fprintf(&sb, "| %s | %s | %.1f%% | %d | %d | %d |\n",
			p.a, p.b, 100*p.similarity, p.aStmts, p.bStmts, p.shared)
	}
	if len(rep.pairs) == 0 {
		sb.WriteString("| _none_ | | | | | |\n")
	}

	sb.WriteString("\n## All tests\n\n")
	sb.WriteString("Unique counts against the full set, before any removal. `rm` marks the greedy removal set.\n\n")
	sb.WriteString("| test | outcome | time (s) | statements | unique | rm |\n")
	sb.WriteString("| --- | --- | ---: | ---: | ---: | --- |\n")
	for _, r := range rep.results {
		rm := ""
		if r.removed {
			rm = "x"
		}
		fmt.Fprintf(&sb, "| %s | %s | %.2f | %d | %d | %s |\n",
			r.name, r.outcome, r.seconds, r.statements, r.unique, rm)
	}

	var odd []string
	for _, r := range rep.results {
		if r.outcome == outcomeFail || r.outcome == outcomeNoProfile {
			odd = append(odd, r.name+" ("+r.outcome+")")
		}
	}
	if len(odd) > 0 {
		fmt.Fprintf(&sb, "\nTests that failed alone or left no profile, kept out of the sets: %s.\n", strings.Join(odd, ", "))
	}
	return sb.String()
}

func moreOrLess(union, full int) string {
	if union > full {
		return "more"
	}
	return "less"
}

// jsonReport is the report's JSON shape: the same numbers as the Markdown,
// per test plus the removal order and the pairs.
type jsonReport struct {
	Package        string     `json:"package"`
	ImportPath     string     `json:"importPath"`
	Created        time.Time  `json:"created"`
	GoVersion      string     `json:"goVersion"`
	FullSeconds    float64    `json:"fullSeconds"`
	TestsSeconds   float64    `json:"testsSeconds"`
	TotalStmts     int        `json:"totalStatements"`
	CoveredAll     int        `json:"coveredAll"`
	UnionAll       int        `json:"unionAll"`
	CoveredKept    int        `json:"coveredKept"`
	Removal        []string   `json:"removal"`
	RemovalSeconds float64    `json:"removalSeconds"`
	Tests          []jsonTest `json:"tests"`
	Pairs          []jsonPair `json:"pairs"`
}

type jsonTest struct {
	Name       string  `json:"name"`
	Outcome    string  `json:"outcome"`
	Seconds    float64 `json:"seconds"`
	Statements int     `json:"statements"`
	Unique     int     `json:"unique"`
	Removed    bool    `json:"removed"`
}

type jsonPair struct {
	A            string  `json:"a"`
	B            string  `json:"b"`
	Similarity   float64 `json:"similarity"`
	AStatements  int     `json:"aStatements"`
	BStatements  int     `json:"bStatements"`
	SharedBlocks int     `json:"sharedBlocks"`
}

func (rep report) json() jsonReport {
	j := jsonReport{
		Package:        rep.pkg,
		ImportPath:     rep.importPath,
		Created:        rep.created,
		GoVersion:      rep.goVersion,
		FullSeconds:    rep.fullSeconds,
		TestsSeconds:   rep.testsSeconds,
		TotalStmts:     rep.total,
		CoveredAll:     rep.coveredAll,
		UnionAll:       rep.unionAll,
		CoveredKept:    rep.coveredKept,
		Removal:        rep.removal,
		RemovalSeconds: rep.removalSeconds,
	}
	if j.Removal == nil {
		j.Removal = []string{}
	}
	for _, r := range rep.results {
		j.Tests = append(j.Tests, jsonTest{
			Name:       r.name,
			Outcome:    r.outcome,
			Seconds:    r.seconds,
			Statements: r.statements,
			Unique:     r.unique,
			Removed:    r.removed,
		})
	}
	if j.Tests == nil {
		j.Tests = []jsonTest{}
	}
	for _, p := range rep.pairs {
		j.Pairs = append(j.Pairs, jsonPair{
			A:            p.a,
			B:            p.b,
			Similarity:   p.similarity,
			AStatements:  p.aStmts,
			BStatements:  p.bStmts,
			SharedBlocks: p.shared,
		})
	}
	if j.Pairs == nil {
		j.Pairs = []jsonPair{}
	}
	return j
}
