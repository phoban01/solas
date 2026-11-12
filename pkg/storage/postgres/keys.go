package postgres

import (
	"fmt"
	"strings"

	"k8s.io/apiserver/pkg/storage"
)

// objectKey maps a storage key to the key column of its row: the part
// after the resource name, as the DynamoDB sort key, spec 2.2. It
// starts with "/", for example "/ns/name" for "/pods/ns/name". For a
// recursive key the result is a prefix.
func (s *store) objectKey(key string, recursive bool) (string, error) {
	prepared, err := storage.PrepareKey(s.resourcePrefix, key, recursive)
	if err != nil {
		return "", err
	}
	rest := strings.TrimPrefix(prepared, s.base())
	if !strings.HasPrefix(rest, "/") {
		return "", fmt.Errorf("invalid key: %q lacks resource prefix: %q", key, s.resourcePrefix)
	}
	return rest, nil
}

// storageKey maps a key column back to its storage key.
func (s *store) storageKey(k string) string {
	return s.base() + k
}

// base is the resource prefix with no trailing "/".
func (s *store) base() string {
	return strings.TrimSuffix(s.resourcePrefix, "/")
}
