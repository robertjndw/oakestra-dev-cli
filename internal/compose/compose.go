// Package compose builds the docker compose -f chains for the three stacks
// (root, cluster, worker) and runs docker compose with the right env and
// working directory. It mirrors the file lists that used to be hardcoded in
// the Makefile (ROOT_COMPOSE / CLUSTER_COMPOSE / WORKER_COMPOSE), and adds
// the live-mount overrides selected by internal/topology.
package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// FileArgs turns a file chain into flat -f/<path> pairs, in order.
func FileArgs(files []string) []string {
	var args []string
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}

// orchestratorFiles builds the compose file chain shared by RootFiles and
// ClusterFiles: the base compose file under dir, the opt-out profile
// overrides it supports, this repo's own overrides, and the live-mount
// overlays. hasDashboard is false for the cluster stack, which has no
// dashboard override to opt out of.
func orchestratorFiles(cfg *config.Config, live map[string]bool, dir string, hasDashboard bool, stack string, repoOverrides ...string) []string {
	repo := cfg.OakestraRepo
	files := []string{filepath.Join(repo, dir, "docker-compose.yml")}
	if !cfg.Profiles.Addons {
		files = append(files, filepath.Join(repo, dir, "override-no-addons.yml"))
	}
	if !cfg.Profiles.Observability {
		files = append(files, filepath.Join(repo, dir, "override-no-observe.yml"))
	}
	if hasDashboard && !cfg.Profiles.Dashboard {
		files = append(files, filepath.Join(repo, dir, "override-no-dashboard.yml"))
	}
	for _, o := range repoOverrides {
		files = append(files, filepath.Join(cfg.RepoRoot, "compose", o))
	}
	files = append(files, liveOverrides(cfg, live, stack)...)
	return files
}

// RootFiles returns the compose file chain for the root orchestrator stack.
func RootFiles(cfg *config.Config, live map[string]bool) []string {
	return orchestratorFiles(cfg, live, "root_orchestrator", true, components.StackRoot,
		"override-root-mongo.yml", "override-root-servicemanager.yml")
}

// ClusterFiles returns the compose file chain for the cluster orchestrator stack.
func ClusterFiles(cfg *config.Config, live map[string]bool) []string {
	return orchestratorFiles(cfg, live, "cluster_orchestrator", false, components.StackCluster,
		"override-cluster-mongo.yml", "override-cluster-servicemanager.yml")
}

// WorkerFiles returns the compose file chain for the dockerized worker.
func WorkerFiles(cfg *config.Config, live map[string]bool) []string {
	files := []string{filepath.Join(cfg.RepoRoot, "compose", "worker.yml")}
	files = append(files, liveOverrides(cfg, live, components.StackWorker)...)
	return files
}

// FilesForStack returns the compose file chain for the named stack (root |
// cluster | worker) - the one place that maps a stack name to its file-chain
// builder, shared by internal/topology and internal/target.
func FilesForStack(cfg *config.Config, live map[string]bool, stack string) ([]string, error) {
	switch stack {
	case components.StackRoot:
		return RootFiles(cfg, live), nil
	case components.StackCluster:
		return ClusterFiles(cfg, live), nil
	case components.StackWorker:
		return WorkerFiles(cfg, live), nil
	}
	return nil, fmt.Errorf("unknown stack %q", stack)
}

func liveOverrides(cfg *config.Config, live map[string]bool, stack string) []string {
	var files []string
	hasLivePython := false
	for _, c := range components.All() {
		if !live[c.Name] {
			continue
		}
		targets := c.InStack(stack)
		for _, t := range targets {
			if t.LiveOverride == "" {
				continue
			}
			files = append(files, filepath.Join(cfg.RepoRoot, "compose", t.LiveOverride))
		}
		if c.Kind == components.KindPython && len(targets) > 0 {
			hasLivePython = true
		}
	}
	// internal/topology.writeLibsOverlays only ever generates libs-<stack>.yml
	// when the stack has a live Python component in it (root/cluster) - only
	// reference the file here when that's actually true, or a worker-only
	// scope with libs_repo set references a file that was never written.
	if hasLivePython {
		if genLibs := libsOverridePath(cfg, stack); genLibs != "" {
			files = append(files, genLibs)
		}
	}
	return files
}

