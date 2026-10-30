package web_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// approveYard is a yard in JWT mode with the operator jo and the member
// sam, the depot home and the crew member yardmaster, served for a
// browser. The store and the view share the gateway's 15-minute window.
type approveYard struct {
	t    *testing.T
	st   store.API
	own  *store.Store
	ts   *httptest.Server
	srv  *web.Server
	keys jwtTestKeys
	jwks *jwksStub
}

func newApproveYard(t *testing.T, hosted bool) *approveYard {
	t.Helper()
	ctx := context.Background()
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	st, own := storetest.NewOwned(t)
	for _, name := range []string{"jo", "sam"} {
		if _, err := st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	must(t, st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(t, st.SetSetting(ctx, store.HostedSetting, strconv.FormatBool(hosted), "jo"))
	must(t, st.SetSetting(ctx, store.WebJWTPrivilegedSessionMaxAgeSetting, "15m", "jo"))
	must(t, st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	_, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard",
		Config: map[string]any{"kind": "claude"}}, "jo")
	must(t, err)
	users := map[string]string{"jo@example.net": "jo", "sam@example.net": "sam"}
	srv, _ := jwtServer(t, st, jwks, "", users, func(c *web.JWTConfig) { c.PrivilegedSessionMaxAge = 15 * time.Minute })
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &approveYard{t: t, st: st, own: own, ts: ts, srv: srv, keys: keys, jwks: jwks}
}

// token signs a gateway token for email, carrying what each of set writes
// beside the valid claims.
func (y *approveYard) token(email string, set ...func(jwt.MapClaims)) string {
	y.t.Helper()
	claims := jwtClaims(email)
	for _, f := range set {
		f(claims)
	}
	return signRSA(y.t, y.keys.a, "a", claims)
}

// authAt writes the gateway's authentication time, in whole seconds as a
// gateway writes it.
func authAt(at time.Time) func(jwt.MapClaims) {
	return func(c jwt.MapClaims) { c["auth_time"] = at.Unix() }
}

// post sends form to target as the token's user, following no redirect,
// so a 303 is seen as one.
func (y *approveYard) post(target, token string, form url.Values) resp {
	y.t.Helper()
	req, err := http.NewRequest(http.MethodPost, y.ts.URL+target, strings.NewReader(form.Encode()))
	if err != nil {
		y.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(web.DefaultJWTHeader, token)
	req.Header.Set("Origin", y.ts.URL)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		y.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

// getNoFollow gets target as the token's user, following no redirect.
func (y *approveYard) getNoFollow(target, token string) resp {
	y.t.Helper()
	req, err := http.NewRequest(http.MethodGet, y.ts.URL+target, nil)
	if err != nil {
		y.t.Fatal(err)
	}
	req.Header.Set(web.DefaultJWTHeader, token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		y.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

func (y *approveYard) bead(title string) *store.Bead {
	y.t.Helper()
	b, err := y.st.CreateBead(context.Background(), store.NewBead{Title: title, Stage: "new", Depot: "home"}, "yardmaster")
	must(y.t, err)
	return b
}

func (y *approveYard) askRun(beadID, to string, argv ...string) uint64 {
	y.t.Helper()
	seq, err := y.st.AddAsk(context.Background(), beadID, store.Ask{
		Question: "Run it?", To: to, Context: []string{"the work needs it"},
		Run: &store.AskRun{Argv: argv}}, "yardmaster")
	must(y.t, err)
	return seq
}

func (y *approveYard) revision(beadID string) string {
	y.t.Helper()
	b, err := y.st.GetBead(context.Background(), beadID)
	must(y.t, err)
	return b.Revision
}

// approveForm is the approval the bead page's form posts for ask.
func (y *approveYard) approveForm(beadID string, seq uint64) url.Values {
	return url.Values{
		"rev":    {y.revision(beadID)},
		"ask":    {strconv.FormatUint(seq, 10)},
		"action": {"approve"},
	}
}

// askedState is the ask's state as the bead's page reads it for user.
func (y *approveYard) askedState(beadID string, seq uint64, user string) string {
	y.t.Helper()
	v, err := y.st.Bead(context.Background(), beadID, user)
	must(y.t, err)
	for _, a := range v.Asks {
		if a.Ask == seq {
			return a.State
		}
	}
	y.t.Fatalf("bead %s has no ask %d", beadID, seq)
	return ""
}

// witness is the newest witness any event claims, "" where none does.
func (y *approveYard) witness() string {
	y.t.Helper()
	evs, err := y.st.Events(context.Background(), "", 30)
	must(y.t, err)
	for i := len(evs) - 1; i >= 0; i-- {
		if w, _ := evs[i].Data["witness"].(string); w != "" {
			return w
		}
	}
	return ""
}

// A routine run card approves on the bead page as it always was: the
// button stands beside the card, the approval runs it, and the answer
// claims no witness.
func TestWebApprovesARoutineRunCard(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	must(t, func() error { _, err := y.st.AddUser(ctx, "gone", "jo"); return err }())
	b := y.bead("take back a user")
	seq := y.askRun(b.ID, "jo", "user", "rm", "gone")
	tok := y.token("jo@example.net")

	page := jwtGet(t, y.ts, "/beads/"+b.ID, web.DefaultJWTHeader, tok)
	if page.status != 200 {
		t.Fatalf("GET the bead: %d: %s", page.status, page.body)
	}
	if !strings.Contains(page.body, `value="approve"`) {
		t.Fatalf("the routine card shows no approve control: %s", page.body)
	}
	r := y.post("/beads/"+b.ID, tok, y.approveForm(b.ID, seq))
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("POST the approval: %d %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	if _, err := y.st.GetUser(ctx, "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the approval left the user: %v", err)
	}
	if got := y.askedState(b.ID, seq, "jo"); got != "answered" {
		t.Fatalf("the ask is %s after the approval, want it answered", got)
	}
	if w := y.witness(); w != "" {
		t.Fatalf("the routine approval is witnessed %q, want none", w)
	}
}

// A strong card's approval starts the gateway re-authentication: the view
// answers with the step to the edge, bound to this card, and the gateway's
// return approves it under the fresh sign-in. The return state is one-use:
// replayed, it approves nothing.
func TestWebStrongCardChallengeAndReturn(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	b := y.bead("add a user")
	seq := y.askRun(b.ID, "jo", "user", "add", "bob")

	r := y.post("/beads/"+b.ID, y.token("jo@example.net"), y.approveForm(b.ID, seq))
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST the approval: %d: %s", r.status, r.body)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil || loc.Path != "/edge/reauth" {
		t.Fatalf("the approval goes to %q, want the gateway re-authentication", r.header.Get("Location"))
	}
	back := loc.Query().Get("return_to")
	if want := "/beads/" + b.ID + "/fresh/" + strconv.FormatUint(seq, 10) + "/"; !strings.HasPrefix(back, want) || back == want {
		t.Fatalf("the return state is %q, want one-use state bound to ask %d", back, seq)
	}
	if _, err := y.st.GetUser(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the challenge added the user: %v", err)
	}
	// The gateway authenticates after the challenge: whole seconds, as a
	// gateway writes them, past the skew only a clock may be ahead by.
	fresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	r = y.getNoFollow(back, fresh)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("GET the return: %d %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	if _, err := y.st.GetUser(ctx, "bob"); err != nil {
		t.Fatalf("the return approved nothing: %v", err)
	}
	if got := y.askedState(b.ID, seq, "jo"); got != "answered" {
		t.Fatalf("the ask is %s after the return, want it answered", got)
	}
	if w := y.witness(); w != store.WitnessFreshSignIn {
		t.Fatalf("the approval is witnessed %q, want a fresh sign-in", w)
	}
	r = y.getNoFollow(back, fresh)
	if r.status != 200 || !strings.Contains(r.body, "used already") {
		t.Fatalf("replaying the return: %d: %s", r.status, r.body)
	}
}

// One fresh sign-in witnesses a second strong card inside the window: the
// card approves without a new sign-in, whichever card started the window.
func TestWebSecondStrongCardSkipsSignIn(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	first := y.bead("add a user")
	seq := y.askRun(first.ID, "jo", "user", "add", "ann")
	r := y.post("/beads/"+first.ID, y.token("jo@example.net"), y.approveForm(first.ID, seq))
	back, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	fresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	if r := y.getNoFollow(back.Query().Get("return_to"), fresh); r.status != http.StatusSeeOther {
		t.Fatalf("GET the first return: %d: %s", r.status, r.body)
	}
	second := y.bead("add another user")
	carol := y.askRun(second.ID, "jo", "user", "add", "carol")
	r = y.post("/beads/"+second.ID, fresh, y.approveForm(second.ID, carol))
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+second.ID {
		t.Fatalf("POST the second approval: %d %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	if _, err := y.st.GetUser(ctx, "carol"); err != nil {
		t.Fatalf("the second approval ran nothing: %v", err)
	}
	if w := y.witness(); w != store.WitnessFreshSignIn {
		t.Fatalf("the second approval is witnessed %q, want a fresh sign-in", w)
	}
}

// A standing login never approves a strong card directly: a stale session
// is challenged, and a return under a stale sign-in, with no
// authentication time, or as another user refuses and leaves the card open.
func TestWebStrongCardReturnRefusals(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	b := y.bead("add a user")
	seq := y.askRun(b.ID, "jo", "user", "add", "bob")

	stale := y.token("jo@example.net", authAt(time.Now().Add(-time.Hour)))
	r := y.post("/beads/"+b.ID, stale, y.approveForm(b.ID, seq))
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST under a stale sign-in: %d: %s", r.status, r.body)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil || loc.Path != "/edge/reauth" {
		t.Fatalf("a stale session goes to %q, want a new challenge", r.header.Get("Location"))
	}
	back := loc.Query().Get("return_to")
	for _, c := range []struct {
		name  string
		token string
		want  string
	}{
		{"a stale sign-in", stale, "not fresh"},
		{"no authentication time", y.token("jo@example.net"), "not fresh"},
		{"another user", y.token("sam@example.net", authAt(time.Now().Add(5*time.Second))), "another card"},
	} {
		if r := y.getNoFollow(back, c.token); r.status != 200 || !strings.Contains(r.body, c.want) {
			t.Errorf("%s: GET the return: %d: %s", c.name, r.status, r.body)
		}
	}
	if _, err := y.st.GetUser(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused return added the user: %v", err)
	}
	if got := y.askedState(b.ID, seq, "jo"); got != "open" {
		t.Fatalf("the ask is %s after the refusals, want it open", got)
	}
	if w := y.witness(); w != "" {
		t.Fatalf("a refused return is witnessed %q, want none", w)
	}
}

// Anywhere but the hosted web a strong card stays terminal-only: off a
// hosted yard, and on a view with no gateway at all.
func TestWebStrongCardStaysTerminalOffHosted(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, false)
	b := y.bead("add a user")
	seq := y.askRun(b.ID, "jo", "user", "add", "bob")
	fresh := y.token("jo@example.net", authAt(time.Now()))
	r := y.post("/beads/"+b.ID, fresh, y.approveForm(b.ID, seq))
	if r.status != 200 || !strings.Contains(r.body, "not approved in this window") ||
		!strings.Contains(r.body, "at a terminal on the yard&#39;s machine") {
		t.Fatalf("POST off a hosted yard: %d: %s", r.status, r.body)
	}

	f, _ := talkFixture(t, "jo")
	ctx := context.Background()
	must(t, f.st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: "/yard",
		Config: map[string]any{"kind": "claude"}}, "jo")
	must(t, err)
	bead, err := f.st.CreateBead(ctx, store.NewBead{Title: "add a user", Stage: "new", Depot: "home"}, "yardmaster")
	must(t, err)
	n, err := f.st.AddAsk(ctx, bead.ID, store.Ask{Question: "Run it?", To: "jo",
		Context: []string{"the work needs it"},
		Run:     &store.AskRun{Argv: []string{"user", "add", "hal"}}}, "yardmaster")
	must(t, err)
	rev, err := f.st.GetBead(ctx, bead.ID)
	must(t, err)
	r = f.postForm("/beads/"+bead.ID, url.Values{
		"rev":    {rev.Revision},
		"ask":    {strconv.FormatUint(n, 10)},
		"action": {"approve"},
	}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "not approved in this window") {
		t.Fatalf("POST with no gateway: %d: %s", r.status, r.body)
	}
}

// A run card that is weighty but not strong keeps the window's no-gate
// words: the page offers no approval, and a posted one is refused.
func TestWebWeightyCardKeepsNoGate(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	_, err := y.st.PutSource(ctx, "crm.yaml",
		"name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n", "jo")
	must(t, err)
	b := y.bead("call the CRM more often")
	seq := y.askRun(b.ID, "jo", "source", "set", "crm", "updates_per_hour=6000")
	v, err := y.st.Bead(ctx, b.ID, "jo")
	must(t, err)
	var classes []string
	for _, a := range v.Asks {
		if a.Ask == seq {
			for _, w := range a.Weight {
				classes = append(classes, w.Class)
			}
		}
	}
	if len(classes) != 1 || classes[0] != store.WeightOutside {
		t.Fatalf("raising the update limit weighs %v, want outside alone", classes)
	}
	tok := y.token("jo@example.net")
	page := jwtGet(t, y.ts, "/beads/"+b.ID, web.DefaultJWTHeader, tok)
	if page.status != 200 {
		t.Fatalf("GET the bead: %d: %s", page.status, page.body)
	}
	if !strings.Contains(page.body, "this window has no gate") {
		t.Fatalf("the weighty card names no gate: %s", page.body)
	}
	if strings.Contains(page.body, `value="approve"`) {
		t.Fatalf("the weighty card shows an approve control: %s", page.body)
	}
	r := y.post("/beads/"+b.ID, tok, y.approveForm(b.ID, seq))
	if r.status != 200 || !strings.Contains(r.body, "this window has no gate") {
		t.Fatalf("POST the approval: %d: %s", r.status, r.body)
	}
	src, err := y.st.GetSource(ctx, "crm")
	must(t, err)
	if src.UpdatesPerHour != 0 {
		t.Fatalf("the refused approval raised the update limit to %d", src.UpdatesPerHour)
	}
}

// approveSetupDecl is the one minted source of the setup pack the tests'
// reader serves, with a read call, an update call, and a mint command: a
// first install is strong.
const approveSetupDecl = `name: setupmail
base: https://setupmail.example.com/v1
credential:
  env: SETUP_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [SETUP_ACCOUNT]
  mint_command: [setup-mint]
calls:
  search: { kind: read, request: { method: GET, path: /messages } }
  draft: { kind: update, request: { method: POST, path: /drafts } }
`

const approveSetupMint = "#!/bin/sh\n# mint a token from the account\n"

// A strong setup approves on the bead page as a run card does: the card
// reads with the pack and the yard's sentences, the approval starts the
// gateway re-authentication, and the return carries the setup out under
// the fresh sign-in.
func TestWebApprovesAStrongSetupCard(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	sum := sha256.Sum256([]byte(approveSetupMint))
	y.own.SetPackReader(func(ctx context.Context, name string, yard store.API) (*store.PackReading, error) {
		if name != "setupmail" {
			return nil, errors.New("no pack " + name)
		}
		return &store.PackReading{Name: "setupmail", Kind: pack.KindSource,
			Label: "embedded in yardr test1", Build: "test1",
			Sources: []store.AskSource{{Name: "setupmail", Text: approveSetupDecl,
				Files: []store.AskFile{{Program: "setup-mint", Text: approveSetupMint, SHA256: hex.EncodeToString(sum[:])}}}},
			Presets: []store.AskPreset{
				{Name: "read", Grants: []store.AskPresetGrant{{Source: "setupmail", Calls: []string{"search"}}}},
				{Name: "drafts", Grants: []store.AskPresetGrant{{Source: "setupmail", Calls: []string{"search", "draft"}}}},
			}}, nil
	})
	ctx := context.Background()
	b := y.bead("set up mail")
	seq, err := y.st.AddAsk(ctx, b.ID, store.Ask{Question: "Set up setupmail for the yardmaster?", To: "jo",
		Context: []string{"the yardmaster reads mail to work beads"},
		Setup:   &store.AskSetup{Pack: "setupmail", To: []store.Grantee{{Crew: "yardmaster"}}}}, "yardmaster")
	must(t, err)

	tok := y.token("jo@example.net")
	page := jwtGet(t, y.ts, "/beads/"+b.ID, web.DefaultJWTHeader, tok)
	if page.status != 200 {
		t.Fatalf("GET the bead: %d: %s", page.status, page.body)
	}
	if !strings.Contains(page.body, "setupmail") || !strings.Contains(page.body, "need-approve") {
		t.Fatalf("the setup card shows no approval: %s", page.body)
	}
	form := y.approveForm(b.ID, seq)
	form.Set("option", "2")
	form.Set("secret-SETUP_ACCOUNT", "ops@example.com")
	r := y.post("/beads/"+b.ID, tok, form)
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST the approval: %d: %s", r.status, r.body)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil || loc.Path != "/edge/reauth" {
		t.Fatalf("the setup approval goes to %q, want the gateway re-authentication", r.header.Get("Location"))
	}
	fresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	r = y.getNoFollow(loc.Query().Get("return_to"), fresh)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("GET the return: %d %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	if got := y.askedState(b.ID, seq, "jo"); got != "answered" {
		t.Fatalf("the ask is %s after the return, want it answered", got)
	}
	if w := y.witness(); w != store.WitnessFreshSignIn {
		t.Fatalf("the setup approval is witnessed %q, want a fresh sign-in", w)
	}
}
