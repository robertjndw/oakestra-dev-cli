package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/debugstate"
	"oak-dev/internal/testsuite"
)

func newDebugCmd() *cobra.Command {
	var noLive bool

	cmd := &cobra.Command{
		Use:     "debug <component>",
		GroupID: groupCode,
		Short:   "Attach a debugger to a component",
		Long: `Recreates the component's container with a debugger in front of it - Delve for
Go, debugpy for Python - and prints the localhost port to attach to. Those
ports match .vscode/launch.json, so "Attach: <component>" just works.

The component is mounted from your working tree first if it isn't already, so
the debugger's source paths line up 1:1 with the files you're editing.

For a component with more than one container (the scheduler runs in both the
root and cluster stacks), narrow it with --stack.`,
		Example: `  oak-dev debug cm                  # debugpy on localhost:5681
  oak-dev debug sched --stack root  # Delve on localhost:2345
  oak-dev debug ne                  # Delve on localhost:2347`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeComponents,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}
			return runDebug(cfg, tl, args[0], noLive)
		},
	}
	cmd.Flags().BoolVar(&noLive, "no-live", false, "fail instead of adding the component to oak-dev.yaml's live: list")
	return cmd
}

// debugOverlays returns the override-debug-*.yml files for every component
// currently attached in this stack, plus every compose service those files
// touch (which must all be recreated together for the overlay to take effect).
//
// Both halves matter. A debug overlay is not part of the rendered topology, so
// recreating a container without reapplying the overlays already on it detaches
// them - nodeengine and netmanager share the `worker` container, so debugging
// one used to knock the other's Delve listener out. And an overlay can
// configure a service other than the one being debugged:
// override-debug-cluster_service_manager.yml publishes its port on
// cluster_manager, because cluster_service_manager has no network namespace of
// its own, so recreating only cluster_service_manager would never publish it.
//
// A missing override file is a registry bug, not a user error, so it's reported
// as such rather than left to surface as a raw `docker compose` failure.
func debugOverlays(cfg *config.Config, stack, container string) (files, services []string, err error) {
	services = []string{container}
	for _, name := range debugstate.InStack(cfg.RepoRoot, stack) {
		c, err := components.Resolve(name)
		if err != nil {
			continue // a component renamed out from under the state file
		}
		for _, t := range c.InStack(stack) {
			if t.DebugOverride == "" {
				continue
			}
			path := filepath.Join(cfg.RepoRoot, "compose", t.DebugOverride)
			if _, err := os.Stat(path); err != nil {
				return nil, nil, fmt.Errorf("no debug override for %s: %s is missing", t.Container, path)
			}
			files = append(files, path)
			svcs, err := composeServices(path)
			if err != nil {
				return nil, nil, err
			}
			for _, s := range svcs {
				if !slices.Contains(services, s) {
					services = append(services, s)
				}
			}
		}
	}
	return files, services, nil
}

// composeServices returns the service names a compose fragment configures.
func composeServices(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var frag struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &frag); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	// Sorted, so the recreate command is the same from one run to the next.
	return slices.Sorted(maps.Keys(frag.Services)), nil
}

// pickTarget chooses which container to debug. --stack is the scope flag, so
// narrowing the scope naturally disambiguates a multi-container component
// rather than needing a second, differently-meaning flag.
func pickTarget(cfg *config.Config, c components.Component) (components.Target, error) {
	inScope := inScopeTargets(cfg, c)
	switch len(inScope) {
	case 1:
		return inScope[0], nil
	case 0:
		return components.Target{}, fmt.Errorf("%s has no containers in the current scope (%s)", c.Name, cfg.Stack)
	}

	var stacks []string
	for _, t := range inScope {
		stacks = append(stacks, t.Stack)
	}
	return components.Target{}, fmt.Errorf("%s runs in %d stacks (%s) - pick one with --stack",
		c.Name, len(inScope), joinNames(stacks))
}

