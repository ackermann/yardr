package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
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
			if _, err := connect(c.Context(), cfg); err != nil {
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
			cfg, err := config.LoadReadOnly()
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
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// One daemon per yard: a second would route the same beads and
			// supervise the same sessions. The lock is taken before the
			// store is touched and held until this process ends. A
			// self-replace keeps the pid but execs a new image: the lock's
			// descriptor is close-on-exec, so the exec releases it and the
			// new build takes it here again, after the old image is gone.
			unlock, err := lockDaemon(cfg.DaemonLock(), time.Second)
			if err != nil {
				return err
			}
			defer unlock()
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			ctx, release, err := whileHomeStands(ctx, cfg, homeCheck, log)
			if err != nil {
				return err
			}
			defer release()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			var lastErr string
			onError := func(err error) {
				// herdr may simply not be running; say so once per distinct error.
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					log.Warn("runner events", "err", err)
				}
			}
			keep, err := retention()
			if err != nil {
				return err
			}
			bin, err := version.Self()
			if err != nil {
				log.Warn("cannot watch the yardr binary; the daemon will not replace itself", "err", err)
			}
			d := &dispatch.Dispatcher{
				Store:       st,
				Runners:     runner.All(a.cfg.Home, st, log, onError),
				Log:         log,
				Home:        a.cfg.Home,
				Interval:    interval,
				MaxAttempts: attempts,
				Binary:      bin,
				Retention:   keep,
				PeerKey:     a.cfg.PeerKey(),
			}
			log.Info("yardr daemon started", "store", a.cfg.DBPath(), "reconcile", interval)
			return d.Run(ctx)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "safety-net reconcile period")
	cmd.Flags().IntVar(&attempts, "max-attempts", 3, "failed sessions per bead+stage before holding it")
	v := a.longRunningCmds(cmd, cmd, kindOf(svcDaemon))
	v.install.Long = `Write the daemon as a login service of this user and load it: a launchd
agent in ~/Library/LaunchAgents on macOS, a systemd user unit in
~/.config/systemd/user on Linux. The service manager starts the daemon now
and at every login, and starts it again when it ends with a failure. A daemon
that runs already is stopped first: a yard has one.

The service is this yard's: its name carries the home, so a second yard gets
a service of its own. Its file holds the yardr that ran the install (by its
path), YARDR_HOME, and the PATH of the shell it was installed from, on which
herdr, git and the harnesses have to be found. It gets nothing else of that
shell: an install says what is not on that PATH. It logs to
<home>/daemon.log.

On macOS it also says which depots are in ~/Desktop, ~/Documents or
~/Downloads, where a daemon started at login has no access until it is
granted its own. On any other system it says that it is not supported and how
to run the daemon by hand.

--file-only writes the file and loads nothing. docs/configuration.md,
"Login services", says what a reboot and an upgrade then do.`
	v.install.Example = `  yardr daemon install
  yardr daemon install --file-only      # write the file, load nothing
  yardr daemon status
  yardr daemon uninstall`
	v.uninstall.Long = `Take the daemon's login service from its manager and delete its file: all
that yardr daemon install made. The manager stops the daemon the service
ran; the sessions that daemon started carry on in herdr. A daemon started any
other way is not the service's and is left running.

--file-only deletes the file and unloads nothing.`
	v.start.Long = `Start yardr daemon detached from this terminal, logging to
<home>/daemon.log, wait until it holds the home's lock and print its pid. It
keeps the environment of the shell it is started from, as a daemon in a pane
of its own has it. yardr init --start does the same and waits for the
yardmaster's session too; yardr daemon install starts one at every login.`
	v.stop.Long = `Tell the daemon that holds the home's lock to stop, and wait until it has:
it finishes the pass it is in. The sessions it started carry on in herdr, and
nothing is dispatched until a daemon runs again. yardr down stops the daemon
and those sessions, and the yard's server with them.

A daemon that runs as a login service stays stopped too: the service starts
it again at the next login, or when it ends with a failure, and a stop is
none. yardr daemon start starts one in the background before that.`
	return cmd
}

