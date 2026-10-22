package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
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

// askSetup puts a setup ask for the pack courier to the user, who is the
// operator, on yd-crm and answers its seq.
func (y *secretYard) askSetup(t *testing.T, user string) uint64 {
	t.Helper()
	seq, err := y.st.AddAsk(context.Background(), "yd-crm", store.Ask{Question: "Set up the courier source?", To: user,
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

// queue asks the listener for the queue of the user of the device with the
// token, and answers it with the reply it came in.
func (y *secretYard) queue(t *testing.T, token string) (store.AskQueue, string) {
	t.Helper()
	raw := call(t, y.h, token, "UserAsks", `{}`)
	var reply struct{ Result store.AskQueue }
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatalf("UserAsks answered %s: %v", raw, err)
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
	first := y.askSetup(t, "jo")
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

	// UserAsks tells the card whole to the operator's device, as Bead does.
	// The ask is put to jo, so sam's queue has none of it; the queue of a user
	// who is not the operator now is TestUserAsksTellsTheSetupToTheOperatorAlone's.
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
	second := y.askSetup(t, "jo")
	told("jo's device", y.jo, []uint64{first, second}, true)
	told("sam's device", y.sam, []uint64{first, second}, false)

	// Each operator is told: the device of the one made an operator moves
	// with the role, and so does the device of one made a member.
	if _, err := y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo"); err != nil {
		t.Fatal(err)
	}
	told("jo's device, one operator's of two", y.jo, []uint64{first, second}, true)
	told("sam's device, an operator's now", y.sam, []uint64{first, second}, true)
	if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
		t.Fatal(err)
	}
	told("jo's device, a member's now", y.jo, []uint64{first, second}, false)
	told("sam's device, the one operator's", y.sam, []uint64{first, second}, true)
}

// carriedOut approves the setup ask seq of yd-crm as the user, who is the
// operator, taking the candidate courier-existing when account names it and
// typing the account when it is empty, and records its carrying out as the
// server's carrier does but for the work: the lines it prints for the apply,
// for the account in file and for the grants, or the failure in its words when
// there is one. It answers what was recorded.
func (y *secretYard) carriedOut(t *testing.T, user string, seq uint64, account, file, failure string) string {
	t.Helper()
	ctx := context.Background()
	var typed map[string]string
	if account == "" {
		typed = map[string]string{"COURIER_ACCOUNT": "typed@courier.example.test"}
	}
	if _, err := y.st.ApproveSetup(ctx, "yd-crm", seq, 1, typed, account, user, true); err != nil {
		t.Fatal(err)
	}
	rec, err := y.st.ClaimCarry(ctx, "boot")
	if err != nil || rec == nil || rec.Bead != "yd-crm" || rec.Ask != seq {
		t.Fatalf("the approval left the carrying out %+v, %v", rec, err)
	}
	if failure != "" {
		if err := y.st.FailCarry(ctx, rec.Bead, rec.Ask, "boot", failure); err != nil {
			t.Fatal(err)
		}
		return failure
	}
	result := strings.Join([]string{
		"applied pack courier to the yard: 1 created, 0 changed, 0 unchanged",
		store.AccountSetWords([]string{"COURIER_ACCOUNT"}) + " in " + file,
		"crew yardmaster may call courier: read",
	}, "\n")
	if err := y.st.CompleteCarry(ctx, rec.Bead, rec.Ask, "boot", result); err != nil {
		t.Fatal(err)
	}
	return result
}

// accountCase is a way the account of a setup comes to be set, or fails to
// be: by the candidate taken, by an account typed on a card that lists none,
// or in a carrying out that failed.
type accountCase struct {
	name   string
	typed  bool
	failed bool
}

var accountCases = []accountCase{
	{"a candidate taken", false, false}, {"an account typed", true, false}, {"a carrying out that failed", false, true}}

// carriedCase asks a setup of the user, who is the operator, and carries it
// out as the case says (carriedOut). candidate is the file of the source the
// yard holds already. Where the case types the account, that file is emptied
// first, so the card offers no candidate and sets the account in its own file.
// It answers the ask, what the carrying out recorded and the file the account
// is set in.
func (y *secretYard) carriedCase(t *testing.T, c accountCase, user, candidate string) (seq uint64, whole, setIn string) {
	t.Helper()
	account, setIn := "courier-existing", candidate
	if c.typed {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		account = ""
	}
	seq = y.askSetup(t, user)
	if c.typed {
		asked, err := y.st.Bead(context.Background(), "yd-crm")
		if err != nil {
			t.Fatal(err)
		}
		if card := setups(*asked)[seq]; len(card.Candidates) != 0 || card.AccountFile == "" {
			t.Fatalf("the card lists %+v, and writes to %q: want no candidate and a file", card.Candidates, card.AccountFile)
		} else {
			setIn = card.AccountFile
		}
	}
	failure := ""
	if c.failed {
		failure = "pack courier is applied, but the candidate courier-existing no longer holds COURIER_ACCOUNT as carded: its file is " + setIn +
			" now, not " + setIn + ". Ask for the account again, or point the source with yardr source set courier env_file=" + setIn + " in a terminal."
	}
	return seq, y.carriedOut(t, user, seq, account, setIn, failure), setIn
}

// ranOf is what the view v tells of the approval of the ask seq.
func ranOf(t *testing.T, v store.BeadView, seq uint64) *store.AskRan {
	t.Helper()
	for _, a := range v.Asks {
		if a.Ask == seq {
			if a.Ran == nil {
				t.Fatalf("the view tells nothing of the approval of ask %d: %+v", seq, a)
			}
			return a.Ran
		}
	}
	t.Fatalf("the view holds no ask %d", seq)
	return nil
}

// Bead tells the file the account of an approved setup is set in to a device
// of the operator and to no other, as it tells the candidates of the card
// (TestUserBeadTellsCandidatesToTheOperatorAlone): what the setup printed
// reads to any other device that the account is set, and not where. It does
// when the approval took the candidate, whose file it is, and when it typed
// the account, a card that lists no candidate to hold back. A carrying out
// that failed names the file and the candidate in the words that say how, and
// reads to any other device as failed and no more. The rest of what the
// setup printed and the rest of the view are the same, the result of a run
// ask is told as it is, and the store, which the socket serves, keeps the
// whole result.
func TestUserBeadTellsTheAccountFileToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	for _, c := range accountCases {
		t.Run(c.name, func(t *testing.T) {
			y, file := setupYard(t)
			seq, whole, file := y.carriedCase(t, c, "jo", file)
			// told reads the view as the device with the token has it, and fails
			// unless the result of the setup is the whole of what it printed
			// when listed, and the same without the file when not, or, of a
			// failure, failed and not the words of it. The candidate is in the
			// reply when listed and nowhere in it when not; a typed account's
			// file is the card's, which every device is told.
			told := func(who, token string, listed bool) store.BeadView {
				t.Helper()
				v, raw := y.bead(t, token)
				want := whole
				if !listed {
					want = strings.Replace(whole, " in "+file, "", 1)
				}
				got := ranOf(t, v, seq)
				switch {
				case got.Failed != c.failed || !slices.Equal(got.Argv, []string{"setup", "courier"}):
					t.Errorf("%s is told the carrying out %+v, want one that failed: %v", who, got, c.failed)
				case c.failed && !listed:
					if got.Result == "" || got.Result == whole {
						t.Errorf("%s is told the failure %q", who, got.Result)
					}
				case got.Result != want:
					t.Errorf("%s is told the result %q, want %q", who, got.Result, want)
				}
				if !c.typed {
					for _, secret := range []string{file, candidateAddress, "courier-existing"} {
						if strings.Contains(raw, secret) != listed {
							t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
						}
					}
				}
				return v
			}

			stored, err := y.st.Bead(ctx, "yd-crm")
			if err != nil {
				t.Fatal(err)
			}
			if got := ranOf(t, *stored, seq).Result; got != whole {
				t.Fatalf("the store tells the result %q, want %q", got, whole)
			}
			jos := told("jo's device", y.jo, true)
			sams := told("sam's device", y.sam, false)

			// UserAsks tells the result whole to the operator's device, as Bead
			// does. The ask is put to jo, so sam's queue has none of it; the queue
			// of a user who is not the operator now is
			// TestUserAsksTellsTheSetupToTheOperatorAlone's.
			if queue := call(t, y.h, y.jo, "UserAsks", `{}`); !strings.Contains(queue, file) {
				t.Errorf("the operator's queue lacks the file of the account: %s", queue)
			}
			if queue := call(t, y.h, y.sam, "UserAsks", `{}`); strings.Contains(queue, file) {
				t.Errorf("sam's queue holds the file of the account: %s", queue)
			}

			// What sam is not told is that file: the same view, but for the
			// candidates of the card, which the other test holds.
			for _, v := range []*store.BeadView{&jos, &sams} {
				for i := range v.Asks {
					if v.Asks[i].Setup != nil {
						v.Asks[i].Setup.Candidates, v.Asks[i].Setup.Does = nil, nil
					}
					if v.Asks[i].Ran != nil {
						v.Asks[i].Ran.Result = ""
					}
				}
			}
			if !reflect.DeepEqual(jos, sams) {
				t.Errorf("the view of sam's device is not the operator's but for the file:\n%+v\nwant\n%+v", sams, jos)
			}

			// The result of a run ask is the commands' and names no account's
			// file: it is told as the store has it, to every device.
			run, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the CRM be called more often?", To: "jo",
				Context: []string{"The import was refused its 61st call of the hour."},
				Run:     &store.AskRun{Argv: []string{"source", "set", "crm", "calls_per_hour=120"}}}, "yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := y.st.ApproveAsk(ctx, "yd-crm", run, "jo", true); err != nil {
				t.Fatal(err)
			}
			if stored, err = y.st.Bead(ctx, "yd-crm"); err != nil {
				t.Fatal(err)
			}
			for who, token := range map[string]string{"jo's device": y.jo, "sam's device": y.sam} {
				v, _ := y.bead(t, token)
				if got, want := ranOf(t, v, run), ranOf(t, *stored, run); got.Result == "" || !reflect.DeepEqual(got, want) {
					t.Errorf("%s is told the run ask's result %+v, want %+v", who, got, want)
				}
			}

			// Each operator is told: the device of the one made an operator moves
			// with the role, and so does the device of one made a member.
			if _, err := y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo"); err != nil {
				t.Fatal(err)
			}
			told("jo's device, one operator's of two", y.jo, true)
			told("sam's device, an operator's now", y.sam, true)
			if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
				t.Fatal(err)
			}
			if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
				t.Fatal(err)
			}
			told("jo's device, a member's now", y.jo, false)
			told("sam's device, the one operator's", y.sam, true)
		})
	}
}

