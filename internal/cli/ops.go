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

	"io"
	"yardr.dev/yardr/internal/agents"
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
			cfg, err := a.config()
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
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if _, err := connect(c.Context(), cfg); err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]string{"store": cfg.DBPath(), "socket": cfg.Socket()}, func(w io.Writer) {
				fmt.Fprintf(w, "yardr server running (store %s)\n", cfg.DBPath())
			})
		},
	}, &cobra.Command{
		Use:   "stop",
		Short: "Stop the background server",
		Long: `Stop the background server.

A running daemon starts the server again within about a minute: stop the
daemon first to keep it stopped. yardr down stops the daemon first and is
unaffected.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			return server.Stop(cfg)
		},
	}, &cobra.Command{
		Use: "status", Short: "Report whether the server answers", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.configReadOnly()
			if err != nil {
				return err
			}
			ok := server.Running(c.Context(), cfg)
			return a.out(c.OutOrStdout(), map[string]any{"store": cfg.DBPath(), "running": ok}, func(w io.Writer) {
				fmt.Fprintf(w, "%s running=%v\n", cfg.DBPath(), ok)
			})
		},
	})
	return cmd
}

// daemonRunners are the runners the daemon supervises sessions with; tests
// stand one in for herdr's runner.
var daemonRunners = func(home string, st store.API, log *slog.Logger, onError func(error)) map[string]dispatch.Runner {
	return runner.All(home, st, log, onError)
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
			cfg, err := a.config()
			if err != nil {
				return err
			}
			// One daemon per yard: a second would route the same beads and
			// supervise the same sessions. The lock is taken before the
			// store is touched and held until this process ends. A
			// self-replace keeps the pid but execs a new image: the lock's
			// descriptor is close-on-exec, so the exec releases it and the
			// new build takes it here again, after the old image is gone.
			unlock, err := lockDaemon(cfg.DaemonLock(), daemonLockWait)
			if err != nil {
				return err
			}
			defer unlock()
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			ctx, release, err := whileHomeStands(ctx, cfg, watchEvery(log), log)
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
			runners := daemonRunners(a.cfg.Home, st, log, onError)
			d := &dispatch.Dispatcher{
				Store:       st,
				Runners:     runners,
				Log:         log,
				Home:        a.cfg.Home,
				Interval:    interval,
				MaxAttempts: attempts,
				Binary:      bin,
				Retention:   keep,
				PeerKey:     a.cfg.PeerKey(),
				// The server it connected to may be gone mid-run: as any
				// command does, the daemon starts one.
				Server: func(ctx context.Context) error {
					if server.Running(ctx, cfg) {
						return nil
					}
					log.Warn("the yardr server is gone; starting it")
					if _, err := server.Connect(ctx, cfg); err != nil {
						return err
					}
					log.Info("the yardr server answers again")
					return nil
				},
			}
			log.Info("yardr daemon started", "store", a.cfg.DBPath(), "reconcile", interval)
			runErr := d.Run(ctx)
			if homeLostCause(context.Cause(ctx)) != "" {
				// The home is gone under the daemon: its herdr sessions
				// are closed first, since nothing else can close them
				// now. On a SIGTERM they are left, for down or the next
				// daemon. ctx is ended, so the closes get their own,
				// bounded: a herdr that hangs must not keep the daemon
				// from exiting.
				closeCtx, cancel := context.WithTimeout(context.Background(), stopWait)
				defer cancel()
				closeHomeSessions(closeCtx, d.HomeSessions(), runners, log)
			}
			return runErr
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
other directory a dir depot; depot add says which it took. Only the operator
changes an existing depot's path, base or test command.`,
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
				fmt.Fprintf(c.ErrOrStderr(), "cloning %s into %s\n", clone, path)
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
				return a.out(c.OutOrStdout(), r, func(w io.Writer) {
					fmt.Fprintf(w, "depot %s → %s (dir: no git, no base)\n", r.Name, r.Path)
					if r.Test != "" {
						fmt.Fprintf(w, "test: %s\n", r.Test)
					}
				})
			}
			if out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output(); err != nil {
				return fmt.Errorf("%s is not a git checkout; --kind dir registers it as a plain directory", path)
			} else if top, real := strings.TrimSpace(string(out)), resolve(path); top != real {
				return fmt.Errorf("%s is inside the repository at %s; register the top level (or --kind dir)", path, top)
			}
			// Branches for beads start from base, so it has to resolve.
			if _, err := store.ResolveRef(context.Background(), path, base+"^{commit}"); err != nil {
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
			return a.out(c.OutOrStdout(), r, func(w io.Writer) {
				fmt.Fprintf(w, "depot %s → %s (git, base %s)\n", r.Name, r.Path, r.Base)
				if r.Test != "" {
					fmt.Fprintf(w, "test: %s\n", r.Test)
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
			return a.out(c.OutOrStdout(), r, func(w io.Writer) {
				fmt.Fprintf(w, "depot %s\n", r.Name)
				fmt.Fprintf(w, "  path:  %s\n", r.Path)
				fmt.Fprintf(w, "  kind:  %s\n", r.Kind)
				if r.IsDir() {
					fmt.Fprintf(w, "  base:  none (a dir depot)\n")
				} else {
					fmt.Fprintf(w, "  base:  %s\n", r.Base)
				}
				fmt.Fprintf(w, "  flow:  %s\n", depotFlow(c.Context(), st, r.Name))
				if r.Test != "" {
					fmt.Fprintf(w, "  test:  %s\n", r.Test)
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
			return a.out(c.OutOrStdout(), rs, func(w io.Writer) {
				rows := [][]string{}
				for _, r := range rs {
					rows = append(rows, []string{r.Name, r.Kind, r.Path, or(r.Base, "-"), depotFlow(c.Context(), st, r.Name)})
				}
				table(w, "NAME\tKIND\tPATH\tBASE\tFLOW", rows)
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
				fmt.Fprintf(c.OutOrStdout(), "%s points at no flow now; it follows %s\n", who, depotFlow(c.Context(), st, args[0]))
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "%s → flow %s\n", who, name)
			return nil
		},
	}
	flow.Flags().StringVarP(&flowType, "type", "t", "", "only the depot's beads of this type")
	flow.Flags().BoolVar(&clear, "clear", false, "remove the pointer")
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a depot from the store with its routes and flow pointers (refused while it has open beads or live sessions, or while upgrade_from names it)",
		Example: `  yardr depot rm yard          # registered under the wrong name; nothing uses it
  yardr depot rm legacy --json # what went with it: its routes and flow pointers

Only the store changes. The clone, its worktrees and its .yardr/ stay where
they are; remove them yourself if you want them gone. Closed beads keep the
depot's name as history. A depot with open beads or live sessions is refused and
the beads and sessions named: close or move the beads first. One upgrade_from
names is refused too: clear the setting first (yardr settings set upgrade_from "").`,
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
			return a.out(c.OutOrStdout(), r, func(w io.Writer) {
				ptrs := make([]string, 0, len(r.FlowPointers))
				for _, p := range r.FlowPointers {
					ptrs = append(ptrs, p.String())
				}
				fmt.Fprintf(w, "removed depot %s; %d route(s) and %d flow pointer(s) went with it\n", r.Depot, len(r.Routes), len(ptrs))
				if len(r.Routes) > 0 {
					fmt.Fprintf(w, "  routes: %s\n", strings.Join(r.Routes, " "))
				}
				if len(ptrs) > 0 {
					fmt.Fprintf(w, "  flow pointers: %s\n", strings.Join(ptrs, ", "))
				}
				fmt.Fprintln(w, "nothing on disk was touched")
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
get default_kind), and a group without --limit gets 1, a manual group 50:
people read what waits for them as a list, so one bead parked with them does
not hold back the others. To change some settings of a group and keep the
rest: yardr group set.

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
				if err := a.groupForm(c.OutOrStdout(), c.Context(), st); errors.Is(err, huh.ErrUserAborted) {
					fmt.Fprintln(c.OutOrStdout(), "cancelled")
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
			if g.Config, err = store.ParseSets(sets); err != nil {
				return err
			}
			g.Name = args[0]
			if c.Flags().Changed("limit") && g.Limit == 0 {
				// 0 pauses, and is set with group set: group add writes
				// the group whole, with limit 1 or more.
				_, err := st.GetGroup(c.Context(), g.Name)
				switch {
				case errors.Is(err, store.ErrNotFound):
					return fmt.Errorf("group %s is new: it starts with limit 1 or more; pause it after with yardr group set %s --limit 0", g.Name, g.Name)
				case err == nil:
					return fmt.Errorf("group %s exists: 0 pauses, and is set with yardr group set %s --limit 0", g.Name, g.Name)
				}
			}
			if !c.Flags().Changed("limit") {
				// group add replaces the whole group. Resolve an omitted
				// limit here so it resets an existing pause to this runner's
				// default, as it did before zero meant "not named".
				g.Limit = store.DefaultLimit(g.Runner)
			}
			withDefaultKind(c.Context(), st, g.Runner, g.Config)
			if err := st.PutGroup(c.Context(), g, a.who()); err != nil {
				return err
			}
			saved, err := st.GetGroup(c.Context(), g.Name)
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), saved, func(w io.Writer) { fmt.Fprintf(w, "group %s (%s)\n", saved.Name, describe(*saved)) })
		},
	}
	add.Flags().StringVar(&g.Runner, "runner", "", "runner: manual, exec, herdr, peer")
	add.Flags().IntVar(&g.Limit, "limit", 0, fmt.Sprintf("max concurrent sessions (default %d, and %d for a manual group, whose people read what waits as a list; 0 pauses, and is set with yardr group set); for a peer group, how many beads may be at that yard at once", store.DefaultLimit("exec"), store.DefaultLimit("manual")))
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
			return a.out(c.OutOrStdout(), gs, func(w io.Writer) {
				rows := [][]string{}
				for _, g := range gs {
					// What the group's sessions may call of the declared
					// sources: "crm: note (update), read; wiki: page".
					var calls []string
					for _, gr := range grantedTo(grants, store.Grantee{Group: g.Name}) {
						calls = append(calls, gr.Source+asSuffix(gr)+": "+words(gr))
					}
					limit := strconv.Itoa(g.Limit)
					if g.Limit == 0 {
						limit = "0 (paused)"
					}
					rows = append(rows, []string{g.Name, g.Runner, limit, or(strings.Join(g.Members, ","), "-"), or(strings.Join(calls, "; "), "-"), fmtSets(g.Config)})
				}
				table(w, "NAME\tRUNNER\tLIMIT\tMEMBERS\tCALLS\tCONFIG", rows)
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
			// The store counts deleting a group that is not there as done (pack
			// apply prunes by name and may run again), so a mistyped name is
			// refused here, as flow rm and crew rm refuse theirs.
			if _, err := st.GetGroup(c.Context(), args[0]); err != nil {
				return err
			}
			// A templated default that resolves to this group stays, and stops
			// serving the depots it served through it: those are named beside
			// what the removal releases.
			routes, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			depots, err := st.Depots(c.Context())
			if err != nil {
				return err
			}
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			operator := settings[store.OperatorSetting]
			listed, err := st.Groups(c.Context())
			if err != nil {
				return err
			}
			byName := make(map[string]store.Group, len(listed))
			for _, g := range listed {
				byName[g.Name] = g
			}
			byID := make(map[string]store.Route, len(routes))
			for _, r := range routes {
				byID[r.ID] = r
			}
			type stopped struct {
				Route store.Route `json:"route"`
				Depot string      `json:"depot"`
			}
			noLonger := []stopped{}
			for _, d := range depots {
				depot := d.Name
				for _, r := range store.EffectiveRoutes(routes, &depot, operator, byName) {
					orig, ok := byID[r.ID]
					if !ok || !orig.Templated() {
						continue
					}
					if r.Names(args[0]) {
						noLonger = append(noLonger, stopped{Route: orig, Depot: depot})
					}
				}
			}
			n, err := st.DeleteGroup(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"group": args[0], "released": n, "unserved": noLonger}, func(w io.Writer) {
				if n > 0 {
					fmt.Fprintf(w, "deleted group %s; ended its %d live session(s) and released their beads for routing\n", args[0], n)
				}
				for _, s := range noLonger {
					fmt.Fprintf(w, "default %s (%s -> %s) no longer serves %s: no group %s\n",
						s.Route.ID, s.Route.Stage, s.Route.GroupList(), s.Depot, args[0])
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
			if g.LoginFor() != "" {
				if account := groupFolderAccount(*g); account != "" {
					out["folder_login"] = account
				}
			}
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
			return a.out(c.OutOrStdout(), out, func(w io.Writer) {
				fmt.Fprintf(w, "%s  %s\n", g.Name, describe(*g))
				if h, ok := agents.Lookup(kind); ok && len(h.Flags.Always) > 0 {
					fmt.Fprintf(w, "  harness %s always: %s\n", kind, strings.Join(h.Flags.Always, " "))
				}
				// One key to a line, as group set takes it back; values
				// named like secrets stay out of terminal scrollback.
				for _, k := range slices.Sorted(maps.Keys(g.Config)) {
					v := g.Config[k]
					if secretSetting(k) {
						v = hidden(true)
					}
					line := fmtSets(map[string]any{k: v})
					// login_for is a declaration and the folder is what
					// burns: beside it, the account the folder's login
					// names where the harness writes one, so a mismatch
					// shows to the eye. Nothing enforces it.
					if k == "login_for" {
						if account := groupFolderAccount(*g); account != "" {
							line += " (folder login: " + account + ")"
						}
					}
					fmt.Fprintf(w, "  %s\n", line)
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
					fmt.Fprintf(w, "  route %s: %s\n", r.ID, line)
				}
				for _, gr := range mayCall {
					fmt.Fprintf(w, "  calls %s%s: %s\n", gr.Source, asSuffix(gr), words(gr))
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
  yardr group set webapp/builders --limit 0   # pause: nothing new starts; set back to resume
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
			if e.Set, err = store.ParseSets(args[1:]); err != nil {
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
			return a.out(c.OutOrStdout(), ch.Is, func(w io.Writer) {
				if len(changes) == 0 {
					fmt.Fprintf(w, "group %s unchanged: it is set so already\n", name)
					return
				}
				fmt.Fprintf(w, "group %s saved\n%s\n", name, changeLines(changes))
			})
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&unset, "unset", nil, "remove this runner config key (repeatable)")
	f.IntVar(&limit, "limit", 0, "max concurrent sessions (0 pauses the group); for a peer group, how many beads may be at that yard at once")
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
and the two forms mix (Weekly<target+10,5h<90). Any comparison may end in
/<duration>, days (1d) or hours and minutes (20h, 30m): Weekly<80/1d holds
while Weekly is under 80% used or resets within a day, so quota that is
about to renew does not keep a bead waiting. It goes on the target form
(Weekly<target+10/1d) and on a source's window too, though a window that
never resets, as the machine source's, holds nothing by it. A reset
already past counts; a window whose report gives no reset is held to the
number alone. The claimed event says which windows held (held). A bead
goes to the first group whose condition holds; a group that starts agents
with none holds while no window of its provider is used up (as <100 would;
a window the source says counts for one model alone counts only where it is
the model the bead would start with in that group: yardr limits marks it),
and --else wait ends the route, so the bead waits. While no
group's does, the bead waits (a quota_wait event says for what). A
condition that cannot be evaluated (no limits source answers for the
group's provider, its command failed, it reports no such window, it gives
the window no percentage or, for a condition on the target, no target, and
for a comparison on a source no source of the name that answers for
itself) holds, and the claimed event says the limits were not known.

Two groups of a route that burn one provider on one login read the same
windows, so a further group with no condition that burns the provider of a
group before it takes the beads that group was passed over for, until those
windows are used up: route add warns of it and adds the route. Two groups
on different logins read each
their own, and a personal group failing over to the operator's warns of
nothing. A route with --else or a condition otherwise names only
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
no general route swallows another person's work, unless its first group
starts no agents (a script, people, another yard): none burns a
subscription, so that route is any. A named originator must be a user that
exists. Without --depot a route is a yard default: it serves
every depot that does not state its own route for the same beads (the
same stage, type, labels, bead priority and originator); with --depot it
is that depot's own, and sets a default aside only for those beads and
only there. A default may name its group with the depot template, first
group or further group: '{depot}-assembly' reads as 'web-assembly' for
depot web, and a depot without the resolved group keeps no route from it.
An own route names no template. Of the routes that match a bead, in its
depot's effective routes, the first in this order takes it, the first of
these that differs deciding: urgency (a route with --bead-priority before
one without, and the narrower threshold first: 0 before 1); asked for by
name (more labels that are not estimates, as claude or airy); estimate
(more labels that begin difficulty: or size:); the rest (the type, the depot
and a named originator count one each); an own route before a yard default;
the route id. A route that takes the same beads as one of the same scope
already standing is refused, naming it: the two would be ordered by id,
which nobody chose. A --note says in words what the filters and the chain
cannot: why this route is there. route list prints it.`,
		Example: `  yardr route add --stage new --group webapp/builders --depot webapp
  yardr route add --stage review --group webapp/reviewers
  yardr route add --stage new --type video --group herald
  yardr route add --stage new --group 'builders@Weekly<80,5h<90' --else spare-builders
  yardr route add --stage new --group 'builders@Weekly<target+10' --else spare-builders
  yardr route add --stage new --group 'builders@Weekly<80/1d' --else spare-builders
  yardr route add --stage new --group 'builders@machine.load<80' --else wait
  yardr route add --stage new --label difficulty:3 --label size:large --group builders
  yardr route add --stage new --bead-priority 0 --group builders --note 'P0 reserve'
  yardr route add --stage new --depot web --originator mckean2 --group mckean2-builders
  yardr route add --stage approved --group '{depot}-assembly'`,
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
				fmt.Fprintf(c.ErrOrStderr(), "warning: route %s: %s\n", saved.ID, w)
			}
			return a.out(c.OutOrStdout(), saved, func(w io.Writer) { fmt.Fprintln(w, saved.ID) })
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
	f.StringVar(&originator, "originator", "", "only beads for this user, or any for every originator's; unset: the operator's, or any when the first group starts no agents")
	f.StringVar(&r.Note, "note", "", "why this route is there, in words; route list prints it")

	var listDepot string
	list := &cobra.Command{
		Use: "list", Short: "List routes in match order", Args: cobra.NoArgs,
		Long: `List the yard's routes in match order: the scope (the depot's own, or a
yard default), what each takes, its groups and its note.
--depot <depot> lists that depot's effective routes instead: its own routes
plus the defaults none of them sets aside, each marked own or default, with
the defaults it sets aside and the defaults that do not serve it for a
missing group named underneath.`,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			operator := settings[store.OperatorSetting]
			rs, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			dispatch.SortRoutes(rs)
			if !c.Flags().Changed("depot") {
				return a.out(c.OutOrStdout(), rs, func(w io.Writer) {
					rows := make([][]string, 0, len(rs))
					for _, r := range rs {
						scope := "default"
						if r.Depot != nil {
							scope = *r.Depot
						}
						rows = append(rows, routeRow(r, scope, operator))
					}
					table(w, "ID\tSCOPE\tSTAGE\tTYPE\tLABEL\tBEADS\tORIGINATOR\tGROUP\tNOTE", rows)
				})
			}
			// As depot show fails for a depot that is not there.
			if _, err := st.GetDepot(c.Context(), listDepot); err != nil {
				return err
			}
			listed, err := st.Groups(c.Context())
			if err != nil {
				return err
			}
			byName := make(map[string]store.Group, len(listed))
			for _, g := range listed {
				byName[g.Name] = g
			}
			eff := store.EffectiveRoutes(rs, &listDepot, operator, byName)
			dispatch.SortRoutes(eff)
			aside := store.SetAsideRoutes(rs, listDepot, operator)
			if aside == nil {
				aside = []store.SetAside{}
			}
			unserved := store.UnservedRoutes(rs, listDepot, operator, byName)
			if unserved == nil {
				unserved = []store.UnservedRoute{}
			}
			marked := make([]markedRoute, 0, len(eff))
			for _, r := range eff {
				mark := "default"
				if r.Depot != nil {
					mark = "own"
				}
				marked = append(marked, markedRoute{Route: r, Effective: mark})
			}
			return a.out(c.OutOrStdout(), map[string]any{"routes": marked, "set_aside": aside, "unserved": unserved}, func(w io.Writer) {
				rows := make([][]string, 0, len(marked))
				for _, m := range marked {
					rows = append(rows, routeRow(m.Route, m.Effective, operator))
				}
				table(w, "ID\tOWN/DEFAULT\tSTAGE\tTYPE\tLABEL\tBEADS\tORIGINATOR\tGROUP\tNOTE", rows)
				saySetAside(w, listDepot, aside, operator)
				sayUnserved(w, listDepot, unserved)
			})
		},
	}
	list.Flags().StringVarP(&listDepot, "depot", "d", "", "list this depot's effective routes: its own plus the defaults none sets aside")
	rm := &cobra.Command{
		Use: "rm <id>", Short: "Delete a route", Args: cobra.ExactArgs(1),
		Long: `Deletes a route. <id> is the whole id, or its first characters when there
are at least ` + fmt.Sprint(store.MinPrefix) + ` of them and only one route's id begins with them. An id
no route has is refused, and a start that fits several routes names them, so
a mistyped id cannot pass for a removal.

Beads already claimed through the route keep its group until their
sessions end; only new claims follow the remaining routes.`,
		Example: `  yardr route rm 57h28dx9dx9cmydnrzj9
  yardr route rm 57h2`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			r, err := routeByRef(rs, args[0])
			if err != nil {
				return err
			}
			if err := st.DeleteRoute(c.Context(), r.ID, a.who()); err != nil {
				return err
			}
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
			return a.out(c.OutOrStdout(), map[string]any{"route": r.ID, "claimed": claimed}, func(w io.Writer) {
				if len(claimed) > 0 {
					fmt.Fprintf(w, "%d bead(s) in %s already claimed by %s keep it until their sessions end: %s\n",
						len(claimed), r.Stage, strings.Join(append([]string{r.Worker()}, r.Further()...), " or "), strings.Join(claimed, " "))
				}
			})
		},
	}
	cmd.AddCommand(add, list, rm, a.routeSetCmd(), a.routeExplainCmd(), a.routeReportCmd())
	return cmd
}

// routeByRef is the route ref names among routes: its whole id, or the
// first characters of one id alone when there are at least store.MinPrefix
// of them, as a bead handle may be cut short. route rm goes through it
// because the store counts deleting a route that is not there as done (pack
// apply prunes by id and may run again), so an id the operator mistyped or
// cut short would otherwise print nothing and exit 0.
func routeByRef(routes []store.Route, ref string) (store.Route, error) {
	var starts []store.Route
	for _, r := range routes {
		if r.ID == ref {
			return r, nil
		}
		if strings.HasPrefix(r.ID, ref) {
			starts = append(starts, r)
		}
	}
	switch {
	case len(starts) == 0:
		return store.Route{}, fmt.Errorf("no route %s: %w", ref, store.ErrNotFound)
	case len(ref) < store.MinPrefix:
		return store.Route{}, fmt.Errorf("route %s: give at least %d characters of its id, or all of it", ref, store.MinPrefix)
	case len(starts) > 1:
		ids := make([]string, len(starts))
		for i, r := range starts {
			ids[i] = r.ID
		}
		return store.Route{}, fmt.Errorf("route %s matches %d routes: %s; give more of its id", ref, len(ids), strings.Join(ids, ", "))
	}
	return starts[0], nil
}

// routeRow is one route of route list: the mark first (the scope, or own
// or default in a depot's effective list), then what the route takes, its
// groups and its note.
func routeRow(r store.Route, mark, operator string) []string {
	orig := operator
	if r.Originator != nil {
		orig = *r.Originator
	}
	return []string{r.ID, mark, r.Stage, deref(r.Type), or(r.LabelList(), "-"),
		or(r.BeadPriorities(), "-"), or(orig, "-"), r.GroupList(), store.OneLine(r.Note)}
}

// markedRoute is a route from route list --depot's effective set, plus
// whether it is the depot's own or a yard default. A templated default's
// groups are resolved for the depot.
type markedRoute struct {
	store.Route
	Effective string `json:"effective"`
}

// setAsideKey is a set-aside default's match key in words, as route list
// --depot names it: the stage, then in brackets the type where the route
// sets one, the labels or no label, the bead priorities where it sets any,
// and whose beads.
func setAsideKey(r store.Route, operator string) string {
	var parts []string
	if r.Type != nil {
		parts = append(parts, "type "+*r.Type)
	}
	if ls := r.Labels(); len(ls) == 1 {
		parts = append(parts, "label "+ls[0])
	} else if len(ls) > 1 {
		parts = append(parts, "labels "+strings.Join(ls, ", "))
	} else {
		parts = append(parts, "no label")
	}
	if r.BeadPriority != nil {
		parts = append(parts, "beads "+r.BeadPriorities())
	}
	who := operator
	if r.Originator != nil {
		who = *r.Originator
	}
	orig := "the operator's beads"
	switch {
	case who == store.OriginatorAny:
		orig = "every originator's beads"
	case who == operator:
	default:
		orig = who + "'s beads"
	}
	parts = append(parts, orig)
	return r.Stage + " (" + strings.Join(parts, ", ") + ")"
}

// setAsideLine is a default a depot's own routes set aside with the own
// route that states its key, as route list --depot and route explain
// name it.
func setAsideLine(s store.SetAside, operator string) string {
	return fmt.Sprintf("%s is stated by %s to %s",
		setAsideKey(s.Route, operator), s.Own.ID, s.Own.GroupList())
}

// saySetAside names the defaults the depot's own routes set aside,
// underneath route list --depot: nothing where none is set aside.
func saySetAside(w io.Writer, depot string, aside []store.SetAside, operator string) {
	switch len(aside) {
	case 0:
	case 1:
		fmt.Fprintf(w, "1 default set aside in %s: %s\n", depot, setAsideLine(aside[0], operator))
	default:
		fmt.Fprintf(w, "%d defaults set aside in %s:\n", len(aside), depot)
		for _, s := range aside {
			fmt.Fprintln(w, setAsideLine(s, operator))
		}
	}
}

// unservedLine is a default that does not serve depot for a missing group,
// as route list --depot and route explain name it: the default with its
// template, and the resolved groups that are not there.
func unservedLine(u store.UnservedRoute, depot string) string {
	reason := "no group " + u.Missing[0]
	if len(u.Missing) > 1 {
		reason = "no groups " + strings.Join(u.Missing, ", ")
	}
	return fmt.Sprintf("default %s (%s -> %s) does not serve %s: %s",
		u.Route.ID, u.Route.Stage, u.Route.GroupList(), depot, reason)
}

// sayUnserved names the defaults that do not serve depot for a missing
// group, underneath route list --depot: nothing where all serve.
func sayUnserved(w io.Writer, depot string, unserved []store.UnservedRoute) {
	for _, u := range unserved {
		fmt.Fprintln(w, unservedLine(u, depot))
	}
}

// routeSetCmd is yardr route set: where a route's beads go changed, which
// beads they are kept, as group set does for a group. The route keeps its
// id, so events and bead show lines that cite it still name this route.
func (a *app) routeSetCmd() *cobra.Command {
	var group, note string
	var further []string
	cmd := &cobra.Command{
		Use:   "set <id>",
		Short: "Change where a route's beads go and keep the rest",
		Long: `Changes where a route's beads go and keeps which beads they are:
--group with its quota condition, the further groups with --else, and
--note. What is not named is kept, and the route keeps its
id, so events and bead show lines that cite it still name this route.
--else replaces the further groups, so name every one wanted; --else ''
drops them, and --note '' drops the note. A set that changes nothing
writes nothing. A set that would tie a route of the same scope already
standing is refused, as an add is.

The stage, type, depot, labels, bead priority and originator are the
route's identity, and are not changed here: restate them with yardr route
rm <id> and yardr route add. A bead claimed through the route keeps its
group until its session ends; the next claim takes the new chain.`,
		Example: `  yardr route set 57h28dx9dx9cmydnrzj9 --group 'yardr-builders@Weekly<target' --else muse-builders
  yardr route set 57h28dx9dx9cmydnrzj9 --note "P0 reserve: strong group whatever the week says"`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			fl := c.Flags()
			if !fl.Changed("group") && !fl.Changed("else") && !fl.Changed("note") {
				return fmt.Errorf("nothing to set: give --group, --else or --note")
			}
			var e store.RouteEdit
			if fl.Changed("group") {
				if strings.TrimSpace(group) == "" {
					return fmt.Errorf("give a group: --group <group>[@<condition>]")
				}
				g := store.ParseRouteGroup(group)
				e.Group = &g
			}
			if fl.Changed("else") {
				// Empty and non-nil: a pointer to a nil slice marshals
				// as null, which the server reads as not named.
				tail := []store.RouteGroup{}
				for _, s := range further {
					// --else '' drops the further groups; anything else
					// replaces them, so every one wanted is named.
					if strings.TrimSpace(s) == "" {
						continue
					}
					tail = append(tail, store.ParseRouteGroup(s))
				}
				e.Else = &tail
			}
			if fl.Changed("note") {
				e.Note = &note
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ch, err := st.SetRoute(c.Context(), args[0], e, a.who())
			if err != nil {
				return err
			}
			// As route add says what cannot work as it reads.
			for _, w := range routeWarnings(c.Context(), st, ch.Is) {
				fmt.Fprintf(c.ErrOrStderr(), "warning: route %s: %s\n", ch.Is.ID, w)
			}
			changes := routeChanges(ch.Was, ch.Is)
			return a.out(c.OutOrStdout(), ch.Is, func(w io.Writer) {
				if len(changes) == 0 {
					fmt.Fprintf(w, "route %s unchanged: it is set so already\n", args[0])
					return
				}
				fmt.Fprintf(w, "route %s saved\n%s\n", args[0], changeLines(changes))
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&group, "group", "g", "", "the group to send to, with an optional quota condition: 'builders@Weekly<80'")
	f.StringArrayVar(&further, "else", nil, "a further group, tried when the ones before it are passed over; replaces the further groups, so name every one wanted (--else '' drops them)")
	f.StringVar(&note, "note", "", "why this route ranks as it does, in words (--note '' drops it)")
	// The route's identity is no flag of route set, so naming one fails
	// the parse before RunE: the failure carries the remove-and-add
	// spelling instead of the unknown flag. No flag is hidden to do it:
	// help lists every flag yardr takes.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		name, ok := strings.CutPrefix(err.Error(), "unknown flag: --")
		if !ok || !slices.Contains([]string{"stage", "type", "depot", "label", "bead-priority", "originator"}, name) {
			return err
		}
		id := "<id>"
		if args := c.Flags().Args(); len(args) == 1 {
			id = args[0]
		}
		return fmt.Errorf("--%s is which beads the route takes, which route set does not change; restate the route with yardr route rm %s and yardr route add", name, id)
	})
	return cmd
}

// routeChanges lists what a route set changed: the first group, the
// further groups and the note, as groupChanges does for a
// group.
func routeChanges(was, is store.Route) []defChange {
	var out []defChange
	wg, ig := was.Groups()[0], is.Groups()[0]
	if wg != ig {
		out = append(out, defChange{"--group", wg.String(), ig.String()})
	}
	join := func(gs []store.RouteGroup) string {
		parts := make([]string, 0, len(gs))
		for _, g := range gs {
			parts = append(parts, g.String())
		}
		return strings.Join(parts, ", ")
	}
	if wt, it := join(was.Groups()[1:]), join(is.Groups()[1:]); wt != it {
		out = append(out, defChange{"--else", wt, it})
	}
	if was.Note != is.Note {
		out = append(out, defChange{"--note", store.OneLine(was.Note), store.OneLine(is.Note)})
	}
	return out
}

// routeAddArgs are the words after `yardr route add` that make route r
// again, in the order the docs write them: its stage, the filters it sets,
// its groups with their conditions, and its note. The
// originator is left to the caller, since restating a route is how one
// changes it (route set does not): an add of this with the originator
// wanted, then a remove of r. The words are as the shell takes them;
// shellQuote makes them one line.
func routeAddArgs(r store.Route) []string {
	args := []string{"--stage", r.Stage}
	if r.Type != nil {
		args = append(args, "--type", *r.Type)
	}
	if r.Depot != nil {
		args = append(args, "--depot", *r.Depot)
	}
	for _, l := range r.Labels() {
		args = append(args, "--label", l)
	}
	if r.BeadPriority != nil {
		args = append(args, "--bead-priority", strconv.Itoa(*r.BeadPriority))
	}
	groups := r.Groups()
	args = append(args, "--group", groups[0].String())
	for _, g := range groups[1:] {
		args = append(args, "--else", g.String())
	}
	if r.Note != "" {
		args = append(args, "--note", r.Note)
	}
	return args
}

func (a *app) sessionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Runner sessions working beads"}
	var running bool
	var bead string
	var cut listCut
	list := &cobra.Command{
		Use: "list", Short: "List live sessions", Args: cobra.NoArgs,
		Long: `List live sessions: one for each bead a group has claimed. ORIGINATOR
is the user the bead is for, beside the group; LOGIN is whose login the
group burns, its login_for else the operator's, so a subsidy route shows
another user's work on the operator's login.

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
			cut.say(c.ErrOrStderr(), len(ss), "sessions")
			listed := dispatch.Listed(ss)
			now := time.Now()
			return a.out(c.OutOrStdout(), listed, func(w io.Writer) {
				rows := [][]string{}
				for _, s := range listed {
					// Which runner owns it, and what it is running there.
					runner := s.RunnerName()
					if v, ok := s.Handle["agent"]; ok {
						runner += " " + show(v)
					} else if v, ok := s.Handle["pid"]; ok {
						runner += " pid " + show(v)
					}
					rows = append(rows, []string{s.ID, s.Bead, s.Train, s.Stage, s.Group, s.Originator, s.Login, s.State, or(runner, "-"),
						s.StartedAt.Local().Format("01-02 15:04")})
				}
				table(w, "ID\tBEAD\tTRAIN\tSTAGE\tGROUP\tORIGINATOR\tLOGIN\tSTATE\tRUNNER\tSTARTED", rows)
				for _, s := range listed {
					if s.ResetWait != nil {
						fmt.Fprintln(w, resetWaitLine(*s.ResetWait, s.Session, now))
					}
				}
			})
		},
	}
	cut.flags(list, "ended", "ended sessions")
	list.Flags().StringVar(&bead, "bead", "", "only this bead's sessions")
	list.Flags().BoolVar(&running, "running", false, "only sessions an agent or a script is behind: leave out what a person holds and what another yard works (plain session list lists both)")
	snooze := &cobra.Command{
		Use: "snooze <dur> <why>", Short: "Say this session waits, not stuck, for a bounded time", Args: cobra.ExactArgs(2),
		Long: `Says this session's agent waits, not stuck, for dur (10m, 90s):
while the snooze lasts the yard counts no idle spell for the session
and sends it no idle prod; when the snooze ends the idle count starts
fresh. Each snooze writes a session_snoozed event with the duration and
why, so yardr events shows it.

A snooze waits 15m at most, and 3 in a row without the pane changing;
past either the snooze is refused and the idle and age limits apply as
they always did. The command runs inside a bead session, as yardr bead
done does, and is refused outside one.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			sess := a.identity().session
			if sess == "" {
				return fmt.Errorf("yardr session snooze runs inside a bead session; outside one there is nothing to snooze")
			}
			dur, err := time.ParseDuration(args[0])
			if err != nil {
				return fmt.Errorf("snooze %q: %w", args[0], err)
			}
			until, err := st.SnoozeSession(c.Context(), sess, dur, args[1], a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{
				"session": sess, "until": until, "duration": dur.String(), "why": args[1],
			}, func(w io.Writer) {
				fmt.Fprintf(w, "snoozed %s until %s: %s\n", args[0], until.Local().Format("15:04"), args[1])
			})
		},
	}
	cmd.AddCommand(list, snooze)
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

func printEvent(w io.Writer, e store.Event) {
	data := ""
	if len(e.Data) > 0 {
		data = fmtConfig(printableThreadFields(store.Brief(e.Data)))
	}
	fmt.Fprintf(w, "%6d %s  %-8s %-14s %-12s %s\n", e.Seq, e.At.Local().Format("01-02 15:04:05"), deref(e.Bead), e.Kind, e.Actor, data)
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
				return a.out(c.OutOrStdout(), evs, func(w io.Writer) {
					for _, e := range evs {
						printEvent(w, e)
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
			return a.out(c.OutOrStdout(), evs, func(w io.Writer) {
				for _, e := range evs {
					printEvent(w, e)
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
				enc = json.NewEncoder(c.OutOrStdout())
			}
			// When the yardr server goes away, Follow waits for it and resumes
			// after the last event printed.
			store.Follow(ctx, st, last, streamRetry, func(e store.Event) {
				if enc != nil {
					_ = enc.Encode(e)
				} else {
					printEvent(c.OutOrStdout(), e)
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
