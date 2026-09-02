//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"oak-dev/internal/oakapi"
)

// deadlineSlack is subtracted from the remaining time before t.Deadline()
// when clamping a Wait's timeout. Without it, a poll timing out a moment
// before go test's own -timeout still loses the race against reporting
// (marshalling the failure, running deferred cleanups) and the run ends in
// a goroutine dump instead of a readable *testing.T failure.
const deadlineSlack = 5 * time.Second

// settings, rootAPI and activeCluster are sync.Once-backed lazy accessors,
// not TestMain. TestMain is eager: it would have to either block every run
// on an active cluster - destroying TestSmokeHealth's purpose as the "is
// anything alive at all" probe - or guess in advance what's about to run.
// Package-level state matches pytest's session-scoped fixtures: computed
// once, reused by every test that asks for it.
//
// Each Once's error is memoized and re-reported by every caller via
// t.Fatalf. A one-shot t.Fatal inside the Once itself would only fail the
// first test to ask - every later caller would see a nil error and a nil
// value and panic somewhere far less informative.
var (
	settingsOnce sync.Once
	settingsVal  oakapi.Settings
	settingsErr  error

	rootAPIOnce sync.Once
	rootAPIVal  *oakapi.Client
	rootAPIErr  error

	activeClusterOnce sync.Once
	activeClusterVal  map[string]any
	activeClusterErr  error
)

// settings resolves the E2E suite's connection settings once per run.
func settings(t *testing.T) oakapi.Settings {
	t.Helper()
	settingsOnce.Do(func() {
		settingsVal, settingsErr = oakapi.LoadSettings()
	})
	if settingsErr != nil {
		t.Fatalf("load E2E settings: %v", settingsErr)
	}
	return settingsVal
}

// rootAPI returns a logged-in client for the root system_manager API,
// replacing tests/conftest.py's root_api fixture. Login is retried for
// ReadyTimeout so `oak-dev test` can run immediately after `oak-dev up`
// without a separate wait for the stack to finish booting.
func rootAPI(t *testing.T) *oakapi.Client {
	t.Helper()
	s := settings(t)
	rootAPIOnce.Do(func() {
		rootAPIVal, rootAPIErr = oakapi.Poll(oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    fmt.Sprintf("login at %s (is the stack up? try 'oak-dev up')", s.RootAPI),
		}, func() (*oakapi.Client, error) {
			cli := oakapi.NewClient(s.RootAPI)
			if err := cli.Login(s.Username, s.Password); err != nil {
				return nil, err
			}
			return cli, nil
		})
	})
	if rootAPIErr != nil {
		t.Fatalf("root API not ready: %v", rootAPIErr)
	}
	return rootAPIVal
}

// activeCluster waits for the first cluster reported active at the root,
// replacing tests/conftest.py's active_cluster fixture. A cluster only
// turns active once a worker is attached and reporting resources, so
// waiting for this implicitly waits for the worker too.
func activeCluster(t *testing.T) map[string]any {
	t.Helper()
	s := settings(t)
	cli := rootAPI(t)
	activeClusterOnce.Do(func() {
		activeClusterVal, activeClusterErr = oakapi.Poll(oakapi.Wait{
			Timeout: s.ReadyTimeout,
			Desc:    "an active cluster (worker attached and reporting)",
		}, func() (map[string]any, error) {
			status, raw, err := cli.Get("/api/clusters/active")
			if err != nil {
				return nil, err
			}
			if status != http.StatusOK {
				return nil, fmt.Errorf("get active clusters: status %d: %s", status, raw)
			}
			var clusters []map[string]any
			if err := oakapi.Decode(raw, &clusters); err != nil {
				return nil, err
			}
			if len(clusters) == 0 {
				return nil, fmt.Errorf("no active clusters registered yet")
			}
			return clusters[0], nil
		})
	})
	if activeClusterErr != nil {
		t.Fatalf("no active cluster: %v", activeClusterErr)
	}
	return activeClusterVal
}

// waitFor is the e2e-side half of tests/helpers.py's wait_until: a thin
// wrapper around oakapi.Poll that reports failure through *testing.T and
// clamps w.Timeout to whatever remains of t.Deadline(). Without that clamp,
// a stuck poll trips go test's own -timeout instead of Poll's, which prints
// a full goroutine dump for every live goroutine - the least readable
// failure mode this package can produce - rather than a normal test
// failure naming what was being waited for.
func waitFor[T any](t *testing.T, w oakapi.Wait, check func() (T, error)) T {
	t.Helper()
	if dl, ok := t.Deadline(); ok {
		if remaining := time.Until(dl) - deadlineSlack; remaining < w.Timeout {
			if remaining < 0 {
				remaining = 0
			}
			w.Timeout = remaining
		}
	}
	v, err := oakapi.Poll(w, check)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return v
}

