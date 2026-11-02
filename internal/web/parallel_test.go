package web_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// allowSerial names every test that stays sequential on purpose: the test
// and the seam that keeps it there (docs/testing.md R3). A test that is
// neither parallel nor listed fails the gate, so a future test cannot join
// the serial set silently; a listed test that turned parallel, or that no
// longer exists, fails it too.
var allowSerial = map[string]string{
	"TestHealth":            "assign:describeServer",
	"TestHealthDaemonBuild": "assign:describeServer",
	"TestHealthDegraded":    "assign:describeServer",
	"TestLimitsTimeout":     "assign:limits.Timeout",
}

// parFacts is what a function does that decides parallelism: the
// package-level helpers it calls, the process and package state it
// touches, and whether it claims parallelism itself.
type parFacts struct {
	calls   map[string]bool // package-level functions and local helpers called
	sels    map[string]bool // selector names called (Setenv, Unsetenv, Chdir, ...)
	assigns map[string]bool // package vars assigned with =, dotted past a package name
	hasPar  bool            // the function calls t.Parallel()
}

func (f *parFacts) union(g parFacts) {
	f.hasPar = f.hasPar || g.hasPar
	for k := range g.calls {
		f.calls[k] = true
	}
	for k := range g.sels {
		f.sels[k] = true
	}
	for k := range g.assigns {
		f.assigns[k] = true
	}
}

// parAssigned is the package state an assignment target writes: a package
// variable of this directory's packages, through a field, map entry or
// pointer, or a variable of an imported package, dotted. locals distinguishes
// a local that shadows a package variable or an import with the same name.
// Anything else writes no shared state.
func parAssigned(expr ast.Expr, imports map[string]bool, pkgVars map[string]bool, locals map[*ast.Ident]bool) string {
	root := expr
	var sel []string
	for {
		switch e := root.(type) {
		case *ast.SelectorExpr:
			sel = append([]string{e.Sel.Name}, sel...)
			root = e.X
		case *ast.IndexExpr:
			root = e.X
		case *ast.SliceExpr:
			root = e.X
		case *ast.StarExpr:
			root = e.X
		case *ast.ParenExpr:
			root = e.X
		default:
			goto found
		}
	}
found:
	id, ok := root.(*ast.Ident)
	if !ok || id.Name == "_" {
		return ""
	}
	if imports[id.Name] && !locals[id] {
		return id.Name + "." + strings.Join(sel, ".")
	}
	if pkgVars[id.Name] && !locals[id] {
		return id.Name
	}
	return ""
}

// parScope is the names one block, or the parameters of one function, have
// declared so far.
type parScope struct {
	names  map[string]token.Pos
	parent *parScope
}

func newParScope(parent *parScope) *parScope {
	return &parScope{names: map[string]token.Pos{}, parent: parent}
}

// declared is whether name has entered scope here or in a scope around it.
func (s *parScope) declared(name string, at token.Pos) bool {
	for ; s != nil; s = s.parent {
		if from, ok := s.names[name]; ok && at >= from {
			return true
		}
	}
	return false
}

// declare records where a name enters scope. A short declaration can reuse
// a name already declared in this block, so keep the earlier binding.
func (s *parScope) declare(name string, from token.Pos) {
	if old, ok := s.names[name]; !ok || from < old {
		s.names[name] = from
	}
}

// fields declares the names of parameter, result and receiver lists.
func (s *parScope) fields(lists ...*ast.FieldList) {
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			for _, n := range f.Names {
				s.declare(n.Name, token.NoPos)
			}
		}
	}
}

// parLocals are the identifiers of fn that name something fn declares, a
// parameter or a variable of a block before them, and so shadow a package
// variable of the same name where they stand. go/ast once resolved them
// (the Obj of an identifier), and deprecates that, since without types it
// cannot always be right, as for the keys of a struct literal. The root of
// an assignment target is plain lexical scoping, which the syntax settles:
// read in source order, a name is declared for what follows it and for no
// more than its block.
func parLocals(fn *ast.FuncDecl) map[*ast.Ident]bool {
	root := newParScope(nil)
	root.fields(fn.Recv, fn.Type.Params, fn.Type.Results)
	r := parResolver{scope: root, locals: map[*ast.Ident]bool{}}
	ast.Walk(r, fn.Body)
	return r.locals
}

// parResolver is the visitor parLocals walks with: the scope of the node it
// is visiting, and the identifiers found local so far.
type parResolver struct {
	scope  *parScope
	locals map[*ast.Ident]bool
}