// runDebug recreates a component's container with a debugger in front of it
// and prints the port to attach to. Split out of RunE so the sequence - guard
// the worker, make it live, build with debug flags, record the attachment,
// reapply every overlay on that container - is reachable from a test.
func runDebug(cfg *config.Config, tl *tools, name string, noLive bool) error {

	c, err := components.Resolve(name)
	if err != nil {
		return err
	}
	t, err := pickTarget(cfg, c)
	if err != nil {
		return err
	}

	// Recreating the worker container mid-suite mints a new node ID
	// and strands scheduled instances (see CLAUDE.md) - `oak-dev dev`
	// already guards its own worker restarts against this via the
	// same lock file, but `debug` force-recreates unconditionally
	// below, so it needs the same guard.
	if t.Stack == components.StackWorker {
		if testsuite.IsLocked(cfg) {
			return fmt.Errorf("oak-dev test is running - never restart/recreate the worker mid-suite (it mints a new node ID and strands scheduled instances)")
		}
	}

	// The debug override layers on top of the live mount, so the
	// component has to be live before this makes sense.
	if err := ensureLive(cfg, tl, c, noLive); err != nil {
		return err
	}

	if err := tl.rebind(cfg); err != nil {
		return err
	}

	if c.Kind == components.KindGo {
		fmt.Printf("oak-dev: building %s with debug flags (-gcflags=\"all=-N -l\")...\n", c.Name)
		if _, err := tl.build.Build(c, build.Options{Debug: true}); err != nil {
			return fmt.Errorf("debug build failed: %w", err)
		}
		if err := tl.build.EnsureDelve(); err != nil {
			return fmt.Errorf("installing delve: %w", err)
		}
	}
	// Python needs no preparation here: the overlays pip-install
	// debugpy themselves, at container start. Installing it up front
	// with `compose exec` would land in a writable layer the
	// --force-recreate below immediately throws away.

	// Record the attachment before rendering the chain, so this
	// component's own overlay is included alongside any already
	// attached to the same container.
	if err := debugstate.Add(cfg.RepoRoot, c.Name, t.Stack); err != nil {
		return err
	}
	overlays, services, err := debugOverlays(cfg, t.Stack, t.Container)
	if err != nil {
		// Same reasoning as the compose failure below: nothing was
		// attached, and leaving the entry behind would make every
		// later `debug` in this stack fail on it too.
		_ = debugstate.Remove(cfg.RepoRoot, c.Name, t.Stack)
		return err
	}
	// Every service the overlays touch is recreated together: an overlay can
	// configure one other than the component being debugged -
	// override-debug-cluster_service_manager.yml publishes its port on
	// cluster_manager, because it has no network namespace of its own.
	svcRefs := make([]compose.Ref, len(services))
	for i, name := range services {
		svcRefs[i] = compose.Ref{Stack: t.Stack, Container: name}
	}

	fmt.Printf("oak-dev: recreating %s with the debugger on localhost:%d...\n", joinNames(services), t.DebugPort)
	if err := tl.compose.WithOverlays(t.Stack, overlays...).ForceRecreate(svcRefs...); err != nil {
		// Nothing got attached, so don't leave the state file
		// claiming otherwise - a later `debug` on a container this one
		// shares would reapply an overlay that never took effect.
		_ = debugstate.Remove(cfg.RepoRoot, c.Name, t.Stack)
		return err
	}
	fmt.Printf("oak-dev: attach from VS Code (.vscode/launch.json) or any DAP client on localhost:%d.\n", t.DebugPort)
	if len(t.InPlaceRestart) > 0 {
		// reload deliberately never recreates the worker, so it can't
		// undo this one - see the comment on reconcile().
		fmt.Printf("         `oak-dev up --stack worker` detaches it again (this recreates the\n" +
			"         container, so the worker re-registers with a new node ID).\n")
	} else {
		fmt.Printf("         `oak-dev reload %s` detaches it again.\n", c.Name)
	}
	return nil
}
