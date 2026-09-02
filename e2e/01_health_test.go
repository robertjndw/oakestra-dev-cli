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
		// Bare Probe, not the authed Client: matches test_01_health.py's use
		// of a plain requests.get with its own short timeout, bypassing
		// ApiClient (and any login) entirely.
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
		// Any status code counts as alive - this only proves the process is
		// listening, not that it's authenticated or ready to schedule.
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
		// tests/test_01_health.py checks this by reading the bearer token
		// straight off the client's request headers; oakapi.Client keeps
		// its token private, so the equivalent - and arguably stronger -
		// check here is that an authenticated call actually succeeds rather
		// than coming back 401.
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
