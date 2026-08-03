package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/debugstate"
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
  nodeengine, cross-compile, then restart the process inside the worker without
  netmanager  recreating the container (recreating mints a new node ID and strands
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
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}

			targets, err := reloadSet(cfg, args)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return fmt.Errorf("nothing to reload: live: is empty in oak-dev.yaml. " +
					"Name a component instead (`oak-dev reload scheduler`) and it will be added for you")
			}

			// Naming a component that isn't in scope is a mistake worth
			// reporting; sweeping the whole live set past one is not.
			explicit := len(args) > 0
			for _, c := range targets {
				if err := reloadComponent(cfg, tl, c, image, noLive, explicit); err != nil {
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
		return liveComponents(cfg)
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

func reloadComponent(cfg *config.Config, tl *tools, c components.Component, image, noLive, explicit bool) error {
	inScope := inScopeTargets(cfg, c)
	if len(inScope) == 0 {
		if !explicit {
			fmt.Printf("oak-dev: skipping %s - not in the current scope (%s).\n", c.Name, cfg.Stack)
			return nil
		}
		return fmt.Errorf("%s has no containers in the current scope (%s).\n"+
			"Widen it with --stack full, or bring up the stack it lives in", c.Name, cfg.Stack)
	}

	if image && c.PrebuiltImage {
		return fmt.Errorf("%s has no local build: section - it runs a pinned upstream image, "+
			"so there's nothing for --image to rebuild.\nDrop --image; a plain `oak-dev reload %s` "+
			"already picks up source edits via gunicorn --reload", c.Name, c.Name)
	}

	// An image rebuild replaces the baked copy, so it works whether or not the
	// component is live. Everything else needs the bind-mount to be in place.
	if !image {
		if err := ensureLive(cfg, tl, c, noLive); err != nil {
			return err
		}
	}

	// Renders the .generated/ artifacts and points the compose client at the
	// chain that is actually in effect now - ensureLive above may have just
	// added an overlay to it.
	if err := tl.rebind(cfg); err != nil {
		return err
	}

	if image {
		return reloadViaImage(cfg, tl, c, inScope)
	}

	switch c.Kind {
	case components.KindPython:
		if err := reconcile(cfg, tl, inScope); err != nil {
			return err
		}
		fmt.Printf("oak-dev: %s runs from %s under gunicorn --reload - your edit is already live.\n",
			c.Name, shortSrc(cfg, c))
		return nil
	case components.KindGo:
		return reloadViaBinary(cfg, tl, c, inScope)
	}
	return fmt.Errorf("%s has an unknown kind %q", c.Name, c.Kind)
}

// reconcile brings a container back in line with the declared configuration.
// Compose only recreates when the effective config actually differs, so this
// is a no-op in the normal case and is what undoes `oak-dev debug` - whose
// overrides change the entrypoint or environment, and so survive a plain
// restart.
//
// Skipped for any target with InPlaceRestart set (currently nodeengine and
// netmanager, both living in the worker container): there, a recreate mints a
// new hostname, which makes cluster_manager register a new node ID and
// strands every already-scheduled instance in NODE_SCHEDULED. Coming back
// from `debug nodeengine`/`debug netmanager` therefore needs an explicit
// `oak-dev up`.
func reconcile(cfg *config.Config, tl *tools, targets []components.Target) error {
	for _, t := range targets {
		if len(t.InPlaceRestart) > 0 {
			continue
		}
		if err := tl.compose.Recreate(ref(t)); err != nil {
			return fmt.Errorf("reconciling %s: %w", t.Container, err)
		}
		// Whatever `debug` attached to this container is gone now (that's the
		// point of reconciling against the plain topology), so stop claiming
		// it's still there - or the next `oak-dev debug` on a container this
		// one shares would layer a dead overlay back on.
		if err := forgetDebug(cfg, t.Stack, t.Container); err != nil {
			return err
		}
	}
	return nil
}

// forgetDebug drops the debug-state entries for every component attached in
// stack whose container is the one that was just recreated from the plain
// topology - that recreate carries no override-debug-*.yml, so every debugger
// in that container is gone.
//
// Not only the component being reloaded: nodeengine and netmanager are both
// the `worker` service, so recreating it detaches both. And only this stack:
// `reload sched --stack root` leaves cluster_scheduler, and any debugger on
// it, untouched.
func forgetDebug(cfg *config.Config, stack, container string) error {
	for _, name := range debugstate.InStack(cfg.RepoRoot, stack) {
		c, err := components.Resolve(name)
		if err != nil {
			continue // a component renamed out from under the state file
		}
		for _, t := range c.InStack(stack) {
			if t.Container != container {
				continue
			}
			if err := debugstate.Remove(cfg.RepoRoot, name, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

// reloadViaBinary cross-compiles on the host and swaps the binary underneath a
// running container, without recreating it.
func reloadViaBinary(cfg *config.Config, tl *tools, c components.Component, targets []components.Target) error {
	fmt.Printf("oak-dev: cross-compiling %s for linux/%s...\n", c.Name, cfg.GOARCH)
	if _, err := tl.build.Build(c, build.Options{}); err != nil {
		// Loud on purpose: the container keeps serving the previous binary, so
		// a quiet failure means testing stale code and not knowing it.
		fmt.Printf("\033[41;97m BUILD FAILED \033[0m %s did not compile - the OLD binary is still running (stale).\n", c.Name)
		return err
	}

	// Undo any debug overlay before restarting; a plain restart would keep it.
	if err := reconcile(cfg, tl, targets); err != nil {
		return err
	}

	var inPlace, recreated []components.Target
	for _, t := range targets {
		if len(t.InPlaceRestart) > 0 {
			// Restart the process inside the container, never the container
			// itself. docker-entrypoint.sh supervises it in a loop precisely
			// so this works: a new container means a new hostname, which
			// means cluster_manager registers a new node ID and every
			// instance scheduled to the old one is stuck in NODE_SCHEDULED
			// forever.
			if out, err := tl.compose.Capture(ref(t), t.InPlaceRestart...); err != nil {
				return fmt.Errorf("restarting %s in %s: %w\n%s", c.Name, t.Container, err, out)
			}
			inPlace = append(inPlace, t)
			continue
		}
		if err := tl.compose.Restart(ref(t)); err != nil {
			return fmt.Errorf("restarting %s: %w", t.Container, err)
		}
		recreated = append(recreated, t)
	}

	if len(inPlace) > 0 {
		fmt.Printf("oak-dev: %s restarted in place inside %s (node ID preserved).\n", c.Name, containerList(inPlace))
	}
	if len(recreated) > 0 {
		fmt.Printf("oak-dev: %s restarted on the new binary (%s).\n", c.Name, containerList(recreated))
	}
	return nil
}

// reloadViaImage is the escape hatch for changes no mount can pick up.
func reloadViaImage(cfg *config.Config, tl *tools, c components.Component, targets []components.Target) error {
	// A live Go component's container runs /oak-bin/<binary> from the host
	// mount, which shadows whatever the rebuild just baked into the image. So
	// --image alone would recreate the container onto the *old* host binary and
	// report success - the exact "tested stale code without knowing it" failure
	// reloadViaBinary shouts about. Cross-compile too, so what ends up running
	// is the source that was just edited either way.
	if c.Kind == components.KindGo && cfg.IsLive(c.Name) {
		fmt.Printf("oak-dev: %s is live, so /oak-bin shadows the image - cross-compiling for linux/%s too...\n",
			c.Name, cfg.GOARCH)
		if _, err := tl.build.Build(c, build.Options{}); err != nil {
			fmt.Printf("\033[41;97m BUILD FAILED \033[0m %s did not compile - the OLD binary is still running (stale).\n", c.Name)
			return err
		}
	}

	for _, t := range targets {
		fmt.Printf("oak-dev: rebuilding the %s image...\n", t.Container)
		if err := tl.compose.Build(ref(t)); err != nil {
			return fmt.Errorf("building %s: %w", t.Container, err)
		}
		if err := tl.compose.Recreate(ref(t)); err != nil {
			return fmt.Errorf("recreating %s: %w", t.Container, err)
		}
		// Recreated from the plain topology, exactly like reconcile does, so
		// any debugger that was attached here is gone - and unlike reconcile
		// this path doesn't skip the in-place (worker) targets, so it's the
		// one thing that detaches `debug nodeengine`/`debug netmanager`
		// without an `oak-dev up`.
		if err := forgetDebug(cfg, t.Stack, t.Container); err != nil {
			return err
		}
	}
	fmt.Printf("oak-dev: %s rebuilt and recreated (%s).\n", c.Name, containerList(targets))
	return nil
}

// ensureLive puts a component in the live: set so the running container is
// serving your working tree rather than the baked image. This used to be an
// error telling you to go edit a YAML file and re-run `up`; doing it here is
// what lets `live:` stop being something you have to know about.
func ensureLive(cfg *config.Config, tl *tools, c components.Component, noLive bool) error {
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

	// The live bind-mount is a compose override, so the topology has changed
	// and the container has to be recreated once for it to take effect.
	if err := tl.rebind(cfg); err != nil {
		return err
	}
	if c.Kind == components.KindGo {
		if err := ensureLiveBinaries(cfg, tl); err != nil {
			return err
		}
	}
	for _, t := range inScopeTargets(cfg, c) {
		fmt.Printf("oak-dev: recreating %s from %s...\n", t.Container, shortSrc(cfg, c))
		if err := tl.compose.Recreate(ref(t)); err != nil {
			return fmt.Errorf("recreating %s: %w", t.Container, err)
		}
	}
	return nil
}
