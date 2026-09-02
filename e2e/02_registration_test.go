//go:build e2e

package e2e

import (
	"fmt"
	"testing"

	"oak-dev/internal/oakapi"
)

// TestSmokeRegistration proves the hierarchy is wired up: a cluster is
// active, a worker is attached to it, and its resources are being reported
// to the root. Ported from tests/test_02_registration.py. Subtest names
// keep the pytest function names' meaning so `-run TestSmokeRegistration/worker_attached`
// replaces `pytest -k test_worker_attached`.
func TestSmokeRegistration(t *testing.T) {
	s := settings(t)

	t.Run("cluster_registered_and_active", func(t *testing.T) {
		c := activeCluster(t)
		active, _ := c["active"].(bool)
		if !active {
			t.Fatalf("active cluster reports active=%v, want true (cluster: %v)", c["active"], c)
		}
	})

	t.Run("worker_attached", func(t *testing.T) {
		// activeCluster already implies a worker is attached and reporting,
		// but check it directly against the resource abstractor too - that's
		// what cluster_manager pushes to the root every 15s.
		activeCluster(t)

		candidate := waitFor(t, oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    "a cluster reporting >= 1 active worker node",
		}, func() (map[string]any, error) {
			candidates, err := clusterCandidates(s)
			if err != nil {
				return nil, err
			}
			for _, c := range candidates {
				if numberField(c, "active_nodes") >= 1 {
					return c, nil
				}
			}
			return nil, fmt.Errorf("no cluster candidate reports >= 1 active node yet")
		})
		if n := numberField(candidate, "active_nodes"); n < 1 {
			t.Fatalf("active_nodes = %v, want >= 1", n)
		}
	})

	t.Run("cluster_reports_resources", func(t *testing.T) {
		activeCluster(t)

		waitFor(t, oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    "a cluster reporting nonzero cpu/memory capacity",
		}, func() (struct{}, error) {
			candidates, err := clusterCandidates(s)
			if err != nil {
				return struct{}{}, err
			}
			for _, c := range candidates {
				if numberField(c, "memory") > 0 && numberField(c, "vcpus") > 0 {
					return struct{}{}, nil
				}
			}
			return struct{}{}, fmt.Errorf("no cluster candidate reports nonzero cpu/memory capacity yet")
		})
	})
}
