package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/debugstate"
	"oak-dev/internal/topology"
)

func newUpCmd() *cobra.Command {
	var workers int

	cmd := &cobra.Command{
		Use:     "up",
		GroupID: groupStack,
		Short:   "Build and start the stack",
		Long: `Starts the stacks in the current scope, building images if needed.

The scope you pass with --stack becomes sticky: it is recorded in
.generated/stack and reused by later commands, so you don't have to repeat
--stack worker on every invocation. A plain ` + "`oak-dev down`" + ` clears it.

Every command prints the scope it is operating on, and where that scope came
from.`,
		Example: `  oak-dev up                     # whatever oak-dev.yaml says
  oak-dev up --stack worker      # cluster + worker only (the worker needs a cluster)
  oak-dev up --workers 3         # start with three dockerized workers`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			if workers > 0 {
				cfg.Workers = workers
			}
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}
			_, err = runUp(cfg, tl, true)
			return err
		},
	}
	cmd.Flags().IntVar(&workers, "workers", 0, "number of dockerized workers to run")
	return cmd
}

// runUp brings up every stack in scope and returns the compose file chains it
// rendered to do so, so callers that need to act on the same topology
// afterward (`dev`'s log sources) don't have to render it again. Shared with
// `dev` and `test`, which both start the stack themselves rather than failing
// against a stopped one.
func runUp(cfg *config.Config, tl *tools, sticky bool) (map[string][]string, error) {
	fmt.Println("oak-dev: " + cfg.ScopeLine())

	if sticky {
		// Persist the scope we're actually bringing up, so a later plain
		// `oak-dev reload/debug/status` stays consistent with what's running
		// instead of falling back to oak-dev.yaml's static stack: and trying
		// to touch a stack that was never started.
		if err := config.SetLastStack(cfg.RepoRoot, cfg.Stack); err != nil {
			return nil, err
		}
	}

	if err := ensureLiveBinaries(cfg, tl); err != nil {
		return nil, err
	}
	files, err := topology.Render(cfg)
	if err != nil {
		return nil, err
	}

	for _, stack := range tl.compose.Stacks() {
		var extra []string
		if stack == components.StackWorker && cfg.Workers > 1 {
			extra = append(extra, "--scale", fmt.Sprintf("worker=%d", cfg.Workers))
		}
		fmt.Printf("oak-dev: starting %s stack...\n", stack)
		if err := tl.compose.StackUp(stack, extra...); err != nil {
			return nil, fmt.Errorf("%s stack: %w", stack, err)
		}
		// This stack was just recreated from the plain topology, which carries
		// no override-debug-*.yml, so any debugger attached in it is gone.
		// Only this stack's entries, and only now that the recreate actually
		// succeeded: `up --stack root` leaves a debugged worker running, and
		// forgetting it would make the next `debug netmanager` drop
		// nodeengine's overlay.
		if err := debugstate.ClearStacks(cfg.RepoRoot, stack); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func newDownCmd() *cobra.Command {
	var volumes, yes bool

	cmd := &cobra.Command{
		Use:     "down",
		GroupID: groupStack,
		Short:   "Stop the stack, optionally wiping its data",
		Long: `Stops the stacks in scope, in reverse start order.

--volumes additionally deletes the MongoDB, Redis and containerd volumes: a
genuinely fresh start rather than a restart. It prompts first unless --yes.

A plain ` + "`oak-dev down`" + ` (no --stack) also clears the sticky scope, so the
next ` + "`oak-dev up`" + ` goes back to oak-dev.yaml's default instead of silently
staying narrowed.`,
		Example: `  oak-dev down
  oak-dev down --stack worker
  oak-dev down --volumes         # was: oak-dev clean`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			scoped := cmd.Flags().Changed("stack")

			if volumes && !yes {
				if !confirm("This deletes all Oakestra volumes (MongoDB, Redis, containerd). Continue?") {
					return nil
				}
			}
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}
			return runDown(cfg, tl, scoped, volumes)
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also delete data volumes (destructive)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// runDown stops the stacks in scope, in reverse start order. scoped reports
// whether --stack was given explicitly: a plain `down` also clears the sticky
// scope, so the next `up` goes back to oak-dev.yaml's default instead of
// silently staying narrowed.
func runDown(cfg *config.Config, tl *tools, scoped, volumes bool) error {
	// Every project, regardless of the current scope: a plain `down` after an
	// `up --stack worker` must still tear down what that scope left running.
	all, err := tl.fullScope(cfg)
	if err != nil {
		return err
	}

	for _, stack := range downOrder() {
		if scoped && !cfg.StackEnabled(stack) {
			continue
		}
		if err := all.compose.StackDown(stack, volumes); err != nil {
			return fmt.Errorf("%s stack: %w", stack, err)
		}
		// Its containers are gone, so nothing is attached in it any more - but
		// a stack this --stack scope skipped keeps its entries (see runUp).
		if err := debugstate.ClearStacks(cfg.RepoRoot, stack); err != nil {
			return err
		}
	}

	if !scoped {
		_ = os.Remove(config.StackStatePath(cfg.RepoRoot))
	}
	return nil
}

func newResetCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:     "reset",
		GroupID: groupStack,
		Short:   "Wipe database state and restart services, keeping images",
		Long: `The "state is weird" command. Returns a dirty system (stuck jobs, odd
scheduler state) to clean in ~15s, without the minutes-long down/up cycle:
drops every non-system database in the root and cluster mongo instances,
flushes both redis instances, and restarts the app-level services so
in-memory caches clear too.

Does not touch images, and does not touch the NetManager databases
(mongo_rootnet/mongo_clusternet). Does not re-register the worker either - it
keeps its node ID and instances until its next handshake.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			scoped := cmd.Flags().Changed("stack")
			if !yes && !confirm("This drops every application database in the root and cluster. Continue?") {
				return nil
			}
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}
			return runReset(cfg, tl, scoped)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// runReset drops every non-system database in the root and cluster mongo
// instances, flushes both redis instances, and restarts the app-level services
// so in-memory caches clear too. It deliberately does not touch the worker: it
// keeps its node ID and instances until its next handshake.
func runReset(cfg *config.Config, tl *tools, scoped bool) error {
	all, err := tl.fullScope(cfg)
	if err != nil {
		return err
	}

	dropDBs := `db.getMongo().getDBNames().forEach(function(n){` +
		`if(["admin","local","config"].indexOf(n)<0){db.getSiblingDB(n).dropDatabase();}})`

	// Which services to restart is derived from the registry rather than
	// hardcoded, so a new component doesn't silently miss out on resets.
	appServices := func(stack string) []string {
		var out []string
		for _, c := range components.All() {
			for _, t := range c.InStack(stack) {
				out = append(out, t.Container)
			}
		}
		return out
	}

	resets := []struct {
		stack, mongo, port, redis string
		redisArgs                 []string
	}{
		{components.StackRoot, "mongo_root", "10007", "root_redis", []string{"-a", "rootRedis"}},
		{components.StackCluster, "mongo_cluster", "10107", "cluster_redis", []string{"-a", "clusterRedis", "-p", "6479"}},
	}

	for _, r := range resets {
		if scoped && !cfg.StackEnabled(r.stack) {
			continue
		}
		fmt.Printf("oak-dev: dropping %s application databases...\n", r.stack)
		mongo := compose.Ref{Stack: r.stack, Container: r.mongo}
		if out, err := all.compose.Capture(mongo,
			"mongosh", "--port", r.port, "--quiet", "--eval", dropDBs); err != nil {
			return fmt.Errorf("dropping %s databases: %w\n%s", r.stack, err, out)
		}
		redis := compose.Ref{Stack: r.stack, Container: r.redis}
		flush := append(append([]string{"redis-cli"}, r.redisArgs...), "FLUSHALL")
		if out, err := all.compose.Capture(redis, flush...); err != nil {
			return fmt.Errorf("flushing %s redis: %w\n%s", r.stack, err, out)
		}
		if svcs := appServices(r.stack); len(svcs) > 0 {
			rs := make([]compose.Ref, len(svcs))
			for i, name := range svcs {
				rs[i] = compose.Ref{Stack: r.stack, Container: name}
			}
			if err := all.compose.Restart(rs...); err != nil {
				return fmt.Errorf("%s stack: %w", r.stack, err)
			}
		}
	}

	fmt.Println("oak-dev: reset. The worker keeps its old node ID and instances until it next re-registers.")
	return nil
}
