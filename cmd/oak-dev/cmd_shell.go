package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/target"
	"oak-dev/internal/topology"
)

func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "shell <target> [-- cmd...]",
		Aliases: []string{"sh"},
		GroupID: groupInspect,
		Short:   "Get a prompt inside a container",
		Long: `Opens an interactive shell in a container, preferring bash and falling back to
sh for the alpine-based images that don't have it.

The target can be a component by name, alias or prefix, a raw container name,
or one of the named endpoints, which open the right client instead of a shell:

  mongo-root      mongosh against the root mongo (port 10007)
  mongo-cluster   mongosh against the cluster mongo (port 10107)

Anything after -- runs instead of the shell.`,
		Example: `  oak-dev shell worker
  oak-dev shell cm                     # the cluster_manager container
  oak-dev shell mongo-root             # mongosh, not a shell
  oak-dev shell worker -- ctr -n oakestra containers ls`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)

			targets, err := target.Resolve(cfg, args[0])
			if err != nil {
				return err
			}
			if len(targets) > 1 {
				names := make([]string, len(targets))
				for i, t := range targets {
					names[i] = t.Container
				}
				return fmt.Errorf("%q names %d containers (%s) - shell needs exactly one",
					args[0], len(targets), joinNames(names))
			}
			t := targets[0]

			files, err := topology.Render(cfg)
			if err != nil {
				return err
			}
			f, ok := files[t.Stack]
			if !ok {
				return fmt.Errorf("the %s stack is not in the current scope (%s)", t.Stack, cfg.Stack)
			}

			// An explicit command wins, then the endpoint's own client, then a shell.
			switch {
			case len(args) > 1:
				return execIn(cfg, f, t.Container, args[1:])
			case len(t.Exec) > 0:
				return execIn(cfg, f, t.Container, t.Exec)
			default:
				return execIn(cfg, f, t.Container, []string{shellIn(cfg, f, t.Container)})
			}
		},
	}
	return cmd
}

// shellIn picks bash if the image has it, sh otherwise. Probing beats trying
// bash and falling back on error: a bash session the user exits non-zero would
// otherwise be mistaken for "bash is missing" and re-run as sh.
func shellIn(cfg *config.Config, files []string, container string) string {
	if _, err := compose.Output(cfg, files, "exec", "-T", container, "which", "bash"); err == nil {
		return "bash"
	}
	return "sh"
}

func execIn(cfg *config.Config, files []string, container string, command []string) error {
	args := append([]string{"exec", container}, command...)
	return compose.Run(cfg, files, args...)
}
