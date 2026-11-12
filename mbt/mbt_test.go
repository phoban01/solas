package mbt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apiserver"
)

// newDriver returns a driver on an empty store. MBT_STORE picks the store:
// dynamodb, the default, etcd, or postgres.
func newDriver(t *testing.T) *Driver {
	t.Helper()
	var g apiserver.RESTOptionsGetter
	switch s := os.Getenv("MBT_STORE"); s {
	case "", "dynamodb":
		g = teststore.Dynamo(t)
	case "etcd":
		g = teststore.Etcd(t)
	case "postgres":
		g = teststore.Postgres(t)
	default:
		t.Fatalf("MBT_STORE=%q is not dynamodb, etcd, or postgres", s)
	}
	d, stop, err := NewDriver(g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return d
}

// traces returns the trace files: MBT_TRACES, a directory from
// scripts/mbt.sh, or the checked-in test data.
func traces(t *testing.T) []string {
	t.Helper()
	dir := os.Getenv("MBT_TRACES")
	if dir == "" {
		dir = "testdata"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.itf.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no traces in %s: %v", dir, err)
	}
	return files
}

// TestMBT replays each trace against the real REST stores.
func TestMBT(t *testing.T) {
	for _, f := range traces(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			tr, err := Load(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := newDriver(t).Replay(tr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestMBTCatchesADivergence changes the expected state of a trace in two
// ways. The replay must report each one, which shows that it compares.
func TestMBTCatchesADivergence(t *testing.T) {
	load := func() *Trace {
		tr, err := Load("testdata/orphan.itf.json")
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	firstBind := func(tr *Trace) int {
		for i, s := range tr.Steps {
			if msg, ok := s.Msg(); ok && s.Action == "landSome" &&
				s.State.Devices[msg.Dev].RV != tr.Steps[i-1].State.Devices[msg.Dev].RV {
				return i
			}
		}
		t.Fatal("the trace has no applied bind")
		return 0
	}

	t.Run("wrong fencing token", func(t *testing.T) {
		tr := load()
		i := firstBind(tr)
		msg, _ := tr.Steps[i].Msg()
		dev := tr.Steps[i].State.Devices[msg.Dev]
		dev.Token += 5
		tr.Steps[i].State.Devices[msg.Dev] = dev
		if err := newDriver(t).Replay(tr); err == nil {
			t.Fatal("the replay did not see a wrong fencing token")
		} else {
			t.Logf("caught: %v", err)
		}
	})

	t.Run("wrong write result", func(t *testing.T) {
		tr := load()
		i := firstBind(tr)
		// Say that the model rejected the bind: keep the old device state.
		msg, _ := tr.Steps[i].Msg()
		tr.Steps[i].State.Devices[msg.Dev] = tr.Steps[i-1].State.Devices[msg.Dev]
		if err := newDriver(t).Replay(tr); err == nil {
			t.Fatal("the replay did not see a write that the model rejected")
		} else {
			t.Logf("caught: %v", err)
		}
	})

	t.Run("missing preemption request", func(t *testing.T) {
		tr, err := Load("testdata/preempt.itf.json")
		if err != nil {
			t.Fatal(err)
		}
		i := 0
		for j, s := range tr.Steps {
			if s.Action == "requestPreemption" && s.State.Devices[s.Pick("d")].Requested {
				i = j
				break
			}
		}
		if i == 0 {
			t.Fatal("the trace has no applied preemption request")
		}
		// Say that the model did not record the request.
		dev := tr.Steps[i].Pick("d")
		d := tr.Steps[i].State.Devices[dev]
		d.Requested = false
		tr.Steps[i].State.Devices[dev] = d
		if err := newDriver(t).Replay(tr); err == nil {
			t.Fatal("the replay did not see a missing preemption request")
		} else {
			t.Logf("caught: %v", err)
		}
	})
}
