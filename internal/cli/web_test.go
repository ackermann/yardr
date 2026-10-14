package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// web serve refuses an address off loopback before it touches the yard: no
// store is opened and no server started for a command that will not serve.
func TestWebServeRefusesNonLoopback(t *testing.T) {
	t.Setenv("YARDR_HOME", t.TempDir())
	for _, addr := range []string{":8791", "0.0.0.0:8791", "192.168.1.10:8791"} {
		a := &app{}
		_, err := run(t, a.webCmd(), "serve", "--addr", addr)
		if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "no authentication") {
			t.Errorf("--addr %s: %v, want a refusal that says why", addr, err)
		}
		if !strings.Contains(err.Error(), "--unsafe") {
			t.Errorf("--addr %s: the refusal does not name --unsafe: %v", addr, err)
		}
		if a.st != nil {
			t.Errorf("--addr %s: the store was opened", addr)
		}
	}
}

// --unsafe alone listens on every interface, on the port of the yard's
// web_addr where it has one; an --addr given stands, and without the flag
// the default stays on loopback.
func TestWebAddr(t *testing.T) {
	def := (&app{}).webCmd().Commands()[0].Flags().Lookup("addr").DefValue
	if def != "127.0.0.1:8791" {
		t.Fatalf("the default --addr is %s", def)
	}
	for _, c := range []struct {
		addr          string
		given, unsafe bool
		want          string
	}{
		{def, false, false, "127.0.0.1:8791"},
		{def, false, true, "0.0.0.0:8791"},
		// The yard's web_addr, which is addr when --addr was not given:
		// every interface on its port.
		{"127.0.0.1:9100", false, false, "127.0.0.1:9100"},
		{"127.0.0.1:9100", false, true, "0.0.0.0:9100"},
		{"[::1]:9100", false, true, "0.0.0.0:9100"},
		{"invalid", false, true, "invalid"},
		{":9000", true, true, ":9000"},
		{"127.0.0.1:8791", true, true, "127.0.0.1:8791"},
		{"192.168.1.10:9000", true, false, "192.168.1.10:9000"},
	} {
		if got := webAddr(c.addr, c.given, c.unsafe); got != c.want {
			t.Errorf("webAddr(%s, %v, %v) = %s, want %s", c.addr, c.given, c.unsafe, got, c.want)
		}
	}
}

// --unsafe gets an address off loopback past the address check and on to
// the yard: what fails here is the store there is none of, not a refusal.
// The home is a short one, so that the command gets as far as the server
// under any TMPDIR (a socket path too long for the system ends it earlier).
func TestWebServeUnsafeAcceptsAnyAddress(t *testing.T) {
	newHome(t)
	for _, args := range [][]string{{"--unsafe"}, {"--unsafe", "--addr", ":9000"}, {"--unsafe", "--addr", "0.0.0.0:9000"}, {"--unsafe", "--addr", "192.168.1.10:9000"},
		{"--unsafe", "--pages-addr", "0.0.0.0:9000"}, {"--unsafe", "--pages-host", "pages.example.net"}} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, args...)...)
		if !errors.Is(err, errNoServer) {
			t.Errorf("%v: %v, want the store's error", args, err)
		}
	}
}

// --host names hosts: as a wildcard or with no name it is refused before
// the yard is touched, with the reason, with --unsafe and without. Without
// it an IP address off loopback is refused too, and the refusal names the
// flag that answers to one.
func TestWebServeRefusesHost(t *testing.T) {
	t.Setenv("YARDR_HOME", t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--host", "*"}, "wildcard"},
		{[]string{"--host", "*.ts.net"}, "wildcard"},
		{[]string{"--host", ""}, "no name"},
		{[]string{"--host", "a.example,,b.example"}, "no name"},
		{[]string{"--host", "a.example", "--host", "b.example:8791"}, "without a scheme or a port"},
		{[]string{"--host", "192.168.178.90"}, "--unsafe"},
		{[]string{"--host", "yard.example.net,10.0.0.1", "--addr", "127.0.0.1:9000"}, "--unsafe"},
		{[]string{"--unsafe", "--host", "*"}, "wildcard"},
		{[]string{"--unsafe", "--host", "*.ts.net"}, "wildcard"},
		{[]string{"--unsafe", "--host", ""}, "no name"},
		{[]string{"--unsafe", "--host", "a.example,,b.example"}, "no name"},
		{[]string{"--unsafe", "--host", "a.example", "--host", "b.example:8791"}, "without a scheme or a port"},
	} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), "--host") || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want a refusal that says %q", c.args, err, c.want)
		}
		if a.st != nil {
			t.Errorf("%v: the store was opened", c.args)
		}
	}
}

