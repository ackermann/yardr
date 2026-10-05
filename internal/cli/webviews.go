package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
)

// A running web view says so in a file of its own in the home's webviews
// directory, which it holds an exclusive flock on for as long as it runs,
// as the daemon holds its lock. A view that ended any way at all (a crash,
// kill -9, a reboot) has let go of the lock, so its record is told from a
// running view's without asking whether its pid is alive: that pid may be
// another program's by then. Nothing looks for a view with pgrep or by
// trying a port, and several views may run on one home.

// webView is a running web view, as yardr status and yardr doctor name it.
type webView struct {
	// Addr is the address it listens on, as the listener gave it.
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
	// Unsafe is whether it was started with --unsafe.
	Unsafe  bool      `json:"unsafe"`
	Started time.Time `json:"started"`
	// URL is where a person opens it, and Open whether another machine on
	// the network can. Neither is in the record: both are worked out when
	// it is read, since the machine's address may have changed by then.
	URL  string `json:"url,omitempty"`
	Open bool   `json:"open"`
}

// webViewsDir is where the home's running web views record themselves.
func webViewsDir(cfg *config.Config) string { return filepath.Join(cfg.Home, "webviews") }

// announceWeb records this process as a web view of the home listening on
// addr, until clear is called. The record is written and locked under a
// name no reader looks at and only then given its own, so what a reader
// finds is whole and held.
func announceWeb(cfg *config.Config, addr string, unsafe bool) (clear func(), err error) {
	dir := webViewsDir(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sweepWebViews(dir)
	f, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "view-"+strings.TrimPrefix(filepath.Base(f.Name()), ".new-")+".json")
	b, _ := json.Marshal(map[string]any{"addr": addr, "pid": os.Getpid(), "unsafe": unsafe, "started": time.Now().UTC()})
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return func() {
		os.Remove(path)
		f.Close()
	}, nil
}

// sweepWebViews removes the records of views that have ended without
// clearing theirs. A record that is held is a running view's and stays.
func sweepWebViews(dir string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "view-*.json"))
	for _, p := range paths {
		if !daemonRunning(p) {
			os.Remove(p)
		}
	}
}

// webViews are the home's running web views, oldest first. A record nobody
// holds is a view that has ended, and is not one of them.
func webViews(cfg *config.Config) []webView {
	views := []webView{}
	paths, _ := filepath.Glob(filepath.Join(webViewsDir(cfg), "view-*.json"))
	for _, p := range paths {
		if !daemonRunning(p) {
			continue
		}
		var v webView
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &v) != nil || v.Addr == "" {
			continue
		}
		v.URL, v.Open = webURL(v.Addr), v.Unsafe && !loopbackOnly(v.Addr)
		views = append(views, v)
	}
	slices.SortStableFunc(views, func(a, b webView) int { return a.Started.Compare(b.Started) })
	return views
}

// reach is the view in words: who can open it.
func (v webView) reach() string {
	if v.Open {
		return "open to the network (--unsafe), no login"
	}
	return "this machine only"
}

// loopbackOnly reports whether addr is on the loopback interface, where
// only this machine reaches it.
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// webURL is the URL a person opens for a view listening on addr. One that
// listens on every interface has no address of its own to name, so it is
// named by this machine's address on the network.
func webURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/"
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = machineAddr()
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// machineAddr is an address another machine on the network reaches this one
// by: its first IPv4 address off loopback, else its host name, else
// loopback. Tests replace it.
var machineAddr = func() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsGlobalUnicast() {
			return n.IP.String()
		}
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "127.0.0.1"
}

// webPart is doctor's line for the web view: whether one runs now, and
// whether a login service keeps one running. The two are told apart: a view
// started by hand runs with no service, and a service that is installed may
// have no view up. A view open to the network is a note, never a fault:
// the operator chose --unsafe.
func webPart(cfg *config.Config) optionalPart {
	yardr := yardrAt(cfg.Home)
	views := webViews(cfg)
	var running []string
	part := optionalPart{Key: "web", Title: "web view", Views: views}
	for _, v := range views {
		running = append(running, fmt.Sprintf("%s (pid %d, %s)", v.URL, v.PID, v.reach()))
		if v.Open {
			_, port, _ := net.SplitHostPort(v.Addr)
			part.Notes = append(part.Notes, fmt.Sprintf("%s (pid %d) is open to the network, no login: it was started with --unsafe, "+
				"and anyone who can reach this machine on port %s can read the yard", v.URL, v.PID, port))
		}
	}
	now := "not running now"
	if len(running) > 0 {
		now = "running at " + strings.Join(running, ", ")
	}
	if webService(cfg) {
		part.Configured = true
		part.Detail = "kept running as a login service" + at(webServiceAddr(cfg)) + fileOf(cfg, svcWeb) + "; " + now
		return part
	}
	if len(running) > 0 {
		part.Detail = fmt.Sprintf("%s; nothing keeps it running: %s web install keeps it running from login on (docs/web.md)", now, yardr)
		return part
	}
	part.Detail = fmt.Sprintf("not running, and nothing keeps it running; %s web serve shows the yard in a browser while it runs, "+
		"%s web install keeps it running from login on (docs/web.md)", yardr, yardr)
	return part
}
