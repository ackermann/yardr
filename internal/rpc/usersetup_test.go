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
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
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

// Bead tells a setup card's account file and candidates, each a file of the
// yard's with the values it holds, to a device of the operator and to no
// other: to a device of anyone else the card reads with no file and no
// candidates, and so it does to every device of a yard with no operator. It is
// the operator now who is told, and every ask of the view is held back, the
// decided ones too. The rest of the view is the same, and the store, which
// the socket serves, keeps the card whole.
func TestUserBeadTellsCandidatesToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	y, file := setupYard(t)
	first := y.askSetup(t, "jo")
	want := store.AskCandidate{Source: "courier-existing", File: file, Values: map[string]string{"COURIER_ACCOUNT": candidateAddress}}
	whole, err := y.st.Bead(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	if got := setups(*whole)[first].Candidates; !reflect.DeepEqual(got, []store.AskCandidate{want}) {
		t.Fatalf("the store lists the candidates %+v, want %+v", got, want)
	}
	// told reads the view as the device with the token has it, and fails
	// unless the setup asks it holds match the store's candidates and account
	// file when listed, and expose neither of them when not.
	told := func(who, token string, asks []uint64, listed bool) store.BeadView {
		t.Helper()
		v, raw := y.bead(t, token)
		cards := setups(v)
		if len(cards) != len(asks) {
			t.Fatalf("%s is told the setup asks %v, want %v", who, slices.Sorted(maps.Keys(cards)), asks)
		}
		for _, seq := range asks {
			card := cards[seq]
			stored := setups(*whole)[seq]
			if card == nil {
				t.Fatalf("%s is told no card for ask %d", who, seq)
			}
			if stored == nil {
				t.Fatalf("the store has no card for ask %d", seq)
			}
			if listed && (!reflect.DeepEqual(card.Candidates, stored.Candidates) || card.AccountFile != stored.AccountFile) {
				t.Errorf("%s is told the candidates %+v and account file %q of ask %d, want %+v and %q", who, card.Candidates, card.AccountFile, seq, stored.Candidates, stored.AccountFile)
			}
			if !listed && (len(card.Candidates) != 0 || card.AccountFile != "") {
				t.Errorf("%s is told the candidates %+v and account file %q of ask %d", who, card.Candidates, card.AccountFile, seq)
			}
			if stored.AccountFile != "" && strings.Contains(strings.Join(card.Does, "\n"), stored.AccountFile) != listed {
				t.Errorf("%s is told the account file in the sentences of ask %d: %v, want %v:\n%s", who, seq, strings.Contains(strings.Join(card.Does, "\n"), stored.AccountFile), listed, strings.Join(card.Does, "\n"))
			}
		}
		// Not in the candidates, and not anywhere else of the reply either:
		// the card says them in its sentences too.
		secrets := []string{file, candidateAddress, "courier-existing"}
		for _, seq := range asks {
			if path := setups(*whole)[seq].AccountFile; path != "" {
				secrets = append(secrets, path)
			}
		}
		for _, secret := range secrets {
			if strings.Contains(raw, secret) != listed {
				t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
			}
		}
		if strings.Contains(raw, `"account_file"`) != listed {
			t.Errorf("%s is told the account_file field: %v, want %v", who, !listed, listed)
		}
		return v
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

	// What sam is not told is the candidates and the card's file: the same
	// view, the card's other sentences and options and fields as the operator
	// has them.
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
		t.Errorf("%d sentences of sam's card differ from the operator's, want the one that names the candidate and the file", differ)
	}
	for _, v := range []*store.BeadView{&jos, &sams} {
		for i := range v.Asks {
			if v.Asks[i].Setup != nil {
				v.Asks[i].Setup.Candidates, v.Asks[i].Setup.Does = nil, nil
				v.Asks[i].Setup.AccountFile = ""
			}
		}
	}
	if !reflect.DeepEqual(jos, sams) {
		t.Errorf("the view of sam's device is not the operator's but for the candidates and the file:\n%+v\nwant\n%+v", sams, jos)
	}

	// Every ask of the view: the first is decided with a candidate, and the
	// second stands open with no candidate. Both cards still name their file
	// only to the operator.
	if err := y.st.AddAnswer(ctx, "yd-crm", store.Answer{Ask: first, Decline: true, Words: "not this week"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	second := y.askSetup(t, "jo")
	whole, err = y.st.Bead(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
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
			// failure, failed and not the words of it. The file the account is
			// set in is in the reply when listed and nowhere in it when not; a
			// typed account's file is the card's, which a device of the operator
			// alone is told. The candidate is so where the card lists one.
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
				secrets := []string{file}
				if !c.typed {
					secrets = append(secrets, candidateAddress, "courier-existing")
				}
				for _, secret := range secrets {
					if strings.Contains(raw, secret) != listed {
						t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
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

			// What sam is not told is that file, the card's and the result's: the
			// same view, but for the candidates of the card, which the other
			// test holds.
			for _, v := range []*store.BeadView{&jos, &sams} {
				for i := range v.Asks {
					if v.Asks[i].Setup != nil {
						v.Asks[i].Setup.Candidates, v.Asks[i].Setup.Does = nil, nil
						v.Asks[i].Setup.AccountFile = ""
					}
					if v.Asks[i].Ran != nil {
						v.Asks[i].Ran.Result = ""
					}
				}
			}
			if !reflect.DeepEqual(jos, sams) {
				t.Errorf("the view of sam's device is not the operator's but for the file:\n%+v\nwant\n%+v", sams, jos)
			}

			// A run ask is told as its asker typed it, to every device. What the
			// yard wrote of it from its own records, which names the files and the
			// settings it is about, and what it printed when approved, are the
			// operator's: the same sentences without them, to any other device.
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
			for _, c := range []struct {
				who, token string
				whole      bool
			}{{"jo's device", y.jo, true}, {"sam's device", y.sam, false}} {
				v, raw := y.bead(t, c.token)
				got, want := ranOf(t, v, run), ranOf(t, *stored, run)
				card, whole := runOf(t, v, run), runOf(t, *stored, run)
				if len(whole.Does) == 0 || whole.Back == "" || want.Result == "" {
					t.Fatalf("the store tells the run ask %+v and its result %+v, want the yard's words of both", whole, want)
				}
				words := append(slices.Clone(whole.Does), whole.Back, want.Result)
				if !c.whole {
					want.Result = ""
					cut := *whole
					cut.Does, cut.Back, cut.BackBy, cut.Carried = []string{}, "", "", nil
					whole = &cut
				}
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(card, whole) {
					t.Errorf("%s is told the run ask %+v and its result %+v, want %+v and %+v", c.who, card, got, whole, want)
				}
				for _, said := range words {
					if strings.Contains(raw, said) != c.whole {
						t.Errorf("%s is told %q: %v, want %v", c.who, said, !c.whole, c.whole)
					}
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
// operator's: the file and the candidates and the sentence of a setup card and
// the result of its approval, and what the yard wrote of a run ask
// (withoutRunWords): the sentences, the command that takes it back, what it
// carries and the plan of its pack, and the result of its approval. Two queues
// are equal after it when they differ in nothing else.
func blankHeld(q *store.AskQueue) {
	blankRun := func(run *store.RunAsked) {
		if run == nil {
			return
		}
		run.Does, run.Back, run.BackBy, run.Carried = []string{}, "", "", nil
		if run.Pack != nil {
			run.Pack.Plan = nil
		}
	}
	for i := range q.Open {
		if card := q.Open[i].Setup; card != nil {
			card.Candidates, card.Does = nil, nil
			card.AccountFile = ""
		}
		blankRun(q.Open[i].Run)
	}
	for i := range q.Decided {
		d := &q.Decided[i]
		blankRun(d.Run)
		if d.Run != nil && d.Ran != nil {
			d.Ran.Result = ""
		}
		if d.Setup == nil {
			continue
		}
		d.Setup.Candidates, d.Setup.Does = nil, nil
		d.Setup.AccountFile = ""
		if d.Ran != nil {
			d.Ran.Result = ""
		}
	}
}

// runOf is the card of the run ask seq in the view, which fails the test when
// the view holds none.
func runOf(t *testing.T, v store.BeadView, seq uint64) *store.RunAsked {
	t.Helper()
	for _, a := range v.Asks {
		if a.Ask == seq {
			if a.Run == nil {
				t.Fatalf("the view tells ask %d as no run ask: %+v", seq, a)
			}
			return a.Run
		}
	}
	t.Fatalf("the view holds no ask %d", seq)
	return nil
}

// UserAsks tells the file of a setup card's typed account, the candidates of
// the card, and the file the account of an approved setup is set in, to a
// device of the current operator and to no other, as Bead tells them
// (TestUserBeadTellsCandidatesToTheOperatorAlone and
// TestUserBeadTellsTheAccountFileToTheOperatorAlone). A setup ask is put to the
// operator, but the operator setting moves, and the user it was put to keeps the
// ask in their queue. So the current operator is told, whoever the ask was put
// to, and in a yard with no operator no device is. Every setup ask of the queue
// is held, the open and the decided. A run ask is told as its asker typed it, and
// what the yard wrote of it and of its approval is the operator's, as in Bead
// (TestUserBeadTellsTheAccountFileToTheOperatorAlone). The rest of the queue is
// as the store has it, and the store, which the socket serves, keeps the queue
// whole.
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
			//   - the file the account is set in is in the reply when listed and
			//     nowhere in it when not; a typed account's file is the card's,
			//     which a device of the current operator alone is told. The
			//     candidate is so where the card lists one;
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
				}
				secrets := []string{ct.file}
				if !c.typed {
					secrets = append(secrets, candidateAddress, "courier-existing")
				}
				for _, secret := range secrets {
					if strings.Contains(raw, secret) != listed {
						t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !listed, listed, raw)
					}
				}
				for _, card := range []*store.SetupAsked{standing.Setup, settled.Setup} {
					if (card.AccountFile != "") != listed {
						t.Errorf("%s is told the card's file %q, want it: %v", who, card.AccountFile, listed)
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
				// A run ask is told as its asker typed it, to every device, and the
				// yard's words of it (what it changes, takes back and printed) to
				// the operator's alone.
				if approved != nil {
					cut := approved.Ran.Result == "" && len(approved.Run.Does) == 0 && approved.Run.Back == ""
					if cut == listed {
						t.Errorf("%s is told the run ask %+v and its result %+v, want the yard's words of it: %v", who, approved.Run, approved.Ran, listed)
					}
					if !slices.Equal(approved.Run.Argv, []string{"source", "set", "crm", "calls_per_hour=120"}) || approved.Run.Command == "" ||
						!slices.Equal(approved.Ran.Argv, approved.Run.Argv) {
						t.Errorf("%s is told the run ask %+v and its result %+v, want the command its asker typed", who, approved.Run, approved.Ran)
					}
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
	t.Run("typed values and paths", userRunAskTellsTypedValueAndPathToTheOperatorAlone)
}

// runPhoneDecl is the declaration a source add run ask of the test carries:
// commands-free, so the approval runs in its own write.
const runPhoneDecl = `name: phone
base: https://phone.example.com/v1
credential: { env: PHONE_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// Bead and UserAsks tell the typed words of a run ask that sets a setting or
// adds a source to a device of the operator and to no other, as they tell the
// yard's words of every run ask (TestUserAsksTellsTheSetupToTheOperatorAlone):
// to any other device the card reads with no argv and no command, and an
// approved ask's ran with no argv, the question and the command's help line
// only. A setting's value and a source's path as typed are the operator's
// configuration, as a stored value and a file of the yard's are. Every other
// row is told as its asker typed it, argv and command whole: names and numbers
// the stream and the calls tell every device already. The stream tells the
// approval's sentence likewise: a held row's noted event carries no answer to
// such a device, and a told row's carries it whole. The store keeps every ask
// and sentence whole.
func userRunAskTellsTypedValueAndPathToTheOperatorAlone(t *testing.T) {
	ctx := context.Background()
	y := newSecretYard(t)
	const value = "early-shift"
	const file = "phone.yaml"
	setting, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the yard's note change?", To: "jo",
		Context: []string{"The night shift ends at six now."},
		Run:     &store.AskRun{Argv: []string{"settings", "set", "operator_note", value}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	source, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the phone source be added?", To: "jo",
		Context: []string{"The night shift wants its calls told by phone."},
		Run: &store.AskRun{Argv: []string{"source", "add", file},
			Carried: []store.AskSource{{Name: "phone", Text: runPhoneDecl}}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	limit, err := y.st.AddAsk(ctx, "yd-crm", store.Ask{Question: "May the CRM be called more often?", To: "jo",
		Context: []string{"The import was refused its 61st call of the hour."},
		Run:     &store.AskRun{Argv: []string{"source", "set", "crm", "calls_per_hour=120"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint64{setting, source, limit} {
		if _, err := y.st.ApproveAsk(ctx, "yd-crm", seq, "jo", true); err != nil {
			t.Fatal(err)
		}
	}
	// runs are the run cards of the view's asks, by ask.
	runs := func(v store.BeadView) map[uint64]*store.RunAsked {
		out := map[uint64]*store.RunAsked{}
		for _, a := range v.Asks {
			if a.Run != nil {
				out[a.Ask] = a.Run
			}
		}
		return out
	}
	// rans are what the view's approved run asks ran, by ask.
	rans := func(v store.BeadView) map[uint64]*store.AskRan {
		out := map[uint64]*store.AskRan{}
		for _, a := range v.Asks {
			if a.Run != nil && a.Ran != nil {
				out[a.Ask] = a.Ran
			}
		}
		return out
	}
	whole := map[uint64][]string{
		setting: {"settings", "set", "operator_note", value},
		source:  {"source", "add", file},
		limit:   {"source", "set", "crm", "calls_per_hour=120"},
	}
	// told reads the view as the device with the token has it, and fails
	// unless the held rows read with no typed words when listed is false and
	// whole when true, and the told row reads whole either way. It answers
	// the view's cards and results, for the queue to read against.
	told := func(who, token string, listed bool) (map[uint64]*store.RunAsked, map[uint64]*store.AskRan) {
		t.Helper()
		v, raw := y.bead(t, token)
		cards, results := runs(v), rans(v)
		for seq, argv := range whole {
			held := seq != limit && !listed
			card, ran := cards[seq], results[seq]
			if card == nil || ran == nil {
				t.Fatalf("%s is told no run card and result for ask %d", who, seq)
			}
			if held && (len(card.Argv) != 0 || card.Command != "" || len(ran.Argv) != 0) {
				t.Errorf("%s is told the typed words %+v %q and ran %+v of ask %d", who, card.Argv, card.Command, ran.Argv, seq)
			}
			if !held && (!slices.Equal(card.Argv, argv) || card.Command == "" || !slices.Equal(ran.Argv, argv)) {
				t.Errorf("%s is told the typed words %+v %q and ran %+v of ask %d, want %+v", who, card.Argv, card.Command, ran.Argv, seq, argv)
			}
			if card.Description == "" {
				t.Errorf("%s is told no help line for ask %d", who, seq)
			}
			if held && ran.Result != "" {
				t.Errorf("%s is told the result %q of ask %d", who, ran.Result, seq)
			}
		}
		// The value and the path are nowhere in the reply but where they are
		// told; the told row's words are in every reply.
		for secret, told := range map[string]bool{value: listed, file: listed, "calls_per_hour=120": true} {
			if strings.Contains(raw, secret) != told {
				t.Errorf("%s is told %q: %v, want %v\n%s", who, secret, !told, told, raw)
			}
		}
		return cards, results
	}
	// queued reads the queue of the device's user as the device with the
	// token has it, and fails unless it reads as Bead told the same device.
	queued := func(who, token string, listed bool, cards map[uint64]*store.RunAsked, results map[uint64]*store.AskRan) {
		t.Helper()
		q, qraw := y.queue(t, token)
		decided := map[uint64]store.DecidedAsk{}
		for _, d := range q.Decided {
			decided[d.Ask] = d
		}
		for seq, argv := range whole {
			held := seq != limit && !listed
			d := decided[seq]
			if d.Run == nil || d.Ran == nil {
				t.Fatalf("%s is told no run card and result for ask %d of the queue", who, seq)
			}
			if held && (len(d.Run.Argv) != 0 || d.Run.Command != "") {
				t.Errorf("%s is told the typed words %+v %q of ask %d of the queue", who, d.Run.Argv, d.Run.Command, seq)
			}
			if !held && !slices.Equal(d.Run.Argv, argv) {
				t.Errorf("%s is told the typed words %+v of ask %d of the queue, want %+v", who, d.Run.Argv, seq, argv)
			}
			if !reflect.DeepEqual(d.Run, cards[seq]) || !reflect.DeepEqual(d.Ran, results[seq]) {
				t.Errorf("%s is told ask %d as %+v %+v by UserAsks and %+v %+v by Bead", who, seq, d.Run, d.Ran, cards[seq], results[seq])
			}
		}
		for secret, told := range map[string]bool{value: listed, file: listed, "calls_per_hour=120": true} {
			if strings.Contains(qraw, secret) != told {
				t.Errorf("%s is told %q in the queue: %v, want %v\n%s", who, secret, !told, told, qraw)
			}
		}
	}
	joCards, joRans := told("jo's device", y.jo, true)
	queued("jo's device", y.jo, true, joCards, joRans)
	told("sam's device", y.sam, false)
	// The asks were put to jo, so sam's queue holds none of them.
	if q, _ := y.queue(t, y.sam); len(q.Open) != 0 || len(q.Decided) != 0 {
		t.Fatalf("sam's device is told the queue %+v, want none of jo's asks", q)
	}
	// The setting moves to sam and jo is made a member. The asks put to jo
	// stay in jo's queue, and jo's device is no longer told their typed
	// words, by either call.
	if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam"); err != nil {
		t.Fatal(err)
	}
	joCards, joRans = told("jo's device, no longer the operator's", y.jo, false)
	queued("jo's device, no longer the operator's", y.jo, false, joCards, joRans)
	// The store keeps every ask whole, as the socket serves it.
	stored, err := y.st.Bead(ctx, "yd-crm")
	if err != nil {
		t.Fatal(err)
	}
	for seq, argv := range whole {
		if card := runs(*stored)[seq]; card == nil || !slices.Equal(card.Argv, argv) {
			t.Errorf("the store tells the typed words %+v of ask %d, want %+v", card, seq, argv)
		}
	}
	// The stream tells the approval's sentence of a held row to a device of
	// the operator alone, and of a told row to every device.
	base := listen(t, y.u, 0)
	head, err := y.st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answers := func(token string) []string {
		t.Helper()
		lines, resp := follow(t, base, token, 0)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the stream is answered %s", resp.Status)
		}
		var out []string
		for {
			line := next(t, lines)
			if line == "" {
				t.Fatalf("the stream ended before the head of the log, seq %d", head)
			}
			var e store.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("the stream sent %q: %v", line, err)
			}
			if e.Kind == "noted" {
				if answer, _ := e.Data["answer"].(string); answer != "" {
					out = append(out, answer)
				}
			}
			if e.Seq >= head {
				return out
			}
		}
	}
	sent := strings.Join(answers(y.jo), "\n")
	for secret, told := range map[string]bool{value: false, file: false, "calls_per_hour=120": true} {
		if strings.Contains(sent, secret) != told {
			t.Errorf("jo's device is told %q in an answer: %v, want %v\n%s", secret, !told, told, sent)
		}
	}
	sent = strings.Join(answers(y.sam), "\n")
	for _, secret := range []string{value, file, "calls_per_hour=120"} {
		if !strings.Contains(sent, secret) {
			t.Errorf("sam's device is told no answer naming %q:\n%s", secret, sent)
		}
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
// the stream tells to a device of the operator alone (deviceEvent): those that
// set or name a file or a command, and the kinds the stream once told every
// device whole, which carry a handle, a path, a directory or the words of an
// error that names one (see the table of store.EventDataToldKinds).
func isConfiguration(kind string) bool {
	switch kind {
	case "source_set", "user_set", "hook_blocked",
		"crew_started", "push_failed", "depot_updated", "backup_failed", "workspace_kept", "move_refused":
		return true
	}
	return false
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
//
// The kinds the stream told every device whole are held the same way: the
// handle of a crew member's session (crew_started), the error of a send that
// names the user's file (push_failed), a depot's path and test command
// (depot_updated), a backup's directory (backup_failed), the worktree of a
// kept workspace (workspace_kept) and the command of a move that was refused
// (move_refused). Each tells the name of what it is about, and the numbers and
// the words of its own that its kind lists.
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
	const regional = `name: regional
base: https://us.example.test
bases:
  eu: https://eu.example.test
credential: { env: REGION_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
	_, err = y.st.PutSource(ctx, "regional.yaml", regional, "jo")
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
	_, err = y.st.SetSource(ctx, "regional", store.SourceBase, "eu", "jo")
	must(err)
	// sam's own device is told no more of their file than jo's of anyone's.
	address := filepath.Join(dir, "sam.env")
	_, err = y.st.SetUser(ctx, "sam", map[string]string{store.UserEnvFile: address, store.UserTell: "mail"}, "jo")
	must(err)
	must(y.st.BlockHook(ctx, "linear", open.Error()))
	// What the stream told every device whole: a crew member's session (its
	// handle is the directory it started in and the yardr it was told to run),
	// a send that failed on the user's file, a depot, a backup that could not
	// be made, a workspace the daemon kept, and a command of an agent's that
	// the yard refused.
	crewDir := filepath.Join(dir, "crew", "yardmaster")
	handle := map[string]any{"dir": crewDir, "first_prompt": "Run " + filepath.Join(dir, "bin", "yardr") + " prime --crew yardmaster"}
	must(y.st.LogEvent(ctx, "", "crew_started", "yard", map[string]any{"crew": "yardmaster", "handle": handle}))
	sendErr := "user sam: env_file " + address + " is not there"
	must(y.st.LogEvent(ctx, "", "push_failed", "yard", map[string]any{"user": "sam", "source": "push", "reason": "not_set_up", "tries": 0, "error": sendErr}))
	depotDir := filepath.Join(dir, "depots", "wharf")
	must(y.st.PutDepot(ctx, store.Depot{Name: "wharf", Kind: store.DepotDir, Path: depotDir, Test: "make -C " + depotDir + " test"}, "jo"))
	backupDir := filepath.Join(dir, "backups")
	backupErr := "mkdir " + backupDir + ": permission denied"
	must(y.st.LogDispatch(ctx, "", "backup_failed", "yard", map[string]any{"every": "1h", "dir": backupDir, "reason": backupErr}))
	worktree := filepath.Join(dir, "worktrees", "yd-crm")
	keptWhy := "git status in " + worktree + ": changes"
	must(y.st.LogDispatch(ctx, "yd-crm", "workspace_kept", "yard", map[string]any{"session": "s1", "worktree": worktree, "reason": keptWhy}))
	refusedCommand := "yardr bead done yd-crm --outcome shipped"
	refusedWhy := "flow has no outcome shipped (see " + filepath.Join(dir, "flows", "default.yaml") + ")"
	must(y.st.LogDispatch(ctx, "yd-crm", "move_refused", "yard", map[string]any{"agent": "yd-crm-new", "group": "builders", "reason": "not_allowed",
		"session": "s1", "session_bead": "yd-crm", "stage": "new", "command": refusedCommand, "error": refusedWhy,
		"hint": "the agent ran this command and the yard refused it"}))

	const before = 4 // the writes above that came before the streams were open
	whole := []map[string]any{
		{"source": "courier-existing", "env_file": file},
		{"source": "courier", "env_file": file},
		{"source": "crm", "env_file": y.file},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm", "env_file": moved},
		{"source": "crm", "mint_command": mint},
		{"source": "regional", "base": "eu"},
		{"user": "sam", "env_file": address, "tell": "mail"},
		{"hook": "linear", "path": secret, "reason": open.Error()},
		{"crew": "yardmaster", "handle": map[string]any{"dir": crewDir, "first_prompt": handle["first_prompt"]}},
		{"user": "sam", "source": "push", "reason": "not_set_up", "tries": float64(0), "error": sendErr},
		{"depot": "wharf", "kind": store.DepotDir, "base": "", "path": depotDir, "test": "make -C " + depotDir + " test"},
		{"every": "1h", "dir": backupDir, "reason": backupErr},
		{"session": "s1", "worktree": worktree, "reason": keptWhy},
		{"agent": "yd-crm-new", "group": "builders", "reason": "not_allowed", "session": "s1", "session_bead": "yd-crm", "stage": "new",
			"command": refusedCommand, "error": refusedWhy, "hint": "the agent ran this command and the yard refused it"},
	}
	without := []map[string]any{
		{"source": "courier-existing"},
		{"source": "courier"},
		{"source": "crm"},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm"},
		{"source": "crm"},
		{"source": "regional"},
		{"user": "sam", "tell": "mail"},
		{"hook": "linear"},
		{"crew": "yardmaster"},
		{"user": "sam", "source": "push", "reason": "not_set_up", "tries": float64(0)},
		{"depot": "wharf", "kind": store.DepotDir, "base": ""},
		{"every": "1h"},
		{"session": "s1"},
		{"agent": "yd-crm-new", "group": "builders", "reason": "not_allowed", "session": "s1", "session_bead": "yd-crm", "stage": "new"},
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
		for _, held := range []string{file, y.file, moved, mint, address, secret, open.Error(),
			crewDir, sendErr, depotDir, backupDir, backupErr, worktree, keptWhy, refusedCommand, refusedWhy} {
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

// Calls beside those four told a file or a command of an operator's to any
// device that made them. Flows says why a depot's flows could not be read in the
// words of the read, which name the directory of the depot's repository: a device
// that is no operator's is told that they could not be, and the depot and
// its beads stay. Peers tells the address of each link and the words of the last
// send that failed, which carry it. DeliveryFiles tells why a file of a hook
// delivery was not taken, in the words of the fetch, which name the program of
// the source and the directory of the yard. UserNotifications tells a person the
// row of each event that became a notification for them, and the row names what
// the event carries: it is made when the event is logged, from what the person
// is told of the event, so a user who is no operator is not told the
// worktree of a workspace that was kept, as the stream does not tell it. Each
// moves with a user's role; the device of an operator is told what the socket
// tells.
func TestUserOtherCallsTellNoFileOrCommandToADeviceThatIsNotTheOperators(t *testing.T) {
	ctx := context.Background()
	y := newSecretYard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Dir(y.db)
	// A depot whose repository is not there: reading its flows fails, and the
	// words of the failure name the path.
	repo := filepath.Join(home, "depots", "wharf")
	must(y.st.PutDepot(ctx, store.Depot{Name: "wharf", Kind: store.DepotGit, Path: repo, Base: "main"}, "jo"))
	// A link whose last send failed, in the words of the client that made it.
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	must(err)
	const northURL = "http://10.0.0.5:8790"
	_, err = y.st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(key), URL: northURL}, "jo")
	must(err)
	_, err = y.st.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{store.PeerPing}}, "jo")
	must(err)
	queued, err := y.st.PeerSend(ctx, "north", store.PeerPing, json.RawMessage(`{}`), "jo")
	must(err)
	failure := `Post "` + northURL + `/peers": dial tcp 10.0.0.5:8790: connection refused`
	must(y.st.PeerSendFailed(ctx, "north", queued.Seq, failure, time.Now().Add(time.Hour), false))
	// A delivery whose file was not fetched, because the source's program did
	// not run.
	delivery, _, err := y.st.RecordHook(ctx, "telegram", map[string]any{"source": "telegram", "type": "message", "text": "[document]"}, []byte("{}"), nil)
	must(err)
	notTaken := "document not fetched: thread.file of source telegram: fork/exec " + filepath.Join(home, "sources", "telegram", "file.py") + ": permission denied"
	must(y.st.RecordDeliveryFiles(ctx, delivery, []store.BlobRef{{Name: "plans.pdf", Bytes: 1 << 20, NotTaken: notTaken}}))
	// Both users hear of a workspace the daemon kept, which names its worktree
	// and why in the words of git.
	worktree := filepath.Join(home, "worktrees", "yd-crm")
	for _, user := range []string{"jo", "sam"} {
		must(y.st.AddSubscription(ctx, user, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
			Where: map[string]string{"kind": "workspace_kept"}}, user))
	}
	keep := func() {
		t.Helper()
		must(y.st.LogDispatch(ctx, "yd-crm", "workspace_kept", "yard", map[string]any{"session": "s1", "worktree": worktree,
			"reason": "git status in " + worktree + ": changes"}))
	}

	// answer decodes the result of a call.
	answer := func(token, method, args string, result any) {
		t.Helper()
		if err := json.Unmarshal([]byte(call(t, y.h, token, method, args)), &struct {
			Result any `json:"result"`
		}{result}); err != nil {
			t.Fatal(err)
		}
	}
	// probes tell what the device with the token is told of each: the words the
	// operator's device is told, and what stands in their place for any other.
	probes := []struct {
		call, whole, held string
		get               func(token string) string
	}{
		{"Flows", repo, flowsHeld, func(token string) string {
			var board store.FlowsBoard
			answer(token, "Flows", `{}`, &board)
			for _, d := range board.Depots {
				if d.Name == "wharf" {
					return d.Error
				}
			}
			t.Fatalf("Flows tells no depot wharf: %+v", board)
			return ""
		}},
		{"Peers (the address)", northURL, "", func(token string) string {
			var peers []store.Peer
			answer(token, "Peers", `{}`, &peers)
			for _, p := range peers {
				if p.Name == "north" && slices.Equal(p.Send, []string{store.PeerPing}) {
					return p.URL
				}
			}
			t.Fatalf("Peers tells no link north that carries ping: %+v", peers)
			return ""
		}},
		{"Peers (the last failure)", failure, "", func(token string) string {
			var peers []store.Peer
			answer(token, "Peers", `{}`, &peers)
			for _, p := range peers {
				if p.Name == "north" {
					return p.Error
				}
			}
			t.Fatalf("Peers tells no link north: %+v", peers)
			return ""
		}},
		{"DeliveryFiles", notTaken, notTakenHeld, func(token string) string {
			var files []store.BlobRef
			answer(token, "DeliveryFiles", fmt.Sprintf(`{"seq":%d}`, delivery), &files)
			if len(files) != 1 || files[0].Name != "plans.pdf" || files[0].Bytes != 1<<20 {
				t.Fatalf("DeliveryFiles tells %+v, want the file's name and size", files)
			}
			return files[0].NotTaken
		}},
	}
	// told fails unless each call tells the device with the token words that
	// hold what the operator's are told when whole, and what stands in their
	// place when not.
	told := func(who, token string, whole bool) {
		t.Helper()
		for _, p := range probes {
			got := p.get(token)
			if whole && !strings.Contains(got, p.whole) || !whole && (got != p.held || strings.Contains(got, p.whole)) {
				t.Errorf("%s is told %q by %s, want %q: whole is %v", who, got, p.call, p.whole, whole)
			}
		}
	}
	// notified is the text of the last notification of the device's user.
	notified := func(token, user string) string {
		t.Helper()
		var page store.NotificationPage
		answer(token, "UserNotifications", `{"name":`+strconv.Quote(user)+`}`, &page)
		if len(page.Notifications) == 0 {
			t.Fatalf("%s has no notification", user)
		}
		return page.Notifications[len(page.Notifications)-1].Text
	}

	// The socket tells whole, and so does the operator's device.
	socket := ask(Handler(y.st, nil), http.MethodPost, "/call/Flows", "", strings.NewReader("{}")).Body.String()
	if !strings.Contains(socket, repo) {
		t.Fatalf("the socket's Flows does not name the repository %s: %s", repo, socket)
	}
	told("jo's device", y.jo, true)
	told("sam's device", y.sam, false)
	keep()
	for _, c := range []struct {
		who, token, user string
		whole            bool
	}{{"jo's device", y.jo, "jo", true}, {"sam's device", y.sam, "sam", false}} {
		got := notified(c.token, c.user)
		if !strings.Contains(got, "workspace_kept") || !strings.Contains(got, "session=s1") {
			t.Errorf("%s is told the notification %q, want the row of the workspace that was kept", c.who, got)
		}
		if strings.Contains(got, worktree) != c.whole {
			t.Errorf("%s is told the notification %q, want the worktree %s there: %v", c.who, got, worktree, c.whole)
		}
	}

	// Each operator is told: the device of one made an operator moves with the
	// role, and so does one made a member.
	_, err = y.st.SetUser(ctx, "sam", map[string]string{store.UserRole: store.RoleOperator}, "jo")
	must(err)
	told("jo's device, one operator's of two", y.jo, true)
	told("sam's device, an operator's now", y.sam, true)
	must(y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"))
	_, err = y.st.SetUser(ctx, "jo", map[string]string{store.UserRole: store.RoleMember}, "sam")
	must(err)
	told("jo's device, a member's now", y.jo, false)
	told("sam's device, the one operator's", y.sam, true)
	// The row of a notification is made when its event is logged, so the next
	// one follows the roles.
	keep()
	for _, c := range []struct {
		who, token, user string
		whole            bool
	}{{"jo's device, a member's now", y.jo, "jo", false}, {"sam's device, the one operator's", y.sam, "sam", true}} {
		if got := notified(c.token, c.user); strings.Contains(got, worktree) != c.whole {
			t.Errorf("%s is told the notification %q, want the worktree %s there: %v", c.who, got, worktree, c.whole)
		}
	}
}