// --host gets past the checks and on to the yard, with --unsafe and on
// loopback without it, given once, twice or as a comma list, and each name
// is kept as it is compared.
func TestWebHosts(t *testing.T) {
	newHome(t)
	for _, args := range [][]string{{"--unsafe", "--host", "yard.example.net"}, {"--unsafe", "--host", "a.example,b.example", "--host", "C.example."},
		{"--unsafe", "--host", "192.168.178.90"},
		{"--host", "yard.example.net"}, {"--host", "a.example,b.example", "--host", "C.example.", "--addr", "127.0.0.1:9000"}, {"--host", "127.0.0.1"}} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, args...)...)
		if !errors.Is(err, errNoServer) {
			t.Errorf("%v: %v, want the store's error", args, err)
		}
	}
	got, err := webHosts([]string{"a.example", " B.Example ", "C.example."}, true, true)
	if want := []string{"a.example", "b.example", "c.example"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("webHosts = %v, %v, want %v", got, err, want)
	}
	if got, err := webHosts([]string{"a.example", " B.Example ", "C.example."}, true, false); err != nil || !slices.Equal(got, []string{"a.example", "b.example", "c.example"}) {
		t.Errorf("webHosts without --unsafe = %v, %v", got, err)
	}
	if got, err := webHosts(nil, false, false); err != nil || got != nil {
		t.Errorf("no --host: %v, %v", got, err)
	}
}

// --as with a name no user of the yard has is refused, naming the flag
// and where the users are; with a user's it gets on to serving.
func TestWebServeAsRefused(t *testing.T) {
	_, st, a := statusYard(t, "abcdef123456")
	_, err := run(t, a().webCmd(), "serve", "--as", "nosuch", "--addr", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "--as") || !strings.Contains(err.Error(), "refused") ||
		!strings.Contains(err.Error(), "no user") || !strings.Contains(err.Error(), "yardr user list") {
		t.Fatalf("--as nosuch: %v, want a refusal that names the flag and the users", err)
	}
	if _, err := st.GetUser(context.Background(), "Jo"); err != nil {
		t.Fatalf("the yard's user: %v", err)
	}
}

