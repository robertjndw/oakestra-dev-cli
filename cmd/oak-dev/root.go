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

// annotationNoConfig marks a command (and, via hasNoConfigAnnotation, all of
// its subcommands) as exempt from loadConfigInto's "is this an
// oakestra-dev-cli checkout" requirement. `skill` and `completion` both
// need to run from *other* repos - that's the whole point of `oak-dev skill
// install` - so neither can require the config this repo's checkout provides.
const annotationNoConfig = "oak-dev/no-config"

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
		"run as if from this oakestra-dev-cli checkout (default: the current directory)")
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
		newConfigCmd(),
		newSkillCmd(),
		newVSCodeCmd(),
	)
	wireCompletionInstall(root)
	return root
}

// wireCompletionInstall adds `install` under cobra's own auto-generated
// `completion` command. That command doesn't normally exist until Execute()
// creates it lazily, so it's forced into being here (InitDefaultCompletionCmd
// is safe to call early - it no-ops on a second call) purely so `install` has
// a parent to attach to; the bash/zsh/fish/powershell children it brings are
// exactly cobra's defaults, untouched.
func wireCompletionInstall(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.Annotations = map[string]string{annotationNoConfig: "true"}
			c.AddCommand(newCompletionInstallCmd())
			return
		}
	}
}

// hasNoConfigAnnotation reports whether cmd or any of its ancestors carries
// annotationNoConfig. Cobra's Annotations aren't inherited by children, so
// this walks up to the command that actually declared it (`skill`,
// `completion`) rather than requiring every leaf subcommand to repeat it.
func hasNoConfigAnnotation(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[annotationNoConfig] == "true" {
			return true
		}
	}
	return false
}

// loadConfigInto resolves configuration once, before any subcommand runs, and
// stashes it on the command context. Previously every RunE called a helper
// that re-read the config and called os.Exit(1) on failure, which made errors
// unformattable and untestable.
func loadConfigInto(stackFlag, repoRoot *string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		if hasNoConfigAnnotation(cmd) {
			return nil
		}

		root := *repoRoot
		if root == "" {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			root = wd
		}
		start, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		// config.FindRoot is the same worktreeMarker walk-up LoadE2E uses to
		// find .env from the E2E test binary's package-dir cwd - sharing it
		// here means the definition of "an oakestra-dev-cli checkout" lives
		// in exactly one place.
		root, err = config.FindRoot(start)
		if err != nil {
			return fmt.Errorf("%s is not an oakestra-dev-cli checkout (no compose/worker.yml).\n"+
				"cd there, or point at it with -C <path>", start)
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
	cfg := cfgFrom(cmd)
	tl, err := newTools(cfg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return target.NewResolver(cfg, tl.compose).Complete(toComplete), cobra.ShellCompDirectiveNoFileComp
}
