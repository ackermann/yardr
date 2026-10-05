package cli

import (
	"context"
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

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rigcheck"
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
	a.daemonService(cmd)
	return cmd
}

func (a *app) rigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "rig", Short: "Repositories (or plain directories) beads are worked in"}
	var base, clone, check, checkLight, kind string
	var allowNoCheck, lightSlot bool
	add := &cobra.Command{
		Use:   "add <name> [path]",
		Short: "Register (or update) a rig: an existing clone, --clone a URL, or a directory",
		Long: `Register (or update) a rig. A git rig is a repository: beads work on
branches in worktrees and land on its --base. A dir rig is a plain directory
(documents, images, invoices): beads are worked in it as it is, and it has no
base. Without --kind, a path git sees as a checkout is a git rig and any
other directory a dir rig; rig add says which it took.`,
		Example: `  yardr rig add webapp ~/projects/webapp                 # existing clone
  yardr rig add invoices ~/Documents/invoices --kind dir # a folder, no git
  yardr rig add webapp --clone git@github.com:me/webapp.git   # clones to ~/.yardr/rigs/webapp
  yardr rig add legacy ~/src/legacy --base origin/master
  yardr rig add webapp ~/projects/webapp --check 'make test'   # overrides .yardr/check
  yardr rig add webapp ~/projects/webapp --check 'make test' --check-light 'make vet'   # and its light gate
  yardr rig add scratch ~/src/scratch --allow-no-check         # no gate passes`,
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
			if kind != "" && kind != store.RigGit && kind != store.RigDir {
				return fmt.Errorf("--kind %s: give %s or %s", kind, store.RigGit, store.RigDir)
			}
			if clone != "" && kind == store.RigDir {
				return fmt.Errorf("--clone makes a git rig; drop --kind dir")
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
				kind = detectRigKind(path)
			}
			if kind == store.RigDir {
				if c.Flags().Changed("base") {
					return fmt.Errorf("a dir rig has no base; drop --base, or register a git checkout")
				}
				r := store.Rig{Name: name, Path: path, Kind: store.RigDir, Check: check, CheckLight: checkLight, LightSlot: lightSlot, AllowNoCheck: allowNoCheck}
				if err := st.PutRig(c.Context(), r, a.who()); err != nil {
					return err
				}
				return a.out(r, func() {
					fmt.Printf("rig %s → %s (dir: no git, no base)\n", r.Name, r.Path)
					fmt.Printf("check: %s\n", rigCheckLine(r))
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
			r := store.Rig{Name: name, Path: path, Kind: store.RigGit, Base: base, Check: check, CheckLight: checkLight, LightSlot: lightSlot, AllowNoCheck: allowNoCheck}
			if err := st.PutRig(c.Context(), r, a.who()); err != nil {
				return err
			}
			return a.out(r, func() {
				fmt.Printf("rig %s → %s (git, base %s)\n", r.Name, r.Path, r.Base)
				fmt.Printf("check: %s\n", rigCheckLine(r))
			})
		},
	}
	add.Flags().StringVar(&base, "base", "origin/main", "ref bead branches start from (git rigs)")
	add.Flags().StringVar(&kind, "kind", "", "git or dir (default: git when the path is a git checkout, else dir)")
	add.Flags().StringVar(&clone, "clone", "", "clone this URL first (into [path] or ~/.yardr/rigs/<name>)")
	add.Flags().StringVar(&check, "check", "", "merge gate command (sh -c), overriding the repo's "+rigcheck.RepoPath)
	add.Flags().StringVar(&checkLight, "check-light", "", "light gate command (sh -c), overriding the repo's "+rigcheck.LightPath+" (yardr rig check --light)")
	add.Flags().BoolVar(&lightSlot, "light-slot", false, "the light gate takes one of the machine's gate slots, as the full gate does (yardr rig check --help)")
	add.Flags().BoolVar(&allowNoCheck, "allow-no-check", false, "let the gate pass when the rig has no check at all (default: no gate fails)")
	show := &cobra.Command{
		Use: "show <rig>", Short: "Show a rig: path, kind, base, flow and gate", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			r, err := st.GetRig(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.out(r, func() {
				fmt.Printf("rig %s\n", r.Name)
				fmt.Printf("  path:  %s\n", r.Path)
				fmt.Printf("  kind:  %s\n", r.Kind)
				if r.IsDir() {
					fmt.Printf("  base:  none (a dir rig)\n")
				} else {
					fmt.Printf("  base:  %s\n", r.Base)
				}
				fmt.Printf("  flow:  %s\n", rigFlow(c.Context(), st, r.Name))
				fmt.Printf("  check: %s\n", rigCheckLine(*r))
			})
		},
	}
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
					rows = append(rows, []string{r.Name, r.Kind, r.Path, or(r.Base, "-"), rigFlow(c.Context(), st, r.Name)})
				}
				table("NAME\tKIND\tPATH\tBASE\tFLOW", rows)
			})
		},
	}

	var flowType string
	var clear bool
	flow := &cobra.Command{
		Use:   "flow <rig> [--type <type>] (<flow> | --clear)",
		Short: "Point a rig, or its beads of one type, at a named flow",
		Example: `  yardr rig flow yard code                   # the rig's flow
  yardr rig flow yard --type decision decide  # wins for decisions
  yardr rig flow yard --type decision --clear # decisions follow the rig's flow again

The flow is a named flow in the store (yardr flow set) or one in the rig's
repository (.yardr/flows/<name>.flow on the base branch). A pointer
overrides the repository's default.flow and type/<type>.flow.

Setting or clearing a pointer is refused, naming the beads, while an open
bead it would hand to another flow sits in a stage that flow lacks. Move
them under the flow they follow now, or pin them to it (yardr bead update
<id> --meta flow=<name>), then point the rig again.`,
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
			who := store.FlowPointer{Rig: args[0], Type: flowType}.String()
			if clear {
				fmt.Printf("%s points at no flow now; it follows %s\n", who, rigFlow(c.Context(), st, args[0]))
				return nil
			}
			fmt.Printf("%s → flow %s\n", who, name)
			return nil
		},
	}
	flow.Flags().StringVarP(&flowType, "type", "t", "", "only the rig's beads of this type")
	flow.Flags().BoolVar(&clear, "clear", false, "remove the pointer")
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a rig from the store with its routes and flow pointers (refused while it has open beads or live sessions)",
		Example: `  yardr rig rm yard          # registered under the wrong name; nothing uses it
  yardr rig rm legacy --json # what went with it: its routes and flow pointers

Only the store changes. The clone, its worktrees and its .yardr/ stay where
they are; remove them yourself if you want them gone. Closed beads keep the
rig's name as history. A rig with open beads or live sessions is refused and
the beads and sessions named: close or move the beads first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			r, err := st.DeleteRig(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(r, func() {
				ptrs := make([]string, 0, len(r.FlowPointers))
				for _, p := range r.FlowPointers {
					ptrs = append(ptrs, p.String())
				}
				fmt.Printf("removed rig %s; %d route(s) and %d flow pointer(s) went with it\n", r.Rig, len(r.Routes), len(ptrs))
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
	cmd.AddCommand(add, list, show, flow, rm, a.rigCheckCmd(), a.rigInitCmd(), a.rigSourcesCmd(), a.rigSourceCmd())
	return cmd
}

func (a *app) rigCheckCmd() *cobra.Command {
	var dir string
	var tail int
	var timeout, slotWait time.Duration
	var light, first bool
	cmd := &cobra.Command{
		Use:   "check <rig>",
		Short: "Run the rig's merge gate in a directory and report pass or fail",
		Long: `Run the rig's merge gate: the store's --check override if set, else
