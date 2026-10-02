// Package server runs the one process that owns the yard's store file and
// serves it to every other yard process over a unix socket. The CLI starts
// it on first use, detached, so it outlives the call that started it.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mckean/yard/internal/config"
	"github.com/mckean/yard/internal/rpc"
	"github.com/mckean/yard/internal/store"
)

// Run owns the store and serves it until ctx ends.
func Run(ctx context.Context, c *config.Config, log *slog.Logger) error {
	if len(c.Socket()) > 100 {
		return fmt.Errorf("socket path %s is too long for a unix socket; use a shorter YARD_HOME", c.Socket())
	}
	st, err := store.Open(c.DBPath())
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("another yard server owns %s", c.DBPath())
	} else if err != nil {
		return err
	}
	defer st.Close(context.Background())

	// We own the store, so a socket file left behind is stale.
	_ = os.Remove(c.Socket())
	ln, err := net.Listen("unix", c.Socket())
	if err != nil {
		return err
	}
	if err := os.Chmod(c.Socket(), 0o600); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(c.Socket())
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(c.PIDFile())

	srv := &http.Server{Handler: rpc.Handler(st, log), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Streams (events, wake) never finish on their own; Shutdown waits
		// for them until the deadline, then Close cuts them.
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
	}()
	log.Info("yard server ready", "store", c.DBPath(), "socket", c.Socket())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Connect returns a client for the running server, starting one if none
// answers.
func Connect(ctx context.Context, c *config.Config) (*rpc.Client, error) {
	client := rpc.NewClient(c.Socket())
	if ping(ctx, client) {
		return client, nil
	}
	if err := Start(ctx, c); err != nil {
		return nil, err
	}
	return client, nil
}

// Running reports whether a server answers on the socket.
func Running(ctx context.Context, c *config.Config) bool {
	return ping(ctx, rpc.NewClient(c.Socket()))
}

func ping(ctx context.Context, client *rpc.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return client.Ping(ctx) == nil
}

// Start launches `yard server run` detached and waits until it answers.
func Start(ctx context.Context, c *config.Config) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(c.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "server", "run")
	cmd.Env = append(os.Environ(), "YARD_HOME="+c.Home)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	client := rpc.NewClient(c.Socket())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ping(ctx, client) {
			return nil
		}
		select {
		case <-exited:
			// Lost a race with another starter? Then its server answers.
			if ping(ctx, client) {
				return nil
			}
			return fmt.Errorf("yard server exited during startup; see %s", c.LogFile())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("yard server did not answer within 10s; see %s", c.LogFile())
}

// Stop terminates the server recorded in the pidfile.
func Stop(c *config.Config) error {
	b, err := os.ReadFile(c.PIDFile())
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no yard server is running")
	} else if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("bad pidfile: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	for i := 0; i < 200 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		return fmt.Errorf("yard server (pid %d) did not stop", pid)
	}
	return nil
}
