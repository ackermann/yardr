package source

import (
	"os"
	"slices"
)

// egressVars are the standard variables by which a machine behind a
// company's egress proxy reaches the outside: the proxy's address and what
// is reached without it, in the two cases tools read them in (Go reads
// either, the upper case first; curl, and git with it, reads only the lower
// case http_proxy), and the roots a proxy that inspects TLS signs with:
// SSL_CERT_FILE and SSL_CERT_DIR for Go on Linux and for OpenSSL,
// NODE_EXTRA_CA_CERTS for a harness that runs on Node, which does not read
// the system's store. yardr sets none and runs no proxy: it hands on what
// the operator's environment has (docs/configuration.md, "Behind an egress
// proxy"), to the server it starts, to the services it installs and, from
// the server and the daemon, to a source's commands (egressEnv). The list is
// here, beside the commands' environment, and the server and the services
// read it from here, so that which variables are handed on is answered once.
var egressVars = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// IsEgressVar reports whether key is one of the egressVars.
func IsEgressVar(key string) bool { return slices.Contains(egressVars, key) }

// egressEnv is the egressVars the process that runs a command has, each as
// it has it, for the command to start with. A command's environment is an
// allow-list, so without these a command that calls its service with curl,
// which reads them, cannot reach it from behind a company's proxy, however
// the yard's own calls are set up. One that is set and empty goes on empty,
// as server.ServerEnv hands it on, and one that is not set is left out:
// LookupEnv, since Getenv answers the same for both.
func egressEnv() []string {
	var env []string
	for _, k := range egressVars {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
