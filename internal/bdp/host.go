package bdp

import (
	"net"
	"slices"
	"strings"
)

// The server has no authentication, so a page on another site could make
// the browser ask it under a name of its own that resolves to this machine
// (DNS rebinding) and read the answer. The Host header then carries that
// name, so a request is answered only when its Host is one the server
// answers to. The rules are the web view's (knownHost in internal/web),
// which this package cannot import, with the Scope URL's host besides:
// docs/bdp.md says so.

// AllowNetwork is --unsafe for the requests: the server is served off
// loopback, so a request may name the machine by any IP or by hostname, its
// host name, with or without .local. Every other name is still refused: one
// that resolves here is what DNS rebinding looks like, and an IP literal
// cannot be rebound. hosts are answered as AllowHosts answers them. Call it
// before the server serves.
func (s *Server) AllowNetwork(hostname string, hosts ...string) {
	s.network = true
	s.names = nil
	if name := strings.TrimSuffix(strings.ToLower(hostname), ".local"); name != "" {
		s.names = []string{name, name + ".local"}
	}
	s.addNames(hosts)
}

// AllowHosts is --host without --unsafe: the server stays on loopback and
// answers to hosts too, the names the operator says are this machine's
// (each as web.CheckHost returns it): one the machine cannot work out by
// itself, as a proxy's. Nothing is added to them: no IP address off
// loopback, and not the machine's own host name, which only its operator
// can say resolves nowhere else. Call it before the server serves.
func (s *Server) AllowHosts(hosts ...string) {
	s.network = false
	s.names = nil
	s.addNames(hosts)
}

func (s *Server) addNames(hosts []string) {
	for _, h := range hosts {
		if h = strings.ToLower(h); h != "" && !slices.Contains(s.names, h) {
			s.names = append(s.names, h)
		}
	}
}

// knownHost reports whether a request's Host header names a host the server
// answers to: the Scope URL's, which is how a client reaches it, this
// machine's loopback interface, one of the host names it was given or,
// served off loopback, the machine by any IP literal.
func (s *Server) knownHost(hostport string) bool {
	host := hostOf(hostport)
	if s.scopeHost != "" && strings.EqualFold(host, s.scopeHost) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return s.network || ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost") || slices.Contains(s.names, strings.ToLower(host))
}

// hostOf is the host of a Host header, without its port and without the
// brackets of an IPv6 literal.
func hostOf(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}
