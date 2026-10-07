package server

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"syscall"
	"time"
)

// PprofEnv names the variable (YARDR_PPROF) that has the server serve
// net/http/pprof on a unix socket at the path it holds, to see where a
// server's time goes: `curl --unix-socket <path>
// 'http://x/debug/pprof/profile?seconds=60'`. Unset, which is the default,
// nothing listens and nothing is profiled. A socket of its own, not the
// store's: a profile is no call of a client, and the store's handler counts
// and drains its calls.
const PprofEnv = "PPROF"

// servePprof serves the profiles at path until the function returned is
// called. A path that cannot be listened on is logged and left: the yard
// works without it.
func servePprof(path string, log *slog.Logger) (stop func()) {
	if path == "" {
		return func() {}
	}
	// A server before us may have left its socket file behind. Never unlink
	// an unrelated file or a socket that another server is still using.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			log.Warn("pprof not served", "path", path, "err", "path exists and is not a socket")
			return func() {}
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			log.Warn("pprof not served", "path", path, "err", "socket is already in use")
			return func() {}
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			log.Warn("pprof not served", "path", path, "err", dialErr)
			return func() {}
		}
		if err := os.Remove(path); err != nil {
			log.Warn("pprof not served", "path", path, "err", err)
			return func() {}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Warn("pprof not served", "path", path, "err", err)
		return func() {}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Warn("pprof not served", "path", path, "err", err)
		return func() {}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log.Warn("pprof not served", "path", path, "err", err)
		ln.Close()
		return func() {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	log.Info("pprof served", "path", path)
	return func() {
		_ = srv.Close()
	}
}