func (a *app) depotCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "depot", Short: "Repositories (or plain directories) beads are worked in"}
	var base, clone, test, kind string
	add := &cobra.Command{
		Use:   "add <name> [path]",
		Short: "Register (or update) a depot: an existing clone, --clone a URL, or a directory",
		Long: `Register (or update) a depot. A git depot is a repository: beads work on
branches in worktrees and land on its --base. A dir depot is a plain directory
(documents, images, invoices): beads are worked in it as it is, and it has no
base. Without --kind, a path git sees as a checkout is a git depot and any
other directory a dir depot; depot add says which it took.`,
		Example: `  yardr depot add webapp ~/projects/webapp                 # existing clone
  yardr depot add invoices ~/Documents/invoices --kind dir # a folder, no git
  yardr depot add webapp --clone git@github.com:me/webapp.git   # clones to ~/.yardr/depots/webapp
  yardr depot add legacy ~/src/legacy --base origin/master
  yardr depot add webapp ~/projects/webapp --test 'make test'    # what tests it, for agents and scripts`,
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
				path = filepath.Join(a.cfg.Home, store.DepotsDir, name)
			default:
				return fmt.Errorf("give a path to an existing clone, or --clone <url>")
			}
			if path, err = filepath.Abs(path); err != nil {
				return err
			}
			if kind != "" && kind != store.DepotGit && kind != store.DepotDir {
				return fmt.Errorf("--kind %s: give %s or %s", kind, store.DepotGit, store.DepotDir)
			}
			if clone != "" && kind == store.DepotDir {
				return fmt.Errorf("--clone makes a git depot; drop --kind dir")
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
			if info, err := os.Stat(path); err != nil {
				return err
			} else if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", path)
			}
			if kind == "" {
				kind = detectDepotKind(path)
			}
			if kind == store.DepotDir {
				if c.Flags().Changed("base") {
					return fmt.Errorf("a dir depot has no base; drop --base, or register a git checkout")
				}
				r := store.Depot{Name: name, Path: path, Kind: store.DepotDir, Test: test}
				if err := st.PutDepot(c.Context(), r, a.who()); err != nil {
					return err
				}
				return a.out(r, func() {
					fmt.Printf("depot %s → %s (dir: no git, no base)\n", r.Name, r.Path)
					if r.Test != "" {
						fmt.Printf("test: %s\n", r.Test)
					}
				})
			}
			if out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output(); err != nil {
				return fmt.Errorf("%s is not a git checkout; --kind dir registers it as a plain directory", path)
			} else if top, real := strings.TrimSpace(string(out)), resolve(path); top != real {
				return fmt.Errorf("%s is inside the repository at %s; register the top level (or --kind dir)", path, top)
			}
			// Branches for beads start from base, so it has to resolve.
			if err := exec.Command("git", "-C", path, "rev-parse", "--verify", "--quiet", base+"^{commit}").Run(); err != nil {
				refs, _ := exec.Command("git", "-C", path, "for-each-ref", "--format=%(refname:short)",
					"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
					"refs/heads/main", "refs/heads/master").Output()
				return fmt.Errorf("base %q does not resolve in %s (found: %s); pass --base",
					base, path, strings.Join(strings.Fields(string(refs)), ", "))
			}
			r := store.Depot{Name: name, Path: path, Kind: store.DepotGit, Base: base, Test: test}
			if err := st.PutDepot(c.Context(), r, a.who()); err != nil {
				return err
			}
			return a.out(r, func() {
				fmt.Printf("depot %s → %s (git, base %s)\n", r.Name, r.Path, r.Base)
				if r.Test != "" {
					fmt.Printf("test: %s\n", r.Test)
				}
			})
		},
	}
	add.Flags().StringVar(&base, "base", "origin/main", "ref bead branches start from (git depots)")
	add.Flags().StringVar(&kind, "kind", "", "git or dir (default: git when the path is a git checkout, else dir)")
	add.Flags().StringVar(&clone, "clone", "", "clone this URL first (into [path] or ~/.yardr/depots/<name>)")
	add.Flags().StringVar(&test, "test", "", "the command that tests the project (sh -c): yardr never runs it; a bead's prime prints it and a script group gets it as YARDR_TEST")
	show := &cobra.Command{
		Use: "show <depot>", Short: "Show a depot: path, kind, base, flow and test command", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			r, err := st.GetDepot(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.out(r, func() {
				fmt.Printf("depot %s\n", r.Name)
				fmt.Printf("  path:  %s\n", r.Path)
				fmt.Printf("  kind:  %s\n", r.Kind)
				if r.IsDir() {
					fmt.Printf("  base:  none (a dir depot)\n")
				} else {
					fmt.Printf("  base:  %s\n", r.Base)
				}
				fmt.Printf("  flow:  %s\n", depotFlow(c.Context(), st, r.Name))
				if r.Test != "" {
					fmt.Printf("  test:  %s\n", r.Test)
				}
			})
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "List depots", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Depots(c.Context())
			if err != nil {
				return err
			}
			return a.out(rs, func() {
				rows := [][]string{}
				for _, r := range rs {
					rows = append(rows, []string{r.Name, r.Kind, r.Path, or(r.Base, "-"), depotFlow(c.Context(), st, r.Name)})
				}
				table("NAME\tKIND\tPATH\tBASE\tFLOW", rows)
			})
		},
	}

	var flowType string
	var clear bool
	flow := &cobra.Command{
		Use:   "flow <depot> [--type <type>] (<flow> | --clear)",
		Short: "Point a depot, or its beads of one type, at a named flow",
		Example: `  yardr depot flow yard code                   # the depot's flow
  yardr depot flow yard --type decision decide  # wins for decisions
  yardr depot flow yard --type decision --clear # decisions follow the depot's flow again

The flow is a named flow in the store (yardr flow set) or one in the depot's
repository (.yardr/flows/<name>.flow on the base branch). A pointer
overrides the repository's default.flow and type/<type>.flow.

Setting or clearing a pointer is refused, naming the beads, while an open
bead it would hand to another flow sits in a stage that flow lacks. Move
them under the flow they follow now, or pin them to it (yardr bead update
<id> --meta flow=<name>), then point the depot again.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			name := ""
			switch {
			case clear && len(args) == 2:
				return fmt.Errorf("give a flow or --clear, not both")
			case !clear && len(args) == 1:
				return fmt.Errorf("give a flow, or --clear to remove the pointer")
			case len(args) == 2:
				name = args[1]
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.SetFlowPointer(c.Context(), args[0], flowType, name, a.who()); err != nil {
				return err
			}
			who := store.FlowPointer{Depot: args[0], Type: flowType}.String()
			if clear {
				fmt.Printf("%s points at no flow now; it follows %s\n", who, depotFlow(c.Context(), st, args[0]))
				return nil
			}
			fmt.Printf("%s → flow %s\n", who, name)
			return nil
		},
	}
	flow.Flags().StringVarP(&flowType, "type", "t", "", "only the depot's beads of this type")
	flow.Flags().BoolVar(&clear, "clear", false, "remove the pointer")
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a depot from the store with its routes and flow pointers (refused while it has open beads or live sessions)",
		Example: `  yardr depot rm yard          # registered under the wrong name; nothing uses it
  yardr depot rm legacy --json # what went with it: its routes and flow pointers

Only the store changes. The clone, its worktrees and its .yardr/ stay where
they are; remove them yourself if you want them gone. Closed beads keep the
depot's name as history. A depot with open beads or live sessions is refused and
the beads and sessions named: close or move the beads first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			r, err := st.DeleteDepot(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(r, func() {
				ptrs := make([]string, 0, len(r.FlowPointers))
				for _, p := range r.FlowPointers {
					ptrs = append(ptrs, p.String())
				}
				fmt.Printf("removed depot %s; %d route(s) and %d flow pointer(s) went with it\n", r.Depot, len(r.Routes), len(ptrs))
				if len(r.Routes) > 0 {
					fmt.Printf("  routes: %s\n", strings.Join(r.Routes, " "))
				}
				if len(ptrs) > 0 {
					fmt.Printf("  flow pointers: %s\n", strings.Join(ptrs, ", "))
				}
				fmt.Println("nothing on disk was touched")
			})
		},
	}
	cmd.AddCommand(add, list, show, flow, rm, a.depotInitCmd(), a.depotSourcesCmd(), a.depotSourceCmd())
	return cmd
}

