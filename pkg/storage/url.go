// Package storage reads the storage URL, which picks the shared store of
// the API server, spec 2.6.
package storage

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// Kind names a store.
type Kind string

const (
	// DynamoDB is the DynamoDB store, spec 2.1 to 2.4.
	DynamoDB Kind = "dynamodb"
	// Etcd is the etcd store, spec 2.7.
	Etcd Kind = "etcd"
	// Postgres is the Postgres store, spec 2.8.
	Postgres Kind = "postgres"
)

// Config is a parsed storage URL.
type Config struct {
	Kind Kind

	// Table, Region, Endpoint, and CreateTable hold the DynamoDB settings.
	// An empty Region or Endpoint means the AWS default.
	Table       string
	Region      string
	Endpoint    string
	CreateTable bool

	// Endpoints holds the etcd client URLs, for example
	// http://10.0.0.1:2379.
	Endpoints []string

	// Host, Database, SSLMode, and CreateTables hold the Postgres settings.
	// Host is host:port.
	Host         string
	Database     string
	SSLMode      string
	CreateTables bool
}

//= spec/solas.md#2-6-storage-url
//# The store MUST be a setting of the API server, in the form of one
//# storage URL.

// Parse reads a storage URL.
func Parse(raw string) (Config, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return Config{}, fmt.Errorf("storage URL %q: want <scheme>://...", raw)
	}
	authority, query, _ := strings.Cut(rest, "?")
	authority, path, _ := strings.Cut(authority, "/")
	if path != "" {
		return Config{}, fmt.Errorf("storage URL %q: a path is not allowed", raw)
	}
	if strings.Contains(authority, "@") {
		return Config{}, fmt.Errorf("storage URL %q: credentials come from the environment, not the URL", raw)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return Config{}, fmt.Errorf("storage URL %q: %w", raw, err)
	}

	switch Kind(scheme) {
	case DynamoDB:
		return parseDynamo(raw, authority, values)
	case Etcd:
		return parseEtcd(raw, authority, values)
	case Postgres:
		return parsePostgres(raw, authority, values)
	}
	//= spec/solas.md#2-6-storage-url
	//# The server MUST reject a storage URL with another scheme.
	return Config{}, fmt.Errorf("storage URL %q: scheme %q is not dynamodb, etcd, or postgres", raw, scheme)
}

//= spec/solas.md#2-6-storage-url
//# A URL with the scheme `dynamodb` MUST select the DynamoDB store.

func parseDynamo(raw, table string, values url.Values) (Config, error) {
	//= spec/solas.md#2-6-storage-url
	//# The server MUST reject a storage URL with a query key that this section
	//# does not name for its scheme.
	if err := onlyKeys(raw, values, "region", "endpoint", "create-table"); err != nil {
		return Config{}, err
	}
	//= spec/solas.md#2-6-storage-url
	//# An empty table name selects the default table name.
	if table == "" {
		table = dynamo.DefaultTable
	}
	c := Config{Kind: DynamoDB, Table: table, Region: values.Get("region"), Endpoint: values.Get("endpoint")}
	if v := values.Get("create-table"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("storage URL %q: create-table=%q is not true or false", raw, v)
		}
		c.CreateTable = b
	}
	if c.Endpoint != "" {
		if u, err := url.Parse(c.Endpoint); err != nil || u.Scheme == "" || u.Host == "" {
			return Config{}, fmt.Errorf("storage URL %q: endpoint %q is not a URL", raw, c.Endpoint)
		}
	}
	return c, nil
}

//= spec/solas.md#2-6-storage-url
//# A URL with the scheme `etcd` MUST select the etcd store.

func parseEtcd(raw, hosts string, values url.Values) (Config, error) {
	if err := onlyKeys(raw, values); err != nil {
		return Config{}, err
	}
	if hosts == "" {
		return Config{}, fmt.Errorf("storage URL %q: no etcd endpoints", raw)
	}
	c := Config{Kind: Etcd}
	for _, h := range strings.Split(hosts, ",") {
		host, port, err := net.SplitHostPort(h)
		if err != nil || host == "" {
			return Config{}, fmt.Errorf("storage URL %q: endpoint %q is not host:port", raw, h)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("storage URL %q: endpoint %q has a bad port", raw, h)
		}
		c.Endpoints = append(c.Endpoints, "http://"+h)
	}
	return c, nil
}

