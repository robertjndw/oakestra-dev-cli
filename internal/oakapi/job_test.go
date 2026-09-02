package oakapi

import "testing"

// Every FailureStatuses entry must abort a poll, and every non-failure
// status (RUNNING and the two in-flight statuses) must not - a status that
// silently falls into either bucket wrongly either loops forever on a dead
// job or aborts a deployment that was still progressing.
func TestNotFailedIsTerminalForEveryFailureStatus(t *testing.T) {
	for status := range FailureStatuses {
		t.Run(status, func(t *testing.T) {
			j := &Job{JobName: "svc", Status: status, StatusDetail: "boom"}
			err := j.NotFailed()
			if err == nil {
				t.Fatalf("NotFailed() = nil for failure status %s, want an error", status)
			}
			if !IsTerminal(err) {
				t.Errorf("NotFailed() for %s = %v, want a Terminal error", status, err)
			}
		})
	}

	for _, status := range []string{"RUNNING", "REQUESTED", "COMPLETED"} {
		t.Run(status, func(t *testing.T) {
			j := &Job{JobName: "svc", Status: status}
			if err := j.NotFailed(); err != nil {
				t.Errorf("NotFailed() for non-failure status %s = %v, want nil", status, err)
			}
		})
	}
}

// The "no detail" fallback matters: status_detail is frequently empty on a
// scheduler rejection, and an empty message there would make a failed test
// harder to diagnose than the pytest suite it replaces.
func TestNotFailedFallsBackToNoDetail(t *testing.T) {
	j := &Job{JobName: "svc", Status: "FAILED"}
	err := j.NotFailed()
	if err == nil {
		t.Fatal("NotFailed() = nil, want an error")
	}
	const want = "job svc entered failure state FAILED: no detail"
	if err.Error() != want {
		t.Errorf("NotFailed() = %q, want %q", err.Error(), want)
	}
}

func TestJobRunningFiltersByStatus(t *testing.T) {
	j := &Job{InstanceList: []Instance{
		{InstanceNumber: 0, Status: "RUNNING"},
		{InstanceNumber: 1, Status: "NODE_SCHEDULED"},
		{InstanceNumber: 2, Status: "RUNNING"},
	}}
	running := j.Running()
	if len(running) != 2 {
		t.Fatalf("Running() = %v, want 2 instances", running)
	}
	if running[0].InstanceNumber != 0 || running[1].InstanceNumber != 2 {
		t.Errorf("Running() = %v, want instances 0 and 2", running)
	}
}