// depotFlow names the flow governing depot's beads (pins and types aside) and
// where it comes from, for listings.
func depotFlow(ctx context.Context, st store.API, depot string) string {
	rfs, err := st.DepotFlows(ctx, depot)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(rfs) == 0 || rfs[0].Type != "" {
		return "-"
	}
	return rfs[0].Flow.Name + " (" + rfs[0].Source.String() + ")"
}

func (a *app) groupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Who works beads: agent pools, code steps, humans"}
	var g store.Group
	var sets []string
	add := &cobra.Command{
		Use:   "add [<name> --runner <manual|exec|herdr|peer>]",
		Short: "Create or replace a group (no name: an interactive form)",
		Long: `Create or replace a group. With no name, in a terminal, an interactive form
lists the existing groups to edit or creates a new one, and for herdr groups
offers the agent (Claude Code, Codex, Grok, Kimi, opencode, pi, or any other
herdr agent), a model read live from that agent, and its permission flags.

With a name, the group is replaced by exactly what the flags say, except that
a herdr group without --set kind gets the yard's default_kind (yardr settings
get default_kind). To change some settings of a group and keep the rest:
yardr group set.

A peer group hands its beads to a linked yard (--set peer=<name>, a peer whose
link carries bead out: yardr peer allow <name> --send bead). The bead waits in
its stage until that yard returns it, with the outcome its flow there came to.
--limit is how many beads may be there at once, --set tries=<n> how often the
bead is posted before it is given up as undelivered (5), and --set max_age how
long one is away before the yardmaster hears (24h). A branch that yard returns
is fetched from the bead's depot's remote origin, or --set remote=<name>, before
the outcome moves the bead. See docs/peers.md, "The sending side: a stage
worked by a linked yard" and "Code that comes back".

An agent nobody watches stops at every permission prompt. These flags keep it
going (pass them as --set 'args=[...]'; yardr init gives them to the
yardmaster):
` + agents.UnattendedHelp(),
		Example: `  yardr group add                     # interactive
  yardr group add webapp/builders --runner herdr --limit 2 --set kind=claude --set prompt=roles/builder.md
  yardr group add webapp/merger --runner exec --set cmd='make merge-bead'
  yardr group add humans --runner manual --set notify=true   # herdr notification per waiting bead
  yardr group add approvers --runner manual --members mckean,yardmaster   # only they may advance beads it holds
  yardr group add at-north --runner peer --set peer=north --limit 3   # the linked yard north works its beads`,
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
			withDefaultKind(c.Context(), st, g.Runner, g.Config)
			if err := st.PutGroup(c.Context(), g, a.who()); err != nil {
				return err
			}
			saved, err := st.GetGroup(c.Context(), g.Name)
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Printf("group %s (%s)\n", saved.Name, describe(*saved)) })
		},
	}
	add.Flags().StringVar(&g.Runner, "runner", "", "runner: manual, exec, herdr, peer")
	add.Flags().IntVar(&g.Limit, "limit", 1, "max concurrent sessions; for a peer group, how many beads may be at that yard at once")
	add.Flags().StringArrayVar(&sets, "set", nil, "runner config key=value (repeatable); a value that is JSON (true, 3, [\"a\"]) keeps its type, so a string that reads as JSON is written as one: 'cmd=\"true\"'")
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
			grants, err := st.SourceGrants(c.Context(), "")
			if err != nil {
				return err
			}
			words := grantWords(c.Context(), st)
			return a.out(gs, func() {
				rows := [][]string{}
				for _, g := range gs {
					// What the group's sessions may call of the declared
					// sources: "crm: note (update), read; wiki: page".
					var calls []string
					for _, gr := range grantedTo(grants, store.Grantee{Group: g.Name}) {
						calls = append(calls, gr.Source+": "+words(gr))
					}
					rows = append(rows, []string{g.Name, g.Runner, strconv.Itoa(g.Limit), or(strings.Join(g.Members, ","), "-"), or(strings.Join(calls, "; "), "-"), fmtSets(g.Config)})
				}
				table("NAME\tRUNNER\tLIMIT\tMEMBERS\tCALLS\tCONFIG", rows)
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
	show := &cobra.Command{
		Use: "show <name>", Short: "Show a group: its settings, the routes that send it work, what it may call", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			g, err := st.GetGroup(c.Context(), args[0])
			if err != nil {
				return err
			}
			all, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			routes := []store.Route{}
			for _, r := range all {
				if r.Names(g.Name) {
					routes = append(routes, r)
				}
			}
			grants, err := st.SourceGrants(c.Context(), "")
			if err != nil {
				return err
			}
			mayCall := grantedTo(grants, store.Grantee{Group: g.Name})
			out := map[string]any{"group": g, "routes": routes}
			kind, _ := g.Config["kind"].(string)
			if h, ok := agents.Lookup(kind); ok {
				out["harness"] = map[string]any{"kind": kind, "always": h.Flags.Always}
			}
			if len(mayCall) > 0 {
				out["calls"] = mayCall
			}
			words := grantWords(c.Context(), st)
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			operator := settings[store.OperatorSetting]
			return a.out(out, func() {
				fmt.Printf("%s  %s\n", g.Name, describe(*g))
				if h, ok := agents.Lookup(kind); ok && len(h.Flags.Always) > 0 {
					fmt.Printf("  harness %s always: %s\n", kind, strings.Join(h.Flags.Always, " "))
				}
				// One key to a line, as group set takes it back; values
				// named like secrets stay out of terminal scrollback.
				for _, k := range slices.Sorted(maps.Keys(g.Config)) {
					v := g.Config[k]
					if secretSetting(k) {
						v = hidden(true)
					}
					fmt.Printf("  %s\n", fmtSets(map[string]any{k: v}))
				}
				for _, r := range routes {
					line := "stage " + r.Stage
					set := func(p *string) string {
						if p == nil {
							return ""
						}
						return *p
					}
					orig := operator
					if r.Originator != nil {
						orig = *r.Originator
					}
					for _, f := range []struct{ name, v string }{{"type", set(r.Type)}, {"depot", set(r.Depot)},
						{"label", r.LabelList()}, {"beads", r.BeadPriorities()}, {"originator", orig}} {
						if f.v != "" {
							line += " · " + f.name + " " + f.v
						}
					}
					fmt.Printf("  route %s: %s · priority %d\n", r.ID, line, r.Priority)
				}
				for _, gr := range mayCall {
					fmt.Printf("  calls %s: %s\n", gr.Source, words(gr))
				}
			})
		},
	}
	cmd.AddCommand(add, a.groupSetCmd(), list, show, rm)
	return cmd
}

