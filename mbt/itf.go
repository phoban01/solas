// Package mbt replays Quint traces of quint/solas.qnt against the real
// solas-apiserver REST stores and checks that the stores keep the state
// that the model predicts, step by step.
package mbt

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Trace is a decoded ITF trace.
type Trace struct {
	Steps []Step
}

// Step is one state of the trace and the action that led to it.
type Step struct {
	Action string
	// Picks holds the nondeterministic choices of the action. A choice the
	// action did not make is absent.
	Picks map[string]any
	State State
}

// State is the part of the model state that the server holds.
type State struct {
	Devices       map[string]Device
	Members       map[string]Member
	Clusters      map[string]Cluster
	MemberCounter int64
}

// Device is a model device.
type Device struct {
	RV    int64
	Held  bool
	Ref   Ref
	Token int64
	// Preempt is the preemption request, when Requested is true.
	Requested bool
	Preempt   Ref
	// Offer is the transfer offer, when Offered is true.
	Offered bool
	Offer   Ref
}

// Ref is a model claimRef.
type Ref struct {
	Member string
	MUID   int64
	Claim  string
	Prio   int64
}

// Member is a model Member object.
type Member struct {
	Exists bool
	UID    int64
	RV     int64
}

// Cluster is the part of a model cluster that the driver needs.
type Cluster struct {
	UID           int64
	RenewInFlight bool
	RenewRV       int64
	Seen          map[string]Seen
}

// Seen is what a sweeper observed of a Member.
type Seen struct {
	RV int64
}

// BindMsg is a bind write in flight.
type BindMsg struct {
	Cluster, Claim, Dev string
	RV, MUID            int64
}

// Load reads and decodes a trace file.
func Load(path string) (*Trace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		States []map[string]any `json:"states"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t := &Trace{}
	for i, st := range doc.States {
		step, err := decodeStep(st)
		if err != nil {
			return nil, fmt.Errorf("%s: state %d: %w", path, i, err)
		}
		t.Steps = append(t.Steps, step)
	}
	return t, nil
}

func decodeStep(st map[string]any) (Step, error) {
	var step Step
	step.Action, _ = st["mbt::actionTaken"].(string)
	step.Picks = map[string]any{}
	if picks, ok := st["mbt::nondetPicks"].(map[string]any); ok {
		for name, v := range picks {
			if tag, val := variant(v); tag == "Some" {
				step.Picks[name] = val
			}
		}
	}
	var s map[string]any
	for k, v := range st {
		if strings.HasSuffix(k, "::s") {
			s, _ = v.(map[string]any)
		}
	}
	if s == nil {
		return step, fmt.Errorf("no state variable s")
	}
	var err error
	step.State, err = decodeState(s)
	return step, err
}

func decodeState(s map[string]any) (State, error) {
	st := State{
		Devices:       map[string]Device{},
		Members:       map[string]Member{},
		Clusters:      map[string]Cluster{},
		MemberCounter: bigint(s["memberCounter"]),
	}
	for name, v := range itfMap(s["devices"]) {
		d := v.(map[string]any)
		dev := Device{RV: bigint(d["rv"]), Token: bigint(d["token"])}
		if tag, val := variant(d["ref"]); tag == "Held" {
			r := val.(map[string]any)
			dev.Held = true
			dev.Ref = decodeRef(r)
		}
		if tag, val := variant(d["preempt"]); tag == "Held" {
			dev.Requested = true
			dev.Preempt = decodeRef(val.(map[string]any))
		}
		if tag, val := variant(d["offer"]); tag == "Held" {
			dev.Offered = true
			dev.Offer = decodeRef(val.(map[string]any))
		}
		st.Devices[name] = dev
	}
	for name, v := range itfMap(s["members"]) {
		m := v.(map[string]any)
		st.Members[name] = Member{Exists: m["exists"].(bool), UID: bigint(m["uid"]), RV: bigint(m["rv"])}
	}
	for name, v := range itfMap(s["clusters"]) {
		c := v.(map[string]any)
		cl := Cluster{
			UID:           bigint(c["uid"]),
			RenewInFlight: c["renewInFlight"].(bool),
			RenewRV:       bigint(c["renewRv"]),
			Seen:          map[string]Seen{},
		}
		for m, sv := range itfMap(c["seen"]) {
			cl.Seen[m] = Seen{RV: bigint(sv.(map[string]any)["rv"])}
		}
		st.Clusters[name] = cl
	}
	return st, nil
}

// Pick returns the choice name as a string.
func (s Step) Pick(name string) string {
	v, _ := s.Picks[name].(string)
	return v
}

// Msg returns the bind message that landSome picked.
func (s Step) Msg() (BindMsg, bool) {
	m, ok := s.Picks["msg"].(map[string]any)
	if !ok {
		return BindMsg{}, false
	}
	return BindMsg{
		Cluster: m["cluster"].(string),
		Claim:   m["claim"].(string),
		Dev:     m["dev"].(string),
		RV:      bigint(m["rv"]),
		MUID:    bigint(m["muid"]),
	}, true
}

// bigint decodes {"#bigint": "n"}.
func bigint(v any) int64 {
	m, _ := v.(map[string]any)
	s, _ := m["#bigint"].(string)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// itfMap decodes {"#map": [[k, v], ...]} with string keys.
func itfMap(v any) map[string]any {
	out := map[string]any{}
	m, _ := v.(map[string]any)
	pairs, _ := m["#map"].([]any)
	for _, p := range pairs {
		kv, _ := p.([]any)
		if len(kv) == 2 {
			if k, ok := kv[0].(string); ok {
				out[k] = kv[1]
			}
		}
	}
	return out
}

// variant decodes {"tag": t, "value": v}.
func variant(v any) (string, any) {
	m, _ := v.(map[string]any)
	tag, _ := m["tag"].(string)
	return tag, m["value"]
}

func decodeRef(r map[string]any) Ref {
	return Ref{Member: r["member"].(string), MUID: bigint(r["muid"]), Claim: r["claim"].(string), Prio: bigint(r["prio"])}
}