// libsOverridePath returns the path to the generated libs-mount overlay for
// this stack, if libs_repo is configured (internal/topology writes it).
func libsOverridePath(cfg *config.Config, stack string) string {
	if cfg.LibsRepo == "" {
		return ""
	}
	return filepath.Join(cfg.RepoRoot, ".generated", "libs-"+stack+".yml")
}

// Env returns the environment docker compose needs, matching what the
// Makefile used to export, plus the extra vars the live overrides reference.
func Env(cfg *config.Config) []string {
	env := os.Environ()
	set := func(k, v string) {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	set("OAKESTRA_REPO", cfg.OakestraRepo)
	set("OAKESTRA_NET_REPO", cfg.OakestraNetRepo)
	set("OAK_DEV_ROOT", cfg.RepoRoot)
	set("SYSTEM_MANAGER_URL", cfg.SystemManagerURL)
	set("CLUSTER_ADDRESS", cfg.ClusterAddr)
	set("CLUSTER_NAME", cfg.ClusterName)
	set("CLUSTER_LOCATION", cfg.ClusterLoc)
	set("LIB_BRANCH", cfg.LibBranch)
	set("NETMANAGER_VERSION", cfg.NetManagerVersion)
	set("GOARCH", cfg.GOARCH)
	return env
}

// Client runs docker compose. It holds the Runner rather than reaching for
// os/exec directly, which is what lets a test substitute a proc.Recorder and
// assert on the commands a code path would have issued.
//
// Callers receive one rather than calling a package-level function, so there
// is no process-starting global to reassign in tests.
type Client struct {
	cfg    *config.Config
	runner proc.Runner
	// topo is the rendered per-stack file chain, bound once at construction.
	// A stack missing from it is out of the current scope, which is the one
	// place that error is now produced.
	topo map[string][]string
	// overlays are extra files layered onto one stack's chain by
	// WithOverlays, for `oak-dev debug`.
	overlays map[string][]string
}

// stackOrder is the order stacks must start in, mirrored here so Stacks() can
// report the scope in a stable order.
var stackOrder = []string{components.StackRoot, components.StackCluster, components.StackWorker}

// New returns a Client that runs commands through runner, acting on the stacks
// in topo (as returned by topology.Chains or topology.Render).
func New(cfg *config.Config, runner proc.Runner, topo map[string][]string) *Client {
	return &Client{cfg: cfg, runner: runner, topo: topo, overlays: map[string][]string{}}
}

// spec builds the `docker compose <files> <args...>` command shared by Run
// and Output, before they diverge on how the output is handled.
func (c *Client) spec(files []string, args ...string) proc.Spec {
	cmdArgs := append([]string{"compose"}, FileArgs(files)...)
	cmdArgs = append(cmdArgs, args...)
	return proc.Spec{
		Name: "docker",
		Args: cmdArgs,
		Dir:  c.cfg.RepoRoot,
		Env:  Env(c.cfg),
	}
}

// run executes `docker compose <files> <args...>` with output streamed to the
// current process's stdout/stderr.
//
// Unexported: callers name an intent (Recreate, Restart, Exec) and the chain
// is resolved from the bound topology. Handing them a raw file list and
// argument slice is what let docker's vocabulary spread across fifteen call
// sites in the first place.
func (c *Client) run(files []string, args ...string) error {
	s := c.spec(files, args...)
	// Stdin is wired for every invocation, not just the interactive shell:
	// that is what `oak-dev shell` needs and what compose has always done, so
	// narrowing it here would be a behaviour change disguised as a cleanup.
	s.Stdin = true
	return c.runner.Run(s)
}

// output runs `docker compose <files> <args...>` and returns combined output.
// Combined, because every caller reports it back to the user attached to the
// error - stderr is the part worth reading when compose fails.
func (c *Client) output(files []string, args ...string) ([]byte, error) {
	s := c.spec(files, args...)
	s.Combined = true
	return c.runner.Capture(s)
}
