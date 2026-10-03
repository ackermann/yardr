package cli

import (
	"context"
	"errors"
	"log/slog"
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
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a read-only view of the yard over HTTP",
		Long: `Serve the yard as web pages: its rigs and where their beads stand. The
view reads the yard and changes nothing. Every page answers a request with
"Accept: application/json" with the data it shows.

There is no authentication yet, so it listens on loopback only: an address
on any other interface is refused.`,
		Example: `  yardr web serve                          # http://127.0.0.1:8791/
  yardr web serve --addr 127.0.0.1:9000
  curl -H 'Accept: application/json' localhost:8791/`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: a refused address must not start a server.
			if err := web.CheckLoopback(addr); err != nil {
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
			ln, err := web.Listen(addr)
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
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
	serve.Flags().StringVar(&addr, "addr", "127.0.0.1:8791", "listen address, on loopback")
	cmd.AddCommand(serve)
	return cmd
}
