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
	"os"
	"path"
	"path/filepath"
	"slices"
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
// so a 303 is seen as one. headers are more request headers, each a name
// and a value.
func (y *approveYard) post(target, token string, form url.Values, headers ...string) resp {
	y.t.Helper()
	req, err := http.NewRequest(http.MethodPost, y.ts.URL+target, strings.NewReader(form.Encode()))
	if err != nil {
		y.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(web.DefaultJWTHeader, token)
	req.Header.Set("Origin", y.ts.URL)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
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

// approveSurfaces are the places a card stands: its bead's page, Home's
// needs rows and the line's waits (whose right column is the same
// partial). Each posts the card's answers to the bead's handler.
var approveSurfaces = []struct {
	name string
	path func(bead string) string
}{
	{"the bead's page", func(bead string) string { return "/beads/" + bead }},
	{"home", func(string) string { return "/" }},
	{"the line", func(string) string { return "/line" }},
}

const (
	// approveButton is the button a strong card's approval stands behind,
	// in the yard these tests name depot.
	approveButton = `<button type="submit" name="action" value="approve">Approve in depot</button>`
	// noGateLine is what a weighty card says where its window holds no gate
	// for it.
	noGateLine = "This card is weighty and this window has no gate."
	// wordsField is the field a card's words ride in, a decline's reason
	// among them, and declineButton the button that sends the decline.
	wordsField    = `<input type="text" name="words" placeholder="Words with the answer, or why not" aria-label="Words">`
	declineButton = `<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`
)

// surfaceBody is the page at path as the token's user reads it.
func (y *approveYard) surfaceBody(path, token string) string {
	y.t.Helper()
	r := y.getNoFollow(path, token)
	if r.status != http.StatusOK {
		y.t.Fatalf("GET %s: %d: %s", path, r.status, r.body)
	}
	return r.body
}

// weighed is the classes the yard stored on the ask.
func (y *approveYard) weighed(beadID string, seq uint64) []string {
	y.t.Helper()
	v, err := y.st.Bead(context.Background(), beadID, "jo")
	must(y.t, err)
	var out []string
	for _, a := range v.Asks {
		if a.Ask == seq {
			for _, w := range a.Weight {
				out = append(out, w.Class)
			}
		}
	}
	return out
}

// approveCRM is the source the weighty-card tests change: one read call
// and the credential the yard holds under CRM_TOKEN. approveCRMList is it
// with one more read call, which a standing grant of read reaches once a
// declaration with it replaces crm's.
const (
	approveCRM     = "name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	approveCRMList = approveCRM + "  list: { kind: read, request: { method: GET, path: /lists } }\n"
)

// putCRM puts the source approveCRM on the yard, with a standing grant of
// read to the yardmaster where grant is set.
func (y *approveYard) putCRM(grant bool) {
	y.t.Helper()
	ctx := context.Background()
	_, err := y.st.PutSource(ctx, "crm.yaml", approveCRM, "jo")
	must(y.t, err)
	if grant {
		_, err = y.st.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Crew: "yardmaster"}, "", "jo")
		must(y.t, err)
	}
}

// askSetup asks for setupmail's setup for the yardmaster, as the yard's
// first install of it: a credential, an outside and a code.
func (y *approveYard) askSetup(beadID string) uint64 {
	y.t.Helper()
	seq, err := y.st.AddAsk(context.Background(), beadID, store.Ask{Question: "Set up setupmail for the yardmaster?", To: "jo",
		Context: []string{"the yardmaster reads mail to work beads"},
		Setup:   &store.AskSetup{Pack: "setupmail", To: []store.Grantee{{Crew: "yardmaster"}}}}, "yardmaster")
	must(y.t, err)
	return seq
}

