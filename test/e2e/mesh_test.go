//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

// eventually polls cond once a second until it holds or the timeout ends.
func eventually(t *testing.T, timeout time.Duration, what string, cond func(ctx context.Context) bool) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), time.Second, timeout, true,
		func(ctx context.Context) (bool, error) { return cond(ctx), nil })
	if err != nil {
		dumpState(t)
		t.Fatalf("%s: not true after %v", what, timeout)
	}
}

// dumpState prints the claims, the devices, and the last solas logs of each
// cluster, so a failed wait shows why. The clusters are gone after the run.
func dumpState(t *testing.T) {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		for _, args := range [][]string{
			{"get", "deviceclaims", "-A", "-o", "wide"},
			{"get", "devices", "-o", "wide"},
			{"-n", "solas-system", "logs", "deploy/solas", "--tail=60"},
		} {
			out, _ := exec.Command("kubectl", append([]string{"--kubeconfig", kubeconfig(name)}, args...)...).CombinedOutput()
			t.Logf("[%s] kubectl %s\n%s", name, strings.Join(args, " "), out)
		}
	}
}

func getClaim(ctx context.Context, c client.Client, name string) *claimsv1alpha1.DeviceClaim {
	var claim claimsv1alpha1.DeviceClaim
	if err := c.Get(ctx, client.ObjectKey{Namespace: "work", Name: name}, &claim); err != nil {
		return &claimsv1alpha1.DeviceClaim{}
	}
	return &claim
}

func phase(ctx context.Context, c client.Client, name string) claimsv1alpha1.ClaimPhase {
	return getClaim(ctx, c, name).Status.Phase
}

// waitPhase waits until the claim in cluster c has phase p.
func waitPhase(t *testing.T, c client.Client, name string, p claimsv1alpha1.ClaimPhase, timeout time.Duration) {
	t.Helper()
	eventually(t, timeout, fmt.Sprintf("claim %s %s", name, p), func(ctx context.Context) bool {
		return phase(ctx, c, name) == p
	})
}

func getDevice(ctx context.Context, c client.Client, name string) *solasv1alpha1.Device {
	var d solasv1alpha1.Device
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
		return &solasv1alpha1.Device{}
	}
	return &d
}

// holder returns member/claim/token of the holder of a device.
func holder(ctx context.Context, c client.Client, name string) string {
	d := getDevice(ctx, c, name)
	if d.Status.ClaimRef == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%d", d.Status.ClaimRef.Member, d.Status.ClaimRef.Name, d.Status.FencingToken)
}

type use struct {
	Claim    string `json:"claim"`
	Token    int64  `json:"token"`
	Accepted bool   `json:"accepted"`
}

func deviceLog(url string) []use {
	resp, err := http.Get(url + "/log")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var log []use
	_ = json.NewDecoder(resp.Body).Decode(&log)
	return log
}

func seen(url, cluster string, token int64, accepted bool) bool {
	for _, u := range deviceLog(url) {
		if u.Claim == cluster+"/work/job" && u.Token == token && u.Accepted == accepted {
			return true
		}
	}
	return false
}

func memberUID(ctx context.Context, c client.Client, name string) string {
	var m solasv1alpha1.Member
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &m); err != nil {
		return ""
	}
	return string(m.UID)
}

// setReady sets the Ready condition of a device, as the party that runs
// the device would.
func setReady(t *testing.T, device, status, reason string) {
	t.Helper()
	patch := fmt.Sprintf(`{"status":{"conditions":[{"type":"Ready","status":%q,"reason":%q,`+
		`"message":"set by the test","lastTransitionTime":%q}]}}`, status, reason, time.Now().UTC().Format(time.RFC3339))
	run(t, "", "kubectl", "--kubeconfig", kubeconfig("a"), "patch", "device", device,
		"--subresource=status", "--type=merge", "-p", patch)
}

func apply(t *testing.T, cluster, manifest string) {
	t.Helper()
	run(t, "", "kubectl", "--kubeconfig", kubeconfig(cluster), "apply", "-f", root+"/demo/k3d/manifests/"+manifest)
}