// instanceDetail and jobDetail decode a service document with two fields
// oakapi.Job deliberately leaves out because only this package's failure
// and network checks need them: a per-instance status_detail, and the
// fixed round-robin service IP (RR_ip). Keeping them local to e2e avoids
// growing the shared Job type for two call sites.
type instanceDetail struct {
	InstanceNumber int    `json:"instance_number"`
	Status         string `json:"status"`
	StatusDetail   string `json:"status_detail"`
}

type jobDetail struct {
	JobName      string           `json:"job_name"`
	Status       string           `json:"status"`
	StatusDetail string           `json:"status_detail"`
	InstanceList []instanceDetail `json:"instance_list"`
	RRIP         string           `json:"RR_ip"`
}

// serviceDetail fetches and decodes a service document, or returns (nil,
// nil) on 404 - matches tests/helpers.py's get_service.
func serviceDetail(cli *oakapi.Client, serviceID oakapi.ObjectID) (*jobDetail, error) {
	status, raw, err := cli.Get(fmt.Sprintf("/api/service/%s", serviceID))
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get service %s: status %d: %s", serviceID, status, raw)
	}
	var job jobDetail
	if err := oakapi.Decode(raw, &job); err != nil {
		return nil, fmt.Errorf("decode service %s: %w", serviceID, err)
	}
	return &job, nil
}

// getService is serviceDetail narrowed to the shared oakapi.Job view, for
// the majority of checks that only need NotFailed()/Running() and don't
// care about per-instance detail or RR_ip.
func getService(cli *oakapi.Client, serviceID oakapi.ObjectID) (*oakapi.Job, error) {
	detail, err := serviceDetail(cli, serviceID)
	if err != nil || detail == nil {
		return nil, err
	}
	job := &oakapi.Job{
		JobName:      detail.JobName,
		Status:       detail.Status,
		StatusDetail: detail.StatusDetail,
		InstanceList: make([]oakapi.Instance, len(detail.InstanceList)),
	}
	for i, inst := range detail.InstanceList {
		job.InstanceList[i] = oakapi.Instance{InstanceNumber: inst.InstanceNumber, Status: inst.Status}
	}
	return job, nil
}

// deployInstance requests one new instance of serviceID - matches the bare
// `POST /api/service/{id}/instance` (no body) used throughout the pytest
// suite for both a fresh deploy and a scale-up.
func deployInstance(t *testing.T, cli *oakapi.Client, serviceID oakapi.ObjectID) {
	t.Helper()
	status, body, err := cli.Post(fmt.Sprintf("/api/service/%s/instance", serviceID), nil)
	if err != nil {
		t.Fatalf("deploy instance of %s: %v", serviceID, err)
	}
	if status != http.StatusOK {
		t.Fatalf("deploy instance of %s: status %d: %s", serviceID, status, body)
	}
}

// waitForRunningCount waits until exactly count instances of serviceID
// report RUNNING, matching tests/test_03_deployment.py's _wait_for_running.
// Every poll calls job.NotFailed() first so a terminal status aborts
// immediately instead of burning the full DeployTimeout - the fail-fast
// contract this whole port exists to make structural.
func waitForRunningCount(t *testing.T, cli *oakapi.Client, s oakapi.Settings, serviceID oakapi.ObjectID, count int) {
	t.Helper()
	waitFor(t, oakapi.Wait{
		Timeout:  s.DeployTimeout,
		Interval: 5 * time.Second,
		Desc:     fmt.Sprintf("%d RUNNING instance(s) of service %s", count, serviceID),
	}, func() (struct{}, error) {
		job, err := getService(cli, serviceID)
		if err != nil {
			return struct{}{}, err
		}
		if job == nil {
			return struct{}{}, oakapi.Terminal("service %s disappeared while waiting", serviceID)
		}
		if err := job.NotFailed(); err != nil {
			return struct{}{}, err
		}
		if running := len(job.Running()); running != count {
			return struct{}{}, fmt.Errorf("%d RUNNING, want %d; job status %s", running, count, job.Status)
		}
		return struct{}{}, nil
	})
}

// undeployAllInstances best-effort deletes every instance of serviceID -
// matches tests/helpers.py's undeploy_all_instances, used from cleanup
// paths where a failed earlier step means there is no well-known instance
// count left to undeploy precisely.
func undeployAllInstances(t *testing.T, cli *oakapi.Client, serviceID oakapi.ObjectID) {
	t.Helper()
	job, err := getService(cli, serviceID)
	if err != nil {
		t.Logf("cleanup: get service %s: %v", serviceID, err)
		return
	}
	if job == nil {
		return
	}
	for _, inst := range job.InstanceList {
		if _, _, err := cli.Delete(fmt.Sprintf("/api/service/%s/instance/%d", serviceID, inst.InstanceNumber)); err != nil {
			t.Logf("cleanup: undeploy instance %d of %s: %v", inst.InstanceNumber, serviceID, err)
		}
	}
}

