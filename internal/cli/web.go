package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/web"
)

func (a *app) webCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "web", Short: "Show the yard in a browser"}
	var addr string
	var unsafe bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a read-only view of the yard over HTTP",
		Long: `Serve the yard as web pages: a feed of what happens, the flows of its
rigs with the beads in each stage, every bead, what waits for a person, the
running sessions and the residents, live. The view reads the yard and
changes nothing. Every page answers a request with "Accept:
application/json" with the data it shows, and /events streams the yard's
events as server-sent events.

There is no authentication yet, so it listens on loopback only: an address
on any other interface is refused.

--unsafe listens on every interface (0.0.0.0:8791, or the --addr given,
which may then be any address), to open the view from another machine on
the same network. There is still no authentication: anyone who can reach
this machine on that port can read the whole yard. A request is answered
when its Host header is an IP address, a loopback name or this machine's
host name (with or without .local), and 421 under any other name.`,
		Example: `  yardr web serve                          # http://127.0.0.1:8791/
  yardr web serve --addr 127.0.0.1:9000
  yardr web serve --unsafe                 # http://<this machine's IP>:8791/
  yardr web serve --unsafe --addr 192.168.178.90:9000   # that interface only
  curl -H 'Accept: application/json' localhost:8791/
  curl -N 'localhost:8791/events?format=json&after=0'   # every event, then live`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: a refused address must not start a server.
			addr := webAddr(addr, c.Flags().Changed("addr"), unsafe)
			if err := web.CheckAddr(addr, unsafe); err != nil {
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
				srv.AllowNetwork(hostname)
				_, port, _ := net.SplitHostPort(ln.Addr().String())
				log.Warn("no authentication: anyone who can reach this machine on port " + port + " can read the yard")
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
			log.Info("serving the yard", "url", "http://"+ln.Addr().String()+"/")
			if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	serve.Flags().StringVar(&addr, "addr", "127.0.0.1:8791", "listen address, on loopback; "+webUnsafeAddr+" with --unsafe")
	serve.Flags().BoolVar(&unsafe, "unsafe", false, "listen on every interface, or on any --addr; there is no authentication")
	cmd.AddCommand(serve)
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