// toldAsks are the asks of one user's queue that the test of UserAsks checks: a
// setup ask carried out, with what the carrying out recorded and the file the
// account is set in, another setup ask that stands open, and the run ask that
// was approved (0 where the queue has none).
type toldAsks struct {
	open, done, run uint64
	whole, file     string
}

// blankHeld blanks what UserAsks holds back from a device that is not the
// operator's: the candidates and the sentence of a setup card, and the result of
// its approval. Two queues are equal after it when they differ in nothing else.
func blankHeld(q *store.AskQueue) {
	for i := range q.Open {
		if card := q.Open[i].Setup; card != nil {
			card.Candidates, card.Does = nil, nil
		}
	}
	for i := range q.Decided {
		d := &q.Decided[i]
		if d.Setup == nil {
			continue
		}
		d.Setup.Candidates, d.Setup.Does = nil, nil
		if d.Ran != nil {
			d.Ran.Result = ""
		}
	}
}

// UserAsks tells the candidates of a setup card, and the file the account of an
// approved setup is set in, to a device of the current operator and to no other,
// as Bead tells them (TestUserBeadTellsCandidatesToTheOperatorAlone and
// TestUserBeadTellsTheAccountFileToTheOperatorAlone). A setup ask is put to the
// operator, but the operator setting moves, and the user it was put to keeps the
// ask in their queue. So the current operator is told, whoever the ask was put
// to, and in a yard with no operator no device is. Every setup ask of the queue
// is held, the open and the decided. The rest of the queue is as the store has
// it, an approved run ask's result among it, and the store, which the socket
// serves, keeps the queue whole.
func TestUserAsksTellsTheSetupToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	for _, c := range accountCases {
		t.Run(c.name, func(t *testing.T) {
			y, candidate := setupYard(t)
			// queued reads the queue of user as the device with the token has it,
			// listed saying whether that device is the current operator's, and
			// fails unless:
			//   - the setup asks of ct, the open one and the carried out one, read
			//     as Bead tells the same device them;
			//   - the result of the carried out one is the whole of what was
			//     recorded when listed, and the same without the file when not, or,
			//     of a failure, failed and not the words of it;
			//   - the candidate is in the reply when listed and nowhere in it when
			//     not (a typed account's file is the card's, which every device is
			//     told);
			//   - the rest of the queue is the store's, which still has the whole.
			queued := func(who, token, user string, ct toldAsks, listed bool) {
				t.Helper()
				got, raw := y.queue(t, token)
				var standing *store.OpenAsk
				for i := range got.Open {
					if got.Open[i].Ask == ct.open {
						standing = &got.Open[i]
					}
				}
				var settled, approved *store.DecidedAsk
				for i := range got.Decided {
					switch got.Decided[i].Ask {
					case ct.done:
						settled = &got.Decided[i]
					case ct.run:
						approved = &got.Decided[i]
					}
				}
				if standing == nil || standing.Setup == nil || settled == nil || settled.Setup == nil || settled.Ran == nil || ct.run != 0 && (approved == nil || approved.Ran == nil) {
					t.Fatalf("%s is told the queue %s, want the setup ask %d open, %d carried out and the run ask %d", who, raw, ct.open, ct.done, ct.run)
				}
				want := ct.whole
				if !listed {
					want = strings.Replace(ct.whole, " in "+ct.file, "", 1)
				}
				switch {
				case settled.Ran.Failed != c.failed || !slices.Equal(settled.Ran.Argv, []string{"setup", "courier"}):
					t.Errorf("%s is told the carrying out %+v, want one that failed: %v", who, settled.Ran, c.failed)
				case c.failed && !listed:
					if settled.Ran.Result == "" || settled.Ran.Result == ct.whole {
						t.Errorf("%s is told the failure %q", who, settled.Ran.Result)
					}
				case settled.Ran.Result != want:
					t.Errorf("%s is told the result %q, want %q", who, settled.Ran.Result, want)
				}
				if !c.typed {
					for _, card := range []*store.SetupAsked{standing.Setup, settled.Setup} {
						if (len(card.Candidates) > 0) != listed {
							t.Errorf("%s is told the candidates %+v of a card, want them: %v", who, card.Candidates, listed)
						}
					}
					for _, secret := range []string{ct.file, candidateAddress, "courier-existing"} {
						if strings.Contains(raw, secret) != listed {
							t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
						}
					}
				}
				// One device is not told by one call what it is not by the other.
				view, _ := y.bead(t, token)
				for _, a := range view.Asks {
					switch a.Ask {
					case ct.open:
						if !reflect.DeepEqual(a.Setup, standing.Setup) {
							t.Errorf("%s is told the open card %+v by UserAsks and %+v by Bead", who, standing.Setup, a.Setup)
						}
					case ct.done:
						if !reflect.DeepEqual(a.Setup, settled.Setup) || !reflect.DeepEqual(a.Ran, settled.Ran) {
							t.Errorf("%s is told the carried out ask %+v %+v by UserAsks and %+v %+v by Bead", who, settled.Setup, settled.Ran, a.Setup, a.Ran)
						}
					}
				}
				stored, err := y.st.UserAsks(ctx, user)
				if err != nil {
					t.Fatal(err)
				}
				// The store's queue as the reply carries it, to compare like with like.
				b, err := json.Marshal(stored)
				if err != nil {
					t.Fatal(err)
				}
				var whole store.AskQueue
				if err := json.Unmarshal(b, &whole); err != nil {
					t.Fatal(err)
				}
				for _, d := range whole.Decided {
					if d.Ask == ct.done && (d.Ran == nil || d.Ran.Result != ct.whole) {
						t.Errorf("the store tells the result %+v after %s was told, want %q", d.Ran, who, ct.whole)
					}
				}
				if approved != nil && approved.Ran.Result == "" {
					t.Errorf("%s is told no result of the run ask: %+v", who, approved)
				}
				if !listed {
					blankHeld(&got)
					blankHeld(&whole)
				}
				if !reflect.DeepEqual(got, whole) {
					t.Errorf("%s is told the queue\n%+v\nwant the store's\n%+v", who, got, whole)
				}
			}

			// jo, the operator, has a setup carried out, another asked, and a run
			// ask approved.
			done, whole, file := y.carriedCase(t, c, "jo", candidate)
			jos := toldAsks{open: y.askSetup(t, "jo"), done: done, whole: whole, file: file}
			run, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the CRM be called more often?", To: "jo",
				Context: []string{"The import was refused its 61st call of the hour."},
				Run:     &store.AskRun{Argv: []string{"source", "set", "crm", "calls_per_hour=120"}}}, "yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := y.st.ApproveAsk(ctx, "yd-crm", run, "jo", true); err != nil {
				t.Fatal(err)
			}
			jos.run = run
			queued("jo's device", y.jo, "jo", jos, true)

			// The setting moves to sam and jo is made a member. The asks put to
			// jo stay in jo's queue, and jo's device is no longer told them. A
			// setup ask can be put to sam now, and sam's device is told it whole.
			if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
				t.Fatal(err)
			}
			if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
				t.Fatal(err)
			}
			queued("jo's device, no longer the operator's", y.jo, "jo", jos, false)
			done, whole, file = y.carriedCase(t, c, "sam", candidate)
			sams := toldAsks{open: y.askSetup(t, "sam"), done: done, whole: whole, file: file}
			queued("sam's device, the operator's now", y.sam, "sam", sams, true)

			// Made an operator again, jo's device is told jo's queue whole.
			if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleOperator}, "sam"); err != nil {
				t.Fatal(err)
			}
			queued("jo's device, an operator's again", y.jo, "jo", jos, true)
		})
	}
}

