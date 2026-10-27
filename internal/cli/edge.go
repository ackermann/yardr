package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/source"
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
senders post to https://<edge>/hooks/<account>/<yard>/<hook> and hands them
to the yard connected under that account and name, which still verifies each
one itself. A
connected yard dials out, so it needs no open port and no tunnel of its own.

  yardr edge connect <url>    connect this yard: fetch the edge's key, show it,
                              and enrol the yard by a code you approve in a
                              browser
  yardr edge status           what this yard is connected to
  yardr edge disconnect       leave the edge, and put back the address
                              webhooks were reached at before
  yardr edge mint-google <url> <source>
                              mint a Google access token through the edge,
                              as a google source's mint_command
  yardr edge setup-google <url>
                              set the hosted mint on the google sources the
                              edge's connection granted, and record the
                              provider's terms on your reading of them

Connecting sets the edge setting and hook_public_url (yardr settings).`}
	cmd.AddCommand(a.edgeConnectCmd(), a.edgeStatusCmd(), a.edgeDisconnectCmd(), a.edgeMintGoogleCmd(), a.edgeSetupGoogleCmd())
	return cmd
}

// edgeView is what yardr edge status prints, and what a connect ends with.
type edgeView struct {
	Connected bool   `json:"connected"`
	URL       string `json:"url,omitempty"`
	// Key is the edge's key as this yard pinned it, Handle the account's at
	// the edge, and Name this yard's among its yards.
	Key    string `json:"key,omitempty"`
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
	// HooksURL is where the edge takes this yard's webhooks, a hook's name
	// after it. HookPublicURL is the setting as it is now, which is HooksURL
	// unless it was changed after connecting, and Replaced the one connecting
	// replaced, which disconnecting puts back.
	HooksURL      string `json:"hooks_url,omitempty"`
	HookPublicURL string `json:"hook_public_url,omitempty"`
	Replaced      string `json:"replaced_hook_public_url,omitempty"`
	// ApprovedBy is who approved the yard at the edge, said when connecting.
	ApprovedBy string `json:"approved_by,omitempty"`
	// Link is the link as the running server last said it is, said by
	// status; absent while it has said nothing.
	Link *edgelink.State `json:"link,omitempty"`
}

// edgeLinkLine is the yard's link to its edge in one line, as the running
// server last said it is (s, when known): yardr status, yardr doctor and
// yardr edge status show it beside the listener's. yardr is the command as
// it is typed for this home.
func edgeLinkLine(s edgelink.State, known bool, yardr string) string {
	switch {
	case !known:
		return "the server has not said how its link is: it dials the edge while it runs with the edge set"
	case s.Up:
		line := "up since " + since(s.Since)
		if n := s.Reconnects(); n > 0 {
			line += fmt.Sprintf(" (%s since the server started)", plural(n, "reconnect"))
		}
		return line
	}
	line := "down since " + since(s.Since) + ": " + s.Reason
	if s.Detail != "" {
		line += " (" + s.Detail + ")"
	}
	switch s.Reason {
	case edgelink.DownRefusedKey:
		return line + ". If the edge's key changed on purpose, " + yardr + " edge disconnect and connect again"
	case edgelink.DownRefused:
		return line + ". The edge does not take this yard's link: is the yard still connected at the edge? " + yardr + " edge disconnect and connect again"
	}
	return line + "; the server dials again on its own"
}

func viewOf(e store.Edge, settings map[string]string) edgeView {
	return edgeView{Connected: true, URL: e.URL, Key: e.Key, Handle: e.Handle, Name: e.Name, HooksURL: e.HooksURL(),
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
	var as string
	cmd := &cobra.Command{
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

Then the yard enrols. It sends its public key and the name it proposes for
itself at the edge, signed to the edge's key, and gets a code back. Open
<url>/connect, sign in to your account there and enter the code. The page
shows this yard's proposed name and the start of its key before you approve
it, and you may change the name there; what the edge answers back is kept.
You type and paste nothing on this machine: the yard's key is its credential,
and every answer from the edge is checked against the pinned key. The
proposal is the yard's own name, or --as <name> to choose another, in lower
case letters, digits and '-'.

Connecting sets the edge setting, and sets hook_public_url to
<url>/hooks/<account>/<yard>, where the edge takes this yard's webhooks, so
hooks print and register the right URL. A hook_public_url you had set is kept
inside the edge setting, and yardr edge disconnect puts it back.

A yard connects to one edge at a time: run yardr edge disconnect before
connecting again. Only the operator connects a yard. With --json the command
prints two documents: the keys and the code at once, the result at the end.`,
		Example: `  yardr edge connect https://my.yardr.dev
  yardr edge connect https://my.yardr.dev --as airy`,
		RunE: func(c *cobra.Command, args []string) error {
			return a.edgeConnect(c, args[0], as)
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "the name this yard proposes for itself at the edge (default: the yard's own name)")
	return cmd
}

