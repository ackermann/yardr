package store

import (
	"context"

	"yardr.dev/yardr/internal/source"
)

// validateSourceCallParams uses the inputs a run-ask card captured where
// there are any, so validation cannot read a different file version.
func validateSourceCallParams(d *source.Declaration, name string, params map[string]string, inputs map[string][]byte) error {
	if inputs != nil {
		return source.ValidateCallParamsWithInputs(d, name, params, inputs)
	}
	return source.ValidateCallParams(d, name, params)
}

// sourceCallRequest makes the network request outside the store write. A
// run-ask's captured inputs go to the same request path as ordinary calls.
func (s *Store) sourceCallRequest(ctx context.Context, d *source.Declaration, call *source.Call, params map[string]string, credential, key string, inputs map[string][]byte) (*source.Result, error) {
	if len(call.Build) > 0 {
		body, err := source.BuildBody(ctx, d, call.Name, params, credential, s.home)
		if err != nil {
			return nil, err
		}
		return source.DoKeyedBody(ctx, d, call.Name, params, credential, key, body)
	}
	if inputs != nil {
		return source.DoKeyedWithInputs(ctx, d, call.Name, params, credential, key, inputs)
	}
	return source.DoKeyed(ctx, d, call.Name, params, credential, key)
}
