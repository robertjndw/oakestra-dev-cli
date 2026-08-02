package compose

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// Fixed, obviously-fake checkout paths keep the golden chains readable and
// independent of wherever the test happens to run.
const (
	testRepoRoot = "/repo"
	testOakestra = "/oakestra"
	testNet      = "/oakestra-net"
)

func testConfig() *config.Config {
	return &config.Config{
		RepoRoot:        testRepoRoot,
		OakestraRepo:    testOakestra,
		OakestraNetRepo: testNet,
		Stack:           components.ScopeFull,
		Live:            map[string]bool{},
		GOARCH:          "arm64",
		Workers:         1,
	}
}

// The chain is the part of every compose invocation that is easy to break and
// impossible to notice: a dropped override silently changes which image runs.
func TestFileChains(t *testing.T) {
	cfg := testConfig()
	live := map[string]bool{}

	tests := []struct {
		stack string
		want  []string
	}{
		{components.StackRoot, []string{
			"/oakestra/root_orchestrator/docker-compose.yml",
			"/oakestra/root_orchestrator/override-no-addons.yml",
			"/oakestra/root_orchestrator/override-no-observe.yml",
			"/oakestra/root_orchestrator/override-no-dashboard.yml",
			"/repo/compose/override-root-mongo.yml",
			"/repo/compose/override-root-servicemanager.yml",
		}},
		{components.StackCluster, []string{
			"/oakestra/cluster_orchestrator/docker-compose.yml",
			"/oakestra/cluster_orchestrator/override-no-addons.yml",
			"/oakestra/cluster_orchestrator/override-no-observe.yml",
			"/repo/compose/override-cluster-mongo.yml",
			"/repo/compose/override-cluster-servicemanager.yml",
		}},
		{components.StackWorker, []string{
			"/repo/compose/worker.yml",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.stack, func(t *testing.T) {
			got, err := FilesForStack(cfg, live, tt.stack)
			if err != nil {
				t.Fatalf("FilesForStack: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("chain =\n  %s\nwant\n  %s",
					strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

// Enabling a profile drops the upstream opt-out file for it. This repo's own
// overrides are not profile-gated and must survive: the mongo pin is what
// keeps the database starting at all on a 6.19+ kernel, and the service
// manager override is what keeps overlay traffic working.
func TestFileChainsProfilesOptIn(t *testing.T) {
	cfg := testConfig()
	cfg.Profiles = config.ProfilesCfg{Dashboard: true, Observability: true, Addons: true}

	got, err := FilesForStack(cfg, map[string]bool{}, components.StackRoot)
	if err != nil {
		t.Fatalf("FilesForStack: %v", err)
	}
	want := []string{
		"/oakestra/root_orchestrator/docker-compose.yml",
		"/repo/compose/override-root-mongo.yml",
		"/repo/compose/override-root-servicemanager.yml",
	}
	if !slices.Equal(got, want) {
		t.Errorf("chain = %v, want %v", got, want)
	}
}

// A live component adds its bind-mount overlay, and only in the stack that
// component actually runs in.
func TestFileChainsLiveOverrides(t *testing.T) {
	cfg := testConfig()
	live := map[string]bool{"scheduler": true}

	root, err := FilesForStack(cfg, live, components.StackRoot)
	if err != nil {
		t.Fatalf("FilesForStack(root): %v", err)
	}
	if got, want := last(root), "/repo/compose/override-live-root_scheduler.yml"; got != want {
		t.Errorf("root chain ends %q, want %q", got, want)
	}

	cluster, err := FilesForStack(cfg, live, components.StackCluster)
	if err != nil {
		t.Fatalf("FilesForStack(cluster): %v", err)
	}
	if got, want := last(cluster), "/repo/compose/override-live-cluster_scheduler.yml"; got != want {
		t.Errorf("cluster chain ends %q, want %q", got, want)
	}

	worker, err := FilesForStack(cfg, live, components.StackWorker)
	if err != nil {
		t.Fatalf("FilesForStack(worker): %v", err)
	}
	if slices.ContainsFunc(worker, func(f string) bool {
		return strings.Contains(f, "override-live-")
	}) {
		t.Errorf("worker chain = %v, want no live overlay - scheduler does not run there", worker)
	}
}

// The generated libs overlay is only referenced when the stack actually has a
// live Python component in it; otherwise the chain names a file topology never
// wrote.
func TestFileChainsLibsOverlayOnlyWithLivePython(t *testing.T) {
	cfg := testConfig()
	cfg.LibsRepo = "/libs"

	pythonLive, err := FilesForStack(cfg, map[string]bool{"system_manager": true}, components.StackRoot)
	if err != nil {
		t.Fatalf("FilesForStack: %v", err)
	}
	if got, want := last(pythonLive), "/repo/.generated/libs-root.yml"; got != want {
		t.Errorf("chain ends %q, want %q", got, want)
	}

	goLive, err := FilesForStack(cfg, map[string]bool{"scheduler": true}, components.StackRoot)
	if err != nil {
		t.Fatalf("FilesForStack: %v", err)
	}
	if slices.ContainsFunc(goLive, func(f string) bool { return strings.Contains(f, "libs-") }) {
		t.Errorf("chain = %v, want no libs overlay for a Go-only live set", goLive)
	}
}

func TestFilesForStackUnknown(t *testing.T) {
	if _, err := FilesForStack(testConfig(), nil, "nope"); err == nil {
		t.Error("FilesForStack(nope) = nil error, want one")
	}
}

// Run and Output must differ in exactly two ways and no others: Output
// captures combined output, Run wires stdin.
func TestClientSpec(t *testing.T) {
	cfg := testConfig()
	rec := proc.NewRecorder()
	c := New(cfg, rec, map[string][]string{})

	files := []string{"/repo/compose/worker.yml"}
	if err := c.run(files, "restart", "worker"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := c.output(files, "config", "--services"); err != nil {
		t.Fatalf("Output: %v", err)
	}

	if len(rec.Specs) != 2 {
		t.Fatalf("recorded %d specs, want 2", len(rec.Specs))
	}
	run, out := rec.Specs[0], rec.Specs[1]

	if got, want := run.String(),
		"docker compose -f /repo/compose/worker.yml restart worker"; got != want {
		t.Errorf("Run spec = %q, want %q", got, want)
	}
	if got, want := out.String(),
		"docker compose -f /repo/compose/worker.yml config --services"; got != want {
		t.Errorf("Output spec = %q, want %q", got, want)
	}

	if !run.Stdin {
		t.Error("Run spec has Stdin false, want true - `oak-dev shell` needs it")
	}
	if run.Combined {
		t.Error("Run spec has Combined true, want false")
	}
	if !out.Combined {
		t.Error("Output spec has Combined false, want true - callers report stderr to the user")
	}
	if run.Dir != testRepoRoot {
		t.Errorf("Run spec Dir = %q, want %q", run.Dir, testRepoRoot)
	}
}

// Compose resolves relative paths against the first -f file's directory, not
// the invoking cwd, so every variable the overrides interpolate has to be
// present and absolute.
func TestEnv(t *testing.T) {
	cfg := testConfig()
	cfg.SystemManagerURL = "system_manager"
	cfg.ClusterAddr = "cluster_manager"
	cfg.ClusterName = "test-cluster"
	cfg.ClusterLoc = "52.5200,13.4050,100"
	cfg.LibBranch = "develop"
	cfg.NetManagerVersion = "alpha-v0.4.411"

	env := Env(cfg)
	want := map[string]string{
		"OAKESTRA_REPO":      testOakestra,
		"OAKESTRA_NET_REPO":  testNet,
		"OAK_DEV_ROOT":       testRepoRoot,
		"SYSTEM_MANAGER_URL": "system_manager",
		"CLUSTER_ADDRESS":    "cluster_manager",
		"CLUSTER_NAME":       "test-cluster",
		"CLUSTER_LOCATION":   "52.5200,13.4050,100",
		"LIB_BRANCH":         "develop",
		"NETMANAGER_VERSION": "alpha-v0.4.411",
		"GOARCH":             "arm64",
	}
	for k, v := range want {
		if !slices.Contains(env, k+"="+v) {
			t.Errorf("Env() missing %s=%s", k, v)
		}
	}
}

func TestFileArgs(t *testing.T) {
	got := FileArgs([]string{"a.yml", "b.yml"})
	want := []string{"-f", "a.yml", "-f", "b.yml"}
	if !slices.Equal(got, want) {
		t.Errorf("FileArgs() = %v, want %v", got, want)
	}
}

// A compose failure must reach the caller intact - reload prints it back with
// the container name attached.
func TestClientPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	rec := proc.NewRecorder()
	rec.Err = boom

	cfg := testConfig()
	topo := map[string][]string{components.StackWorker: {"/repo/compose/worker.yml"}}
	c := New(cfg, rec, topo)

	if err := c.Recreate(Ref{Stack: components.StackWorker, Container: "worker"}); !errors.Is(err, boom) {
		t.Errorf("Recreate err = %v, want %v", err, boom)
	}
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