func (a *app) edgeConnect(c *cobra.Command, address, as string) error {
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
	if err := st.May(ctx, a.who(), store.ActConnectEdge, store.Object{Kind: store.KindYard}); err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if was, connected, err := edgeOf(settings); err != nil {
		return err
	} else if connected {
		return fmt.Errorf("this yard is already connected to %s as %s/%s: run yardr edge disconnect first. "+
			"The key it pinned stays the edge's until then; only a new connection pins another", was.URL, was.Handle, was.Name)
	}
	// The name the yard proposes for itself at the edge: its own, or the
	// operator's choice. The edge takes only its own rule, which the yard's
	// own name may not meet.
	asWasSet := c.Flags().Changed("as")
	name := runner.YardName(cfg.Home, settings)
	if asWasSet {
		name = as
	}
	if err := store.CheckEdgeName(name); err != nil {
		if !asWasSet {
			return fmt.Errorf("%w; this yard enrols under another name with --as <name>", err)
		}
		return fmt.Errorf("--as %w", err)
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
	// it: a handle or name that is no path segment would make the hook
	// address one that points somewhere else.
	edge := store.Edge{URL: addr, Key: edgeKey, Handle: en.Handle, Name: en.Name}
	if err := store.CheckEdge(edge.String()); err != nil {
		return fmt.Errorf("the edge approved this yard, and what it answered cannot be kept: %w; nothing was written", err)
	}
	by := ""
	if en.Email != "" {
		by = "by " + en.Email + " "
	}
	a.progress(w, warn, "… approved %sas %s/%s", by, en.Handle, en.Name)

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
	// The server follows the setting and dials at once; whether the link
	// came up is its to say, after this command has ended.
	a.progress(w, warn, "The server opens the link to the edge now: yardr edge status says when it is up.")
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
pinned, the account's handle and this yard's name at the edge, and where
webhooks reach it through the edge. It reads what yardr edge connect wrote
and does not call the edge.`,
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
			cfg, err := a.config()
			if err != nil {
				return err
			}
			view := viewOf(e, settings)
			if s, known := edgelink.ReadState(cfg); known {
				view.Link = &s
			}
			return a.out(c.OutOrStdout(), view, func(w io.Writer) {
				fmt.Fprintf(w, "edge      %s  (key %s, as %s/%s)\n", view.URL, keyLabel(view.Key), view.Handle, view.Name)
				s, known := edgelink.State{}, view.Link != nil
				if known {
					s = *view.Link
				}
				fmt.Fprintf(w, "link      %s\n", edgeLinkLine(s, known, yardrAt(cfg.Home)))
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
			if err := st.May(ctx, a.who(), store.ActDisconnectEdge, store.Object{Kind: store.KindYard}); err != nil {
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

// googleMintSources are the sources yardr edge mint-google mints for, with
// the Google scopes each needs from the connection's granted set (design
// yardr-hosted-4dx0 3.2): a source whose scope was not granted refuses at
// its mint, before any token is printed.
var googleMintSources = []string{"google-drive", "google-docs", "google-slides", "google-calendar"}

var googleMintScopes = map[string][]string{
	"google-drive":    {"https://www.googleapis.com/auth/drive.file"},
	"google-docs":     {"https://www.googleapis.com/auth/documents"},
	"google-slides":   {"https://www.googleapis.com/auth/presentations"},
	"google-calendar": {"https://www.googleapis.com/auth/calendar.events", "https://www.googleapis.com/auth/calendar.calendarlist.readonly"},
}

func (a *app) edgeMintGoogleCmd() *cobra.Command {
	return &cobra.Command{
		Use: "mint-google <edge-url> <source>", Short: "Mint a Google access token through the edge, as a source's mint_command",
		Long: `Mints a Google access token through the edge, for a google source whose
mint_command names this command: the hosted mint, beside the gws login the
pack's own google-mint reads.

It reads {inputs, scope} on stdin, as every mint_command does: the scope is
the call's, and is read and ignored. GOOGLE_ACCOUNT in the inputs is the
Google account to mint for. <source> is the source it mints for, one of
google-drive, google-docs, google-slides or google-calendar.

The call is signed with this yard's key to the edge's: the key this yard
pinned where its edge setting names <edge-url>, else the key fetched over
HTTPS as yardr edge connect fetches it. The edge's answer is believed only
signed with that key. Its granted scopes are checked against what <source>
needs before anything is printed: a source whose scope the connection was
not granted refuses, naming the missing scope and where to reconnect, and
the token in that answer is discarded.

On success it prints {token, expires_at} on stdout, for the core to call
with. Every refusal goes to stderr, and the run fails.`,
		Args:    cobra.ExactArgs(2),
		Example: `  yardr source set google-drive mint_command="yardr edge mint-google https://my.yardr.dev google-drive"`,
		RunE: func(c *cobra.Command, args []string) error {
			return a.edgeMintGoogle(c, args[0], args[1])
		},
	}
}

func (a *app) edgeMintGoogle(c *cobra.Command, address, source string) error {
	ctx := c.Context()
	w, warn := c.OutOrStdout(), c.ErrOrStderr()
	// A mint's refusals are said on its error stream, and the run fails:
	// stdout holds the mint's answer alone, on success alone.
	refuse := func(format string, args ...any) error {
		fmt.Fprintf(warn, format+"\n", args...)
		return errCheckFailed
	}
	addr := strings.TrimRight(address, "/")
	if err := store.CheckEdgeURL(addr); err != nil {
		return refuse("%s", err)
	}
	if _, ok := googleMintScopes[source]; !ok {
		return refuse("unknown source %q: one of %s", source, strings.Join(googleMintSources, ", "))
	}
	raw, err := io.ReadAll(c.InOrStdin())
	if err != nil {
		return refuse("%s", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return refuse("reads {inputs, scope} on stdin, and got nothing")
	}
	var in struct {
		Inputs map[string]string `json:"inputs"`
		Scope  *string           `json:"scope"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return refuse("reads {inputs, scope} on stdin, and got no JSON object")
	}
	if in.Scope == nil {
		return refuse("reads {inputs, scope} on stdin, and got no scope")
	}
	account := in.Inputs["GOOGLE_ACCOUNT"]
	if account == "" {
		return refuse("its inputs name no GOOGLE_ACCOUNT")
	}
	cfg, err := a.config()
	if err != nil {
		return refuse("%s", err)
	}
	st, err := a.store(ctx)
	if err != nil {
		return refuse("%s", err)
	}
	mint, err := a.mintGoogleThrough(ctx, addr, account, st, cfg)
	if err != nil {
		return refuse("%s", err)
	}
	if missing := missingGoogleScopes(source, mint.GrantedScopes); len(missing) > 0 {
		return refuse("%s needs %s, missing from the Google connection's grant: reconnect at %s/google", source, strings.Join(missing, ", "), addr)
	}
	answer, err := json.Marshal(struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}{mint.Token, mint.ExpiresAt})
	if err != nil {
		return refuse("%s", err)
	}
	fmt.Fprintln(w, string(answer))
	return nil
}

