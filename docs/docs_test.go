package docs

import (
	"io/fs"
	"strings"
	"testing"
)

// Every page the binary carries is listed, and every listed name is a
// page: a .md added beside docs.go without its name in Order, or a
// rename that leaves one behind, fails here.
func TestOrderListsEveryPage(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var embedded []string
	for _, e := range entries {
		embedded = append(embedded, strings.TrimSuffix(e.Name(), ".md"))
	}
	if len(Order) != len(embedded) {
		t.Fatalf("Order lists %d pages, the binary carries %d: %q against %q", len(Order), len(embedded), Order, embedded)
	}
	seen := map[string]bool{}
	for _, n := range Order {
		seen[n] = true
	}
	for _, n := range embedded {
		if !seen[n] {
			t.Errorf("the binary carries %s.md, which Order does not list", n)
		}
	}
	for _, n := range Order {
		if text, ok := Read(n); !ok || text == "" {
			t.Errorf("Read(%q) = %d bytes, %v", n, len(text), ok)
		} else if title, ok := Title(n); !ok || title == "" {
			t.Errorf("Title(%q) = %q, %v: every page starts with a heading", n, title, ok)
		}
	}
}

// Lookup is what a name on the command line goes through: it finds a
// page however it is cased, and refuses an unknown name and a walk
// out of the pages.
func TestLookup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"flows", "flows"},
		{"README", "README"},
		{"readme", "README"},
		{"Line", "line"},
	} {
		if got, ok := Lookup(tc.in); !ok || got != tc.want {
			t.Errorf("Lookup(%q) = %q, %v, want %q, true", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"", "flow", "../go.mod", "flows.md", "design/notes"} {
		if got, ok := Lookup(in); ok || got != "" {
			t.Errorf("Lookup(%q) = %q, %v, want no page", in, got, ok)
		}
		if _, ok := Read(in); ok {
			t.Errorf("Read(%q) reads a page for an unknown name", in)
		}
	}
}

// Section prints one heading's part of a page: the match ignores case,
// the section runs to the next heading of the same or higher level,
// and deeper headings stay in.
func TestSection(t *testing.T) {
	text, ok := Section("groups", "runners")
	if !ok {
		t.Fatal("Section(groups, runners) finds nothing")
	}
	if !strings.HasPrefix(text, "## Runners\n") {
		t.Fatalf("Section(groups, runners) starts %q", firstLine(text))
	}
	if strings.Contains(text, "\n## ") {
		t.Errorf("Section(groups, runners) runs past its next ## heading:\n%s", lastLines(text, 3))
	}
	if _, ok := Section("groups", "groups and runners"); ok {
		t.Errorf("Section(groups, groups and runners) matches the title, which would print the whole page")
	}
	if _, ok := Section("groups", "no such heading"); ok {
		t.Errorf("Section(groups, no such heading) finds a section")
	}
	if _, ok := Section("no such page", "runners"); ok {
		t.Errorf("Section(no such page, runners) finds a section")
	}
	if _, ok := Section("groups", "  "); ok {
		t.Errorf("Section(groups, blank) finds a section")
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
