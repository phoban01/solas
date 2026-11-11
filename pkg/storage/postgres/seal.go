package postgres

import (
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
