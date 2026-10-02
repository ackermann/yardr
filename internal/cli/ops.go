package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

func (a *app) serverCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "The process that owns the yard's store (started on first use)"}
	cmd.AddCommand(&cobra.Command{
		Use: "run", Short: "Own the store and serve it in the foreground", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return server.Run(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
		},
	}, &cobra.Command{
		Use: "start", Short: "Start the server in the background if it is not running", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, err := server.Connect(c.Context(), cfg); err != nil {
				return err
			}
			return a.out(map[string]string{"store": cfg.DBPath(), "socket": cfg.Socket()}, func() {
				fmt.Printf("yardr server running (store %s)\n", cfg.DBPath())
			})
		},
	}, &cobra.Command{
		Use: "stop", Short: "Stop the background server", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return server.Stop(cfg)
		},
	}, &cobra.Command{
		Use: "status", Short: "Report whether the server answers", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ok := server.Running(c.Context(), cfg)
			return a.out(map[string]any{"store": cfg.DBPath(), "running": ok}, func() {
				fmt.Printf("%s running=%v\n", cfg.DBPath(), ok)
			})
		},
	})
	return cmd
}

func (a *app) daemonCmd() *cobra.Command {
	var interval time.Duration
	var attempts int
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Route ready beads to groups and supervise their sessions",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			var lastErr string
			onError := func(err error) {
				// herdr may simply not be running; say so once per distinct error.
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					log.Warn("runner events", "err", err)
				}
			}
			d := &dispatch.Dispatcher{
				Store:       st,
				Runners:     runner.All(a.cfg.Home, st, log, onError),
				Log:         log,
				Interval:    interval,
				MaxAttempts: attempts,
			}
			log.Info("yardr daemon started", "store", a.cfg.DBPath(), "reconcile", interval)
			return d.Run(ctx)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "safety-net reconcile period")
	cmd.Flags().IntVar(&attempts, "max-attempts", 3, "failed sessions per bead+stage before holding it")
	return cmd
}

func (a *app) rigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "rig", Short: "Repositories beads are worked in"}
	var base, clone string
	add := &cobra.Command{
		Use:   "add <name> [path]",
		Short: "Register (or update) a rig: an existing clone, or --clone a URL",
		Example: `  yardr rig add webapp ~/projects/webapp                 # existing clone
  yardr rig add webapp --clone git@github.com:me/webapp.git   # clones to ~/.yardr/rigs/webapp
  yardr rig add legacy ~/src/legacy --base origin/master`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			name := args[0]
			var path string
			switch {
			case len(args) == 2:
				path = args[1]
			case clone != "":
				path = filepath.Join(a.cfg.Home, "rigs", name)
			default:
				return fmt.Errorf("give a path to an existing clone, or --clone <url>")
			}
			if path, err = filepath.Abs(path); err != nil {
				return err
			}
			if clone != "" {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists; drop --clone to register it as is", path)
				}
				fmt.Fprintf(os.Stderr, "cloning %s into %s\n", clone, path)
				cl := exec.CommandContext(c.Context(), "git", "clone", clone, path)
				cl.Stdout, cl.Stderr = os.Stderr, os.Stderr
				if err := cl.Run(); err != nil {
					return fmt.Errorf("git clone: %w", err)
				}
			}
			if out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output(); err != nil {
				return fmt.Errorf("%s is not a git checkout", path)
			} else if top, real := strings.TrimSpace(string(out)), resolve(path); top != real {
				return fmt.Errorf("%s is inside the repository at %s; register the top level", path, top)
			}
			// Branches for beads start from base, so it has to resolve.
			if err := exec.Command("git", "-C", path, "rev-parse", "--verify", "--quiet", base+"^{commit}").Run(); err != nil {
				refs, _ := exec.Command("git", "-C", path, "for-each-ref", "--format=%(refname:short)",
					"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
					"refs/heads/main", "refs/heads/master").Output()
				return fmt.Errorf("base %q does not resolve in %s (found: %s); pass --base",
					base, path, strings.Join(strings.Fields(string(refs)), ", "))
			}
			r := store.Rig{Name: name, Path: path, Base: base}
			if err := st.PutRig(c.Context(), r); err != nil {
				return err
			}
			return a.out(r, func() { fmt.Printf("rig %s → %s (base %s)\n", r.Name, r.Path, r.Base) })
		},
	}
	add.Flags().StringVar(&base, "base", "origin/main", "ref bead branches start from")
	add.Flags().StringVar(&clone, "clone", "", "clone this URL first (into [path] or ~/.yardr/rigs/<name>)")
	list := &cobra.Command{
		Use: "list", Short: "List rigs", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Rigs(c.Context())
			if err != nil {
				return err
			}
			return a.out(rs, func() {
				rows := [][]string{}
				for _, r := range rs {
					rows = append(rows, []string{r.Name, r.Path, r.Base})
				}
				table("NAME\tPATH\tBASE", rows)
			})
		},
	}
	cmd.AddCommand(add, list)
	return cmd
}