// replayed is what the device with the token is sent of the log from its
// start to its head: the data of each event that carries the operator's
// configuration (isConfiguration), in order, and the lines of the whole
// stream. The stream stays open, so it is read to the newest event and no
// further.
func (y *secretYard) replayed(t *testing.T, base, token string) (configs []map[string]any, raw string) {
	t.Helper()
	head, err := y.st.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lines, resp := follow(t, base, token, 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the stream is answered %s", resp.Status)
	}
	var all []string
	for {
		line := next(t, lines)
		if line == "" {
			t.Fatalf("the stream ended before the head of the log, seq %d", head)
		}
		var e store.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("the stream sent %q: %v", line, err)
		}
		all = append(all, line)
		if isConfiguration(e.Kind) {
			configs = append(configs, e.Data)
		}
		if e.Seq >= head {
			return configs, strings.Join(all, "\n")
		}
	}
}

// isConfiguration reports whether kind is an event whose files and command
// the stream tells to a device of the operator alone (deviceEvent).
func isConfiguration(kind string) bool {
	return kind == "source_set" || kind == "user_set" || kind == "hook_blocked"
}

// The stream tells the files and the command of the operator's configuration,
// in the events that set or name them, to a device of the operator and to no
// other, as Bead tells the file of an account
// (TestUserBeadTellsTheAccountFileToTheOperatorAlone): to any other device the
// event is the source, the user or the hook without them, replayed from the
// start or sent as it happens, and so it is to every device of a yard with no
// operator. A source_set is so for each write of a file: a source pointed at
// one, as a setup that took a candidate points the source its pack added at
// the candidate's file, and a source with none, for which the server chooses
// one when a value is given; and for the command the source mints its token
// with. A user_set is so for the file of a user, to the device of that user
// as to any other. A hook_blocked is so for the secret file it names and for
// its reason, which is the error of reading that file and names it too. What
// else they carry is told as it is (the limit of a source, the parts a user is
// told of), and the store, which the socket serves, keeps every event whole.
func TestUserStreamTellsTheOperatorsConfigurationToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	y, file := setupYard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// What the server's carrier does for the candidate taken
	// (carrySetupAccountPicked), after the pack added the source.
	_, err := y.st.PutSource(ctx, "courier.yaml", apiSetupSource("courier"), "jo")
	must(err)
	_, err = y.st.SetSource(ctx, "courier", store.SourceEnvFile, file, "jo")
	must(err)
	_, err = y.st.GiveSecret(ctx, "yd-crm", y.ask, map[string]string{"CRM_TOKEN": "s3cret-0f-the-yard"}, "jo", true)
	must(err)
	_, err = y.st.SetSource(ctx, "crm", store.SourceCallsPerHour, "120", "jo")
	must(err)
	// A hook whose secret file the receiver finds open to others, and says so
	// in the words it logs the block with: the error of reading the file.
	dir := filepath.Dir(file)
	secret := filepath.Join(dir, "linear.secret")
	must(os.WriteFile(secret, []byte("whsec-0f-the-yard\n"), 0o600))
	must(os.Chmod(secret, 0o644))
	_, _, open := config.ReadSecretFile(secret)
	if open == nil || !strings.Contains(open.Error(), secret) {
		t.Fatalf("reading a secret file that is open to others gave %v, want an error that names it", open)
	}
	_, err = y.st.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: secret}, "jo")
	must(err)

	// A device sent the log as it happens is sent the same: its streams are
	// open before the next writes, which move a source to a file of its own
	// and to a command, point a user at a file and block the hook.
	base := listen(t, y.u, 0)
	head, err := y.st.Head(ctx)
	must(err)
	jos, _ := follow(t, base, y.jo, head)
	sams, _ := follow(t, base, y.sam, head)
	moved := filepath.Join(dir, "crm-moved.env")
	_, err = y.st.SetSource(ctx, "crm", store.SourceEnvFile, moved, "jo")
	must(err)
	const mint = "/home/jo/bin/crm-mint --org acme"
	_, err = y.st.SetSource(ctx, "crm", store.SourceMintCommand, mint, "jo")
	must(err)
	// sam's own device is told no more of their file than jo's of anyone's.
	address := filepath.Join(dir, "sam.env")
	_, err = y.st.SetUser(ctx, "sam", map[string]string{store.UserEnvFile: address, store.UserTell: "mail"}, "jo")
	must(err)
	must(y.st.BlockHook(ctx, "linear", open.Error()))

	const before = 4 // the writes above that came before the streams were open
	whole := []map[string]any{
		{"source": "courier-existing", "env_file": file},
		{"source": "courier", "env_file": file},
		{"source": "crm", "env_file": y.file},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm", "env_file": moved},
		{"source": "crm", "mint_command": mint},
		{"user": "sam", "env_file": address, "tell": "mail"},
		{"hook": "linear", "path": secret, "reason": open.Error()},
	}
	without := []map[string]any{
		{"source": "courier-existing"},
		{"source": "courier"},
		{"source": "crm"},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm"},
		{"source": "crm"},
		{"user": "sam", "tell": "mail"},
		{"hook": "linear"},
	}
	var stored []map[string]any
	log, err := y.st.EventsSince(ctx, 0, 1000)
	must(err)
	for _, e := range log {
		if isConfiguration(e.Kind) {
			stored = append(stored, e.Data)
		}
	}
	if !reflect.DeepEqual(stored, whole) {
		t.Fatalf("the store keeps the configuration events %v, want %v", stored, whole)
	}
	// live is the data of the next event of a stream that was open for it.
	live := func(who string, lines <-chan string) map[string]any {
		t.Helper()
		var e store.Event
		if line := next(t, lines); json.Unmarshal([]byte(line), &e) != nil || !isConfiguration(e.Kind) {
			t.Fatalf("%s is sent %q, want the next write of the operator's configuration", who, line)
		}
		return e.Data
	}
	for i := before; i < len(whole); i++ {
		if got := live("jo's device", jos); !reflect.DeepEqual(got, whole[i]) {
			t.Errorf("jo's device is sent %v as it happens, want %v", got, whole[i])
		}
		if got := live("sam's device", sams); !reflect.DeepEqual(got, without[i]) {
			t.Errorf("sam's device is sent %v as it happens, want %v", got, without[i])
		}
	}

	// told replays the log to the device with the token, and fails unless it
	// is sent the configuration events whole when listed, and not a file, the
	// command or the reason of the yard's anywhere in the stream when not.
	told := func(who, token string, listed bool) {
		t.Helper()
		configs, raw := y.replayed(t, base, token)
		want := without
		if listed {
			want = whole
		}
		// The role changes below come after, as user_set events of their
		// own, and name no file.
		configs = configs[:min(len(configs), len(want))]
		if !reflect.DeepEqual(configs, want) {
			t.Errorf("%s is sent the configuration events %v, want %v", who, configs, want)
		}
		for _, held := range []string{file, y.file, moved, mint, address, secret, open.Error()} {
			if strings.Contains(raw, held) != listed {
				t.Errorf("%s is sent %q: %v, want %v", who, held, !listed, listed)
			}
		}
	}
	told("jo's device", y.jo, true)
	told("sam's device", y.sam, false)

	// Each operator is told: the device of one made an operator moves with
	// the role, the past events with it, and so does one made a member.
	_, err = y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo")
	must(err)
	told("jo's device, one operator's of two", y.jo, true)
	told("sam's device, an operator's now", y.sam, true)
	must(y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"))
	_, err = y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam")
	must(err)
	told("jo's device, a member's now", y.jo, false)
	told("sam's device, the one operator's", y.sam, true)
}

