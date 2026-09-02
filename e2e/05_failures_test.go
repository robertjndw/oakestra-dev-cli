//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"oak-dev/internal/oakapi"
)

// TestFailureReporting ports tests/test_05_failures.py: negative paths that
// check the platform reports what it can't run, instead of a happy path.
// The two subtests each register and clean up their own single-service
// application, so they're independent of each other, but they still share
// the one worker node with everything else in this package, so they stay
// off t.Parallel too.
//
// Neither subtest calls job.NotFailed(). That helper aborts a poll as soon
// as a job hits a terminal failure status, which is the opposite of what we
// want here - a failure status is the expected outcome, not a reason to
// bail out early.
func TestFailureReporting(t *testing.T) {
	t.Run("oversized_request_rejected_by_scheduler", func(t *testing.T) {
		s := settings(t)
		cli := rootAPI(t)
		activeCluster(t)

		appName := fmt.Sprintf("e2ebig%d", time.Now().Unix()%1_000_000)
		// 10 TB, well more than the whole cluster can possibly have.
		a := registerApp(t, cli, appName, oakapi.NewMicroservice("hugemem", oakapi.WithMemory(10_000_000)))
		serviceID := a.Services["hugemem"]
		t.Cleanup(func() { undeployAllInstances(t, cli, serviceID) })

		deployInstance(t, cli, serviceID)

		waitFor(t, oakapi.Wait{
			Timeout:  s.ReadyTimeout,
			Interval: 5 * time.Second,
			Desc:     "scheduler to reject a 10TB-memory service with a capacity status",
		}, func() (struct{}, error) {
			job, err := getService(cli, serviceID)
			if err != nil {
				return struct{}{}, err
			}
			if job == nil {
				return struct{}{}, fmt.Errorf("service %s disappeared while waiting", serviceID)
			}
			if _, negative := oakapi.NegativeSchedulingStatuses[job.Status]; negative {
				return struct{}{}, nil
			}
			for _, inst := range job.InstanceList {
				if _, negative := oakapi.NegativeSchedulingStatuses[inst.Status]; negative {
					return struct{}{}, nil
				}
			}
			return struct{}{}, fmt.Errorf("no negative scheduling status yet; job status %s", job.Status)
		})
	})

	t.Run("unpullable_image_reports_failed", func(t *testing.T) {
		s := settings(t)
		cli := rootAPI(t)
		activeCluster(t)

		appName := fmt.Sprintf("e2ebadimg%d", time.Now().Unix()%1_000_000)
		a := registerApp(t, cli, appName,
			oakapi.NewMicroservice("badimage", oakapi.WithImage("docker.io/library/oakestra-e2e-does-not-exist:latest")))
		serviceID := a.Services["badimage"]
		t.Cleanup(func() { undeployAllInstances(t, cli, serviceID) })

		deployInstance(t, cli, serviceID)

		waitFor(t, oakapi.Wait{
			Timeout:  s.DeployTimeout,
			Interval: 5 * time.Second,
			Desc:     "a nonexistent image to be reported FAILED with detail",
		}, func() (struct{}, error) {
			detail, err := serviceDetail(cli, serviceID)
			if err != nil {
				return struct{}{}, err
			}
			if detail == nil {
				return struct{}{}, fmt.Errorf("service %s disappeared while waiting", serviceID)
			}

			for _, inst := range detail.InstanceList {
				if inst.Status != "FAILED" {
					continue
				}
				statusDetail := inst.StatusDetail
				if statusDetail == "" {
					statusDetail = detail.StatusDetail
				}
				if strings.TrimSpace(statusDetail) == "" {
					return struct{}{}, oakapi.Terminal("FAILED status arrived without any status_detail")
				}
				return struct{}{}, nil
			}
			if detail.Status == "FAILED" {
				if strings.TrimSpace(detail.StatusDetail) == "" {
					return struct{}{}, oakapi.Terminal("FAILED status arrived without any status_detail")
				}
				return struct{}{}, nil
			}
			return struct{}{}, fmt.Errorf("not FAILED yet; job status %s", detail.Status)
		})
	})
}
