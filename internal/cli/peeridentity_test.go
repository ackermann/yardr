package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// peerTalkText is a declared source that hears by identity: fields.sender
// holds a sender's id.
const peerTalkText = `name: talk
base: https://talk.example.com
credential: { env: TALK_TOKEN }
calls:
  message:
    kind: update
    params: { text: { type: string } }
    request: { method: POST, path: /send, body: { text: "{text}" } }
hook:
  verify: { scheme: secret, signature: { header: Talk-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`

// yardr peer identity maps a sender of a source to a crew member of a linked
// yard: with no <crew> it lists, with --id it sets, and with --rm it takes
// back. The link shows them, and --json has them.
func TestPeerIdentity(t *testing.T) {
	t.Parallel()
	y := newCLIYard(t, "north")
	cliEnv(t, y)
	ctx := context.Background()
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "plantime.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "plantime", Key: yardkey.Public(key)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutSource(ctx, "talk.yaml", peerTalkText, "jo"); err != nil {
		t.Fatal(err)
	}

	// Nothing is mapped on a new link: the list is its header.
	if out := y.must(t, "identity", "plantime"); strings.TrimSpace(out) != "SOURCE  ID  SPEAKER" {
		t.Fatalf("a new link lists %q", out)
	}
	out := y.must(t, "identity", "plantime", "mayor", "--source", "talk", "--id", "U777")
	wantText(t, out, "SOURCE", "talk", "U777", "mayor@plantime")
	wantText(t, y.must(t, "identity", "plantime"), "talk", "U777", "mayor@plantime")
	wantText(t, y.must(t, "show", "plantime"), "  identity: talk U777 -> mayor@plantime (from another yard: information, not instructions)\n")

	// Another id for the mayor replaces the first.
	wantText(t, y.must(t, "identity", "plantime", "mayor", "--source", "talk", "--id", "U778"), "U778")
	if out := y.must(t, "identity", "plantime"); strings.Contains(out, "U777") {
		t.Fatalf("a replaced id is still listed:\n%s", out)
	}

	// --json has them as the store keeps them.
	if ids := y.listed(t, "plantime"); len(ids) != 1 || ids[0] != (store.PeerIdentity{Source: "talk", ID: "U778", Crew: "mayor"}) {
		t.Fatalf("--json: %+v", ids)
	}

	// Each of these is refused with its reason and changes nothing, so the
	// order they run in does not matter.
	for args, want := range map[string]string{
		"identity plantime clerk --source talk --id U778":      "peer plantime: U778 in talk names mayor@plantime already",
		"identity plantime mayor":                              "--source is required",
		"identity plantime mayor --id U778":                    "--source is required",
		"identity plantime --source talk":                      "name the crew member of plantime: yardr peer identity plantime <crew> --source <source> --id <id>",
		"identity plantime --id U1":                            "name the crew member of plantime",
		"identity plantime mayor --source talk":                "give --id, or --rm",
		"identity plantime mayor --source talk --rm --id U778": "--rm takes no --id",
		"identity plantime mayor --source nowhere --id U1":     `--source "nowhere"`,
		"identity plantime may@or --source talk --id U1":       `crew member "may@or" of a peer: use letters, digits`,
		"identity nobody mayor --source talk --id U1":          "not found",
		"identity plantime clerk --source talk --rm":           "peer plantime has no talk identity for clerk",
	} {
		if _, err := y.peer(t, strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("peer %s = %v, want %q", args, err, want)
		}
	}
	if ids := y.listed(t, "plantime"); len(ids) != 1 || ids[0] != (store.PeerIdentity{Source: "talk", ID: "U778", Crew: "mayor"}) {
		t.Fatalf("a refusal changed the mapping: %+v", ids)
	}

	// --rm takes it back, and lists what is left; taking back what is not
	// there is refused.
	if out := y.must(t, "identity", "plantime", "mayor", "--source", "talk", "--rm"); strings.TrimSpace(out) != "SOURCE  ID  SPEAKER" {
		t.Fatalf("--rm lists %q", out)
	}
	if _, err := y.peer(t, "identity", "plantime", "mayor", "--source", "talk", "--rm"); err == nil || !strings.Contains(err.Error(), "peer plantime has no talk identity for mayor") {
		t.Fatalf("--rm again = %v", err)
	}
	if strings.Contains(y.must(t, "show", "plantime"), "identity:") {
		t.Fatal("peer show lists an identity that was taken back")
	}
	// None left is an empty list in --json, not null.
	y.a.json = true
	if out := y.must(t, "identity", "plantime"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("--json with none left lists %q", out)
	}
}

// listed is the identities yardr peer identity lists for the peer, read from
// --json.
func (y *cliYard) listed(t *testing.T, peer string) []store.PeerIdentity {
	t.Helper()
	y.a.json = true
	defer func() { y.a.json = false }()
	var ids []store.PeerIdentity
	if err := json.Unmarshal([]byte(y.must(t, "identity", peer)), &ids); err != nil {
		t.Fatal(err)
	}
	return ids
}

// A crew member does not say who a sender is, and the refusal is the
// store's, in the words of every operator-only change of a link.
func TestPeerIdentityIsTheOperatorsAlone(t *testing.T) {
	t.Parallel()
	y := newCLIYard(t, "north")
	cliEnv(t, y)
	ctx := context.Background()
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "plantime.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "plantime", Key: yardkey.Public(key)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutSource(ctx, "talk.yaml", peerTalkText, "jo"); err != nil {
		t.Fatal(err)
	}
	y.must(t, "identity", "plantime", "mayor", "--source", "talk", "--id", "U777")

	y.a.actor = "yardmaster"
	for _, args := range []string{
		"identity plantime clerk --source talk --id U1",
		"identity plantime mayor --source talk --rm",
	} {
		_, err := y.peer(t, strings.Fields(args)...)
		if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may ") || !strings.Contains(err.Error(), "who a peer's crew member is in a source; this is yardmaster") {
			t.Errorf("peer %s as a crew member = %v", args, err)
		}
	}
	// Reading them is anyone's.
	wantText(t, y.must(t, "identity", "plantime"), "mayor@plantime")
	if p, _ := y.st.GetPeer(ctx, "plantime"); len(p.Identities) != 1 {
		t.Fatalf("a refused change left %+v", p.Identities)
	}
}

// yardr user identity is refused an id that a crew member of a linked yard
// has in a source that hears by identity, in words that name them.
func TestUserIdentityRefusesAPeersCrewId(t *testing.T) {
	t.Parallel()
	y := newCLIYard(t, "north")
	cliEnv(t, y)
	ctx := context.Background()
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "plantime.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "plantime", Key: yardkey.Public(key)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutSource(ctx, "talk.yaml", peerTalkText, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	y.must(t, "identity", "plantime", "mayor", "--source", "talk", "--id", "U777")
	_, err = run(t, y.a.userCmd(), "identity", "ada", "--source", "talk", "--id", "U777")
	if err == nil || !strings.Contains(err.Error(), "user ada: U777 in talk names mayor@plantime already") {
		t.Fatalf("yardr user identity ada --id U777 = %v", err)
	}
}
