package bdp

import (
	"bytes"
	"net"
	"regexp"
)

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