func (r parResolver) Visit(n ast.Node) ast.Visitor {
	switch n := n.(type) {
	case nil:
		return nil
	case *ast.FuncLit:
		r.scope = newParScope(r.scope)
		r.scope.fields(n.Type.Params, n.Type.Results)
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.CaseClause, *ast.CommClause:
		r.scope = newParScope(r.scope)
	}
	switch n := n.(type) {
	case *ast.AssignStmt:
		if n.Tok == token.DEFINE {
			r.define(n.End(), n.Lhs...)
		}
	case *ast.RangeStmt:
		if n.Tok == token.DEFINE {
			r.define(n.X.End(), n.Key, n.Value)
		}
	case *ast.DeclStmt:
		if g, ok := n.Decl.(*ast.GenDecl); ok && (g.Tok == token.VAR || g.Tok == token.CONST) {
			for _, spec := range g.Specs {
				if v, ok := spec.(*ast.ValueSpec); ok {
					for _, id := range v.Names {
						r.scope.declare(id.Name, v.End())
					}
				}
			}
		}
	case *ast.Ident:
		if r.scope.declared(n.Name, n.Pos()) {
			r.locals[n] = true
		}
	}
	return r
}

// define declares the identifiers among lhs when the declaration ends, as
// := does: its names do not shadow an outer one in their initializer.
func (r parResolver) define(from token.Pos, lhs ...ast.Expr) {
	for _, e := range lhs {
		if id, ok := e.(*ast.Ident); ok {
			r.scope.declare(id.Name, from)
		}
	}
}

// parMarked reports whether f touches process or package state: the
// environment, the working directory, or a package variable.
func parMarked(f parFacts) bool {
	return f.sels["Setenv"] || f.sels["Unsetenv"] || f.sels["Clearenv"] || f.sels["Chdir"] || len(f.assigns) > 0
}

// parCollect reads fn's own facts. Imported package functions enter the
// call graph by name; a method sharing a helper's name merges with it at
// the caller's, so reachability stays conservative across the test packages.
func parCollect(fn *ast.FuncDecl, imports map[string]bool, pkgVars map[string]bool) parFacts {
	f := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}}
	locals := parLocals(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fun := n.Fun.(type) {
			case *ast.Ident:
				f.calls[fun.Name] = true
			case *ast.SelectorExpr:
				f.sels[fun.Sel.Name] = true
				if pkg, ok := fun.X.(*ast.Ident); ok && imports[pkg.Name] {
					f.calls[fun.Sel.Name] = true
				}
				if fun.Sel.Name == "Parallel" {
					f.hasPar = true
				}
			}
		case *ast.AssignStmt:
			if n.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range n.Lhs {
				if name := parAssigned(lhs, imports, pkgVars, locals); name != "" {
					f.assigns[name] = true
				}
			}
		case *ast.RangeStmt:
			if n.Tok != token.ASSIGN {
				return true
			}
			for _, lhs := range []ast.Expr{n.Key, n.Value} {
				if lhs == nil {
					continue
				}
				if name := parAssigned(lhs, imports, pkgVars, locals); name != "" {
					f.assigns[name] = true
				}
			}
		case *ast.IncDecStmt:
			if name := parAssigned(n.X, imports, pkgVars, locals); name != "" {
				f.assigns[name] = true
			}
		}
		return true
	})
	return f
}

// parReach is every function's transitive facts: its own unioned with what
// the helpers it calls reach.
func parReach(own map[string]parFacts) map[string]parFacts {
	reached := map[string]parFacts{}
	for name := range own {
		out := parFacts{calls: map[string]bool{}, sels: map[string]bool{}, assigns: map[string]bool{}}
		seen := map[string]bool{}
		var visit func(string)
		visit = func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			f, ok := own[name]
			if !ok {
				return
			}
			out.union(f)
			for c := range f.calls {
				visit(c)
			}
		}
		visit(name)
		reached[name] = out
	}
	return reached
}

// parWitness is a shortest call path from test to a function whose own
// facts hold marker (a selector or assign:var), for the error.
func parWitness(test, marker string, own map[string]parFacts) string {
	holds := func(f parFacts) bool {
		if v, ok := strings.CutPrefix(marker, "assign:"); ok {
			return f.assigns[v]
		}
		return f.sels[marker]
	}
	prev := map[string]string{test: ""}
	queue := []string{test}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if holds(own[cur]) {
			path := []string{cur}
			for prev[path[0]] != "" {
				path = append([]string{prev[path[0]]}, path...)
			}
			if len(path) == 1 {
				return "in its own body"
			}
			return "via " + strings.Join(path[1:], " -> ")
		}
		for c := range own[cur].calls {
			if _, ok := own[c]; !ok {
				continue
			}
			if _, seen := prev[c]; !seen {
				prev[c] = cur
				queue = append(queue, c)
			}
		}
	}
	return "through an unknown path"
}