// missingGoogleScopes are the scopes source needs that granted does not
// hold, in the order googleMintScopes names them.
func missingGoogleScopes(source string, granted []string) []string {
	var missing []string
	for _, need := range googleMintScopes[source] {
		if !slices.Contains(granted, need) {
			missing = append(missing, need)
		}
	}
	return missing
}

// mintGoogleThrough makes one signed mint call for account at the edge
// addr, as mint-google and the hosted setup's probe both make it: the
// yard's key loaded from peer.key, and the edge's key the one this yard
// pinned where its edge setting names addr, else the one a connect
// would fetch. The answer's token is the caller's to discard or print;
// nothing here checks its scopes.
func (a *app) mintGoogleThrough(ctx context.Context, addr, account string, st store.API, cfg *config.Config) (*edgelink.GoogleMint, error) {
	yard, err := yardkey.Load(cfg.PeerKey())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("this yard has no key, so it can sign nothing: connect it first (%s edge connect <url> makes the pair)", yardrAt(cfg.Home))
		}
		return nil, err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	client := &edgelink.Client{URL: addr, HTTP: a.edgeHTTP}
	// The key this yard pinned, where its edge setting names this edge;
	// else the key a connect would fetch. A pin is never replaced by a
	// later fetch.
	edgeKey := ""
	if e, connected, perr := store.EdgeOf(settings); perr == nil && connected && e.URL == addr {
		edgeKey = e.Key
	}
	if edgeKey == "" {
		if edgeKey, err = client.Key(ctx); err != nil {
			return nil, err
		}
	}
	return client.MintGoogle(ctx, edgeKey, yard, account)
}

