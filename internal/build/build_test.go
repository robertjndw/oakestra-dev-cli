package build

import (
	"errors"
	"slices"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		RepoRoot:        t.TempDir(),
		OakestraRepo:    "/oakestra",
		OakestraNetRepo: "/oakestra-net",
		GOARCH:          "arm64",
		Live:            map[string]bool{},
	}
}

func resolve(t *testing.T, name string) components.Component {
	t.Helper()
	c, err := components.Resolve(name)
	if err != nil {
		t.Fatalf("resolving %s: %v", name, err)
	}
	return c
}

// The cross-compile has to target the container's OS and architecture, not the
// host's: the binary is bind-mounted into a linux container from macOS.
func TestBuildScheduler(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	out, err := New(cfg, rec).Build(resolve(t, "scheduler"), Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{"go build -o build/linux_arm64/scheduler ./cmd"}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if len(out) != 1 {
		t.Errorf("produced %v, want one binary", out)
	}

	spec := rec.Specs[0]
	if got, want := spec.Dir, "/oakestra/scheduler"; got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	for _, env := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"} {
		if !slices.Contains(spec.Env, env) {
			t.Errorf("Env missing %s", env)
		}
	}
}

// NodeEngine is the one component whose build produces two binaries. The
// daemon is the reload/debug target; the CLI is built alongside it and is what
// the worker's entrypoint uses for configuration.
func TestBuildNodeEngineProducesBothBinaries(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	out, err := New(cfg, rec).Build(resolve(t, "nodeengine"), Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{
		"go build -o build/linux_arm64/NodeEngine -ldflags=-X 'go_node_engine/cmd.Version=dev' .",
		"go build -o build/linux_arm64/nodeengined -ldflags=-X 'go_node_engine/cmd.Version=dev' ./internal/daemon/nodeengined.go",
	}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands =\n  %v\nwant\n  %v", got, want)
	}
	if len(out) != 2 {
		t.Errorf("produced %v, want two binaries", out)
	}
}

// NetManager.go lives at its repo root rather than under ./cmd, and its
// version var is stamped so `NetManager version` does not report the upstream
// default from a locally built binary.
func TestBuildNetManagerFromNetRepo(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	if _, err := New(cfg, rec).Build(resolve(t, "netmanager"), Options{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{"go build -o build/linux_arm64/NetManager -ldflags=-X 'NetManager/cmd.Version=dev' ."}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if got, want := rec.Specs[0].Dir, "/oakestra-net/node-net-manager"; got != want {
		t.Errorf("Dir = %q, want %q - netmanager builds from the net checkout", got, want)
	}
}

// Without -gcflags Delve cannot map optimized, inlined code back to source,
// so stepping through a debug session lands on the wrong lines.
func TestBuildDebugDisablesOptimization(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	if _, err := New(cfg, rec).Build(resolve(t, "scheduler"), Options{Debug: true}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{"go build -o build/linux_arm64/scheduler -gcflags=all=-N -l ./cmd"}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

func TestBuildRejectsPythonWithoutRunningAnything(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	if _, err := New(cfg, rec).Build(resolve(t, "system_manager"), Options{}); err == nil {
		t.Error("Build(system_manager) = nil error, want one")
	}
	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("ran %v, want nothing", got)
	}
}

// UnitTest runs host-native with no cross-compilation, so it must not
// inherit the GOOS/GOARCH of a build.
func TestUnitTestRunsInSourceDir(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	if err := New(cfg, rec).UnitTest(resolve(t, "scheduler"), "-run", "TestBestFit"); err != nil {
		t.Fatalf("UnitTest: %v", err)
	}

	want := []string{"go test ./... -run TestBestFit"}
	if got := rec.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	spec := rec.Specs[0]
	if got, want := spec.Dir, "/oakestra/scheduler"; got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	if spec.Env != nil {
		t.Errorf("Env = %v, want nil so the host toolchain is used unchanged", spec.Env)
	}
}

func TestUnitTestRejectsPython(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()

	if err := New(cfg, rec).UnitTest(resolve(t, "cluster_manager")); err == nil {
		t.Error("UnitTest(cluster_manager) = nil error, want one")
	}
	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("ran %v, want nothing", got)
	}
}

// A build failure has to surface: the container keeps serving the previous
// binary, so swallowing this means testing stale code without knowing it.
func TestBuildPropagatesFailure(t *testing.T) {
	cfg := testConfig(t)
	boom := errors.New("boom")
	rec := proc.NewRecorder()
	rec.Err = boom

	if _, err := New(cfg, rec).Build(resolve(t, "scheduler"), Options{}); !errors.Is(err, boom) {
		t.Errorf("Build err = %v, want %v", err, boom)
	}
}

// go install refuses to cross-compile with GOBIN set, and developers commonly
// have it set - so it is cleared rather than pointed at the output directory.
func TestEnsureDelveClearsGOBIN(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()
	rec.OnContains("go env GOPATH", []byte("/gopath\n"), nil)

	// The copy step fails because no dlv exists to copy; the invocations it
	// made before that point are the subject here.
	_ = New(cfg, rec).EnsureDelve()

	if len(rec.Specs) < 2 {
		t.Fatalf("recorded %v, want a go env and a go install", rec.Commands())
	}
	install := rec.Specs[1]
	if got, want := install.String(), "go install github.com/go-delve/delve/cmd/dlv@latest"; got != want {
		t.Errorf("install = %q, want %q", got, want)
	}
	if !slices.Contains(install.Env, "GOBIN=") {
		t.Error("Env missing GOBIN= - go install refuses to cross-compile with it set")
	}
	for _, env := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"} {
		if !slices.Contains(install.Env, env) {
			t.Errorf("Env missing %s - dlv has to run inside the container's OS/arch", env)
		}
	}
}
