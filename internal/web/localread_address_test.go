package web

import (
	"net"
	"testing"
)

func TestLocalReadIPsAreLoopback(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		ips  []net.IPAddr
		want bool
	}{
		{"IPv4 loopback", []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, true},
		{"IPv6 loopback", []net.IPAddr{{IP: net.ParseIP("::1")}}, true},
		{"mixed loopback and external", []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("192.0.2.1")}}, false},
		{"external", []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, false},
		{"no answers", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := localReadIPsAreLoopback(c.ips); got != c.want {
				t.Errorf("localReadIPsAreLoopback(%v) = %v, want %v", c.ips, got, c.want)
			}
		})
	}
}
