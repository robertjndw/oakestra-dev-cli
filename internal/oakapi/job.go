package oakapi

// NegativeSchedulingStatuses are the statuses the scheduler uses to reject a
// job outright. Mirrors NegativeSchedulingStatus in oakestra's
// oakestra_utils library, and copied exactly from
// tests/helpers.py:10-18 - it must stay a byte-for-byte match, since a
// missed rename there silently turns a fail-fast case into a full timeout.
var NegativeSchedulingStatuses = map[string]struct{}{
	"TargetClusterNotFound":       {},
	"TargetClusterNotActive":      {},
	"TargetClusterNoCapacity":     {},
	"NoActiveClusterWithCapacity": {},
	"NO_WORKER_CAPACITY":          {},
	"NO_QUALIFIED_WORKER_FOUND":   {},
	"NO_NODE_FOUND":               {},
}

// FailureStatuses are every status that means the platform gave up on a
// deployment - polling further is pointless. Mirrors
// tests/helpers.py:20-22 (NegativeSchedulingStatuses | {"FAILED", "DEAD"}).
var FailureStatuses = func() map[string]struct{} {
	statuses := map[string]struct{}{"FAILED": {}, "DEAD": {}}
	for s := range NegativeSchedulingStatuses {
		statuses[s] = struct{}{}
	}
	return statuses
}()

// Instance is one running (or scheduled, or failed) copy of a service.
type Instance struct {
	InstanceNumber int    `json:"instance_number"`
	Status         string `json:"status"`
}

// Job is a service/job document as returned by GET /api/service/{id}.
type Job struct {
	JobName      string     `json:"job_name"`
	Status       string     `json:"status"`
	StatusDetail string     `json:"status_detail"`
	InstanceList []Instance `json:"instance_list"`
}

// Running returns the instances of j that report RUNNING - matches
// tests/helpers.py's running_instances.
func (j *Job) Running() []Instance {
	var out []Instance
	for _, inst := range j.InstanceList {
		if inst.Status == "RUNNING" {
			out = append(out, inst)
		}
	}
	return out
}

// NotFailed fails fast if the scheduler or worker gave up on j - matches
// tests/helpers.py's assert_not_failed, including its "no detail" fallback.
// The returned error is Terminal, so a Poll loop checking it aborts
// immediately instead of retrying until DeployTimeout on a job that will
// never recover.
func (j *Job) NotFailed() error {
	if _, failed := FailureStatuses[j.Status]; !failed {
		return nil
	}
	detail := j.StatusDetail
	if detail == "" {
		detail = "no detail"
	}
	return Terminal("job %s entered failure state %s: %s", j.JobName, j.Status, detail)
}
