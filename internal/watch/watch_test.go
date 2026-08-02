package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFire blocks until ch receives a value or the timeout elapses, failing
// the test in the latter case.
func waitFire(t *testing.T, ch <-chan struct{}, want bool, msg string) {
	t.Helper()
	select {
	case <-ch:
		if !want {
			t.Fatalf("%s: fired unexpectedly", msg)
		}
	case <-time.After(2 * time.Second):
		if want {
			t.Fatalf("%s: timed out waiting for a fire", msg)
		}
	}
}

func startWatch(t *testing.T, dir string) (fires chan struct{}, cancel func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fires = make(chan struct{}, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := Run(ctx, Options{Dir: dir, Debounce: 20 * time.Millisecond}, func() {
			fires <- struct{}{}
		}); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})
	// Give fsnotify time to install the initial watches before the caller
	// starts making changes.
	time.Sleep(50 * time.Millisecond)
	return fires, cancel
}

func TestFiresOnGoFileWrite(t *testing.T) {
	dir := t.TempDir()
	fires, _ := startWatch(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFire(t, fires, true, "writing a .go file")
}

func TestIgnoresNonMatchingExtension(t *testing.T) {
	dir := t.TempDir()
	fires, _ := startWatch(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFire(t, fires, false, "writing a .md file")
}

func TestRecursesIntoNewDirectory(t *testing.T) {
	dir := t.TempDir()
	fires, _ := startWatch(t, dir)

	sub := filepath.Join(dir, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The mkdir itself is a directory event, not a relevant file change; it
	// should not fire onChange, only extend the watch.
	waitFire(t, fires, false, "creating a new subdirectory")

	// Give addTree a moment to pick up the new directory before writing into
	// it - real editors don't create-then-instantly-write in the same tick.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(sub, "sub.go"), []byte("package pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFire(t, fires, true, "writing a .go file in a newly created subdirectory")
}

func TestCoalescesBurstIntoOneFire(t *testing.T) {
	dir := t.TempDir()
	fires, _ := startWatch(t, dir)

	for i := range 5 {
		name := filepath.Join(dir, "f"+string(rune('0'+i))+".go")
		if err := os.WriteFile(name, []byte("package main"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFire(t, fires, true, "burst of five writes")

	// No second fire should follow once the debounce window has closed
	// again.
	select {
	case <-fires:
		t.Fatal("burst of five writes produced more than one fire")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRunErrorsWhenDirMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	err := Run(context.Background(), Options{Dir: dir}, func() {})
	if err == nil {
		t.Fatal("Run returned nil error for a nonexistent Dir, want a non-nil error")
	}
}

func TestCancelStopsRun(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Dir: dir}, func() {})
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}
