package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// streamKept is what stays on the process's streams on purpose: the file,
// the line as it reads, and why. An entry whose line is gone fails the gate
// too, so the list says what is there; a line that must change is changed
// here with it.
var streamKept = []struct{ file, text, why string }{
	{"bdp.go", `log := slog.New(slog.NewTextHandler(os.Stderr, nil))`, keptLog},
	{"crew.go", `if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {`, keptTerminal},
	{"devicepair.go", `if picture, err = drawQR(out.QR, term.IsTerminal(int(os.Stdout.Fd()))); err != nil {`, keptTerminal},
	{"identity.go", `warn = os.Stderr`,
		"the process's stderr is the preset where no run recorded a writer, a direct subcommand call that bypassed pre-run (design yardr-14tr, 3.1: unset means the process's)"},
	{"init.go", `return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))`, keptTerminal},
	{"ops.go", `return server.Run(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))`, keptLog},
	{"ops.go", `log := slog.New(slog.NewTextHandler(os.Stderr, nil))`, keptLog},
	{"ops.go", `cl.Stdout, cl.Stderr = os.Stderr, os.Stderr`, keptChild + ": git clone's progress"},
	{"ops.go", `if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {`, keptTerminal},
	{"otel.go", `Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),`, keptLog},
	{"review.go", `git.Stderr = os.Stderr`, keptChild + ": git's own errors, under the pager"},
	{"review.go", `pager.Stdout, pager.Stderr = os.Stdout, os.Stderr`, keptChild + ": the pager draws on the terminal"},
	{"review.go", `if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {`, keptTerminal},
	{"start.go", `cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr`, keptChild + ": herdr agent attach takes the terminal over, keys and all"},
	{"web.go", `log := slog.New(slog.NewTextHandler(os.Stderr, nil))`, keptLog},
}

const (
	keptLog      = "intentional logging: the server or daemon logs to the stderr it was started with, which is a log (slog) and no command's output"
	keptChild    = "child wiring: the program started here is given the process's own streams, and the command carries none of its bytes"
	keptTerminal = "terminal check: whether the process has a terminal is read off its descriptors, and nothing is written (design yardr-14tr, 3.3)"
)

// streamUse is a line of a source file that reaches for a process stream.
type streamUse struct {
	file  string
	line  int
	text  string   // the line as it reads, trimmed
	names []string // what it calls or names: fmt.Println, os.Stderr
}

// streamUses lists the lines of src, the source of file, that call
// fmt.Print, fmt.Printf or fmt.Println or name os.Stdout or os.Stderr. It
// reads the syntax and not the text: a comment or a string that says
// os.Stdout is no use of it, and an import under another name still is.
func streamUses(file string, src []byte) ([]streamUse, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imported := map[string]string{} // the name a file gives fmt and os, by that name
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || (path != "fmt" && path != "os") {
			continue
		}
		name := path
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			// Dot imports hide the selector names this syntax gate recognizes.
			return nil, fmt.Errorf("%s: dot import of %q hides stream uses from the writer gate", file, path)
		}
		imported[name] = path
	}
	named := map[int][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch name := imported[pkg.Name] + "." + sel.Sel.Name; name {
		case "fmt.Print", "fmt.Printf", "fmt.Println", "os.Stdout", "os.Stderr":
			line := fset.Position(sel.Pos()).Line
			named[line] = append(named[line], name)
		}
		return true
	})
	lines := strings.Split(string(src), "\n")
	var uses []streamUse
	for line, names := range named {
		slices.Sort(names)
		uses = append(uses, streamUse{file: file, line: line, text: strings.TrimSpace(lines[line-1]), names: slices.Compact(names)})
	}
	slices.SortFunc(uses, func(a, b streamUse) int { return a.line - b.line })
	return uses, nil
}

// A command writes to the writers its cobra command holds (c.OutOrStdout,
// c.ErrOrStderr), so a test reads what it printed from a buffer and swaps
// nothing of the process (design yardr-14tr, section 3.1). This gate keeps
// it so, in place of the pipe the tests once swapped in to catch a write
// that escaped: no non-test file of the package may have a line that
//   - calls fmt.Print, fmt.Printf or fmt.Println, which have no writer to
//     hand and write to the process's stdout, or
//   - names os.Stdout or os.Stderr, as a diagnostic written with
//     fmt.Fprint*(os.Stderr, ...) does,
//
// but those streamKept lists. The first has no exception.
func TestCommandsWriteOnlyToTheirWriters(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := make([]bool, len(streamKept))
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		uses, err := streamUses(file, src)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, u := range uses {
			if slices.ContainsFunc(u.names, func(n string) bool { return strings.HasPrefix(n, "fmt.") }) {
				t.Errorf("%s:%d: %s\n\tfmt.Print* has no writer and prints to the process's stdout: write to the command's, fmt.Fprint*(w, ...), "+
					"where w is c.OutOrStdout(), c.ErrOrStderr() or the writer the helper is handed", u.file, u.line, u.text)
				continue
			}
			kept := -1
			for i, k := range streamKept {
				if !found[i] && k.file == u.file && k.text == u.text {
					kept = i
					break
				}
			}
			if kept < 0 {
				t.Errorf("%s:%d: %s\n\tnames %s: write to the command's writer, c.OutOrStdout() or c.ErrOrStderr(); "+
					"a line that must stay on the process's stream is listed in streamKept with its reason",
					u.file, u.line, u.text, strings.Join(u.names, ", "))
				continue
			}
			found[kept] = true
		}
	}
	for i, k := range streamKept {
		if !found[i] {
			t.Errorf("streamKept: %s has no line %q any more (kept as %s): delete the entry, or change it with the line", k.file, k.text, k.why)
		}
	}
}

// The gate reads syntax: what it refuses it finds in code however fmt and
// os are named, and what it passes it passes in comments and strings.
func TestStreamUsesReadTheSyntax(t *testing.T) {
	src := `package x

import (
	"fmt"
	stdos "os"
)

// fmt.Println and os.Stdout in a comment are no use of them.
func f(w io.Writer) {
	fmt.Fprintln(w, "fmt.Println(os.Stderr)")
	fmt.Println("hello")
	fmt.Fprintln(stdos.Stderr, "said")
	cmd.Stdout, cmd.Stderr = stdos.Stdout, stdos.Stderr
	say := fmt.Printf
	_ = os.Stdout
}
`
	uses, err := streamUses("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range uses {
		got = append(got, strconv.Itoa(u.line)+" "+strings.Join(u.names, ",")+" | "+u.text)
	}
	want := []string{
		`11 fmt.Println | fmt.Println("hello")`,
		`12 os.Stderr | fmt.Fprintln(stdos.Stderr, "said")`,
		`13 os.Stderr,os.Stdout | cmd.Stdout, cmd.Stderr = stdos.Stdout, stdos.Stderr`,
		`14 fmt.Printf | say := fmt.Printf`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("uses =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStreamUsesRejectDotImports(t *testing.T) {
	for _, src := range []string{
		`package x; import . "fmt"; func f() { Println("hello") }`,
		`package x; import . "os"; var _ = Stdout`,
	} {
		if _, err := streamUses("x.go", []byte(src)); err == nil {
			t.Errorf("streamUses accepted a dot import in %q", src)
		}
	}
}
