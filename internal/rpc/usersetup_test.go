package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// courierMint is the mint program of the pack the setup ask sets up and of
// the source the yard holds already: the same bytes, which is what makes
// them one account, and the second a candidate on the first's card.
const courierMint = "#!/bin/sh\nexit 1\n"

// candidateAddress is the account the yard's source courier-existing holds.
const candidateAddress = "ops@courier.example.test"

// setupYard is a secretYard (the operator jo and sam, a device of each) that
// also holds the source courier-existing with the account in its file, and
// whose pack courier a setup ask can be put to jo for: the card then lists
// courier-existing as a candidate. It answers the file of the candidate.
func setupYard(t *testing.T) (*secretYard, string) {
	t.Helper()
	ctx := context.Background()
	y := newSecretYard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := y.st.PutSource(ctx, "courier-existing.yaml", apiSetupSource("courier-existing"), "jo")
	must(err)
	dir := filepath.Join(filepath.Dir(y.db), "sources")
	must(os.MkdirAll(filepath.Join(dir, "courier-existing"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "courier-existing", "courier-mint"), []byte(courierMint), 0o700))
	file := filepath.Join(dir, "courier-existing.env")
	_, err = y.st.SetSource(ctx, "courier-existing", store.SourceEnvFile, file, "jo")
	must(err)
	must(os.WriteFile(file, []byte("COURIER_ACCOUNT="+candidateAddress+"\n"), 0o600))
	sum := sha256.Sum256([]byte(courierMint))
	reading := &store.PackReading{Name: "courier", Kind: "source", Label: "embedded in yardr test",
		Sources: []store.AskSource{{Name: "courier", Text: apiSetupSource("courier"), Files: []store.AskFile{{
			Program: "courier-mint", Text: courierMint, SHA256: hex.EncodeToString(sum[:])}}}},
		Presets: []store.AskPreset{{Name: "read", Grants: []store.AskPresetGrant{{Source: "courier", Calls: []string{"read"}}}}}}
	y.st.SetPackReader(func(context.Context, string, store.API) (*store.PackReading, error) { return reading, nil })
	return y, file
}

