package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/docs"
)

// docsCmd prints the documentation this build of yardr carries. It
// touches neither the store nor the server, so it answers on a home
// with no yard: a crew member in any yard reads how the yard works
// with no checkout beside the binary.
func (a *app) docsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "docs [name [section]]", Short: "Read the documentation this build of yardr carries",
		Long: `Print the user documentation this build of yardr carries: the pages of
docs/, embedded so a crew member in any yard can read them with no
checkout beside the binary.

With no name it lists every page's name and first heading, in the order
of the index. With a name it prints that page; the name is the file
without its .md (flows, groups, line, glossary, ...), in any case. With
words after the name it prints only the first section whose heading
contains them, so ` + "`yardr docs groups runners`" + ` reads the runners
without the whole page.

Where a page points at another as line.md, that is yardr docs line.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			switch len(args) {
			case 0:
				return a.printDocsList(c.OutOrStdout())
			case 1:
				return a.printDoc(c.OutOrStdout(), args[0], "")
			default:
				return a.printDoc(c.OutOrStdout(), args[0], strings.Join(args[1:], " "))
			}
		},
	}
}

// printDocsList is yardr docs: every page's name and first heading, in
// the index's order.
func (a *app) printDocsList(w io.Writer) error {
	type entry struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	var entries []entry
	var rows [][]string
	for _, name := range docs.Order {
		title, _ := docs.Title(name)
		entries = append(entries, entry{Name: name, Title: title})
		rows = append(rows, []string{name, title})
	}
	return a.out(w, map[string]any{"docs": entries}, func(w io.Writer) {
		table(w, "NAME\tHEADING", rows)
	})
}

// printDoc is yardr docs <name> [section]: the page, or only the
// section whose heading contains the words.
func (a *app) printDoc(w io.Writer, name, words string) error {
	page, ok := docs.Lookup(name)
	if !ok {
		return fmt.Errorf("no doc named %q; yardr docs lists: %s", name, strings.Join(docs.Order, ", "))
	}
	title, _ := docs.Title(page)
	if words == "" {
		text, _ := docs.Read(page)
		return a.out(w, map[string]any{"name": page, "title": title, "text": text}, func(w io.Writer) {
			io.WriteString(w, text)
			if !strings.HasSuffix(text, "\n") {
				fmt.Fprintln(w)
			}
		})
	}
	text, ok := docs.Section(page, words)
	if !ok {
		return fmt.Errorf("doc %s has no section matching %q; yardr docs %s prints the page", page, words, page)
	}
	return a.out(w, map[string]any{"name": page, "title": title, "section": words, "text": text}, func(w io.Writer) {
		io.WriteString(w, text)
	})
}