func (a *app) groupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Who works beads: agent pools, code steps, humans"}
	var g store.Group
	var sets []string
	add := &cobra.Command{
		Use:   "add [<name> --runner <manual|exec|herdr>]",
		Short: "Create or replace a group (no name: an interactive form)",
		Long: `Create or replace a group. With no name, in a terminal, an interactive form
lists the existing groups to edit or creates a new one, and for herdr groups
offers the agent (Claude Code, Codex, Grok, Kimi, opencode, pi, or any other
herdr agent), a model read live from that agent, and its permission flags.

With a name, the group is replaced by exactly what the flags say.`,
		Example: `  yardr group add                     # interactive
  yardr group add webapp/builders --runner herdr --limit 2 --set kind=claude --set prompt=roles/builder.md
  yardr group add webapp/merger --runner exec --set cmd='make merge-bead'
  yardr group add humans --runner manual --set notify=true   # herdr notification per waiting bead
  yardr group add approvers --runner manual --members mckean,mayor   # only they may advance beads it holds`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
					return fmt.Errorf("give a group name and --runner, or run in a terminal for the interactive form")
				}
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				if err := a.groupForm(c.Context(), st); errors.Is(err, huh.ErrUserAborted) {
					fmt.Println("cancelled")
					return nil
				} else {
					return err
				}
			}
			if g.Runner == "" {
				return fmt.Errorf("--runner is required")
			}
			if _, ok := runner.All("", nil, nil, nil)[g.Runner]; !ok {
				return fmt.Errorf("unknown runner %q", g.Runner)
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if g.Config, err = parseSets(sets); err != nil {
				return err
			}
			g.Name = args[0]
			if err := st.PutGroup(c.Context(), g); err != nil {
				return err
			}
			saved, err := st.GetGroup(c.Context(), g.Name)
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Printf("group %s (%s)\n", saved.Name, describe(*saved)) })
		},
	}
	add.Flags().StringVar(&g.Runner, "runner", "", "runner: manual, exec, herdr")
	add.Flags().IntVar(&g.Limit, "limit", 1, "max concurrent sessions")
	add.Flags().StringArrayVar(&sets, "set", nil, "runner config key=value (repeatable)")
	add.Flags().StringSliceVar(&g.Members, "members", nil, "manual groups: actors who may move a bead out of its stages (comma-separated)")

	list := &cobra.Command{
		Use: "list", Short: "List groups", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			gs, err := st.Groups(c.Context())
			if err != nil {
				return err
			}
			return a.out(gs, func() {
				rows := [][]string{}
				for _, g := range gs {
					rows = append(rows, []string{g.Name, g.Runner, strconv.Itoa(g.Limit), or(strings.Join(g.Members, ","), "-"), fmtConfig(g.Config)})
				}
				table("NAME\tRUNNER\tLIMIT\tMEMBERS\tCONFIG", rows)
			})
		},
	}
	rm := &cobra.Command{
		Use: "rm <name>", Short: "Delete a group and its routes, releasing beads its live sessions hold", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			n, err := st.DeleteGroup(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"group": args[0], "released": n}, func() {
				if n > 0 {
					fmt.Printf("deleted group %s; ended its %d live session(s) and released their beads for routing\n", args[0], n)
				}
			})
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

func fmtConfig(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + show(m[k])
	}
	return strings.Join(parts, " ")
}

// show prints a decoded JSON value for humans. JSON numbers arrive as
// float64, which %v prints as 2.366465e+06; whole numbers print as integers.
func show(v any) string {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]any:
		return "{" + fmtConfig(x) + "}"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = show(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprint(v)
}

