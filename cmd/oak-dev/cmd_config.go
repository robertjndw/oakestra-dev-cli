package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"oak-dev/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		GroupID: groupSetup,
		Short:   "Read or change oak-dev.yaml settings from the command line",
		Long: `With no subcommand, prints every setting oak-dev resolved: from oak-dev.yaml,
.env, or a built-in default.

` + "`get`" + ` prints one resolved value; ` + "`set`" + ` writes into oak-dev.yaml, preserving
its comments and formatting - the same file ` + "`oak-dev reload`" + `/` + "`debug`" + ` edit
themselves when they add a component to live:.

.env and the process environment take priority over oak-dev.yaml for
oakestra_repo, cluster.name, cluster.location, versions.netmanager and
versions.lib_branch (see .env.example); ` + "`set`" + ` warns when that means the
write won't take effect until the override is removed. The sticky stack scope
left behind by an earlier ` + "`up`" + `/` + "`down --stack`" + ` similarly outranks stack: -
` + "`set stack`" + ` warns about that too.

Keys not covered here - most often ` + "`live:`" + `, which ` + "`reload`" + `/` + "`debug`" + ` manage
themselves - are still editable by hand in oak-dev.yaml.`,
		Example: `  oak-dev config
  oak-dev config get cluster.location
  oak-dev config set workers 3
  oak-dev config set cluster.location "37.7749,-122.4194,15"
  oak-dev config set profiles.dashboard true`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown config subcommand %q - use `get`/`set`, or no argument to list everything", args[0])
			}
			printAllConfig(cfgFrom(cmd))
			return nil
		},
	}
	cmd.AddCommand(newConfigGetCmd(), newConfigSetCmd())
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "get <key>",
		Short:             "Print one resolved setting",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := cfgFrom(cmd).Get(args[0])
			if err != nil {
				return err
			}
			fmt.Println(v)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "set <key> <value>",
		Short:             "Write one setting into oak-dev.yaml",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			key, value := args[0], args[1]

			envOverride, err := cfg.Set(key, value)
			if err != nil {
				return err
			}
			fmt.Printf("oak-dev: %s = %s (written to oak-dev.yaml)\n", key, value)

			if key == "stack" {
				if sticky, ok := config.StickyStack(cfg.RepoRoot); ok && sticky != value {
					fmt.Printf("oak-dev: an earlier `up`/`down --stack` pinned the active scope to %q, which "+
						"outranks stack: - run `oak-dev down` to clear it.\n", sticky)
				}
			}
			if envOverride != "" {
				fmt.Printf("oak-dev: %s is set in the environment or .env and takes priority over oak-dev.yaml - "+
					"unset it for this change to take effect.\n", envOverride)
			}
			return nil
		},
	}
}

func completeConfigKeys(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, k := range config.ConfigKeys() {
		if strings.HasPrefix(k, toComplete) {
			out = append(out, k)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func printAllConfig(cfg *config.Config) {
	fmt.Println("oak-dev.yaml settings (resolved from oak-dev.yaml, .env, or a default):")
	for _, k := range config.ConfigKeys() {
		v, err := cfg.Get(k)
		if err != nil {
			continue
		}
		fmt.Printf("  %-24s %s\n", k, v)
	}
}
