// Package cli implements the yard command line. Agents use the same tool as
// humans; every command accepts --json.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

type app struct {
	json  bool
	actor string
	// actingAs is the actor resolved once by PersistentPreRunE, so a later
	// lookup cannot time out and silently change this run's attribution.
	actingAs    string
	actorLoaded bool
	// homeDir is --home: the yard this run and all it starts work in.
	homeDir string
	cfg     *config.Config
	st      store.API
	// onboarded is set once this run has created the yard (see firstRun).
	onboarded bool
	// guided is set once the onboarding has asked how to go on with the
	// setup (see handOff): yardr setup, run on a home with no yard, has
	// then been answered.
	guided bool
	// linkAsked is set once this run has asked whether to link the yard
	// with another (offerLink).
	linkAsked bool
	// noChat is --no-chat: this run ends where it ended before there was a
	// chat, with the yardmaster's place printed, and opens no view (see
	// toChat).
	noChat bool
	// self is who this run acts as, once asked (see identity).
	self *resolved
}

// stderr is where Execute reports an error; tests replace it.
var stderr io.Writer = os.Stderr

// Execute runs the CLI and returns the process exit code.
func Execute() int { return execute(&app{}, os.Args[1:]) }

// execute runs args on a and returns the exit code.
func execute(a *app, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	if a.st != nil {
		_ = a.st.Close(context.Background())
	}
	if err != nil && !errors.Is(err, errCheckFailed) { // a red check has reported itself
		if a.json {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(stderr, "yardr:", err)
		}
	}
	return exitCode(err)
}

func (a *app) rootCmd() *cobra.Command {
	var showVersion bool
	root := &cobra.Command{
		Use:           "yardr",
		Short:         "Route beads of work to whoever works them",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// Bare yardr: the onboarding on a home with no yard yet (run by
		// firstRun), help otherwise.
		RunE: func(c *cobra.Command, _ []string) error {
			if showVersion {
				return a.printVersion()
			}
			if a.onboarded {
				return nil
			}
			if err := c.Help(); err != nil {
				return err
			}
			a.unreadLine(c.Context())
			return nil
		},
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			// Before anything reads the home: every config.Load of this
			// run, and the environment of what it starts, follow the flag.
			if c.Flags().Changed("home") {
				if err := config.UseHome(a.homeDir); err != nil {
					return err
				}
			}
			// Who acts, before anything hangs asking: with no actor
			// named the OS is asked, and under Codex's sandbox that
			// lookup hangs past signals, so the command fails here, with
			// how to name one, instead of hanging wherever it first acts
			// (yardr-tbm4).
			actor, err := a.resolveActor()
			if err != nil {
				return err
			}
			a.actingAs, a.actorLoaded = actor, true
			// yardr --version answers as yardr version does: on a home
			// with no yard, without the onboarding.
			if showVersion {
				return nil
			}
			return a.firstRun(c)
		},
	}
	// Not persistent: it is the flag of bare yardr, tried before any
	// command is known. -v as cobra gives its own version flag.
	root.Flags().BoolVarP(&showVersion, "version", "v", false, "print which build of yardr this is, as yardr version does")
	root.PersistentFlags().StringVar(&a.homeDir, "home", "", "the yard's directory (default $YARDR_HOME, else ~/.yardr)")
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print JSON")
	root.PersistentFlags().StringVar(&a.actor, "actor", "", "who is acting (default $YARDR_ACTOR, else $USER)")
	root.AddCommand(a.serverCmd(), a.daemonCmd(), a.depotCmd(), a.beadCmd(), a.readyCmd(),
		a.depCmd(), a.linkCmd(), a.groupCmd(), a.routeCmd(), a.flowCmd(), a.sessionCmd(), a.eventsCmd(), a.watchCmd(), a.primeCmd(),
		a.bdpCmd(), a.webCmd(), a.crewCmd(), a.userCmd(), a.inboxCmd(), a.mailCmd(), a.timerCmd(), a.chatCmd(), a.rememberCmd(), a.recallCmd(), a.hookCmd(), a.sourceCmd(), a.callCmd(), a.limitsCmd(), a.harnessCmd(),
		a.peerCmd(), a.subscribeCmd(), a.unsubscribeCmd(), a.threadCmd(), a.replyCmd(), a.reviewCmd(), a.storeCmd(), a.packCmd(),
		a.initCmd(), a.settingsCmd(), a.doctorCmd(), a.statusCmd(), a.upgradeCmd(), a.upCmd(), a.downCmd(), a.uninstallCmd(), a.setupCmd(), a.otelCmd(), a.diskCmd(), a.versionCmd())
	return root
}

