//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"oak-dev/internal/oakapi"
)

// TestSmokeHealth is the "is anything alive at all" probe, ported from
// tests/test_01_health.py. It must not depend on rootAPI or activeCluster:
// those block on a working login and an attached worker, which is exactly
// what a stack that just started (or never came up) doesn't have yet.
func TestSmokeHealth(t *testing.T) {
	s := settings(t)

	t.Run("root_api_reachable", func(t *testing.T) {
		// Plain Probe here, not the authed Client - test_01_health.py does
		// this with a bare requests.get and its own short timeout, no login.
		waitFor(t, oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    fmt.Sprintf("system_manager at %s", s.RootAPI),
		}, func() (struct{}, error) {
			status, err := oakapi.Probe(s.RootAPI+"/api/clusters/", 5*time.Second)
			if err != nil {
				return struct{}{}, err
			}
			if status != http.StatusOK {
				return struct{}{}, fmt.Errorf("status %d, want 200", status)
			}
			return struct{}{}, nil
		})
	})

	t.Run("swagger_docs_available", func(t *testing.T) {
		status, err := oakapi.Probe(s.RootAPI+"/api/docs", 10*time.Second)
		if err != nil {
			t.Fatalf("GET %s/api/docs: %v", s.RootAPI, err)
		}
		if status != http.StatusOK {
			t.Fatalf("GET %s/api/docs: status %d, want 200", s.RootAPI, status)
		}
	})

	t.Run("cluster_manager_reachable", func(t *testing.T) {
		// Any status code counts as alive here. This only proves the process
		// is listening, not that it's ready to schedule anything.
		waitFor(t, oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    fmt.Sprintf("cluster_manager at %s", s.ClusterAPI),
		}, func() (struct{}, error) {
			if _, err := oakapi.Probe(s.ClusterAPI+"/", 5*time.Second); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, nil
		})
	})

	t.Run("admin_login_succeeds", func(t *testing.T) {
		// test_01_health.py checks this by reading the bearer token off the
		// client's request headers. oakapi.Client keeps its token private, so
		// we check the same thing more directly: an authenticated call
		// actually succeeds instead of coming back 401.
		cli := rootAPI(t)
		status, body, err := cli.Get("/api/clusters/active")
		if err != nil {
			t.Fatalf("authenticated request after login: %v", err)
		}
		if status == http.StatusUnauthorized {
			t.Fatalf("authenticated request after login returned 401: %s", body)
		}
	})
}
