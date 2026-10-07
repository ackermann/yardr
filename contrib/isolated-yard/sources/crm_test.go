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
)

//go:embed crm.yaml
var crmFile string

// crm.yaml is a declaration yardr takes, and its calls are answered by the
// stand-in's customer system as the file's comments say: a customer with
// the keys the call selects, a redirect that is not followed, an answer
// over the call's bound, and nothing without the token.
func TestCRMDeclarationAgainstTheStandIn(t *testing.T) {
	srv := httptest.NewServer((&standin.Server{CRMToken: "crm-standin"}).Handler())
	defer srv.Close()
	d, err := source.Parse("crm.yaml", []byte(strings.Replace(crmFile, "BASE/crm", srv.URL+"/crm", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := source.Do(ctx, d, "customer", map[string]string{"id": "1"}, "crm-standin")
	if err != nil || string(res.Answer) != `{"id":"1","name":"Customer 1","open_tickets":2,"owner":"ada","plan":"pro"}` {
		t.Fatalf("customer = %+v, %v", res, err)
	}
	if res, err = source.Do(ctx, d, d.Check, nil, "crm-standin"); err != nil || !res.OK() {
		t.Fatalf("the check call = %+v, %v", res, err)
	}
	if res, err = source.Do(ctx, d, "customer", map[string]string{"id": "1"}, "another"); err != nil || res.Status != 401 {
		t.Fatalf("with another token = %+v, %v", res, err)
	}
	for call, want := range map[string]string{"moved": source.Redirect, "big": source.TooLarge} {
		var failed *source.CallError
		if _, err := source.Do(ctx, d, call, nil, "crm-standin"); !errors.As(err, &failed) || failed.Reason != want {
			t.Errorf("%s = %v, want %s", call, err, want)
		}
	}
}
