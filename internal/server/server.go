// Package server starts and stops the local SurrealDB process yard uses.
// The server runs detached so it outlives the CLI call that started it and
// every CLI, agent and daemon shares the one instance.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mckean/yard/internal/config"
)

// Healthy reports whether the server answers its health endpoint.
func Healthy(ctx context.Context, c *config.Config) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HealthURL(), nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Ensure starts the local server if it is not already answering.
func Ensure(ctx context.Context, c *config.Config) error {
	if Healthy(ctx, c) {
		return nil
	}
	if !c.IsLocal() {
		return fmt.Errorf("store at %s is not reachable", c.Addr)
	}
	return Start(ctx, c)
}

// Start launches `surreal start` detached and waits until it is healthy.
func Start(ctx context.Context, c *config.Config) error {
	bin, err := exec.LookPath("surreal")
	if err != nil {
		return fmt.Errorf("surreal binary not found in PATH: %w", err)
	}
	logf, err := os.OpenFile(c.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, "start", "--no-banner", "--log", "warn",
		"--bind", c.Addr, "--username", c.User, "--password", c.Pass,
		"surrealkv://"+c.DataDir())
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if Healthy(ctx, c) {
			return nil
		}
		if syscall.Kill(pid, 0) != nil {
			return fmt.Errorf("surreal exited during startup; see %s", c.LogFile())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("surreal did not become healthy within 15s; see %s", c.LogFile())
}

// Stop terminates the server recorded in the pidfile.
func Stop(c *config.Config) error {
	b, err := os.ReadFile(c.PIDFile())
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no pidfile; server not started by yard")
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
	for i := 0; i < 100 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	return os.Remove(c.PIDFile())
}
