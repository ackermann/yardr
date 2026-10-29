package main

import (
	"bytes"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// docs/architecture.md names which internal package may import which;
// this test holds the code to that page. The page is the norm and this
// test its check: the two change together, so a new import that breaks
// the rules fails the gate here. It lives in the root package because
// the gate runs root whenever a package the binary imports changes,
// which is every internal package these rules judge.
//
// The rules, quoted from the page:
// (a) internal/store imports no internal package;
// (b) a leaf imports no internal package;
// (c) no internal package imports internal/cli, and only the root main may.
const (
	internalPrefix = "yardr.dev/yardr/internal/"
	storePkg       = internalPrefix + "store"
	cliPkg         = internalPrefix + "cli"
	rootPkg        = "yardr.dev/yardr"
)

// leafPkgs is every internal package that imports no internal package,
// measured at f75abf88, the page's leaves layer. A leaf that gains an
// internal import fails rule (b) below; a rename updates this list with it.
var leafPkgs = []string{
	internalPrefix + "agents",
	internalPrefix + "peer",
	internalPrefix + "rpc/clientgen",
	internalPrefix + "scripttest",
	internalPrefix + "shared",
	internalPrefix + "udiff",
	internalPrefix + "version",
}

// allowEdge is one import edge that breaks a rule today, standing until
// the bead in removeBy cuts it. The fix deletes its row with it: a row
// whose edge no longer breaks a rule fails the test, as a breaking edge
// with no row does.
type allowEdge struct {
	from     string
	to       string
	removeBy string
}

// allowImportEdges is the store's 8 internal imports, one row each naming
// its package and the bead that cuts it. No leaf or cli edge breaks a
// rule today, so the table holds no other row.
var allowImportEdges = []allowEdge{
	{storePkg, internalPrefix + "agents", "yardr-8nca"},
	{storePkg, internalPrefix + "config", "yardr-t8m6"},
	{storePkg, internalPrefix + "pairing", "yardr-rxk9"},
	{storePkg, internalPrefix + "peer", "yardr-yz0h"},
	{storePkg, internalPrefix + "shared", "yardr-g1sf"},
	{storePkg, internalPrefix + "source", "yardr-j8vt"},
	{storePkg, internalPrefix + "udiff", "yardr-g1sf"},
	{storePkg, internalPrefix + "yardkey", "yardr-rxk9"},
}

// TestImportRules runs go list over the repository and checks every
// import edge against the page's three rules. A breaking edge not in the
// allow table fails naming that edge, and a table row whose edge is gone
// fails telling its fix to delete the row.
func TestImportRules(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	// The test runs in the repository root, the root package's directory.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./...")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list ./...: %v: %s", err, stderr.String())
	}
	imports := map[string][]string{}
	for line := range strings.Lines(stdout.String()) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		imports[f[0]] = f[1:]
	}
	if len(imports) == 0 {
		t.Fatal("go list ./... printed no packages")
	}
	for _, pkg := range append(slices.Clone(leafPkgs), storePkg, cliPkg) {
		if _, ok := imports[pkg]; !ok {
			t.Errorf("package %s is gone (renamed?): update the leaf list and the page with it", pkg)
		}
	}
	byEdge := map[[2]string][]int{}
	for i, row := range allowImportEdges {
		key := [2]string{row.from, row.to}
		byEdge[key] = append(byEdge[key], i)
	}
	used := make([]bool, len(allowImportEdges))
	allow := func(from, to string) bool {
		for _, i := range byEdge[[2]string{from, to}] {
			if !used[i] {
				used[i] = true
				return true
			}
		}
		return false
	}
	isInternal := func(p string) bool { return strings.HasPrefix(p, internalPrefix) }
	for _, from := range slices.Sorted(maps.Keys(imports)) {
		for _, to := range imports[from] {
			var rule string
			switch {
			case from == storePkg && isInternal(to):
				rule = "(a): internal/store imports no internal package"
			case to == cliPkg && from != rootPkg:
				rule = "(c): only the root main imports internal/cli"
			case slices.Contains(leafPkgs, from) && isInternal(to):
				rule = "(b): a leaf imports no internal package"
			}
			if rule == "" {
				continue
			}
			if allow(from, to) {
				continue
			}
			t.Errorf("%s imports %s, breaking rule %s", from, to, rule)
		}
	}
	for i, row := range allowImportEdges {
		if !used[i] {
			t.Errorf("allow row %s -> %s (remove-by %s) names no breaking edge: delete the row", row.from, row.to, row.removeBy)
		}
	}
}