// parMarkers names what r touches, for the error: the environment, the
// directory, or the package variable.
func parMarkers(r parFacts) []string {
	var out []string
	for _, marker := range []string{"Clearenv", "Setenv", "Unsetenv"} {
		if r.sels[marker] {
			out = append(out, marker)
		}
	}
	if r.sels["Chdir"] {
		out = append(out, "Chdir")
	}
	for v := range r.assigns {
		out = append(out, "assign:"+v)
	}
	slices.Sort(out)
	return out
}

// parCheck enforces the R3 rule over the package's tests: a test may carry
// t.Parallel only when it neither assigns a package variable nor calls a
// helper that does, and touches neither the process environment nor the
// working directory. A test that stays sequential must be listed in allow
// with its seam. It returns a violation per breach.
func parCheck(own map[string]parFacts, allow map[string]string) []string {
	var tests []string
	for name := range own {
		if strings.HasPrefix(name, "Test") && name != "TestMain" {
			tests = append(tests, name)
		}
	}
	slices.Sort(tests)
	reached := parReach(own)
	var bad []string
	for _, name := range tests {
		r := reached[name]
		if r.hasPar {
			for _, m := range parMarkers(r) {
				what := map[string]string{
					"Setenv":   "the process environment (t.Setenv or os.Setenv)",
					"Unsetenv": "the process environment (os.Unsetenv)",
					"Clearenv": "the process environment (os.Clearenv)",
					"Chdir":    "the working directory (t.Chdir or os.Chdir)",
				}[m]
				if what == "" {
					what = "package state (" + m + ")"
				}
				bad = append(bad, fmt.Sprintf("%s calls t.Parallel but reaches %s %s", name, what, parWitness(name, m, own)))
			}
			continue
		}
		seam, ok := allow[name]
		if !ok {
			via := []string{}
			for _, marker := range []string{"Setenv", "Unsetenv", "Clearenv"} {
				if own[name].sels[marker] {
					via = append(via, marker)
				}
			}
			if own[name].sels["Chdir"] {
				via = append(via, "t.Chdir")
			}
			for v := range own[name].assigns {
				via = append(via, "assign:"+v)
			}
			for c := range own[name].calls {
				if parMarked(reached[c]) {
					via = append(via, c)
				}
			}
			slices.Sort(via)
			hint := ""
			if len(via) > 0 {
				hint = fmt.Sprintf(" (reaches %s)", strings.Join(via, ", "))
			}
			bad = append(bad, fmt.Sprintf("%s calls no t.Parallel and is not listed in allowSerial%s: parallelise it, or list it with its seam", name, hint))
			continue
		}
		if strings.TrimSpace(seam) == "" {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s without naming its seam", name))
		}
	}
	for name := range allow {
		_, ok := own[name]
		if !ok {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which is no test any more: delete the entry", name))
			continue
		}
		if reached[name].hasPar {
			bad = append(bad, fmt.Sprintf("allowSerial lists %s, which calls t.Parallel now: delete the entry", name))
		}
	}
	return bad
}

// parLoad parses sources (file name to contents) and reads every test
// function's facts: package vars and imports from every file first, facts
// second, so no file is read before its vars are known, however the files
// sort.
func parLoad(sources map[string][]byte) (map[string]parFacts, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed[name] = f
	}
	// Package vars live in test files too (etaRunning, wantHome), and
	// assigning one pins the test just the same.
	pkgVars := map[string]bool{}
	for _, f := range parsed {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				if v, ok := s.(*ast.ValueSpec); ok {
					for _, n := range v.Names {
						pkgVars[n.Name] = true
					}
				}
			}
		}
	}
	own := map[string]parFacts{}
	for file, f := range parsed {
		if !strings.HasSuffix(file, "_test.go") {
			continue
		}
		imports := map[string]bool{}
		for _, imp := range f.Imports {
			if imp.Name != nil {
				if imp.Name.Name == "_" || imp.Name.Name == "." {
					continue
				}
				imports[imp.Name.Name] = true
				continue
			}
			path := strings.Trim(imp.Path.Value, `"`)
			imports[path[strings.LastIndex(path, "/")+1:]] = true
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			got := parCollect(fd, imports, pkgVars)
			if old, ok := own[fd.Name.Name]; ok {
				old.union(got)
				got = old
			}
			own[fd.Name.Name] = got
		}
	}
	return own, nil
}

// Only the tests allowSerial names stay sequential; every other test runs
// beside the rest (docs/testing.md R3). A test that merely calls a helper
// which stubs is just as pinned as one that stubs itself.
func TestOnlyAllowedTestsStaySerial(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string][]byte{}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources[file] = src
	}
	own, err := parLoad(sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range parCheck(own, allowSerial) {
		t.Error(v)
	}
}

