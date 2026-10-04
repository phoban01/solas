// Package format holds the range of object formats that this release
// reads and writes, spec 11.1. The storage guard checks it, and the member
// controller reports it in the Member.
package format

import (
	"fmt"
	"sort"
	"sync/atomic"
)

// Min and Max are the oldest and the newest format that this release
// reads and writes.
const (
	Min = 1
	Max = 1
)

// finalized is the finalized format of the store, which this server
// writes in. The server reads it from the store at start and on a slow
// poll, spec 11.2.
var finalized atomic.Int32

func init() { finalized.Store(1) }

// SetFinalized records the finalized format that the server read.
func SetFinalized(f int32) { finalized.Store(f) }

// Write returns the format of each write: the finalized format, spec 11.2.
func Write() int { return int(finalized.Load()) }

// Member is what finalization needs to know about one member.
type Member struct {
	Name   string
	Active bool
	// Max is the highest format the member supports. Zero reads as 1.
	Max int32
}

// Behind returns, in order, the Active members that do not support format
// n, spec 11.4. Finalization to n may go ahead only when it is empty.
func Behind(members []Member, n int32) []string {
	var out []string
	for _, m := range members {
		if m.Active && max(m.Max, 1) < n {
			out = append(out, fmt.Sprintf("%s (highest format %d)", m.Name, max(m.Max, 1)))
		}
	}
	sort.Strings(out)
	return out
}

// epoch is the epoch of the store, spec 13.1. The server reads it with
// the finalized format.
var epoch atomic.Int64

// SetEpoch records the epoch that the server read.
func SetEpoch(e int64) { epoch.Store(e) }

// Epoch returns the epoch of the store.
func Epoch() int64 { return epoch.Load() }

//= spec/solas.md#13-2-fencing-tokens
//# When a status update sets `claimRef` on a free device whose token is
//# below `epoch * 2^32`, the server MUST set the token to `epoch * 2^32 + 1`.

// NextToken returns the token of a bind of a free device with token old,
// in epoch e. The epoch sits in the high 32 bits, so the first bind of a
// new epoch starts above every token of the epochs before, spec 13.2.
func NextToken(old, e int64) int64 {
	if base := e << 32; old < base {
		return base + 1
	}
	return old + 1
}
