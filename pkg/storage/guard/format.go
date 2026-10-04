package guard

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/phoban01/solas/pkg/format"
)

//= spec/solas.md#11-1-format-version
//# Each object MUST record the format version of its last write in the
//# annotation `solas.dev/format`.

// FormatAnnotation records the format of the last write of an object.
const FormatAnnotation = "solas.dev/format"

// MinFormat and MaxFormat are the oldest and the newest format that this
// release reads and writes, spec 11.1.
const (
	MinFormat = format.Min
	MaxFormat = format.Max
)

// formatOf returns the format of an object.
func formatOf(obj runtime.Object) (int, error) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return 0, err
	}
	v, ok := m.GetAnnotations()[FormatAnnotation]
	//= spec/solas.md#11-1-format-version
	//# An object with no record has format 1.
	if !ok {
		return 1, nil
	}
	f, err := strconv.Atoi(v)
	if err != nil || f < 1 {
		return 0, fmt.Errorf("annotation %s=%q is not a format", FormatAnnotation, v)
	}
	return f, nil
}

//= spec/solas.md#11-3-newer-objects
//# A server MUST reject a write to an object whose format is newer than its
//# own maximum.

//= spec/solas.md#11-3-newer-objects
//# A server MUST NOT drop a field that it does not know.

// checkFormat rejects an object that this release cannot keep: one in a
// newer format, or with a record it cannot read. The store decodes the
// object before the guard sees it, so a field of a newer format is already
// gone from it; the rejection stops the server from writing it back.
func checkFormat(obj runtime.Object) error {
	f, err := formatOf(obj)
	if err != nil {
		return err
	}
	if f > MaxFormat {
		return fmt.Errorf("the object is in format %d, and this server reads formats %d to %d; upgrade it first", f, MinFormat, MaxFormat)
	}
	return nil
}

//= spec/solas.md#11-2-write-rule
//# A server MUST NOT write an object in a format newer than the finalized
//# format.

// stamp records the format of this write: the finalized format that the
// server read from the store.
func stamp(obj runtime.Object, f int) error {
	m, err := meta.Accessor(obj)
	if err != nil {
		return err
	}
	a := m.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[FormatAnnotation] = strconv.Itoa(f)
	m.SetAnnotations(a)
	return nil
}