// Users, GetUser, Crew and GetCrew tell the files and the commands of the
// operator's configuration to a device of the operator and to no other, as the
// stream tells the events that set them
// (TestUserStreamTellsTheOperatorsConfigurationToTheOperatorAlone). To any
// other device a user reads without the file their address is read from and
// without the words of a send that found it unfit, which name it; a crew member
// without the directory it runs in, without the files and commands its config
// names, and without the handles of its sessions, which name the directory and
// the yardr it was started with. So it does to the device of a user made a
// member, and to the user's own device, which is told no more of its file
// than another's. The rest of each record is told as it is, and the socket,
// which the terminal and the agents use, keeps the whole record.
func TestUserCallsTellTheOperatorsConfigurationToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	y := newSecretYard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Dir(y.db)
	path := func(elem ...string) string { return filepath.Join(append([]string{dir}, elem...)...) }

	// Both users are told through a source by the file their address is read
	// from, and a send to each found the file unfit, in the words the daemon
	// records it in (notify.userEnv): they name the file.
	_, err := y.st.PutSource(ctx, "push.yaml", addressDecl, "jo")
	must(err)
	files := map[string]string{"jo": path("users", "jo.env"), "sam": path("users", "sam.env")}
	const unfit = "does not set PUSH_USER"
	for _, user := range []string{"jo", "sam"} {
		_, err = y.st.SetUser(ctx, user, map[string]string{store.UserNotify: "push", store.UserEnvFile: files[user], store.UserTell: "mail"}, "jo")
		must(err)
		must(y.st.Mail(ctx, user, "the nightly run is done", "yardmaster"))
	}
	pushes, err := y.st.Pushes(ctx)
	must(err)
	if len(pushes) != 2 {
		t.Fatalf("the yard has %d pushes waiting, want one for each user: %+v", len(pushes), pushes)
	}
	sent := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, p := range pushes {
		must(y.st.PushTried(ctx, p.User, []uint64{p.Seq}, store.PushResult{At: sent, Source: "push", Reason: store.PushNotSetUp,
			Error: fmt.Sprintf("user %s: env_file %s %s", p.User, files[p.User], unfit)}))
	}

	// A crew member whose config names a file or a command for each thing it
	// reads one for, beside the names and flags a device is told; with a
	// session running, one being started, one left behind and a conversation,
	// each with a handle that names a pane and the directory.
	workdir, yardr := path("crew", "signalman"), path("bin", "yardr")
	_, err = y.st.PutCrew(ctx, store.CrewMember{Name: "signalman", Runner: "herdr", Dir: workdir, Config: map[string]any{
		"kind": "claude", "model": "sonnet", "provider": "claude", "trust": false, "greet": "jo",
		store.CrewForKey: "jo,sam", store.OpenThreads: "push", store.SetState: "push", store.MailPeers: "north",
		"args": []any{"--add-dir", path("extra")}, "prompt": path("roles", "signalman.md"),
		"env_file": path("crew", "signalman.env"), "env_command": []any{path("bin", "signalman-env"), "--org", "acme"},
		store.EnvForPrefix + "jo": path("crew", "jo.env"), store.EnvForPrefix + "sam": path("crew", "sam.env"),
	}}, "jo")
	must(err)
	must(y.st.CrewStarted(ctx, "signalman", map[string]any{"agent": "south-signalman", "pane": "pane-line", "dir": workdir,
		"first_prompt": "Run `" + yardr + " prime --crew signalman` first.", store.HarnessLoginKey: "Select login method:"}))
	must(y.st.CrewStarting(ctx, "signalman", map[string]any{"pane": "pane-next", "dir": workdir + "-next"}))
	must(y.st.CrewLeft(ctx, "signalman", []map[string]any{{"pane": "pane-left", "dir": workdir + "-left"}}))
	must(y.st.CrewStartedFor(ctx, "signalman", "sam", map[string]any{"pane": "pane-sam", "dir": workdir, "for": "sam"}))

	// held are the words of the users and of the crew a device that is not the
	// operator's is told none of.
	heldOfUsers := []string{files["jo"], files["sam"], unfit}
	heldOfCrew := []string{workdir, yardr, "pane-line", "pane-next", "pane-left", "pane-sam",
		path("roles", "signalman.md"), path("crew", "signalman.env"), path("bin", "signalman-env"), "--org",
		path("crew", "jo.env"), path("crew", "sam.env"), "--add-dir", path("extra")}
	held := slices.Concat(heldOfUsers, heldOfCrew)

	// The store keeps the whole records, and the socket serves them: the
	// fixture really holds every word, and the socket tells every one of them.
	crew, err := y.st.Crew(ctx)
	must(err)
	socket := Handler(y.st, nil)
	for _, c := range []struct {
		call  string
		words []string
	}{{"Users", heldOfUsers}, {"Crew", heldOfCrew}} {
		raw := ask(socket, http.MethodPost, "/call/"+c.call, "", strings.NewReader("{}")).Body.String()
		for _, word := range c.words {
			if !strings.Contains(raw, word) {
				t.Errorf("the socket's %s does not tell %q, want the whole record", c.call, word)
			}
		}
	}

	// What a device that is not the operator's is told of a record: the same
	// but for what is held. These are the cuts the doc promises, written out
	// here and not taken from the code that makes them.
	userToldTo := func(u store.User) store.User {
		u.EnvFile = ""
		if u.LastPush != nil {
			last := *u.LastPush
			last.Error = ""
			u.LastPush = &last
		}
		return u
	}
	crewToldTo := func(m store.CrewMember) store.CrewMember {
		m.Dir = ""
		kept := map[string]any{}
		for _, key := range []string{"kind", "model", "provider", "trust", "greet", store.CrewForKey, store.OpenThreads, store.SetState, store.MailPeers} {
			if v, ok := m.Config[key]; ok {
				kept[key] = v
			}
		}
		m.Config = kept
		m.Handle, m.Starting, m.Left = nil, nil, nil
		conversations := map[string]store.CrewSession{}
		for user, session := range m.Conversations {
			session.Handle, session.Starting, session.Left = nil, nil, nil
			conversations[user] = session
		}
		m.Conversations = conversations
		return m
	}
	// told fails unless the four calls of the device with the token are
	// answered with the whole records when whole, and when not with them as a
	// device that is not the operator's is told them, and none of the held
	// words anywhere in the answers.
	told := func(who, token, user string, whole bool) {
		t.Helper()
		own, err := y.st.GetUser(ctx, user)
		must(err)
		member, err := y.st.GetCrew(ctx, "signalman")
		must(err)
		// The users are read again, since a user's role is in their record.
		users, err := y.st.Users(ctx)
		must(err)
		wantUsers, wantOwn, wantCrew, wantMember := users, *own, crew, *member
		if !whole {
			wantUsers, wantCrew = nil, nil
			for _, u := range users {
				wantUsers = append(wantUsers, userToldTo(u))
			}
			for _, m := range crew {
				wantCrew = append(wantCrew, crewToldTo(m))
			}
			wantOwn, wantMember = userToldTo(wantOwn), crewToldTo(wantMember)
		}
		for _, c := range []struct {
			method, args string
			want         any
		}{
			{"Users", `{}`, wantUsers},
			{"GetUser", `{}`, wantOwn},
			{"Crew", `{}`, wantCrew},
			{"GetCrew", `{"name":"signalman"}`, wantMember},
		} {
			raw := call(t, y.h, token, c.method, c.args)
			var answer struct {
				Result json.RawMessage
				Error  *wireError
			}
			if err := json.Unmarshal([]byte(raw), &answer); err != nil || answer.Error != nil {
				t.Fatalf("%s is answered %s to %s: %v", c.method, raw, who, err)
			}
			wantJSON, err := json.Marshal(c.want)
			must(err)
			var got, want any
			if json.Unmarshal(answer.Result, &got) != nil || json.Unmarshal(wantJSON, &want) != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s is answered %s to %s, want %s", c.method, answer.Result, who, wantJSON)
			}
			for _, word := range held {
				if strings.Contains(raw, word) && !whole {
					t.Errorf("%s is answered to %s with %q in it", c.method, who, word)
				}
			}
		}
	}
	told("jo's device", y.jo, "jo", true)
	told("sam's device", y.sam, "sam", false)

	// What the yard does not have is answered as it is on the socket.
	if raw := call(t, y.h, y.sam, "GetCrew", `{"name":"nobody"}`); !strings.Contains(raw, `"code":"not-found"`) {
		t.Errorf("GetCrew of a crew member the yard does not have is answered %s to sam's device, want not-found", raw)
	}

	// Each operator is told: the device of one made an operator moves with the
	// role, and so does one made a member.
	_, err = y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo")
	must(err)
	told("jo's device, one operator's of two", y.jo, "jo", true)
	told("sam's device, an operator's now", y.sam, "sam", true)
	must(y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"))
	_, err = y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam")
	must(err)
	told("jo's device, a member's now", y.jo, "jo", false)
	told("sam's device, the one operator's", y.sam, "sam", true)
}