// A routine run card approves on the bead page as it always was: the
// button stands beside the card, the approval runs it, and the answer
// claims no witness. Home and the line leave a routine card's approval
// where it is: they offer none.
func TestWebApprovesARoutineRunCard(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	ctx := context.Background()
	must(t, func() error { _, err := y.st.AddUser(ctx, "gone", "jo"); return err }())
	must(t, y.st.SetSetting(ctx, store.NameSetting, "depot", "jo"))
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
	// The card reads as in the other places: the ask line, the asker, the
	// run sentence naming the yard that runs the command and the user the
	// approval is called as, and Ask about this to the crew asker's
	// conversation.
	for _, want := range []string{
		`<p class="ask-line">` + b.ID + ` · ask ` + strconv.FormatUint(seq, 10) + `</p>`,
		`<p class="ask-asker">asked by yardmaster</p>`,
		`<p>in depot, when you approve, the yard runs this as jo, and nothing else:</p>`,
		`<pre class="text file">yardr user rm gone</pre>`,
		`<a class="ask-about" href="/talk?with=yardmaster">Ask about this</a>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the routine card lacks %q:\n%s", want, page.body)
		}
	}
	for _, s := range approveSurfaces[1:] {
		if body := y.surfaceBody(s.path(b.ID), tok); strings.Contains(body, `value="approve"`) {
			t.Errorf("%s offers a routine card's approval:\n%s", s.name, body)
		}
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
// replayed, it approves nothing. A post htmx made (the bead page's forms
// post with it) is answered 200 with HX-Redirect to the same step, since
// htmx follows a 303 as an XHR and would swap the sign-in page into the
// card's place; the state it carries is the one followed through the return
// here.
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
	// The same post from htmx names the same step with a one-use state of
	// its own, and no redirect for the XHR to follow.
	hx := y.post("/beads/"+b.ID, y.token("jo@example.net"), y.approveForm(b.ID, seq), "HX-Request", "true")
	if hx.status != http.StatusOK || hx.header.Get("Location") != "" {
		t.Fatalf("POST the approval with htmx: %d %q: %s", hx.status, hx.header.Get("Location"), hx.body)
	}
	hxLoc, err := url.Parse(hx.header.Get("HX-Redirect"))
	if err != nil || hxLoc.Path != "/edge/reauth" {
		t.Fatalf("the htmx approval goes to %q, want the gateway re-authentication", hx.header.Get("HX-Redirect"))
	}
	hxBack := hxLoc.Query().Get("return_to")
	if want := "/beads/" + b.ID + "/fresh/" + strconv.FormatUint(seq, 10) + "/"; !strings.HasPrefix(hxBack, want) || hxBack == want || hxBack == back {
		t.Fatalf("the htmx return state is %q, want a state of its own bound to ask %d", hxBack, seq)
	}
	// Up to the state each post mints, it is the URL the 303 names.
	if hxStep, step := strings.TrimSuffix(hx.header.Get("HX-Redirect"), path.Base(hxBack)), strings.TrimSuffix(r.header.Get("Location"), path.Base(back)); hxStep != step {
		t.Fatalf("the htmx approval goes to %q, the 303 to %q, want the same step", hxStep, step)
	}
	if _, err := y.st.GetUser(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the challenge added the user: %v", err)
	}
	// The gateway authenticates after the challenge: whole seconds, as a
	// gateway writes them, past the skew only a clock may be ahead by. The
	// return followed is the htmx post's, the one a browser on the bead's
	// page carries.
	fresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	r = y.getNoFollow(hxBack, fresh)
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
	r = y.getNoFollow(hxBack, fresh)
	if r.status != 200 || !strings.Contains(r.body, "used already") {
		t.Fatalf("replaying the return: %d: %s", r.status, r.body)
	}

	const botDecl = `
name: bot
base: http://127.0.0.1:9
credential: { env: BOT_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256}, project: {type: string, pattern: "^[0-9]+$", max: 20} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}", project: "{project}" } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
  keys: [chat]
  register: { call: setwebhook, url: url, secret: secret_token, ask: [project] }
`
	_, err = y.st.PutSource(ctx, "bot.yaml", botDecl, "jo")
	must(t, err)
	env := filepath.Join(t.TempDir(), "bot.env")
	must(t, os.WriteFile(env, []byte("BOT_TOKEN=the-token\n"), 0o600))
	_, err = y.st.SetSource(ctx, "bot", store.SourceEnvFile, env, "jo")
	must(t, err)
	must(t, y.st.SetSetting(ctx, store.HookPublicURLSetting, "https://yard.example/hooks", "jo"))
	hookBead := y.bead("set the bot hook up")
	hookAsk := y.askRun(hookBead.ID, "jo", "hook", "add", "bot", "--source", "bot")
	if !slices.ContainsFunc(y.weighed(hookBead.ID, hookAsk), func(class string) bool {
		return class == store.WeightTrust || class == store.WeightCode
	}) {
		t.Fatalf("the hook card weighs %v, want a strong class", y.weighed(hookBead.ID, hookAsk))
	}
	standing := y.token("jo@example.net")
	for _, s := range approveSurfaces {
		body := y.surfaceBody(s.path(hookBead.ID), standing)
		for _, want := range []string{
			`<input type="text" name="value-project" placeholder="project" aria-label="project">`,
			approveButton,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: hook card lacks %q:\n%s", s.name, want, body)
			}
		}
	}
	r = y.post("/beads/"+hookBead.ID, standing, y.approveForm(hookBead.ID, hookAsk))
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST the hook approval: %d: %s", r.status, r.body)
	}
	loc, err = url.Parse(r.header.Get("Location"))
	if err != nil || loc.Path != "/edge/reauth" {
		t.Fatalf("the hook approval goes to %q, want the gateway re-authentication", r.header.Get("Location"))
	}
	missing := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	r = y.getNoFollow(loc.Query().Get("return_to"), missing)
	if r.status != http.StatusOK || !strings.Contains(r.body, "asks for project in the clear") {
		t.Fatalf("the hook return without project: %d: %s", r.status, r.body)
	}
	if got := y.askedState(hookBead.ID, hookAsk, "jo"); got != "open" {
		t.Fatalf("the hook ask is %s after the refusal, want it open", got)
	}
	if _, err := y.st.GetHook(ctx, "bot"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused approval made the hook: %v", err)
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
	// The window holds no gate, so the card reads as any weighty card with
	// none, wherever it stands: its line and its terminal command, and no
	// control to approve it by.
	for _, s := range approveSurfaces {
		body := y.surfaceBody(s.path(b.ID), fresh)
		if strings.Contains(body, `value="approve"`) || !strings.Contains(body, noGateLine) ||
			!strings.Contains(body, "yardr bead answer "+b.ID+" "+strconv.FormatUint(seq, 10)+" --approve") {
			t.Errorf("%s offers a strong card's approval off a hosted yard, or lacks its terminal command:\n%s", s.name, body)
		}
	}
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
	for _, s := range approveSurfaces {
		if body := f.get(s.path(bead.ID)).body; strings.Contains(body, `value="approve"`) || !strings.Contains(body, noGateLine) {
			t.Errorf("%s with no gateway offers a strong card's approval, or lacks the line:\n%s", s.name, body)
		}
	}
	r = f.postForm("/beads/"+bead.ID, url.Values{
		"rev":    {rev.Revision},
		"ask":    {strconv.FormatUint(n, 10)},
		"action": {"approve"},
	}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "not approved in this window") {
		t.Fatalf("POST with no gateway: %d: %s", r.status, r.body)
	}
}

// A card's approval stands wherever the card does, class by class. A
// strong card (trust or code in its weight, alone or beside other classes)
// reads "Approve in <yard>" on its bead's page, on Home and on the line,
// and approving it from any of them starts the gateway re-authentication,
// even for a session that signed in a moment ago; a weighty card with
// neither class (credential, outside, as-user or reads alone) offers no
// approval on any of them, keeps its line and its terminal command, and a
// post at the handler is refused with the ask open and nothing run.
func TestWebCardApprovalPerClassOfWeight(t *testing.T) {
	t.Parallel()
	const freshDecl = "name: fresh\nbase: https://fresh.example.com/v1\ncredential: { env: FRESH_TOKEN, header: \"Authorization: Bearer {credential}\" }\nthread:\n  reply: [fresh/thread, reply]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	const freshThread = "#!/bin/sh\n# thread\n"
	threadSum := sha256.Sum256([]byte(freshThread))
	// askAs puts ask on the bead as the yardmaster.
	askAs := func(y *approveYard, bead string, ask store.Ask) uint64 {
		y.t.Helper()
		ask.To, ask.Context = "jo", []string{"the work needs it"}
		seq, err := y.st.AddAsk(context.Background(), bead, ask, "yardmaster")
		must(y.t, err)
		return seq
	}
	for _, c := range []struct {
		name    string
		classes []string
		strong  bool
		// ask puts the card on the bead and returns its number, with the
		// fields its approval posts beside the usual three.
		ask func(y *approveYard, bead string) (uint64, url.Values)
		// refusal is what a withheld card's refused post says of its weight.
		refusal string
		// complete follows the sign-in's return and approves the card.
		complete bool
		// intact checks that nothing ran, before the return.
		intact func(t *testing.T, y *approveYard)
	}{
		{name: "trust", classes: []string{store.WeightTrust}, strong: true, complete: true,
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				return y.askRun(bead, "jo", "user", "add", "bob"), nil
			},
			intact: func(t *testing.T, y *approveYard) {
				if _, err := y.st.GetUser(context.Background(), "bob"); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("the challenge added the user: %v", err)
				}
			}},
		{name: "code", classes: []string{store.WeightCode}, strong: true,
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				return askAs(y, bead, store.Ask{Question: "Add fresh?", Run: &store.AskRun{Argv: []string{"source add fresh.yaml"},
					Carried: []store.AskSource{{Name: "fresh", Text: freshDecl,
						Files: []store.AskFile{{Program: "fresh/thread", Text: freshThread, SHA256: hex.EncodeToString(threadSum[:])}}}}}}), nil
			}},
		{name: "code beside reads", classes: []string{store.WeightCode, store.WeightReads}, strong: true,
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				y.putCRM(true)
				return askAs(y, bead, store.Ask{Question: "Replace crm?", Run: &store.AskRun{Argv: []string{"source add crm.yaml"},
					Carried: []store.AskSource{{Name: "crm", Text: approveCRMList}}}}), nil
			}},
		{name: "credential, outside and code", classes: []string{store.WeightCredential, store.WeightOutside, store.WeightCode}, strong: true, complete: true,
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				servesSetupmail(y.own)
				return y.askSetup(bead), url.Values{"option": {"2"}, "secret-SETUP_ACCOUNT": {"ops@example.com"}}
			}},
		{name: "credential", classes: []string{store.WeightCredential}, refusal: "weighty (credential)",
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				y.putCRM(false)
				return askAs(y, bead, store.Ask{Question: "The API key for the CRM", Secret: &store.AskSecret{Source: "crm"}}),
					url.Values{"action": {"give"}, "secret-CRM_TOKEN": {"s3cret"}}
			}},
		{name: "outside", classes: []string{store.WeightOutside}, refusal: "weighty (outside)",
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				y.putCRM(false)
				return y.askRun(bead, "jo", "source", "set", "crm", "updates_per_hour=6000"), nil
			},
			intact: func(t *testing.T, y *approveYard) {
				src, err := y.st.GetSource(context.Background(), "crm")
				must(t, err)
				if src.UpdatesPerHour != 0 {
					t.Errorf("the refused approval raised the update limit to %d", src.UpdatesPerHour)
				}
			}},
		{name: "as-user", classes: []string{store.WeightAsUser}, refusal: "weighty (as-user)",
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				y.putCRM(false)
				return y.askRun(bead, "jo", "user", "identity", "sam", "--source", "crm", "--id", "7"), nil
			}},
		{name: "reads", classes: []string{store.WeightReads}, refusal: "weighty (reads)",
			ask: func(y *approveYard, bead string) (uint64, url.Values) {
				y.putCRM(true)
				y.own.SetPackReader(func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
					if name != "crm-pack" {
						return nil, errors.New("no pack " + name)
					}
					return &store.PackReading{Name: name, Kind: pack.KindSource, Label: "embedded in yardr test1", Build: "test1",
						Sources: []store.AskSource{{Name: "crm", Text: approveCRMList}}}, nil
				})
				return askAs(y, bead, store.Ask{Question: "Apply crm-pack?", Run: &store.AskRun{Argv: []string{"pack apply crm-pack"}}}), nil
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y := newApproveYard(t, true)
			b := y.bead(c.name)
			seq, extra := c.ask(y, b.ID)
			if got := y.weighed(b.ID, seq); !slices.Equal(got, c.classes) {
				t.Fatalf("the card weighs %v, want %v", got, c.classes)
			}
			// A session that signed in a moment ago: a standing login, which
			// never approves a strong card.
			tok := y.token("jo@example.net", authAt(time.Now()))
			rev := y.revision(b.ID)
			for _, s := range approveSurfaces {
				body := y.surfaceBody(s.path(b.ID), tok)
				// Wherever the card stands it answers to the bead's handler,
				// from the revision it was drawn at.
				for _, want := range []string{
					`action="/beads/` + b.ID + `"`,
					`<input type="hidden" name="rev" value="` + rev + `">`,
					`<input type="hidden" name="ask" value="` + strconv.FormatUint(seq, 10) + `">`,
				} {
					if !strings.Contains(body, want) {
						t.Errorf("%s: the card lacks %q:\n%s", s.name, want, body)
					}
				}
				if c.strong {
					if !strings.Contains(body, approveButton) || strings.Contains(body, noGateLine) {
						t.Errorf("%s: the strong card is withheld, or says it has no gate:\n%s", s.name, body)
					}
					continue
				}
				if strings.Contains(body, `value="approve"`) || strings.Contains(body, `value="give"`) ||
					strings.Contains(body, `type="password"`) || !strings.Contains(body, noGateLine) {
					t.Errorf("%s: the weighty card offers a control, or lacks its line:\n%s", s.name, body)
				}
			}
			form := y.approveForm(b.ID, seq)
			for k, v := range extra {
				form[k] = v
			}
			r := y.post("/beads/"+b.ID, tok, form)
			if !c.strong {
				if r.status != http.StatusOK || !strings.Contains(r.body, c.refusal) {
					t.Fatalf("POST at the handler: %d: %s", r.status, r.body)
				}
			} else {
				loc, err := url.Parse(r.header.Get("Location"))
				if r.status != http.StatusSeeOther || err != nil || loc.Path != "/edge/reauth" {
					t.Fatalf("POST the approval: %d %q, want the gateway re-authentication", r.status, r.header.Get("Location"))
				}
			}
			if c.intact != nil {
				c.intact(t, y)
			}
			if got := y.askedState(b.ID, seq, "jo"); got != "open" {
				t.Fatalf("the ask is %s after the post, want it open", got)
			}
			if w := y.witness(); w != "" {
				t.Fatalf("the post is witnessed %q, want none", w)
			}
			if !c.complete {
				return
			}
			// The gateway authenticates after the challenge, and the return
			// lands on the bead, where the answered card shows.
			loc, _ := url.Parse(r.header.Get("Location"))
			back := loc.Query().Get("return_to")
			if want := "/beads/" + b.ID + "/fresh/" + strconv.FormatUint(seq, 10) + "/"; !strings.HasPrefix(back, want) {
				t.Fatalf("the return is %q, want state bound to ask %d", back, seq)
			}
			fresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
			if r := y.getNoFollow(back, fresh); r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
				t.Fatalf("GET the return: %d %q: %s", r.status, r.header.Get("Location"), r.body)
			}
			if got := y.askedState(b.ID, seq, "jo"); got != "answered" {
				t.Fatalf("the ask is %s after the return, want it answered", got)
			}
			if w := y.witness(); w != store.WitnessFreshSignIn {
				t.Fatalf("the approval is witnessed %q, want a fresh sign-in", w)
			}
		})
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

// servesSetupmail has the store read the one pack the setup tests apply:
// setupmail, whose source mints its token from an account (a credential),
// brings the mint command (code) and offers a preset that updates
// (outside).
func servesSetupmail(own *store.Store) {
	sum := sha256.Sum256([]byte(approveSetupMint))
	own.SetPackReader(func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
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
}

// A strong setup approves on the bead page as a run card does: the card
// reads with the pack and the yard's sentences, the approval starts the
// gateway re-authentication, and the return carries the setup out under
// the fresh sign-in. Home and the line carry the same form: the presets,
// the account typed unseen and the candidate, posting to the bead's
// handler with no script, and no pick or note beside it.
func TestWebApprovesAStrongSetupCard(t *testing.T) {
	t.Parallel()
	y := newApproveYard(t, true)
	servesSetupmail(y.own)
	b := y.bead("set up mail")
	seq := y.askSetup(b.ID)

	tok := y.token("jo@example.net")
	page := jwtGet(t, y.ts, "/beads/"+b.ID, web.DefaultJWTHeader, tok)
	if page.status != 200 {
		t.Fatalf("GET the bead: %d: %s", page.status, page.body)
	}
	if !strings.Contains(page.body, "setupmail") || !strings.Contains(page.body, "need-approve") {
		t.Fatalf("the setup card shows no approval: %s", page.body)
	}
	for _, want := range []string{
		`<p class="ask-says">yardmaster says:</p>`,
		`<p>When you approve, the yard applies the pack setupmail as jo, and nothing else:</p>`,
		approveButton + `</form>`,
		// On its bead's page the form answers in place.
		`<form class="need-approve" method="post" action="/beads/` + b.ID + `" hx-post="/beads/` + b.ID + `" hx-target="#bead"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the setup card lacks %q:\n%s", want, page.body)
		}
	}
	for _, s := range approveSurfaces[1:] {
		body := y.surfaceBody(s.path(b.ID), tok)
		for _, want := range []string{
			`<form class="need-approve" method="post" action="/beads/` + b.ID + `"><input type="hidden" name="rev"`,
			`<input type="radio" name="option" value="1">read</label>`,
			`<input type="radio" name="option" value="2">drafts</label>`,
			`<input type="password" name="secret-SETUP_ACCOUNT" placeholder="SETUP_ACCOUNT" aria-label="SETUP_ACCOUNT" autocomplete="off">`,
			`<input type="text" name="account" placeholder="Candidate source, or type above" aria-label="Candidate source">`,
			approveButton + `</form>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the setup card lacks %q:\n%s", s.name, want, body)
			}
		}
		// A preset is picked in the approval, never answered by a button,
		// and a setup takes no words as a note.
		for _, foreign := range []string{`<button type="submit" name="option"`, `value="send"`, `hx-target="#bead"`} {
			if strings.Contains(body, foreign) {
				t.Errorf("%s: the setup card carries %q:\n%s", s.name, foreign, body)
			}
		}
	}
	missing := y.bead("set up mail without account")
	missingSeq := y.askSetup(missing.ID)
	missingForm := y.approveForm(missing.ID, missingSeq)
	missingForm.Set("option", "2")
	r := y.post("/beads/"+missing.ID, y.token("jo@example.net"), missingForm)
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST setup without account: %d: %s", r.status, r.body)
	}
	missingLoc, err := url.Parse(r.header.Get("Location"))
	if err != nil || missingLoc.Path != "/edge/reauth" {
		t.Fatalf("setup without account goes to %q, want the gateway re-authentication", r.header.Get("Location"))
	}
	missingFresh := y.token("jo@example.net", authAt(time.Now().Add(5*time.Second)))
	r = y.getNoFollow(missingLoc.Query().Get("return_to"), missingFresh)
	if r.status != http.StatusOK || !strings.Contains(r.body, "SETUP_ACCOUNT was not given") {
		t.Fatalf("the setup return without account: %d: %s", r.status, r.body)
	}
	if got := y.askedState(missing.ID, missingSeq, "jo"); got != "open" {
		t.Fatalf("the setup ask is %s after the refusal, want it open", got)
	}
	if _, err := y.st.GetSource(context.Background(), "setupmail"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused setup wrote the source: %v", err)
	}
	form := y.approveForm(b.ID, seq)
	form.Set("option", "2")
	form.Set("secret-SETUP_ACCOUNT", "ops@example.com")
	r = y.post("/beads/"+b.ID, tok, form)
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

// answerForm is the card's answer form in body, from its tag to its end:
// what a person has in front of them to answer with.
func answerForm(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `<form class="need-answer"`)
	if !ok {
		t.Fatalf("the page has no answer form:\n%s", body)
	}
	form, _, _ := strings.Cut(rest, `</form>`)
	return form
}

// A run or setup card declines from its bead's page as it does from Home
// and the line. The yard refuses a decline without its reason, so the
// words field stands beside Decline on all three, though the card takes no
// words as an answer and offers no button for one. A decline posted
// without words is still refused, with the ask open; with them it ends the
// ask and keeps the reason.
func TestWebDeclinesARunOrSetupCard(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		ask  func(y *approveYard, bead string) uint64
	}{
		{"run", func(y *approveYard, bead string) uint64 {
			return y.askRun(bead, "jo", "user", "add", "bob")
		}},
		{"setup", func(y *approveYard, bead string) uint64 {
			servesSetupmail(y.own)
			return y.askSetup(bead)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			y := newApproveYard(t, true)
			b := y.bead("decline a " + c.name + " card")
			seq := c.ask(y, b.ID)
			ask := strconv.FormatUint(seq, 10)
			tok := y.token("jo@example.net")
			for _, s := range approveSurfaces {
				form := answerForm(t, y.surfaceBody(s.path(b.ID), tok))
				if !strings.Contains(form, wordsField) || !strings.Contains(form, declineButton) {
					t.Errorf("%s: the card's form lacks the words field or Decline:\n%s", s.name, form)
				}
				// Neither card takes words as its answer or as a note.
				for _, foreign := range []string{`name="action" value="answer"`, `name="action" value="send"`} {
					if strings.Contains(form, foreign) {
						t.Errorf("%s: the card's form offers %q:\n%s", s.name, foreign, form)
					}
				}
			}
			form := url.Values{"rev": {y.revision(b.ID)}, "ask": {ask}, "action": {"decline"}}
			r := y.post("/beads/"+b.ID, tok, form)
			if want := "declining ask " + ask + " needs words"; r.status != http.StatusOK || !strings.Contains(r.body, want) {
				t.Fatalf("POST the decline without words: %d, want the page redrawn with %q:\n%s", r.status, want, r.body)
			}
			if got := y.askedState(b.ID, seq, "jo"); got != "open" {
				t.Fatalf("the ask is %s after the refused decline, want it open", got)
			}
			form.Set("words", "not this one")
			r = y.post("/beads/"+b.ID, tok, form)
			if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
				t.Fatalf("POST the decline with words: %d %q: %s", r.status, r.header.Get("Location"), r.body)
			}
			v, err := y.st.Bead(context.Background(), b.ID, "jo")
			must(t, err)
			if len(v.Asks) != 1 || v.Asks[0].State != "declined" || v.Asks[0].Words != "not this one" {
				t.Fatalf("the ask: %+v, want it declined with the words", v.Asks)
			}
		})
	}
}
