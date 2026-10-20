package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// productionEdge is the edge yardr runs, and publishedKeyAt the page outside
// its hosting where its key is also published, so that a person who does not
// take a certificate's word for the key can compare it.
const (
	productionEdge = "https://my.yardr.dev"
	publishedKeyAt = "https://yardr.dev/edge"
)

// keyLabel is a public key as a person is shown it.
func keyLabel(key string) string { return "ed25519:" + key }

func (a *app) edgeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "edge", Short: "The edge this yard is connected to, which takes its webhooks with no open port",
		Long: `The edge is a service that stands in front of yards. It takes the webhooks
senders post to https://<edge>/hooks/<handle>/<hook> and hands them to the
yard connected under that handle, which still verifies each one itself. A
connected yard dials out, so it needs no open port and no tunnel of its own.

  yardr edge connect <url>    connect this yard: fetch the edge's key, show it,
                              and enrol the yard by a code you approve in a
                              browser
  yardr edge status           what this yard is connected to
  yardr edge disconnect       leave the edge, and put back the address
                              webhooks were reached at before

Connecting sets the edge setting and hook_public_url (yardr settings).`}
	cmd.AddCommand(a.edgeConnectCmd(), a.edgeStatusCmd(), a.edgeDisconnectCmd())
	return cmd
}

// edgeView is what yardr edge status prints, and what a connect ends with.
type edgeView struct {
	Connected bool   `json:"connected"`
	URL       string `json:"url,omitempty"`
	// Key is the edge's key as this yard pinned it, and Handle what the edge
	// calls this yard.
	Key    string `json:"key,omitempty"`
	Handle string `json:"handle,omitempty"`
	// HooksURL is where the edge takes this yard's webhooks, a hook's name
	// after it. HookPublicURL is the setting as it is now, which is HooksURL
	// unless it was changed after connecting, and Replaced the one connecting
	// replaced, which disconnecting puts back.
	HooksURL      string `json:"hooks_url,omitempty"`
	HookPublicURL string `json:"hook_public_url,omitempty"`
	Replaced      string `json:"replaced_hook_public_url,omitempty"`
	// ApprovedBy is who approved the yard at the edge, said when connecting.
	ApprovedBy string `json:"approved_by,omitempty"`
}

func viewOf(e store.Edge, settings map[string]string) edgeView {
	return edgeView{Connected: true, URL: e.URL, Key: e.Key, Handle: e.Handle, HooksURL: e.HooksURL(),
		HookPublicURL: settings[store.HookPublicURLSetting], Replaced: e.HookPublicURL}
}

// edgeOf is the edge the yard is connected to, or a refusal that says so.
// An edge setting this build cannot read is no connection, and is told with
// how it is cleared.
func edgeOf(settings map[string]string) (store.Edge, bool, error) {
	e, connected, err := store.EdgeOf(settings)
	if err != nil {
		return store.Edge{}, false, fmt.Errorf("%w; yardr edge disconnect clears it", err)
	}
	return e, connected, nil
}

func (a *app) edgeConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use: "connect <url>", Short: "Connect this yard to an edge: fetch its key, show it, enrol by a code", Args: cobra.ExactArgs(1),
		Long: `Connects this yard to the edge at <url>, such as https://my.yardr.dev, which
then takes this yard's webhooks and hands them to it.

The first step is trust. The command fetches the edge's key over HTTPS (plain
http only to this machine, for a stand-in), prints it and pins it. Whoever
holds a valid certificate for the address at that moment is taken for the
edge. After that the pin holds on its own: a different certificate, a
different machine behind the name, or a different key served later moves
nothing. To check the key, compare it with the one published at
` + publishedKeyAt + `.

Then the yard enrols. It sends its public key and its name, signed to the
edge's key, and gets a code back. Open <url>/connect, sign in to your account
there and enter the code. The page shows this yard's name and the start of
its key before you approve it. You type and paste nothing on this machine:
the yard's key is its credential, and every answer from the edge is checked
against the pinned key.

Connecting sets the edge setting, and sets hook_public_url to
<url>/hooks/<handle>, where the edge takes this yard's webhooks, so hooks
print and register the right URL. A hook_public_url you had set is kept
inside the edge setting, and yardr edge disconnect puts it back.

A yard connects to one edge at a time: run yardr edge disconnect before
connecting again. Only the operator connects a yard. With --json the command
prints two documents: the keys and the code at once, the result at the end.`,
		Example: `  yardr edge connect https://my.yardr.dev`,
		RunE: func(c *cobra.Command, args []string) error {
			return a.edgeConnect(c, args[0])
		},
	}
}

