package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/debugstate"
)

// debugRoot builds a throwaway repo root whose compose/ is the real one, so
// these tests read the checked-in override-debug-*.yml fragments without
// writing .generated/debug into the actual checkout.
func debugRoot(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	real, err := filepath.Abs(filepath.Join("..", "..", "compose"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "compose")); err != nil {
		t.Fatal(err)
	}
	return &config.Config{RepoRoot: root}
}

func TestDebugOverlaysCombinesSharedContainer(t *testing.T) {
	cfg := debugRoot(t)

	// nodeengine and netmanager are one container. Attaching the second must
	// bring the first one's overlay along, or recreating `worker` silently
	// drops the debugger that was already there.
	for _, name := range []string{"nodeengine", "netmanager"} {
		if err := debugstate.Add(cfg.RepoRoot, name, components.StackWorker); err != nil {
			t.Fatal(err)
		}
	}

	files, services, err := debugOverlays(cfg, components.StackWorker, "worker")
	if err != nil {
		t.Fatalf("debugOverlays: %v", err)
	}

	var bases []string
	for _, f := range files {
		bases = append(bases, filepath.Base(f))
	}
	want := []string{"override-debug-worker.yml", "override-debug-netmanager.yml"}
	if !slices.Equal(bases, want) {
		t.Errorf("overlay files = %v, want %v", bases, want)
	}
	if !slices.Equal(services, []string{"worker"}) {
		t.Errorf("services = %v, want [worker]", services)
	}
}

func TestDebugOverlaysIncludesServicesTheOverrideTouches(t *testing.T) {
	cfg := debugRoot(t)
	if err := debugstate.Add(cfg.RepoRoot, "cluster_service_manager", components.StackCluster); err != nil {
		t.Fatal(err)
	}

	// cluster_service_manager has no network namespace of its own, so its
	// debug port is published on cluster_manager - recreating only
	// cluster_service_manager would never publish it.
	_, services, err := debugOverlays(cfg, components.StackCluster, "cluster_service_manager")
	if err != nil {
		t.Fatalf("debugOverlays: %v", err)
	}
	for _, want := range []string{"cluster_service_manager", "cluster_manager"} {
		if !slices.Contains(services, want) {
			t.Errorf("services = %v, want it to contain %q", services, want)
		}
	}
}

func TestDebugOverlaysIgnoresOtherStacks(t *testing.T) {
	cfg := debugRoot(t)
	attached := []debugstate.Entry{
		{Component: "nodeengine", Stack: components.StackWorker},
		{Component: "system_manager", Stack: components.StackRoot},
	}
	for _, e := range attached {
		if err := debugstate.Add(cfg.RepoRoot, e.Component, e.Stack); err != nil {
			t.Fatal(err)
		}
	}

	files, services, err := debugOverlays(cfg, components.StackWorker, "worker")
	if err != nil {
		t.Fatalf("debugOverlays: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "override-debug-worker.yml" {
		t.Errorf("overlay files = %v, want just the worker one", files)
	}
	if !slices.Equal(services, []string{"worker"}) {
		t.Errorf("services = %v, want [worker]", services)
	}
}

func TestDebugOverlaysNoneAttached(t *testing.T) {
	cfg := debugRoot(t)
	files, services, err := debugOverlays(cfg, components.StackRoot, "system_manager")
	if err != nil {
		t.Fatalf("debugOverlays: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("overlay files = %v, want none", files)
	}
	// The container being acted on is always recreated, overlay or not.
	if !slices.Equal(services, []string{"system_manager"}) {
		t.Errorf("services = %v, want [system_manager]", services)
	}
}

// TestWaitForPortReturnsOnceListening simulates a real debugger: it accepts
// the connection and, like dlv/debugpy waiting for their client to speak
// first, sends nothing and keeps it open rather than closing it.
func TestWaitForPortReturnsOnceListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	port, err := strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForPort(port, 5*time.Second); err != nil {
		t.Fatalf("waitForPort on a listening port: %v", err)
	}
}

// TestWaitForPortIgnoresForwarderThatAcceptsThenDrops covers the race
// portBacked exists to close: Docker/OrbStack's host-side port forwarder
// accepts the TCP handshake as soon as the container's port mapping exists,
// then drops the connection once it discovers nothing is listening inside
// yet - well before the real debugger has started. A bare dial-success check
// would treat that as ready; waitForPort must not.
func TestWaitForPortIgnoresForwarderThatAcceptsThenDrops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port, err := strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	err = waitForPort(port, 500*time.Millisecond)
	if err == nil {
		t.Fatal("waitForPort on an accept-then-drop forwarder = nil, want a timeout error - that's not a real listener")
	}
}

func TestWaitForPortTimesOutWhenNothingListens(t *testing.T) {
	// Reserve a port, then close it before waiting - almost certainly nothing
	// else grabs it in the meantime, and the short timeout keeps the test fast.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	err = waitForPort(port, 500*time.Millisecond)
	if err == nil {
		t.Fatal("waitForPort on a closed port = nil, want a timeout error")
	}
}
