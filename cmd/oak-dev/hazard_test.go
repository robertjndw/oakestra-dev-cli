package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/build"
	"oak-dev/internal/components"
	"oak-dev/internal/debugstate"
	"oak-dev/internal/proc"
)

// The invariants in this file are the ones AGENTS.md warns about: each is a
// failure that looks like success. They are stated as intent - "nothing
// recreated the worker" - rather than as argv, so they keep meaning if the
// exact compose flags ever change.

// touched reports whether any recorded command names container in a position
// that would act on it. Deliberately blunt: the point is to prove a container
// was left alone, and a blunt check cannot miss a way of naming it.
func touched(cmds []string, container string) []string {
	var hits []string
	for _, c := range cmds {
		if strings.Contains(c, " "+container) {
			hits = append(hits, c)
		}
	}
	return hits
}

// verbs extracts the compose subcommand from each recorded command, dropping
// the -f chain: "up -d worker", "restart root_scheduler", "exec -T worker ...".
func verbs(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		if !strings.HasPrefix(c, "docker compose ") {
			continue
		}
		rest := strings.TrimPrefix(c, "docker compose ")
		for {
			if after, ok := strings.CutPrefix(rest, "-f "); ok {
				_, rest, _ = strings.Cut(after, " ")
				continue
			}
			if strings.HasPrefix(rest, "<") {
				_, rest, _ = strings.Cut(rest, " ")
				continue
			}
			break
		}
		out = append(out, rest)
	}
	return out
}

// HAZARD: recreating the worker mints a new hostname, which cluster_manager
// registers as a new node ID, stranding every already-scheduled instance in
// NODE_SCHEDULED forever. Nothing in a reload of a worker-resident component
// may recreate or even restart that container.
func TestHazardWorkerIsNeverRecreatedByReload(t *testing.T) {
	for _, name := range []string{"nodeengine", "netmanager"} {
		t.Run(name, func(t *testing.T) {
			cfg, tl, rec := argvEnv(t, map[string]bool{name: true})

			if err := reloadComponent(cfg, tl, mustResolve(t, name), false, false, true); err != nil {
				t.Fatalf("reload: %v", err)
			}

			for _, v := range verbs(commands(t, cfg, rec)) {
				switch {
				case strings.HasPrefix(v, "up "), strings.HasPrefix(v, "restart "):
					t.Errorf("reload %s issued %q - that recreates or restarts the worker container", name, v)
				}
			}
		})
	}
}

// HAZARD: reset drops databases and restarts app services. If it swept the
// worker in with them, the node would re-register and its instances would be
// lost - the command's own output promises the opposite.
func TestHazardResetNeverTouchesTheWorker(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := runReset(cfg, tl, false); err != nil {
		t.Fatalf("runReset: %v", err)
	}

	if hits := touched(commands(t, cfg, rec), "worker"); len(hits) > 0 {
		t.Errorf("reset touched the worker: %v", hits)
	}
}

// HAZARD: /oak-bin shadows whatever a rebuild bakes into the image, so an
// --image reload of a live Go component that skipped the cross-compile would
// recreate the container onto the OLD binary and report success.
func TestHazardImageReloadCrossCompilesWhenLive(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), true, false, true); err != nil {
		t.Fatalf("reload --image: %v", err)
	}

	cmds := commands(t, cfg, rec)
	if !slices.ContainsFunc(cmds, func(c string) bool { return strings.HasPrefix(c, "go build") }) {
		t.Errorf("commands = %v, want a cross-compile: /oak-bin shadows the rebuilt image", cmds)
	}
}

// The same reload for a component that is NOT live has no host mount to
// shadow the image, so the cross-compile would be wasted work.
func TestHazardImageReloadSkipsBuildWhenNotLive(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), true, false, true); err != nil {
		t.Fatalf("reload --image: %v", err)
	}

	for _, c := range commands(t, cfg, rec) {
		if strings.HasPrefix(c, "go build") {
			t.Errorf("cross-compiled %q for a component that is not live", c)
		}
	}
}

// HAZARD: a failed build must leave the running container alone. Restarting
// onto a binary that did not compile means silently serving stale code, which
// is why the failure is also shouted about.
func TestHazardFailedBuildIssuesNoRestart(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})
	boom := errors.New("compile error")
	rec.OnContains("go build", nil, boom)

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), false, false, true); !errors.Is(err, boom) {
		t.Fatalf("reload err = %v, want %v", err, boom)
	}

	for _, v := range verbs(commands(t, cfg, rec)) {
		t.Errorf("issued %q after a failed build - the old binary is still running", v)
	}
}

// HAZARD: reconcile recreates from the plain topology to undo a debug overlay,
// but must skip anything with InPlaceRestart - recreating the worker is the
// node-ID hazard above. Coming back from `debug nodeengine` therefore needs an
// explicit `oak-dev up`, and that is deliberate.
func TestHazardReconcileSkipsInPlaceTargets(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"nodeengine": true})

	ne := mustResolve(t, "nodeengine")
	if err := reconcile(cfg, tl, ne.Targets); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("reconcile issued %v, want nothing - the worker must not be recreated", got)
	}
}

// ...while a component without InPlaceRestart is reconciled normally.
func TestHazardReconcileRecreatesOrdinaryTargets(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})

	sched := mustResolve(t, "scheduler")
	if err := reconcile(cfg, tl, sched.Targets); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	want := []string{"up -d root_scheduler", "up -d cluster_scheduler"}
	if got := verbs(commands(t, cfg, rec)); !slices.Equal(got, want) {
		t.Errorf("verbs = %v, want %v", got, want)
	}
}

