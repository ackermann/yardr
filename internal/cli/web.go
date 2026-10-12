package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/web"
)

func (a *app) webCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "web", Short: "Show the yard in a browser"}
	var addr string
	var unsafe bool
	var hosts []string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a read-only view of the yard over HTTP",
		Long: `Serve the yard as web pages: a feed of what happens, the flows of its
depots with the beads in each stage, every bead, what waits for a person, the
running sessions and the crew, live. The view reads the yard and
changes nothing. Every page answers a request with "Accept:
application/json" with the data it shows, and /events streams the yard's
events as server-sent events.

There is no authentication yet, so it listens on loopback only: an address
on any other interface is refused. A request is answered when its Host
header is a loopback name or a name given with --host, and 421 under any
other name.

--unsafe listens on every interface (0.0.0.0:8791, or the --addr given,
which may then be any address), to open the view from another machine on
the same network. There is still no authentication: anyone who can reach
this machine on that port can read the whole yard. A request is answered
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

The open /events streams are limited: 16 from one address off loopback
(then 429) and 128 from all of them together (then 503), and 128 from
loopback, counted apart, so the network cannot take a stream from a page on
this machine. Both answers carry Retry-After.`,
		Example: `  yardr web serve                          # http://127.0.0.1:8791/
  yardr web serve --addr 127.0.0.1:9000
  yardr web serve --host yard.example.net  # on loopback, for a proxy that forwards that name
  yardr web serve --unsafe                 # http://<this machine's IP>:8791/
  yardr web serve --unsafe --addr 192.168.178.90:9000   # that interface only
  yardr web serve --unsafe --host yard.example.net      # http://yard.example.net:8791/ too
  curl -H 'Accept: application/json' localhost:8791/
  curl -N 'localhost:8791/events?format=json&after=0'   # every event, then live`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: a refused address must not start a server.
			addr := webAddr(addr, c.Flags().Changed("addr"), unsafe)
			if err := web.CheckAddr(addr, unsafe); err != nil {
				return err
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
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			srv, err := web.NewServer(st, a.cfg.Home, log)
			if err != nil {
				return err
			}
			ln, err := web.Listen(addr, unsafe)
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
			// Every request ends with ctx: an open event stream would
			// otherwise hold Shutdown until its time ran out.
			hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second,
				BaseContext: func(net.Listener) context.Context { return ctx }}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = hs.Shutdown(sctx)
			}()
			// For yardr status and yardr doctor, which name the views that
			// run. A view that cannot say so still serves.
			if clear, err := announceWeb(a.cfg, ln.Addr().String(), unsafe); err != nil {
				log.Warn("yardr status will not list this view: it could not be recorded", "err", err)
			} else {
				defer clear()
			}
			at := []any{"url", "http://" + ln.Addr().String() + "/"}
			if to := webAnswersTo(unsafe, srv.Names()); to != "" {
				at = append(at, "answers_to", to)
			}
			log.Info("serving the yard", at...)
			if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	serve.Flags().StringVar(&addr, "addr", webLocalAddr, "listen address, on loopback; "+webUnsafeAddr+" with --unsafe")
	serve.Flags().BoolVar(&unsafe, "unsafe", false, "listen on every interface, or on any --addr; there is no authentication")
	serve.Flags().StringSliceVar(&hosts, "host", nil, "a host name this machine is reached by, answered as its own are (repeatable)")
	cmd.AddCommand(serve)
	v := a.longRunningCmds(cmd, serve, kindOf(svcWeb))
	for _, c := range []*cobra.Command{v.start, v.install} {
		c.Flags().Lookup("addr").Usage = "listen address, on loopback"
		c.Flags().Lookup("host").Usage = "a host name the view answers to besides the loopback names, for a proxy of yours in front of it (repeatable)"
	}
	v.start.Short = "Start the web view in the background, on loopback, and print its address"
	v.start.Long = `Start yardr web serve detached from this terminal, logging to
<home>/web.log, wait until it listens and print its address. It runs until
yardr web stop stops it or the machine restarts; yardr web install keeps one
running from login on instead.

It listens on loopback only: there is no --unsafe here. --host is handed on
to yardr web serve, and names what the view answers to on loopback besides
the loopback names (docs/web.md, "Behind a proxy").`
	v.start.Example = `  yardr web start                          # http://127.0.0.1:8791/
  yardr web start --host yard.example.net  # the same, for a proxy that forwards that name`
	v.stop.Long = `Tell every web view that runs on this yard to stop, however it was started
(yardr web serve in a terminal, yardr web start, the login service), and wait
until it has: yardr status lists them. A yardr web start after it answers at
the same address.

A view that runs as a login service stays stopped too: the service starts it
again at the next login, or when it ends with a failure, and a stop is none.`
	v.install.Short = "Keep the web view running from login on: a launchd agent (macOS) or a systemd user unit (Linux)"
	v.install.Long = `Write yardr web serve as a login service of this user and load it, as
yardr daemon install does for the daemon: the service manager starts the view
now and at every login. It prints the view's address and logs to
<home>/web.log.

A service listens on loopback only: --addr takes 127.0.0.1, ::1 or localhost
with a port, and there is no --unsafe here. --host is written into the
service and handed on to yardr web serve: the view answers to the names
given besides the loopback names, and still listens on loopback only. To
reach the view from another machine, forward the port over ssh or put a
proxy of yours in front of it (docs/web.md, "Behind a proxy").

--file-only writes the file and loads nothing.`
	v.install.Example = `  yardr web install                       # http://127.0.0.1:8791/
  yardr web install --addr 127.0.0.1:9000
  yardr web install --host yard.example.net   # for a proxy that forwards that name
  yardr web uninstall`
	return cmd
}

// webUnsafeAddr is where --unsafe listens when --addr is not given.
const webUnsafeAddr = "0.0.0.0:8791"

// webAddr is the address web serve listens on: --addr when it was given,
// else its loopback default, or every interface under --unsafe.
func webAddr(addr string, given, unsafe bool) string {
	if unsafe && !given {
		return webUnsafeAddr
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
