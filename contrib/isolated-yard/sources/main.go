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
// With -github-app-key the stand-in knows a GitHub App too: it makes a
// throwaway key, writes the private half to the file named (mode 0600),
// and takes JWTs signed with it from the App -github-app-id, installed on
// -github-app-repos. A resident answers as the App with
//
//	GITHUB_APP_ID=<the -github-app-id>
//	GITHUB_APP_PRIVATE_KEY_FILE=<the -github-app-key>
//
// in its env_file, and its comments are by <-github-app-slug>[bot].
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
	"strings"

	"yardr.dev/yardr/internal/standin"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on (default: a free port on the loopback)")
	state := flag.String("state", "", "file the issues are kept in across restarts")
	linear := flag.String("linear-key", "lin_api_standin", "the Linear credential taken")
	github := flag.String("github-token", "gh-standin", "the GitHub credential taken")
	appKey := flag.String("github-app-key", "", "file to write a GitHub App's throwaway private key to; without it there is no App")
	appID := flag.String("github-app-id", "12345", "the App's id, the issuer of its JWTs")
	appSlug := flag.String("github-app-slug", "yardr", "the App's slug: it comments as <slug>[bot]")
	appRepos := flag.String("github-app-repos", "", "owner/repo the App is installed on, comma-separated")
	flag.Parse()
	s := &standin.Server{LinearKey: *linear, GitHubToken: *github, File: *state}
	if *appKey != "" {
		app, err := standin.NewApp(*appID, *appSlug, *appKey, strings.FieldsFunc(*appRepos, func(r rune) bool { return r == ',' })...)
		if err != nil {
			log.Fatal(err)
		}
		s.App = app
	}
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
