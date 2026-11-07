package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/telemetry"
	"yardr.dev/yardr/internal/web"
)

// telemetryFixture serves a yard as newFixture does, with the CLI's
// switch registered on its store and the operator jo beside the member
// sam, acting as as ("" for a view that does not act).
func telemetryFixture(t *testing.T, as string) *fixture {
	t.Helper()
	ctx := context.Background()
	st, own := storetest.NewOwned(t)
	own.SetTelemetrySwitch(telemetry.Switch{})
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if as != "" {
		token = actAs(t, srv, as)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, own: own, ts: ts, home: home}
	if as != "" {
		f.signIn(srv, token)
	}
	return f
}

// telemetryJSON is the page's JSON, unmarshalled, since the tests below
// read fields of it.
func telemetryJSON(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	r := f.get("/telemetry", "Accept", "application/json")
	if r.status != http.StatusOK {
		t.Fatalf("GET /telemetry as JSON: status %d", r.status)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("GET /telemetry as JSON does not parse: %v", err)
	}
	return out
}

// The Telemetry page shows every reader the consent words the switch is
// turned on under, and the operator the switch with its control: off it
// turns on only on the click, and on it shows the install id and the next
// payload as yardr telemetry show renders them. A non-operator's session
// sees neither the control nor the id, and the hub links the page for the
// operator alone.
func TestTelemetryPage(t *testing.T) {
	t.Parallel()
	f := telemetryFixture(t, "jo")
	off := f.get("/telemetry")
	if off.status != http.StatusOK {
		t.Fatalf("GET /telemetry: status %d", off.status)
	}
	for _, want := range []string{
		"four questions",
		"eu.i.posthog.com/batch/",
		"id-only person profile",
		"pseudonymous, not anonymous",
		"off — nothing is sent",
		`name="action" value="on"`,
		"Turn on",
	} {
		if !strings.Contains(off.body, want) {
			t.Errorf("the page with the switch off lacks %q:\n%s", want, off.body)
		}
	}
	for _, unwanted := range []string{"https://", "install id</dt>", `value="off"`} {
		if strings.Contains(off.body, unwanted) {
			t.Errorf("the page with the switch off holds %q:\n%s", unwanted, off.body)
		}
	}
	// Reading changes nothing: the switch stands off after two reads.
	again := f.get("/telemetry")
	if !strings.Contains(again.body, "off — nothing is sent") {
		t.Errorf("reading the page twice: %s", again.body)
	}
	if settings, err := f.st.Settings(context.Background()); err != nil {
		t.Fatal(err)
	} else if settings[store.TelemetrySetting] != "" {
		t.Errorf("reading the page set telemetry to %q", settings[store.TelemetrySetting])
	}
	// The click turns it on: the page lands on itself, and reads back
	// the install id and the next payload with the button that turns it
	// off again.
	r := f.postForm("/telemetry", url.Values{"action": {"on"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST /telemetry on: status %d, want 303", r.status)
	}
	on := f.get("/telemetry")
	for _, want := range []string{
		"on (since ",
		"install id</dt>",
		"next request",
		"sending day ",
		"next payload",
		"yardr_daily",
		"data deleted, send the project",
		`name="action" value="off"`,
		"Turn off",
	} {
		if !strings.Contains(on.body, want) {
			t.Errorf("the page with the switch on lacks %q:\n%s", want, on.body)
		}
	}
	if strings.Contains(on.body, `value="on"`) {
		t.Errorf("the page with the switch on still offers to turn on:\n%s", on.body)
	}
	got := telemetryJSON(t, f)
	if got["telemetry"] != "on" || got["operator"] != true {
		t.Errorf("the JSON with the switch on = %v, want telemetry on for the operator", got)
	}
	if got["profile"] != telemetry.Profile || got["id_sentence"] != telemetry.IDSentence || got["deletion"] != telemetry.DeletionSentence {
		t.Errorf("the JSON consent words = %v, want the shared sentences", got)
	}
	if questions, _ := got["questions"].(string); !strings.Contains(questions, "four questions") || strings.Contains(questions, "https://") {
		t.Errorf("the JSON questions = %q, want the consent words with the endpoint's scheme off", questions)
	}
	id, _ := got["install_id"].(string)
	if id == "" || !strings.Contains(on.body, id) {
		t.Errorf("the JSON install id %q is not on the page", id)
	}
	payload, _ := got["payload"].(string)
	var request struct {
		Batch []map[string]any `json:"batch"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil || len(request.Batch) == 0 {
		t.Errorf("the JSON payload is no batch: %v", err)
	}
	// Off again: the control reads off, and the id leaves the page with
	// the payload.
	r = f.postForm("/telemetry", url.Values{"action": {"off"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("POST /telemetry off: status %d, want 303", r.status)
	}
	off = f.get("/telemetry")
	if !strings.Contains(off.body, "off — nothing is sent") || !strings.Contains(off.body, `value="on"`) {
		t.Errorf("the page after off lacks the off control:\n%s", off.body)
	}
	if strings.Contains(off.body, id) {
		t.Errorf("the page after off still holds the install id:\n%s", off.body)
	}
	// A session that acts as no operator sees the consent words and no
	// switch, no id and no control, and the hub does not link the page
	// for it.
	for _, as := range []string{"sam", ""} {
		other := telemetryFixture(t, as)
		page := other.get("/telemetry")
		if page.status != http.StatusOK {
			t.Fatalf("GET /telemetry as %q: status %d", as, page.status)
		}
		for _, want := range []string{"four questions", "pseudonymous, not anonymous", "to see and set"} {
			if !strings.Contains(page.body, want) {
				t.Errorf("the page as %q lacks %q:\n%s", as, want, page.body)
			}
		}
		for _, unwanted := range []string{"off — nothing is sent", "Turn on", "Turn off", "install id</dt>", "<form"} {
			if strings.Contains(page.body, unwanted) {
				t.Errorf("the page as %q holds %q:\n%s", as, unwanted, page.body)
			}
		}
		data := telemetryJSON(t, other)
		if data["operator"] != false {
			t.Errorf("the JSON as %q = %v, want no operator", as, data)
		}
		for _, key := range []string{"telemetry", "install_id", "payload", "sending_day"} {
			if _, ok := data[key]; ok {
				t.Errorf("the JSON as %q holds %q: %v", as, key, data)
			}
		}
		if hub := other.get("/config"); strings.Contains(hub.body, "/telemetry") {
			t.Errorf("the hub as %q links /telemetry:\n%s", as, hub.body)
		}
	}
	if hub := f.get("/config"); !strings.Contains(hub.body, "/telemetry") {
		t.Errorf("the hub as jo does not link /telemetry:\n%s", hub.body)
	}

	// The hosted view reaches this same handler over its separate web link.
	// The link presents the edge's host, and JWT mode resolves the signed-in
	// operator for the page and its POST.
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	jwtStore, jwtOwn := storetest.NewOwned(t)
	jwtOwn.SetTelemetrySwitch(telemetry.Switch{})
	for _, name := range []string{"jo", "sam"} {
		if _, err := jwtStore.AddUser(context.Background(), name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := jwtStore.SetSetting(context.Background(), store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := jwtStore.SetSetting(context.Background(), store.HostedSetting, "true", "jo"); err != nil {
		t.Fatal(err)
	}
	jwtHome := filepath.Join(t.TempDir(), ".depot")
	jwtServer, err := web.NewServer(jwtStore, jwtHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	edgeHost := "my.yardr.dev"
	jwtServer.AllowHosts(edgeHost)
	if err := jwtServer.UseJWT(context.Background(), web.JWTConfig{
		Issuer: jwtTestIssuer, Audience: jwtTestAudience, JWKSURL: jwks.ts.URL,
		Users: map[string]string{"jo@example.net": "jo", "sam@example.net": "sam"},
	}); err != nil {
		t.Fatal(err)
	}
	jwtTS := httptest.NewServer(jwtServer)
	t.Cleanup(func() { shut(jwtTS) })
	hosted := &fixture{t: t, st: jwtStore, own: jwtOwn, ts: jwtTS, home: jwtHome}
	operatorJWT := signRSA(t, keys.a, "a", jwtClaims("jo@example.net"))
	memberJWT := signRSA(t, keys.a, "a", jwtClaims("sam@example.net"))
	const jwtHeader = web.DefaultJWTHeader
	off = hosted.get("/telemetry", jwtHeader, operatorJWT, "Host", edgeHost)
	if off.status != http.StatusOK || !strings.Contains(off.body, "off — nothing is sent") ||
		!strings.Contains(off.body, `name="action" value="on"`) {
		t.Errorf("hosted telemetry page as the JWT operator: status %d:\n%s", off.status, off.body)
	}
	r = hosted.postForm("/telemetry", url.Values{"action": {"on"}},
		jwtHeader, operatorJWT, "Host", edgeHost, "Origin", "https://"+edgeHost)
	if r.status != http.StatusSeeOther {
		t.Fatalf("hosted telemetry click as the JWT operator: status %d:\n%s", r.status, r.body)
	}
	on = hosted.get("/telemetry", jwtHeader, operatorJWT, "Host", edgeHost)
	if on.status != http.StatusOK || !strings.Contains(on.body, "on (since ") ||
		!strings.Contains(on.body, "install id</dt>") || !strings.Contains(on.body, "no batch available: nothing to send") {
		t.Errorf("hosted telemetry page after the JWT operator's click: status %d:\n%s", on.status, on.body)
	}
	member := hosted.get("/telemetry", jwtHeader, memberJWT, "Host", edgeHost)
	for _, unwanted := range []string{"off — nothing is sent", "Turn on", "Turn off", "install id</dt>", "no batch available: nothing to send"} {
		if member.status != http.StatusOK || strings.Contains(member.body, unwanted) {
			t.Errorf("hosted telemetry page as a JWT member holds %q (status %d):\n%s", unwanted, member.status, member.body)
		}
	}
	if hub := hosted.get("/config", jwtHeader, memberJWT, "Host", edgeHost); strings.Contains(hub.body, "/telemetry") {
		t.Errorf("the hosted hub as the JWT member links /telemetry:\n%s", hub.body)
	}
	t.Run("POST refusals", func(t *testing.T) {
		// A non-operator write is refused with the page redrawn, and changes
		// nothing; an action the page offers none of, and one from another
		// origin, are refused the same way.
		f := telemetryFixture(t, "sam")
		r := f.postForm("/telemetry", url.Values{"action": {"on"}}, "Origin", f.ts.URL)
		if r.status != http.StatusOK || !strings.Contains(r.body, "telemetry is the operator&#39;s to change") {
			t.Errorf("POST /telemetry on as sam: status %d, want the redrawn page saying the operator's to change:\n%s", r.status, r.body)
		}
		if settings, err := f.st.Settings(context.Background()); err != nil {
			t.Fatal(err)
		} else if settings[store.TelemetrySetting] != "" {
			t.Errorf("sam's write set telemetry to %q", settings[store.TelemetrySetting])
		}
		op := telemetryFixture(t, "jo")
		for _, c := range []struct {
			form url.Values
			want string
		}{
			{url.Values{}, "on or off"},
			{url.Values{"action": {"maybe"}}, "no telemetry change is named"},
		} {
			r := op.postForm("/telemetry", c.form, "Origin", op.ts.URL)
			if r.status != http.StatusOK || !strings.Contains(r.body, c.want) {
				t.Errorf("POST /telemetry %v: status %d, want the redrawn page saying %q:\n%s", c.form, r.status, c.want, r.body)
			}
		}
		r = op.postForm("/telemetry", url.Values{"action": {"on"}}, "Origin", "http://evil.example")
		if r.status != http.StatusForbidden {
			t.Errorf("POST /telemetry from another origin: status %d, want 403", r.status)
		}
		if settings, err := op.st.Settings(context.Background()); err != nil {
			t.Fatal(err)
		} else if settings[store.TelemetrySetting] != "" {
			t.Errorf("a refused write set telemetry to %q", settings[store.TelemetrySetting])
		}
		// Without --as the route takes no write at all.
		plain := telemetryFixture(t, "")
		r = plain.postForm("/telemetry", url.Values{"action": {"on"}}, "Origin", plain.ts.URL)
		if r.status != http.StatusMethodNotAllowed {
			t.Errorf("POST /telemetry on a view that does not act: status %d, want 405", r.status)
		}
	})
}
