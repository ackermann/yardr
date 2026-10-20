package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"yardr.dev/yardr/internal/bdp"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func (a *app) bdpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bdp", Short: "Serve the yard over the Bead Protocol (BDP)"}
	var addr, scope string
	var unsafe bool
	var hosts []string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the BDP Read profile over HTTP",
		Long: `Serve the yard's beads and dependency links over the BDP Read profile.

The Scope URL is the canonical public address of this yard: every Bead and Link
id is minted under it, and it is what BDP clients use. It may differ from the
listen address (behind a proxy, or a fixed name for tests).

It listens on --addr, else on YARDR_BDP_ADDR when set in the command's
environment, else on the yard's bdp_addr setting (yardr settings set
bdp_addr <host:port>), else on 127.0.0.1:8765.

There is no authentication, so by default it listens on loopback only: an
address on any other interface is refused unless --unsafe is given. A request
is answered when its Host header is a loopback name, the Scope URL's host or a
name given with --host, and 421 under any other name.

--unsafe listens on every interface (0.0.0.0:8765, on the selected address's
port, or the --addr given, which may then be any address), to read the yard
from another machine on the same network. There is still no authentication:
anyone who can reach this machine on that port can read the whole yard. A
request is answered when its Host header is an IP address, a loopback name, the
Scope URL's host, this machine's host name (with or without .local) or a name
given with --host, and 421 under any other name. The Scope URL it mints ids
under is not widened with the address: give --scope the name clients reach it
by.

--host names a host the machine is reached by and cannot work out by itself:
its name in the LAN's DNS, on a tailnet, or a proxy's. It is answered as the
machine's own names are, in any case and with any port. It takes one name or a
comma list and may be repeated. A wildcard is refused. Without --unsafe, --host
leaves the server on loopback, where such a name reaches it through a proxy of
yours on this machine that forwards the Host header unchanged. Name only names
whose resolution you control: a page under a name someone else controls could
read the yard through a visitor's browser.`,
		Example: `  yardr bdp serve                                  # http://127.0.0.1:8765/yard/
  yardr bdp serve --scope https://beads.example/team/ --host proxy.lan   # on loopback, for a proxy that forwards that name
  yardr bdp serve --unsafe --scope https://beads.example/team/          # every interface, port 8765`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: a refused address must not start a server.
			given := c.Flags().Changed("addr")
			if err := bdp.CheckAddr("--addr", webAddr(addr, given, unsafe), unsafe); err != nil {
				return err
			}
			hosts, err := bdpHosts(hosts, c.Flags().Changed("host"), unsafe)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			// The yard's bdp_addr, where no --addr was given: the setting
			// takes any host:port, so the address it gives is checked here
			// as the flag's was, and by Listen below.
			if err := a.addrFromSetting(ctx, a.cfg, kindOf(svcBDP), c.Flags()); err != nil {
				return err
			}
			// The default Scope names the address that was selected, not
			// the interfaces --unsafe widens it to.
			if scope == "" {
				scope = "http://" + addr + "/yard/"
			}
			addr := webAddr(addr, given, unsafe)
			if err := bdp.CheckAddr("--addr", addr, unsafe); err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			sources := hooks.LinkSources()
			srv, err := bdp.NewServer(scope, sources, bdp.CachedLoader(scope, sources, st), bdp.DefaultLimits, log)
			if err != nil {
				return err
			}
			hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = hs.Shutdown(sctx)
			}()
			ln, err := bdp.Listen("--addr", addr, unsafe)
			if err != nil {
				return err
			}
			if unsafe {
				// A machine with no name is still reached by its IP.
				hostname, _ := os.Hostname()
				srv.AllowNetwork(hostname, hosts...)
				_, port, _ := net.SplitHostPort(ln.Addr().String())
				log.Warn("no authentication: anyone who can reach this machine on port " + port + " can read the yard")
			} else if len(hosts) > 0 {
				srv.AllowHosts(hosts...)
			}
			defer kindOf(svcBDP).announced(a.cfg, log, map[string]any{"addr": ln.Addr().String(), "scope": scope})()
			log.Info("serving BDP Read", "addr", addr, "scope", scope)
			if err := hs.Serve(bdp.BodylessTransportErrors(ln)); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	serve.Flags().StringVar(&addr, "addr", store.DefaultBDPAddr, "listen address, on loopback (default: YARDR_BDP_ADDR, else bdp_addr, else "+store.DefaultBDPAddr+"); every interface on that port with --unsafe")
	serve.Flags().StringVar(&scope, "scope", "", "canonical Scope URL, ending in / (default http://<addr>/yard/)")
	serve.Flags().BoolVar(&unsafe, "unsafe", false, "listen on every interface, or on any --addr; there is no authentication")
	serve.Flags().StringSliceVar(&hosts, "host", nil, "a host name this machine is reached by, answered as its own are (repeatable)")
	cmd.AddCommand(serve)
	v := a.longRunningCmds(cmd, serve, kindOf(svcBDP))
	for _, c := range []*cobra.Command{v.start, v.install} {
		c.Flags().Lookup("addr").Usage = "listen address, on loopback (default: YARDR_BDP_ADDR, else bdp_addr, else " + store.DefaultBDPAddr + ")"
		c.Flags().Lookup("host").Usage = "a host name the server answers to besides the loopback names and the Scope URL's, for a proxy of yours in front of it (repeatable)"
	}
	return cmd
}

// bdpHosts is webHosts for the BDP server, whose refusal of an IP address off
// loopback says the server where the web view's says the view.
func bdpHosts(hosts []string, given, unsafe bool) ([]string, error) {
	names, err := webHosts(hosts, given, unsafe)
	if off := hostOffLoopback(""); errors.As(err, &off) {
		return nil, fmt.Errorf("--host %s refused: without --unsafe the server listens on loopback only and answers to no IP address off it. "+
			"Give --unsafe too to open the server to the network, where every IP address is answered", string(off))
	}
	return names, err
}

// bdpLoopback keeps a BDP server that no terminal runs on loopback, whatever
// yardr bdp serve --unsafe allows, as webLoopback does the web view. The names
// --host gives it are held to what a server on loopback takes, here and not in
// a log nobody reads.
func bdpLoopback(flags *pflag.FlagSet) error {
	const never = "yardr bdp start and yardr bdp install never listen off loopback"
	if err := bdp.CheckAddr("--addr", flags.Lookup("addr").Value.String(), false); err != nil {
		return fmt.Errorf("%w. %s", err, never)
	}
	host := flags.Lookup("host")
	_, err := webHosts(host.Value.(pflag.SliceValue).GetSlice(), host.Changed, false)
	if off := hostOffLoopback(""); errors.As(err, &off) {
		// Its own words name --unsafe, which these two do not have.
		return fmt.Errorf("--host %s refused: %s, and a server on loopback answers to no IP address off it", string(off), never)
	}
	return err
}
