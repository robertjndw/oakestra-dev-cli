package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"oak-dev/internal/config"
	"oak-dev/internal/vscode"
)

// newVSCodeCmd wires the debug port and source path this CLI already knows
// about (internal/components, internal/config) into VS Code's own
// configuration files, so the Run and Debug menu can start/attach a
// component instead of a terminal command.
func newVSCodeCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "vscode",
		GroupID: groupSetup,
		Short:   "Install VS Code debug/task configs generated from the component registry",
		Long: `Generates .vscode/launch.json and .vscode/tasks.json from internal/components
and this checkout's resolved config (oak-dev.yaml, .env) - every debug port
and source path oak-dev knows about, rendered as VS Code configuration
instead of hand-maintained and left to drift out of sync with it.

Run it from wherever you actually edit source. oak-dev still needs an
oakestra-dev-cli checkout to resolve that config from - if the current
directory isn't one, point -C at it; -C only changes where configuration
comes from, the .vscode files still land in the directory you name (or the
current directory, by default):

  cd ../oakestra && oak-dev vscode install -C ../oakestra-dev-cli

Existing files are merged, not replaced: only entries whose name/label
starts with "oak-dev: " (or, for tasks.json's inputs, an id starting with
"oakDev") are regenerated on each run - anything else already there is left
alone. Comments in an existing file can't survive that merge, though
(encoding/json has nowhere to keep them); install warns when that would
happen.

Each generated launch configuration's preLaunchTask runs
"oak-dev debug <component> --wait" first, so F5 both attaches the debugger
and recreates the container with it turned on - see "oak-dev debug --help".`,
		Example: `  oak-dev vscode install                                # this directory
  oak-dev vscode install ../oakestra ../oakestra-net     # explicit targets
  oak-dev vscode install --all                           # this checkout + both repos
  cd ../oakestra && oak-dev vscode install -C ../oakestra-dev-cli
  oak-dev vscode status
  oak-dev vscode uninstall`,
	}
	cmd.PersistentFlags().BoolVar(&all, "all", false,
		"this checkout plus $OAKESTRA_REPO and $OAKESTRA_NET_REPO, instead of the current directory")
	cmd.AddCommand(
		newVSCodeInstallCmd(&all),
		newVSCodeStatusCmd(&all),
		newVSCodeUninstallCmd(&all),
	)
	return cmd
}

// vscodeTargets resolves which directories a vscode subcommand acts on:
// explicit positional args, --all (this checkout plus both source repos,
// skipping any that aren't actually checked out), or the current directory
// when neither was given.
func vscodeTargets(cfg *config.Config, args []string, all bool) ([]string, error) {
	if all {
		if len(args) > 0 {
			return nil, fmt.Errorf("--all and explicit directories are mutually exclusive")
		}
		var out []string
		seen := map[string]bool{}
		for _, dir := range []string{cfg.RepoRoot, cfg.OakestraRepo, cfg.OakestraNetRepo} {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if _, err := os.Stat(dir); err != nil {
				fmt.Printf("oak-dev: skipping %s (not checked out)\n", dir)
				continue
			}
			out = append(out, dir)
		}
		return out, nil
	}
	if len(args) > 0 {
		return args, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return []string{wd}, nil
}

func newVSCodeInstallCmd(all *bool) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install [dir...]",
		Short: "Write launch.json and tasks.json",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			dirs, err := vscodeTargets(cfg, args, *all)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				res, err := vscode.Install(cfg, dir, dryRun)
				if err != nil {
					return fmt.Errorf("installing into %s: %w", dir, err)
				}
				printVSCodeResult(res, dryRun)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would change without writing anything")
	return cmd
}

func printVSCodeResult(res vscode.Result, dryRun bool) {
	verb := "wrote"
	if dryRun {
		verb = "would write"
	}
	for _, f := range []vscode.FileResult{res.Launch, res.Tasks} {
		if !f.Changed {
			fmt.Printf("oak-dev: %s already up to date\n", f.Path)
			continue
		}
		fmt.Printf("oak-dev: %s %s\n", verb, f.Path)
		// Only worth a warning when the file is actually about to be
		// rewritten: oak-dev's own generated header is itself a jsonc
		// comment, so an up-to-date file always "had comments" too - that's
		// not new information once nothing is changing.
		if f.HadComments {
			fmt.Printf("oak-dev: warning: %s had comments that could not be preserved through the merge\n", f.Path)
		}
	}
}

func newVSCodeStatusCmd(all *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "status [dir...]",
		Short: "Show whether the generated config is installed and up to date",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			dirs, err := vscodeTargets(cfg, args, *all)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				state, err := vscode.Status(cfg, dir)
				if err != nil {
					return fmt.Errorf("checking %s: %w", dir, err)
				}
				fmt.Printf("%-60s %s\n", dir, state)
			}
			return nil
		},
	}
}

func newVSCodeUninstallCmd(all *bool) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "uninstall [dir...]",
		Short: "Remove oak-dev's own entries from launch.json and tasks.json",
		Long: `Removes oak-dev's own entries from launch.json/tasks.json, deleting either
file outright once nothing but the bare skeleton oak-dev itself would have
written is left in it. Hand-written entries are always kept, but comments are
not - the merge pipeline has no concept of them, so a file with a comment
prompts for confirmation before it's rewritten, unless --yes.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			dirs, err := vscodeTargets(cfg, args, *all)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				launchHasComments, tasksHasComments, err := vscode.WouldDiscardComments(dir)
				if err != nil {
					return fmt.Errorf("checking %s: %w", dir, err)
				}
				if (launchHasComments || tasksHasComments) && !yes {
					if !confirm(fmt.Sprintf(
						"%s/.vscode has hand-written comments that can't be preserved through uninstall. Continue?", dir)) {
						continue
					}
				}
				res, err := vscode.Uninstall(dir)
				if err != nil {
					return fmt.Errorf("uninstalling from %s: %w", dir, err)
				}
				printVSCodeUninstallResult(res)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func printVSCodeUninstallResult(res vscode.UninstallResult) {
	for _, f := range []vscode.UninstallFileResult{res.Launch, res.Tasks} {
		if !f.Found {
			continue
		}
		if f.Removed {
			fmt.Printf("oak-dev: removed %s\n", f.Path)
		} else {
			fmt.Printf("oak-dev: removed oak-dev's entries from %s\n", f.Path)
		}
		// Same reasoning as printVSCodeResult's warning: comments in a file
		// oak-dev merges/rewrites don't survive the jsonc parse.
		if f.HadComments {
			fmt.Printf("oak-dev: warning: %s had comments that could not be preserved\n", f.Path)
		}
	}
}
