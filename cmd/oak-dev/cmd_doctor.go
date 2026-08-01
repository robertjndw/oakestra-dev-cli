package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"oak-dev/internal/doctor"
	"oak-dev/internal/testsuite"
)

func newDoctorCmd() *cobra.Command {
	var fix bool

	cmd := &cobra.Command{
		Use:     "doctor",
		GroupID: groupSetup,
		Short:   "Check prerequisites and bootstrap the workspace",
		Long: `Runs the preflight checks and prints a fix for anything that fails. Checks
marked (optional) only gate one command, so they don't fail the run.

--fix repairs what it can rather than only telling you how:

  the pytest venv           created if missing (always, with or without --fix)
  proto/*_pb2.py            generated into your oakestra checkout
  the oak CLI configuration pointed at this local stack

Both of those last two write outside this repo, so --fix names every file and
setting it touches as it goes.`,
		Example: `  oak-dev doctor
  oak-dev doctor --fix`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)

			fmt.Println("oak-dev: ensuring the pytest venv...")
			if err := testsuite.EnsureVenv(cfg); err != nil {
				fmt.Fprintln(os.Stderr, "  venv setup failed:", err)
			}

			pending := 0
			for _, f := range doctor.Fixers() {
				if !f.Needed(cfg) {
					continue
				}
				pending++
				if !fix {
					continue
				}
				fmt.Printf("\noak-dev: %s...\n", f.Name)
				if err := f.Run(cfg); err != nil {
					fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
				}
			}
			if fix {
				for _, f := range doctor.Fixers() {
					if f.Needed(cfg) || f.Skip == nil {
						continue
					}
					if why := f.Skip(cfg); why != "" {
						fmt.Printf("\noak-dev: skipping %q - %s\n", f.Name, why)
					}
				}
			}

			fmt.Println()
			failed := 0
			for _, r := range doctor.Run(cfg) {
				mark := "\033[32m✓\033[0m"
				if !r.OK {
					mark = "\033[31m✗\033[0m"
					if !r.Optional {
						failed++
					}
				}
				optTag := ""
				if r.Optional {
					optTag = " (optional)"
				}
				fmt.Printf("%s %-30s%s %s\n", mark, r.Name, optTag, r.Detail)
				if !r.OK && r.Fix != "" {
					fmt.Printf("    fix: %s\n", r.Fix)
				}
			}

			if pending > 0 && !fix {
				fmt.Printf("\noak-dev: %d bootstrap action(s) pending - run `oak-dev doctor --fix`.\n", pending)
			}
			if failed > 0 {
				return fmt.Errorf("%d required check(s) failed", failed)
			}
			fmt.Println("\noak-dev: all required checks passed.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "repair what can be repaired automatically, then re-check")
	return cmd
}
