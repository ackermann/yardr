package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/version"
)

// A running web view says so in a file of its own in the home's webviews
// directory, which it holds an exclusive flock on for as long as it runs,
// as the daemon holds its lock and as every long-running command but the
// daemon does (longRunning.announce). Nothing looks for a view with pgrep
// or by trying a port, and several views may run on one home.

// webView is a running web view, as yardr status and yardr doctor name it.
type webView struct {
	// Addr is the address it listens on, as the listener gave it.
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
	// Unsafe is whether it was started with --unsafe.
	Unsafe bool `json:"unsafe"`
	// As is the user it acts as (--as, YARDR_WEB_AS or web_as), "" when it
	// acts as none.
	As string `json:"as,omitempty"`
	// JWTFrom is the issuer whose tokens a view in JWT mode verifies
	// (web_jwt_issuer), "" when the view takes no JWT mode. It stands in
	// place of As: the two never stand together.
	JWTFrom string    `json:"jwt_from,omitempty"`
	Started time.Time `json:"started"`
	// Revision is the build it runs, as it recorded it: a view does not
	// replace itself when a new yardr is installed, as the server and the
	// daemon do, so it may be another than the server's all day.
	Revision string `json:"revision"`
	// Build is Revision for people, and Differs whether the yardr that
	// reads the record is another build. Both are the reader's.
	Build   string `json:"build,omitempty"`
	Differs bool   `json:"differs"`
	// URL is where a person opens it, and Open whether another machine on
	// the network can. Neither is in the record: both are worked out when
	// it is read, since the machine's address may have changed by then.
	URL  string `json:"url,omitempty"`
	Open bool   `json:"open"`
}

// webViewsDir is where the home's running web views record themselves.
func webViewsDir(cfg *config.Config) string { return cfg.WebViews() }

// announceWeb records this process as a web view of the home listening on
// addr, acting as the user as ("" for none) or verifying the tokens of
// jwtFrom in JWT mode ("" for none), until clear is called.
func announceWeb(cfg *config.Config, addr string, unsafe bool, as, jwtFrom string) (clear func(), err error) {
	return kindOf(svcWeb).announce(cfg, map[string]any{"addr": addr, "unsafe": unsafe, "as": as, "jwt_from": jwtFrom})
}

// webViews are the home's running web views, oldest first. A record nobody
// holds is a view that has ended, and is not one of them.
func webViews(cfg *config.Config) []webView {
	views := []webView{}
	for _, p := range held(webViewsDir(cfg)) {
		var v webView
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &v) != nil || v.Addr == "" {
			continue
		}
		v.URL, v.Open = webURL(v.Addr), v.Unsafe && !loopbackOnly(v.Addr)
		v.Build, v.Differs = version.Show(v.Revision), v.Revision != version.Current()
		views = append(views, v)
	}
	slices.SortStableFunc(views, func(a, b webView) int { return a.Started.Compare(b.Started) })
	return views
}

// reach is the view in words: who can open it, and whom it acts as — or, in
// JWT mode, whose tokens it verifies.
func (v webView) reach() string {
	reach := "this machine only"
	if v.Open {
		reach = "open to the network (--unsafe)"
		if v.JWTFrom == "" {
			reach += ", no login"
		}
	}
	if v.JWTFrom != "" {
		reach += ", jwt from " + v.JWTFrom
	} else if v.As != "" {
		reach += ", acts as " + v.As
	}
	return reach
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
			part.Notes = append(part.Notes, webOpenNote(v))
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

// webOpenNote is doctor's note for a view open to the network: anyone who
// can reach the machine reads the yard — or, in JWT mode, is refused
// without a token the gateway signed.
func webOpenNote(v webView) string {
	_, port, _ := net.SplitHostPort(v.Addr)
	if v.JWTFrom != "" {
		return fmt.Sprintf("%s (pid %d) is open to the network: it was started with --unsafe, "+
			"and every request but /health needs a token signed by the gateway", v.URL, v.PID)
	}
	return fmt.Sprintf("%s (pid %d) is open to the network, no login: it was started with --unsafe, "+
		"and anyone who can reach this machine on port %s can read the yard", v.URL, v.PID, port)
}
