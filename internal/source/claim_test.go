package source

import "testing"

// ClaimAddress is the hear key's field with the notify address's
// variable, and nothing for a source that hears nothing back.
func TestClaimAddress(t *testing.T) {
	base := pushover + "hook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: P-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [chat]\n"
	d, err := Parse("p.yaml", []byte(base+"hear:\n  field: chat\n  address: { env: PUSHOVER_USER }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if field, env, ok := d.ClaimAddress(); !ok || field != "chat" || env != "PUSHOVER_USER" {
		t.Fatalf("ClaimAddress = %q, %q, %v", field, env, ok)
	}
	d, err = Parse("p.yaml", []byte(pushover))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.ClaimAddress(); ok {
		t.Fatal("a source with no hear key claims an address")
	}
	var none *Declaration
	if _, _, ok := none.ClaimAddress(); ok {
		t.Fatal("no declaration claims an address")
	}
}
