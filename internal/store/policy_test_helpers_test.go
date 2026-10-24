package store

import (
	"context"

	bolt "go.etcd.io/bbolt"
)

type storePolicyFunc func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error

func (f storePolicyFunc) May(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	return f(ctx, tx, actor, act, obj)
}
