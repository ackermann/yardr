package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

func (a *app) webCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "web", Short: "Show the yard in a browser"}
	var addr string
	var unsafe bool
	var hosts []string
	var as string
	var pagesAddr, pagesHost string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a view of the yard over HTTP",
		Long: `Serve the yard as web pages: a feed of what happens, the flows of its
depots with the beads in each stage, every bead, what waits for a person, the
running sessions and the crew, live. Without --as, YARDR_WEB_AS or the
web_as setting the view reads the yard and changes nothing. Every page
answers a request with "Accept: application/json" with the data it shows,
and /events streams the yard's events as server-sent events.

It listens on --addr, else on YARDR_WEB_ADDR when set in the command's
environment, else on the yard's web_addr setting (yardr settings set
web_addr <host:port>), else on 127.0.0.1:8791.

By default it listens on loopback only: an address on any other interface
is refused unless --unsafe is given. A request is answered when its Host
header is a loopback name or a name given with --host, and 421 under any
other name.

--unsafe listens on every interface (0.0.0.0:8791, on the selected
address's port, or the --addr given, which may then be any address), to open the view from another machine on
the same network. Outside JWT mode there is still no authentication: anyone
who can reach this machine on that port can read the whole yard. In JWT mode
every request but /health must carry a token the gateway signed. A request is answered
when its Host header is an IP address, a loopback name or this machine's
host name (with or without .local), and 421 under any other name.

--host names a host the machine is reached by and cannot work out by
itself: its name in the LAN's DNS, on a tailnet, or a proxy's. It is
answered as the machine's own names are, in any case and with any port. It
takes one name or a comma list and may be repeated. A wildcard is refused.

Without --unsafe, --host leaves the view on loopback, where such a name
reaches it through a proxy of yours on this machine that forwards the Host
header unchanged. TLS, and who reaches the view, are that proxy's. The view
then answers to the loopback names and the names given, and to nothing it
was not given: not this machine's host name, and no IP address off
loopback. Name only names whose resolution you control: a page under a
name someone else controls could read the yard through a visitor's browser
(docs/web.md, "Behind a proxy").

Beside the view it serves the sandbox origin, where a note's pages run:
a second listener on loopback answering blob bytes only, under a policy
that permits inline scripts and blocks fetches, frames, workers and
popups. WebRTC blocking depends on browser support, and a page opened
top-level can navigate itself (docs/web.md). It listens on
--pages-addr, else on YARDR_WEB_PAGES_ADDR when set in the command's
environment, else on the yard's web_pages_addr setting (yardr settings
set web_pages_addr <host:port>), else on 127.0.0.1:8789, on loopback
like the view and under the same Host rules. A bead's page carries run
beside each page's static link, opening the same bytes there. Behind a
proxy of yours the origin is reached by the name the view was reached
by, on the origin's port, which the proxy forwards beside the view's;
--pages-host names a second name for it instead, which the proxy routes
to the origin and run links use (docs/web.md, "Behind a proxy").

The view acts as --as when it is given, else as YARDR_WEB_AS when set in
the command's environment, else as the yard's web_as setting (yardr
settings set web_as <user>), and reads only with none of them. Whoever it
acts as must be a user of the yard (yardr user list): with any other name
it refuses to start. The talk page then holds the user's conversation with
a crew member, live, with the form that sends it mail as them, and a bead's
page answers its asks to the user and sends the bead on where it waits for
them, as yardr review offers it. A form's POST is taken only from the
view's own pages, and every other route stays GET. Without a user the talk
page says the view does not act, and the view changes nothing.

Behind an auth gateway that forwards each request with a signed JWT proving
who the visitor is, the yard's web_jwt_issuer setting turns JWT mode on
(docs/web.md, "Behind an auth gateway"): every request but /health must
carry the gateway's token, and each request acts as the yard user its email
maps to — two visitors act as themselves on one view. --as, YARDR_WEB_AS
and web_as are then refused at start, and the start line and yardr status
say whose tokens the view verifies (jwt from <issuer>).

The open /events streams are limited: 16 from one address off loopback
(then 429) and 128 from all of them together (then 503), and 128 from
loopback, counted apart, so the network cannot take a stream from a page on
this machine. Both answers carry Retry-After.`,
		Example: `  yardr web serve                          # http://127.0.0.1:8791/
  yardr web serve --addr 127.0.0.1:9000
  yardr web serve --host yard.example.net  # on loopback, for a proxy that forwards that name
  yardr web serve --as mckean              # acts as mckean: talk, asks, moves
  yardr web serve --unsafe                 # http://<this machine's IP>:8791/
  yardr web serve --unsafe --addr 192.168.178.90:9000   # that interface only
  yardr web serve --unsafe --host yard.example.net      # http://yard.example.net:8791/ too
  curl -H 'Accept: application/json' localhost:8791/
  curl -N 'localhost:8791/events?format=json&after=0'   # every event, then live`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: a refused address must not start a server.
			given := c.Flags().Changed("addr")
			if err := web.CheckAddr("--addr", webAddr(addr, given, unsafe), unsafe); err != nil {
				return err
			}
			pagesGiven := c.Flags().Changed("pages-addr")
			if err := web.CheckAddr("--pages-addr", webAddr(pagesAddr, pagesGiven, unsafe), unsafe); err != nil {
				return err
			}
			if c.Flags().Changed("pages-host") {
				if _, err := store.CheckWebHost("--pages-host", pagesHost); err != nil {
					return err
				}
			}
			hosts, err := webHosts(hosts, c.Flags().Changed("host"), unsafe)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			// Who the view acts as: --as, else YARDR_WEB_AS, else the
			// yard's web_as setting, else no one, and the view reads the
			// yard and changes nothing. A start and an install hand on
			// --as only when it is given, so a view of theirs reads the
			// setting itself.
			asFrom := ""
			var all map[string]string
			if !c.Flags().Changed("as") {
				set, _ := store.SettingOf(store.WebAsSetting)
				if all, err = st.Settings(ctx); err != nil {
					return err
				}
				as, asFrom = set.Effective(all)
			}
			if as != "" {
				if _, err := st.GetUser(ctx, as); err != nil {
					return webAsRefused(as, asFrom)
				}
			}
			// JWT mode is on when the yard sets web_jwt_issuer: every
			// request but /health must then carry a token the gateway
			// signed, so no acting user stands beside it.
			if all == nil {
				if all, err = st.Settings(ctx); err != nil {
					return err
				}
			}
			jwtCfg, err := webJWTConfig(ctx, st, all, as, asFrom)
			if err != nil {
				return err
			}
			// The yard's web_addr, where no --addr was given: checked when
			// it was set, and by web.Listen below.
			if err := a.addrFromSetting(ctx, a.cfg, kindOf(svcWeb), c.Flags()); err != nil {
				return err
			}
			addr := webAddr(addr, given, unsafe)
			if err := web.CheckAddr("--addr", addr, unsafe); err != nil {
				return err
			}
			// The sandbox origin's address and public name, each flag
			// when given, else the environment, else the yard's
			// setting, else the default address and no name.
			if err := a.pagesFlagsFromSetting(ctx, a.cfg, c.Flags()); err != nil {
				return err
			}
			pagesAddr = webAddr(pagesAddr, pagesGiven, unsafe)
			if err := web.CheckAddr("--pages-addr", pagesAddr, unsafe); err != nil {
				return err
			}
			if pagesHost != "" {
				if checked, err := store.CheckWebHost("--pages-host", pagesHost); err != nil {
					return err
				} else {
					pagesHost = checked
				}
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			srv, err := web.NewServer(st, a.cfg.Home, log)
			if err != nil {
				return err
			}
			if as != "" {
				srv.ActAs(as)
			}
			if jwtCfg.Issuer != "" {
				if err := srv.UseJWT(ctx, jwtCfg); err != nil {
					return fmt.Errorf("%s %s refused: %w", store.WebJWTJWKSSetting, jwtCfg.JWKSURL, err)
				}
			}
			ln, err := web.Listen("--addr", addr, unsafe)
			if err != nil {
				return err
			}
			defer ln.Close()
			// The sandbox origin beside the view: a note's pages run
			// there, on an origin of their own. Both listen before
			// either serves, so a taken pages port fails the view
			// instead of serving it without its run links.
			pln, err := web.Listen("--pages-addr", pagesAddr, unsafe)
			if err != nil {
				return err
			}
			defer pln.Close()
			srv.SetPagesAddr(pln.Addr().String())
			srv.SetPagesHost(pagesHost)
			if unsafe {
				// A machine with no name is still reached by its IP.
				hostname, _ := os.Hostname()
				srv.AllowNetwork(hostname, hosts...)
				_, port, _ := net.SplitHostPort(ln.Addr().String())
				log.Warn(unsafeWarning(port, as, jwtCfg.Issuer))
			} else if len(hosts) > 0 {
				srv.AllowHosts(hosts...)
			}
			// Every request ends with ctx: an open event stream would
			// otherwise hold Shutdown until its time ran out.
			hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second,
				BaseContext: func(net.Listener) context.Context { return ctx }}
			ps := &http.Server{Handler: srv.PagesHandler(), ReadHeaderTimeout: 10 * time.Second,
				BaseContext: func(net.Listener) context.Context { return ctx }}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = hs.Shutdown(sctx)
				_ = ps.Shutdown(sctx)
			}()
			// For yardr status and yardr doctor, which name the views that
			// run. A view that cannot say so still serves.
			if clear, err := announceWeb(a.cfg, ln.Addr().String(), unsafe, as, jwtCfg.Issuer); err != nil {
				log.Warn("yardr status will not list this view: it could not be recorded", "err", err)
			} else {
				defer clear()
			}
			at := []any{"url", "http://" + ln.Addr().String() + "/",
				"pages", "http://" + pln.Addr().String() + "/"}
			if as != "" {
				at = append(at, "acts_as", as)
			} else if jwtCfg.Issuer != "" {
				at = append(at, "jwt_from", jwtCfg.Issuer)
			}
			if to := webAnswersTo(unsafe, srv.Names()); to != "" {
				at = append(at, "answers_to", to)
			}
			log.Info("serving the yard", at...)
			serveErr := make(chan error, 2)
			go func() { serveErr <- hs.Serve(ln) }()
			go func() { serveErr <- ps.Serve(pln) }()
			var first error
			for i := 0; i < 2; i++ {
				if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) && first == nil {
					first = err
					// The view serves nothing half up: one that
					// ends ends them both.
					stop()
				}
			}
			if first != nil {
				return first
			}
			return nil
		},
	}
	serve.Flags().StringVar(&addr, "addr", webLocalAddr, "listen address, on loopback (default: YARDR_WEB_ADDR, else web_addr, else "+webLocalAddr+"); every interface on that port with --unsafe")
	serve.Flags().BoolVar(&unsafe, "unsafe", false, "listen on every interface, or on any --addr; outside JWT mode there is no authentication")
	serve.Flags().StringSliceVar(&hosts, "host", nil, "a host name this machine is reached by, answered as its own are (repeatable)")
	serve.Flags().StringVar(&as, "as", "", "act as this user of the yard: talk, asks, moves (default: YARDR_WEB_AS, else web_as)")
	serve.Flags().StringVar(&pagesAddr, "pages-addr", webPagesLocalAddr, "listen address of the sandbox origin, on loopback (default: YARDR_WEB_PAGES_ADDR, else web_pages_addr, else "+webPagesLocalAddr+"); every interface on that port with --unsafe")
	serve.Flags().StringVar(&pagesHost, "pages-host", "", "public name of the sandbox origin behind a proxy of yours, for run links (default: YARDR_WEB_PAGES_HOST, else web_pages_host)")
	cmd.AddCommand(serve)
	v := a.longRunningCmds(cmd, serve, kindOf(svcWeb))
	for _, c := range []*cobra.Command{v.start, v.install} {
		c.Flags().Lookup("addr").Usage = "listen address, on loopback (default: YARDR_WEB_ADDR, else web_addr, else " + webLocalAddr + ")"
		c.Flags().Lookup("host").Usage = "a host name the view answers to besides the loopback names, for a proxy of yours in front of it (repeatable)"
		c.Flags().Lookup("as").Usage = "act as this user of the yard: talk, asks, moves (default: YARDR_WEB_AS, else web_as)"
		c.Flags().Lookup("pages-addr").Usage = "listen address of the sandbox origin, on loopback (default: YARDR_WEB_PAGES_ADDR, else web_pages_addr, else " + webPagesLocalAddr + ")"
		c.Flags().Lookup("pages-host").Usage = "public name of the sandbox origin behind a proxy of yours, for run links (default: YARDR_WEB_PAGES_HOST, else web_pages_host)"
	}
	v.start.Short = "Start the web view in the background, on loopback, and print its address"
	v.start.Long = `Start yardr web serve detached from this terminal, logging to
<home>/web.log, wait until it listens and print its address. It runs until
yardr web stop stops it or the machine restarts; yardr web install keeps one
running from login on instead.

It listens on loopback only: there is no --unsafe here. --host is handed on
to yardr web serve, and names what the view answers to on loopback besides
the loopback names (docs/web.md, "Behind a proxy"). --as is handed on too:
the view acts as that user, and refuses to start when no user of the yard
has the name; without --as it acts as YARDR_WEB_AS, else the yard's web_as
setting, and reads only with neither (docs/web.md, "Acting as a user").`
	v.start.Example = `  yardr web start                          # http://127.0.0.1:8791/
  yardr web start --host yard.example.net  # the same, for a proxy that forwards that name
  yardr web start --as mckean              # acts as mckean: talk, asks, moves`
	v.stop.Long = `Tell every web view that runs on this yard to stop, however it was started
(yardr web serve in a terminal, yardr web start, or the service that starts
when you log in), and wait
until it has: yardr status lists them. A yardr web start after it answers at
the same address.

A view run by the service that starts when you log in stays stopped too: it starts it
again at the next login, or when it ends with a failure, and a stop is none.`
	v.install.Short = "Keep the web view running from login on: a launchd agent (macOS) or a systemd user unit (Linux)"
	v.install.Long = `Write yardr web serve as a service that starts when you log in, and load it, as
yardr daemon install does for the daemon: the service manager starts the view
now and at every login. It prints the view's address and logs to
<home>/web.log.

A service listens on loopback only: --addr takes 127.0.0.1, ::1 or localhost
with a port, and there is no --unsafe here. --host is written into the
service and handed on to yardr web serve: the view answers to the names
given besides the loopback names, and still listens on loopback only. --as
is written into the service too: the view acts as that user. Without --as
the view acts as YARDR_WEB_AS from the install environment when set, else
the yard's web_as setting, which it reads at every start. To reach the
view from another machine, forward the port over ssh or put a proxy of
yours in front of it (docs/web.md, "Behind a proxy").

--file-only writes the file and loads nothing.`
	v.install.Example = `  yardr web install                       # http://127.0.0.1:8791/
  yardr web install --addr 127.0.0.1:9000
  yardr web install --host yard.example.net   # for a proxy that forwards that name
  yardr web install --host adventuretime.tail5cf2e8.ts.net --as mckean
  yardr web uninstall`
	return cmd
}

