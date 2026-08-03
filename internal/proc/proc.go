// Package proc is the seam between oak-dev and the external programs it
// drives: docker, go, python3 and pytest. Every such command is described as
// a Spec and handed to a Runner, so a test can substitute a Recorder for the
// real process and assert on what would have run.
//
// Before this existed, each caller built its own exec.Cmd and wired its own
// stdio, which meant nothing that orchestrated docker could be tested at all:
// the only tests in cmd/oak-dev covered the two functions that happened not to
// shell out.
//
// Named proc rather than exec because internal/compose, internal/build and
// internal/testsuite all need the standard library's os/exec alongside this
// package; an internal package called exec would force an import alias in
// every one of them.
package proc

import (
	"os"
	osexec "os/exec"
	"strings"
)

// Spec describes one external command. It is a value, so a Runner can record
// it, compare it, or print it without having started anything.
type Spec struct {
	// Name is the program, resolved through $PATH.
	Name string
	Args []string
	// Dir is the working directory. Empty means the calling process's.
	Dir string
	// Env is the complete environment for the command. Nil means inherit the
	// calling process's environment unchanged - which is os/exec's own
	// meaning for a nil Cmd.Env, not "run with an empty environment".
	Env []string
	// Stdin wires the calling process's stdin to the command. Only the
	// interactive `oak-dev shell` genuinely needs it, but every compose
	// invocation has always been wired this way, so it stays opt-in per Spec
	// rather than being assumed either way.
	Stdin bool
	// Combined makes Capture interleave stderr into the returned bytes.
	// The distinction is load-bearing and not cosmetic: `docker compose exec`
	// failures are reported to the user with their stderr attached, whereas
	// `go env GOPATH` is parsed as a path and must not have warnings mixed in.
	Combined bool
}

// String renders the spec roughly as it would be typed at a shell. Used for
// test assertions and error messages, not for re-execution - it does not
// quote, so a Spec carrying an argument with spaces renders ambiguously.
func (s Spec) String() string {
	if len(s.Args) == 0 {
		return s.Name
	}
	return s.Name + " " + strings.Join(s.Args, " ")
}

// Rel renders the spec with every occurrence of root replaced by a relative
// path, so an assertion over a Spec built from an absolute repo root does not
// depend on where that checkout happens to live.
func (s Spec) Rel(root string) string {
	if root == "" {
		return s.String()
	}
	trimmed := strings.TrimSuffix(root, "/") + "/"
	return strings.ReplaceAll(s.String(), trimmed, "")
}

// Runner starts external commands. The two methods differ only in what
// happens to the command's output: Run streams it to the terminal, Capture
// returns it.
type Runner interface {
	// Run streams stdout and stderr to the calling process's own.
	Run(Spec) error
	// Capture returns the command's output instead of streaming it.
	Capture(Spec) ([]byte, error)
}

// OS is the Runner that actually starts processes. It holds no state, so the
// zero value is usable and callers can pass OS{} directly.
type OS struct{}

// Run implements Runner.
func (OS) Run(s Spec) error {
	cmd := s.cmd()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if s.Stdin {
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

// Capture implements Runner.
func (OS) Capture(s Spec) ([]byte, error) {
	cmd := s.cmd()
	if s.Combined {
		return cmd.CombinedOutput()
	}
	return cmd.Output()
}

func (s Spec) cmd() *osexec.Cmd {
	cmd := osexec.Command(s.Name, s.Args...)
	cmd.Dir = s.Dir
	// A nil Env is os/exec's own "inherit", so this assignment is correct for
	// both cases and needs no branch.
	cmd.Env = s.Env
	return cmd
}

// Ensure the adapter satisfies the seam at compile time rather than at the
// first call site that happens to need it.
var _ Runner = OS{}
