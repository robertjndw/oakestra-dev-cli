package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// These are characterization tests: every command each code path issues,
// pinned exactly. They exist so a change to how oak-dev talks to docker has to
// be made on purpose. A wrong argv here does not fail loudly at runtime - it
// looks like success while serving a stale binary or leaving a debug port
// unpublished.
//
// The base -f chain for each stack is collapsed to <root>/<cluster>/<worker>,
// because it is identical in every command and is pinned separately, in full,
// by internal/compose's TestFileChains. Everything that varies per call site -
// the extra overlays and the verb - is asserted literally.

func argvEnv(t *testing.T, live map[string]bool) (*config.Config, *tools, *proc.Recorder) {
	t.Helper()
	return argvEnvScope(t, live, components.ScopeFull)
}

// argvEnvScope takes the scope up front: the compose client binds its chains
// at construction, so a test that narrowed cfg.Stack afterwards would be
// asserting against a topology the client never saw. Production sets the scope
// in PersistentPreRunE, before any command builds its tools.
func argvEnvScope(t *testing.T, live map[string]bool, stack string) (*config.Config, *tools, *proc.Recorder) {
	t.Helper()
	root := t.TempDir()
	// The real compose/ so the checked-in override fragments resolve, without
	// writing .generated into the actual checkout.
	real, err := filepath.Abs(filepath.Join("..", "..", "compose"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "compose")); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		RepoRoot:        root,
		OakestraRepo:    "/oakestra",
		OakestraNetRepo: "/oakestra-net",
		Stack:           stack,
		Live:            live,
		GOARCH:          "arm64",
		Workers:         1,
	}
	rec := proc.NewRecorder()
	tl, err := newToolsWith(cfg, rec)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, tl, rec
}

// commands renders what was recorded with each stack's base chain collapsed.
func commands(t *testing.T, cfg *config.Config, rec *proc.Recorder) []string {
	t.Helper()
	type sub struct{ from, to string }
	var subs []sub
	for _, stack := range components.Stacks() {
		base, err := compose.FilesForStack(cfg, map[string]bool{}, stack)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub{strings.Join(compose.FileArgs(base), " "), "<" + stack + ">"})
	}
	// Longest first, so the cluster chain is not partly eaten by a shorter one.
	slices.SortFunc(subs, func(a, b sub) int { return len(b.from) - len(a.from) })

	out := make([]string, len(rec.Specs))
	for i, s := range rec.Specs {
		line := s.String()
		for _, sb := range subs {
			line = strings.ReplaceAll(line, sb.from, sb.to)
		}
		out[i] = strings.ReplaceAll(line, cfg.RepoRoot+"/", "")
	}
	return out
}

func want(t *testing.T, cfg *config.Config, rec *proc.Recorder, want ...string) {
	t.Helper()
	got := commands(t, cfg, rec)
	if slices.Equal(got, want) {
		return
	}
	t.Errorf("commands:\n  got  (%d)\n    %s\n  want (%d)\n    %s",
		len(got), strings.Join(got, "\n    "), len(want), strings.Join(want, "\n    "))
}

func mustResolve(t *testing.T, name string) components.Component {
	t.Helper()
	c, err := components.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A component with a container in two stacks is cross-compiled once and
// restarted in both.
func TestArgvReloadGoComponent(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}

	want(t, cfg, rec,
		"go build -o build/linux_arm64/scheduler ./cmd",
		"docker compose <root> -f compose/override-live-root_scheduler.yml up -d root_scheduler",
		"docker compose <cluster> -f compose/override-live-cluster_scheduler.yml up -d cluster_scheduler",
		"docker compose <root> -f compose/override-live-root_scheduler.yml restart root_scheduler",
		"docker compose <cluster> -f compose/override-live-cluster_scheduler.yml restart cluster_scheduler",
	)
}

