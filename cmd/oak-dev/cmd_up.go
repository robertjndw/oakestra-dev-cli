package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
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
			_, err := runUp(cfg, true)
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
func runUp(cfg *config.Config, sticky bool) (map[string][]string, error) {
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

	if err := ensureLiveBinaries(cfg); err != nil {
		return nil, err
	}
	files, err := topology.Render(cfg)
	if err != nil {
		return nil, err
	}

	for _, stack := range upOrder {
		f, ok := files[stack]
		if !ok {
			continue
		}
		upArgs := []string{"up", "-d", "--build"}
		if stack == components.StackWorker && cfg.Workers > 1 {
			upArgs = append(upArgs, "--scale", fmt.Sprintf("worker=%d", cfg.Workers))
		}
		fmt.Printf("oak-dev: starting %s stack...\n", stack)
		if err := runStack(cfg, stack, f, upArgs...); err != nil {
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

			files, err := renderAll(cfg)
			if err != nil {
				return err
			}
			downArgs := []string{"down"}
			if volumes {
				downArgs = append(downArgs, "-v")
			}

			for _, stack := range downOrder() {
				if scoped && !cfg.StackEnabled(stack) {
					continue
				}
				f, ok := files[stack]
				if !ok {
					continue
				}
				if err := runStack(cfg, stack, f, downArgs...); err != nil {
					return err
				}
			}

			if !scoped {
				_ = os.Remove(config.StackStatePath(cfg.RepoRoot))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false, "also delete data volumes (destructive)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
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
			if !yes && !confirm("This drops every application database in the root and cluster. Continue?") {
				return nil
			}

			files, err := renderAll(cfg)
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
				f, ok := files[r.stack]
				if !ok {
					continue
				}
				fmt.Printf("oak-dev: dropping %s application databases...\n", r.stack)
				if out, err := compose.Output(cfg, f, "exec", "-T", r.mongo,
					"mongosh", "--port", r.port, "--quiet", "--eval", dropDBs); err != nil {
					return fmt.Errorf("dropping %s databases: %w\n%s", r.stack, err, out)
				}
				flush := append([]string{"exec", "-T", r.redis, "redis-cli"}, r.redisArgs...)
				if out, err := compose.Output(cfg, f, append(flush, "FLUSHALL")...); err != nil {
					return fmt.Errorf("flushing %s redis: %w\n%s", r.stack, err, out)
				}
				if svcs := appServices(r.stack); len(svcs) > 0 {
					if err := runStack(cfg, r.stack, f, append([]string{"restart"}, svcs...)...); err != nil {
						return err
					}
				}
			}

			fmt.Println("oak-dev: reset. The worker keeps its old node ID and instances until it next re-registers.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