// HAZARD: nodeengine and netmanager are both the `worker` service. Attaching a
// debugger to the second must carry the first one's overlay along, or
// recreating the container silently detaches the debugger already on it.
func TestHazardDebugKeepsOverlaysSharingAContainer(t *testing.T) {
	cfg, tl, rec := argvEnvScope(t, map[string]bool{"nodeengine": true, "netmanager": true}, components.StackWorker)

	if err := debugstate.Add(cfg.RepoRoot, "nodeengine", components.StackWorker); err != nil {
		t.Fatal(err)
	}
	// A pre-existing cross-compiled Delve, so EnsureDelve returns early rather
	// than trying to install one through the recorder.
	dir, err := build.Dir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dlv"), nil, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runDebug(cfg, tl, "netmanager", false, false); err != nil {
		t.Fatalf("runDebug: %v", err)
	}

	var recreate string
	for _, c := range commands(t, cfg, rec) {
		if strings.Contains(c, "--force-recreate") {
			recreate = c
		}
	}
	if recreate == "" {
		t.Fatalf("no force-recreate issued; commands = %v", commands(t, cfg, rec))
	}
	for _, w := range []string{
		"override-debug-worker.yml",     // nodeengine's, already attached
		"override-debug-netmanager.yml", // the one being attached now
	} {
		if !strings.Contains(recreate, w) {
			t.Errorf("recreate %q missing %q - recreating `worker` without it detaches that debugger", recreate, w)
		}
	}
}

// HAZARD: `up` recreates a stack from the plain topology, which carries no
// debug overlay - so every debugger in that stack is gone and must not still
// be claimed. But a stack the scope skipped keeps its entries: `up --stack
// root` leaves a debugged worker running, and forgetting it would make the
// next `debug netmanager` drop nodeengine's overlay.
func TestHazardUpClearsOnlyItsOwnStacksDebugState(t *testing.T) {
	cfg, tl, _ := argvEnvScope(t, map[string]bool{}, components.StackRoot)

	for _, e := range []debugstate.Entry{
		{Component: "system_manager", Stack: components.StackRoot},
		{Component: "nodeengine", Stack: components.StackWorker},
	} {
		if err := debugstate.Add(cfg.RepoRoot, e.Component, e.Stack); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := runUp(cfg, tl, false); err != nil {
		t.Fatalf("runUp: %v", err)
	}

	if got := debugstate.InStack(cfg.RepoRoot, components.StackRoot); len(got) != 0 {
		t.Errorf("root entries = %v, want none - up recreated it from the plain topology", got)
	}
	if got, want := debugstate.InStack(cfg.RepoRoot, components.StackWorker), []string{"nodeengine"}; !slices.Equal(got, want) {
		t.Errorf("worker entries = %v, want %v - that stack was out of scope", got, want)
	}
}

// HAZARD: root and cluster both declare the shared oakestra network;
// worker.yml declares it external and fails outright if neither has run yet.
// Down must be the exact reverse.
func TestHazardStartAndStopOrder(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{})

	if _, err := runUp(cfg, tl, false); err != nil {
		t.Fatalf("runUp: %v", err)
	}
	upSeen := stacksIn(commands(t, cfg, rec))
	if want := []string{"root", "cluster", "worker"}; !slices.Equal(upSeen, want) {
		t.Errorf("up order = %v, want %v", upSeen, want)
	}

	rec.Reset()
	if err := runDown(cfg, tl, false, false); err != nil {
		t.Fatalf("runDown: %v", err)
	}
	downSeen := stacksIn(commands(t, cfg, rec))
	if want := []string{"worker", "cluster", "root"}; !slices.Equal(downSeen, want) {
		t.Errorf("down order = %v, want %v", downSeen, want)
	}
}

// stacksIn reads the collapsed <stack> token out of each command, in order.
func stacksIn(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		open := strings.Index(c, "<")
		close := strings.Index(c, ">")
		if open < 0 || close < open {
			continue
		}
		out = append(out, c[open+1:close])
	}
	return out
}

// HAZARD: a narrowed scope must not silently act on a stack that was never
// started. Naming a component outside the scope is a mistake worth reporting;
// sweeping the whole live set past one is not.
func TestHazardOutOfScopeComponent(t *testing.T) {
	cfg, tl, rec := argvEnvScope(t, map[string]bool{"system_manager": true}, components.StackWorker)

	// Named explicitly: an error.
	if err := reloadComponent(cfg, tl, mustResolve(t, "system_manager"), false, false, true); err == nil {
		t.Error("naming an out-of-scope component = nil error, want one")
	}
	// Swept up by a bare `reload`: skipped with a note, nothing run.
	if err := reloadComponent(cfg, tl, mustResolve(t, "system_manager"), false, false, false); err != nil {
		t.Errorf("sweeping past an out-of-scope component = %v, want nil", err)
	}
	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("ran %v, want nothing", got)
	}
}

// The seam itself: proving a code path issues no commands is only meaningful
// if the recorder is actually what those paths would have used.
func TestHazardRecorderIsWiredEverywhere(t *testing.T) {
	cfg, tl, rec := argvEnv(t, map[string]bool{"scheduler": true})

	if err := reloadComponent(cfg, tl, mustResolve(t, "scheduler"), false, false, true); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(rec.Specs) == 0 {
		t.Fatal("recorded nothing - the fake runner is not reaching the code under test")
	}
	for _, s := range rec.Specs {
		if s.Name != "docker" && s.Name != "go" {
			t.Errorf("unexpected program %q", s.Name)
		}
	}
	var _ proc.Runner = rec
	_ = cfg
}
