//go:build e2e && soak

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/audit"
	"github.com/phoban01/solas/pkg/migrate"
)

// soakDevices is the device pool of the soak, from devices.yaml.
var soakDevices = []string{"gpu-a100-1", "gpu-a100-2", "gpu-h100-1", "gpu-t4-1", "gpu-t4-2", "fpga-1", "nic-1", "nic-2"}

// manifestOf names the manifest that creates each claim of the soak, so
// the loop can create a Lost claim again.
var manifestOf = map[string]string{"train": "claims-a.yaml", "infer": "claims-a.yaml", "net": "claims-a.yaml",
	"render": "claims-b.yaml", "encode": "claims-b.yaml"}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// soak is the state of one soak run.
type soak struct {
	t         *testing.T
	clients   map[string]client.Client
	store     string
	container string
	deviceURL string
	rng       *rand.Rand
	// counts of what the run did and saw
	faults      map[string]int
	churned     int
	lostDeleted int
	uidChanges  int
	lastUID     map[string]string
	skipped     int
	start       time.Time
	// loadRow is the row that solas-bench printed, when SOAK_LOAD ran it.
	loadRow string
}

// startLoad starts solas-bench against the same store for the length of
// the faults, when SOAK_LOAD names a number of simulated members and the
// soak script set SOAK_BENCH. Each bench member joins with a Member and
// renews it, so the clusters' sweepers treat its devices as held, and the
// bench binds only its own devices. The replay then audits the bench's
// events with the clusters'. The returned function waits for the bench
// and returns its row.
func (s *soak) startLoad(ctx context.Context, duration time.Duration) func() string {
	members := envInt("SOAK_LOAD", 0)
	bench := os.Getenv("SOAK_BENCH")
	if members == 0 || bench == "" {
		return func() string { return "" }
	}
	url := strings.TrimSpace(run(s.t, "", root+"/demo/k3d/mesh.sh", "url", s.store))
	cmd := exec.CommandContext(ctx, bench, "--storage-url", url, "--storage-prefix", "/registry",
		"--members", fmt.Sprint(members), "--claims", "4", "--duration", duration.String(),
		"--member-lease", "10s", "--header=false")
	cmd.Dir = root
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Start(); err != nil {
		s.t.Fatalf("start solas-bench: %v", err)
	}
	s.logf("solas-bench runs %d members against %s for %s", members, s.store, duration)
	return func() string {
		if err := cmd.Wait(); err != nil {
			s.logf("solas-bench: %v\n%s", err, errs.String())
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		return lines[len(lines)-1]
	}
}

func (s *soak) logf(format string, args ...any) {
	s.t.Helper()
	s.t.Logf("%6.0fs %s", time.Since(s.start).Seconds(), fmt.Sprintf(format, args...))
}

// kubectl runs kubectl against one cluster with a short request timeout,
// and returns the output and the error. A paused cluster does not
// answer, so a failure is not an error of the soak.
func (s *soak) kubectl(cluster string, args ...string) (string, error) {
	return runQuiet("kubectl", append([]string{"--kubeconfig", kubeconfig(cluster), "--request-timeout=10s"}, args...)...)
}

func (s *soak) sleep(d time.Duration) {
	time.Sleep(d)
}

func (s *soak) between(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(s.rng.Int64N(int64(hi-lo)))
}

// TestSoak runs faults against the mesh for SOAK_DURATION, then replays
// the Device history of the store and reads the gatekeeper log. It
// fails on any violation of the rules of spec 5.3, 6.6, and 14.
func TestSoak(t *testing.T) {
	duration := envDuration("SOAK_DURATION", 30*time.Minute)
	seed := envInt("SOAK_SEED", 1)
	store := os.Getenv("SOLAS_STORE")
	if store == "" {
		store = "dynamodb"
	}
	s := &soak{t: t, store: store, rng: rand.New(rand.NewPCG(uint64(seed), uint64(seed))),
		faults: map[string]int{}, lastUID: map[string]string{}, start: time.Now()}
	s.clients = map[string]client.Client{"a": clientFor(t, "a"), "b": clientFor(t, "b")}
	s.container = map[string]string{"dynamodb": "solas-ddb", "etcd": "solas-etcd"}[store]
	ctx := context.Background()
	t.Cleanup(func() {
		for _, c := range []string{s.container, "k3d-e2e-a-server-0", "k3d-e2e-b-server-0"} {
			_, _ = runQuiet("docker", "unpause", c)
		}
	})

	s.setup(ctx)
	s.logf("soak: %s for %s, seed %d", store, duration, seed)
	load := s.startLoad(ctx, duration)
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		s.housekeeping(ctx)
		s.fault()
		s.sleep(s.between(5*time.Second, 10*time.Second))
	}
	s.logf("faults done; waiting for the mesh to settle")
	for _, c := range []string{s.container, "k3d-e2e-a-server-0", "k3d-e2e-b-server-0"} {
		_, _ = runQuiet("docker", "unpause", c)
	}
	s.loadRow = load()
	eventually(t, 240*time.Second, "no claim Suspended and no claim Pending that a free device could serve", func(ctx context.Context) bool {
		s.housekeeping(ctx)
		for _, c := range s.clients {
			var list v1alpha1.DeviceClaimList
			if err := c.List(ctx, &list); err != nil {
				return false
			}
			for _, cl := range list.Items {
				if cl.Status.Phase == v1alpha1.ClaimSuspended {
					return false
				}
			}
		}
		return true
	})
	s.verify(ctx)
}