// appDoc and serviceDoc decode the id out of application/service documents
// returned by the register/list endpoints, which render an id either as a
// plain string in their own ID field or as Mongo's _id (string or extended
// JSON) - mirrors tests/helpers.py's object_id() fallback, applied once per
// document type instead of at every call site.
type appDoc struct {
	ApplicationID oakapi.ObjectID `json:"applicationID"`
	Underscore    oakapi.ObjectID `json:"_id"`
	Name          string          `json:"application_name"`
}

func (a appDoc) id() oakapi.ObjectID {
	if a.ApplicationID != "" {
		return a.ApplicationID
	}
	return a.Underscore
}

type serviceDoc struct {
	MicroserviceID oakapi.ObjectID `json:"microserviceID"`
	Underscore     oakapi.ObjectID `json:"_id"`
	Name           string          `json:"microservice_name"`
}

func (s serviceDoc) id() oakapi.ObjectID {
	if s.MicroserviceID != "" {
		return s.MicroserviceID
	}
	return s.Underscore
}

// app is what registerApp hands back: the registered application's id and
// its microservices' ids, keyed by name.
type app struct {
	ID       oakapi.ObjectID
	Name     string
	Services map[string]oakapi.ObjectID
}

// registerApp ports tests/helpers.py's register_app: POST the SLA, find the
// created application by name in the (whole-user) response list, then GET
// its services and map each microservice name to its id.
//
// A delete of the application is registered via t.Cleanup immediately after
// a successful POST - not after the whole function succeeds - so the same
// single path covers both a mid-setup failure (e.g. the services GET below)
// and normal end-of-test teardown, where helpers.py needed a separate
// try/except.
func registerApp(t *testing.T, cli *oakapi.Client, appName string, ms ...oakapi.Microservice) app {
	t.Helper()

	sla := oakapi.NewSLA(appName, ms...)
	body, err := oakapi.MarshalJSON(sla)
	if err != nil {
		t.Fatalf("marshal SLA for %s: %v", appName, err)
	}

	status, raw, err := cli.Post("/api/application/", body)
	if err != nil {
		t.Fatalf("register app %s: %v", appName, err)
	}
	if status != http.StatusOK {
		t.Fatalf("register app %s: status %d: %s", appName, status, raw)
	}

	var apps []appDoc
	if err := oakapi.Decode(raw, &apps); err != nil {
		t.Fatalf("decode app registration response for %s: %v", appName, err)
	}

	var created *appDoc
	for i := range apps {
		if apps[i].Name == appName {
			created = &apps[i]
			break
		}
	}
	if created == nil {
		t.Fatalf("app %s not found in registration response: %s", appName, raw)
	}
	appID := created.id()

	t.Cleanup(func() {
		if _, _, err := cli.Delete(fmt.Sprintf("/api/application/%s", appID)); err != nil {
			t.Logf("cleanup: delete application %s (%s): %v", appName, appID, err)
		}
	})

	status, raw, err = cli.Get(fmt.Sprintf("/api/services/%s", appID))
	if err != nil {
		t.Fatalf("list services for %s: %v", appName, err)
	}
	if status != http.StatusOK {
		t.Fatalf("list services for %s: status %d: %s", appName, status, raw)
	}

	var services []serviceDoc
	if err := oakapi.Decode(raw, &services); err != nil {
		t.Fatalf("decode services for %s: %v", appName, err)
	}
	if len(services) != len(ms) {
		t.Fatalf("expected %d service(s) for %s, got %d: %s", len(ms), appName, len(services), raw)
	}

	ids := make(map[string]oakapi.ObjectID, len(services))
	for _, svc := range services {
		ids[svc.Name] = svc.id()
	}

	return app{ID: appID, Name: appName, Services: ids}
}

// clusterCandidates fetches the raw cluster candidate documents from the
// root resource abstractor - unauthenticated, matching
// tests/test_02_registration.py's _cluster_candidates, which bypasses
// ApiClient entirely.
func clusterCandidates(s oakapi.Settings) ([]map[string]any, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(s.RootRA + "/api/v1/resources/")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get cluster candidates: status %d: %s", resp.StatusCode, raw)
	}

	var candidates []map[string]any
	if err := oakapi.Decode(raw, &candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// numberField reads a numeric field out of a decoded JSON document. JSON
// numbers land in a map[string]any as float64; this also accepts a numeric
// string, matching Python's float(candidate.get(key) or 0) tolerance for
// either shape.
func numberField(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}
