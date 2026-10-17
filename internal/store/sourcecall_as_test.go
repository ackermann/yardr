package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// asYard is crmYard with the crew member yardmaster and the users ada and
// bia, ada's credential holding the token the stand-in takes while the
// source's file holds a wrong one: a call that succeeds as ada read ada's
// file, and a plain call still reads the source's.
func asYard(t *testing.T, s store.API) context.Context {
	t.Helper()
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.KeyUserCredential(ctx, "ada", "crm", map[string]string{"CRM_TOKEN": crmToken}, "ada"); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(t.TempDir(), "crm.env")
	if err := os.WriteFile(wrong, []byte("CRM_TOKEN=tok-wrong-not-to-be-seen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSource(ctx, "crm", store.SourceEnvFile, wrong, "jo"); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// crewPane is the context of the test process in the crew member's pane,
// whose handle names it.
func crewPane(t *testing.T, s store.API, ctx context.Context, crew string) context.Context {
	t.Helper()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStarted(ctx, crew, map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	return store.WithCaller(ctx, me)
}

// A call as a user from a granted crew member reads the user's file and
// its event names as beside by, with neither the credential nor the answer
// in any event. A bare call from the as-only grantee is refused, a caller
// without the as grant is refused with the command that grants it, the
// operator tries any set-up user, and a user the yard does not have is
// refused before anything is recorded.
//
// No pane is recorded here, so the test runs over the socket too, where
// every call has the test process as its caller: a recorded pane would
// resolve every call to it.
func TestSourceCallAsAUser(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := asYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}

	res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "customer", As: "ada", Actor: "yardmaster", Params: map[string]string{"id": "4711"}})
	if err != nil || string(res.Answer) != `{"id":"4711","name":"Ada","plan":"pro"}` {
		t.Fatalf("a call as ada = %+v, %v", res, err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"by":"crew member yardmaster"`) || !strings.Contains(data, `"as":"ada"`) ||
		!strings.Contains(data, `"crew":"yardmaster"`) || !strings.Contains(data, `"status":200`) {
		t.Fatalf("its event = %s", data)
	}

	// A bare call from the as-only grantee is refused, and names no user.
	_, err = s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "customer", Actor: "yardmaster", Params: map[string]string{"id": "1"}})
	if err == nil || !strings.Contains(err.Error(), "crew member yardmaster is not granted it; the operator grants it with: yardr source grant crm customer --crew yardmaster") {
		t.Fatalf("a bare call on an as grant alone = %v", err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":"not_granted"`) || strings.Contains(data, `"as"`) {
		t.Fatalf("its event = %s", data)
	}

	// A caller without the as grant is refused with the command that
	// grants it.
	_, err = s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "customer", As: "ada", Actor: "clerk", Params: map[string]string{"id": "1"}})
	if err == nil || !strings.Contains(err.Error(), "crew member clerk is not granted it as ada; ada grants it with: yardr source grant crm customer --crew clerk --as ada") {
		t.Fatalf("a call as ada without the as grant = %v", err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":"not_granted"`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}

	// The operator tries any set-up user.
	if res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", As: "ada", Actor: "jo"}); err != nil || string(res.Answer) != `{"id":"app"}` {
		t.Fatalf("the operator as ada = %+v, %v", res, err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"by":"the operator jo"`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}

	// A user the yard does not have is refused before anything is
	// recorded, like a call the source does not have.
	n := len(calls(t, s))
	_, err = s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", As: "eve", Actor: "jo"})
	if !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "user eve") {
		t.Fatalf("a call as a user the yard does not have = %v", err)
	}
	if got := len(calls(t, s)); got != n {
		t.Fatalf("the refused user left %d events, want %d", got, n)
	}

	// Without --as nothing changes: the plain call reads the source's
	// own file, whose wrong token the service refuses.
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, to, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", Actor: "yardmaster"}); err != nil || res.Status != 401 {
		t.Fatalf("a plain call on the source's wrong token = %+v, %v", res, err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":401`) || strings.Contains(data, `"as"`) {
		t.Fatalf("its event = %s", data)
	}

	all, _ := json.Marshal(calls(t, s))
	if strings.Contains(string(all), crmToken) || strings.Contains(string(all), "tok-wrong") || strings.Contains(string(all), "Ada") {
		t.Fatalf("an event has a credential or the answer in it: %s", all)
	}
}

// A call as a user from the granted crew member's pane is the crew
// member's, whatever actor it names: the pane's crew member, naming the
// operator, calls as ada on its as grant. The owning store, not the
// socket's client, so the pane is the test's own in both modes.
func TestSourceCallAsAUserFromACrewPane(t *testing.T) {
	t.Parallel()
	_, s := storetest.NewOwned(t)
	ctx := asYard(t, s)
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Crew: "yardmaster"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	pane := crewPane(t, s, ctx, "yardmaster")

	res, err := s.CallSource(pane, store.SourceCall{Source: "crm", Call: "customer", As: "ada", Actor: "jo", Params: map[string]string{"id": "4711"}})
	if err != nil || string(res.Answer) != `{"id":"4711","name":"Ada","plan":"pro"}` {
		t.Fatalf("a call as ada from the crew pane = %+v, %v", res, err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"by":"crew member yardmaster"`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}
}

// A call as a user whose file sets none of the names is refused with the
// file, what it does not set and the card that gives them; a user with no
// file is refused with the names and the card. A session of a granted
// group calls as the user on its bead.
func TestSourceCallAsWithoutTheNames(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := asYard(t, s)
	other := filepath.Join(t.TempDir(), "bia.env")
	if err := os.WriteFile(other, []byte("OTHER=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUser(ctx, "bia", map[string]string{store.UserEnvFile: other}, "jo"); err != nil {
		t.Fatal(err)
	}

	_, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", As: "bia", Actor: "jo"})
	if err == nil || !strings.Contains(err.Error(), "source crm, call ping: env_file "+other+" does not set CRM_TOKEN") ||
		!strings.Contains(err.Error(), `yardr bead ask <bead> "<question>" --secret crm --as bia --context "<where to get it>" asks bia for it`) {
		t.Fatalf("a file without the names = %v", err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":"no_credential"`) || !strings.Contains(data, `"as":"bia"`) {
		t.Fatalf("its event = %s", data)
	}

	// No file at all: the names unprompted, and the card.
	if _, err := s.AddUser(ctx, "cob", "jo"); err != nil {
		t.Fatal(err)
	}
	_, err = s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", As: "cob", Actor: "jo"})
	if err == nil || !strings.Contains(err.Error(), "CRM_TOKEN is not set for cob: ") || !strings.Contains(err.Error(), "--secret crm --as cob") {
		t.Fatalf("a user with no file = %v", err)
	}

	// A session of a granted group calls as the user, on its bead.
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Group: "builders"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	b, sess := session(t, s, "builders")
	res, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "customer", As: "ada", Session: sess.ID, Actor: "x", Params: map[string]string{"id": "7"}})
	if err != nil || string(res.Answer) != `{"id":"7","name":"Ada","plan":"pro"}` {
		t.Fatalf("a session's call as ada = %+v, %v", res, err)
	}
	ev, data := lastCall(t, s)
	if !strings.Contains(data, `"session":"`+sess.ID+`"`) || !strings.Contains(data, `"group":"builders"`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}
	if got, _ := s.GetBead(ctx, *ev.Bead); got == nil || got.ID != b.ID {
		t.Fatalf("its event is on %v, want %s", got, b.ID)
	}
}

// What a caller may call lists each call as its command: a plain call
// where its plain grant covers it, and beside it each user its grants as
// one cover. The operator's list stays plain.
func TestSourceCallAsLists(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", "customer", to, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantSource(ctx, "crm", "customer", to, "bia", "bia"); err != nil {
		t.Fatal(err)
	}
	may, err := s.SourceCalls(ctx, "", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range may.Calls {
		lines = append(lines, c.Line())
	}
	want := []string{
		"yardr call crm ping --as ada",
		"yardr call crm customer id=<string> [note=<string>]",
		"yardr call crm customer --as ada id=<string> [note=<string>]",
		"yardr call crm customer --as bia id=<string> [note=<string>]",
		"yardr call crm moved --as ada",
		"yardr call crm big --as ada",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("callables = %q, want %q", lines, want)
	}
	for _, c := range may.Calls {
		if c.As == "bia" && c.Name != "customer" {
			t.Fatalf("read as bia covers %+v", c)
		}
		if c.Update() {
			t.Fatalf("read as a user covers the update %+v", c)
		}
	}

	// The operator may make any call as any user, and lists each plainly.
	omay, err := s.SourceCalls(ctx, "", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if len(omay.Calls) != 5 {
		t.Fatalf("the operator lists %d calls, want every call plainly", len(omay.Calls))
	}
	for _, c := range omay.Calls {
		if c.As != "" || strings.Contains(c.Line(), "--as") {
			t.Fatalf("the operator lists %+v", c)
		}
	}

	// A session's brief names what its group may call as: only that.
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Group: "builders"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	_, sess := session(t, s, "builders")
	smay, err := s.SessionCalls(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(smay.Calls) != 4 {
		t.Fatalf("the session lists %d calls, want the four reads as ada", len(smay.Calls))
	}
	for _, c := range smay.Calls {
		if c.As != "ada" {
			t.Fatalf("the session lists %+v", c)
		}
	}
}

// The hourly limits stay per caller: a crew member's plain calls and its
// calls as any number of users share the one budget.
func TestSourceCallAsSharesTheCallersBudget(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := crmYard(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KeyUserCredential(ctx, "ada", "crm", map[string]string{"CRM_TOKEN": crmToken}, "ada"); err != nil {
		t.Fatal(err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, to, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantSource(ctx, "crm", store.GrantRead, to, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSource(ctx, "crm", store.SourceCallsPerHour, "2", "jo"); err != nil {
		t.Fatal(err)
	}
	call := func(as string) error {
		_, err := s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "ping", As: as, Actor: "yardmaster"})
		return err
	}
	if err := call(""); err != nil {
		t.Fatalf("a plain call: %v", err)
	}
	if err := call("ada"); err != nil {
		t.Fatalf("a call as ada: %v", err)
	}
	err := call("ada")
	if err == nil || !strings.Contains(err.Error(), "crew member yardmaster has made the 2 calls of the source it has in 1h0m0s") {
		t.Fatalf("the third call, as ada over a shared budget = %v", err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":"limit"`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}
}

// An update left pending as ada is not taken for the same caller's same
// call with the same parameters as bia: each is sent under its own
// idempotency key. A second try as ada takes ada's key again.
func TestSourceUpdateAsAUserHasItsOwnKey(t *testing.T) {
	// Sequential: source.Timeout is package-global; keep it long enough for
	// context cancellation after the stand-in has taken the first update.
	was := source.Timeout
	t.Cleanup(func() { source.Timeout = was })
	source.Timeout = time.Hour
	s := storetest.New(t)
	ctx, crm := updateYard(t, s)
	for _, u := range []string{"ada", "bia"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.KeyUserCredential(ctx, u, "crm", map[string]string{"CRM_UPDATE_TOKEN": crmUpdateToken}, u); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GrantSource(ctx, "crm", "note", store.Grantee{Group: "support"}, u, u); err != nil {
			t.Fatal(err)
		}
	}
	_, sess := session(t, s, "support")
	note := func(ctx context.Context, as, text string) (*store.SourceAnswer, error) {
		return s.CallSource(ctx, store.SourceCall{Source: "crm", Call: "note", As: as, Session: sess.ID, Actor: "x", Params: map[string]string{"id": "7", "text": text}})
	}

	taken := crm.HoldNote() // the service takes the note and holds its answer
	callCtx, cancel := context.WithCancel(ctx)
	type callResult struct {
		answer *store.SourceAnswer
		err    error
	}
	done := make(chan callResult, 1)
	go func() {
		answer, err := note(callCtx, "ada", "called back")
		done <- callResult{answer: answer, err: err}
	}()
	select {
	case <-taken:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("the stand-in did not take ada's update")
	}
	cancel()
	first := <-done
	if first.err == nil || !strings.Contains(first.err.Error(), "context canceled") {
		t.Fatalf("the held update as ada = %+v, %v", first.answer, first.err)
	}
	if notes, asked := crm.CustomerNotes(), crm.NotesAsked(); len(notes) != 1 || asked != 1 {
		t.Fatalf("the stand-in has not taken ada's update: %+v, asked %d times", notes, asked)
	}
	if _, err := note(ctx, "bia", "called back"); err != nil {
		t.Fatalf("the same call as bia = %v", err)
	}
	notes := crm.CustomerNotes()
	if len(notes) != 2 || notes[0].Key == notes[1].Key {
		t.Fatalf("two users' same call: the service has %+v", notes)
	}
	if _, data := lastCall(t, s); strings.Contains(data, `"attempt"`) {
		t.Fatalf("bia's call taken for ada's retry: %s", data)
	}
	// A second try as ada takes ada's key again: the service writes
	// nothing more.
	res, err := note(ctx, "ada", "called back")
	if err != nil || string(res.Answer) != `{"customer":"7","id":"n1"}` {
		t.Fatalf("the same call as ada again = %+v, %v", res, err)
	}
	if _, data := lastCall(t, s); !strings.Contains(data, `"status":201`) || !strings.Contains(data, `"attempt":2`) || !strings.Contains(data, `"as":"ada"`) {
		t.Fatalf("its event = %s", data)
	}
	if notes, asked := crm.CustomerNotes(), crm.NotesAsked(); len(notes) != 2 || asked != 3 {
		t.Fatalf("after ada's retry the service has %+v, asked %d times", notes, asked)
	}
}
