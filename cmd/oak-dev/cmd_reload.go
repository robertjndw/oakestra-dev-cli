package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/topology"
)

func newReloadCmd() *cobra.Command {
	var image, noLive bool

	cmd := &cobra.Command{
		Use:     "reload [component...]",
		GroupID: groupCode,
		Short:   "Apply your code changes to the running stack",
		Long: `Makes what you just edited take effect, choosing the cheapest mechanism that
works for the component:

  Python    already bind-mounted and running under gunicorn --reload - nothing to do
  scheduler cross-compile for linux/<arch>, then restart the container in place
  nodeengine cross-compile, then restart nodeengined inside the worker without
            recreating the container (recreating mints a new node ID and strands
            scheduled instances - see CLAUDE.md)

Use --image when the change is one no mount can pick up: requirements.txt, a
Dockerfile, or go.mod. That rebuilds the image and recreates the container.

With no arguments, reloads every component in live:.`,
		Example: `  oak-dev reload                    # everything in live:
  oak-dev reload sched              # alias for scheduler
  oak-dev reload cluster_manager
  oak-dev reload ne                 # nodeengine, in place
  oak-dev reload sm --image         # after editing requirements.txt`,
		// Old spellings, so muscle memory lands on a suggestion rather than a
		// bare "unknown command".
		SuggestFor:        []string{"go", "rebuild", "build", "sync", "apply", "restart"},
		ValidArgsFunction: completeComponents,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)

			targets, err := reloadSet(cfg, args)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return fmt.Errorf("nothing to reload: live: is empty in oak-dev.yaml.\n" +
					"Name a component instead (`oak-dev reload scheduler`) and it will be added for you.")
			}

			// Naming a component that isn't in scope is a mistake worth
			// reporting; sweeping the whole live set past one is not.
			explicit := len(args) > 0
			for _, c := range targets {
				if err := reloadComponent(cfg, c, image, noLive, explicit); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&image, "image", false, "rebuild the container image (for requirements.txt/Dockerfile/go.mod changes)")
	cmd.Flags().BoolVar(&noLive, "no-live", false, "fail instead of adding the component to oak-dev.yaml's live: list")
	return cmd
}