// versionCmd prints which build this is. It touches neither the store nor
// the server, so it answers on a home with no yard and while a server of
// another build runs.
func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print which build of yardr this is", Args: cobra.NoArgs,
		Long: `Print which build of yardr this is: the release and its revision for a
build from a release archive ("v1.2.3 (abcdef123456)"), the revision alone
for a go install, "unknown" for a build that carries neither.`,
		RunE: func(*cobra.Command, []string) error { return a.printVersion() },
	}
}

// printVersion is yardr version, and yardr --version.
func (a *app) printVersion() error {
	return a.out(map[string]string{"version": version.Tag(), "revision": version.Current()}, func() {
		fmt.Println(version.Describe())
	})
}

// exitCode is the process status for a command's error: 1 for a red check
// or most errors, 2 for a command refused for what it had to ask and no
// terminal to ask in (a home with no yard yet, an import with answers
// open), 3 for something not found.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.As(err, new(notInitialized)), errors.As(err, new(noTerminal)):
		return 2
	case errors.Is(err, errCheckFailed):
		return 1
	case errors.Is(err, store.ErrNotFound):
		return 3
	}
	return 1
}

// errCheckFailed ends a command that has already printed its failing result
// (yardr doctor's faults): exit 1 without a second message.
var errCheckFailed = errors.New("check failed")

// noTerminal is a command that had something to ask and no terminal to ask
// it in: nothing was written, and the same command with what it named goes
// through. Exit 2, as a home with no yard and no terminal to onboard in
// (notInitialized), so that whoever runs yardr for a person tells "ask them
// and run it again" from a failure by the status alone.
type noTerminal struct{ msg string }

func (e noTerminal) Error() string { return e.msg }

// resolveActor is who this run acts as: --actor, else the session's
// actor, else the login (see config.Actor). It fails only when the login
// must come from the OS and that lookup does not answer.
func (a *app) resolveActor() (string, error) {
	if a.actor != "" {
		return a.actor, nil
	}
	if id := a.identity(); id.actor != "" {
		return id.actor, nil
	}
	return config.Actor()
}

func (a *app) who() string {
	if a.actorLoaded {
		return a.actingAs
	}
	who, err := a.resolveActor()
	if err != nil {
		// Commands run through the root fail in pre-run instead. A
		// direct subcommand call (as in some tests) keeps the old
		// fallback when it bypasses pre-run.
		return "unknown"
	}
	return who
}

// expect is what a move made from this process expects of the bead: that
// the session yardr started it for ($YARDR_SESSION) still owns the bead it
// was started for, and that the bead is still at rev (--revision) when one
// is given. An agent kept or orphaned after its bead moved on is then
// refused rather than ending the session that owns the bead now. What the
// session owns the store takes from its own record of it, and a call from
// a pane the store knows is checked as that pane's session whatever this
// says (see store.Expect). Outside a session, and without rev, it expects
// nothing. In a bead's worktree with another bead's session in the
// environment, the session is the worktree's (see identity).
func (a *app) expect(rev string) store.Expect {
	return store.Expect{Session: a.identity().session, Revision: rev}
}

// connect returns the store of cfg's yard from its server, starting the
// server if none answers. Tests replace it: the server it starts is this
// binary run as `server run`, and a test binary is not yardr.
var connect = func(ctx context.Context, cfg *config.Config) (store.API, error) {
	st, err := server.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// store connects to the yardr server, starting it on first use.
func (a *app) store(ctx context.Context) (store.API, error) {
	if a.st == nil {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		a.cfg = cfg
		st, err := connect(ctx, cfg)
		if err != nil {
			return nil, err
		}
		// The session this run acts as goes with every call, so one made
		// before its session is on record is refused as its session's.
		if c, ok := st.(*rpc.Client); ok {
			c.Session = a.identity().session
		}
		a.st = st
	}
	// The panes a command asks herdr about are the yard's, whatever herdr
	// session its shell is in.
	runner.FollowSetting(a.st)
	return a.st, nil
}

// out prints v as JSON, or calls text for humans.
func (a *app) out(v any, text func()) error {
	if a.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text()
	return nil
}

func table(header string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
}

func deref(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

// parseSets turns key=value flags into a config map; values that parse as
// JSON (true, 3, ["a"]) keep their type, anything else is a string.
