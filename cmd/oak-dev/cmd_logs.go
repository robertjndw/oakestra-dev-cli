package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"oak-dev/internal/compose"
	"oak-dev/internal/multilog"
	"oak-dev/internal/proc"
	"oak-dev/internal/target"
)

func newLogsCmd() *cobra.Command {
	var tail int
	var noFollow bool

	cmd := &cobra.Command{
		Use:     "logs [target...]",
		GroupID: groupInspect,
		Short:   "Stream logs from stacks, components or containers",
		Long: `With no argument, merges the logs of every stack in scope into one stream,
each line tagged and coloured by stack.

A target can be a stack (root, cluster, worker), a component by name, alias or
prefix (cluster_manager, cm, or sched - which follows both schedulers), a raw
container name (mongo_root, root_redis), or the special target ` + "`mqtt`" + `.

` + "`mqtt`" + ` subscribes to the broker instead of reading a container's stdout,
because the cluster<->worker control plane is MQTT: it is the only way to
watch messages like nodes/<id>/control/deploy as they happen.

Ctrl-C stops everything.`,
		Example: `  oak-dev logs                  # everything, merged
  oak-dev logs cluster          # one stack
  oak-dev logs cm               # one component
  oak-dev logs sched            # both schedulers
  oak-dev logs mqtt             # the control plane
  oak-dev logs worker --tail 200`,
		ValidArgsFunction: completeTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)
			tl, err := newTools(cfg)
			if err != nil {
				return err
			}

			var sources []multilog.Source
			if len(args) == 0 {
				sources = stackLogSources(tl, "", tail)
			} else {
				targets, err := target.NewResolver(cfg, tl.compose).ResolveAll(args)
				if err != nil {
					return err
				}
				if sources, err = targetLogSources(tl, targets, tail, noFollow); err != nil {
					return err
				}
			}
			if len(sources) == 0 {
				return fmt.Errorf("nothing to follow in the current scope (%s)", cfg.Stack)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return multilog.Run(ctx, sources)
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 50, "number of existing lines to show per container")
	cmd.Flags().BoolVar(&noFollow, "no-follow", false, "print what's buffered and exit instead of streaming")
	return cmd
}

func targetLogSources(tl *tools, targets []target.Target, tail int, noFollow bool) ([]multilog.Source, error) {
	var out []multilog.Source
	for _, t := range targets {
		r := compose.Ref{Stack: t.Stack, Container: t.Container}

		var spec proc.Spec
		var err error
		if len(t.Follow) > 0 {
			// An endpoint that has to be subscribed to rather than read: the
			// cluster<->worker control plane is MQTT, not a container's stdout.
			spec, err = tl.compose.ExecSpec(r, t.Follow...)
		} else {
			spec, err = tl.compose.LogSpec(r, compose.LogOpts{Tail: tail, Follow: !noFollow})
		}
		if err != nil {
			continue // out of scope; nothing to follow there
		}

		out = append(out, multilog.Source{
			Tag: t.Label, Dir: spec.Dir, Env: spec.Env,
			Name: spec.Name, Args: spec.Args,
		})
	}
	return out, nil
}
