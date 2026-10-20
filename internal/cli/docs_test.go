package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/docs"
)

// yardr docs lists every page the binary carries, each with its first
// heading, in the index's order: on a home with no yard, creating
// nothing there.
func TestDocsListNamesEveryPage(t *testing.T) {
	noHome(t)
	requireNonInteractive(t)
	out, code := executeOut(&app{}, "docs")
	if code != 0 {
		t.Fatalf("yardr docs: exit %d\n%s", code, out)
	}
	at := 0
	for _, name := range docs.Order {
		title, ok := docs.Title(name)
		if !ok {
			t.Fatalf("docs.Title(%q) finds nothing", name)
		}
		i := strings.Index(out[at:], name)
		j := strings.Index(out[at:], title)
		if i < 0 || j < 0 || j < i {
			t.Fatalf("yardr docs lists no %q under %q after %d:\n%s", title, name, at, out)
		}
		at += j + len(title)
	}
	var listed struct {
		Docs []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"docs"`
	}
	out, code = executeOut(&app{}, "docs", "--json")
	if code != 0 {
		t.Fatalf("yardr docs --json: exit %d\n%s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("yardr docs --json is no JSON: %v\n%s", err, out)
	}
	if len(listed.Docs) != len(docs.Order) {
		t.Fatalf("yardr docs --json lists %d pages, want %d", len(listed.Docs), len(docs.Order))
	}
}

// yardr docs <name> prints the page, in any case; an unknown name is
// refused with the names there are.
func TestDocsShowPrintsOnePage(t *testing.T) {
	noHome(t)
	requireNonInteractive(t)
	out, code := executeOut(&app{}, "docs", "flows")
	if code != 0 {
		t.Fatalf("yardr docs flows: exit %d\n%s", code, out)
	}
	want, _ := docs.Read("flows")
	if out != want {
		t.Fatalf("yardr docs flows prints %d bytes, the page holds %d", len(out), len(want))
	}
	if out, code := executeOut(&app{}, "docs", "Readme"); code != 0 || !strings.HasPrefix(out, "# yardr documentation\n") {
		t.Fatalf("yardr docs Readme: exit %d, %.40q", code, out)
	}
	var errs bytes.Buffer
	out, code = executeOut(&app{stderr: &errs}, "docs", "flow")
	if code == 0 {
		t.Fatalf("yardr docs flow: exit 0\n%s", out)
	}
	for _, name := range []string{"flows", "groups", "line", "glossary"} {
		if !strings.Contains(errs.String(), name) {
			t.Errorf("yardr docs flow names no %q:\n%s", name, errs.String())
		}
	}
}

// yardr docs <name> <words> prints only the section whose heading
// matches: the runners without the thousand lines of groups.md.
func TestDocsShowPrintsOneSection(t *testing.T) {
	noHome(t)
	requireNonInteractive(t)
	out, code := executeOut(&app{}, "docs", "groups", "runners")
	if code != 0 {
		t.Fatalf("yardr docs groups runners: exit %d\n%s", code, out)
	}
	want, _ := docs.Section("groups", "runners")
	if out != want || !strings.HasPrefix(out, "## Runners\n") {
		t.Fatalf("yardr docs groups runners prints %.60q", out)
	}
	var errs bytes.Buffer
	out, code = executeOut(&app{stderr: &errs}, "docs", "groups", "no", "such", "heading")
	if code == 0 || !strings.Contains(errs.String(), "no section matching") {
		t.Fatalf("yardr docs groups no such heading: exit %d\n%s%s", code, out, errs.String())
	}
}

// The crew prime's How you work points at the docs the binary carries,
// which a crew member in another yard has no other way to read.
func TestPrimeCrewHowYouWorkNamesDocs(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	const want = "yardr docs [name [section]]: how the yard works: flows, groups, the line, the glossary"
	if i, j := strings.Index(out, "## How you work"), strings.Index(out, want); i < 0 || j < i {
		t.Fatalf("prime --crew names no docs line after How you work:\n%s", out)
	}
}
