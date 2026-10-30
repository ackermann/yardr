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
// the gate runs root whenever a package the binary imports changes and,
// by rows of the reads table in .yardr/test, whenever a test helper or a
// generator under internal/ does (scripttest and rpc/clientgen are leaves
// the rules judge, and the binary imports neither): together every
// internal package these rules judge.
//
// The rules, quoted from the page:
// (a) internal/store imports no internal package, except internal/peer,
// which stays: it is OS process code with no yard types, so the edge
// carries none of the coupling the rule is there to stop; and
// internal/source, which stays: the store executes run asks through its
// functions, not only its types, so cutting the edge would make the two
// import each other, while source itself imports only internal/config;
// (b) a leaf imports no internal package;
// (c) no internal package imports internal/cli, and only the root main may.
const (
	internalPrefix = "yardr.dev/yardr/internal/"
	storePkg       = internalPrefix + "store"
	peerPkg        = internalPrefix + "peer"
	sourcePkg      = internalPrefix + "source"
	cliPkg         = internalPrefix + "cli"
	rootPkg        = "yardr.dev/yardr"
)

// leafPkgs is every internal package that imports no internal package:
// the page's leaves layer, measured at f75abf88 minus shared and udiff,
// which yardr-g1sf folded into store, and minus agents, which yardr-8nca
// made import store for the harness types, and plus pairing, which
// yardr-rxk9 left with the pairing paths alone. A leaf that gains an
// internal import fails rule (b) below; a rename updates this list with it.
var leafPkgs = []string{
	peerPkg,
	internalPrefix + "pairing",
	internalPrefix + "rpc/clientgen",
	internalPrefix + "scripttest",
	internalPrefix + "version",
}

// TestImportRules runs go list over the repository and checks every
// import edge against the page's three rules. A breaking edge fails
// naming that edge.
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
	isInternal := func(p string) bool { return strings.HasPrefix(p, internalPrefix) }
	for _, from := range slices.Sorted(maps.Keys(imports)) {
		for _, to := range imports[from] {
			var rule string
			switch {
			case from == storePkg && isInternal(to) && to != peerPkg && to != sourcePkg:
				rule = "(a): internal/store imports no internal package except internal/peer and internal/source"
			case to == cliPkg && from != rootPkg:
				rule = "(c): only the root main imports internal/cli"
			case slices.Contains(leafPkgs, from) && isInternal(to):
				rule = "(b): a leaf imports no internal package"
			}
			if rule == "" {
				continue
			}
			t.Errorf("%s imports %s, breaking rule %s", from, to, rule)
		}
	}
}
