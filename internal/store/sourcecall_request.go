package store

import (
	"context"

	"yardr.dev/yardr/internal/source"
)

// validateSourceCallParams uses the inputs a run-ask card captured where
// there are any, so validation cannot read a different file version.
func validateSourceCallParams(d *source.Declaration, name string, params map[string]string, inputs map[string][]byte) error {
	if inputs != nil {
		return source.ValidateCallParamsWithInputs(d, name, params, inputs, ReadSecretFile)
	}
	return source.ValidateCallParams(d, name, params, ReadSecretFile)
}

// sourceCallRequest makes the network request outside the store write, or
// runs the command that answers the call. A run-ask's captured inputs go to
// the same request path as ordinary calls.
func (s *Store) sourceCallRequest(ctx context.Context, d *source.Declaration, call *source.Call, params map[string]string, credential, key string, inputs map[string][]byte) (*source.Result, error) {
	// A command is handed a file parameter's path and reads the file
	// itself, as a build is, so the bytes a card read are not its to hold
	// to; and a read has no key.
	if len(call.Run) > 0 {
		return source.RunCall(ctx, d, call.Name, params, credential, s.home, ReadSecretFile)
	}
	if len(call.Build) > 0 {
		body, err := source.BuildBody(ctx, d, call.Name, params, credential, s.home, ReadSecretFile)
		if err != nil {
			return nil, err
		}
		return source.DoKeyedBody(ctx, d, call.Name, params, credential, key, body, ReadSecretFile)
	}
	if inputs != nil {
		return source.DoKeyedWithInputs(ctx, d, call.Name, params, credential, key, inputs, ReadSecretFile)
	}
	return source.DoKeyed(ctx, d, call.Name, params, credential, key, ReadSecretFile)
}