func (a *app) edgeConnect(c *cobra.Command, address string) error {
	ctx := c.Context()
	w, warn := c.OutOrStdout(), c.ErrOrStderr()
	addr := strings.TrimRight(address, "/")
	if err := store.CheckEdgeURL(addr); err != nil {
		return err
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	// Before anything is asked of the edge: its row is made when the person
	// approves, and a yard that may not write the setting would be left with
	// one it cannot use.
	if err := a.operatorOnly(ctx, st, "connect this yard to an edge"); err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if was, connected, err := edgeOf(settings); err != nil {
		return err
	} else if connected {
		return fmt.Errorf("this yard is already connected to %s as %s: run yardr edge disconnect first. "+
			"The key it pinned stays the edge's until then; only a new connection pins another", was.URL, was.Handle)
	}
	name := runner.YardName(cfg.Home, settings)
	if !yardkey.ValidName(name) {
		return fmt.Errorf("this yard's name %q is none the edge can be given: name it with %s settings set name <name>, in letters, digits, '.', '_', '-' and '/'",
			name, yardrAt(cfg.Home))
	}

	// Ctrl+C ends the wait for the approval: nothing is written until it
	// comes.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &edgelink.Client{URL: addr, HTTP: a.edgeHTTP, Poll: a.edgePoll}
	edgeKey, err := client.Key(ctx)
	if err != nil {
		return err
	}
	host := addr[strings.Index(addr, "://")+3:]
	how := "over HTTPS from " + host
	if strings.HasPrefix(addr, "http://") {
		how = "over plain http from this machine"
	}
	if addr == productionEdge {
		how += "; compare it with " + publishedKeyAt
	}
	a.progress(w, warn, "The edge's key: %s  (fetched %s)", keyLabel(edgeKey), how)
	yard, _, err := yardkey.Ensure(cfg.PeerKey())
	if err != nil {
		return err
	}
	a.progress(w, warn, "This yard's key: %s", keyLabel(yardkey.Public(yard)))

	en, err := client.Enrol(ctx, edgeKey, yard, name)
	if err != nil {
		return fmt.Errorf("%w: nothing was written", err)
	}
	if a.json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		first := map[string]any{"edge": addr, "edge_key": edgeKey, "yard_key": yardkey.Public(yard), "name": name}
		if en.Code != "" {
			first["code"], first["connect_url"] = en.Code, addr+"/connect"
			if !en.Expires.IsZero() {
				first["expires"] = en.Expires
			}
		}
		if err := enc.Encode(first); err != nil {
			return err
		}
	}
	if en.Code != "" {
		until := ""
		if left := time.Until(en.Expires).Round(time.Minute); left > 0 {
			until = "  (" + short(left) + ")"
		}
		a.progress(w, warn, "Open %s/connect and enter the code  %s%s", addr, en.Code, until)
		a.progress(w, warn, "Waiting for you to approve this yard there. Ctrl+C stops, and writes nothing.")
		if err := en.Wait(ctx); err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				return errors.New("stopped before the yard was approved: nothing was written")
			case errors.Is(err, edgelink.ErrNotApproved):
				return fmt.Errorf("nobody approved this yard within %s, and nothing was written. Run yardr edge connect again for another code", short(edgelink.DefaultWait))
			}
			return fmt.Errorf("%w: nothing was written", err)
		}
	}
	// What the edge answered is shown, and kept, only as the setting keeps
	// it: a handle that is no path segment would make the hook address one
	// that points somewhere else.
	edge := store.Edge{URL: addr, Key: edgeKey, Handle: en.Handle}
	if err := store.CheckEdge(edge.String()); err != nil {
		return fmt.Errorf("the edge approved this yard, and what it answered cannot be kept: %w; nothing was written", err)
	}
	by := ""
	if en.Email != "" {
		by = "by " + en.Email + " "
	}
	a.progress(w, warn, "… approved %sas %s", by, en.Handle)

	// The setting as it is now, not as it was before the wait: a hand that
	// set it meanwhile is the one connecting replaces. Keep it even when it
	// already names this edge, since disconnect must restore a hand-set value.
	if settings, err = st.Settings(ctx); err != nil {
		return err
	}
	edge.HookPublicURL = settings[store.HookPublicURLSetting]
	// The edge setting first: it holds the value the second write replaces,
	// so a stop between the two loses nothing.
	who := a.who()
	if err := st.SetSetting(ctx, store.EdgeSetting, edge.String(), who); err != nil {
		return err
	}
	if err := st.SetSetting(ctx, store.HookPublicURLSetting, edge.HooksURL(), who); err != nil {
		return fmt.Errorf("the edge setting is written, and hook_public_url is not (%w): yardr edge disconnect undoes it, or set hook_public_url to %s", err, edge.HooksURL())
	}
	a.progress(w, warn, "Connected: webhooks reach this yard at %s/<hook>", edge.HooksURL())
	a.progress(w, warn, "%s set to %s", store.HookPublicURLSetting, edge.HooksURL())
	if edge.HookPublicURL != "" {
		a.progress(w, warn, "%s was %s; restored on disconnect", store.HookPublicURLSetting, edge.HookPublicURL)
	}
	if !a.json {
		return nil
	}
	view := viewOf(edge, map[string]string{store.HookPublicURLSetting: edge.HooksURL()})
	view.ApprovedBy = en.Email
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(view)
}

