package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/testsuite"
)

func newTestCmd() *cobra.Command {
	var smoke, noUp bool

	cmd := &cobra.Command{
		Use:     "test [component] [-- args...]",
		GroupID: groupCode,
		Short:   "Run the end-to-end suite, or one component's unit tests",
		Long: `With no argument, runs the pytest end-to-end suite against the stack,
starting it first if it isn't already running. A green run means the whole
chain worked: REST API -> root scheduler -> cluster scheduler -> MQTT ->
NodeEngine -> containerd, with a real workload deployed inside the worker.

With a component, runs that component's own ` + "`go test ./...`" + ` on the host - no
Docker, no stack, about two seconds. Only Go components have unit tests here
(` + components.GoNames() + `).

Anything after -- goes straight to pytest or go test.`,
		Example: `  oak-dev test                          # the full E2E suite
  oak-dev test --smoke                  # health + registration only
  oak-dev test sched                    # go test ./... for the scheduler
  oak-dev test sched -- -run TestBestFit
  oak-dev test -- tests/test_03_deployment.py -k scale`,
		// Only the arguments before `--` are ours; everything after belongs to
		// pytest or go test. cobra.MaximumNArgs would count both.
		Args: func(cmd *cobra.Command, args []string) error {
			if n := ownArgCount(cmd, args); n > 1 {
				return fmt.Errorf("expected at most one component, got %d: %s",
					n, joinNames(args[:n]))
			}
			return nil
		},
		ValidArgsFunction: completeGoComponents,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}

			own := ownArgCount(cmd, args)
			passthrough := args[own:]

			if own > 0 {
				c, err := components.Resolve(args[0])
				if err != nil {
					return err
				}
				if c.Kind != components.KindGo {
					return fmt.Errorf("%s is a Python service - it has no unit tests here, it's covered by the E2E suite.\n"+
						"Components with unit tests: %s", c.Name, components.GoNames())
				}
				if smoke {
					return fmt.Errorf("--smoke applies to the E2E suite, not to `oak-dev test %s`", c.Name)
				}
				fmt.Printf("oak-dev: go test ./... in %s\n", shortSrc(cfg, c))
				return tl.build.UnitTest(c, passthrough...)
			}

			// Every stack has to be in scope before anything is started: the
			// suite drives the root API end to end (even --smoke checks the
			// root, the cluster and worker registration), so a narrowed scope
			// can only fail, and it would fail as an opaque connection timeout
			// minutes into a run rather than immediately.
			if missing := stacksOutOfScope(cfg); len(missing) > 0 {
				return fmt.Errorf("the E2E suite needs the root, cluster and worker stacks, but %s leaves out %s.\n"+
					"Re-run as `oak-dev test --stack full`, or `oak-dev up --stack full` first",
					cfg.ScopeLine(), joinNames(missing))
			}

			if !noUp {
				if _, err := runUp(cfg, tl, true); err != nil {
					return err
				}
			}

			// The lock tells `oak-dev dev`'s watchers to leave the worker alone:
			// restarting it mid-suite mints a new node ID and strands every
			// already-scheduled instance in NODE_SCHEDULED forever.
			if err := testsuite.Lock(cfg); err != nil {
				return err
			}
			defer testsuite.Unlock(cfg)

			return tl.tests.Run(smoke, passthrough...)
		},
	}
	cmd.Flags().BoolVar(&smoke, "smoke", false, "health + registration only, skip the deployment tests")
	cmd.Flags().BoolVar(&noUp, "no-up", false, "don't start the stack first (fail if it isn't running)")
	return cmd
}

// completeGoComponents offers only the components that actually have unit
// tests, since those are the only valid arguments to `test`.
func completeGoComponents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, cand := range components.Complete(toComplete) {
		name, _, _ := strings.Cut(cand, "\t")
		if c, err := components.Resolve(name); err == nil && c.Kind == components.KindGo {
			out = append(out, cand)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