// groupSetCmd is yardr group set: some settings of a group changed, the
// rest kept, as crew set does for a crew member. group add cannot do that,
// as it writes the whole group: raising a limit with it costs the group
// every --set that is not retyped.
func (a *app) groupSetCmd() *cobra.Command {
	var unset, members []string
	var limit int
	cmd := &cobra.Command{
		Use:   "set <name> [key=value ...]",
		Short: "Change some settings of a group and keep the rest",
		Long: `Changes the settings named and keeps the rest of the group. key=value is a
runner config key, as --set on yardr group add takes it; --unset <key> removes
one. --limit and --members change what the same flags of add set. The runner
is not changed here: another runner reads another config, so that is yardr
group add, which writes the group whole.

It prints each setting changed, as it was and as it is. The value of a key
named like a secret (token, secret, password, key) is not printed.`,
		Example: `  yardr group set webapp/builders model=opus
  yardr group set webapp/builders --limit 3
  yardr group set webapp/builders 'args=["--permission-mode","auto"]' --unset idle
  yardr group set approvers --members mckean,yardmaster`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			name := args[0]
			// parseSets would speak of --set, which this command has not.
			for _, kv := range args[1:] {
				if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
					return fmt.Errorf("%q: want key=value (--unset <key> removes a setting)", kv)
				}
			}
			e := store.GroupEdit{Unset: unset}
			if e.Set, err = parseSets(args[1:]); err != nil {
				return err
			}
			fl := c.Flags()
			if len(e.Set) == 0 && len(unset) == 0 && !fl.Changed("limit") && !fl.Changed("members") {
				return fmt.Errorf("nothing to set: give key=value, --unset <key>, --limit or --members")
			}
			for _, k := range unset {
				if _, both := e.Set[k]; both {
					return fmt.Errorf("%s is both set and unset", k)
				}
			}
			if fl.Changed("limit") {
				e.Limit = &limit
			}
			if fl.Changed("members") {
				e.Members = &members
			}
			// The store holds the group as it would be to what its runner
			// reads (store.ConfigError), as it does for group add.
			ch, err := st.SetGroup(ctx, name, e, a.who())
			if err != nil {
				return bareSetHint(err)
			}
			changes := groupChanges(ch.Was, ch.Is)
			return a.out(ch.Is, func() {
				if len(changes) == 0 {
					fmt.Printf("group %s unchanged: it is set so already\n", name)
					return
				}
				fmt.Printf("group %s saved\n%s\n", name, changeLines(changes))
			})
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&unset, "unset", nil, "remove this runner config key (repeatable)")
	f.IntVar(&limit, "limit", 0, "max concurrent sessions; for a peer group, how many beads may be at that yard at once")
	f.StringSliceVar(&members, "members", nil, "manual groups: actors who may move a bead out of its stages; replaces the list, so name every member wanted (empty: anyone)")
	return cmd
}

