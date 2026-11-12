package postgres

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

//= spec/solas.md#12-1-seal
//# A write that fails because the store is sealed MUST return
//# `503 ServiceUnavailable`.

//= spec/solas.md#2-8-postgres-store
//# When the counter update changes no row, the store is sealed, section
//# 12.1, and the write MUST fail with `503 ServiceUnavailable`.

// errSealed is the error of a write to a sealed store.
func errSealed(resource string) error {
	return apierrors.NewServiceUnavailable(fmt.Sprintf("store is sealed: %s moved to another store", resource))
}

// IsSealed reports whether err is the error of a write to a sealed store.
func IsSealed(err error) bool {
	return apierrors.IsServiceUnavailable(err) && strings.Contains(err.Error(), "store is sealed")
}

//= spec/solas.md#12-1-seal
//# The Postgres store seals with the flag `sealed` on the `counters` row of
//# each resource.

//= spec/solas.md#12-1-seal
//# A seal MUST NOT change any object.

// Seal seals each resource: a write to it then fails, spec 12.1. Reads
// keep working. prefix is the storage prefix of the API server, and each
// resource prefix has the form /<group>/<resource>.
func Seal(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) error {
	return setSealed(ctx, cfg, prefix, resourcePrefixes, true)
}

// Unseal removes the seal of each resource.
func Unseal(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) error {
	return setSealed(ctx, cfg, prefix, resourcePrefixes, false)
}

func setSealed(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string, sealed bool) error {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return err
		}
		//= spec/solas.md#12-1-seal
		//# To seal a resource that has no `counters` row, the tool MUST insert the
		//# row with `sealed` set.
		//
		// readCounter inserts a missing counter row, with its INIT event. The
		// update below then sets the flag before any write can use the new
		// counter for an object.
		if _, _, err := s.readCounterSealed(ctx); err != nil {
			return err
		}
		//= spec/solas.md#12-1-seal
		//# On the Postgres store, an unseal clears `sealed`.
		if _, err := s.pool.Exec(ctx, "UPDATE "+s.schema+".counters SET sealed = $2 WHERE resource = $1", s.resource, sealed); err != nil {
			return fmt.Errorf("seal %s: %w", s.resource, err)
		}
	}
	return nil
}

// IsStoreSealed reports whether any of the resources is sealed.
func IsStoreSealed(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) (bool, error) {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return false, err
		}
		_, sealed, err := s.readCounterSealed(ctx)
		if err != nil || sealed {
			return sealed, err
		}
	}
	return false, nil
}

// schemaFor is only for error messages of a store that Seal builds.
func schemaFor(resourcePrefix string) (gr schema.GroupResource) {
	parts := strings.Split(strings.Trim(resourcePrefix, "/"), "/")
	if len(parts) == 2 {
		gr.Group, gr.Resource = parts[0], parts[1]
	}
	return gr
}

//= spec/solas.md#13-3-resource-versions
//# On the Postgres store, the restore tool MUST raise `n` of each
//# `counters` row to at least `epoch * 2^32`.

// RaiseCounters raises the counter row of each resource to at least min,
// so every later write gets a resource version above min, spec 13.3. A
// watch from an older version then finds a gap and fails with 410 Gone.
func RaiseCounters(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string, min uint64) error {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return err
		}
		if _, _, err := s.readCounterSealed(ctx); err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, "UPDATE "+s.schema+".counters SET n = $2 WHERE resource = $1 AND n < $2", s.resource, int64(min))
		if err != nil {
			return fmt.Errorf("raise the counter of %s: %w", s.resource, err)
		}
	}
	return nil
}