` + rigcheck.RepoPath + ` as it is at the tip of the rig's base branch. The gate
is never read from the directory it runs in, so a bead branch that edits
` + rigcheck.RepoPath + ` does not change its own gate. When the base has a remote
the file is read from the remote-tracking ref after a fetch (origin/main),
not from the local branch, which any worktree can move. A rig with neither
fails ("no gate") unless it was added with --allow-no-check.

A dir rig has no base: its ` + rigcheck.RepoPath + ` is read from the rig's directory
and run in --dir (default the rig's directory). A dir rig without one passes
("no gate"): nothing of it merges anywhere.

--light runs the rig's light gate instead: the store's --check-light if set,
else ` + rigcheck.LightPath + ` at the tip of the base, found and run under
every rule here (read from the base, the timeout, the exit statuses). It is
for where the full gate still stands before the base moves: a builder's and a
reviewer's own check of a bead, and a part of an epic, which lands on the
epic's branch under it (docs/pipeline.md). A rig without
a light gate runs its full gate and says so in one line: --light never passes
for want of a file. A rig whose gate is a store override has its light gate
in the store too: ` + rigcheck.LightPath + ` in its repository is not read.

Either gate gets ` + rigcheck.BaseVar + ` in its environment: the commit the
repository's gate was read at (the tip of the base, as fetched), so a check
can look at what the branch changed against it. Under a store override it is
the rig's base as it resolves locally; it is unset when that does not
resolve, and for a dir rig.

The check is killed, with everything it started, once it has run for
--timeout, which fails the gate; each git call that finds the gate is bounded
too.

No more than gate_slots full gates (yardr settings, default ` + strconv.Itoa(rigcheck.DefaultSlots) + `) run at once
on the machine, whoever starts them and from whichever yard of the user: a
gate takes a slot before the check starts and gives it back when the check
ends, however it ends. With none free it waits, and says so on stderr when
it starts to wait and when it gets one, with how long it waited. --timeout
starts when the check starts, not while it waits. The wait is bounded by
--slot-wait; past it the check could not run (exit 2). --first lets this run
go before the others that wait: it is for the refinery and the integrator
(YARDR_GATE_FIRST=1 says the same), whose one gate at a time every approved
bead waits behind. gate_slots 0 is no limit; YARDR_GATE_SLOTS, when set, is
used instead of the setting. The light gate takes no slot, unless the rig
was added with --light-slot; the full gate run for want of a light one takes
one. A rig check started by a check that holds a slot (a gate whose tests
run a gate) takes none: ` + rigcheck.SlotVar + ` in the check's environment says so.
The slots are lock files in ` + slotDirHelp + `
(YARDR_GATE_DIR names another directory), held by this process: the kernel
frees one when its process ends. yardr status counts them.

Exits 0 when the gate passes, 1 when it fails (including no gate and a
timeout), 2 when it could not be run at all (unknown rig, base that does not
resolve, git failing, no slot within --slot-wait), so a merge script can tell a red branch from a broken
setup. A check ended by a signal from outside (its process killed, or its own
exit status 128+N, which is how a shell reports a command killed by signal N)
exits 128+N: no branch did that, so a merge script runs the gate again rather
than send the bead back. The kill at --timeout is not that: it exits 1.

The whole output of a gate that did not pass (red, timed out, ended by a
signal; the light gate's too) is kept in a file, mode 0600, which the FAIL
line names after "whole output: ":
<home>/gates/<rig>/<dir>-<UTC time>.log, <dir> being the last element of the
directory it ran in (a bead's worktree is named after the bead), and
".light.log" for the light gate. A gate that passed keeps nothing. Each run
that keeps one removes the rig's kept outputs older than 14 days and, for
the same <dir>, all but the newest 5. --json has the path as log_file.

Before the tail, a red gate's output is searched for the lines that say what
failed, when the rig says how to tell them: ` + rigcheck.FailPatternPath + `, read
where the repository's gate is (the base; a dir rig's directory), holds
regular expressions (Go's syntax), one per line, # for a comment. Every line
matching one is printed with 2 lines before it and 8 after, each line once,
150 lines at most, each stretch headed by its line number in the output. A
rig without the file gets the tail alone, and the file.

The check also fails while an open bead of the rig sits in a stage the flow
governing it lacks (a repository flow lost the stage on the base), and names
each: bead, stage, flow and the commit it was read at. No branch can fix
that, so with a green gate it exits 2; advance the beads to a stage the flow
has (yardr bead advance <id> --to <stage>).

Stage files are checked too. A repository flow's (` + store.RepoFlowDir + `/<flow>/<stage>.md)
are read in --dir as they are there, since that is what would land: one named
after no stage of its flow, or in a directory with no flow file beside it,
fails the check (exit 1), as nobody would ever be shown it. A store flow's
are in the yard's home (<home>/flows/<name>/<stage>.md): one named after no
stage of its flow fails the check too, with exit 2 when nothing else does,
as no branch can fix it. The non-terminal stages without a stage file are
named in a note, which never fails the check.

So is each file of the rig's repository that nothing reads because a flow in
the store, or a prompt from a pack or the yard's home, stands in front of
it: a note, never a failure. They are read on the base, as a brief reads
them; yardr rig sources <rig> shows every source.`,
		Example: `  yardr rig check yard                                   # in the rig itself
  yardr rig check yard --dir ~/.yardr/worktrees/yard/yard-12   # in a bead's worktree
  yardr rig check yard --light --dir ~/.yardr/worktrees/yard/yard-12   # the light gate, a builder's check`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			res, err := a.runRigCheck(c.Context(), args[0], dir, timeout, light,
				rigcheck.Slots{First: first || config.Getenv("GATE_FIRST") != "", Wait: slotWait})
			if err != nil {
				return checkNotRun{err}
			}
			if err := a.out(res, func() {
				// First, so nobody reads the full gate's output as the light one's.
				if res.Fallback != "" {
					fmt.Println(res.Fallback)
				}
				// What failed first, where the rig says how to tell: in a long
				// run it is far above the tail.
				out := rigcheck.Tail(res.Output, tail)
				if res.Matched != "" {
					fmt.Printf("lines matching %s, of %d lines of output:\n%s\n", rigcheck.FailPatternPath, rigcheck.Lines(res.Output), res.Matched)
					if res.MatchedMore > 0 {
						fmt.Printf("(%d more matching lines not shown)\n", res.MatchedMore)
					}
					if strings.TrimSpace(out) != "" {
						fmt.Printf("the last %d lines:\n", rigcheck.Lines(out))
					}
				}
				// No gate has only yardr's own message, said below.
				switch {
				case res.Source == "":
				case strings.TrimSpace(out) != "":
					fmt.Println(out)
				// Of the gate itself: what else fails the check (stranded
				// beads, stray stage files) is said below, not laid on a gate
				// that passed.
				case gateGreen(res):
				case strings.TrimSpace(res.Output) == "":
					fmt.Println("(the check printed nothing)")
				default:
					// A red gate always says where to look, even with --tail 0.
					fmt.Printf("(%d lines of output not shown; --tail to see them, or --json for all)\n",
						strings.Count(strings.TrimRight(res.Output, "\n"), "\n")+1)
				}
				for _, n := range slices.Concat(res.PatternNotes, res.StageNotes, res.ShadowNotes) {
					fmt.Printf("note: %s\n", n)
				}
				// A red gate's line ends with where all of its output is: the
				// refinery reads the file's name from here (wholeOutput).
				var kept string
				switch {
				case res.LogFile != "":
					kept = wholeOutput + res.LogFile
				case res.LogError != "":
					kept = "; its output could not be kept: " + res.LogError
				}
				src := res.Source
				if res.From != "" {
					src += " from " + res.From
				}
				if res.Light {
					src = "light gate, " + src
				}
				switch {
				case res.Source == "" && res.Kind == store.RigDir:
					fmt.Printf("rig %s is a dir rig with no check (no --check, no %s in %s): pass (no gate)\n", res.Rig, rigcheck.RepoPath, res.Dir)
				case res.Source == "" && gateGreen(res):
					fmt.Printf("rig %s has no check (no --check, no %s on %s), allowed by allow_no_check: pass\n", res.Rig, rigcheck.RepoPath, res.From)
				case res.Source == "":
					fmt.Printf("rig %s has no check (no --check, no %s on %s): FAIL (no gate)\n", res.Rig, rigcheck.RepoPath, res.From)
				case res.Pass:
					fmt.Printf("check %s (%s) in %s: pass\n", res.Rig, src, res.Dir)
				case res.TimedOut:
					fmt.Printf("check %s (%s) in %s: FAIL (timed out after %s)%s\n", res.Rig, src, res.Dir, timeout, kept)
				case len(res.Stranded)+len(res.StrayStageFiles)+len(res.StrayHomeStageFiles) > 0 && res.ExitCode == 0:
					fmt.Printf("check %s (%s) in %s: gate passed\n", res.Rig, src, res.Dir)
				case res.Signal != 0:
					fmt.Printf("check %s (%s) in %s: FAIL (exit %d, ended by signal %d)%s\n", res.Rig, src, res.Dir, res.ExitCode, res.Signal, kept)
				default:
					fmt.Printf("check %s (%s) in %s: FAIL (exit %d)%s\n", res.Rig, src, res.Dir, res.ExitCode, kept)
				}
				for _, sb := range res.Stranded {
					fmt.Printf("stranded: bead %s; advance it to a stage the flow has (yardr bead advance %s --to <stage>)\n", sb, sb.Bead)
				}
				if n := len(res.Stranded); n > 0 {
					fmt.Printf("rig %s: FAIL (open beads in a stage their flow lacks: %d)\n", res.Rig, n)
				}
				for _, s := range slices.Concat(res.StrayStageFiles, res.StrayHomeStageFiles) {
					fmt.Printf("stage file: %s\n", s)
				}
				if n := len(res.StrayStageFiles) + len(res.StrayHomeStageFiles); n > 0 {
					fmt.Printf("rig %s: FAIL (stage files that belong to no stage: %d)\n", res.Rig, n)
				}
			}); err != nil {
				return err
			}
			switch {
			case res.Pass:
				return nil
			case gateGreen(res) && len(res.StrayStageFiles) == 0:
				// Only stranded beads, or stage files in the yard's home, fail
				// it: no branch's to fix.
				return checkNotRun{errCheckFailed}
			case res.Signal != 0:
				return checkSignalled(res.Signal)
			}
			return errCheckFailed
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to run it in (default: the rig)")
	cmd.Flags().IntVar(&tail, "tail", 40, "last lines of output to show (a red gate's lines matching "+rigcheck.FailPatternPath+" are shown before them, and all of it is kept in a file)")
	cmd.Flags().DurationVar(&timeout, "timeout", rigcheck.DefaultTimeout, "kill the check and fail after this long")
	cmd.Flags().BoolVar(&light, "light", false, "run the rig's light gate ("+rigcheck.LightPath+"); the full one when the rig has none")
	cmd.Flags().BoolVar(&first, "first", false, "take a gate slot before the other runs that wait for one (the refinery, the integrator)")
	cmd.Flags().DurationVar(&slotWait, "slot-wait", rigcheck.DefaultSlotWait, "give up (exit 2) after waiting this long for a gate slot")
	return cmd
}

// slotDirHelp is where the gate slots are, for the help.
const slotDirHelp = "~/.local/state/yardr/gates"

// runRigCheck runs rig's gate in dir within timeout, its light one when
// light (see rigcheck.RunLight); an error means it could not be run, as
// opposed to a failing Result. slots says how this run waits for its slot
// of the machine; where the slots are and how many is filled in here, from
// the yard.
func (a *app) runRigCheck(ctx context.Context, rigName, dir string, timeout time.Duration, light bool, slots rigcheck.Slots) (*rigcheck.Result, error) {
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	rig, err := st.GetRig(ctx, rigName)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		if dir, err = filepath.Abs(dir); err != nil {
			return nil, err
		}
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("--timeout must be positive")
	}
	if slots.Wait <= 0 {
		return nil, fmt.Errorf("--slot-wait must be positive")
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if slots.Limit, err = rigcheck.SlotLimit(all); err != nil {
		return nil, err
	}
	slots.Dir, slots.Holder = rigcheck.SlotDir(), fmt.Sprintf("rig %s in %s", rig.Name, or(dir, rig.Path))
	// On stderr, and at once: a person who waits must see why, and --json
	// keeps stdout.
	slots.Notify = func(line string) { fmt.Fprintln(stderr, "yardr: "+line) }
	// Before the gate and outside its deadline: a gate that ran out of time
	// must not hide the stranded beads.
	stranded, err := st.Stranded(ctx, rig.Name)
	if err != nil {
		return nil, err
	}
	notes, stray, strayHome, err := stageFileFindings(ctx, st, *rig, or(dir, rig.Path), a.home())
	if err != nil {
		return nil, err
	}
	// The timeout is the check's own: the wait for a slot does not eat it.
	res, err := rigcheck.RunWith(ctx, *rig, dir, rigcheck.Options{Light: light, Timeout: timeout, Slots: &slots})
	if err != nil {
		return nil, err
	}
	if len(stranded) > 0 {
		res.Stranded, res.Pass = stranded, false
	}
	// The whole output of a gate that did not pass is kept, whoever ran it:
	// the tail shown is rarely where the failure is. A run that passed keeps
	// nothing (nobody is sent back to read it), and no gate printed nothing.
	if !gateGreen(res) && res.Source != "" {
		if res.LogFile, err = rigcheck.Keep(a.home(), res, time.Now()); err != nil {
			res.LogError = err.Error()
		}
	}
	res.StageNotes = notes
	// A rig whose sources do not resolve has other trouble; the notes wait.
	if lines, err := rigSources(ctx, st, rig.Name, a.home()); err == nil {
		res.ShadowNotes = shadowNotes(rig.Name, lines)
	}
	if len(stray) > 0 {
		res.StrayStageFiles, res.Pass = stray, false
	}
	if len(strayHome) > 0 {
		res.StrayHomeStageFiles, res.Pass = strayHome, false
	}
	return res, nil
}

// stageFileFindings looks at the stage files of the flows governing rig.
// A repository flow's are read in dir, the tree the gate runs in, as they
// are on disk: read from the base like a brief's, a typo could only be found
// after it landed, and its fix could not pass the gate. A store flow's are
// in the yard's home. notes name, per flow, the non-terminal stages without
// a stage file. stray are the files in dir that belong to no stage (see
// store.RepoFlows.StrayStageFiles), which the branch can fix; strayHome are
// those of a store flow in the home, which no branch can.
func stageFileFindings(ctx context.Context, st store.API, rig store.Rig, dir, home string) (notes, stray, strayHome []string, err error) {
	var tree *store.RepoFlows
	if dir != "" {
		if tree, err = store.ReadFlowDir(dir); err != nil {
			return nil, nil, nil, err
		}
		stray = tree.StrayStageFiles()
	}
	// A rig whose flows do not resolve has other trouble; the notes wait.
	governing, _ := st.RigFlows(ctx, rig.Name)
	for _, g := range governing {
		f := g.Flow
		var has func(stage string) bool
		var where string
		switch {
		case g.Source.InStore():
			have, more, err := store.HomeStageFiles(home, f)
			if err != nil {
				return nil, nil, nil, err
			}
			strayHome = append(strayHome, more...)
			has = func(s string) bool { return have[s] != "" }
			where = filepath.Join(store.HomeStageDir(home, f.Name), "<stage>.md")
		case tree == nil:
			continue
		default:
			// A repository flow: the one in dir is the one that would land.
			for _, rf := range tree.Flows {
				if rf.Path == g.Source.Path && rf.Flow != nil {
					f = rf.Flow
				}
			}
			has = func(s string) bool { return tree.StageFile(f.Name, s) != nil }
			where = store.StageFilePath(f.Name, "<stage>")
		}
		var missing []string
		for _, s := range f.Stages {
			if !f.IsTerminal(s) && !has(s) {
				missing = append(missing, s)
			}
		}
		if len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("flow %s has no stage file for %s (%s)", f.Name, strings.Join(missing, ", "), where))
		}
	}
	return notes, stray, strayHome, nil
}

