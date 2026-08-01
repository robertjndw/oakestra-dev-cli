package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/multilog"
	"oak-dev/internal/testsuite"
	"oak-dev/internal/topology"
)

func newDevCmd() *cobra.Command {
	var testMode string
	var noUp bool

	cmd := &cobra.Command{
		Use:     "dev [component...]",
		GroupID: groupCode,
		Short:   "Start the stack, watch for edits, stream all logs",
		Long: `The one command to start working.

Brings up the stack if it isn't running, then aggregates the logs of every
stack in scope into a single prefixed stream and watches the source of every
live Go component, cross-compiling and restarting it in place on each save.
Live Python services need no watcher - gunicorn --reload already handles them.

Naming components restricts the watchers to those; logs still cover the whole
scope. The worker is skipped automatically while ` + "`oak-dev test`" + ` is running,
because restarting it mid-suite strands scheduled instances on a stale node ID.

Ctrl-C stops everything.`,
		Example: `  oak-dev dev                  # everything
  oak-dev dev sched            # only watch the scheduler
  oak-dev dev --no-up          # attach to an already-running stack
  oak-dev dev --test smoke     # re-run the smoke suite after each rebuild`,
		ValidArgsFunction: completeComponents,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			if testMode != "" && testMode != "smoke" {
				return fmt.Errorf("--test only supports 'smoke' (never the full suite: the deployment tests are order-dependent and share state)")
			}

			if _, err := exec.LookPath("watchexec"); err != nil {
				return fmt.Errorf("watchexec not found on PATH - required for `oak-dev dev`.\n" +
					"Install it (brew install watchexec) or run `oak-dev doctor`")
			}

			// Bring the stack up (or just render it, with --no-up) before
			// resolving/promoting watched components: on a fresh checkout,
			// promoting a worker-stack component (e.g. `oak-dev dev ne`) has
			// to recreate the worker container, and worker.yml's network is
			// `external: true` - it doesn't exist until the root/cluster
			// stacks have created it. Doing this first, rather than inside
			// watchedComponents, avoids a "network oakestra not found" error
			// on the very first run.
			var files map[string][]string
			var err error
			if noUp {
				fmt.Println("oak-dev: " + cfg.ScopeLine())
				if err := ensureLiveBinaries(cfg); err != nil {
					return err
				}
				files, err = topology.Render(cfg)
				if err != nil {
					return err
				}
			} else {
				files, err = runUp(cfg, true)
				if err != nil {
					return err
				}
			}

			watched, err := watchedComponents(cfg, args)
			if err != nil {
				return err
			}

			self, err := os.Executable()
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			var sources []multilog.Source
			sources = append(sources, stackLogSources(files, cfg, "log:", 20)...)
			sources = append(sources, watchSources(cfg, watched, self, testMode)...)

			printDevSummary(cfg, watched)
			return multilog.Run(ctx, sources)
		},
	}
	cmd.Flags().StringVar(&testMode, "test", "", "re-run tests after a successful rebuild: 'smoke' (never the full suite)")
	cmd.Flags().BoolVar(&noUp, "no-up", false, "don't start the stack first, just attach to it")
	return cmd
}

// watchedComponents returns the Go components to watch: those named on the
// command line, or every live one. Named components that aren't live are
// promoted, so `oak-dev dev sched` works without editing oak-dev.yaml first.
func watchedComponents(cfg *config.Config, args []string) ([]components.Component, error) {
	if len(args) > 0 {
		var out []components.Component
		for _, a := range args {
			c, err := components.Resolve(a)
			if err != nil {
				return nil, err
			}
			if err := ensureLive(cfg, c, false); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}

	return liveComponents(cfg)
}

// stackLogSources returns one `docker compose logs -f` per stack in scope,
// from an already-rendered topology. Shared with `oak-dev logs`, which
// previously had its own near-identical copy that differed only in tail
// length and tag prefix.
func stackLogSources(files map[string][]string, cfg *config.Config, prefix string, tail int) []multilog.Source {
	var out []multilog.Source
	for _, stack := range upOrder {
		f, ok := files[stack]
		if !ok {
			continue
		}
		args := append([]string{"compose"}, compose.FileArgs(f)...)
		args = append(args, "logs", "-f", fmt.Sprintf("--tail=%d", tail))
		out = append(out, multilog.Source{
			Tag: prefix + stack, Dir: cfg.RepoRoot, Env: compose.Env(cfg),
			Name: "docker", Args: args,
		})
	}
	return out
}

// watchSources returns one watchexec-backed source per watched Go component:
// on every source change it re-execs `<self> reload <component>`, guarding the
// worker while a test run is in progress and optionally re-running the smoke
// suite after a successful rebuild.
func watchSources(cfg *config.Config, watched []components.Component, self, testMode string) []multilog.Source {
	var out []multilog.Source
	for _, c := range watched {
		if c.Kind != components.KindGo {
			continue // Python reloads itself via gunicorn --reload.
		}
		if len(inScopeTargets(cfg, c)) == 0 {
			continue
		}

		srcDir := cfg.SourceDir(c)
		inner := fmt.Sprintf("%q reload %q", self, c.Name)
		// Any component living in the worker container (nodeengine,
		// netmanager) must not be touched mid-suite: restarting either one
		// disrupts in-flight deployments, and recreating the container mints
		// a new node ID that strands scheduled instances (see CLAUDE.md).
		if len(c.InStack(components.StackWorker)) > 0 {
			lock := testsuite.LockPath(cfg)
			inner = fmt.Sprintf("if [ -f %q ]; then echo 'oak-dev: skipping %s reload - oak-dev test is running (never restart the worker mid-suite)'; else %s; fi", lock, c.Name, inner)
		}
		if testMode == "smoke" {
			inner = fmt.Sprintf("%s && %q test --smoke", inner, self)
		}

		out = append(out, multilog.Source{
			Tag: "watch:" + c.Name, Dir: srcDir, Env: os.Environ(),
			Name: "watchexec",
			Args: []string{"-w", srcDir, "-e", "go", "--", "sh", "-c", inner},
		})
	}
	return out
}

func printDevSummary(cfg *config.Config, watched []components.Component) {
	fmt.Println("oak-dev: watching and aggregating logs, Ctrl-C to stop")

	watching := map[string]bool{}
	for _, c := range watched {
		watching[c.Name] = true
	}

	// cfg.LiveNames() rather than ranging the map, so this list doesn't
	// reorder itself between runs.
	for _, name := range cfg.LiveNames() {
		c, err := components.Resolve(name)
		if err != nil {
			continue
		}
		switch {
		case c.Kind == components.KindPython:
			fmt.Printf("  %-28s python, gunicorn --reload picks up saves\n", name)
		case watching[name]:
			fmt.Printf("  %-28s go, watched: save -> rebuild -> in-place restart\n", name)
		default:
			fmt.Printf("  %-28s go, live but not watched this run\n", name)
		}
	}
}
