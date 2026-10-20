// Package docs carries the user documentation this build of yardr ships,
// so a crew member in any yard can read it with no checkout beside the
// binary: `yardr docs` lists it, `yardr docs <name>` prints one page.
// The pages live in this directory, at the top of the repository,
// because go:embed cannot reach out of the package naming the pattern;
// docs/ is that package, as packs/ is for the packs.
package docs

import (
	"embed"
	"strings"
)

// FS holds every page: the .md files beside this one. The
// subdirectories stay out: design notes and flow drawings are history
// and examples, not what a crew member needs at hand.
//
//go:embed *.md
var FS embed.FS

// Order is every page's name, the file without its .md, in the order
// README.md's index lists them, with the index itself first. A page
// added without its name here fails the test below, which lists the
// embedded files against this order.
var Order = []string{
	"README",
	"glossary",
	"install",
	"agents",
	"setup",
	"doctor",
	"configuration",
	"flows",
	"line",
	"dir-depots",
	"packs",
	"groups",
	"review",
	"crew",
	"users",
	"chat",
	"timers",
	"subscriptions",
	"links",
	"integrations",
	"pushover",
	"google",
	"peers",
	"reports",
	"api",
	"bdp",
	"web",
	"upgrade",
	"otel",
	"testing",
}

// Lookup returns the page named name, matching case-insensitively so
// `yardr docs readme` finds README.md. ok is false for an unknown
// name. Only a name from Order ever reaches the files below, so a
// page's text cannot be walked out of with dots.
func Lookup(name string) (page string, ok bool) {
	for _, n := range Order {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	return "", false
}

// Read returns the page's text. ok is false for an unknown name.
func Read(name string) (text string, ok bool) {
	page, ok := Lookup(name)
	if !ok {
		return "", false
	}
	b, err := FS.ReadFile(page + ".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Title returns the page's name and its first "# " heading, the line
// `yardr docs` lists it under. ok is false for an unknown name or a
// page with no heading.
func Title(name string) (title string, ok bool) {
	text, ok := Read(name)
	if !ok {
		return "", false
	}
	for line := range strings.Lines(strings.TrimSuffix(text, "\n")) {
		if t, found := strings.CutPrefix(line, "# "); found {
			return strings.TrimRight(t, "\n"), true
		}
	}
	return "", false
}

// Section returns the first section of the page whose heading contains
// words, ignoring case, from that heading to the next heading of the
// same or higher level or the end of the page. The page's own title
// never matches: it would print the whole page, which is `yardr docs
// <name>` without words. ok is false when the page is unknown or no
// section heading matches.
func Section(name, words string) (text string, ok bool) {
	text, ok = Read(name)
	if !ok {
		return "", false
	}
	words = strings.TrimSpace(words)
	if words == "" {
		return "", false
	}
	var out []string
	level := 0
	titled := false
	for line := range strings.Lines(strings.TrimSuffix(text, "\n")) {
		trimmed := strings.TrimRight(line, "\n")
		if n, head, is := cutHeading(trimmed); is {
			if out == nil {
				// The first heading is the title, not a section.
				if !titled {
					titled = true
					continue
				}
				if strings.Contains(strings.ToLower(head), strings.ToLower(words)) {
					level, out = n, []string{trimmed}
				}
				continue
			}
			if n <= level {
				break
			}
		}
		if out != nil {
			out = append(out, trimmed)
		}
	}
	if out == nil {
		return "", false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", true
}

// cutHeading splits a markdown heading line into its level and its
// text: "## Runners" is level 2, "Runners". A line that is no heading
// reports false.
func cutHeading(line string) (level int, text string, is bool) {
	rest := strings.TrimLeft(line, "#")
	if n := len(line) - len(rest); n == 0 || n == len(line) {
		return 0, "", false
	} else {
		level = n
	}
	text, is = strings.CutPrefix(rest, " ")
	if !is {
		return 0, "", false
	}
	return level, text, true
}
