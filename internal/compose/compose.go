// Package compose builds the docker compose -f chains for the three stacks
// (root, cluster, worker) and runs docker compose with the right env and
// working directory. It mirrors the file lists that used to be hardcoded in
// the Makefile (ROOT_COMPOSE / CLUSTER_COMPOSE / WORKER_COMPOSE), and adds
// the live-mount overrides selected by internal/topology.
package compose

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
)

// FileArgs turns a file chain into flat -f/<path> pairs, in order. Exported
// because the commands that build their own `docker compose` invocations
// (logs, dev) need it too, and used to each keep a private copy.
func FileArgs(files []string) []string {
	var args []string
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}

// RootFiles returns the compose file chain for the root orchestrator stack.
func RootFiles(cfg *config.Config, live map[string]bool) []string {
	repo := cfg.OakestraRepo
	files := []string{filepath.Join(repo, "root_orchestrator", "docker-compose.yml")}
	if !cfg.Profiles.Addons {
		files = append(files, filepath.Join(repo, "root_orchestrator", "override-no-addons.yml"))
	}
	if !cfg.Profiles.Observability {
		files = append(files, filepath.Join(repo, "root_orchestrator", "override-no-observe.yml"))
	}
	if !cfg.Profiles.Dashboard {
		files = append(files, filepath.Join(repo, "root_orchestrator", "override-no-dashboard.yml"))
	}
	files = append(files,
		filepath.Join(cfg.RepoRoot, "compose", "override-root-mongo.yml"),
		filepath.Join(cfg.RepoRoot, "compose", "override-root-servicemanager.yml"),
	)
	files = append(files, liveOverrides(cfg, live, components.StackRoot)...)
	return files
}

// ClusterFiles returns the compose file chain for the cluster orchestrator stack.
func ClusterFiles(cfg *config.Config, live map[string]bool) []string {
	repo := cfg.OakestraRepo
	files := []string{filepath.Join(repo, "cluster_orchestrator", "docker-compose.yml")}
	if !cfg.Profiles.Addons {
		files = append(files, filepath.Join(repo, "cluster_orchestrator", "override-no-addons.yml"))
	}
	if !cfg.Profiles.Observability {
		files = append(files, filepath.Join(repo, "cluster_orchestrator", "override-no-observe.yml"))
	}
	files = append(files,
		filepath.Join(cfg.RepoRoot, "compose", "override-cluster-mongo.yml"),
		filepath.Join(cfg.RepoRoot, "compose", "override-cluster-servicemanager.yml"),
	)
	files = append(files, liveOverrides(cfg, live, components.StackCluster)...)
	return files
}

// WorkerFiles returns the compose file chain for the dockerized worker.
func WorkerFiles(cfg *config.Config, live map[string]bool) []string {
	files := []string{filepath.Join(cfg.RepoRoot, "compose", "worker.yml")}
	files = append(files, liveOverrides(cfg, live, components.StackWorker)...)
	return files
}

func liveOverrides(cfg *config.Config, live map[string]bool, stack string) []string {
	var files []string
	for _, c := range components.All() {
		if !live[c.Name] {
			continue
		}
		for _, t := range c.InStack(stack) {
			if t.LiveOverride == "" {
				continue
			}
			files = append(files, filepath.Join(cfg.RepoRoot, "compose", t.LiveOverride))
		}
	}
	if len(live) > 0 {
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

// Run executes `docker compose <files> <args...>` with output streamed to
// the current process's stdout/stderr.
func Run(cfg *config.Config, files []string, args ...string) error {
	cmdArgs := append([]string{"compose"}, FileArgs(files)...)
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Dir = cfg.RepoRoot
	cmd.Env = Env(cfg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// Output runs `docker compose <files> <args...>` and returns combined output.
func Output(cfg *config.Config, files []string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"compose"}, FileArgs(files)...)
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Dir = cfg.RepoRoot
	cmd.Env = Env(cfg)
	return cmd.CombinedOutput()
}
