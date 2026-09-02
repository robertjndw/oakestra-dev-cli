package debugstate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestActiveEmpty(t *testing.T) {
	root := t.TempDir()
	if got := Active(root); len(got) != 0 {
		t.Fatalf("Active on a fresh root = %v, want none", got)
	}
}

func TestAddRemove(t *testing.T) {
	root := t.TempDir()

	// Two components attached to the same container both have to survive,
	// in attach order - that's the case this package exists to handle.
	for _, name := range []string{"nodeengine", "netmanager"} {
		if err := Add(root, name, "worker"); err != nil {
			t.Fatalf("Add(%s): %v", name, err)
		}
	}
	if got, want := InStack(root, "worker"), []string{"nodeengine", "netmanager"}; !slices.Equal(got, want) {
		t.Fatalf("InStack(worker) = %v, want %v", got, want)
	}

	// Adding twice must not duplicate - `oak-dev debug ne` twice in a row is
	// an ordinary thing to do.
	if err := Add(root, "nodeengine", "worker"); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	if got, want := InStack(root, "worker"), []string{"nodeengine", "netmanager"}; !slices.Equal(got, want) {
		t.Fatalf("InStack(worker) after re-Add = %v, want %v", got, want)
	}

	if err := Remove(root, "nodeengine", "worker"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got, want := InStack(root, "worker"), []string{"netmanager"}; !slices.Equal(got, want) {
		t.Fatalf("InStack(worker) after Remove = %v, want %v", got, want)
	}

	// Removing something that was never there is a no-op, not an error:
	// `reload` calls it unconditionally.
	if err := Remove(root, "scheduler", "root"); err != nil {
		t.Fatalf("Remove of an absent entry: %v", err)
	}
}

// A component with a container in two stacks is debugged per stack, so
// touching one must leave the other attached.
func TestPerStackEntries(t *testing.T) {
	root := t.TempDir()
	for _, stack := range []string{"root", "cluster"} {
		if err := Add(root, "scheduler", stack); err != nil {
			t.Fatalf("Add(scheduler, %s): %v", stack, err)
		}
	}
	if err := Remove(root, "scheduler", "root"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := InStack(root, "root"); len(got) != 0 {
		t.Fatalf("InStack(root) = %v, want none", got)
	}
	if got, want := InStack(root, "cluster"), []string{"scheduler"}; !slices.Equal(got, want) {
		t.Fatalf("InStack(cluster) = %v, want %v", got, want)
	}
}

// The regression `up --stack root` used to cause: it cleared the whole file,
// so a worker debugger oak-dev never touched was forgotten and the next
// `debug netmanager` detached nodeengine.
func TestClearStacksLeavesOtherStacks(t *testing.T) {
	root := t.TempDir()
	adds := []Entry{
		{"system_manager", "root"},
		{"scheduler", "cluster"},
		{"nodeengine", "worker"},
		{"netmanager", "worker"},
	}
	for _, e := range adds {
		if err := Add(root, e.Component, e.Stack); err != nil {
			t.Fatalf("Add(%v): %v", e, err)
		}
	}

	if err := ClearStacks(root, "root", "cluster"); err != nil {
		t.Fatalf("ClearStacks: %v", err)
	}
	want := []Entry{{"nodeengine", "worker"}, {"netmanager", "worker"}}
	if got := Active(root); !slices.Equal(got, want) {
		t.Fatalf("Active after ClearStacks = %v, want %v", got, want)
	}

	// Clearing stacks with nothing attached is what `up`/`down` do every run.
	if err := ClearStacks(root, "root"); err != nil {
		t.Fatalf("ClearStacks with nothing to clear: %v", err)
	}

	if err := ClearStacks(root, "root", "cluster", "worker"); err != nil {
		t.Fatalf("ClearStacks(all): %v", err)
	}
	if got := Active(root); len(got) != 0 {
		t.Fatalf("Active after clearing every stack = %v, want none", got)
	}
}

func TestRemoveLastClearsFile(t *testing.T) {
	root := t.TempDir()
	if err := Add(root, "scheduler", "root"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Remove(root, "scheduler", "root"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(Path(root)); !os.IsNotExist(err) {
		t.Fatalf("state file still present after removing the last entry (err=%v)", err)
	}
}

// Blank lines, and leftovers from the old component-only format, are ignored
// rather than failing the command that reads them.
func TestActiveIgnoresMalformedLines(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "\nscheduler root\n\n  system_manager   root  \nnetmanager\nfoo bar baz\n"
	if err := os.WriteFile(Path(root), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"scheduler", "root"}, {"system_manager", "root"}}
	if got := Active(root); !slices.Equal(got, want) {
		t.Fatalf("Active = %v, want %v", got, want)
	}
}