// groupChanges lists what a group set changed, the fields first and the
// config keys by name, as defChanges does for a crew member.
func groupChanges(was, is store.Group) []defChange {
	var out []defChange
	if was.Limit != is.Limit {
		out = append(out, defChange{"--limit", strconv.Itoa(was.Limit), strconv.Itoa(is.Limit)})
	}
	if a, b := strings.Join(was.Members, ","), strings.Join(is.Members, ","); a != b {
		out = append(out, defChange{"--members", a, b})
	}
	return append(out, configChanges(was.Config, is.Config)...)
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

// fmtSets prints a group's config as --set would take it back: a string as
// it is, anything else as JSON, and a string that --set would read as JSON
// (the command "true", the model "5") as the JSON string. So cmd=true is the
// boolean and cmd="true" the command, which fmtConfig prints alike.
func fmtSets(m map[string]any) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		v := m[k]
		if s, ok := v.(string); ok && !json.Valid([]byte(s)) {
			parts = append(parts, k+"="+s)
			continue
		}
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false) // a command's & and < as they were typed
		if err := enc.Encode(v); err != nil {
			parts = append(parts, k+"="+show(v))
			continue
		}
		parts = append(parts, k+"="+strings.TrimSuffix(b.String(), "\n"))
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
	var typ, depot, group, originator string
	var labels, further []string
	var beadPriority int
	add := &cobra.Command{
		Use:   "add --stage <stage> --group <group>[@<condition>] [--else <group>[@<condition>]]...",
		Short: "Send open, unblocked beads in a stage to a group",
		Long: `Send open, unblocked beads in a stage to a group.

A route may name further groups with --else, in the order they are tried,
and a group that starts agents may carry a condition after @ on the windows
of its provider's usage limits, as the yard's limits source labels them
(yardr limits): Weekly<80 holds while the window labelled Weekly is under
80% used, Weekly<80,5h<90 while both are, <80 while every window is. A
comparison may name a source for the window: machine.load<80 holds while
the window labelled load of what the source machine says of itself is under
80% used (yardr limits names such a source beside the providers), whatever
the group's provider is, and the two forms mix (machine.load<80,Weekly<90).
In place of a number the right side may be the window's target, the share of
it that has passed as the source reports it, with an optional margin:
Weekly<target holds while Weekly is under its target, Weekly<target+10
while under ten points above it, Weekly<target-5 while under five below,
and the two forms mix (Weekly<target+10,5h<90). A bead
goes to the first group whose condition holds; a group with none always
holds, and --else wait ends the route, so the bead waits. While no group's
does, the bead waits (a quota_wait event says for what). A condition that
cannot be evaluated (no limits source answers for the group's provider, its
command failed, it reports no such window, it gives the window no
percentage or, for a condition on the target, no target, and for a
comparison on a source no source of the name that answers for itself)
holds, and the claimed event says the limits were not known.

Two groups of a route that burn one provider read the same windows, so a
further group with no condition that burns the provider of a group before
it takes the beads that group was passed over for: route add warns of it
and adds the route. A route with --else or a condition otherwise names only
groups that start agents, or gates one exec group alone on the windows of
sources, as refinery@machine.load<80. yardr bead show says which route gave
a bead to its group and which groups it passed over, or what a waiting bead
waits for.

--type, --depot, --label and --bead-priority say which beads of the stage
the route is for; one it does not give matches any bead. --label may be
given more than once, and the bead must have every one. --bead-priority
<n> matches beads of priority n or more urgent: 1 is P0 and P1.
--originator <user> takes only that user's beads, and --originator any
every originator's; a route without it takes only the operator's beads, so
no general route swallows another person's work. A named originator must be
a user that exists. Of the routes that match a bead, the one with the
highest --priority takes it, and among those the one that asks for more:
each label counts, and a named originator counts as one.`,
		Example: `  yardr route add --stage new --group webapp/builders --depot webapp
  yardr route add --stage review --group webapp/reviewers
  yardr route add --stage new --type video --group herald --priority 10
  yardr route add --stage new --group 'builders@Weekly<80,5h<90' --else spare-builders
  yardr route add --stage new --group 'builders@Weekly<target+10' --else spare-builders
  yardr route add --stage new --group 'builders@machine.load<80' --else wait
  yardr route add --stage new --label difficulty:3 --label size:large --group builders
  yardr route add --stage new --bead-priority 1 --group builders --priority 5
  yardr route add --stage new --depot web --originator mckean2 --group mckean2-builders`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if r.Stage == "" || group == "" {
				return fmt.Errorf("--stage and --group are required")
			}
			first := store.ParseRouteGroup(group)
			r.Group, r.When, r.Else = first.Name, first.When, nil
			for _, e := range further {
				r.Else = append(r.Else, store.ParseRouteGroup(e))
			}
			fl := c.Flags()
			if fl.Changed("type") {
				r.Type = &typ
			}
			if fl.Changed("depot") {
				r.Depot = &depot
			}
			// AddRoute stores the first label as the route's label and the
			// rest beside it.
			r.Label, r.AndLabels, r.BeadPriority, r.Originator = nil, labels, nil, nil
			if fl.Changed("bead-priority") {
				r.BeadPriority = &beadPriority
			}
			if fl.Changed("originator") {
				r.Originator = &originator
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			saved, err := st.AddRoute(c.Context(), r, a.who())
			if err != nil {
				return err
			}
			// The route is the operator's to write: what cannot work as it
			// reads is said, and the route stands.
			for _, w := range routeWarnings(c.Context(), st, *saved) {
				fmt.Fprintf(stderr, "warning: route %s: %s\n", saved.ID, w)
			}
			return a.out(saved, func() { fmt.Println(saved.ID) })
		},
	}
	f := add.Flags()
	f.StringVarP(&r.Stage, "stage", "s", "", "stage to match")
	f.StringVarP(&group, "group", "g", "", "group to send to, with an optional quota condition: 'builders@Weekly<80'")
	f.StringArrayVar(&further, "else", nil, "a further group, tried when the ones before it are passed over; repeatable, in order")
	f.StringVarP(&typ, "type", "t", "", "only beads of this type")
	f.StringVarP(&depot, "depot", "d", "", "only beads in this depot")
	f.StringArrayVarP(&labels, "label", "l", nil, "only beads with this label; repeatable, and the bead must have every one")
	f.IntVar(&beadPriority, "bead-priority", 0, "only beads of this priority or more urgent: 1 is P0 and P1")
	f.StringVar(&originator, "originator", "", "only beads for this user, or any for every originator's; unset: only the operator's")
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
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			operator := settings[store.OperatorSetting]
			return a.out(rs, func() {
				rows := [][]string{}
				for _, r := range rs {
					orig := operator
					if r.Originator != nil {
						orig = *r.Originator
					}
					rows = append(rows, []string{r.ID, r.Stage, deref(r.Type), deref(r.Depot), or(r.LabelList(), "-"),
						or(r.BeadPriorities(), "-"), or(orig, "-"), strconv.Itoa(r.Priority), r.GroupList()})
				}
				table("ID\tSTAGE\tTYPE\tDEPOT\tLABEL\tBEADS\tORIGINATOR\tPRI\tGROUP", rows)
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
			if err := st.DeleteRoute(c.Context(), args[0], a.who()); err != nil {
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
				if r.Names(s.Group) && s.Stage == r.Stage {
					claimed = append(claimed, s.Bead)
				}
			}
			return a.out(map[string]any{"route": r.ID, "claimed": claimed}, func() {
				if len(claimed) > 0 {
					fmt.Printf("%d bead(s) in %s already claimed by %s keep it until their sessions end: %s\n",
						len(claimed), r.Stage, strings.Join(append([]string{r.Worker()}, r.Further()...), " or "), strings.Join(claimed, " "))
				}
			})
		},
	}
	cmd.AddCommand(add, list, rm, a.routeReportCmd())
	return cmd
}

