//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"oak-dev/internal/oakapi"
)

// TestDeploymentLifecycle ports tests/test_03_deployment.py: one shared
// nginx application, deployed, scaled up, scaled down, then undeployed and
// deleted, in that order.
//
// The four steps are ordered subtests using `if !t.Run(name, fn) { return }`
// instead of four independent top-level tests. t.Fatalf inside a subtest only
// unwinds that subtest, so without the early return a failed deploy would
// still let "scale up to two instances" run against an application that
// never reached RUNNING - three cascading failures instead of one. pytest's
// four independent test functions had exactly that problem.
func TestDeploymentLifecycle(t *testing.T) {
	// No t.Parallel: these subtests change one application's instance count
	// in sequence, and share the one worker node with every other test here.
	s := settings(t)
	cli := rootAPI(t)
	activeCluster(t)

	appName := fmt.Sprintf("e2e%d", time.Now().Unix()%1_000_000)
	a := registerApp(t, cli, appName, oakapi.NewMicroservice("nginx"))
	serviceID := a.Services["nginx"]

	if !t.Run("deploy reaches RUNNING", func(t *testing.T) {
		deployInstance(t, cli, serviceID)
		waitForRunningCount(t, cli, s, serviceID, 1)
	}) {
		return
	}

	if !t.Run("scale up to two instances", func(t *testing.T) {
		deployInstance(t, cli, serviceID)
		waitForRunningCount(t, cli, s, serviceID, 2)
	}) {
		return
	}

	if !t.Run("scale down to one instance", func(t *testing.T) {
		job, err := getService(cli, serviceID)
		if err != nil {
			t.Fatalf("get service %s: %v", serviceID, err)
		}
		if job == nil || len(job.InstanceList) != 2 {
			t.Fatalf("expected 2 instances before scale down, got: %+v", job)
		}

		highest := job.InstanceList[0].InstanceNumber
		for _, inst := range job.InstanceList {
			if inst.InstanceNumber > highest {
				highest = inst.InstanceNumber
			}
		}

		status, body, err := cli.Delete(fmt.Sprintf("/api/service/%s/instance/%d", serviceID, highest))
		if err != nil {
			t.Fatalf("scale down instance %d of %s: %v", highest, serviceID, err)
		}
		if status != http.StatusOK {
			t.Fatalf("scale down instance %d of %s: status %d: %s", highest, serviceID, status, body)
		}

		waitForRunningCount(t, cli, s, serviceID, 1)
	}) {
		return
	}

	t.Run("undeploy and delete application", func(t *testing.T) {
		job, err := getService(cli, serviceID)
		if err != nil {
			t.Fatalf("get service %s: %v", serviceID, err)
		}
		if job != nil {
			for _, inst := range job.InstanceList {
				status, body, err := cli.Delete(fmt.Sprintf("/api/service/%s/instance/%d", serviceID, inst.InstanceNumber))
				if err != nil {
					t.Fatalf("undeploy instance %d of %s: %v", inst.InstanceNumber, serviceID, err)
				}
				if status != http.StatusOK {
					t.Fatalf("undeploy instance %d of %s: status %d: %s", inst.InstanceNumber, serviceID, status, body)
				}
			}
		}

		waitFor(t, oakapi.Wait{
			Timeout:  s.DeployTimeout,
			Interval: 5 * time.Second,
			Desc:     "all instances undeployed",
		}, func() (struct{}, error) {
			current, err := getService(cli, serviceID)
			if err != nil {
				return struct{}{}, err
			}
			if current == nil {
				return struct{}{}, oakapi.Terminal("service %s disappeared before app deletion", serviceID)
			}
			if len(current.InstanceList) != 0 {
				return struct{}{}, fmt.Errorf("%d instance(s) still present", len(current.InstanceList))
			}
			return struct{}{}, nil
		})

		status, body, err := cli.Delete(fmt.Sprintf("/api/application/%s", a.ID))
		if err != nil {
			t.Fatalf("delete application %s: %v", a.ID, err)
		}
		if status != http.StatusOK {
			t.Fatalf("delete application %s: status %d: %s", a.ID, status, body)
		}

		waitFor(t, oakapi.Wait{
			Timeout:  60 * time.Second,
			Interval: 3 * time.Second,
			Desc:     "service removed with the app",
		}, func() (struct{}, error) {
			gone, err := getService(cli, serviceID)
			if err != nil {
				return struct{}{}, err
			}
			if gone != nil {
				return struct{}{}, fmt.Errorf("service still present after app deletion")
			}
			return struct{}{}, nil
		})
	})
}
