package sim

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// Config sets the size and the faults of a run.
type Config struct {
	Clusters int
	Devices  int
	Steps    int
	Settings Settings
	// Tick is how much real time one time step moves.
	Tick time.Duration
	// Rho bounds the rate drift of each clock, spec 7.5.
	Rho float64
	// Lag is the chance that a device list is the one of the call before,
	// see router.
	Lag float64
	// Unconditional breaks the store on purpose, see fakekube.Options.
	Unconditional bool
	// NoLeave turns off the random graceful leave, for scenarios that need
	// every member Active.
	NoLeave bool
}

// DefaultConfig is the configuration of the gate.
func DefaultConfig() Config {
	return Config{
		Clusters: 3,
		Devices:  2,
		Steps:    2000,
		Settings: Settings{Lease: 30 * time.Second, Margin: 3 * time.Second, Sweep: 10 * time.Second},
		Tick:     time.Second,
		Rho:      0.01,
		Lag:      0.3,
	}
}

// World is one run: the shared store, the clusters, the device
// gatekeepers, and what the checks need to remember.
type World struct {
	cfg      Config
	seed     uint64
	rng      *rand.Rand
	shared   client.Client
	clusters []*cluster
	gates    map[string]*gate
	// bindings maps device and token to the claim UID that held it.
	bindings map[string]map[int64]string
	trace    []string
	claimSeq int
	// sawPreempted is true once a claim went through Preempted, for the
	// witness test.
	sawPreempted bool
	step         int
	// finalized is the finalized format of the store, spec 11.1.
	finalized int32
}

// gate is a device gatekeeper, spec 6.6.
type gate struct {
	highest int64
	last    use
}

type use struct {
	token int64
	claim string
}

// NewWorld builds a run from a seed.
func NewWorld(ctx context.Context, seed uint64, cfg Config) (*World, error) {
	w := &World{
		cfg:       cfg,
		seed:      seed,
		rng:       rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		shared:    newShared(cfg.Unconditional),
		gates:     map[string]*gate{},
		finalized: 1,
		bindings:  map[string]map[int64]string{},
	}
	for i := range cfg.Devices {
		name := fmt.Sprintf("d%d", i+1)
		dev := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}
		// The first device is preemptible with a short grace period.
		if i == 0 {
			grace := int32(10)
			dev.Spec.Preemptible, dev.Spec.PreemptionGracePeriodSeconds = true, &grace
		}
		if err := w.shared.Create(ctx, dev); err != nil {
			return nil, err
		}
		w.gates[name] = &gate{}
		w.bindings[name] = map[int64]string{}
	}
	for i := range cfg.Clusters {
		// The clocks start far apart; solas does not depend on synced clocks.
		start := time.Unix(int64(1_000_000*(i+1)), 0)
		w.clusters = append(w.clusters, newCluster(fmt.Sprintf("c%d", i), w.shared, start, cfg.Settings, w.rng, cfg.Lag))
	}
	return w, nil
}

// Run runs cfg.Steps steps and checks the invariants after each one.
func (w *World) Run(ctx context.Context) error {
	for w.step = 0; w.step < w.cfg.Steps; w.step++ {
		w.Step(ctx)
		if err := w.check(ctx); err != nil {
			return w.fail(err)
		}
	}
	return nil
}

// Trace returns the steps so far.
func (w *World) Trace() []string { return w.trace }

func (w *World) fail(err error) error {
	from := max(0, len(w.trace)-40)
	if os.Getenv("SIM_FULL_TRACE") != "" {
		from = 0
	}
	msg := fmt.Sprintf("seed %d, step %d: %v\nreplay with: SIM_SEED=%d go test ./sim -run TestSimSeed -v\nlast steps:", w.seed, w.step, err, w.seed)
	for _, s := range w.trace[from:] {
		msg += "\n  " + s
	}
	return fmt.Errorf("%s", msg)
}

func (w *World) log(format string, args ...any) {
	w.trace = append(w.trace, fmt.Sprintf("%5d ", w.step)+fmt.Sprintf(format, args...))
}

