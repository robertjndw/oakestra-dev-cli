package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/testsuite"
	"oak-dev/internal/topology"
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

			c, err := components.Resolve(args[0])
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
				if _, err := os.Stat(testsuite.LockPath(cfg)); err == nil {
					return fmt.Errorf("oak-dev test is running - never restart/recreate the worker mid-suite (it mints a new node ID and strands scheduled instances)")
				}
			}

			// The debug override layers on top of the live mount, so the
			// component has to be live before this makes sense.
			if err := ensureLive(cfg, c, noLive); err != nil {
				return err
			}

			files, err := topology.Render(cfg)
			if err != nil {
				return err
			}
			f, ok := files[t.Stack]
			if !ok {
				return fmt.Errorf("the %s stack is not in the current scope (%s)", t.Stack, cfg.Stack)
			}

			debugFile := filepath.Join(cfg.RepoRoot, "compose", t.DebugOverride)
			if _, err := os.Stat(debugFile); err != nil {
				return fmt.Errorf("no debug override for %s: %s is missing", t.Container, debugFile)
			}
			full := append(append([]string{}, f...), debugFile)

			if c.Kind == components.KindGo {
				fmt.Printf("oak-dev: building %s with debug flags (-gcflags=\"all=-N -l\")...\n", c.Name)
				if _, err := build.Build(cfg, c, build.Options{Debug: true}); err != nil {
					return fmt.Errorf("debug build failed: %w", err)
				}
				if err := build.EnsureDelve(cfg); err != nil {
					return fmt.Errorf("installing delve: %w", err)
				}
			} else {
				fmt.Printf("oak-dev: installing debugpy into %s...\n", t.Container)
				if out, err := compose.Output(cfg, f, "exec", "-T", t.Container,
					"pip", "install", "--quiet", "debugpy"); err != nil {
					return fmt.Errorf("installing debugpy: %w\n%s", err, out)
				}
			}

			fmt.Printf("oak-dev: recreating %s with the debugger on localhost:%d...\n", t.Container, t.DebugPort)
			if err := compose.Run(cfg, full, "up", "-d", "--force-recreate", t.Container); err != nil {
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
		},
	}
	cmd.Flags().BoolVar(&noLive, "no-live", false, "fail instead of adding the component to oak-dev.yaml's live: list")
	return cmd
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
