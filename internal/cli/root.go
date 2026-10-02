// Package cli implements the yard command line. Agents use the same tool as
// humans; every command accepts --json.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

type app struct {
	json  bool
	actor string
	cfg   *config.Config
	st    store.API
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	a := &app{}
	root := &cobra.Command{
		Use:           "yardr",
		Short:         "Route beads of work to whoever works them",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print JSON")
	root.PersistentFlags().StringVar(&a.actor, "actor", "", "who is acting (default $YARDR_ACTOR, else $USER)")
	root.AddCommand(a.serverCmd(), a.daemonCmd(), a.rigCmd(), a.beadCmd(), a.readyCmd(),
		a.depCmd(), a.groupCmd(), a.routeCmd(), a.flowCmd(), a.sessionCmd(), a.eventsCmd(), a.watchCmd(), a.primeCmd(),
		a.bdpCmd(), a.residentCmd(), a.inboxCmd(), a.mailCmd(), a.rememberCmd(), a.recallCmd(), a.hookCmd(),
		a.subscribeCmd(), a.unsubscribeCmd(), a.threadCmd(), a.reviewCmd(), a.storeCmd())
	err := root.ExecuteContext(context.Background())
	if a.st != nil {
		_ = a.st.Close(context.Background())
	}
	if errors.Is(err, errCheckFailed) {
		return 1 // the command has reported the failure itself
	}
	if err != nil {
		if a.json {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(os.Stderr, "yardr:", err)
		}
		if errors.Is(err, store.ErrNotFound) {
			return 3
		}
		return 1
	}
	return 0
}

// errCheckFailed ends a command that has already printed its failing result
// (a red merge gate): exit 1 without a second message.
var errCheckFailed = errors.New("check failed")

func (a *app) who() string {
	if a.actor != "" {
		return a.actor
	}
	return config.Actor()
}

// expect is what a move made from this process expects of the bead: that
// the session yardr started it for ($YARDR_SESSION) still owns it, and that
// it is still at rev (--revision) when one is given. An agent kept or
// orphaned after its bead moved on is then refused rather than ending the
// session that owns the bead now. Outside a session, and without rev, it
// expects nothing.
func (a *app) expect(rev string) store.Expect {
	return store.Expect{Session: config.Getenv("SESSION"), Revision: rev}
}

// store connects to the yardr server, starting it on first use.
func (a *app) store(ctx context.Context) (store.API, error) {
	if a.st != nil {
		return a.st, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	st, err := server.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a.st = st
	return st, nil
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
func parseSets(sets []string) (map[string]any, error) {
	m := map[string]any{}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q: want key=value", kv)
		}
		var js any
		if err := json.Unmarshal([]byte(v), &js); err == nil {
			m[k] = js
		} else {
			m[k] = v
		}
	}
	return m, nil
}
