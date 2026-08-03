package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"oak-dev/internal/skill"
)

// newSkillCmd is the one command that must work outside an
// oakestra-macos-testing checkout - most sessions that want it are editing a
// *different* repo (../oakestra, ../oakestra-net, oakestra-deploy) and just
// need the skill planted there. See wireCompletionInstall/annotationNoConfig
// in root.go for how it opts out of loadConfigInto.
func newSkillCmd() *cobra.Command {
	var global bool
	var target string

	cmd := &cobra.Command{
		Use:     "skill",
		GroupID: groupSetup,
		Short:   "Install the oak-dev Agent Skill for Claude Code and other coding agents",
		Long: `Installs skills/oak-dev - a portable Agent Skill (see https://agentskills.io)
that teaches an AI coding agent to drive this CLI - into whichever
directory your agent(s) actually read skills from:

  .claude/skills/oak-dev   Claude Code
  .agents/skills/oak-dev   Codex, Cursor, Gemini CLI, Copilot, Zed, OpenCode

Unlike every other command here, this works from any directory - it never
needs an oakestra-macos-testing checkout or -C. Run it from wherever you're
actually working: ../oakestra, ../oakestra-net, oakestra-deploy.

The same skill installs without a Go toolchain via
` + "`npx skills add oakestra/oakestra-macos-testing`" + ` - see https://skills.sh.`,
		Example: `  oak-dev skill install                 # this directory, agent(s) auto-detected
  oak-dev skill install --global        # ~/.claude and/or ~/.agents
  oak-dev skill install --target all    # both, regardless of detection
  oak-dev skill status
  oak-dev skill uninstall`,
	}
	cmd.Annotations = map[string]string{annotationNoConfig: "true"}
	cmd.PersistentFlags().BoolVar(&global, "global", false, "act on $HOME instead of the current directory")
	cmd.PersistentFlags().StringVar(&target, "target", "auto", "auto|claude|agents|all - which agent directories to act on")
	cmd.AddCommand(
		newSkillInstallCmd(&global, &target),
		newSkillStatusCmd(&global, &target),
		newSkillUninstallCmd(&global, &target),
	)
	return cmd
}

func resolveTargets(global bool, targetFlag string) ([]skill.Target, error) {
	scope := skill.ScopeProject
	if global {
		scope = skill.ScopeGlobal
	}
	base, err := skill.Base(scope)
	if err != nil {
		return nil, err
	}
	return skill.Resolve(base, targetFlag)
}

// forEachTarget resolves the targets named by global/targetFlag and runs fn
// on each in turn, stopping at the first error.
func forEachTarget(global bool, targetFlag string, fn func(skill.Target) error) error {
	targets, err := resolveTargets(global, targetFlag)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err := fn(t); err != nil {
			return err
		}
	}
	return nil
}

func newSkillInstallCmd(global *bool, target *string) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the skill to .claude/skills and/or .agents/skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := forEachTarget(*global, *target, func(t skill.Target) error {
				if dryRun {
					fmt.Printf("oak-dev: would write skill to %s (%s)\n", t.Dir, t.Agent)
					return nil
				}
				wrote, unchanged, err := skill.Install(t)
				if err != nil {
					return fmt.Errorf("installing to %s: %w", t.Dir, err)
				}
				if len(wrote) == 0 {
					fmt.Printf("oak-dev: %s already up to date (%s)\n", t.Dir, t.Agent)
				} else {
					fmt.Printf("oak-dev: wrote skill to %s (%s) - %d file(s) written, %d unchanged\n",
						t.Dir, t.Agent, len(wrote), len(unchanged))
				}
				return nil
			})
			if err != nil {
				return err
			}
			if !dryRun {
				fmt.Println("oak-dev: start a new agent session for it to take effect.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be written without writing it")
	return cmd
}

func newSkillStatusCmd(global *bool, target *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the skill is installed, and where",
		RunE: func(cmd *cobra.Command, args []string) error {
			return forEachTarget(*global, *target, func(t skill.Target) error {
				state, err := skill.Status(t)
				if err != nil {
					return fmt.Errorf("checking %s: %w", t.Dir, err)
				}
				fmt.Printf("%-7s %-52s %s\n", t.Agent, t.Dir, state)
				return nil
			})
		},
	}
}

func newSkillUninstallCmd(global *bool, target *string) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the installed skill",
		RunE: func(cmd *cobra.Command, args []string) error {
			return forEachTarget(*global, *target, func(t skill.Target) error {
				if err := skill.Uninstall(t); err != nil {
					return fmt.Errorf("removing %s: %w", t.Dir, err)
				}
				fmt.Printf("oak-dev: removed %s (%s)\n", t.Dir, t.Agent)
				return nil
			})
		},
	}
}
