package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mckean/yard/internal/config"
	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/runner"
	"github.com/mckean/yard/internal/server"
	"github.com/mckean/yard/internal/store"
)

func (a *app) serverCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Manage the local SurrealDB server"}
	cmd.AddCommand(&cobra.Command{
		Use: "start", Short: "Start the server if it is not running", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := server.Ensure(c.Context(), cfg); err != nil {
				return err
			}
			return a.out(map[string]string{"addr": cfg.Addr, "data": cfg.DataDir()}, func() {
				fmt.Printf("surreal running on %s (data %s)\n", cfg.Addr, cfg.DataDir())
			})
		},
	}, &cobra.Command{
		Use: "stop", Short: "Stop the server yard started", Args: cobra.NoArgs,
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
			ok := server.Healthy(c.Context(), cfg)
			return a.out(map[string]any{"addr": cfg.Addr, "healthy": ok}, func() {
				fmt.Printf("%s healthy=%v\n", cfg.Addr, ok)
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
				Runners:     runner.All(a.cfg.Home, onError),
				Log:         log,
				Interval:    interval,
				MaxAttempts: attempts,
			}
			log.Info("yard daemon started", "store", a.cfg.Addr, "reconcile", interval)
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
		Example: `  yard rig add webapp ~/projects/webapp                 # existing clone
  yard rig add webapp --clone git@github.com:me/webapp.git   # clones to ~/.yard/rigs/webapp
  yard rig add legacy ~/src/legacy --base origin/master`,
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
	add.Flags().StringVar(&clone, "clone", "", "clone this URL first (into [path] or ~/.yard/rigs/<name>)")
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
		Use:   "add <name> --runner <manual|exec|herdr>",
		Short: "Create or replace a group",
		Example: `  yard group add webapp/builders --runner herdr --limit 2 --set kind=claude --set prompt=roles/builder.md
  yard group add webapp/merger --runner exec --set cmd='make merge-bead'
  yard group add humans --runner manual`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if g.Runner == "" {
				return fmt.Errorf("--runner is required")
			}
			if _, ok := runner.All("", nil)[g.Runner]; !ok {
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
			return a.out(saved, func() { fmt.Printf("group %s (%s, limit %d)\n", saved.Name, saved.Runner, saved.Limit) })
		},
	}
	add.Flags().StringVar(&g.Runner, "runner", "", "runner: manual, exec, herdr")
	add.Flags().IntVar(&g.Limit, "limit", 1, "max concurrent sessions")
	add.Flags().StringArrayVar(&sets, "set", nil, "runner config key=value (repeatable)")

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
					rows = append(rows, []string{g.Name, g.Runner, strconv.Itoa(g.Limit), fmtConfig(g.Config)})
				}
				table("NAME\tRUNNER\tLIMIT\tCONFIG", rows)
			})
		},
	}
	rm := &cobra.Command{
		Use: "rm <name>", Short: "Delete a group and its routes", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			return st.DeleteGroup(c.Context(), args[0])
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
		parts[i] = fmt.Sprintf("%s=%v", k, m[k])
	}
	return strings.Join(parts, " ")
}

func (a *app) routeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "route", Short: "Which group works which stage"}
	var r store.Route
	var typ, rig, label string
	add := &cobra.Command{
		Use:   "add --stage <stage> --group <group>",
		Short: "Send open, unblocked beads in a stage to a group",
		Example: `  yard route add --stage new --group webapp/builders --rig webapp
  yard route add --stage review --group webapp/reviewers
  yard route add --stage new --type video --group herald --priority 10`,
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
			return st.DeleteRoute(c.Context(), args[0])
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
					agent := ""
					if v, ok := s.Handle["agent"]; ok {
						agent = fmt.Sprint(v)
					} else if v, ok := s.Handle["pid"]; ok {
						agent = fmt.Sprintf("pid %v", v)
					}
					rows = append(rows, []string{s.ID, s.Bead, s.Stage, s.Group, s.State, agent,
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
	fmt.Printf("%s  %-8s %-14s %-12s %s\n", e.At.Local().Format("01-02 15:04:05"), deref(e.Bead), e.Kind, e.Actor, data)
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
	return &cobra.Command{
		Use:   "watch",
		Short: "Stream events as they happen (pushed, not polled)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			ch, err := st.TailEvents(ctx)
			if err != nil {
				return err
			}
			for e := range ch {
				if a.json {
					_ = a.out(e, nil)
				} else {
					printEvent(e)
				}
			}
			return contextErr(ctx)
		},
	}
}

func contextErr(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil // interrupted by the user
	}
	return fmt.Errorf("event stream closed")
}

// resolve follows symlinks so paths compare the way git reports them.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
