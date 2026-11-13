package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/phoban01/solas/pkg/migrate"
	storageurl "github.com/phoban01/solas/pkg/storage"
)

// runRaiseEtcd raises the revision of an etcd destination above the
// highest resource version of the source, the step that spec 12.2 asks
// of an operator before a move to etcd. A real operator restores etcd
// with etcdutl snapshot restore --bump-revision; the demo etcd is empty,
// so writing one scratch key until its revision passes the source, with
// a margin for the writes before the seal, is
// enough.
func runRaiseEtcd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("raise-etcd", flag.ContinueOnError)
	from := fs.String("from", "", "the storage URL of the source")
	to := fs.String("to", "", "the storage URL of the etcd destination")
	prefix := fs.String("storage-prefix", "/registry", "the storage prefix of the API servers")
	margin := fs.Uint64("margin", 1000, "how far above the source to raise it: the source takes writes until the tool seals it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	src, err := migrate.Open(ctx, *from, *prefix)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	top, err := src.HighestVersion(ctx)
	src.Close()
	if err != nil {
		return err
	}
	sc, err := storageurl.Parse(*to)
	if err != nil || sc.Kind != storageurl.Etcd {
		return fmt.Errorf("--to: want an etcd:// URL: %v", err)
	}
	c, err := clientv3.New(clientv3.Config{Endpoints: sc.Endpoints, DialTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer c.Close()
	for {
		resp, err := c.Put(ctx, "/solas-demo/raise", "x")
		if err != nil {
			return err
		}
		if rev := uint64(resp.Header.Revision); rev > top+*margin {
			fmt.Printf("raised the etcd revision to %d, above the highest version %d of the source\n", rev, top)
			return nil
		}
	}
}