// reloadSet resolves the component arguments, defaulting to the live set.
func reloadSet(cfg *config.Config, args []string) ([]components.Component, error) {
	if len(args) == 0 {
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

	var out []components.Component
	for _, a := range args {
		c, err := components.Resolve(a)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func reloadComponent(cfg *config.Config, c components.Component, image, noLive, explicit bool) error {
	inScope := inScopeTargets(cfg, c)
	if len(inScope) == 0 {
		if !explicit {
			fmt.Printf("oak-dev: skipping %s - not in the current scope (%s).\n", c.Name, cfg.Stack)
			return nil
		}
		return fmt.Errorf("%s has no containers in the current scope (%s).\n"+
			"Widen it with --stack full, or bring up the stack it lives in", c.Name, cfg.Stack)
	}

	// An image rebuild replaces the baked copy, so it works whether or not the
	// component is live. Everything else needs the bind-mount to be in place.
	if !image {
		if err := ensureLive(cfg, c, noLive); err != nil {
			return err
		}
	}

	files, err := topology.Render(cfg)
	if err != nil {
		return err
	}

	if image {
		return reloadViaImage(cfg, c, inScope, files)
	}

	switch c.Kind {
	case components.KindPython:
		if err := reconcile(cfg, c, inScope, files); err != nil {
			return err
		}
		fmt.Printf("oak-dev: %s runs from %s under gunicorn --reload - your edit is already live.\n",
			c.Name, shortSrc(cfg, c))
		return nil
	case components.KindGo:
		return reloadViaBinary(cfg, c, inScope, files)
	}
	return fmt.Errorf("%s has an unknown kind %q", c.Name, c.Kind)
}

// reconcile brings a container back in line with the declared configuration.
// Compose only recreates when the effective config actually differs, so this
// is a no-op in the normal case and is what undoes `oak-dev debug` - whose
// overrides change the entrypoint or environment, and so survive a plain
// restart.
//
// Skipped for the worker: there, a recreate mints a new hostname, which makes
// cluster_manager register a new node ID and strands every already-scheduled
// instance in NODE_SCHEDULED. Coming back from `debug nodeengine` therefore
// needs an explicit `oak-dev up`.
func reconcile(cfg *config.Config, c components.Component, targets []components.Target, files map[string][]string) error {
	if c.Name == components.NodeEngineName {
		return nil
	}
	for _, t := range targets {
		f := files[t.Stack]
		if f == nil {
			continue
		}
		if err := compose.Run(cfg, f, "up", "-d", t.Container); err != nil {
			return fmt.Errorf("reconciling %s: %w", t.Container, err)
		}
	}
	return nil
}

// reloadViaBinary cross-compiles on the host and swaps the binary underneath a
// running container, without recreating it.
func reloadViaBinary(cfg *config.Config, c components.Component, targets []components.Target, files map[string][]string) error {
	fmt.Printf("oak-dev: cross-compiling %s for linux/%s...\n", c.Name, cfg.GOARCH)
	if _, err := build.Build(cfg, c, build.Options{}); err != nil {
		// Loud on purpose: the container keeps serving the previous binary, so
		// a quiet failure means testing stale code and not knowing it.
		fmt.Printf("\033[41;97m BUILD FAILED \033[0m %s did not compile - the OLD binary is still running (stale).\n", c.Name)
		return err
	}

	// Undo any debug overlay before restarting; a plain restart would keep it.
	if err := reconcile(cfg, c, targets, files); err != nil {
		return err
	}

	for _, t := range targets {
		f := files[t.Stack]
		if f == nil {
			continue
		}
		if c.Name == components.NodeEngineName {
			// Restart the daemon, never the container. docker-entrypoint.sh
			// supervises nodeengined in a loop precisely so this works: a new
			// container means a new hostname, which means cluster_manager
			// registers a new node ID and every instance scheduled to the old
			// one is stuck in NODE_SCHEDULED forever.
			if out, err := compose.Output(cfg, f, "exec", "-T", t.Container, "pkill", "nodeengined"); err != nil {
				return fmt.Errorf("restarting nodeengined in %s: %w\n%s", t.Container, err, out)
			}
			fmt.Printf("oak-dev: nodeengined restarted in place (node ID preserved).\n")
			continue
		}
		if err := compose.Run(cfg, f, "restart", t.Container); err != nil {
			return fmt.Errorf("restarting %s: %w", t.Container, err)
		}
	}

	if c.Name != components.NodeEngineName {
		fmt.Printf("oak-dev: %s restarted on the new binary (%s).\n", c.Name, containerList(targets))
	}
	return nil
}

// reloadViaImage is the escape hatch for changes no mount can pick up.
func reloadViaImage(cfg *config.Config, c components.Component, targets []components.Target, files map[string][]string) error {
	for _, t := range targets {
		f := files[t.Stack]
		if f == nil {
			continue
		}
		fmt.Printf("oak-dev: rebuilding the %s image...\n", t.Container)
		if err := compose.Run(cfg, f, "build", t.Container); err != nil {
			return fmt.Errorf("building %s: %w", t.Container, err)
		}
		if err := compose.Run(cfg, f, "up", "-d", t.Container); err != nil {
			return fmt.Errorf("recreating %s: %w", t.Container, err)
		}
	}
	fmt.Printf("oak-dev: %s rebuilt and recreated (%s).\n", c.Name, containerList(targets))
	return nil
}

// ensureLive puts a component in the live: set so the running container is
// serving your working tree rather than the baked image. This used to be an
// error telling you to go edit a YAML file and re-run `up`; doing it here is
// what lets `live:` stop being something you have to know about.
func ensureLive(cfg *config.Config, c components.Component, noLive bool) error {
	if cfg.IsLive(c.Name) {
		return nil
	}
	if noLive {
		return fmt.Errorf("%s is not in oak-dev.yaml's live: list, so the container is running its baked image.\n"+
			"Drop --no-live to have oak-dev add it, or add it by hand and re-run `oak-dev up`", c.Name)
	}

	changed, err := cfg.AddLive(c.Name)
	if err != nil {
		return fmt.Errorf("adding %s to live:: %w", c.Name, err)
	}
	if changed {
		fmt.Printf("oak-dev: %s was not live - added it to oak-dev.yaml.\n  live: [%s]\n",
			c.Name, joinNames(cfg.LiveNames()))
	}

	// The live bind-mount is a compose override, so the container has to be
	// recreated once for it to take effect.
	files, err := topology.Render(cfg)
	if err != nil {
		return err
	}
	if c.Kind == components.KindGo {
		if err := ensureLiveBinaries(cfg); err != nil {
			return err
		}
	}
	for _, t := range inScopeTargets(cfg, c) {
		f := files[t.Stack]
		if f == nil {
			continue
		}
		fmt.Printf("oak-dev: recreating %s from %s...\n", t.Container, shortSrc(cfg, c))
		if err := compose.Run(cfg, f, "up", "-d", t.Container); err != nil {
			return fmt.Errorf("recreating %s: %w", t.Container, err)
		}
	}
	return nil
}