// setup creates the pool, the gatekeeper, and the claims of both
// clusters, and waits for the first binds.
func (s *soak) setup(ctx context.Context) {
	t := s.t
	apply(t, "a", "devices.yaml")
	for _, d := range soakDevices {
		setReady(t, d, "True", "Healthy")
	}
	run(t, "", "docker", "rm", "-f", "solas-device-gpu-h100-1")
	run(t, "", "docker", "run", "-d", "--name", "solas-device-gpu-h100-1", "--network", "solas-mesh", "solas-demo:dev", "device")
	ip := strings.TrimSpace(run(t, "", "docker", "inspect", "-f",
		`{{(index .NetworkSettings.Networks "solas-mesh").IPAddress}}`, "solas-device-gpu-h100-1"))
	s.deviceURL = "http://" + ip + ":9000"
	eventually(t, 30*time.Second, "gatekeeper up", func(context.Context) bool {
		resp, err := http.Get(s.deviceURL + "/log")
		if err == nil {
			_ = resp.Body.Close()
		}
		return err == nil
	})
	apply(t, "a", "claims-a.yaml")
	apply(t, "b", "claims-b.yaml")
	for _, name := range []string{"a", "b"} {
		s.applyJob(name)
	}
	eventually(t, 120*time.Second, "at least five claims Bound", func(ctx context.Context) bool {
		n := 0
		for _, c := range s.clients {
			n += len(boundClaims(ctx, t, c))
		}
		return n >= 5
	})
	for name, c := range s.clients {
		s.lastUID[name] = memberUID(ctx, c, name)
	}
}

// housekeeping counts member UID changes, which are takeovers, and
// deletes Lost claims and creates them again, as an operator would, so
// the pool stays contended.
func (s *soak) housekeeping(ctx context.Context) {
	for name, c := range s.clients {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		uid := memberUID(cctx, c, name)
		if uid != "" && s.lastUID[name] != "" && uid != s.lastUID[name] {
			s.uidChanges++
			s.logf("%s joined again with a new member UID: its old claims are Lost", name)
		}
		if uid != "" {
			s.lastUID[name] = uid
		}
		var list v1alpha1.DeviceClaimList
		err := c.List(cctx, &list)
		cancel()
		if err != nil {
			continue
		}
		for _, cl := range list.Items {
			if cl.Status.Phase != v1alpha1.ClaimLost {
				continue
			}
			s.lostDeleted++
			s.logf("%s/%s is Lost: delete it and create it again", name, cl.Name)
			if _, err := s.kubectl(name, "-n", cl.Namespace, "delete", "deviceclaim", cl.Name, "--wait=false"); err != nil {
				continue
			}
			switch {
			case cl.Name == "job":
				s.applyJob(name)
			case manifestOf[cl.Name] != "":
				_, _ = s.kubectl(name, "apply", "-f", root+"/demo/k3d/manifests/"+manifestOf[cl.Name])
			}
		}
	}
}