func (a *app) routeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "route", Short: "Which group works which stage"}
	var r store.Route
	var typ, rig, label string
	add := &cobra.Command{
		Use:   "add --stage <stage> --group <group>",
		Short: "Send open, unblocked beads in a stage to a group",
		Example: `  yardr route add --stage new --group webapp/builders --rig webapp
  yardr route add --stage review --group webapp/reviewers
  yardr route add --stage new --type video --group herald --priority 10`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if r.Stage == "" || r.Group == "" {
				return fmt.Errorf("--stage and --group are required")
			}
			fl := c.Flags()
			if fl.Changed("type") {
				r.Type = &typ
			}
			if fl.Changed("rig") {
				r.Rig = &rig
			}
			if fl.Changed("label") {
				r.Label = &label
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			saved, err := st.AddRoute(c.Context(), r)
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Println(saved.ID) })
		},
	}
	f := add.Flags()
	f.StringVarP(&r.Stage, "stage", "s", "", "stage to match")
	f.StringVarP(&r.Group, "group", "g", "", "group to send to")
	f.StringVarP(&typ, "type", "t", "", "only beads of this type")
	f.StringVarP(&rig, "rig", "r", "", "only beads in this rig")
	f.StringVarP(&label, "label", "l", "", "only beads with this label")
	f.IntVarP(&r.Priority, "priority", "p", 0, "higher wins when several routes match")

	list := &cobra.Command{
		Use: "list", Short: "List routes in match order", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			dispatch.SortRoutes(rs)
			return a.out(rs, func() {
				rows := [][]string{}
				for _, r := range rs {
					rows = append(rows, []string{r.ID, r.Stage, deref(r.Type), deref(r.Rig), deref(r.Label),
						strconv.Itoa(r.Priority), r.Group})
				}
				table("ID\tSTAGE\tTYPE\tRIG\tLABEL\tPRI\tGROUP", rows)
			})
		},
	}
	rm := &cobra.Command{
		Use: "rm <id>", Short: "Delete a route", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			if err := st.DeleteRoute(c.Context(), args[0]); err != nil {
				return err
			}
			i := slices.IndexFunc(rs, func(r store.Route) bool { return r.ID == args[0] })
			if i < 0 {
				return a.out(map[string]any{"route": args[0], "claimed": []string{}}, func() {})
			}
			r := rs[i]
			// Beads already claimed through the route stay with its group until
			// their sessions end; only new claims follow the remaining routes.
			ss, err := st.Sessions(c.Context(), false)
			if err != nil {
				return err
			}
			claimed := []string{}
			for _, s := range ss {
				if s.Group == r.Group && s.Stage == r.Stage {
					claimed = append(claimed, s.Bead)
				}
			}
			return a.out(map[string]any{"route": r.ID, "claimed": claimed}, func() {
				if len(claimed) > 0 {
					fmt.Printf("%d bead(s) in %s already claimed by %s keep it until their sessions end: %s\n",
						len(claimed), r.Stage, r.Group, strings.Join(claimed, " "))
				}
			})
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

func (a *app) sessionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Runner sessions working beads"}
	var all bool
	list := &cobra.Command{
		Use: "list", Short: "List live sessions", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ss, err := st.Sessions(c.Context(), all)
			if err != nil {
				return err
			}
			return a.out(ss, func() {
				rows := [][]string{}
				for _, s := range ss {
					// Which runner owns it, and what it is running there.
					runner := s.Runner
					if runner == "" && s.Handle["manual"] != nil {
						runner = "manual"
					}
					if v, ok := s.Handle["agent"]; ok {
						runner = strings.TrimSpace(or(runner, "herdr") + " " + show(v))
					} else if v, ok := s.Handle["pid"]; ok {
						runner = strings.TrimSpace(or(runner, "exec") + " pid " + show(v))
					}
					rows = append(rows, []string{s.ID, s.Bead, s.Stage, s.Group, s.State, or(runner, "-"),
						s.StartedAt.Local().Format("01-02 15:04")})
				}
				table("ID\tBEAD\tSTAGE\tGROUP\tSTATE\tRUNNER\tSTARTED", rows)
			})
		},
	}
	list.Flags().BoolVarP(&all, "all", "a", false, "include ended sessions")
	cmd.AddCommand(list)
	return cmd
}

func printEvent(e store.Event) {
	data := ""
	if len(e.Data) > 0 {
		data = fmtConfig(e.Data)
	}
	fmt.Printf("%6d %s  %-8s %-14s %-12s %s\n", e.Seq, e.At.Local().Format("01-02 15:04:05"), deref(e.Bead), e.Kind, e.Actor, data)
}

func (a *app) eventsCmd() *cobra.Command {
	var bead string
	var limit int
	cmd := &cobra.Command{
		Use: "events", Short: "Show the event log", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			evs, err := st.Events(c.Context(), bead, limit)
			if err != nil {
				return err
			}
			return a.out(evs, func() {
				for _, e := range evs {
					printEvent(e)
				}
			})
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "only this bead")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many")
	return cmd
}

func (a *app) watchCmd() *cobra.Command {
	var after uint64
	var fromStart bool
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream events as they happen (pushed, not polled)",
		Long: `Stream events as they are committed. Each event carries a sequence number
(seq); --after resumes a stream from where a previous consumer stopped, with
no gaps and no duplicates. If the yardr server restarts, watch reconnects and
continues from the last event it printed.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			last := after
			if !fromStart && !c.Flags().Changed("after") {
				if last, err = st.Head(ctx); err != nil {
					return err
				}
			}
			for ctx.Err() == nil {
				ch, err := st.Subscribe(ctx, last)
				if err == nil {
					for e := range ch {
						last = e.Seq
						if a.json {
							_ = a.out(e, nil)
						} else {
							printEvent(e)
						}
					}
				}
				if ctx.Err() != nil {
					break
				}
				// The server went away (or dropped us as too slow). Wait for
				// it to come back (any yard command starts it; watch never
				// does, so a deliberate stop stays stopped) and resume after
				// the last event.
				select {
				case <-ctx.Done():
				case <-time.After(500 * time.Millisecond):
				}
			}
			return nil
		},
	}
	cmd.Flags().Uint64Var(&after, "after", 0, "resume after this event seq")
	cmd.Flags().BoolVar(&fromStart, "all", false, "replay every retained event first")
	return cmd
}

// resolve follows symlinks so paths compare the way git reports them.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
