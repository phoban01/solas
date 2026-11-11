// Package postgres is the Postgres store, spec 2.8: three tables in a
// schema named by the storage prefix, the write as one transaction
// behind the row lock of the counter row, the list in one snapshot, and
// the watch by polling. ADR 0026.
package postgres

import (
	"regexp"
	"strings"
)

var notIdent = regexp.MustCompile(`[^a-z0-9_]+`)

// SchemaName is the schema that holds the tables of a storage prefix,
// spec 2.8: the prefix without its leading slash, in lower case, with
// every run of other characters as one underscore. "/registry" is
// "registry".
func SchemaName(prefix string) string {
	s := strings.ToLower(strings.TrimPrefix(prefix, "/"))
	s = notIdent.ReplaceAllString(s, "_")
	if s == "" {
		return "registry"
	}
	return s
}
