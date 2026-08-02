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
	"oak-dev/internal/proc"
	"oak-dev/internal/testsuite"
	"oak-dev/internal/topology"
)

// tools bundles the three collaborators that start external processes.
// Commands construct one in RunE and pass it down, rather than reading it
// from a package-level variable: that is what lets a test call these same
// functions with a proc.Recorder underneath and assert on what would have run.
//
// They are bundled because they travel together - reloadViaBinary needs the
// builder and the compose client in the same breath - and three parameters at
// every call would drown the arguments that carry meaning.
type tools struct {
	compose *compose.Client
	build   *build.Builder
	tests   *testsuite.Suite

	// runner is kept so the compose client can be rebuilt against a different
	// topology - see rebind and fullScope.
	runner proc.Runner
}

// newTools wires the real, process-starting Runner.
func newTools(cfg *config.Config) (*tools, error) {
	return newToolsWith(cfg, proc.OS{})
}

// newToolsWith is the seam: tests pass a proc.Recorder here.
//
// It binds the compose client to the rendered topology, using Chains rather
// than Render: constructing tools must not write .generated/, because shell
// completion constructs them on every TAB press.
func newToolsWith(cfg *config.Config, r proc.Runner) (*tools, error) {
	topo, err := topology.Chains(cfg)
	if err != nil {
		return nil, err
	}
	return &tools{
		compose: compose.New(cfg, r, topo),
		build:   build.New(cfg, r),
		tests:   testsuite.New(cfg, r),
		runner:  r,
	}, nil
}

// rebind recomputes the compose client's topology and writes the .generated/
// artifacts.
//
// It has to exist because the topology is not constant for the life of a
// command: adding a component to live: introduces a new override-live-*.yml,
// and a client still bound to the chain from before would recreate the
// container without the bind-mount - looking like success while running the
// baked image.
func (t *tools) rebind(cfg *config.Config) error {
	topo, err := topology.Render(cfg)
	if err != nil {
		return err
	}
	t.compose = compose.New(cfg, t.runner, topo)
	return nil
}

// fullScope returns tools whose compose client can reach every stack,
// regardless of the current --stack. `down` and `reset` need it: a plain
// `oak-dev down` after `up --stack worker` must still tear down the projects
// that earlier scope left running.
func (t *tools) fullScope(cfg *config.Config) (*tools, error) {
	full := *cfg
	full.Stack = components.ScopeFull
	topo, err := topology.Render(&full)
	if err != nil {
		return nil, err
	}
	next := *t
	next.compose = compose.New(cfg, t.runner, topo)
	return &next, nil
}

// ref narrows a registry target to the pair compose addresses containers by.
func ref(t components.Target) compose.Ref {
	return compose.Ref{Stack: t.Stack, Container: t.Container}
}

// refs narrows several at once.
func refs(targets []components.Target) []compose.Ref {
	out := make([]compose.Ref, len(targets))
	for i, t := range targets {
		out[i] = ref(t)
	}
	return out
}

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

// stacksOutOfScope returns the stacks the current scope excludes, in start
// order - empty when the scope covers everything.
func stacksOutOfScope(cfg *config.Config) []string {
	var out []string
	for _, stack := range upOrder {
		if !cfg.StackEnabled(stack) {
			out = append(out, stack)
		}
	}
	return out
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

// liveComponents resolves cfg.LiveNames() into components, in the same
// stable order. Shared by `reload`/`dev`'s no-argument case: reload the
// whole live set, watch the whole live set.
func liveComponents(cfg *config.Config) ([]components.Component, error) {
	var out []components.Component
	for _, name := range cfg.LiveNames() {
		c, err := components.Resolve(name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ensureLiveBinaries cross-compiles any live Go component that doesn't have a
// binary on disk yet, so `up` doesn't hand the container an empty /oak-bin
// mount (the entrypoint would fail to resolve the binary at all, not just run
// a stale one).
func ensureLiveBinaries(cfg *config.Config, tl *tools) error {
	dir, err := build.Dir(cfg)
	if err != nil {
		return err
	}
	for _, c := range components.GoComponents() {
		if !cfg.IsLive(c.Name) {
			continue
		}
		wanted := append([]string{c.BinName}, c.ExtraBinNames...)
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
		if _, err := tl.build.Build(c, build.Options{}); err != nil {
			return fmt.Errorf("initial build of %s: %w", c.Name, err)
		}
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