// wholeOutput is what, in the line that says a gate failed, comes before the
// file its whole output was kept in, up to the end of the line. merge-local
// and integrate-epic find the file by it.
const wholeOutput = "; whole output: "

// gateGreen reports whether the gate itself passed in res, whatever else
// fails the check: it ran to exit 0, or the rig needs none.
func gateGreen(res *rigcheck.Result) bool {
	return res.ExitCode == 0 && !res.TimedOut
}

// rigCheckLine says what gates a rig's merges, as the store has it: the
// --check override if set, else the repo's file on the base.
func rigCheckLine(r store.Rig) string {
	line := rigcheck.RepoPath + " on " + r.Base
	if r.IsDir() {
		line = rigcheck.RepoPath + " in the directory, if any (none: no gate, passes)"
	}
	if r.Check != "" {
		line = r.Check + " (store override of " + rigcheck.RepoPath + ")"
	}
	if r.CheckLight != "" {
		line += "; light gate: " + r.CheckLight + " (store override of " + rigcheck.LightPath + ")"
	}
	if r.LightSlot {
		line += "; light_slot: the light gate takes a gate slot"
	}
	if r.AllowNoCheck {
		line += "; allow_no_check: no gate passes"
	}
	return line
}

// rigFlow names the flow governing rig's beads (pins and types aside) and
// where it comes from, for listings.
func rigFlow(ctx context.Context, st store.API, rig string) string {
	rfs, err := st.RigFlows(ctx, rig)
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
		Use:   "add [<name> --runner <manual|exec|herdr>]",
		Short: "Create or replace a group (no name: an interactive form)",
		Long: `Create or replace a group. With no name, in a terminal, an interactive form
lists the existing groups to edit or creates a new one, and for herdr groups
offers the agent (Claude Code, Codex, Grok, Kimi, opencode, pi, or any other
herdr agent), a model read live from that agent, and its permission flags.

With a name, the group is replaced by exactly what the flags say, except that
a herdr group without --set kind gets the yard's default_kind (yardr settings
get default_kind).

An agent nobody watches stops at every permission prompt. These flags keep it
going (pass them as --set 'args=[...]'; yardr init gives them to the mayor):
` + agents.UnattendedHelp(),
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
			withDefaultKind(c.Context(), st, g.Runner, g.Config)
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
	var all, running bool
	list := &cobra.Command{
		Use: "list", Short: "List live sessions", Args: cobra.NoArgs,
		Long: `List live sessions: one for each bead a group has claimed.

A manual group's claim is among them, though nothing runs in it: the bead
waits for a person. --running leaves those out, so the list is what an
agent (herdr) or a script (exec) is working on now.`,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ss, err := st.Sessions(c.Context(), all)
			if err != nil {
				return err
			}
			if running {
				// A manual group's claim is a bead waiting for a person, with
				// nothing running behind it (yardr-7r3h).
				ss = slices.DeleteFunc(ss, store.Session.Manual)
			}
			return a.out(ss, func() {
				rows := [][]string{}
				for _, s := range ss {
					// Which runner owns it, and what it is running there.
					runner := s.Runner
					if s.Manual() {
						runner = "manual"
					}
					if v, ok := s.Handle["agent"]; ok {
						runner = strings.TrimSpace(or(runner, "herdr") + " " + show(v))
					} else if v, ok := s.Handle["pid"]; ok {
						runner = strings.TrimSpace(or(runner, "exec") + " pid " + show(v))
					}
					rows = append(rows, []string{s.ID, s.Bead, s.Epic, s.Stage, s.Group, s.State, or(runner, "-"),
						s.StartedAt.Local().Format("01-02 15:04")})
				}
				table("ID\tBEAD\tEPIC\tSTAGE\tGROUP\tSTATE\tRUNNER\tSTARTED", rows)
			})
		},
	}
	list.Flags().BoolVarP(&all, "all", "a", false, "include ended sessions")
	list.Flags().BoolVar(&running, "running", false, "only sessions an agent or a script is behind: leave out the beads a manual group holds for a person")
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

// detectRigKind is the kind of rig a path makes when rig add is given no
// --kind: git when git sees a repository there, else a plain directory.
func detectRigKind(path string) string {
	if exec.Command("git", "-C", path, "rev-parse", "--git-dir").Run() == nil {
		return store.RigGit
	}
	return store.RigDir
}
