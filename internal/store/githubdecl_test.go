package store_test

import (
	"context"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// githubKeysDecl declares a github source whose events carry the fields
// the pack's hook.keys names: what a subscription's --where, a hook's
// says and makes may filter on beside the store's.
const githubKeysDecl = `name: github
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: X-Hub-Signature-256, encoding: hex, prefix: "sha256=" }
    delivery: { header: X-GitHub-Delivery }
  bodies: unique-per-hook
  normalize: [github/normalize]
  keys: [event, number, state, merged, conclusion, workflow, head_branch, head_sha, head_repo, base, train, sender, association]
`

// declareGitHubKeys declares the github source above in st, as actor.
func declareGitHubKeys(t *testing.T, st store.API, actor string) {
	t.Helper()
	if _, err := st.PutSource(context.Background(), "github.yaml", githubKeysDecl, actor); err != nil {
		t.Fatal(err)
	}
}