// applyJob applies claim.yaml in a cluster, with the placeholders filled:
// the job claim for the H100 and the workload that uses it through the
// gatekeeper.
func (s *soak) applyJob(cluster string) {
	manifest, err := os.ReadFile(root + "/demo/k3d/manifests/claim.yaml")
	if err != nil {
		s.t.Fatal(err)
	}
	m := strings.NewReplacer("CLUSTER_ID", cluster, "DEVICE_URL", s.deviceURL).Replace(string(manifest))
	out, err := execCommandStdin(m, "kubectl", "--kubeconfig", kubeconfig(cluster), "--request-timeout=10s", "apply", "-f", "-")
	if err != nil {
		s.logf("apply claim.yaml in %s: %v\n%s", cluster, err, out)
	}
}

// fault runs one fault from the schedule.
func (s *soak) fault() {
	cluster := []string{"a", "b"}[s.rng.IntN(2)]
	node := "k3d-e2e-" + cluster + "-server-0"
	switch roll := s.rng.IntN(100); {
	case roll < 20:
		d := s.between(15*time.Second, 35*time.Second)
		s.faults["store pause"]++
		s.logf("pause the store %s for %s", s.container, d.Round(time.Second))
		run(s.t, "", "docker", "pause", s.container)
		s.sleep(d)
		run(s.t, "", "docker", "unpause", s.container)
	case roll < 45:
		d := s.between(12*time.Second, 30*time.Second)
		s.faults["node pause"]++
		s.logf("pause the node of %s for %s", cluster, d.Round(time.Second))
		run(s.t, "", "docker", "pause", node)
		s.sleep(d)
		run(s.t, "", "docker", "unpause", node)
	case roll < 65:
		s.faults["pod delete"]++
		s.logf("delete the solas Pod of %s", cluster)
		if _, err := s.kubectl(cluster, "-n", "solas-system", "delete", "pod", "-l", "app=solas", "--wait=false"); err != nil {
			s.skipped++
		}
	case roll < 90:
		s.churned++
		name := fmt.Sprintf("churn-%d", s.churned)
		d := s.between(5*time.Second, 15*time.Second)
		s.faults["claim churn"]++
		s.logf("create %s in %s for any free device, delete it after %s", name, cluster, d.Round(time.Second))
		claim := fmt.Sprintf("apiVersion: claims.solas.dev/v1alpha1\nkind: DeviceClaim\nmetadata:\n  name: %s\n  namespace: work\nspec:\n  selector:\n    cel: \"!has(device.spec.attributes.bench)\"\n", name)
		if _, err := execCommandStdin(claim, "kubectl", "--kubeconfig", kubeconfig(cluster), "--request-timeout=10s", "apply", "-f", "-"); err != nil {
			s.skipped++
			return
		}
		s.sleep(d)
		if _, err := s.kubectl(cluster, "-n", "work", "delete", "deviceclaim", name, "--wait=false"); err != nil {
			s.skipped++
		}
	default:
		d := s.between(10*time.Second, 20*time.Second)
		s.faults["quiet"]++
		s.logf("quiet for %s", d.Round(time.Second))
		s.sleep(d)
	}
}

