package controller

import (
	"testing"
	"time"
)

//= spec/solas.md#16-2-claims
//= type=test
//# The controller MUST bound each request to its API server to `D / 3`.

func TestRequestTimeoutIsAThirdOfTheLease(t *testing.T) {
	for _, lease := range []time.Duration{10 * time.Second, 30 * time.Second, 90 * time.Second} {
		if got := RequestTimeout(lease); got != lease/3 {
			t.Errorf("RequestTimeout(%v) = %v, want %v", lease, got, lease/3)
		}
	}
}
