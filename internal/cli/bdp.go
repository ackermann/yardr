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

	"yardr.dev/yardr/internal/bdp"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func (a *app) bdpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bdp", Short: "Serve the yard over the Bead Protocol (BDP)"}
	var addr, scope string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the BDP Read profile over HTTP",
		Long: `Serve the yard's beads and dependency links over the BDP Read profile.

The Scope URL is the canonical public address of this yard: every Bead and Link
id is minted under it, and it is what BDP clients use. It may differ from the
listen address (behind a proxy, or a fixed name for tests).

It listens on --addr, else on YARDR_BDP_ADDR when set in the command's
environment, else on the yard's bdp_addr setting (yardr settings set
bdp_addr <host:port>), else on 127.0.0.1:8765.`,
		Example: `  yardr bdp serve                                  # http://127.0.0.1:8765/yard/
  yardr bdp serve --addr :8080 --scope https://beads.example/team/`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if err := a.addrFromSetting(ctx, a.cfg, kindOf(svcBDP), c.Flags()); err != nil {
				return err
			}
			if scope == "" {
				scope = "http://" + addr + "/yard/"
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
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			defer kindOf(svcBDP).announced(a.cfg, log, map[string]any{"addr": ln.Addr().String(), "scope": scope})()
			log.Info("serving BDP Read", "addr", addr, "scope", scope)
			if err := hs.Serve(bdp.BodylessTransportErrors(ln)); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	serve.Flags().StringVar(&addr, "addr", store.DefaultBDPAddr, "listen address (default: YARDR_BDP_ADDR, else bdp_addr, else "+store.DefaultBDPAddr+")")
	serve.Flags().StringVar(&scope, "scope", "", "canonical Scope URL, ending in / (default http://<addr>/yard/)")
	cmd.AddCommand(serve)
	a.longRunningCmds(cmd, serve, kindOf(svcBDP))
	return cmd
}
