// Package cli implements the yard command line. Agents use the same tool as
// humans; every command accepts --json.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

type app struct {
	json bool
	// stdout and stderr are the presets rootCmd applies to cobra with
	// SetOut and SetErr. Commands write to their cobra command at run
	// time (c.OutOrStdout, c.ErrOrStderr); these exist only for what
	// runs before a command does, and for tests to preset. Nil means
	// the process's streams. Pre-run records the run's error writer
	// on stderr, for the helpers with no command in reach.
	stdout io.Writer
	stderr io.Writer
	// stdin is where a command a program leads reads its answers; tests
	// hand their own. Nil means the process's standard input.
	stdin io.Reader
	// browser opens a URL in this machine's browser, for yardr web open;
	// tests hand their own, so that none opens. Nil means the machine's
	// (openBrowser).
	browser func(url string) error
	// isTerm tells whether a stream is a terminal, for what a command
	// shows a person and keeps out of a log (the token URL of yardr web
	// serve); tests hand their own. Nil means the real check (isTerminal).
	isTerm func(w io.Writer) bool
	// inTerm tells whether the standard input a command reads is a
	// terminal with a person at it, for the gate in front of a weighty
	// approval (atTerminal); tests hand their own, since none has a
	// terminal to give. Nil means the real check.
	inTerm func(in io.Reader) bool
	actor  string
	// actingAs is the actor resolved once by PersistentPreRunE, so a later
	// lookup cannot time out and silently change this run's attribution.
	actingAs    string
	actorLoaded bool
	// homeDir is --home: the yard this run and all it starts work in.
	homeDir string
	// env is the run's environment by full key, less the process facts
	// (see environ): what names its yard, who acts, and what its children
	// start with. Nil reads nothing (see getenv): Execute seeds it, and
	// tests construct it.
	env map[string]string
	cfg *config.Config
	st  store.API
	// onboarded is set once this run has created the yard (see firstRun).
	onboarded bool
	// guided is set once the onboarding has asked how to go on with the
	// setup (see handOff): yardr setup, run on a home with no yard, has
	// then been answered.
	guided bool
	// linkAsked is set once this run has asked whether to link the yard
	// with another (offerLink).
	linkAsked bool
	// loginPATHMerged is set once this run has the login shell's PATH in
	// its own, by useLoginPATH for a service's file or by startPart for
	// a child: one asking of the shell serves the run.
	loginPATHMerged bool
	// noChat is --no-chat: this run ends where it ended before there was a
	// chat, with the yardmaster's place printed, and opens no view (see
	// toChat).
	noChat bool
	// self is who this run acts as, once asked (see identity).
	self *resolved
	// edgeHTTP is what yardr edge calls the edge by; nil is a client with
	// the system's roots. edgePoll is how often a connect asks whether its
	// code is approved; zero is edgelink's. Tests hand the client that
	// trusts their stand-in's certificate, and a short poll.
	edgeHTTP *http.Client
	edgePoll time.Duration
}

// Execute runs the CLI and returns the process exit code.
func Execute() int { return execute(&app{env: environMap(os.Environ())}, os.Args[1:]) }

// execute runs args on a and returns the exit code.
func execute(a *app, args []string) int {
	if a.stdin == nil {
		a.stdin = os.Stdin
	}
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	if a.st != nil {
		_ = a.st.Close(context.Background())
	}
	if err != nil && !errors.Is(err, errCheckFailed) { // a red check has reported itself
		if a.json {
			_ = json.NewEncoder(root.OutOrStdout()).Encode(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(root.ErrOrStderr(), "yardr:", err)
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
				return a.printVersion(c.OutOrStdout())
			}
			if a.onboarded {
				return nil
			}
			if err := c.Help(); err != nil {
				return err
			}
			a.unreadLine(c.OutOrStdout(), c.Context())
			return nil
		},
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			// First: the run's error writer, for the helpers with no
			// command in reach (identity). A preset survives: the root
			// applies it, so this resolves to it.
			a.stderr = c.ErrOrStderr()
			// Before anything reads the home: the config of this run, and
			// the environment of what it starts, follow the flag.
			if c.Flags().Changed("home") {
				if err := a.useHome(a.homeDir); err != nil {
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
	root.AddCommand(a.serverCmd(), a.daemonCmd(), a.depotCmd(), a.beadCmd(), a.readyCmd(), a.telemetryCmd(),
		a.depCmd(), a.linkCmd(), a.groupCmd(), a.routeCmd(), a.flowCmd(), a.sessionCmd(), a.eventsCmd(), a.watchCmd(), a.primeCmd(),
		a.bdpCmd(), a.webCmd(), a.crewCmd(), a.userCmd(), a.inboxCmd(), a.mailCmd(), a.timerCmd(), a.chatCmd(), a.rememberCmd(), a.recallCmd(), a.hookCmd(), a.sourceCmd(), a.callCmd(), a.limitsCmd(), a.harnessCmd(),
		a.peerCmd(), a.edgeCmd(), a.subscribeCmd(), a.unsubscribeCmd(), a.threadCmd(), a.replyCmd(), a.reactCmd(), a.reviewCmd(), a.storeCmd(), a.packCmd(), a.docsCmd(), a.previewCmd(),
		a.initCmd(), a.settingsCmd(), a.doctorCmd(), a.statusCmd(), a.upgradeCmd(), a.upCmd(), a.downCmd(), a.uninstallCmd(), a.setupCmd(), a.otelCmd(), a.diskCmd(), a.versionCmd())
	if a.stdout != nil {
		root.SetOut(a.stdout)
	}
	if a.stderr != nil {
		root.SetErr(a.stderr)
	}
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
		RunE: func(c *cobra.Command, _ []string) error { return a.printVersion(c.OutOrStdout()) },
	}
}

// printVersion is yardr version, and yardr --version.
func (a *app) printVersion(w io.Writer) error {
	return a.out(w, map[string]string{"version": version.Tag(), "revision": version.Current()}, func(w io.Writer) {
		fmt.Fprintln(w, version.Describe())
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
// actor, else the login (see config.ActorWith). It fails only when the login
// must come from the OS and that lookup does not answer.
func (a *app) resolveActor() (string, error) {
	if a.actor != "" {
		return a.actor, nil
	}
	if id := a.identity(); id.actor != "" {
		return id.actor, nil
	}
	return config.ActorWith(a.getenv)
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
		cfg, err := a.config()
		if err != nil {
			return nil, err
		}
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

// out prints v as JSON to w, or calls text for humans with the same w.
func (a *app) out(w io.Writer, v any, text func(w io.Writer)) error {
	if a.json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(w)
	return nil
}

func table(w io.Writer, header string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func deref(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

// parseSets turns key=value flags into a config map; values that parse as
// JSON (true, 3, ["a"]) keep their type, anything else is a string.
