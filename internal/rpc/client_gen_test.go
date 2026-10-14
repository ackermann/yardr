package rpc

import (
	"os"
	"testing"

	"yardr.dev/yardr/internal/rpc/clientgen"
)

// client_gen.go is what the generator writes now: after a change to
// store.API, run `go generate ./internal/rpc` and commit it.
func TestClientGenIsCurrent(t *testing.T) {
	want, err := clientgen.Generate("../store/api.go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("client_gen.go")
	if err != nil {
		t.Fatalf("%v: run go generate ./internal/rpc", err)
	}
	if string(got) != string(want) {
		t.Error("client_gen.go is not what the generator writes now: run go generate ./internal/rpc and commit it")
	}
}
