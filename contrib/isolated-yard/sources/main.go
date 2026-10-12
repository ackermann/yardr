// sources is a stand-in for Linear's and GitHub's APIs, for trying out what
// yardr does to an issue's status in an isolated yard (see ../isolated-yard
// and internal/standin). Build it into the yard's bin directory, so that
// `isolated-yard down` stops it with the rest:
//
//	go build -o D/bin/sources ./contrib/isolated-yard/sources
//	D/e sources -state D/issues.json &        # prints the address it took, A
//
// and start the yard's daemon with YARDR_GITHUB_API=http://A. A source the
// yard declares with base http://A hears Linear through it: the pack's
// commands call the stand-in. Without -addr it takes a free port on the
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
// -github-app-repos. A crew member answers as the App with
//
//	GITHUB_APP_ID=<the -github-app-id>
//	GITHUB_APP_PRIVATE_KEY_FILE=<the -github-app-key>
//
// in its env_file, and its comments are by <-github-app-slug>[bot].
//
// It also takes messages for a person, for a declared source whose file
// has `base: http://A/1`, a call that POSTs
// /messages.json with a JSON body (token, user, title, message), and a
// notify key naming it. -push-token is the application token it takes.
//
//	curl http://A/messages                          # the messages taken, and how many were asked for
//	curl -d status=500 http://A/messages/answer     # fail from now on; status=200 takes them again
//
// And it is a customer system, for a declared source whose calls a session
// makes with yardr call (contrib/isolated-yard/sources/crm.yaml, with BASE
// replaced by http://A): GET /crm/me, /crm/customers/<digits>, /crm/moved
// (a redirect) and /crm/big (200 KB), and POST /crm/customers/<digits>/notes,
// a note on a customer, which honours the Idempotency-Key header: a second
// request with a key writes nothing and gets the first one's answer.
// -crm-token is the bearer token it takes, and -crm-update-token the one a
// note is taken with (crm.yaml's update_env).
//
//	curl http://A/notes                             # the notes written, and how many were asked for
//	curl -X POST http://A/notes/hold                # the next note is written and its answer held: a timeout
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
	push := flag.String("push-token", "push-standin", "the application token a message is taken with")
	crm := flag.String("crm-token", "crm-standin", "the bearer token the customer system under /crm/ takes")
	crmUpdate := flag.String("crm-update-token", "crm-update-standin", "the bearer token a note on a customer is taken with")
	appKey := flag.String("github-app-key", "", "file to write a GitHub App's throwaway private key to; without it there is no App")
	appID := flag.String("github-app-id", "12345", "the App's id, the issuer of its JWTs")
	appSlug := flag.String("github-app-slug", "yardr", "the App's slug: it comments as <slug>[bot]")
	appRepos := flag.String("github-app-repos", "", "owner/repo the App is installed on, comma-separated")
	flag.Parse()
	s := &standin.Server{LinearKey: *linear, GitHubToken: *github, File: *state, PushToken: *push, CRMToken: *crm, CRMUpdateToken: *crmUpdate}
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
	log.Printf("stand-in for Linear (/graphql), GitHub (/repos/) a service that takes messages (/1/messages.json) and a customer system (/crm/) on %s", ln.Addr())
	log.Fatal(http.Serve(ln, s.Handler()))
}
