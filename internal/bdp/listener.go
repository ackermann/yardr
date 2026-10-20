package bdp

import (
	"bytes"
	"fmt"
	"net"
	"regexp"

	"yardr.dev/yardr/internal/store"
)

// refusedAs names the command whose server refuses an address, as its
// refusal says it.
const refusedAs = "yardr bdp"

// CheckAddr refuses a listen address that is not on the loopback interface,
// saying why as whose refused it (the flag that gave it). The server has no
// authentication, so it listens on loopback only, as the web view does
// (web.CheckAddr). With unsafe it refuses only what is no address.
func CheckAddr(whose, addr string, unsafe bool) error {
	return store.CheckLoopbackAddr(refusedAs, whose, addr, unsafe)
}

// Listen opens addr for the server, on loopback only unless unsafe: any
// other address is refused, and so is a name that turns out to listen
// elsewhere (web.Listen). whose is the flag that gave the address, as a
// refusal names it.
func Listen(whose, addr string, unsafe bool) (net.Listener, error) {
	if err := CheckAddr(whose, addr, unsafe); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// localhost is a name, and a hosts file may point it anywhere.
	if tcp, ok := ln.Addr().(*net.TCPAddr); !unsafe && (!ok || !tcp.IP.IsLoopback()) {
		_ = ln.Close()
		return nil, store.LoopbackAddrRefused(refusedAs, whose, addr, fmt.Sprintf("it listens on %s", ln.Addr()))
	}
	return ln, nil
}

// BodylessTransportErrors wraps a listener so the plain-text error responses
// net/http writes for requests it rejects before any handler runs (a control
// byte in the request line, malformed headers) carry no body, as BDP
// requires of transport-level rejections. Handler responses are untouched.
func BodylessTransportErrors(l net.Listener) net.Listener { return &bodylessListener{l} }

type bodylessListener struct{ net.Listener }

func (l *bodylessListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &bodylessConn{Conn: c}, nil
}

type bodylessConn struct{ net.Conn }

// net/http's canned rejection: status line, these exact headers, then the
// status text as the body, all in one write on the raw connection.
var cannedRejection = regexp.MustCompile(
	`^HTTP/1\.1 (4\d\d [^\r\n]*)\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n`)

func (c *bodylessConn) Write(p []byte) (int, error) {
	if m := cannedRejection.FindSubmatchIndex(p); m != nil && bytes.Equal(p[m[1]:], p[m[2]:m[3]]) {
		status := p[m[2]:m[3]]
		out := append([]byte("HTTP/1.1 "), status...)
		out = append(out, "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"...)
		if _, err := c.Conn.Write(out); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}