// Step does one action that the seed picks.
func (w *World) Step(ctx context.Context) {
	c := w.clusters[w.rng.IntN(len(w.clusters))]
	roll := w.rng.IntN(100)
	switch {
	case roll < 20:
		w.tick()
	case c.down:
		// A cluster whose release refused to start does nothing.
	case c.paused:
		// A paused cluster does nothing, except wake up now and then.
		if roll < 25 {
			c.paused = false
			w.log("%s unpause", c.name)
		}
	case roll < 35:
		w.log("%s member tick: %s", c.name, errText(c.members.Tick(ctx)))
	case roll < 60:
		keys := c.claimKeys(ctx)
		if len(keys) > 0 {
			k := keys[w.rng.IntN(len(keys))]
			w.log("%s reconcile %s: %s", c.name, k.Name, errText(c.reconcile(ctx, k)))
		}
	case roll < 68:
		w.log("%s sweep: %s", c.name, errText(c.sweeper.Run(ctx)))
	case roll < 71:
		w.log("%s orphan sweep: %s", c.name, errText(c.orphans.Sweep(ctx)))
	case roll < 81:
		w.workload(ctx, c)
	case roll < 86:
		w.createClaim(ctx, c)
	case roll < 88:
		w.deleteClaim(ctx, c)
	case roll < 91:
		c.paused = true
		w.log("%s pause", c.name)
	case roll < 93:
		c.restart()
		w.log("%s restart", c.name)
	case roll < 94 && !w.cfg.NoLeave:
		w.leave(ctx, c)
	}
}

// tick moves real time by one tick. Each clock moves by the tick times a
// rate within 1 - rho and 1 + rho.
func (w *World) tick() {
	for _, c := range w.clusters {
		rate := 1 + w.cfg.Rho*(2*w.rng.Float64()-1)
		c.clock.SetTime(c.clock.Now().Add(time.Duration(float64(w.cfg.Tick) * rate)))
	}
	w.log("tick")
}

func (w *World) createClaim(ctx context.Context, c *cluster) {
	if len(c.claimKeys(ctx)) >= 2 {
		return
	}
	w.claimSeq++
	name := fmt.Sprintf("job%d", w.claimSeq)
	claim := &claimsv1alpha1.DeviceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
	claim.Spec.Priority = int32(w.rng.IntN(4))
	w.log("%s create claim %s: %s", c.name, name, errText(c.client.Create(ctx, claim)))
}

func (w *World) deleteClaim(ctx context.Context, c *cluster) {
	keys := c.claimKeys(ctx)
	if len(keys) == 0 {
		return
	}
	k := keys[w.rng.IntN(len(keys))]
	claim := &claimsv1alpha1.DeviceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: k.Namespace, Name: k.Name}}
	w.log("%s delete claim %s: %s", c.name, k.Name, errText(c.client.Delete(ctx, claim)))
}

// workload uses the device of a claim that the cluster sees as Bound. It
// acts on the local phase, even when the cluster's lease has ended.
func (w *World) workload(ctx context.Context, c *cluster) {
	keys := c.claimKeys(ctx)
	if len(keys) == 0 {
		return
	}
	var claim claimsv1alpha1.DeviceClaim
	if err := c.client.Get(ctx, keys[w.rng.IntN(len(keys))], &claim); err != nil ||
		(claim.Status.Phase != claimsv1alpha1.ClaimBound && claim.Status.Phase != claimsv1alpha1.ClaimPreempting) {
		return
	}
	g := w.gates[claim.Status.DeviceName]
	if g == nil {
		return
	}
	u := use{token: claim.Status.FencingToken, claim: string(claim.UID)}
	if u.token < g.highest {
		w.log("%s use %s token %d: rejected", c.name, claim.Status.DeviceName, u.token)
		return
	}
	g.highest, g.last = u.token, u
	w.log("%s use %s token %d: accepted", c.name, claim.Status.DeviceName, u.token)
}

// leave starts a graceful leave, as an operator would, spec 7.6.
func (w *World) leave(ctx context.Context, c *cluster) {
	var m solasv1alpha1.Member
	if err := w.shared.Get(ctx, client.ObjectKey{Name: c.name}, &m); err != nil {
		return
	}
	m.Status.Phase = solasv1alpha1.MemberDraining
	w.log("%s leave: %s", c.name, errText(w.shared.Status().Update(ctx, &m)))
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return "error: " + err.Error()
}