// nodeengine is restarted *inside* the worker. Recreating that container mints
// a new hostname, which cluster_manager registers as a new node ID, stranding
// every already-scheduled instance in NODE_SCHEDULED forever.
func TestArgvReloadNodeEngineNeverRecreatesTheWorker(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"nodeengine": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "nodeengine"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}

	want(t, cfg, rec,
		"go build -o build/linux_arm64/NodeEngine -ldflags=-X 'go_node_engine/cmd.Version=dev' .",
		"go build -o build/linux_arm64/nodeengined -ldflags=-X 'go_node_engine/cmd.Version=dev' ./internal/daemon/nodeengined.go",
		"docker compose <worker> -f compose/override-live-worker.yml exec -T worker pkill nodeengined",
	)
}

// netmanager shares the worker container with nodeengine and bounces it too:
// NodeEngine dials NetManager's unix socket once, at startup, and will not
// re-handshake on its own - so it must not be killed until the relaunched
// NetManager has actually recreated the socket.
func TestArgvReloadNetManagerWaitsForItsSocket(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"netmanager": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "netmanager"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got := commands(t, cfg, rec)
	if len(got) != 2 {
		t.Fatalf("commands = %v, want a build and one exec", got)
	}
	if w := "go build -o build/linux_arm64/NetManager -ldflags=-X 'NetManager/cmd.Version=dev' ."; got[0] != w {
		t.Errorf("build = %q, want %q", got[0], w)
	}
	for _, w := range []string{
		"docker compose <worker> -f compose/override-live-netmanager.yml exec -T worker sh -c",
		"pkill -x NetManager",                   // -x: must not match the dlv supervisor
		"rm -f /etc/netmanager/netmanager.sock", // or the poll false-positives on the stale socket
		"[ -S /etc/netmanager/netmanager.sock ]",
		"pkill nodeengined", // only after the socket is back
	} {
		if !strings.Contains(got[1], w) {
			t.Errorf("exec %q missing %q", got[1], w)
		}
	}
}

// Python is already bind-mounted under gunicorn --reload, so the only thing to
// do is reconcile the container against the declared topology - which is what
// undoes a debug overlay.
func TestArgvReloadPythonOnlyReconciles(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"system_manager": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "system_manager"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <root> -f compose/override-live-system_manager.yml up -d system_manager",
	)
}

// /oak-bin shadows whatever the rebuild bakes into the image, so --image alone
// would recreate the container onto the OLD host binary and report success.
func TestArgvReloadImageAlsoCrossCompilesWhenLive(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), true, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}

	want(t, cfg, rec,
		"go build -o build/linux_arm64/scheduler ./cmd",
		"docker compose <root> -f compose/override-live-root_scheduler.yml build root_scheduler",
		"docker compose <root> -f compose/override-live-root_scheduler.yml up -d root_scheduler",
		"docker compose <cluster> -f compose/override-live-cluster_scheduler.yml build cluster_scheduler",
		"docker compose <cluster> -f compose/override-live-cluster_scheduler.yml up -d cluster_scheduler",
	)
}

