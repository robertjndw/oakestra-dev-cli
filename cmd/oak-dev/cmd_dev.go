package main

import (
	"context"
	"fmt"
	"io"
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
	"oak-dev/internal/watch"
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
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}
			if testMode != "" && testMode != "smoke" {
				return fmt.Errorf("--test only supports 'smoke' (never the full suite: the deployment tests are order-dependent and share state)")
			}

			// Bring the stack up (or just render it, with --no-up) before
			// resolving/promoting watched components: on a fresh checkout,
			// promoting a worker-stack component (e.g. `oak-dev dev ne`) has
			// to recreate the worker container, and worker.yml's network is
			// `external: true` - it doesn't exist until the root/cluster
			// stacks have created it. Doing this first, rather than inside
			// watchedComponents, avoids a "network oakestra not found" error
			// on the very first run.
			if noUp {
				fmt.Println("oak-dev: " + cfg.ScopeLine())
				if err := ensureLiveBinaries(cfg, tl); err != nil {
					return err
				}
				if err := tl.rebind(cfg); err != nil {
					return err
				}
			} else if _, err := runUp(cfg, tl, true); err != nil {
				return err
			}

			watched, err := watchedComponents(cfg, tl, args)
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
			sources = append(sources, stackLogSources(tl, "log:", 20)...)
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
func watchedComponents(cfg *config.Config, tl *tools, args []string) ([]components.Component, error) {
	if len(args) > 0 {
		var out []components.Component
		for _, a := range args {
			c, err := components.Resolve(a)
			if err != nil {
				return nil, err
			}
			if err := ensureLive(cfg, tl, c, false); err != nil {
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
func stackLogSources(tl *tools, prefix string, tail int) []multilog.Source {
	var out []multilog.Source
	for _, stack := range tl.compose.Stacks() {
		spec, err := tl.compose.StackLogSpec(stack, compose.LogOpts{Tail: tail, Follow: true})
		if err != nil {
			continue
		}
		out = append(out, multilog.Source{
			Tag: prefix + stack, Dir: spec.Dir, Env: spec.Env,
			Name: spec.Name, Args: spec.Args,
		})
	}
	return out
}

// watchSources returns one file-watcher-backed source per watched Go
// component: on every source change it re-execs `<self> reload <component>`,
// guarding the worker while a test run is in progress and optionally
// re-running the smoke suite after a successful rebuild.
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
		// Any component living in the worker container (nodeengine,
		// netmanager) must not be touched mid-suite: restarting either one
		// disrupts in-flight deployments, and recreating the container mints
		// a new node ID that strands scheduled instances (see CLAUDE.md).
		guarded := len(c.InStack(components.StackWorker)) > 0

		out = append(out, multilog.Source{
			Tag: "watch:" + c.Name, Dir: srcDir,
			Func: func(ctx context.Context, w io.Writer) error {
				return watch.Run(ctx, watch.Options{
					Dir: srcDir,
					Log: func(msg string) { fmt.Fprintln(w, msg) },
				}, func() {
					reloadOnChange(ctx, w, cfg, c, self, testMode, guarded)
				})
			},
		})
	}
	return out
}

// reloadOnChange runs once a debounced source change fires for component c:
// it skips the reload while a guarded (worker-resident) component's mid-suite
// restart would strand scheduled instances, otherwise cross-compiles/restarts
// it via `<self> reload <c>` and optionally re-runs the smoke suite.
func reloadOnChange(ctx context.Context, w io.Writer, cfg *config.Config, c components.Component, self, testMode string, guarded bool) {
	if guarded && testsuite.IsLocked(cfg) {
		fmt.Fprintf(w, "oak-dev: skipping %s reload - oak-dev test is running (never restart the worker mid-suite)\n", c.Name)
		return
	}
	// A half-swapped binary is worse than a slightly delayed exit, so Ctrl-C
	// lets an in-flight reload finish rather than killing it mid-rebuild or
	// mid-`docker restart`; multilog.Run's shutdown grace period bounds how
	// long that can take.
	reloadCtx := context.WithoutCancel(ctx)
	if err := runTagged(reloadCtx, w, cfg, self, "reload", c.Name); err != nil {
		return // runTagged already reported why; don't chain the smoke suite
	}
	if testMode == "smoke" {
		_ = runTagged(reloadCtx, w, cfg, self, "test", "--smoke")
	}
}

// runTagged re-execs oak-dev itself with args, streaming its combined
// stdout/stderr into w (`reload`/`test --smoke` already narrate their own
// failures - e.g. the BUILD FAILED banner - so the only thing the caller needs
// back is whether it succeeded).
//
// The child is pinned to this run's repo root and scope with -C/--stack rather
// than inheriting them from the process environment: the watcher's working
// directory is the *source* checkout ($OAKESTRA_REPO), where a bare `oak-dev
// reload` would fail its "is this an oakestra-macos-testing checkout" test, and
// --stack keeps a `dev --stack root` session from reloading through whatever
// scope .generated/stack happens to hold.
func runTagged(ctx context.Context, w io.Writer, cfg *config.Config, self string, args ...string) error {
	full := append([]string{"-C", cfg.RepoRoot, "--stack", cfg.Stack}, args...)
	cmd := exec.CommandContext(ctx, self, full...)
	cmd.Dir = cfg.RepoRoot
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
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
