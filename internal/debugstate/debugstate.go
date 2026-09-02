// Package debugstate records which components currently have a debugger
// attached, and in which stack, in .generated/debug.
//
// A debug overlay isn't part of the rendered topology - topology.Render only
// knows about override-live-*.yml - so `oak-dev debug` appends its own
// override-debug-*.yml on top and force-recreates the container. Without
// remembering what was already attached, debugging a second component that
// shares a container with a first (nodeengine and netmanager both live in
// `worker`) would recreate that container from a chain missing the first
// overlay and silently detach it. This package's job is to remember the
// active set so every overlay for a container gets reapplied together.
//
// Entries carry the stack because `up`/`down`/`reload` work one stack at a
// time. `up --stack root` doesn't touch the worker, so it must not forget the
// debugger attached there - otherwise the next `debug netmanager` would omit
// nodeengine's overlay and detach it.
package debugstate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const genDir = ".generated"
const fileName = "debug"

// Entry is one attached debugger: a component, and the stack whose container
// it is attached to. A component with containers in two stacks (scheduler)
// can be debugged in each independently, so the pair is the identity.
type Entry struct {
	Component string
	Stack     string
}

// Path returns where the active set is persisted.
func Path(repoRoot string) string {
	return filepath.Join(repoRoot, genDir, fileName)
}

// Active returns the attached debuggers, in the order they were attached. A
// missing or unreadable file means "none", and so does any line that isn't a
// "<component> <stack>" pair - this is a convenience cache under .generated,
// not a source of truth worth failing a command over.
func Active(repoRoot string) []Entry {
	data, err := os.ReadFile(Path(repoRoot))
	if err != nil {
		return nil
	}
	var out []Entry
	for line := range strings.SplitSeq(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			out = append(out, Entry{Component: f[0], Stack: f[1]})
		}
	}
	return out
}

// InStack returns the components attached in one stack, in attach order.
func InStack(repoRoot, stack string) []string {
	var out []string
	for _, e := range Active(repoRoot) {
		if e.Stack == stack {
			out = append(out, e.Component)
		}
	}
	return out
}

// Add records that name now has a debugger attached in stack.
func Add(repoRoot, name, stack string) error {
	want := Entry{Component: name, Stack: stack}
	entries := Active(repoRoot)
	if slices.Contains(entries, want) {
		return nil
	}
	return write(repoRoot, append(entries, want))
}

// Remove records that name no longer has a debugger attached in stack - what
// `reload` does when it recreates that container from the plain topology.
func Remove(repoRoot, name, stack string) error {
	return deleteIf(repoRoot, func(e Entry) bool {
		return e.Component == name && e.Stack == stack
	})
}

// ClearStacks forgets every debugger attached in the given stacks, for the
// commands that recreate whole stacks from the plain topology (`up`, `down`).
// Stacks they didn't touch keep their entries.
func ClearStacks(repoRoot string, stacks ...string) error {
	return deleteIf(repoRoot, func(e Entry) bool {
		return slices.Contains(stacks, e.Stack)
	})
}

func deleteIf(repoRoot string, drop func(Entry) bool) error {
	entries := Active(repoRoot)
	kept := slices.DeleteFunc(entries, drop)
	if len(kept) == len(entries) {
		return nil
	}
	return write(repoRoot, kept)
}

func write(repoRoot string, entries []Entry) error {
	path := Path(repoRoot)
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.Component + " " + e.Stack
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