// webAsRefused is the refusal of the user the view would act as, from
// --as, YARDR_WEB_AS or the yard's web_as setting (from), when no user of
// the yard has the name.
func webAsRefused(as, from string) error {
	whose := "--as " + as
	switch from {
	case store.FromEnvironment:
		whose = "YARDR_WEB_AS " + as
	case store.FromSet:
		whose = "web_as " + as
	}
	return fmt.Errorf("%s refused: the yard has no user of that name (yardr user list)", whose)
}

// pagesFlagsFromSetting gives --pages-addr and --pages-host the
// environment override, else the yard's setting, when the flag was not
// given. It is the web kind's FromSetting: serve, start and install go
// by it, so a start and an install check and hand on what serve would
// listen on. The flags stay ones that were not given. The yard is asked
// only when a setting decides.
func (a *app) pagesFlagsFromSetting(ctx context.Context, cfg *config.Config, flags *pflag.FlagSet) error {
	givenAddr, givenHost := flags.Changed("pages-addr"), flags.Changed("pages-host")
	if givenAddr && givenHost {
		return nil
	}
	if cfg == nil {
		var err error
		if cfg, err = config.Load(); err != nil {
			return err
		}
		a.cfg = cfg
	}
	if !givenAddr && cfg.WebPagesAddr != "" {
		if err := flags.Lookup("pages-addr").Value.Set(cfg.WebPagesAddr); err != nil {
			return err
		}
		givenAddr = true
	}
	if !givenHost && cfg.WebPagesHost != "" {
		if err := flags.Lookup("pages-host").Value.Set(cfg.WebPagesHost); err != nil {
			return err
		}
		givenHost = true
	}
	if givenAddr && givenHost {
		return nil
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if !givenAddr {
		if v := all[store.WebPagesAddrSetting]; v != "" {
			if err := flags.Lookup("pages-addr").Value.Set(v); err != nil {
				return err
			}
		}
	}
	if !givenHost {
		if v := all[store.WebPagesHostSetting]; v != "" {
			if err := flags.Lookup("pages-host").Value.Set(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// webJWTConfig is JWT mode as the yard's settings shape it, when its
// web_jwt_issuer is set: without it the view is today's, and the config is
// empty. It refuses what JWT mode cannot start with: an acting user from
// --as, YARDR_WEB_AS or web_as (as, from), a missing audience or keys'
// URL, and a users table naming no user of the yard.
func webJWTConfig(ctx context.Context, st store.API, all map[string]string, as, from string) (web.JWTConfig, error) {
	var none web.JWTConfig
	issuer := all[store.WebJWTIssuerSetting]
	if issuer == "" {
		return none, nil
	}
	if as != "" {
		return none, webJWTAsRefused(as, from)
	}
	if all[store.WebJWTAudienceSetting] == "" {
		return none, webJWTMissing(store.WebJWTAudienceSetting, "the audience the gateway mints tokens for")
	}
	if all[store.WebJWTJWKSSetting] == "" {
		return none, webJWTMissing(store.WebJWTJWKSSetting, "the URL the keys are fetched from")
	}
	header, _ := store.SettingOf(store.WebJWTHeaderSetting)
	headerValue, _ := header.Effective(all)
	users := map[string]string{}
	rawUsers := all[store.WebJWTUsersSetting]
	if rawUsers == "" {
		return none, webJWTMissing(store.WebJWTUsersSetting, "the email-to-user table")
	}
	// The shape was checked when it was set; what is stored parses.
	var err error
	if users, err = store.ParseWebJWTUsers(rawUsers); err != nil {
		return none, fmt.Errorf("%s refused: %w", store.WebJWTUsersSetting, err)
	}
	for _, email := range slices.Sorted(maps.Keys(users)) {
		if _, err := st.GetUser(ctx, users[email]); err != nil {
			return none, fmt.Errorf("%s %s=%s refused: the yard has no user of that name (yardr user list)",
				store.WebJWTUsersSetting, email, users[email])
		}
	}
	return web.JWTConfig{Issuer: issuer, Audience: all[store.WebJWTAudienceSetting],
		JWKSURL: all[store.WebJWTJWKSSetting], Header: headerValue, Users: users}, nil
}

// webJWTAsRefused is the refusal of the user the view would act as, from
// --as, YARDR_WEB_AS or the yard's web_as setting (from), in JWT mode: no
// request is ever served as the default user, and no flag silently does
// nothing.
func webJWTAsRefused(as, from string) error {
	whose, unset := "--as "+as, "start without --as"
	switch from {
	case store.FromEnvironment:
		whose, unset = "YARDR_WEB_AS "+as, "unset YARDR_WEB_AS"
	case store.FromSet:
		whose, unset = "web_as "+as, `unset web_as (yardr settings set web_as "")`
	}
	return fmt.Errorf("%s refused: the yard sets %s, and a view in JWT mode takes no acting user (%s, or unset %s)",
		whose, store.WebJWTIssuerSetting, unset, store.WebJWTIssuerSetting)
}

// webJWTMissing is the refusal of JWT mode without one of its required
// settings: without an audience a token minted for another application
// would be accepted, and without a keys' URL no token verifies.
func webJWTMissing(setting, what string) error {
	return fmt.Errorf("JWT mode is on (the yard sets %s) and %s is unset: set %s (yardr settings set %s <value>)",
		store.WebJWTIssuerSetting, setting, what, setting)
}

// webAddr is the address web serve listens on: addr, which is the flag
// when it was given, else the yard's setting, else the loopback
// default. Under --unsafe an address that was not given is every
// interface, on the port of that one: the selected address's port.
func webAddr(addr string, given, unsafe bool) string {
	if unsafe && !given {
		if _, port, err := net.SplitHostPort(addr); err == nil {
			return net.JoinHostPort("0.0.0.0", port)
		}
		return addr
	}
	return addr
}

// webAnswersTo is what the start line says the view answers to, when that
// is more than the loopback names every view answers to: any IP address
// under --unsafe, and the names, which on loopback are the ones --host gave.
func webAnswersTo(unsafe bool, names []string) string {
	switch {
	case unsafe:
		return strings.Join(append([]string{"an IP address", "localhost"}, names...), ", ")
	case len(names) > 0:
		return strings.Join(append([]string{"a loopback address", "localhost"}, names...), ", ")
	}
	return ""
}

// unsafeWarning is what --unsafe says at start: who can reach the view on
// port, and what reaching it lets them do — read the yard, and act as its
// user where one stands. In JWT mode only /health is open without a token.
func unsafeWarning(port, as, issuer string) string {
	if issuer != "" {
		return "open to the network on port " + port + ": every request but /health needs a token signed by the gateway"
	}
	can := "can read the yard"
	if as != "" {
		can = "can read the yard and act as " + as
	}
	return "no authentication: anyone who can reach this machine on port " + port + " " + can
}

// webHosts is what --host gave, each name as the view compares it, or why
// one is refused. Without --unsafe an IP address off loopback is: the view
// is on loopback then and answers to names only, so the flag would say
// nothing, where its operator means the view to be reached at that address.
func webHosts(hosts []string, given, unsafe bool) ([]string, error) {
	if !given {
		return nil, nil
	}
	// An empty list is what `--host ""` leaves of itself.
	if len(hosts) == 0 {
		return nil, errors.New(`--host "" refused: it is no name`)
	}
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		name, err := web.CheckHost(strings.TrimSpace(h))
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(name); !unsafe && ip != nil && !ip.IsLoopback() {
			return nil, hostOffLoopback(name)
		}
		names = append(names, name)
	}
	return names, nil
}

// hostOffLoopback is the refusal of an IP address off loopback given with
// --host to a view on loopback, by the address.
type hostOffLoopback string

func (h hostOffLoopback) Error() string {
	return "--host " + string(h) + " refused: without --unsafe the view listens on loopback only and answers to no IP address off it. " +
		"Give --unsafe too to open the view to the network, where every IP address is answered"
}