//= spec/solas.md#2-6-storage-url
//# A URL with the scheme `postgres` MUST select the Postgres store.

// DefaultDatabase is the database name when the URL has none, spec 2.6.
const DefaultDatabase = "solas"

// DefaultSSLMode is the TLS mode when the URL has none, spec 2.6.
const DefaultSSLMode = "require"

func parsePostgres(raw, host string, values url.Values) (Config, error) {
	//= spec/solas.md#2-6-storage-url
	//# The server MUST reject a storage URL with a query key that this section
	//# does not name for its scheme.
	if err := onlyKeys(raw, values, "database", "sslmode", "create-tables"); err != nil {
		return Config{}, err
	}
	h, port, err := net.SplitHostPort(host)
	if err != nil || h == "" {
		return Config{}, fmt.Errorf("storage URL %q: want postgres://<host>:<port>", raw)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return Config{}, fmt.Errorf("storage URL %q: %q has a bad port", raw, host)
	}
	c := Config{Kind: Postgres, Host: host, Database: values.Get("database"), SSLMode: values.Get("sslmode")}
	if c.Database == "" {
		c.Database = DefaultDatabase
	}
	if c.SSLMode == "" {
		c.SSLMode = DefaultSSLMode
	}
	if v := values.Get("create-tables"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("storage URL %q: create-tables=%q is not true or false", raw, v)
		}
		c.CreateTables = b
	}
	return c, nil
}

func onlyKeys(raw string, values url.Values, allowed ...string) error {
	for k := range values {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return fmt.Errorf("storage URL %q: unknown query key %q", raw, k)
		}
	}
	return nil
}

// String prints the URL of the configuration.
func (c Config) String() string {
	switch c.Kind {
	case DynamoDB:
		// The URL reads as people write it: the endpoint is not escaped
		// unless it has a character that the query would split on.
		var q []string
		if c.CreateTable {
			q = append(q, "create-table=true")
		}
		if c.Region != "" {
			q = append(q, "region="+queryValue(c.Region))
		}
		if c.Endpoint != "" {
			q = append(q, "endpoint="+queryValue(c.Endpoint))
		}
		s := "dynamodb://" + c.Table
		if len(q) > 0 {
			s += "?" + strings.Join(q, "&")
		}
		return s
	case Etcd:
		hosts := make([]string, len(c.Endpoints))
		for i, e := range c.Endpoints {
			hosts[i] = strings.TrimPrefix(e, "http://")
		}
		return "etcd://" + strings.Join(hosts, ",")
	case Postgres:
		var q []string
		if c.Database != DefaultDatabase {
			q = append(q, "database="+queryValue(c.Database))
		}
		if c.SSLMode != DefaultSSLMode {
			q = append(q, "sslmode="+queryValue(c.SSLMode))
		}
		if c.CreateTables {
			q = append(q, "create-tables=true")
		}
		s := "postgres://" + c.Host
		if len(q) > 0 {
			s += "?" + strings.Join(q, "&")
		}
		return s
	}
	return ""
}

// queryValue escapes a query value only when it holds &, #, +, %, or a
// space, which the query would read in another way.
func queryValue(v string) string {
	if strings.ContainsAny(v, "&#+% ") {
		return url.QueryEscape(v)
	}
	return v
}

//= spec/solas.md#2-6-storage-url
//# The user and the password of a `postgres` URL MUST come from the
//# environment variables `PGUSER` and `PGPASSWORD`.

// PostgresURL prints the connection URL of a Postgres store for the
// driver: the host, the database, and the TLS mode, and no user or
// password, which the driver reads from PGUSER and PGPASSWORD.
func (c Config) PostgresURL() string {
	return "postgres://" + c.Host + "/" + url.PathEscape(c.Database) + "?sslmode=" + url.QueryEscape(c.SSLMode)
}
