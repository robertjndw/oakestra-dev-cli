package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/topology"
)

// upOrder is the order stacks must start in. Root and cluster both declare
// the shared "oakestra" network (non-external) so whichever comes up first
// creates it; worker.yml declares it `external: true` and fails outright if
// neither has run yet.
var upOrder = []string{components.StackRoot, components.StackCluster, components.StackWorker}

func downOrder() []string {
	rev := make([]string, len(upOrder))
	for i, s := range upOrder {
		rev[len(upOrder)-1-i] = s
	}
	return rev
}

// renderAll forces `full` scope so down/clean can reach every project
// regardless of what --stack was used to bring things up with.
func renderAll(cfg *config.Config) (map[string][]string, error) {
	full := *cfg
	full.Stack = components.ScopeFull
	return topology.Render(&full)
}

// inScopeTargets returns the targets of c that fall inside the current scope.
func inScopeTargets(cfg *config.Config, c components.Component) []components.Target {
	var out []components.Target
	for _, t := range c.Targets {
		if cfg.StackEnabled(t.Stack) {
			out = append(out, t)
		}
	}
	return out
}

// containerList renders target container names for status messages.
func containerList(targets []components.Target) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Container
	}
	return strings.Join(names, ", ")
}

func joinNames(names []string) string { return strings.Join(names, ", ") }

// ownArgCount returns how many positional arguments belong to oak-dev rather
// than to a command it forwards to. Everything after a literal `--` is
// passthrough, and cobra reports both in args.
func ownArgCount(cmd *cobra.Command, args []string) int {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return n
	}
	return len(args)
}

// shortSrc renders a component's source directory relative to the parent of
// its checkout (oakestra or oakestra-net), for "running from ..." messages.
func shortSrc(cfg *config.Config, c components.Component) string {
	repo := cfg.RepoPath(c.Repo)
	if rel, err := filepath.Rel(filepath.Dir(repo), cfg.SourceDir(c)); err == nil {
		return rel
	}
	return c.SourcePath
}

// ensureLiveBinaries cross-compiles any live Go component that doesn't have a
// binary on disk yet, so `up` doesn't hand the container an empty /oak-bin
// mount (the entrypoint would fail to resolve the binary at all, not just run
// a stale one).
func ensureLiveBinaries(cfg *config.Config) error {
	dir, err := build.Dir(cfg)
	if err != nil {
		return err
	}
	for _, c := range components.GoComponents() {
		if !cfg.IsLive(c.Name) {
			continue
		}
		wanted := []string{c.BinName}
		if c.Name == components.NodeEngineName {
			wanted = []string{"NodeEngine", "nodeengined"}
		}
		present := true
		for _, name := range wanted {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				present = false
			}
		}
		if present {
			continue
		}
		fmt.Printf("oak-dev: %s is live but has no build yet, cross-compiling once...\n", c.Name)
		if _, err := build.Build(cfg, c, build.Options{}); err != nil {
			return fmt.Errorf("initial build of %s: %w", c.Name, err)
		}
	}
	return nil
}

// runStack runs `docker compose <files...> <args...>` for one stack and
// prefixes any error with which stack failed.
func runStack(cfg *config.Config, stack string, files []string, args ...string) error {
	if err := compose.Run(cfg, files, args...); err != nil {
		return fmt.Errorf("%s stack: docker compose %v: %w", stack, args, err)
	}
	return nil
}

// confirm prompts for a destructive action. Returns true only on an explicit
// "y"; --yes skips it entirely for non-interactive use.
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}