func (a *app) sessionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Runner sessions working beads"}
	var running bool
	var bead string
	var cut listCut
	list := &cobra.Command{
		Use: "list", Short: "List live sessions", Args: cobra.NoArgs,
		Long: `List live sessions: one for each bead a group has claimed. ORIGINATOR
is the user the bead is for, beside the group. The group uses the login in
its configuration; a subsidy route can use the operator's login.

A manual group's claim is among them, though nothing runs in it: the bead
waits for a person. --running leaves those out, so the list is what an
agent (herdr) or a script (exec) is working on now.

Under the table is a line for each session whose agent stopped on a usage
limit and waits for the window to reset: until when the daemon waits before
it prompts the agent, and the model its group starts no further session
with meanwhile (docs/groups.md). --json has it on the session as reset_wait:
until, window, model. It is read from the session, and asks no provider.

-a lists the ended sessions too. A yard keeps those until they are pruned,
so that listing is the newest ` + strconv.Itoa(listLimit) + ` by start: -n <N> shows more or
fewer, and --all every session there is, which is what a script wants.
--bead keeps one bead's sessions and --since those started in that time
(7d, 12h); the limit takes the newest of what they keep. Without -a or
--all the sessions are the live ones, however many.`,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// Running leaves out a manual group's claim: a bead waiting for
			// a person, with nothing running behind it (yardr-7r3h).
			f := store.SessionFilter{Bead: bead, Running: running}
			if f.All, f.Limit, f.Since, err = cut.read(c); err != nil {
				return err
			}
			ss, err := st.ListSessions(c.Context(), f)
			if err != nil {
				return err
			}
			cut.say(len(ss), "sessions")
			listed := dispatch.Listed(ss)
			now := time.Now()
			return a.out(listed, func() {
				rows := [][]string{}
				for _, s := range listed {
					// Which runner owns it, and what it is running there.
					runner := s.RunnerName()
					if v, ok := s.Handle["agent"]; ok {
						runner += " " + show(v)
					} else if v, ok := s.Handle["pid"]; ok {
						runner += " pid " + show(v)
					}
					rows = append(rows, []string{s.ID, s.Bead, s.Train, s.Stage, s.Group, s.Originator, s.State, or(runner, "-"),
						s.StartedAt.Local().Format("01-02 15:04")})
				}
				table("ID\tBEAD\tTRAIN\tSTAGE\tGROUP\tORIGINATOR\tSTATE\tRUNNER\tSTARTED", rows)
				for _, s := range listed {
					if s.ResetWait != nil {
						fmt.Println(resetWaitLine(*s.ResetWait, s.Session, now))
					}
				}
			})
		},
	}
	cut.flags(list, "ended", "ended sessions")
	list.Flags().StringVar(&bead, "bead", "", "only this bead's sessions")
	list.Flags().BoolVar(&running, "running", false, "only sessions an agent or a script is behind: leave out the beads a manual group holds for a person")
	cmd.AddCommand(list)
	return cmd
}

