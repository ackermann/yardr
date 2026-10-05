// sources is a stand-in for Linear's and GitHub's APIs, for trying out what
// yardr does to an issue's status in an isolated yard (see ../isolated-yard
// and internal/standin). Build it into the yard's bin directory, so that
// `isolated-yard down` stops it with the rest:
//
//	go build -o D/bin/sources ./contrib/isolated-yard/sources
//	D/e sources -state D/issues.json &        # prints the address it took, A
//
// and start the yard's daemon with YARDR_LINEAR_API=http://A/graphql and
// YARDR_GITHUB_API=http://A. Without -addr it takes a free port on the
// loopback. Give it one only after checking that nothing listens on the port
// on any address: a listener on 127.0.0.1 takes the loopback's traffic from
// one on all addresses, which is how a stand-in on 8791 once stood in front
// of a real yard's web view. The issues are kept in -state, so the stand-in
// can be stopped and started again, on the same -addr, as a source that was
// down.
//
//	curl -d '{"source":"linear","id":"uuid-1","key":"YRD-1","title":"Web view","status":"Todo"}' http://A/issues
//	curl http://A/issues                 # the issues, and every call made
//	curl -d source=linear -d key=YRD-1 -d status=Done http://A/status   # a person moves it
package main

import (
	"flag"
	"log"
	"net"
	"net/http"

	"yardr.dev/yardr/internal/standin"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on (default: a free port on the loopback)")
	state := flag.String("state", "", "file the issues are kept in across restarts")
	linear := flag.String("linear-key", "lin_api_standin", "the Linear credential taken")
	github := flag.String("github-token", "gh-standin", "the GitHub credential taken")
	flag.Parse()
	s := &standin.Server{LinearKey: *linear, GitHubToken: *github, File: *state}
	if *state != "" {
		if err := s.Load(); err != nil {
			log.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("stand-in for Linear (/graphql) and GitHub (/repos/) on %s", ln.Addr())
	log.Fatal(http.Serve(ln, s.Handler()))
}