// googleSetupSource is one source's outcome of a hosted setup.
type googleSetupSource struct {
	Source string `json:"source"`
	// Action is set, kept or skipped.
	Action string `json:"action"`
	Mint   string `json:"mint,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// googleSetup is what yardr edge setup-google did.
type googleSetup struct {
	Edge          string               `json:"edge"`
	Sources       []googleSetupSource  `json:"sources"`
	ProviderTerms *store.ProviderTerms `json:"provider_terms,omitempty"`
	TermsNote     string               `json:"terms_note,omitempty"`
}

func (a *app) edgeSetupGoogleCmd() *cobra.Command {
	var termsRead, bars bool
	var termsURL, readOn string
	cmd := &cobra.Command{
		Use: "setup-google <edge-url>", Short: "Set the hosted mint on the granted google sources, and record the provider's terms",
		Long: `Sets the hosted mint on this yard's google sources, for a yard whose
Google is connected at the edge: each of google-drive, google-docs,
google-slides and google-calendar is probed once through the edge, and
only a source whose scopes the connection was granted gets
mint_command="yardr edge mint-google <edge-url> <source>". A source the
probe refuses keeps the pack's google-mint, and its token is discarded.
google-mail is untouched: it mints locally in this slice. Clearing a
source's mint_command (yardr source set <source> mint_command=) is the
way back to the pack's mint. Only the operator sets a mint, as only the
operator sets any source's.

The same setup records what you read of your model provider's terms.
It names the provider of the yard's harness kind (default_kind) and
asks you to read that provider's terms page. With --terms-read you say
you read it: --terms-url names the page you relied on, --read-on the
date (today by default), and --bars-general-training says those terms
bar creating, training or improving a general model from the data.
Without --terms-read no record is made.`,
		Example: `  yardr edge setup-google https://my.yardr.dev --terms-read --terms-url https://provider.example/terms --bars-general-training`,
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.edgeSetupGoogle(c, args[0], termsRead, termsURL, readOn, bars)
		},
	}
	cmd.Flags().BoolVar(&termsRead, "terms-read", false, "say you read the provider's terms page, and record it")
	cmd.Flags().StringVar(&termsURL, "terms-url", "", "the provider's terms page you read and relied on (with --terms-read)")
	cmd.Flags().StringVar(&readOn, "read-on", "", "the date you read it, YYYY-MM-DD (with --terms-read; today by default)")
	cmd.Flags().BoolVar(&bars, "bars-general-training", false, "whether the terms bar creating, training or improving a general model from the data; pass true or false (with --terms-read)")
	return cmd
}

