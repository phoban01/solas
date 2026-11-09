//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
)

// storeContainer returns the Docker container of the store that the mesh
// uses now, from the storage URL in the Secret of cluster a. TestMesh
// ends with a move to the other store, so a default run tests the outage
// on etcd and a SOLAS_STORE=etcd run tests it on dynamodb-local.
func storeContainer(t *testing.T) string {
	t.Helper()
	enc := run(t, "", "kubectl", "--kubeconfig", kubeconfig("a"), "-n", "solas-system",
		"get", "secret", "solas-storage", "-o", "jsonpath={.data.url}")
	url, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		t.Fatalf("decode the storage URL: %v", err)
	}
	if strings.HasPrefix(string(url), "etcd://") {
		return "solas-etcd"
	}
	return "solas-ddb"
}

// held is what a Bound claim holds: its device and its fencing token.
type held struct {
	device string
	token  int64
}

// boundClaims returns the Bound claims of a cluster, by namespace/name.
func boundClaims(ctx context.Context, t *testing.T, c client.Client) map[string]held {
	t.Helper()
	var list claimsv1alpha1.DeviceClaimList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]held{}
	for _, cl := range list.Items {
		if cl.Status.Phase == claimsv1alpha1.ClaimBound {
			out[cl.Namespace+"/"+cl.Name] = held{cl.Status.DeviceName, cl.Status.FencingToken}
		}
	}
	return out
}

// phases returns the phase of each named claim of a cluster.
func phases(ctx context.Context, c client.Client, names map[string]held) map[string]claimsv1alpha1.ClaimPhase {
	out := map[string]claimsv1alpha1.ClaimPhase{}
	for key := range names {
		ns, name, _ := strings.Cut(key, "/")
		var cl claimsv1alpha1.DeviceClaim
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &cl); err == nil {
			out[key] = cl.Status.Phase
		}
	}
	return out
}

// allPhase reports whether every claim in before has phase p, in every
// cluster.
func allPhase(ctx context.Context, clients map[string]client.Client, before map[string]map[string]held, p claimsv1alpha1.ClaimPhase) bool {
	for name, c := range clients {
		for _, got := range phases(ctx, c, before[name]) {
			if got != p {
				return false
			}
		}
	}
	return true
}

//= spec/solas.md#16-1-the-server
//= type=test
//# The readiness of the server MUST NOT depend on the store.

//= spec/solas.md#16-2-claims
//= type=test
//# The controller MUST set a `Bound` claim to `Suspended` without a read of
//# the store, when its member is not live, section 6.5.

// TestStoreOutage pauses the store container for longer than D, spec 16.
// Every Bound claim goes to Suspended, a new claim stays Pending, and a
// namespace of the host cluster can still be deleted. When the store
// returns, every claim is Bound again on the same device with the same
// token, and the new claim binds a device that was free.
func TestStoreOutage(t *testing.T) {
	clients := map[string]client.Client{}
	var container string
	before := map[string]map[string]held{}
	var holdersBefore map[string]string

	f := features.New("the mesh survives a store outage").
		Setup(func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			clients["a"], clients["b"] = clientFor(t, "a"), clientFor(t, "b")
			container = storeContainer(t)
			t.Cleanup(func() { _, _ = runQuiet("docker", "unpause", container) })
			return ctx
		}).
		Assess("bound claims go Suspended and no bind happens while the store is paused", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			holdersBefore = holders(ctx, t, clients["a"])
			n := 0
			for name, c := range clients {
				before[name] = boundClaims(ctx, t, c)
				n += len(before[name])
			}
			if n == 0 {
				t.Fatal("no Bound claim to suspend; TestMesh must run first")
			}
			run(t, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: outage\n",
				"kubectl", "--kubeconfig", kubeconfig("a"), "apply", "-f", "-")

			t.Logf("pause %s", container)
			run(t, "", "docker", "pause", container)
			start := time.Now()
			eventually(t, 120*time.Second, "every Bound claim Suspended", func(ctx context.Context) bool {
				return allPhase(ctx, clients, before, claimsv1alpha1.ClaimSuspended)
			})
			t.Logf("every Bound claim Suspended %s after the pause", time.Since(start).Round(time.Second))

			// A new claim gets its phase from the controller, which needs
			// no store for that. Then it stays Pending: its member is not
			// live, so it does not bind.
			claim := `apiVersion: claims.solas.dev/v1alpha1
kind: DeviceClaim
metadata:
  name: during-outage
  namespace: work
spec:
  selector:
    cel: device.spec.attributes.model == 'T4'
`
			run(t, claim, "kubectl", "--kubeconfig", kubeconfig("a"), "apply", "-f", "-")
			waitPhase(t, clients["a"], "during-outage", claimsv1alpha1.ClaimPending, 60*time.Second)
			time.Sleep(20 * time.Second)
			if p := phase(ctx, clients["a"], "during-outage"); p != claimsv1alpha1.ClaimPending {
				t.Errorf("a/during-outage is %s during the outage, want Pending", p)
			}
			return ctx
		}).
		Assess("a namespace of the host cluster can be deleted during the outage", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			run(t, "", "kubectl", "--kubeconfig", kubeconfig("a"), "delete", "namespace", "outage", "--wait=false")
			eventually(t, 90*time.Second, "namespace outage gone", func(context.Context) bool {
				_, err := runQuiet("kubectl", "--kubeconfig", kubeconfig("a"), "get", "namespace", "outage")
				return err != nil
			})
			return ctx
		}).
		Assess("claims are Bound again on the same devices with the same tokens when the store returns", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			t.Logf("unpause %s", container)
			run(t, "", "docker", "unpause", container)
			start := time.Now()
			eventually(t, 120*time.Second, "every claim Bound again", func(ctx context.Context) bool {
				return allPhase(ctx, clients, before, claimsv1alpha1.ClaimBound) &&
					phase(ctx, clients["a"], "during-outage") == claimsv1alpha1.ClaimBound
			})
			t.Logf("every claim Bound again %s after the unpause", time.Since(start).Round(time.Second))
			for name, c := range clients {
				after := boundClaims(ctx, t, c)
				for key, want := range before[name] {
					if got := after[key]; got != want {
						t.Errorf("%s/%s after the outage holds %+v, want %+v", name, key, got, want)
					}
				}
			}
			// The new claim took a device that was free. Every other device
			// has the holder and the token it had before.
			taken := getClaim(ctx, clients["a"], "during-outage").Status
			if !strings.HasPrefix(holdersBefore[taken.DeviceName], "/") {
				t.Errorf("a/during-outage took %s, which was held before: %s", taken.DeviceName, holdersBefore[taken.DeviceName])
			}
			holdersAfter := holders(ctx, t, clients["b"])
			for device, want := range holdersBefore {
				if device == taken.DeviceName {
					want = fmt.Sprintf("a/during-outage/%d", taken.FencingToken)
				}
				if got := holdersAfter[device]; got != want {
					t.Errorf("%s after the outage: %s, want %s", device, got, want)
				}
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// runQuiet runs a command and returns its output and error, without
// failing the test.
func runQuiet(name string, args ...string) (string, error) {
	out, err := execCommand(name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}