// Who a view acts as is --as, else YARDR_WEB_AS, else the yard's web_as
// setting, else no one: serve resolves them in that order and records the
// resolved user, and refuses an unknown name from each source, naming it.
func TestWebServeAsTakesFlagEnvAndSetting(t *testing.T) {
	home, st, a := statusYard(t, "abcdef123456")
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ann", "Jo"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_WEB_AS", "")
	cfg := &config.Config{Home: home}
	servesAs := func(args []string, want string) {
		t.Helper()
		cmd := (&app{st: st, cfg: cfg, actor: "me"}).rootCmd()
		cmd.SetArgs(append([]string{"web", "serve", "--addr", "127.0.0.1:0", "--pages-addr", "127.0.0.1:0"}, args...))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(sctx) }()
		var views []webView
		for deadline := time.Now().Add(10 * time.Second); len(views) == 0; time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				t.Fatalf("web serve %v ended: %v", args, err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("web serve %v recorded nothing", args)
			}
			views = webViews(cfg)
		}
		if views[0].As != want {
			t.Errorf("web serve %v acts as %q, want %q", args, views[0].As, want)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("web serve did not stop")
		}
	}
	servesAs(nil, "")
	if err := st.SetSetting(ctx, store.WebAsSetting, "Jo", "Jo"); err != nil {
		t.Fatal(err)
	}
	servesAs(nil, "Jo")
	t.Setenv("YARDR_WEB_AS", "ann")
	servesAs(nil, "ann")
	servesAs([]string{"--as", "Jo"}, "Jo")
	// An explicitly empty flag wins too: it is how a caller can keep a
	// view read-only despite an environment or setting default.
	servesAs([]string{"--as="}, "")
	t.Setenv("YARDR_WEB_AS", "")

	for _, c := range []struct {
		env, setting string
		args         []string
		want         string
	}{
		{"", "nosuch", nil, "web_as nosuch refused"},
		{"nosuch", "Jo", nil, "YARDR_WEB_AS nosuch refused"},
		{"ann", "Jo", []string{"--as", "nosuch"}, "--as nosuch refused"},
	} {
		t.Setenv("YARDR_WEB_AS", c.env)
		if err := st.SetSetting(ctx, store.WebAsSetting, c.setting, "Jo"); err != nil {
			t.Fatal(err)
		}
		_, err := run(t, a().webCmd(), append([]string{"serve", "--addr", "127.0.0.1:0"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "no user") || !strings.Contains(err.Error(), "yardr user list") {
			t.Errorf("web_as=%s under YARDR_WEB_AS=%q, %v: %v, want a refusal with %q", c.setting, c.env, c.args, err, c.want)
		}
	}
}

// A view whose user came from web_as is said to act as them: the resolved
// user is in the web record, so yardr status and doctor say "acts as".
func TestStatusAndDoctorSayActsAsFromSetting(t *testing.T) {
	home, st, a := statusYard(t, "abcdef123456")
	ctx := context.Background()
	t.Setenv("YARDR_WEB_AS", "")
	if err := st.SetSetting(ctx, store.WebAsSetting, "Jo", "Jo"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home}
	cmd := (&app{st: st, cfg: cfg, actor: "me"}).rootCmd()
	cmd.SetArgs([]string{"web", "serve", "--addr", "127.0.0.1:0", "--pages-addr", "127.0.0.1:0"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(sctx) }()
	var views []webView
	for deadline := time.Now().Add(10 * time.Second); len(views) == 0; time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("web serve ended: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("web serve recorded nothing")
		}
		views = webViews(cfg)
	}
	if views[0].As != "Jo" {
		t.Fatalf("view: %+v", views[0])
	}
	out, err := run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "acts as Jo")
	rep, _ := doctorJSON(t, st)
	var web *optionalPart
	for i := range rep.Optional {
		if rep.Optional[i].Key == "web" {
			web = &rep.Optional[i]
		}
	}
	if web == nil {
		t.Fatalf("doctor has no web part: %+v", rep.Optional)
	}
	if len(web.Views) != 1 || web.Views[0].As != "Jo" || !strings.Contains(web.Detail, "acts as Jo") {
		t.Errorf("doctor's web part: %+v", web)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("web serve did not stop")
	}
}

func TestWebAnswersTo(t *testing.T) {
	for _, c := range []struct {
		unsafe bool
		names  []string
		want   string
	}{
		{false, nil, ""},
		{false, []string{"yard.example.net", "yard.lan"}, "a loopback address, localhost, yard.example.net, yard.lan"},
		{true, nil, "an IP address, localhost"},
		{true, []string{"adventuretime", "adventuretime.local", "yard.example.net"}, "an IP address, localhost, adventuretime, adventuretime.local, yard.example.net"},
	} {
		if got := webAnswersTo(c.unsafe, c.names); got != c.want {
			t.Errorf("webAnswersTo(%v, %v) = %q, want %q", c.unsafe, c.names, got, c.want)
		}
	}
}