// askSetup puts a setup ask for the pack courier to jo on yd-crm and answers
// its seq.
func (y *secretYard) askSetup(t *testing.T) uint64 {
	t.Helper()
	seq, err := y.st.AddAsk(context.Background(), "yd-crm", store.Ask{Question: "Set up the courier source?", To: "jo",
		Context: []string{"The nightly run pages through it."},
		Setup:   &store.AskSetup{Pack: "courier", To: []store.Grantee{{Crew: "yardmaster"}}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// bead asks the listener for yd-crm as the device with the token, and
// answers the view with the reply it came in.
func (y *secretYard) bead(t *testing.T, token string) (store.BeadView, string) {
	t.Helper()
	raw := call(t, y.h, token, "Bead", `{"bead":"yd-crm"}`)
	var reply struct{ Result store.BeadView }
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatalf("Bead answered %s: %v", raw, err)
	}
	return reply.Result, raw
}

// setups are the cards of the view's asks that set up a pack, by ask.
func setups(v store.BeadView) map[uint64]*store.SetupAsked {
	out := map[uint64]*store.SetupAsked{}
	for _, a := range v.Asks {
		if a.Setup != nil {
			out[a.Ask] = a.Setup
		}
	}
	return out
}

// Bead tells the candidates of a setup card, each a file of the yard's with
// the values it holds, to a device of the operator and to no other: to a
// device of anyone else the card reads as one with none, in the candidates
// and in the sentence that names them, and so it does to every device of a
// yard with no operator. It is the operator now who is told, and it is every
// ask of the view that is held back, the decided ones too. The rest of the
// view is the same, and the store, which the socket serves, keeps the card
// whole.
func TestUserBeadTellsCandidatesToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	y, file := setupYard(t)
	first := y.askSetup(t)
	want := store.AskCandidate{Source: "courier-existing", File: file, Values: map[string]string{"COURIER_ACCOUNT": candidateAddress}}
	// told reads the view as the device with the token has it, and fails
	// unless the setup asks it holds are asks, and the candidate is on each
	// of their cards and in the reply when listed, and nowhere in it when not.
	told := func(who, token string, asks []uint64, listed bool) store.BeadView {
		t.Helper()
		v, raw := y.bead(t, token)
		cards := setups(v)
		if len(cards) != len(asks) {
			t.Fatalf("%s is told the setup asks %v, want %v", who, slices.Sorted(maps.Keys(cards)), asks)
		}
		for _, seq := range asks {
			card := cards[seq]
			if card == nil {
				t.Fatalf("%s is told no card for ask %d", who, seq)
			}
			if listed && !reflect.DeepEqual(card.Candidates, []store.AskCandidate{want}) {
				t.Errorf("%s is told the candidates %+v of ask %d, want %+v", who, card.Candidates, seq, want)
			}
			if !listed && len(card.Candidates) != 0 {
				t.Errorf("%s is told the candidates %+v of ask %d", who, card.Candidates, seq)
			}
		}
		// Not in the candidates, and not anywhere else of the reply either:
		// the card says them in its sentences too.
		for _, secret := range []string{file, candidateAddress, "courier-existing"} {
			if strings.Contains(raw, secret) != listed {
				t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
			}
		}
		return v
	}

	whole, err := y.st.Bead(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	if got := setups(*whole)[first].Candidates; !reflect.DeepEqual(got, []store.AskCandidate{want}) {
		t.Fatalf("the store lists the candidates %+v, want %+v", got, want)
	}
	jos := told("jo's device", y.jo, []uint64{first}, true)
	sams := told("sam's device", y.sam, []uint64{first}, false)

	// UserAsks holds nothing back, and needs not: a setup ask is put to the
	// operator, so no queue but theirs has it. If it is ever put to another
	// user, this is where that is found out.
	if queue := call(t, y.h, y.jo, "UserAsks", `{}`); !strings.Contains(queue, candidateAddress) {
		t.Errorf("the operator's queue lacks the card's candidate: %s", queue)
	}
	if queue := call(t, y.h, y.sam, "UserAsks", `{}`); strings.Contains(queue, candidateAddress) || strings.Contains(queue, file) {
		t.Errorf("sam's queue holds the card's candidate: %s", queue)
	}

	// What sam is not told is the candidates alone: the same view, the
	// card's other sentences and options and fields as the operator has them.
	card, theirs := setups(jos)[first], setups(sams)[first]
	if len(card.Does) != len(theirs.Does) || !strings.Contains(strings.Join(card.Does, "\n"), file) {
		t.Errorf("sam's card has %d sentences, the operator's %d:\n%s\n%s", len(theirs.Does), len(card.Does), strings.Join(theirs.Does, "\n"), strings.Join(card.Does, "\n"))
	}
	differ := 0
	for i := range min(len(card.Does), len(theirs.Does)) {
		if card.Does[i] != theirs.Does[i] {
			differ++
		}
	}
	if differ != 1 {
		t.Errorf("%d sentences of sam's card differ from the operator's, want the one that names the candidate", differ)
	}
	for _, v := range []*store.BeadView{&jos, &sams} {
		for i := range v.Asks {
			if v.Asks[i].Setup != nil {
				v.Asks[i].Setup.Candidates, v.Asks[i].Setup.Does = nil, nil
			}
		}
	}
	if !reflect.DeepEqual(jos, sams) {
		t.Errorf("the view of sam's device is not the operator's but for the candidates:\n%+v\nwant\n%+v", sams, jos)
	}

	// Every ask of the view: this one is decided, and another stands open.
	if err := y.st.AddAnswer(ctx, "yd-crm", store.Answer{Ask: first, Decline: true, Words: "not this week"}, "jo"); err != nil {
		t.Fatal(err)
	}
	second := y.askSetup(t)
	told("jo's device", y.jo, []uint64{first, second}, true)
	told("sam's device", y.sam, []uint64{first, second}, false)

	// The operator now is told: the device of the one named moves with it,
	// and in a yard with no operator no device is.
	if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	told("jo's device, no longer the operator's", y.jo, []uint64{first, second}, false)
	told("sam's device, the operator's now", y.sam, []uint64{first, second}, true)
	if err := y.st.SetSetting(ctx, store.OperatorSetting, "", "sam"); err != nil {
		t.Fatal(err)
	}
	told("jo's device, in a yard with no operator", y.jo, []uint64{first, second}, false)
	told("sam's device, in a yard with no operator", y.sam, []uint64{first, second}, false)
}
