package agents

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// docTables are the tables of docs/groups.md that are made from the harness
// files, each by the name its marker carries.
var docTables = map[string]func() []string{
	"harness errors": errorsTable,
	"harness marks":  marksTable,
}

// code is s as a code span in a table's cell.
func code(s string) string { return "`" + strings.ReplaceAll(s, "|", `\|`) + "`" }

// errorsTable is every error line of every kind, in the order it is tried.
func errorsTable() []string {
	rows := []string{"| kind | reason | a line that matches |", "|---|---|---|"}
	for _, h := range Harnesses {
		for _, e := range h.Errors {
			rows = append(rows, fmt.Sprintf("| %s | %s | %s |", code(h.Kind), code(e.Reason), code(e.Pattern)))
		}
	}
	return append(rows, "| any other | | none known: nothing is reported, and `session_idle` with its `tail` is what there is |")
}

// marksTable is the marks of every kind whose pane is read by its own. The
// last row is what the herdr runner does for the rest (inputMarks there).
func marksTable() []string {
	rows := []string{"| kind | the input begins with | a message it took begins with |", "|---|---|---|"}
	for _, h := range Harnesses {
		m := h.Marks
		if m.Input == "" {
			continue
		}
		input := code(m.Input)
		if m.Edge != "" {
			input += " after " + code(m.Edge) + ", the edge of the box its input is drawn in; the edges are no part of the text"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s |", code(h.Kind), input, code(m.Message)))
	}
	return append(rows, "| any other, or none | `❯` or `›` | no mark known: the prompt anywhere above the input, "+
		"and anywhere at all in a pane with neither mark, as before |")
}

// docs/groups.md shows the error lines and the pane marks of each kind as
// tables. They are made from the harness files and stand between two
// marker lines each, `<!-- <name>: ... -->` and `<!-- /<name> -->`, indented
// as the first marker is. This fails when a table is not what the files
// say; YARDR_UPDATE_GOLDEN=1 writes the tables again instead.
func TestGroupsDocTables(t *testing.T) {
	const file = "../../docs/groups.md"
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	var out []string
	found := map[string]bool{}
	stale := false
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		name, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(lines[i]), "<!-- "), ":")
		table := docTables[name]
		if !ok || table == nil || !strings.HasPrefix(strings.TrimSpace(lines[i]), "<!-- ") {
			continue
		}
		found[name] = true
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " "))]
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "<!-- /"+name+" -->" {
			end++
		}
		if end == len(lines) {
			t.Fatalf("%s: no `<!-- /%s -->` after line %d", file, name, i+1)
		}
		var want []string
		for _, row := range table() {
			want = append(want, indent+row)
		}
		if got := lines[i+1 : end]; strings.Join(got, "\n") != strings.Join(want, "\n") {
			stale = true
			if os.Getenv("YARDR_UPDATE_GOLDEN") == "" {
				t.Errorf("%s: the table %q is not what the harness files say; write it again with\n"+
					"  YARDR_UPDATE_GOLDEN=1 go test ./internal/agents -run TestGroupsDocTables\ngot:\n%s\nwant:\n%s",
					file, name, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
		out = append(out, want...)
		i = end - 1
	}
	for name := range docTables {
		if !found[name] {
			t.Errorf("%s has no `<!-- %s: ... -->` marker: the table is gone", file, name)
		}
	}
	if stale && os.Getenv("YARDR_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(file, []byte(strings.Join(out, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