// TestMesh runs the demo scenario: the device pool, selection, preemption,
// a race, fencing, and rejoin. Spec sections 5 to 10.
func TestMesh(t *testing.T) {
	var winner, loser, deviceURL, oldUID, victim string
	clients := map[string]client.Client{}

	f := features.New("two clusters share one device pool").
		Setup(func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			clients["a"], clients["b"] = clientFor(t, "a"), clientFor(t, "b")
			return ctx
		}).
		Assess("devices created in a appear in b", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			apply(t, "a", "devices.yaml")
			for _, d := range []string{"gpu-a100-1", "gpu-a100-2", "gpu-h100-1", "gpu-t4-1", "fpga-1", "nic-1", "nic-2"} {
				setReady(t, d, "True", "Healthy")
			}
			setReady(t, "gpu-t4-2", "False", "Overheating")
			eventually(t, 30*time.Second, "gpu-t4-2 not Ready in b", func(ctx context.Context) bool {
				c := meta.FindStatusCondition(getDevice(ctx, clients["b"], "gpu-t4-2").Status.Conditions, "Ready")
				return c != nil && c.Status == "False"
			})
			return ctx
		}).
		Assess("claims select devices with labels and CEL", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			apply(t, "a", "claims-a.yaml")
			for _, c := range []string{"train", "infer", "net"} {
				waitPhase(t, clients["a"], c, claimsv1alpha1.ClaimBound, 60*time.Second)
			}
			apply(t, "b", "claims-b.yaml")
			for _, c := range []string{"render", "encode"} {
				waitPhase(t, clients["b"], c, claimsv1alpha1.ClaimBound, 60*time.Second)
			}
			want := map[string]string{"infer": "gpu-t4-1", "net": "nic-1"}
			for c, d := range want {
				if got := getClaim(ctx, clients["a"], c).Status.DeviceName; got != d {
					t.Errorf("a/%s holds %q, want %s", c, got, d)
				}
			}
			if got := getClaim(ctx, clients["b"], "encode").Status.DeviceName; got != "fpga-1" {
				t.Errorf("b/encode holds %q, want fpga-1", got)
			}
			for _, d := range []string{"gpu-t4-2", "nic-2", "gpu-h100-1"} {
				if h := holder(ctx, clients["a"], d); h != "" {
					t.Errorf("%s holder = %s, want free", d, h)
				}
			}
			if c := getClaim(ctx, clients["a"], "train"); c.Status.LeaseExpiresAt == nil {
				t.Errorf("a/train shows no lease end")
			}
			return ctx
		}).
		Assess("a/net hands nic-1 to a pre-bound claim in b", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			apply(t, "b", "transfer-b.yaml")
			waitPhase(t, clients["b"], "net", claimsv1alpha1.ClaimPending, 30*time.Second)
			if h := holder(ctx, clients["a"], "nic-1"); h != "a/net/1" {
				t.Fatalf("nic-1 holder before the transfer = %q, want a/net/1", h)
			}
			// Poll nic-1 during the transfer: it must never be free.
			stop := make(chan struct{})
			sawFree, sawOffer := make(chan bool, 1), make(chan bool, 1)
			go func() {
				free, offer := false, false
				for {
					select {
					case <-stop:
						sawFree <- free
						sawOffer <- offer
						return
					case <-time.After(100 * time.Millisecond):
					}
					d := getDevice(ctx, clients["a"], "nic-1")
					if d.Name != "" && d.Status.ClaimRef == nil {
						free = true
					}
					if d.Status.Offer != nil && d.Status.Offer.Name == "net" && d.Status.Offer.Member == "b" {
						offer = true
					}
				}
			}()
			uid := getClaim(ctx, clients["b"], "net").UID
			run(t, "", "kubectl", "--kubeconfig", kubeconfig("a"), "-n", "work", "annotate", "deviceclaim", "net",
				"solas.dev/transfer-to=b/work/net/"+string(uid))
			waitPhase(t, clients["b"], "net", claimsv1alpha1.ClaimBound, 60*time.Second)
			waitPhase(t, clients["a"], "net", claimsv1alpha1.ClaimPending, 30*time.Second)
			close(stop)
			if <-sawFree {
				t.Error("nic-1 was free during the transfer")
			}
			if !<-sawOffer {
				t.Log("the poll did not see the offer; b bound it within 100ms")
			}
			d := getDevice(ctx, clients["a"], "nic-1")
			if h := holder(ctx, clients["a"], "nic-1"); h != "b/net/2" {
				t.Errorf("nic-1 holder after the transfer = %q, want b/net/2", h)
			}
			if d.Status.Offer != nil || d.Status.LastRelease != nil {
				t.Errorf("nic-1 after the transfer: offer %+v, lastRelease %+v; want none", d.Status.Offer, d.Status.LastRelease)
			}
			if got := getClaim(ctx, clients["b"], "net").Status.FencingToken; got != 2 {
				t.Errorf("b/net token = %d, want 2", got)
			}
			return ctx
		}).
		Assess("a claim of higher priority preempts across clusters", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			victim = getClaim(ctx, clients["a"], "train").Status.DeviceName
			apply(t, "b", "urgent.yaml")
			waitPhase(t, clients["a"], "train", claimsv1alpha1.ClaimPreempting, 30*time.Second)
			if p := getDevice(ctx, clients["a"], victim).Status.Preemption; p == nil || p.Claim.Name != "urgent" || p.Claim.Priority != 5 {
				t.Fatalf("%s preemption = %+v, want urgent at priority 5", victim, p)
			}
			waitPhase(t, clients["b"], "urgent", claimsv1alpha1.ClaimBound, 60*time.Second)
			if got := getClaim(ctx, clients["b"], "urgent").Status.DeviceName; got != victim {
				t.Fatalf("b/urgent holds %q, want %s", got, victim)
			}
			waitPhase(t, clients["a"], "train", claimsv1alpha1.ClaimPending, 30*time.Second)
			train := getClaim(ctx, clients["a"], "train")
			if !meta.IsStatusConditionTrue(train.Status.Conditions, "Preempted") {
				t.Errorf("a/train has no Preempted condition: %+v", train.Status.Conditions)
			}
			if train.Status.LeaseExpiresAt != nil {
				t.Errorf("a/train is Pending but shows a lease end")
			}
			// train may not preempt render, which has a higher priority.
			render := getClaim(ctx, clients["b"], "render")
			if render.Status.Phase != claimsv1alpha1.ClaimBound || render.Status.DeviceName == victim {
				t.Errorf("b/render = %s on %s, want Bound on the other A100", render.Status.Phase, render.Status.DeviceName)
			}
			return ctx
		}).
		Assess("claims in a and b race for the H100 and one wins", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			run(t, "", "docker", "rm", "-f", "solas-device-gpu-h100-1")
			run(t, "", "docker", "run", "-d", "--name", "solas-device-gpu-h100-1", "--network", "solas-mesh", "solas-demo:dev", "device")
			ip := strings.TrimSpace(run(t, "", "docker", "inspect", "-f",
				`{{(index .NetworkSettings.Networks "solas-mesh").IPAddress}}`, "solas-device-gpu-h100-1"))
			deviceURL = "http://" + ip + ":9000"
			eventually(t, 30*time.Second, "gatekeeper up", func(context.Context) bool {
				_, err := http.Get(deviceURL + "/log")
				return err == nil
			})
			manifest, err := os.ReadFile(root + "/demo/k3d/manifests/claim.yaml")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b"} {
				m := strings.NewReplacer("CLUSTER_ID", name, "DEVICE_URL", deviceURL).Replace(string(manifest))
				run(t, m, "kubectl", "--kubeconfig", kubeconfig(name), "apply", "-f", "-")
			}
			eventually(t, 60*time.Second, "one job claim Bound", func(ctx context.Context) bool {
				return (phase(ctx, clients["a"], "job") == claimsv1alpha1.ClaimBound) !=
					(phase(ctx, clients["b"], "job") == claimsv1alpha1.ClaimBound)
			})
			winner, loser = "a", "b"
			if phase(ctx, clients["b"], "job") == claimsv1alpha1.ClaimBound {
				winner, loser = "b", "a"
			}
			if h := holder(ctx, clients[loser], "gpu-h100-1"); h != winner+"/job/1" {
				t.Fatalf("gpu-h100-1 holder = %s, want %s/job/1", h, winner)
			}
			time.Sleep(3 * time.Second)
			if p := phase(ctx, clients[loser], "job"); p != claimsv1alpha1.ClaimPending {
				t.Fatalf("loser claim phase = %s, want Pending", p)
			}
			eventually(t, 30*time.Second, "winner uses the device", func(context.Context) bool {
				return seen(deviceURL, winner, 1, true)
			})
			return ctx
		}).
		Assess("the device rejects the stale holder after a reclaim", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			oldUID = memberUID(ctx, clients[winner], winner)
			node := "k3d-e2e-" + winner + "-server-0"
			run(t, "", "docker", "pause", node)
			waitPhase(t, clients[loser], "job", claimsv1alpha1.ClaimBound, 90*time.Second)
			if h := holder(ctx, clients[loser], "gpu-h100-1"); h != loser+"/job/2" {
				t.Fatalf("gpu-h100-1 holder = %s, want %s/job/2", h, loser)
			}
			eventually(t, 30*time.Second, "the new holder uses the device", func(context.Context) bool {
				return seen(deviceURL, loser, 2, true)
			})
			run(t, "", "docker", "unpause", node)
			eventually(t, 60*time.Second, "the device rejects token 1", func(context.Context) bool {
				return seen(deviceURL, winner, 1, false)
			})
			return ctx
		}).
		Assess("the old holder joins again and its claims are Lost", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			eventually(t, 90*time.Second, "new member UID", func(ctx context.Context) bool {
				uid := memberUID(ctx, clients[winner], winner)
				return uid != "" && uid != oldUID
			})
			waitPhase(t, clients[winner], "job", claimsv1alpha1.ClaimLost, 60*time.Second)
			return ctx
		}).
		Assess("the mesh moves to the other store with one setting", moveStore(clients)).
		Feature()

	testenv.Test(t, f)
}