func (a *app) edgeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Say which edge this yard is connected to", Args: cobra.NoArgs,
		Long: `Says which edge this yard is connected to: its address, the key this yard
pinned, the handle the edge gave it, and where webhooks reach it through the
edge. It reads what yardr edge connect wrote and does not call the edge.`,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			settings, err := st.Settings(ctx)
			if err != nil {
				return err
			}
			e, connected, err := edgeOf(settings)
			if err != nil {
				return err
			}
			if !connected {
				return a.out(c.OutOrStdout(), edgeView{}, func(w io.Writer) {
					fmt.Fprintf(w, "not connected to an edge: yardr edge connect <url>, such as %s\n", productionEdge)
				})
			}
			view := viewOf(e, settings)
			return a.out(c.OutOrStdout(), view, func(w io.Writer) {
				fmt.Fprintf(w, "edge      %s  (key %s, as handle %s)\n", view.URL, keyLabel(view.Key), view.Handle)
				fmt.Fprintf(w, "hooks     %s/<hook>\n", view.HooksURL)
				if view.HookPublicURL != view.HooksURL {
					now := "unset"
					if view.HookPublicURL != "" {
						now = view.HookPublicURL
					}
					fmt.Fprintf(w, "note      %s is %s, not the edge's address: hooks registered from it do not reach the edge\n", store.HookPublicURLSetting, now)
				}
				if view.Replaced != "" {
					fmt.Fprintf(w, "          %s was %s before connecting; yardr edge disconnect puts it back\n", store.HookPublicURLSetting, view.Replaced)
				}
			})
		},
	}
}

// dropRow asks the edge to drop its row for this yard, with the key the yard
// pinned, and says why not when it could not.
func (a *app) dropRow(ctx context.Context, cfg *config.Config, e store.Edge) string {
	yard, err := yardkey.Load(cfg.PeerKey())
	if err != nil {
		return err.Error()
	}
	client := &edgelink.Client{URL: e.URL, HTTP: a.edgeHTTP}
	if err := client.Drop(ctx, e.Key, yard); err != nil {
		return err.Error()
	}
	return ""
}

