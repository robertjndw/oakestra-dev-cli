package multilog

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"
)

// TestRunCommandOnlySourceReturnsPromptlyOnCancel guards the shutdownGrace
// scoping added alongside Func-backed sources: a command-only source list
// (what `oak-dev logs` builds) must still return as soon as ctx is
// cancelled, not after waiting up to shutdownGrace, even if the underlying
// process ignores SIGTERM and keeps running.
func TestRunCommandOnlySourceReturnsPromptlyOnCancel(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sources := []Source{
		{
			Tag: "ignores-term", Name: "sh",
			// Trap SIGTERM so the process outlives cancellation on its own -
			// if Run still waited for it, this test would take shutdownGrace
			// (10s) to finish instead of returning promptly.
			Args: []string{"-c", "trap '' TERM; sleep 5"},
		},
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Run(ctx, sources) }()

	// Give the process a moment to actually start (and install its trap)
	// before cancelling, so cancellation genuinely races a still-running
	// command rather than an unstarted one.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly for a command-only source list (shutdownGrace should not apply)")
	}
	if elapsed := time.Since(start); elapsed > shutdownGrace {
		t.Fatalf("Run took %v, longer than shutdownGrace even though no source was Func-backed", elapsed)
	}
}

// TestRunFuncSourceGetsShutdownGrace confirms a Func-backed source (the file
// watchers `oak-dev dev` uses) is still given time to finish on its own
// after cancellation, rather than being cut off the instant ctx is done.
func TestRunFuncSourceGetsShutdownGrace(t *testing.T) {
	const finishAfter = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	sources := []Source{
		{
			Tag: "slow-func",
			Func: func(ctx context.Context, out io.Writer) error {
				<-ctx.Done()
				time.Sleep(finishAfter)
				close(finished)
				return nil
			},
		},
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Run(ctx, sources) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("Run did not return even after shutdownGrace elapsed")
	}

	select {
	case <-finished:
	default:
		t.Fatal("Run returned before the Func-backed source finished, want it to wait up to shutdownGrace")
	}
	if elapsed := time.Since(start); elapsed < finishAfter {
		t.Fatalf("Run returned after %v, faster than the source's own %v finish time - grace period was not honored", elapsed, finishAfter)
	}
}