// The rule's own check is checked: a parallel test reaching a stub
// through a helper is caught with its path, an unlisted sequential test
// is caught with what it reaches, and a stale allow entry is caught. A
// local named as a package variable or as an import is no write to either,
// but the name is the package's again before the local is declared.
func TestParCheckFindsViolations(t *testing.T) {
	t.Parallel()
	own, err := parLoad(map[string][]byte{
		"z.go": []byte("package x\n\nvar seam = 1\nvar state = struct{ n int }{}\nvar stateMap = map[string]bool{}\n"),
		"web_export_test.go": []byte(`package web

var describeServer = 0

func SetDescribeServer() { describeServer = 1 }
`),
		"a_test.go": []byte(`package x

import (
	"os"
	"testing"

	"example.com/mod/limits"
	"example.com/mod/web"
)

func stubIt(t *testing.T) { t.Setenv("K", "v") }
func stubServer() { web.SetDescribeServer() }
func parallelIt(t *testing.T) { t.Parallel() }
func first() { second() }
func second() { first(); state.n++ }

func TestParallelBad(t *testing.T) {
	t.Parallel()
	stubIt(t)
}

func TestParallelUnsetenvBad(t *testing.T) {
	t.Parallel()
	os.Unsetenv("K")
}

func TestParallelBadViaHelper(t *testing.T) {
	parallelIt(t)
	state.n++
}

func TestParallelForeignAssignBad(t *testing.T) {
	t.Parallel()
	limits.Timeout = 1
}

func TestParallelServerStubBad(t *testing.T) {
	t.Parallel()
	stubServer()
}

func TestNestedParallelBad(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		t.Parallel()
		limits.Timeout = 1
	})
}

func TestParallelLocalShadow(t *testing.T) {
	t.Parallel()
	state := struct{ n int }{}
	state.n++
}

func shadowedParam(state *struct{ n int }) { state.n++ }

func TestParallelOtherShadows(t *testing.T) {
	t.Parallel()
	shadowedParam(nil)
	if state := 1; state > 0 {
		state++
	}
	var seam int
	seam++
	for stateMap := range 3 {
		stateMap++
	}
	func(stateMap map[string]bool) { stateMap["x"] = true }(nil)
}

func TestParallelShortInitializer(t *testing.T) {
	t.Parallel()
	state := func() { state.n++ }
	state()
}

func TestParallelVarInitializer(t *testing.T) {
	t.Parallel()
	var state = func() { state.n++ }
	state()
}

func TestParallelWriteBeforeShadow(t *testing.T) {
	t.Parallel()
	state.n++
	state := struct{ n int }{}
	_ = state
}

func TestParallelWriteAfterShadowScope(t *testing.T) {
	t.Parallel()
	if state := 1; state > 0 {
		state++
	}
	state.n++
}

func TestParallelImportShadow(t *testing.T) {
	t.Parallel()
	page := func() (int, int) { return 1, 2 }
	got, limits := page()
	got, limits = page()
	_, _ = got, limits
}

func TestParallelWriteBeforeImportShadow(t *testing.T) {
	t.Parallel()
	limits.Timeout = 1
	limits := struct{ Timeout int }{}
	_ = limits
}

func TestSerialOK(t *testing.T) { stubIt(t) }

func TestParallelOK(t *testing.T) { t.Parallel() }

func TestSerialUnlisted(t *testing.T) { stubIt(t); seam = 2 }

func TestSerialCycleUnlisted(t *testing.T) { first() }
`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := parCheck(own, map[string]string{"TestSerialOK": "", "TestParallelOK": "stale", "TestGone": "stale"})
	want := []string{
		"TestNestedParallelBad calls t.Parallel but reaches package state (assign:limits.Timeout) in its own body",
		"TestParallelBad calls t.Parallel but reaches the process environment (t.Setenv or os.Setenv) via stubIt",
		"TestParallelBadViaHelper calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelForeignAssignBad calls t.Parallel but reaches package state (assign:limits.Timeout) in its own body",
		"TestParallelServerStubBad calls t.Parallel but reaches package state (assign:describeServer) via stubServer -> SetDescribeServer",
		"TestParallelShortInitializer calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelUnsetenvBad calls t.Parallel but reaches the process environment (os.Unsetenv) in its own body",
		"TestParallelVarInitializer calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelWriteAfterShadowScope calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestParallelWriteBeforeImportShadow calls t.Parallel but reaches package state (assign:limits.Timeout) in its own body",
		"TestParallelWriteBeforeShadow calls t.Parallel but reaches package state (assign:state) in its own body",
		"TestSerialCycleUnlisted calls no t.Parallel and is not listed in allowSerial (reaches first): parallelise it, or list it with its seam",
		"TestSerialUnlisted calls no t.Parallel and is not listed in allowSerial (reaches assign:seam, stubIt): parallelise it, or list it with its seam",
		"allowSerial lists TestParallelOK, which calls t.Parallel now: delete the entry",
		"allowSerial lists TestSerialOK without naming its seam",
		"allowSerial lists TestGone, which is no test any more: delete the entry",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("violations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