// verify replays the Device history of the store and reads the
// gatekeeper log, then prints the summary of the run.
func (s *soak) verify(ctx context.Context) {
	t := s.t
	url := strings.TrimSpace(run(t, "", root+"/demo/k3d/mesh.sh", "url", s.store))
	st, err := migrate.Open(ctx, url, "/registry")
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()
	replay := audit.New()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := replay.ReadStore(rctx, st.Devices, "/solas.dev/devices"); err != nil {
		t.Fatalf("replay the device history: %v", err)
	}
	for _, v := range replay.Violations {
		t.Errorf("history: %s", v)
	}

	log := deviceLog(s.deviceURL)
	accepted, rejected, lateRejects := 0, 0, 0
	var highest int64
	for _, u := range log {
		if u.Accepted {
			accepted++
			//= spec/solas.md#6-6-device-use
			//= type=test
			//# The device MUST reject a use with a token lower than the highest token
			//# that it has accepted.
			if u.Token < highest {
				t.Errorf("gatekeeper accepted token %d after token %d", u.Token, highest)
			}
			highest = max(highest, u.Token)
		} else {
			rejected++
			if u.Token < highest {
				lateRejects++
			} else {
				t.Errorf("gatekeeper rejected token %d although the highest accepted was %d", u.Token, highest)
			}
		}
	}

	//= spec/solas.md#17-2-check
	//= type=test
	//# It MUST forward the request only when the `claimRef` of the device names
	//# the member, namespace, and name of the header, and the `fencingToken`
	//# of the device equals the token of the header.
	//
	// The actuator behind the gatekeeper accepted every call that reached
	// it. Each one must come from the claim that held the device with that
	// token in the replayed history, and the tokens of a machine must never
	// go down.
	actuatorURL := strings.TrimSpace(run(t, "", root+"/demo/k3d/mesh.sh", "actuator"))
	machines := s.machines(ctx)
	calls := actuatorLog(actuatorURL)
	highestOf := map[string]int64{}
	for _, c := range calls {
		device, ok := machines[c.Machine]
		if !ok {
			t.Errorf("actuator: a call for machine %s, which no device names", c.Machine)
			continue
		}
		token, err := strconv.ParseInt(c.Token, 10, 64)
		if err != nil {
			t.Errorf("actuator: a call with token %q reached it", c.Token)
			continue
		}
		if holder, ok := replay.Holder(device, token); !ok || holder != c.Claim {
			t.Errorf("actuator: %s %s with token %d by %s, but the history gives that token to %q", c.Operation, c.Machine, token, c.Claim, holder)
		}
		if token < highestOf[c.Machine] {
			t.Errorf("actuator: %s %s with token %d after token %d", c.Operation, c.Machine, token, highestOf[c.Machine])
		}
		highestOf[c.Machine] = max(highestOf[c.Machine], token)
	}

	kinds := make([]string, 0, len(s.faults))
	for k := range s.faults {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var faults []string
	for _, k := range kinds {
		faults = append(faults, fmt.Sprintf("%s %d", k, s.faults[k]))
	}
	t.Logf("soak summary (%s, %s):", s.store, time.Since(s.start).Round(time.Second))
	t.Logf("  faults: %s; %d fault steps skipped because a paused cluster did not answer", strings.Join(faults, ", "), s.skipped)
	t.Logf("  takeovers: %d member UID changes, %d Lost claims deleted and created again", s.uidChanges, s.lostDeleted)
	t.Logf("  history: %d events, %d binds, %d releases, %d binds against an offer, %d recoveries, %d deletes, %d violations",
		replay.Events, replay.Binds, replay.Releases, replay.OfferBinds, replay.Recoveries, replay.Deletes, len(replay.Violations))
	t.Logf("  actuator: %d calls accepted through the gatekeeper, each from the holder of the token at the time", len(calls))
	if s.loadRow != "" {
		t.Logf("  load: solas-bench row (store members claims devices binds/s conflict%% bind p50 p99 list p50 p99 sync lag p50 p99 reads errors):\n    %s", s.loadRow)
	}
	t.Logf("  gatekeeper: %d uses accepted, %d rejected, all %d rejections for a token below the highest accepted (%d)",
		accepted, rejected, lateRejects, highest)
}

// machines maps each machine ID at the actuator to the device that names
// it in its machineID attribute.
func (s *soak) machines(ctx context.Context) map[string]string {
	var list solasv1alpha1.DeviceList
	if err := s.clients["a"].List(ctx, &list); err != nil {
		s.t.Fatalf("list devices: %v", err)
	}
	out := map[string]string{}
	for _, d := range list.Items {
		if id, ok := d.Spec.Attributes["machineID"]; ok {
			out[id] = d.Name
		}
	}
	return out
}
