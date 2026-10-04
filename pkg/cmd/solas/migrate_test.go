package solas

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/ddbtest"
	"github.com/phoban01/solas/internal/etcdtest"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/migrate"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// run runs solas with args and returns its output.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(context.Background(), NewOptions())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	endpoint := os.Getenv(ddbtest.EndpointEnv)
	if endpoint == "" {
		t.Skipf("%s is not set; run the tests with devbox run test", ddbtest.EndpointEnv)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "local")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local")
	table := ddbtest.TableName(t)
	ddbtest.DeleteTable(t, ddbtest.Client(t), table)
	from := "dynamodb://" + table + "?create-table=true&endpoint=" + url.QueryEscape(endpoint)
	to := "etcd://" + strings.TrimPrefix(etcdtest.Servers(t)[0], "http://")
	prefix := etcdtest.Prefix(t)

	src, err := migrate.Open(ctx, from, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	d := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1", UID: "uid-d1"}}
	d.Status.ClaimRef = &solas.ClaimRef{Member: "a", MemberUID: "uid-a", Namespace: "work", Name: "job", UID: "uid-job"}
	d.Status.FencingToken = 4
	if err := src.Devices.Create(ctx, "/solas.dev/devices/d1", d, &solas.Device{}, 0); err != nil {
		t.Fatal(err)
	}

	args := []string{"migrate", "--from", from, "--to", to, "--storage-prefix", prefix}
	if out, err := run(t, append(args, "--dry-run")...); err != nil || !strings.Contains(out, "would copy 1 devices and 0 members") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if out, err := run(t, args...); err != nil || !strings.Contains(out, "copied and verified 1 devices") {
		t.Fatalf("migrate: %v\n%s", err, out)
	}

	sealed, err := dynamo.IsStoreSealed(ctx, *src.Dynamo, prefix, migrate.ResourcePrefixes())
	if err != nil || !sealed {
		t.Errorf("source sealed = %v, %v; want true", sealed, err)
	}
	if out, err := run(t, "migrate", "verify", "--from", from, "--to", to, "--storage-prefix", prefix); err != nil {
		t.Errorf("verify: %v\n%s", err, out)
	}

	// A cluster writes to the destination; unseal must refuse.
	dst, err := migrate.Open(ctx, to, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.Devices.GuaranteedUpdate(ctx, "/solas.dev/devices/d1", &solas.Device{}, false, nil,
		func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
			c := in.(*solas.Device).DeepCopy()
			c.Status.ClaimRef = nil
			return c, nil, nil
		}, nil); err != nil {
		t.Fatal(err)
	}
	unseal := []string{"unseal", "--from", from, "--to", to, "--storage-prefix", prefix}
	if out, err := run(t, unseal...); err == nil {
		t.Errorf("unseal after a write to the destination passed:\n%s", out)
	}
}

//= spec/solas.md#12-1-seal
//= type=test
//# A destination that holds part of a copy, or nothing, does not stop an
//# unseal.

func TestUnsealAnEmptyDestination(t *testing.T) {
	endpoint := os.Getenv(ddbtest.EndpointEnv)
	if endpoint == "" {
		t.Skipf("%s is not set; run the tests with devbox run test", ddbtest.EndpointEnv)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "local")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local")
	table := ddbtest.TableName(t)
	ddbtest.DeleteTable(t, ddbtest.Client(t), table)
	from := "dynamodb://" + table + "?create-table=true&endpoint=" + url.QueryEscape(endpoint)
	to := "etcd://" + strings.TrimPrefix(etcdtest.Servers(t)[0], "http://")
	prefix := etcdtest.Prefix(t)
	src, err := migrate.Open(context.Background(), from, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := dynamo.Seal(context.Background(), *src.Dynamo, prefix, migrate.ResourcePrefixes()); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "unseal", "--from", from, "--to", to, "--storage-prefix", prefix); err != nil {
		t.Fatalf("unseal: %v\n%s", err, out)
	}
	if sealed, err := dynamo.IsStoreSealed(context.Background(), *src.Dynamo, prefix, migrate.ResourcePrefixes()); err != nil || sealed {
		t.Errorf("sealed after unseal = %v, %v", sealed, err)
	}
}

//= spec/solas.md#11-5-rollback
//= type=test
//# A server MUST refuse to start when the finalized format is above its
//# maximum format.

func TestRefuseStartAboveMaxFormat(t *testing.T) {
	ctx := context.Background()
	to := "etcd://" + strings.TrimPrefix(etcdtest.Servers(t)[0], "http://")
	s, err := migrate.Open(ctx, to, etcdtest.Prefix(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := checkFormat(ctx, s); err != nil {
		t.Fatalf("start on a new store: %v", err)
	}
	// A newer release finalized format 2; no member is Active here.
	if err := s.Finalize(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := checkFormat(ctx, s); err == nil || !strings.Contains(err.Error(), "finalized at format 2") {
		t.Errorf("start on a store at format 2: err = %v, want a refusal", err)
	}
}
