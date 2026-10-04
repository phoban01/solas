package migrate

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/meta"
)

// Report counts the objects of a copy.
type Report struct {
	Devices, Members int
}

//= spec/solas.md#12-2-copy
//# The tool MUST NOT copy into a store that holds a `Device` or a `Member`.

// Empty returns an error when the store holds a Device or a Member.
func (s *Store) Empty(ctx context.Context) error {
	for _, r := range resources {
		objs, err := s.list(ctx, r)
		if err != nil {
			return err
		}
		if len(objs) > 0 {
			return fmt.Errorf("the destination already holds %d %s; it must hold no Device and no Member", len(objs), r.gr.Resource)
		}
	}
	return nil
}

// Copy copies every Device and Member from src to dst. The caller seals
// src first, or checks that it is quiet. A dry run writes nothing and
// prints what it would copy.
func Copy(ctx context.Context, src, dst *Store, dryRun bool, out io.Writer) (Report, error) {
	var rep Report
	if err := dst.Empty(ctx); err != nil {
		return rep, err
	}
	for _, r := range resources {
		objs, err := src.list(ctx, r)
		if err != nil {
			return rep, err
		}
		for _, o := range objs {
			m, err := meta.Accessor(o)
			if err != nil {
				return rep, err
			}
			fmt.Fprintf(out, "copy %s/%s uid %s\n", r.gr.Resource, m.GetName(), m.GetUID())
			switch r.gr {
			case devices.gr:
				rep.Devices++
			case members.gr:
				rep.Members++
			}
			if dryRun {
				continue
			}
			//= spec/solas.md#12-2-copy
			//# The copy MUST keep the name, UID, labels, annotations, spec, status, and
			//# creation time of each object.

			//= spec/solas.md#12-2-copy
			//# The copy MUST NOT keep the resource version.
			//
			// A create at the storage level keeps every field it gets, and a
			// create needs an empty resource version. The destination gives
			// the new one.
			m.SetResourceVersion("")
			key := r.prefix() + "/" + m.GetName()
			if err := dst.storageFor(r).Create(ctx, key, o, r.newFunc(), 0); err != nil {
				return rep, fmt.Errorf("copy %s: %w", key, err)
			}
		}
	}
	return rep, nil
}