func (a *app) edgeSetupGoogle(c *cobra.Command, address string, termsRead bool, termsURL, readOn string, bars bool) error {
	ctx := c.Context()
	addr := strings.TrimRight(address, "/")
	if err := store.CheckEdgeURL(addr); err != nil {
		return err
	}
	if termsRead && termsURL == "" {
		return errors.New("--terms-read names the page relied on: --terms-url <the provider's terms page>")
	}
	if termsRead && !c.Flags().Changed("bars-general-training") {
		return errors.New("--terms-read records whether the terms bar general training: pass --bars-general-training=true or --bars-general-training=false")
	}
	if !termsRead && (termsURL != "" || readOn != "" || c.Flags().Changed("bars-general-training")) {
		return errors.New("--terms-url, --read-on and --bars-general-training say what a reading records: they go with --terms-read")
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	cfg, err := a.config()
	if err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	kind := settings[store.DefaultKindSetting]
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider := ""
	if h, ok := agents.Lookup(kind); ok {
		provider = h.Provider
	}
	var terms *store.ProviderTerms
	var termsValue string
	if termsRead {
		if provider == "" {
			return fmt.Errorf("the harness kind %s names no provider: no provider-terms record is made", kind)
		}
		if readOn == "" {
			readOn = time.Now().Format("2006-01-02")
		}
		rec := store.ProviderTerms{Provider: provider, TermsURL: termsURL, ReadOn: readOn, BarsGeneralTraining: bars}
		termsValue, err = store.WithProviderTerms(settings, rec)
		if err != nil {
			return err
		}
		terms = &rec
	}
	// Check every source and setting this command may write before a
	// probe signs a call with the yard's key. A policy may decide by
	// source name, so permission to change one is not permission to probe
	// the rest.
	for _, name := range googleMintSources {
		if _, err := st.GetSource(ctx, name); errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return fmt.Errorf("source %s: %w", name, err)
		}
		if err := st.May(ctx, a.who(), store.ActSetSourceKey, store.Object{Kind: store.KindSource, Name: name, Ask: store.Alone}); err != nil {
			return err
		}
	}
	if terms != nil {
		if err := st.May(ctx, a.who(), store.ActChangeSetting, store.Object{Kind: store.KindSetting, Name: store.ProviderTermsSetting, Ask: store.Alone}); err != nil {
			return err
		}
	}
	out := googleSetup{Edge: addr, Sources: []googleSetupSource{}}
	for _, name := range googleMintSources {
		source, err := a.setupGoogleSource(ctx, st, cfg, addr, name)
		if err != nil {
			return err
		}
		out.Sources = append(out.Sources, source)
	}
	if _, err := st.GetSource(ctx, "google-mail"); err == nil {
		out.Sources = append(out.Sources, googleSetupSource{Source: "google-mail", Action: "skipped", Reason: "mints locally in this slice, and is left as it is"})
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("source google-mail: %w", err)
	}
	// The provider of the yard's harness kind, and the record of its
	// terms: shown and asked here, recorded only on the operator's
	// reading, since no code in the yard can read terms and judge them.
	switch {
	case provider == "":
		out.TermsNote = fmt.Sprintf("the harness kind %s names no provider, so no provider-terms record is made", kind)
	case !termsRead:
		out.TermsNote = fmt.Sprintf("read %s's terms page, then run this again with --terms-read --terms-url <the page you relied on> --bars-general-training=<true|false>: no provider-terms record is made until you do", provider)
	default:
		if err := st.SetSetting(ctx, store.ProviderTermsSetting, termsValue, a.who()); err != nil {
			return err
		}
		out.ProviderTerms = terms
	}
	return a.out(c.OutOrStdout(), out, func(w io.Writer) {
		for _, s := range out.Sources {
			switch s.Action {
			case "set":
				fmt.Fprintf(w, "source %s: mint set to %s\n", s.Source, s.Mint)
			case "kept":
				fmt.Fprintf(w, "source %s: kept the pack's mint (%s)\n", s.Source, s.Reason)
			default:
				fmt.Fprintf(w, "source %s: skipped (%s)\n", s.Source, s.Reason)
			}
		}
		if out.ProviderTerms != nil {
			r := out.ProviderTerms
			bars := "do not bar"
			if r.BarsGeneralTraining {
				bars = "bar"
			}
			fmt.Fprintf(w, "provider terms: %s, read at %s on %s; they %s creating, training or improving a general model from the data\n", r.Provider, r.TermsURL, r.ReadOn, bars)
		} else if out.TermsNote != "" {
			fmt.Fprintf(w, "provider terms: %s\n", out.TermsNote)
		}
	})
}

