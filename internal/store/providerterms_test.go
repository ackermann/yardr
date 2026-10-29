package store_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The provider-terms record beside the yard's settings: written as one
// JSON list, read back by provider, replaced per provider, and refused
// in shapes no record takes.
func TestProviderTermsRecord(t *testing.T) {
	t.Parallel()
	rec := store.ProviderTerms{Provider: "Anthropic", TermsURL: "https://provider.example/terms", ReadOn: "2026-10-27", BarsGeneralTraining: true}
	settings := map[string]string{}
	value, err := store.WithProviderTerms(settings, rec)
	if err != nil {
		t.Fatal(err)
	}
	settings[store.ProviderTermsSetting] = value
	if got := termsFor(settings, "Anthropic"); got == nil || *got != rec {
		t.Fatalf("the record for Anthropic = %+v, want %+v", got, rec)
	}
	if got := termsFor(settings, "OpenAI"); got != nil {
		t.Fatalf("a record for a provider never read: %+v", got)
	}
	// A second reading for the same provider replaces the first, and
	// another provider's stands beside it.
	again := rec
	again.Provider = "anthropic" // the provider is matched without regard to case
	again.ReadOn, again.BarsGeneralTraining = "2026-11-01", false
	value, err = store.WithProviderTerms(settings, again)
	if err != nil {
		t.Fatal(err)
	}
	settings[store.ProviderTermsSetting] = value
	other, err := store.WithProviderTerms(settings, store.ProviderTerms{Provider: "OpenAI", TermsURL: "https://other.example/terms", ReadOn: "2026-11-02"})
	if err != nil {
		t.Fatal(err)
	}
	settings[store.ProviderTermsSetting] = other
	if all := store.ProviderTermsOf(settings); len(all) != 2 {
		t.Fatalf("the records = %+v, want Anthropic replaced and OpenAI beside it", all)
	}
	if got := termsFor(settings, "Anthropic"); got == nil || got.ReadOn != "2026-11-01" || got.BarsGeneralTraining {
		t.Fatalf("the replaced record = %+v", got)
	}
	for _, bad := range []store.ProviderTerms{
		{TermsURL: "https://provider.example/terms", ReadOn: "2026-10-27"},
		{Provider: "Anthropic", TermsURL: "provider.example/terms", ReadOn: "2026-10-27"},
		{Provider: "Anthropic", TermsURL: "https://provider.example/terms", ReadOn: "27 Oct 2026"},
	} {
		if _, err := store.WithProviderTerms(settings, bad); err == nil {
			t.Errorf("a record %+v was taken", bad)
		}
	}
	// The setting itself holds the shape, and the store keeps it.
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.ProviderTermsSetting, "not json", "jo"); err == nil || !strings.Contains(err.Error(), store.ProviderTermsSetting) {
		t.Fatalf("the setting took %q: %v", "not json", err)
	}
	if err := s.SetSetting(ctx, store.ProviderTermsSetting, value, "jo"); err != nil {
		t.Fatal(err)
	}
	all, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := termsFor(all, "Anthropic"); got == nil || got.ReadOn != "2026-11-01" {
		t.Fatalf("the stored record = %+v", got)
	}
}

// termsFor is the record for provider among the setting's records, nil
// where there is none.
func termsFor(settings map[string]string, provider string) *store.ProviderTerms {
	for _, r := range store.ProviderTermsOf(settings) {
		if strings.EqualFold(r.Provider, provider) {
			r := r
			return &r
		}
	}
	return nil
}