// In JWT mode, with web_jwt_issuer set, serve refuses what the mode cannot
// start with: an acting user from each of --as, YARDR_WEB_AS and web_as, a
// missing audience or keys' URL, a users table naming no user of the yard,
// and keys that cannot be fetched.
func TestWebServeJWTRefusals(t *testing.T) {
	home, st, _ := statusYard(t, "abcdef123456")
	ctx := context.Background()
	t.Setenv("YARDR_WEB_AS", "")
	a := func() *app { return &app{st: st, cfg: &config.Config{Home: home}, actor: "me"} }
	set := func(key, value string) {
		t.Helper()
		if err := st.SetSetting(ctx, key, value, "Jo"); err != nil {
			t.Fatal(err)
		}
	}
	serve := func(args ...string) error {
		t.Helper()
		_, err := run(t, a().webCmd(), append([]string{"serve", "--addr", "127.0.0.1:0"}, args...)...)
		return err
	}
	refused := func(err error, wants ...string) string {
		t.Helper()
		if err == nil {
			t.Fatal("serve started, want a refusal")
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%v, want a refusal with %q", err, want)
			}
		}
		return err.Error()
	}
	// Keys that cannot be fetched: the URL answers nothing.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	set(store.WebJWTIssuerSetting, "https://team.example.net")
	set(store.WebJWTAudienceSetting, "aud-tag")
	set(store.WebJWTJWKSSetting, closed.URL)
	set(store.WebJWTUsersSetting, "jo@example.net=Jo")
	// Each acting user is refused, naming the setting it stands beside.
	refused(serve("--as", "Jo"), "--as Jo refused", "web_jwt_issuer")
	t.Setenv("YARDR_WEB_AS", "Jo")
	refused(serve(), "YARDR_WEB_AS Jo refused", "web_jwt_issuer")
	t.Setenv("YARDR_WEB_AS", "")
	set(store.WebAsSetting, "Jo")
	refused(serve(), "web_as Jo refused", "web_jwt_issuer")
	set(store.WebAsSetting, "")
	// A missing audience or keys' URL.
	set(store.WebJWTAudienceSetting, "")
	refused(serve(), "web_jwt_audience is unset")
	set(store.WebJWTAudienceSetting, "aud-tag")
	set(store.WebJWTJWKSSetting, "")
	refused(serve(), "web_jwt_jwks is unset")
	set(store.WebJWTJWKSSetting, closed.URL)
	// JWT mode with no mappings could only refuse every visitor at request
	// time, so make the missing table a start-time error too.
	set(store.WebJWTUsersSetting, "")
	refused(serve(), "web_jwt_users is unset")
	// A users table naming no user of the yard, before any fetch: the
	// keys' URL answers nothing, and still the refusal names the user.
	set(store.WebJWTUsersSetting, "stranger@example.net=nosuch")
	refused(serve(), "web_jwt_users", "no user", "yardr user list")
	set(store.WebJWTUsersSetting, "jo@example.net=Jo")
	// Keys that cannot be fetched.
	refused(serve(), "web_jwt_jwks", "could not fetch")
}

// In JWT mode with keys that fetch, serve starts and records the view; an
// explicitly empty --as stands aside for the mode, as it does for web_as.
func TestWebServeJWTModeServes(t *testing.T) {
	home, st, _ := statusYard(t, "abcdef123456")
	ctx := context.Background()
	t.Setenv("YARDR_WEB_AS", "")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": "a",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	}))
	t.Cleanup(jwks.Close)
	for k, v := range map[string]string{
		store.WebJWTIssuerSetting:   "https://team.example.net",
		store.WebJWTAudienceSetting: "aud-tag",
		store.WebJWTJWKSSetting:     jwks.URL,
		store.WebJWTUsersSetting:    "jo@example.net=Jo",
	} {
		if err := st.SetSetting(ctx, k, v, "Jo"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Home: home}
	serves := func(args ...string) {
		t.Helper()
		cmd := (&app{st: st, cfg: cfg, actor: "me"}).rootCmd()
		cmd.SetArgs(append([]string{"web", "serve", "--addr", "127.0.0.1:0"}, args...))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(sctx) }()
		var views []webView
		for deadline := time.Now().Add(10 * time.Second); len(views) == 0; time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				t.Fatalf("web serve %v ended: %v", args, err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("web serve %v recorded nothing", args)
			}
			views = webViews(cfg)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("web serve did not stop")
		}
	}
	serves()
	serves("--as=")
}
