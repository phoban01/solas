package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
)

// workload uses the device of one claim while it sees the claim Bound.
type workload struct {
	client    client.Client
	key       client.ObjectKey
	clusterID string
	// device is the URL of the demo device, which checks tokens itself.
	// actuator and machine point instead at a gatekeeper in front of an
	// actuator, and the machine the claim's device stands for, spec 17.
	device   string
	actuator string
	machine  string
	// refresh is how often the workload reads its claim. It uses the device
	// more often than that, so it can act on a view that is out of date,
	// as a paused process would.
	refresh time.Duration
	every   time.Duration
}

//= spec/solas.md#6-6-device-use
//# A workload MUST present the fencing token of its claim each time it uses
//# the device.

func (w *workload) run(ctx context.Context) error {
	var phase claimsv1alpha1.ClaimPhase
	var token int64
	var readAt time.Time
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		//= spec/solas.md#6-5-phases
		//# Workloads MUST use a device only while its claim is in effect.
		//
		// The workload acts on the phase it last read, then reads again.
		// A workload that wakes from a pause acts on its old view first, as
		// a real one would; the fencing token keeps the device safe.
		if phase == claimsv1alpha1.ClaimBound {
			w.use(ctx, token)
		}
		if time.Since(readAt) >= w.refresh {
			var claim claimsv1alpha1.DeviceClaim
			if err := w.client.Get(ctx, w.key, &claim); err != nil {
				fmt.Printf("read claim: %v\n", err)
			} else {
				if claim.Status.Phase != phase || claim.Status.FencingToken != token {
					fmt.Printf("claim %s: phase %s, token %d\n", w.key, claim.Status.Phase, claim.Status.FencingToken)
				}
				phase, token = claim.Status.Phase, claim.Status.FencingToken
				readAt = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (w *workload) use(ctx context.Context, token int64) {
	if w.actuator != "" {
		w.deploy(ctx, token)
		return
	}
	body, _ := json.Marshal(useRequest{Claim: w.clusterID + "/" + w.key.String(), Token: token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.device+"/use", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("use: %v\n", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("use with token %d: %v\n", token, err)
		return
	}
	defer resp.Body.Close()
	var rec useRecord
	_ = json.NewDecoder(resp.Body).Decode(&rec)
	if rec.Accepted {
		fmt.Printf("use with token %d: accepted\n", token)
	} else {
		fmt.Printf("use with token %d: REJECTED, the device has seen token %d\n", token, rec.Highest)
	}
}

//= spec/solas.md#17-1-request
//# A request that changes a device MUST carry the claim of its workload in
//# the header `Solas-Claim`, as `member/namespace/name`.
//= spec/solas.md#17-1-request
//# It MUST carry the fencing token of the claim in the header
//# `Solas-Fencing-Token`.

// deploy asks the actuator, through the gatekeeper, to deploy the
// machine of the claim's device, with the claim and its token in the
// headers. The gatekeeper rejects a stale token with 403.
func (w *workload) deploy(ctx context.Context, token int64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.actuator+"/machines/"+w.machine+"/deploy", bytes.NewReader([]byte("{}")))
	if err != nil {
		fmt.Printf("deploy: %v\n", err)
		return
	}
	req.Header.Set("Solas-Claim", w.clusterID+"/"+w.key.Namespace+"/"+w.key.Name)
	req.Header.Set("Solas-Fencing-Token", fmt.Sprint(token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("deploy %s with token %d: %v\n", w.machine, token, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		fmt.Printf("deploy %s with token %d: accepted\n", w.machine, token)
		return
	}
	reason := make([]byte, 256)
	n, _ := resp.Body.Read(reason)
	fmt.Printf("deploy %s with token %d: REJECTED %d, %s\n", w.machine, token, resp.StatusCode, strings.TrimSpace(string(reason[:n])))
}
