package postgres

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
)

// Watch comes with #311.
func (s *store) Watch(ctx context.Context, key string, opts storage.ListOptions) (watch.Interface, error) {
	return nil, errors.New("postgres: the watch is not built yet, #311")
}
