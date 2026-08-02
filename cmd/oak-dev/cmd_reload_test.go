package main

import (
	"slices"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/debugstate"
)

// Recreating a container from the plain topology detaches every debugger in
// it, not just the one belonging to the component being reloaded: nodeengine
// and netmanager are both the `worker` service. Leaving the other entry behind
// would make the next `oak-dev debug` layer a dead overlay back on.
func TestForgetDebugClearsEveryComponentInTheContainer(t *testing.T) {
	cfg := &config.Config{RepoRoot: t.TempDir()}
	attached := []debugstate.Entry{
		{Component: "nodeengine", Stack: components.StackWorker},
		{Component: "netmanager", Stack: components.StackWorker},
		{Component: "system_manager", Stack: components.StackRoot},
	}
	for _, e := range attached {
		if err := debugstate.Add(cfg.RepoRoot, e.Component, e.Stack); err != nil {
			t.Fatal(err)
		}
	}

	if err := forgetDebug(cfg, components.StackWorker, "worker"); err != nil {
		t.Fatalf("forgetDebug: %v", err)
	}
	if got := debugstate.InStack(cfg.RepoRoot, components.StackWorker); len(got) != 0 {
		t.Errorf("worker entries = %v, want none", got)
	}
	// A stack this reload never touched keeps its entries.
	if got, want := debugstate.InStack(cfg.RepoRoot, components.StackRoot), []string{"system_manager"}; !slices.Equal(got, want) {
		t.Errorf("root entries = %v, want %v", got, want)
	}
}

// Only the container that was recreated: the scheduler runs in both stacks,
// and `reload sched --stack root` must leave cluster_scheduler attached.
func TestForgetDebugLeavesOtherContainers(t *testing.T) {
	cfg := &config.Config{RepoRoot: t.TempDir()}
	for _, stack := range []string{components.StackRoot, components.StackCluster} {
		if err := debugstate.Add(cfg.RepoRoot, "scheduler", stack); err != nil {
			t.Fatal(err)
		}
	}

	if err := forgetDebug(cfg, components.StackRoot, "root_scheduler"); err != nil {
		t.Fatalf("forgetDebug: %v", err)
	}
	if got := debugstate.InStack(cfg.RepoRoot, components.StackRoot); len(got) != 0 {
		t.Errorf("root entries = %v, want none", got)
	}
	if got, want := debugstate.InStack(cfg.RepoRoot, components.StackCluster), []string{"scheduler"}; !slices.Equal(got, want) {
		t.Errorf("cluster entries = %v, want %v", got, want)
	}
}
