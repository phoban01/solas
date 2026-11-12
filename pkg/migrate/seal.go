package migrate

import (
	"context"
	"errors"

	"github.com/phoban01/solas/pkg/storage/dynamo"
	"github.com/phoban01/solas/pkg/storage/postgres"
)

// ErrCannotSeal is the error of a seal on a store that has no counter,
// spec 12.3: the etcd store.
var ErrCannotSeal = errors.New("only a DynamoDB or a Postgres store can be sealed")

// CanSeal reports whether the store has a seal, spec 12.1: a DynamoDB or
// a Postgres store. An etcd store has none, spec 12.3.
func (s *Store) CanSeal() bool { return s.Dynamo != nil || s.Postgres != nil }

// Seal seals every resource of the store, spec 12.1.
func (s *Store) Seal(ctx context.Context) error {
	switch {
	case s.Dynamo != nil:
		return dynamo.Seal(ctx, *s.Dynamo, s.Prefix, ResourcePrefixes())
	case s.Postgres != nil:
		return postgres.Seal(ctx, *s.Postgres, s.Prefix, ResourcePrefixes())
	}
	return ErrCannotSeal
}

// Unseal removes the seal of every resource of the store.
func (s *Store) Unseal(ctx context.Context) error {
	switch {
	case s.Dynamo != nil:
		return dynamo.Unseal(ctx, *s.Dynamo, s.Prefix, ResourcePrefixes())
	case s.Postgres != nil:
		return postgres.Unseal(ctx, *s.Postgres, s.Prefix, ResourcePrefixes())
	}
	return ErrCannotSeal
}

// Sealed reports whether any resource of the store is sealed. A store
// that has no seal is never sealed.
func (s *Store) Sealed(ctx context.Context) (bool, error) {
	switch {
	case s.Dynamo != nil:
		return dynamo.IsStoreSealed(ctx, *s.Dynamo, s.Prefix, ResourcePrefixes())
	case s.Postgres != nil:
		return postgres.IsStoreSealed(ctx, *s.Postgres, s.Prefix, ResourcePrefixes())
	}
	return false, nil
}

// RaiseCounters raises the counter of each resource to at least min,
// spec 13.3, on a store that has counters. On etcd the operator raises
// the revision, and this does nothing.
func (s *Store) RaiseCounters(ctx context.Context, min uint64) error {
	switch {
	case s.Dynamo != nil:
		return dynamo.RaiseCounters(ctx, *s.Dynamo, s.Prefix, ResourcePrefixes(), min)
	case s.Postgres != nil:
		return postgres.RaiseCounters(ctx, *s.Postgres, s.Prefix, ResourcePrefixes(), min)
	}
	return nil
}
