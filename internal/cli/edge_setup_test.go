package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bbolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

type edgeSetupPolicy struct{ deniedSource string }

func (p edgeSetupPolicy) May(ctx context.Context, tx *bbolt.Tx, actor string, act store.Action, obj store.Object) error {
	if act == store.ActSetSourceKey && obj.Name == p.deniedSource {
		return fmt.Errorf("test policy denies setting %s", obj.Name)
	}
	return store.CorePolicy.May(ctx, tx, actor, act, obj)
}

// googleSourceText is one google source's declaration as its pack brings
// it, against no real Google address: the hosted setup reads its name
// and credential alone.
func googleSourceText(name string) string {
	return `name: ` + name + `
base: https://google.example.com
credential:
  env: GOOGLE_TOKEN
  header: "Authorization: Bearer {credential}"
  mint:
    - name: GOOGLE_ACCOUNT
      secret: false
  mint_command: [google-mint]
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
}

// googleYard adds the five google sources to st, each with the account
// ada@example.com in one shared env file, as a google setup leaves them.
func googleYard(t *testing.T, st store.API) {
	t.Helper()
	ctx := context.Background()
	env := filepath.Join(t.TempDir(), "google.env")
	if err := os.WriteFile(env, []byte("GOOGLE_ACCOUNT=ada@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"google-drive", "google-docs", "google-slides", "google-calendar", "google-mail"} {
		if _, err := st.PutSource(ctx, name+".yaml", googleSourceText(name), "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetSource(ctx, name, store.SourceEnvFile, env, "jo"); err != nil {
			t.Fatal(err)
		}
	}
}

// The hosted setup on a hosted yard: against a stand-in edge whose
// connection granted only some of the fixed set, the granted sources
// take the hosted mint as their effective mint, the ungranted keep the
// pack's google-mint, and google-mail is untouched. Clearing a set
// source's mint_command shows the pack's mint again. With no reading of
// the provider's terms no record stands; with --terms-read one does,
// naming the yard kind's provider.
func TestEdgeSetupGoogleHosted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	if err := st.SetSetting(ctx, store.HostedSetting, "true", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.DefaultKindSetting, "codex", "jo"); err != nil {
		t.Fatal(err)
	}
	googleYard(t, st)
	edge.Behave(func(b *standin.EdgeBehaviour) {
		b.MintToken = "ya29.probe"
		b.MintGranted = []string{"openid", "email",
			"https://www.googleapis.com/auth/documents",
			"https://www.googleapis.com/auth/calendar.events",
			"https://www.googleapis.com/auth/calendar.calendarlist.readonly"}
	})
	out, errs, err := edgeAs(t, st, srv.Client(), "jo", "edge", "setup-google", srv.URL)
	if err != nil {
		t.Fatalf("setup-google: %v\n%s%s", err, out, errs)
	}
	for _, want := range []string{
		"source google-docs: mint set to yardr edge mint-google " + srv.URL + " google-docs",
		"source google-calendar: mint set to yardr edge mint-google " + srv.URL + " google-calendar",
		"source google-drive: kept the pack's mint",
		"source google-slides: kept the pack's mint",
		"needs https://www.googleapis.com/auth/drive.file, missing from the Google connection's grant",
		"needs https://www.googleapis.com/auth/presentations, missing from the Google connection's grant",
		"source google-mail: skipped",
		"no provider-terms record is made until you do",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup-google says no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out+errs, "ya29.probe") {
		t.Errorf("a probe's token was printed:\n%s%s", out, errs)
	}
	effective := func(name string) string {
		t.Helper()
		src, err := st.GetSource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(src.Mint, " ")
	}
	// The pack's mint is the copy under the yard's own sources directory:
	// its suffix names it, wherever the yard stands.
	packMint := func(name string) string { return filepath.Join("sources", name, "google-mint") }
	for name, want := range map[string]string{
		"google-docs":     "yardr edge mint-google " + srv.URL + " google-docs",
		"google-calendar": "yardr edge mint-google " + srv.URL + " google-calendar",
	} {
		if got := effective(name); got != want {
			t.Errorf("%s mints by %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"google-drive", "google-slides", "google-mail"} {
		if got := effective(name); !strings.HasSuffix(got, packMint(name)) {
			t.Errorf("%s mints by %q, want the pack's google-mint", name, got)
		}
	}
	if src, err := st.GetSource(ctx, "google-mail"); err != nil || src.MintCommand != "" {
		t.Errorf("google-mail stands with mint_command %q, %v: untouched", src.MintCommand, err)
	}
	// The setup grants nothing extra.
	if grants, err := st.SourceGrants(ctx, ""); err != nil || len(grants) != 0 {
		t.Errorf("the setup granted %+v, %v", grants, err)
	}
	// One probe a source, for the yard's account, and no more.
	if mints := edge.State().Mints; len(mints) != 4 {
		t.Errorf("the edge took %d mints, want the four probes", len(mints))
	} else {
		for _, m := range mints {
			if m.Account != "ada@example.com" {
				t.Errorf("a probe minted for %q", m.Account)
			}
		}
	}
	// No reading, no record.
	settings, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec := store.ProviderTermsFor(settings, "Anthropic"); rec != nil {
		t.Fatalf("a provider-terms record stands for a provider not configured by the yard: %+v", rec)
	}
	if rec := store.ProviderTermsFor(settings, "OpenAI"); rec != nil {
		t.Fatalf("a provider-terms record stands without a reading: %+v", rec)
	}
	// Clearing the setting shows the pack's mint again.
	if _, err := st.SetSource(ctx, "google-docs", store.SourceMintCommand, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := effective("google-docs"); !strings.HasSuffix(got, packMint("google-docs")) {
		t.Errorf("cleared, google-docs mints by %q, want the pack's google-mint", got)
	}
	// A reading must include an explicit answer to the training question,
	// and invalid dates fail before the setup probes or sets any source.
	probes := len(edge.State().Mints)
	if out, _, err := edgeAs(t, st, srv.Client(), "jo", "edge", "setup-google", srv.URL,
		"--terms-read", "--terms-url", "https://provider.example/terms"); err == nil {
		t.Fatalf("a reading without a training answer succeeded:\n%s", out)
	}
	if got := len(edge.State().Mints); got != probes {
		t.Errorf("a reading without a training answer made %d probes, want none", got-probes)
	}
	if out, _, err := edgeAs(t, st, srv.Client(), "jo", "edge", "setup-google", srv.URL,
		"--terms-read", "--terms-url", "https://provider.example/terms", "--read-on", "not-a-date", "--bars-general-training=false"); err == nil {
		t.Fatalf("a reading with an invalid date succeeded:\n%s", out)
	}
	if got := len(edge.State().Mints); got != probes {
		t.Errorf("an invalid reading date made %d probes, want none", got-probes)
	}
	// With the reading, the record names the yard kind's configured provider.
	out, _, err = edgeAs(t, st, srv.Client(), "jo", "edge", "setup-google", srv.URL,
		"--terms-read", "--terms-url", "https://provider.example/terms", "--read-on", "2026-10-27", "--bars-general-training=true")
	if err != nil {
		t.Fatalf("setup-google with the reading: %v\n%s", err, out)
	}
	if !strings.Contains(out, "provider terms: OpenAI, read at https://provider.example/terms on 2026-10-27; they bar creating") {
		t.Errorf("the reading is not said in:\n%s", out)
	}
	settings, err = st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := store.ProviderTermsFor(settings, "OpenAI")
	if rec == nil || rec.TermsURL != "https://provider.example/terms" || rec.ReadOn != "2026-10-27" || !rec.BarsGeneralTraining {
		t.Fatalf("the provider-terms record = %+v", rec)
	}
	// Permission to change one source does not authorize probing the rest.
	if setter, ok := st.(interface{ SetPolicy(store.Policy) }); ok {
		setter.SetPolicy(edgeSetupPolicy{deniedSource: "google-calendar"})
		probes = len(edge.State().Mints)
		out, _, err = edgeAs(t, st, srv.Client(), "jo", "edge", "setup-google", srv.URL)
		setter.SetPolicy(nil)
		if err == nil || !strings.Contains(err.Error(), "google-calendar") {
			t.Fatalf("a policy refusing one source let setup proceed: %v\n%s", err, out)
		}
		if got := len(edge.State().Mints); got != probes {
			t.Errorf("a denied source setup made %d probes, want none", got-probes)
		}
	} else {
		t.Fatal("the test store cannot set a policy")
	}
}