// holders returns member/claim/token of every device, by name.
func holders(ctx context.Context, t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var list solasv1alpha1.DeviceList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range list.Items {
		h := ""
		if r := d.Status.ClaimRef; r != nil {
			h = r.Member + "/" + r.Name
		}
		out[d.Name] = fmt.Sprintf("%s/%d", h, d.Status.FencingToken)
	}
	return out
}

// moveStore moves the mesh to the other store with switch-store.sh, spec
// section 12. Holders, fencing tokens, and member UIDs stay the same, and
// a new claim binds on the new store.
func moveStore(clients map[string]client.Client) features.Func {
	return func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
		other := "etcd"
		if os.Getenv("SOLAS_STORE") == "etcd" {
			other = "dynamodb"
		}
		before := holders(ctx, t, clients["a"])
		uids := map[string]string{"a": memberUID(ctx, clients["a"], "a"), "b": memberUID(ctx, clients["b"], "b")}

		run(t, "", root+"/demo/k3d/switch-store.sh", other)

		eventually(t, 60*time.Second, "no claim Suspended", func(ctx context.Context) bool {
			for _, c := range clients {
				var list claimsv1alpha1.DeviceClaimList
				if err := c.List(ctx, &list); err != nil {
					return false
				}
				for _, cl := range list.Items {
					if cl.Status.Phase == claimsv1alpha1.ClaimSuspended {
						return false
					}
				}
			}
			return true
		})
		after := holders(ctx, t, clients["b"])
		if fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("holders and tokens changed in the move:\nbefore %v\nafter  %v", before, after)
		}
		for name, uid := range uids {
			if got := memberUID(ctx, clients[name], name); got != uid {
				t.Errorf("member %s uid = %s after the move, want %s", name, got, uid)
			}
		}

		claim := `apiVersion: claims.solas.dev/v1alpha1
kind: DeviceClaim
metadata:
  name: after-move
  namespace: work
spec:
  selector:
    cel: device.spec.attributes.speed == '100G'
`
		run(t, claim, "kubectl", "--kubeconfig", kubeconfig("a"), "apply", "-f", "-")
		waitPhase(t, clients["a"], "after-move", claimsv1alpha1.ClaimBound, 60*time.Second)
		if h := holder(ctx, clients["b"], "nic-2"); h != "a/after-move/1" {
			t.Errorf("nic-2 holder = %s, want a/after-move/1", h)
		}
		return ctx
	}
}