// A component that is not live gets added to live: and its container recreated
// so the bind-mount takes effect. The second up -d is reconcile running over
// the top; compose treats it as a no-op because the config already matches.
func TestArgvReloadPromotesToLive(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := reloadComponent(cfg, tl, mustResolve(t, "cluster_manager"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cfg.IsLive("cluster_manager") {
		t.Error("cluster_manager not added to the live set")
	}

	want(t, cfg, rec,
		"docker compose <cluster> -f compose/override-live-cluster_manager.yml up -d cluster_manager",
		"docker compose <cluster> -f compose/override-live-cluster_manager.yml up -d cluster_manager",
	)
}

// --no-live is the opt-out: fail rather than edit oak-dev.yaml, and touch
// nothing on the way out.
func TestArgvReloadNoLiveRunsNothing(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := reloadComponent(cfg, tl, mustResolve(t, "cluster_manager"), false, true, true); err == nil {
		t.Error("reload --no-live on a non-live component = nil error, want one")
	}
	want(t, cfg, rec)
}

// Those two run pinned upstream images with no local build: section, so there
// is nothing for --image to rebuild.
func TestArgvReloadImageRejectsPrebuiltRunsNothing(t *testing.T) {
	for _, name := range []string{"root_service_manager", "cluster_service_manager"} {
		t.Run(name, func(t *testing.T) {
			cfg, tl, rec := argvEnv(t, map[string]bool{})
			if err := reloadComponent(cfg, tl, mustResolve(t, name), true, false, true); err == nil {
				t.Errorf("reload %s --image = nil error, want one", name)
			}
			want(t, cfg, rec)
		})
	}
}

// Root and cluster both declare the shared network; worker.yml declares it
// external and fails outright if neither has run yet.
func TestArgvUpOrder(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if _, err := runUp(cfg, tl, false); err != nil {
		t.Fatalf("runUp: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <root> up -d --build",
		"docker compose <cluster> up -d --build",
		"docker compose <worker> up -d --build",
	)
}

func TestArgvUpScalesWorkers(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})
	cfg.Workers = 3

	if _, err := runUp(cfg, tl, false); err != nil {
		t.Fatalf("runUp: %v", err)
	}

	got := commands(t, cfg, rec)
	if w := "docker compose <worker> up -d --build --scale worker=3"; !slices.Contains(got, w) {
		t.Errorf("commands = %v, want one to be %q", got, w)
	}
}

// The worker needs a cluster to register with, but not a root.
func TestArgvUpWorkerScope(t *testing.T) {
	cfg, tl, rec := argvEnvScope(t, map[string]bool{}, components.StackWorker)

	if _, err := runUp(cfg, tl, false); err != nil {
		t.Fatalf("runUp: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <cluster> up -d --build",
		"docker compose <worker> up -d --build",
	)
}

func TestArgvDownIsReverseOrder(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runDown(cfg, tl, false, true); err != nil {
		t.Fatalf("runDown: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <worker> down -v",
		"docker compose <cluster> down -v",
		"docker compose <root> down -v",
	)
}

func TestArgvDownWithoutVolumes(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runDown(cfg, tl, false, false); err != nil {
		t.Fatalf("runDown: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <worker> down",
		"docker compose <cluster> down",
		"docker compose <root> down",
	)
}

// reset drops databases and restarts the app-level services. It must not touch
// the worker: it keeps its node ID and instances until its next handshake, and
// recreating it would strand them.
func TestArgvResetNeverTouchesTheWorker(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runReset(cfg, tl, false); err != nil {
		t.Fatalf("runReset: %v", err)
	}

	dropDBs := `db.getMongo().getDBNames().forEach(function(n){` +
		`if(["admin","local","config"].indexOf(n)<0){db.getSiblingDB(n).dropDatabase();}})`

	want(t, cfg, rec,
		"docker compose <root> exec -T mongo_root mongosh --port 10007 --quiet --eval "+dropDBs,
		"docker compose <root> exec -T root_redis redis-cli -a rootRedis FLUSHALL",
		"docker compose <root> restart system_manager jwt_generator root_resource_abstractor root_scheduler root_service_manager",
		"docker compose <cluster> exec -T mongo_cluster mongosh --port 10107 --quiet --eval "+dropDBs,
		"docker compose <cluster> exec -T cluster_redis redis-cli -a clusterRedis -p 6479 FLUSHALL",
		"docker compose <cluster> restart cluster_manager cluster_resource_abstractor cluster_scheduler cluster_service_manager",
	)
}

// The debug overlay layers on top of the live mount, so the component is made
// live first, then the container is force-recreated with the overlay applied.
func TestArgvDebugPython(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runDebug(cfg, tl, "cluster_manager", false, false); err != nil {
		t.Fatalf("runDebug: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <cluster> -f compose/override-live-cluster_manager.yml up -d cluster_manager",
		"docker compose <cluster> -f compose/override-live-cluster_manager.yml -f compose/override-debug-cluster_manager.yml up -d --force-recreate cluster_manager",
	)
}

// A Go component is rebuilt with optimizations off, and Delve is cross-compiled
// so it runs inside the container's OS and architecture.
//
// Scoped to root because --stack is what disambiguates a component with a
// container in more than one stack; unscoped, debug refuses rather than
// guessing which scheduler you meant.
func TestArgvDebugGoBuildsWithDebugFlags(t *testing.T) {
	cfg, tl, rec := argvEnvScope(t, map[string]bool{}, components.StackRoot)
	rec.OnContains("go env GOPATH", []byte("/gopath\n"), nil)

	// EnsureDelve's copy step fails (nothing to copy); the invocations before
	// it are the subject.
	_ = runDebug(cfg, tl, "scheduler", false, false)

	got := commands(t, cfg, rec)
	if !slices.Contains(got, "go build -o build/linux_arm64/scheduler -gcflags=all=-N -l ./cmd") {
		t.Errorf("commands = %v, want a -gcflags build", got)
	}
}

// shell probes for bash rather than trying it and falling back on error: a
// bash session the user exits non-zero would otherwise be mistaken for "bash
// is missing" and re-run as sh.
func TestArgvShellProbesForBash(t *testing.T) {
	tests := []struct {
		name      string
		whichBash error
		wantShell string
	}{
		{"bash present", nil, "bash"},
		{"bash missing", os.ErrNotExist, "sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, tl, rec := argvEnv(t, map[string]bool{})
			rec.OnContains("config --services", []byte("worker\n"), nil)
			rec.OnContains("which bash", nil, tt.whichBash)

			if err := runShell(cfg, tl, []string{"worker"}); err != nil {
				t.Fatalf("runShell: %v", err)
			}

			want(t, cfg, rec,
				"docker compose <worker> config --services",
				"docker compose <worker> exec -T worker which bash",
				"docker compose <worker> exec worker "+tt.wantShell,
			)
		})
	}
}

// An explicit command wins over the shell probe entirely.
func TestArgvShellExplicitCommand(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})
	rec.OnContains("config --services", []byte("worker\n"), nil)

	if err := runShell(cfg, tl, []string{"worker", "ctr", "-n", "oakestra", "containers", "ls"}); err != nil {
		t.Fatalf("runShell: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <worker> config --services",
		"docker compose <worker> exec worker ctr -n oakestra containers ls",
	)
}

// An endpoint opens its own client instead of a shell, and never probes.
func TestArgvShellEndpointUsesItsClient(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runShell(cfg, tl, []string{"mongo-root"}); err != nil {
		t.Fatalf("runShell: %v", err)
	}

	want(t, cfg, rec,
		"docker compose <root> exec mongo_root mongosh --port 10007",
	)
}

// The log sources are handed to multilog as a command to supervise rather than
// run here, so this pins the argv multilog would start.
func TestArgvLogSources(t *testing.T) {
	cfg, tl, _ := argvEnv(t, map[string]bool{})

	sources := stackLogSources(tl, "log:", 20)
	if len(sources) != 3 {
		t.Fatalf("got %d sources, want one per stack", len(sources))
	}
	for _, s := range sources {
		if s.Name != "docker" {
			t.Errorf("%s: Name = %q, want docker", s.Tag, s.Name)
		}
		if s.Dir != cfg.RepoRoot {
			t.Errorf("%s: Dir = %q, want the repo root", s.Tag, s.Dir)
		}
		joined := strings.Join(s.Args, " ")
		for _, w := range []string{"compose", "logs", "-f", "--tail=20"} {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: args %q missing %q", s.Tag, joined, w)
			}
		}
	}
	if got, want := sources[0].Tag, "log:root"; got != want {
		t.Errorf("first tag = %q, want %q", got, want)
	}
}
