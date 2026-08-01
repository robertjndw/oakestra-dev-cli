package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/target"
)

// Command groups, so `--help` reads as four short lists rather than one flat
// alphabetical wall of everything.
const (
	groupStack   = "stack"
	groupCode    = "code"
	groupInspect = "inspect"
	groupSetup   = "setup"
)

// version is overridable at link time (-ldflags "-X main.version=...").
var version = "dev"

type ctxKey string

const cfgKey ctxKey = "oak-dev-config"

func newRootCmd() *cobra.Command {
	var stackFlag, repoRoot string

	root := &cobra.Command{
		Use:     "oak-dev",
		Short:   "Run, edit and test Oakestra locally",
		Version: version,
		Long: `oak-dev runs the Oakestra root orchestrator, cluster orchestrator and a
dockerized worker from your local oakestra checkout, and lets you edit that
source without rebuilding images.

Typical session:

  oak-dev doctor --fix     once, to check prerequisites and bootstrap
  oak-dev dev              start everything, watch for edits, stream logs
  oak-dev test             run the end-to-end suite
  oak-dev down             stop

Components can be named in full (cluster_manager), by alias (cm), or by any
unambiguous prefix (cluster_man). Configuration lives in oak-dev.yaml.`,
		// main prints the error itself, with the oak-dev: prefix every other
		// message uses. Letting cobra print it too showed each failure twice.
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: loadConfigInto(&stackFlag, &repoRoot),
	}

	root.PersistentFlags().StringVar(&stackFlag, "stack", "",
		"limit every command to a scope: full|root|cluster|worker (default: oak-dev.yaml's stack:)")
	root.PersistentFlags().StringVarP(&repoRoot, "repo-root", "C", "",
		"run as if from this oakestra-macos-testing checkout (default: the current directory)")
	_ = root.RegisterFlagCompletionFunc("stack",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return components.Scopes(), cobra.ShellCompDirectiveNoFileComp
		})

	root.AddGroup(
		&cobra.Group{ID: groupStack, Title: "Start and stop:"},
		&cobra.Group{ID: groupCode, Title: "Work on the code:"},
		&cobra.Group{ID: groupInspect, Title: "Look inside:"},
		&cobra.Group{ID: groupSetup, Title: "Set up:"},
	)

	root.AddCommand(
		// Start and stop
		newUpCmd(),
		newDownCmd(),
		newResetCmd(),
		newStatusCmd(),
		// Work on the code
		newDevCmd(),
		newReloadCmd(),
		newDebugCmd(),
		newTestCmd(),
		// Look inside
		newLogsCmd(),
		newShellCmd(),
		// Set up
		newDoctorCmd(),
	)
	return root
}

// loadConfigInto resolves configuration once, before any subcommand runs, and
// stashes it on the command context. Previously every RunE called a helper
// that re-read the config and called os.Exit(1) on failure, which made errors
// unformattable and untestable.
func loadConfigInto(stackFlag, repoRoot *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		root := *repoRoot
		if root == "" {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			root = wd
		}
		root, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(root, "compose", "worker.yml")); err != nil {
			return fmt.Errorf("%s is not an oakestra-macos-testing checkout (no compose/worker.yml).\n"+
				"cd there, or point at it with -C <path>", root)
		}

		cfg, err := config.Load(root)
		if err != nil {
			return err
		}
		if *stackFlag != "" {
			if err := cfg.SetStack(*stackFlag, "--stack"); err != nil {
				return err
			}
		}

		cmd.SetContext(context.WithValue(cmd.Context(), cfgKey, cfg))
		return nil
	}
}

// cfgFrom returns the configuration resolved by PersistentPreRunE.
func cfgFrom(cmd *cobra.Command) *config.Config {
	cfg, ok := cmd.Context().Value(cfgKey).(*config.Config)
	if !ok {
		// Unreachable via cobra: PersistentPreRunE always runs first.
		panic("oak-dev: configuration was not loaded")
	}
	return cfg
}

// completeComponents completes a component argument.
func completeComponents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return components.Complete(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeTargets completes a logs/shell target: stacks, components,
// endpoints and raw container names.
func completeTargets(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return target.Complete(cfgFrom(cmd), toComplete), cobra.ShellCompDirectiveNoFileComp
}
