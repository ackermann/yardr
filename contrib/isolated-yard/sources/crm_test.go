package main

import (
	"context"
	_ "embed"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

//go:embed crm.yaml
var crmFile string

// crm.yaml is a declaration yardr takes, and its calls are answered by the
// stand-in's customer system as the file's comments say: a customer with
// the keys the call selects, a redirect that is not followed, an answer
// over the call's bound, and nothing without the token.
func TestCRMDeclarationAgainstTheStandIn(t *testing.T) {
	crm := &standin.Server{CRMToken: "crm-standin", CRMUpdateToken: "crm-update-standin"}
	srv := httptest.NewServer(crm.Handler())
	defer srv.Close()
	d, err := source.Parse("crm.yaml", []byte(strings.Replace(crmFile, "BASE/crm", srv.URL+"/crm", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := source.Do(ctx, d, "customer", map[string]string{"id": "1"}, "crm-standin", store.ReadSecretFile)
	if err != nil || string(res.Answer) != `{"id":"1","name":"Customer 1","open_tickets":2,"owner":"ada","plan":"pro"}` {
		t.Fatalf("customer = %+v, %v", res, err)
	}
	if res, err = source.Do(ctx, d, d.Check, nil, "crm-standin", store.ReadSecretFile); err != nil || !res.OK() {
		t.Fatalf("the check call = %+v, %v", res, err)
	}
	if res, err = source.Do(ctx, d, "customer", map[string]string{"id": "1"}, "another", store.ReadSecretFile); err != nil || res.Status != 401 {
		t.Fatalf("with another token = %+v, %v", res, err)
	}
	// The note is an update: taken with the credential the file names for
	// one and not with the other, and once for a key sent twice.
	note := map[string]string{"id": "1", "text": "asked for a copy of the invoice"}
	if d.Credential.EnvOf(source.Update) != "CRM_UPDATE_TOKEN" || d.Idempotency != "Idempotency-Key" {
		t.Fatalf("credential = %+v, idempotency %q", d.Credential, d.Idempotency)
	}
	if res, err = source.DoKeyed(ctx, d, "note", note, "crm-standin", "k1", store.ReadSecretFile); err != nil || res.Status != 403 {
		t.Fatalf("a note with the token of the reads = %+v, %v", res, err)
	}
	for i := 0; i < 2; i++ {
		if res, err = source.DoKeyed(ctx, d, "note", note, "crm-update-standin", "k1", store.ReadSecretFile); err != nil || string(res.Answer) != `{"customer":"1","id":"n1","text":"asked for a copy of the invoice"}` {
			t.Fatalf("a note = %+v, %v", res, err)
		}
	}
	if got := crm.CustomerNotes(); len(got) != 1 || got[0].Key != "k1" || crm.NotesAsked() != 3 {
		t.Fatalf("the stand-in has %+v, asked %d times", got, crm.NotesAsked())
	}
	for call, want := range map[string]string{"moved": source.Redirect, "big": source.TooLarge} {
		var failed *source.CallError
		if _, err := source.Do(ctx, d, call, nil, "crm-standin", store.ReadSecretFile); !errors.As(err, &failed) || failed.Reason != want {
			t.Errorf("%s = %v, want %s", call, err, want)
		}
	}
}