// edgeLeft is what yardr edge disconnect prints.
type edgeLeft struct {
	Connected bool `json:"connected"`
	// Was is what the yard was connected to.
	Was *store.Edge `json:"was,omitempty"`
	// EdgeTold is whether the edge was asked to drop its row for this yard
	// and did; EdgeError says why not.
	EdgeTold  bool   `json:"edge_told"`
	EdgeError string `json:"edge_error,omitempty"`
	// HookPublicURL is the setting now, and HookAction what was done to it:
	// restored, cleared, or left as it was when it had been changed since
	// connecting.
	HookPublicURL string `json:"hook_public_url,omitempty"`
	HookAction    string `json:"hook_public_url_action,omitempty"`
}

func (a *app) edgeDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use: "disconnect", Short: "Leave the edge, and put back the address webhooks were reached at before", Args: cobra.NoArgs,
		Long: `Disconnects this yard from the edge. It asks the edge to drop its row for
this yard, clears the edge setting, and puts hook_public_url back as it was
before connecting (or clears it, if it was not set). A hook_public_url you
changed after connecting stays as you left it.

The edge is asked first, and the settings are cleared whether or not it
answers. A yard that cannot reach the edge, or that the edge no longer knows,
is disconnected here all the same. The command then says the edge was not
told, so its operator can drop the row. Only the operator disconnects a yard.`,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if err := a.operatorOnly(ctx, st, "disconnect this yard from the edge"); err != nil {
				return err
			}
			settings, err := st.Settings(ctx)
			if err != nil {
				return err
			}
			w, who := c.OutOrStdout(), a.who()
			was, connected, perr := store.EdgeOf(settings)
			if !connected && perr == nil {
				return a.out(w, edgeLeft{}, func(w io.Writer) { fmt.Fprintln(w, "this yard is not connected to an edge") })
			}
			left := edgeLeft{}
			if connected {
				left.Was = &was
				left.EdgeError = a.dropRow(ctx, cfg, was)
				left.EdgeTold = left.EdgeError == ""
			} else {
				// What is stored is no connection this build reads, and
				// the edge setting is all there is to clear.
				left.EdgeError = "the edge setting cannot be read: " + perr.Error()
			}

			// hook_public_url first: the value it goes back to is in the
			// edge setting, which is cleared last, so a stop between the
			// two loses nothing.
			left.HookPublicURL = settings[store.HookPublicURLSetting]
			if connected && left.HookPublicURL == was.HooksURL() {
				left.HookAction, left.HookPublicURL = "cleared", ""
				if was.HookPublicURL != "" {
					left.HookAction, left.HookPublicURL = "restored", was.HookPublicURL
				}
				if err := st.SetSetting(ctx, store.HookPublicURLSetting, left.HookPublicURL, who); err != nil {
					return err
				}
			} else if left.HookPublicURL != "" {
				left.HookAction = "left"
			}
			if err := st.SetSetting(ctx, store.EdgeSetting, "", who); err != nil {
				return err
			}
			return a.out(w, left, func(w io.Writer) {
				where := "the edge"
				if left.Was != nil {
					where = left.Was.URL
				}
				switch {
				case left.EdgeTold:
					fmt.Fprintf(w, "%s was asked to drop its row for this yard\n", where)
				default:
					fmt.Fprintf(c.ErrOrStderr(), "%s was not told: %s. Its row for this yard stays until its operator drops it.\n", where, left.EdgeError)
				}
				switch left.HookAction {
				case "restored":
					fmt.Fprintf(w, "%s put back to %s\n", store.HookPublicURLSetting, left.HookPublicURL)
				case "cleared":
					fmt.Fprintf(w, "%s cleared: it was not set before connecting\n", store.HookPublicURLSetting)
				case "left":
					fmt.Fprintf(w, "%s is %s, which is not the address connecting set: left as it is\n", store.HookPublicURLSetting, left.HookPublicURL)
				}
				fmt.Fprintf(w, "disconnected from %s\n", where)
			})
		},
	}
}
