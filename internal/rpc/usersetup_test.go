package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
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

// carriedOut approves the setup ask seq of yd-crm as jo, taking the candidate
// courier-existing when account names it and typing the account when it is
// empty, and records its carrying out as the server's carrier does but for
// the work: the lines it prints for the apply, for the account in file and
// for the grants, or the failure in its words when there is one. It answers
// what was recorded.
func (y *secretYard) carriedOut(t *testing.T, seq uint64, account, file, failure string) string {
	t.Helper()
	ctx := context.Background()
	var typed map[string]string
	if account == "" {
		typed = map[string]string{"COURIER_ACCOUNT": "typed@courier.example.test"}
	}
	if _, err := y.st.ApproveSetup(ctx, "yd-crm", seq, 1, typed, account, "jo", true); err != nil {
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
	for _, c := range []struct {
		name   string
		typed  bool
		failed bool
	}{{"a candidate taken", false, false}, {"an account typed", true, false}, {"a carrying out that failed", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			y, file := setupYard(t)
			account := "courier-existing"
			if c.typed {
				// The candidate's file holds no account now, so the card
				// offers none, and the account is set in the card's own file.
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				account = ""
			}
			seq := y.askSetup(t)
			if c.typed {
				asked, err := y.st.Bead(ctx, "yd-crm")
				if err != nil {
					t.Fatal(err)
				}
				if card := setups(*asked)[seq]; len(card.Candidates) != 0 || card.AccountFile == "" {
					t.Fatalf("the card lists %+v, and writes to %q: want no candidate and a file", card.Candidates, card.AccountFile)
				} else {
					file = card.AccountFile
				}
			}
			failure := ""
			if c.failed {
				failure = "pack courier is applied, but the candidate courier-existing no longer holds COURIER_ACCOUNT as carded: its file is " + file +
					" now, not " + file + ". Ask for the account again, or point the source with yardr source set courier env_file=" + file + " in a terminal."
			}
			whole := y.carriedOut(t, seq, account, file, failure)
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

			// UserAsks holds nothing back, and needs not: a setup ask is put to the
			// operator, so no queue but theirs has its result.
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

			// The operator now is told: the device of the one named moves with it,
			// and in a yard with no operator no device is.
			if err := y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"); err != nil {
				t.Fatal(err)
			}
			told("jo's device, no longer the operator's", y.jo, false)
			told("sam's device, the operator's now", y.sam, true)
			if err := y.st.SetSetting(ctx, store.OperatorSetting, "", "sam"); err != nil {
				t.Fatal(err)
			}
			told("jo's device, in a yard with no operator", y.jo, false)
			told("sam's device, in a yard with no operator", y.sam, false)
		})
	}
}

// replayed is what the device with the token is sent of the log from its
// start to its head: the data of each source_set, in order, and the lines of
// the whole stream. The stream stays open, so it is read to the newest event
// and no further.
func (y *secretYard) replayed(t *testing.T, base, token string) (sets []map[string]any, raw string) {
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
		if e.Kind == "source_set" {
			sets = append(sets, e.Data)
		}
		if e.Seq >= head {
			return sets, strings.Join(all, "\n")
		}
	}
}

// The stream tells the file a source was pointed at, in its source_set, to a
// device of the operator and to no other, as Bead tells the file of an
// account (TestUserBeadTellsTheAccountFileToTheOperatorAlone): to any other
// device the event is the source without the path, replayed from the start or
// sent as it happens, and so it is to every device of a yard with no
// operator. It is so for each write of a file: a source pointed at one, as a
// setup that took a candidate points the source its pack added at the
// candidate's file, and a source with none, for which the server chooses one
// when a value is given. A source_set that names no file is told as it is,
// and the store, which the socket serves, keeps every event whole.
func TestUserStreamTellsASourceFileToTheOperatorAlone(t *testing.T) {
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

	// A device sent the log as it happens is sent the same: its streams are
	// open before the next write, which moves a source to a file of its own.
	base := listen(t, y.u, 0)
	head, err := y.st.Head(ctx)
	must(err)
	jos, _ := follow(t, base, y.jo, head)
	sams, _ := follow(t, base, y.sam, head)
	moved := filepath.Join(filepath.Dir(file), "crm-moved.env")
	_, err = y.st.SetSource(ctx, "crm", store.SourceEnvFile, moved, "jo")
	must(err)

	whole := []map[string]any{
		{"source": "courier-existing", "env_file": file},
		{"source": "courier", "env_file": file},
		{"source": "crm", "env_file": y.file},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm", "env_file": moved},
	}
	without := []map[string]any{
		{"source": "courier-existing"},
		{"source": "courier"},
		{"source": "crm"},
		{"source": "crm", "calls_per_hour": "120"},
		{"source": "crm"},
	}
	var stored []map[string]any
	for _, e := range eventsOfKind(t, y.st, "source_set") {
		stored = append(stored, e.Data)
	}
	if !reflect.DeepEqual(stored, whole) {
		t.Fatalf("the store keeps the source_set events %v, want %v", stored, whole)
	}
	// liveSet is the data of the next event of a stream that was open for it.
	liveSet := func(who string, lines <-chan string) map[string]any {
		t.Helper()
		var e store.Event
		if line := next(t, lines); json.Unmarshal([]byte(line), &e) != nil || e.Kind != "source_set" {
			t.Fatalf("%s is sent %q, want the source_set that moved crm", who, line)
		}
		return e.Data
	}
	if got := liveSet("jo's device", jos); !reflect.DeepEqual(got, whole[4]) {
		t.Errorf("jo's device is sent %v as it happens, want %v", got, whole[4])
	}
	if got := liveSet("sam's device", sams); !reflect.DeepEqual(got, without[4]) {
		t.Errorf("sam's device is sent %v as it happens, want %v", got, without[4])
	}

	// told replays the log to the device with the token, and fails unless it
	// is sent the files of the source_set events when listed, and not a file
	// of the yard's anywhere in the stream when not.
	told := func(who, token string, listed bool) {
		t.Helper()
		sets, raw := y.replayed(t, base, token)
		want := without
		if listed {
			want = whole
		}
		if !reflect.DeepEqual(sets, want) {
			t.Errorf("%s is sent the source_set events %v, want %v", who, sets, want)
		}
		for _, path := range []string{file, y.file, moved} {
			if strings.Contains(raw, path) != listed {
				t.Errorf("%s is sent %q: %v, want %v", who, path, !listed, listed)
			}
		}
	}
	told("jo's device", y.jo, true)
	told("sam's device", y.sam, false)

	// The operator now is told: the device of the one named moves with it,
	// the past events with it, and in a yard with no operator no device is.
	must(y.st.SetSetting(ctx, store.OperatorSetting, "sam", "jo"))
	told("jo's device, no longer the operator's", y.jo, false)
	told("sam's device, the operator's now", y.sam, true)
	must(y.st.SetSetting(ctx, store.OperatorSetting, "", "sam"))
	told("jo's device, in a yard with no operator", y.jo, false)
	told("sam's device, in a yard with no operator", y.sam, false)
}