// setupGoogleSource probes one google source through the edge and sets
// its hosted mint only where the probe answers with its scopes granted:
// any refusal keeps the pack's mint, and the probe's token is discarded
// with the probe. The setting is verified back as the source's
// effective mint before it is said to stand.
func (a *app) setupGoogleSource(ctx context.Context, st store.API, cfg *config.Config, addr, name string) (googleSetupSource, error) {
	src, err := st.GetSource(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return googleSetupSource{Source: name, Action: "skipped", Reason: "not in this yard"}, nil
		}
		return googleSetupSource{}, fmt.Errorf("source %s: %w", name, err)
	}
	file := src.EnvFile
	if file == "" {
		file = source.EnvFile(cfg.Home, name)
	}
	f, err := config.ReadEnvFile(file)
	account := ""
	if err == nil {
		account = source.Lookup(f.Env, "GOOGLE_ACCOUNT")
	}
	if account == "" {
		return googleSetupSource{Source: name, Action: "kept", Reason: "its account GOOGLE_ACCOUNT is not set, so there is nothing to probe with"}, nil
	}
	mint, err := a.mintGoogleThrough(ctx, addr, account, st, cfg)
	if err != nil {
		return googleSetupSource{Source: name, Action: "kept", Reason: err.Error()}, nil
	}
	if missing := missingGoogleScopes(name, mint.GrantedScopes); len(missing) > 0 {
		return googleSetupSource{Source: name, Action: "kept", Reason: fmt.Sprintf("needs %s, missing from the Google connection's grant: reconnect at %s/google", strings.Join(missing, ", "), addr)}, nil
	}
	command := "yardr edge mint-google " + addr + " " + name
	if _, err := st.SetSource(ctx, name, store.SourceMintCommand, command, a.who()); err != nil {
		return googleSetupSource{}, fmt.Errorf("source %s: set hosted mint: %w", name, err)
	}
	after, err := st.GetSource(ctx, name)
	if err != nil {
		return googleSetupSource{}, fmt.Errorf("source %s: read back hosted mint: %w", name, err)
	}
	if strings.Join(after.Mint, " ") != command {
		return googleSetupSource{}, fmt.Errorf("source %s: the hosted mint does not stand as the source's mint after setting it", name)
	}
	return googleSetupSource{Source: name, Action: "set", Mint: command}, nil
}
