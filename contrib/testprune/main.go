// Command testprune finds which of a Go package's tests can go without
// losing what the suite checks: a test whose covered statements all lie in
// other tests of the package is a candidate for removal. Coverage is the
// invariant, not the verdict: the report ranks candidates by cost for a
// person to judge, since a redundant-looking test may assert something the
// others do not. README.md beside this file says what each number means.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	out := flag.String("o", ".", "directory for testprune.md and testprune.json")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: testprune [-o DIR] PACKAGE\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *out); err != nil {
		fmt.Fprintln(os.Stderr, "testprune:", err)
		os.Exit(1)
	}
}
