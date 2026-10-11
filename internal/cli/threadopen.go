package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func (a *app) threadOpenCmd() *cobra.Command {
	var member, in, body string
	cmd := &cobra.Command{
		Use:   "open <source> --in <team|repo> <title>",
		Short: "Open a thread in a source, as yourself",
		Long: `Opens a thread of the crew member's own in a source: in Linear an issue in the
team --in names (its key or its name), in GitHub an issue in the repository
--in names (owner/repo). -b is the body (- reads it from stdin); text only.

The rules are those of yardr reply. The call is made from this process with
your own credential, from your environment (LINEAR_API_KEY; for GitHub a
GitHub App's GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, else
GITHUB_TOKEN, else, outside a crew member's session, gh auth token), and the
server never sees it. Only the crew member's own
session or the operator may speak as the crew member (--crew, default
$YARDR_CREW).

A crew member may open threads only in the sources its definition allows:
yardr crew set <crew member> open_threads=linear,github, which only the
operator may add a source to. It is off unless set, and the command then
refuses and says so.

The thread is recorded, with an id chosen for it, before it is opened, and
the id goes with the post (as the issue's id in Linear, as a marker in the
body on GitHub). Run again with the same source, place, title and body
after a lost answer, yardr first looks for the thread that id made and
does not open it twice. The thread is the crew member's at once: it takes
part in it (yardr thread list shows it, how=opened), so an involved
subscription delivers what others say there with no mention, to be
answered with yardr reply; a thread_opened event is logged. Prints the URL
of the thread.`,
		Example: `  yardr thread open linear --in YRD "Web view: train" -b - < train.md
  yardr thread open github --in acme/webapp "Retries drop the last attempt" -b "Seen in yard-k3f9."`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			name, err := currentCrew(member)
			if err != nil {
				return err
			}
			source, title := args[0], strings.TrimSpace(args[1])
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if _, err := st.GetCrew(ctx, name); err != nil {
				return err
			}
			d, err := declaredSource(ctx, st, source)
			if err != nil {
				return err
			}
			o, ok := hooks.OpenerFor(source, d, a.home())
			if !ok {
				return fmt.Errorf("yardr cannot open threads in %s (it can in %s)%s", source, strings.Join(hooks.Openable(allDeclarations(ctx, st)...), ", "), packHint(ctx, st, source))
			}
			if in = strings.TrimSpace(in); in == "" {
				return fmt.Errorf("--in is required: the %s to open the thread in", o.Place())
			}
			if title == "" {
				return fmt.Errorf("nothing to open: the title is empty")
			}
			if body == "-" {
				b, err := io.ReadAll(c.InOrStdin())
				if err != nil {
					return err
				}
				body = string(b)
			}
			body = strings.TrimSpace(body)
			// The thread is on record, with its key, before anything is
			// posted, and the record is where a crew member that may not open
			// threads in the source is refused.
			rp, err := st.Opening(ctx, name, source, in, title, body, a.who())
			if err != nil {
				return err
			}
			t, already := hooks.Thread{ID: rp.Thread, Title: rp.Title, URL: rp.ThreadURL}, rp.Posted
			if !already {
				cred, err := o.Credential(ctx)
				if err != nil {
					return err
				}
				if rp.Attempts > 1 {
					// An earlier attempt got as far as posting, maybe: the
					// source may hold the thread the key made.
					found, err := o.Opened(ctx, in, rp.Key, cred, rp.At)
					if err != nil {
						return fmt.Errorf("looking for an earlier attempt's thread in %s %s: %w", source, in, err)
					}
					if found != nil {
						t, already = *found, true
					}
				}
				if !already {
					t, err = o.Open(ctx, in, title, body, rp.Key, cred)
					if err != nil {
						return fmt.Errorf("open a thread in %s %s: %w", source, in, err)
					}
				}
				rec := store.Reply{Source: source, In: in, Thread: t.ID, Title: or(t.Title, title), ThreadURL: t.URL,
					URL: t.URL, Key: rp.Key}
				if err := st.Opened(ctx, name, rec, name); err != nil {
					return fmt.Errorf("opened %s, but could not record it: %w", t.URL, err)
				}
			}
			return a.out(map[string]any{"crew": name, "source": source, "in": in, "thread": t.ID,
				"title": or(t.Title, title), "url": t.URL, "repeated": already},
				func() {
					if already {
						fmt.Fprintf(c.ErrOrStderr(), "already opened as %s; not opened again\n", t.URL)
					}
					fmt.Println(t.URL)
				})
		},
	}
	cmd.Flags().StringVar(&member, "crew", "", "who is opening it (default $YARDR_CREW)")
	cmd.Flags().StringVar(&in, "in", "", "where: a Linear team's key or name, a GitHub owner/repo")
	cmd.Flags().StringVarP(&body, "body", "b", "", "the thread's body (- reads stdin)")
	return cmd
}