// resetWaitLine says the wait w of s in a line, for a person.
func resetWaitLine(w dispatch.ResetWaiting, s store.Session, now time.Time) string {
	model := "the agent's own model"
	if w.Model != "" {
		model = "model " + w.Model
	}
	when := "until " + limits.Clock(w.Until, now)
	if left := w.Until.Sub(now); left >= 0 {
		when += " (in " + limits.Span(left) + ")"
	} else {
		// The daemon prompts the agent on its next pass, or is not running.
		when += " (" + limits.Span(-left) + " ago)"
	}
	window := "the window"
	if w.Window != "" {
		window = "the " + w.Window + " window"
	}
	return fmt.Sprintf("%s starts no further session with %s %s: session %s (bead %s) waits for %s of its usage limit to reset",
		s.Group, model, when, s.ID, s.Bead, window)
}

func printEvent(e store.Event) {
	data := ""
	if len(e.Data) > 0 {
		data = fmtConfig(store.Brief(e.Data))
	}
	fmt.Printf("%6d %s  %-8s %-14s %-12s %s\n", e.Seq, e.At.Local().Format("01-02 15:04:05"), deref(e.Bead), e.Kind, e.Actor, data)
}

func (a *app) eventsCmd() *cobra.Command {
	var bead, since string
	var kinds []string
	var limit int
	cmd := &cobra.Command{
		Use: "events", Short: "Show the event log", Args: cobra.NoArgs,
		Long: `Show the event log, oldest first.

--bead keeps one bead's events, --kind one kind's (repeatable: --kind
claimed --kind advanced), --since those at or after a time: a duration
back from now (7d, 40h, 30m) or an RFC3339 time. Without -n the newest
50 are shown, or with --since every one in the window.`,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			var cutoff time.Time
			if since != "" {
				if cutoff, err = parseSince(since); err != nil {
					return fmt.Errorf("--since: %w", err)
				}
			}
			wantKind := map[string]bool{}
			for _, k := range kinds {
				if k = strings.TrimSpace(k); k != "" {
					wantKind[k] = true
				}
			}
			// --since without -n returns everything in the window;
			// otherwise the newest limit, 50 unless told.
			n := limit
			if since != "" && !c.Flags().Changed("limit") {
				n = 0
			} else if n <= 0 {
				n = 50
			}
			if since == "" && len(wantKind) == 0 {
				evs, err := st.Events(c.Context(), bead, n)
				if err != nil {
					return err
				}
				return a.out(evs, func() {
					for _, e := range evs {
						printEvent(e)
					}
				})
			}
			// Filtered: read the retained log oldest first and keep
			// what matches, so -n is the newest of those, not of all.
			if bead != "" {
				// Unknown beads fail as the unfiltered listing
				// does, and a UUID reads as its handle below.
				b, err := st.GetBead(c.Context(), bead)
				if err != nil {
					return err
				}
				bead = b.ID
			}
			var all []store.Event
			var after uint64
			for {
				page, err := st.EventsSince(c.Context(), after, 1000)
				if err != nil {
					return err
				}
				if len(page) == 0 {
					break
				}
				all = append(all, page...)
				after = page[len(page)-1].Seq
				if len(page) < 1000 {
					break
				}
			}
			var evs []store.Event
			for _, e := range all {
				if bead != "" && (e.Bead == nil || *e.Bead != bead) {
					continue
				}
				if len(wantKind) > 0 && !wantKind[e.Kind] {
					continue
				}
				if since != "" && e.At.Before(cutoff) {
					continue
				}
				evs = append(evs, e)
			}
			if n > 0 && len(evs) > n {
				evs = evs[len(evs)-n:]
			}
			return a.out(evs, func() {
				for _, e := range evs {
					printEvent(e)
				}
			})
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "only this bead")
	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "only this event kind; repeatable")
	cmd.Flags().StringVar(&since, "since", "", "only events at or after this time: 7d, 40h, 30m or RFC3339")
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
continues from the last event it printed. With --json, one event per line.`,
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
			// --json is a stream, not a document: one event per line, so a
			// consumer reads a line and parses it. The shared printer is for
			// one document and pretty-prints; this encoder stays compact, and
			// each Encode writes its line as the event is committed.
			var enc *json.Encoder
			if a.json {
				enc = json.NewEncoder(os.Stdout)
			}
			// When the yardr server goes away, Follow waits for it and resumes
			// after the last event printed.
			store.Follow(ctx, st, last, streamRetry, func(e store.Event) {
				if enc != nil {
					_ = enc.Encode(e)
				} else {
					printEvent(e)
				}
			}, nil)
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

// detectDepotKind is the kind of depot a path makes when depot add is given no
// --kind: git when git sees a repository there, else a plain directory.
func detectDepotKind(path string) string {
	if exec.Command("git", "-C", path, "rev-parse", "--git-dir").Run() == nil {
		return store.DepotGit
	}
	return store.DepotDir
}
