package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"io"

	"yardr.dev/yardr/internal/preview"
	"yardr.dev/yardr/internal/store"
)

// A preview is a program the yard's crew built, run in the yard under
// its supervisor and opened by the yard's person through the edge.
// The records are the store's; the supervisor, part of yardr server,
// does the running.
// This file is the crew's face: propose, watch, list, stop, logs and
// forget. Start and restart have no crew command: a start is the
// person's approval or Start, the supervisor's crash restart, or the
// on-demand start when its person visits. A watch is the one start a
// crew member makes, because it runs nothing of theirs: the supervisor
// serves files.

func (a *app) previewCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "preview", Short: "Propose and tend the yard's previews of built apps",
		Long: `A preview is a built app the yard keeps running for its person to
open through the edge. A crew member proposes one with its start
command, directory and loopback port; the yard records the depot
commit it read itself, and nothing runs until the yard's person
approves the card (in the web view, a later bead). The supervisor in
yardr server then starts the approved command, captures its output
to ~/.yardr/previews/<app>.log, restarts a crash with backoff, and
stops it after preview_idle without a request (four hours while
unset) or seven days running. An approval is for the commit and dirty
mark its card showed: a start or restart on a tree that reads
otherwise takes the approval back, and the card waits pending for a
new one.

A watch is a preview of another kind, for a site being built: the yard
itself serves the files of a directory of a bead's worktree, read-only,
and the page reloads when a file changes. It runs no command, so there
is nothing to approve. It is plain files only: a site that needs a build
step is an app, proposed as above.`}
	cmd.AddCommand(a.previewProposeCmd(), a.previewWatchCmd(), a.previewListCmd(), a.previewStopCmd(), a.previewLogsCmd(), a.previewForgetCmd())
	return cmd
}

func (a *app) previewProposeCmd() *cobra.Command {
	var do, in string
	var port int
	c := &cobra.Command{
		Use:   "propose <app> --do <command> --in <dir> --port <n>",
		Short: "Propose a preview: record its command, directory, port and the depot commit the yard reads",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			app := args[0]
			if err := store.CheckPreviewApp(app); err != nil {
				return err
			}
			if do == "" {
				return fmt.Errorf("preview propose: --do <command> is required")
			}
			if in == "" {
				return fmt.Errorf("preview propose: --in <dir> is required")
			}
			abs, err := filepath.Abs(in)
			if err != nil {
				return err
			}
			if st, err := os.Stat(abs); err != nil || !st.IsDir() {
				return fmt.Errorf("preview propose: --in %s: not a directory", in)
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			p, err := st.ProposePreview(c.Context(), store.NewPreview{App: app, Command: do, Dir: abs, Port: port}, a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), p, func(w io.Writer) {
				fmt.Fprintf(w, "preview %s proposed: pending approval (port %d, commit %s)\n", p.App, p.Port, shortCommit(p.Commit))
			})
		},
	}
	f := c.Flags()
	f.StringVar(&do, "do", "", "the start command, run through the yard's shell in --in")
	f.StringVar(&in, "in", "", "the directory the command runs in")
	f.IntVar(&port, "port", 0, "the loopback port the app will bind")
	return c
}

func (a *app) previewWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch <bead> [dir]",
		Short: "Serve a bead's worktree files live, reloading the page when one changes",
		Long: `Serve the files of a directory of a bead's worktree through the preview
endpoint, read-only, so a person can watch the site take shape while the
bead is built. dir is read against the worktree (mockups, say) and
defaults to the worktree itself. A file or directory whose name starts
with a dot is never served, nor anything outside dir, and a directory
answers its index.html. A page is given a small script that reloads it
when a file in dir has changed and stood still for a moment.

The bead's builder, the user it is for or an operator may start a watch.
It ends when the bead leaves the stage it is in now or is closed, after
the idle time without a request (preview_idle, four hours while unset), or
seven days after it began, and the same command starts it again at the
same address. Put the address it prints in the bead's note.

A watch serves plain files. A site that needs a build step runs the
builder's code, and is an app: yardr preview propose.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			dir := ""
			if len(args) == 2 {
				dir = args[1]
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			p, err := st.WatchPreview(c.Context(), args[0], dir, a.who())
			if err != nil {
				return err
			}
			settings, _ := st.Settings(c.Context())
			return a.out(c.OutOrStdout(), p, func(w io.Writer) {
				fmt.Fprintf(w, "watching %s: %s is served read-only, and its pages reload when a file changes\n", p.Bead, p.Dir)
				fmt.Fprintf(w, "opens at /preview/%s/\n", p.App)
				fmt.Fprintf(w, "ends when %s leaves %s, after %s without a request, or in %s\n",
					p.Bead, p.Stage, store.Span(store.PreviewIdle(settings)), store.Span(store.PreviewAgeLimit))
			})
		},
	}
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	if commit == "" {
		return "-"
	}
	return commit
}

func (a *app) previewListCmd() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List the yard's previews: app, state, commit and last request", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			all, err := st.Previews(c.Context())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), all, func(w io.Writer) {
				rows := make([][]string, 0, len(all))
				for _, p := range all {
					state := p.State
					if p.StateDetail != "" && p.State != store.PreviewRunning {
						state += " (" + p.StateDetail + ")"
					}
					last := "-"
					if p.LastRequestAt != nil {
						last = p.LastRequestAt.Format(time.RFC3339)
					}
					// A watch holds no commit or port: it serves a worktree as it is.
					kind, port := "app", strconv.Itoa(p.Port)
					if p.Kind == store.PreviewKindWatch {
						kind, port = p.Kind, "-"
					}
					rows = append(rows, []string{p.App, state, shortCommit(p.Commit), last, port, kind})
				}
				table(w, "APP\tSTATE\tCOMMIT\tLAST REQUEST\tPORT\tKIND", rows)
			})
		},
	}
}

func (a *app) previewStopCmd() *cobra.Command {
	return &cobra.Command{
		Use: "stop <app>", Short: "Stop a preview (its proposer or the yard's person)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			p, err := st.PreviewStop(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), p, func(w io.Writer) {
				fmt.Fprintf(w, "preview %s stopped\n", p.App)
			})
		},
	}
}

func (a *app) previewLogsCmd() *cobra.Command {
	var tail int
	c := &cobra.Command{
		Use: "logs <app>", Short: "Print a preview's captured output", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			// The row must exist, so a mistyped app is refused
			// rather than printing nothing.
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if _, err := st.GetPreview(c.Context(), args[0]); err != nil {
				return err
			}
			lines, err := preview.LogTail(cfg.PreviewLog(args[0]), tail)
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"app": args[0], "lines": lines}, func(w io.Writer) {
				for _, line := range lines {
					fmt.Fprintln(w, line)
				}
			})
		},
	}
	c.Flags().IntVar(&tail, "tail", 100, "how many of the last lines to print")
	return c
}

func (a *app) previewForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use: "forget <app>", Short: "Remove a preview and its approval (the yard's person)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			p, err := st.ForgetPreview(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), p, func(w io.Writer) {
				fmt.Fprintf(w, "preview %s forgotten\n", p.App)
			})
		},
	}
}
