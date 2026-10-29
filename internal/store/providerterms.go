package store

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ProviderTermsSetting holds the yard's provider-terms records, one JSON
// value beside the other settings: what the operator read of a model
// provider's terms before hosted Google data may reach an agent of that
// provider (design yardr-hosted-4dx0 3.9). It is written only by the
// hosted Google setup, on the operator's own reading; no code in the
// yard reads a provider's terms and judges them.
const ProviderTermsSetting = "provider_terms"

// ProviderTerms is one provider-terms record: the provider whose terms
// were read, the terms page relied on, the date it was read, and the
// operator's statement whether those terms bar creating, training or
// improving a general model from the data. A yard with no record for a
// session's provider has not made the reading for it, which the agent
// gate of 3.9 treats as no allowance.
type ProviderTerms struct {
	Provider string `json:"provider"`
	// TermsURL is the page the operator read and relied on.
	TermsURL string `json:"terms_url"`
	// ReadOn is the date the operator read it, as YYYY-MM-DD.
	ReadOn string `json:"read_on"`
	// BarsGeneralTraining is the operator's statement: true where the
	// terms bar creating, training or improving a general model from
	// the data, false where they do not.
	BarsGeneralTraining bool `json:"bars_general_training"`
}

// CheckProviderTerms holds one record to what a record must hold: a
// provider, an http(s) terms page, and a read date.
func CheckProviderTerms(r ProviderTerms) error {
	if strings.TrimSpace(r.Provider) == "" {
		return fmt.Errorf("%s: a record names its provider", ProviderTermsSetting)
	}
	u, err := url.Parse(r.TermsURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s: the terms page %q is no http(s) address", ProviderTermsSetting, r.TermsURL)
	}
	if _, err := time.Parse("2006-01-02", r.ReadOn); err != nil {
		return fmt.Errorf("%s: the date read %q is no date (YYYY-MM-DD)", ProviderTermsSetting, r.ReadOn)
	}
	return nil
}

// ProviderTermsOf reads the records the provider_terms setting holds.
// A setting this build cannot read holds none, as a yard that made no
// reading holds none.
func ProviderTermsOf(settings map[string]string) []ProviderTerms {
	var out []ProviderTerms
	if err := json.Unmarshal([]byte(settings[ProviderTermsSetting]), &out); err != nil {
		return nil
	}
	return out
}

// WithProviderTerms is the provider_terms setting's value with r in it,
// replacing any record for its provider: the value SetSetting takes.
func WithProviderTerms(settings map[string]string, r ProviderTerms) (string, error) {
	if err := CheckProviderTerms(r); err != nil {
		return "", err
	}
	out := ProviderTermsOf(settings)
	kept := out[:0]
	for _, old := range out {
		if !strings.EqualFold(old.Provider, r.Provider) {
			kept = append(kept, old)
		}
	}
	raw, err := json.Marshal(append(kept, r))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// checkProviderTermsSetting holds the setting's whole value: a JSON list
// of records, each as CheckProviderTerms takes it.
func checkProviderTermsSetting(v string) error {
	var rs []ProviderTerms
	if err := json.Unmarshal([]byte(v), &rs); err != nil {
		return fmt.Errorf("%s: no list of records: %v", ProviderTermsSetting, err)
	}
	for _, r := range rs {
		if err := CheckProviderTerms(r); err != nil {
			return err
		}
	}
	return nil
}
