// Package testsuite drives the Go E2E suite (./e2e/...) - building the
// `go test -tags e2e` command line and holding the run lock that keeps
// `oak-dev dev` from restarting the worker mid-run (CLAUDE.md: that would
// mint a new node ID and strand every already-scheduled instance).
package testsuite

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// smokeRun is the -run pattern smoke mode uses. TestSmokeHealth and
// TestSmokeRegistration are the only two tests named with this prefix - see
// e2e/doc.go, which documents the prefix as the whole smoke-mode contract.
const smokeRun = "^TestSmoke"

// Suite runs the Go E2E suite.
type Suite struct {
	cfg    *config.Config
	runner proc.Runner
}

// New returns a Suite that runs go test through runner.
func New(cfg *config.Config, runner proc.Runner) *Suite {
	return &Suite{cfg: cfg, runner: runner}
}

// LockPath is a marker file written for the duration of a test run so
// `oak-dev dev`'s watchers know to skip the worker (see CLAUDE.md: recreating
// or restarting the worker mid-run strands scheduled instances on a stale
// node ID).
func LockPath(cfg *config.Config) string {
	return filepath.Join(cfg.RepoRoot, ".generated", "test.lock")
}

// Run executes the suite. smoke=true narrows to TestSmokeHealth and
// TestSmokeRegistration. extra is appended after oak-dev's own flags, so a
// user-supplied -run or -timeout wins - the test binary uses the standard
// flag package, where the last occurrence of a repeated flag is the one that
// takes effect.
func (s *Suite) Run(smoke bool, extra ...string) error {
	if err := rejectPytestArgs(extra); err != nil {
		return err
	}

	args := []string{"test", "-tags", "e2e", "./e2e/...", "-v",
		// go test caches a successful result keyed on package files and the
		// env vars it reads, not on network I/O. Without -count=1, a second
		// run against a stack that has since broken can replay a stale PASS
		// in milliseconds instead of actually talking to it.
		"-count=1",
		"-timeout", derivedTimeout(s.cfg).String(),
	}
	if smoke {
		args = append(args, "-run", smokeRun)
	}
	args = append(args, extra...)

	// No Spec.Env: the child must inherit the process environment unchanged,
	// or the process-env > .env precedence internal/config.LoadE2E relies on
	// breaks for every OAK_* variable the suite reads.
	return s.runner.Run(proc.Spec{Name: "go", Args: args, Dir: s.cfg.RepoRoot})
}

// derivedTimeout sizes go test's -timeout off the configured E2E waits
// instead of trusting its 10-minute default, which the deployment and
// network tests alone can blow through polling worst case. Blowing -timeout
// prints a goroutine dump for every live goroutine rather than a readable
// test failure, so this errs generous: twice the ready wait (the suite logs
// in and waits for an active cluster up front) plus eight deploy waits (each
// of 03/04's steps polls to a terminal status once), rounded up to a whole
// minute, with a 10 minute floor for when cfg.E2E is zero (e.g. a bare
// &config.Config{} in a test).
func derivedTimeout(cfg *config.Config) time.Duration {
	const floor = 10 * time.Minute
	ready, deploy := cfg.E2E.ReadyTimeout, cfg.E2E.DeployTimeout
	if ready <= 0 {
		ready = 180 * time.Second
	}
	if deploy <= 0 {
		deploy = 300 * time.Second
	}
	total := 2*ready + 8*deploy
	if total < floor {
		return floor
	}
	minute := time.Minute
	return time.Duration(math.Ceil(float64(total)/float64(minute))) * minute
}

// pytestOnlyFlags have no go test equivalent at all - they'd otherwise either
// be silently ignored or rejected by the flag package with no context on what
// to use instead.
var pytestOnlyFlags = map[string]bool{"-k": true, "-m": true, "--collect-only": true, "-ra": true}

// rejectPytestArgs rejects passthrough args left over from the pytest suite,
// with a teaching error rather than handing them to go test verbatim.
//
// A bare positional can't just be forwarded: pytest read it as a test path,
// but go test reads a bare positional as a package pattern and fails with
// "package tests/test_03_deployment.py is not in std" - a confusing error for
// something that used to work.
//
// There is no enumerated list of go test's value-taking flags (-run, -list,
// -bench, -timeout, -count, -cpu, -shuffle, ... - and more arrive with every
// Go release). Enumerating them is both unnecessary and a rot hazard: a token
// only needs classifying as "a flag's value" long enough to excuse it from
// the bare-positional check, and that's answered generically by looking at
// what came before it, not by knowing every flag's name.
func rejectPytestArgs(extra []string) error {
	var bad []string
	prevTakesValue := false // previous token was "-flag" (no "="), so this token may be its value
	for _, a := range extra {
		takesValue := false
		switch {
		case a == "":
			continue

		// Unambiguous pytest leftovers are rejected wherever they appear -
		// including as what looks like a flag's value - because go test can
		// never legitimately take one. Without this, "-v tests/test_03.py"
		// would treat the path as -v's value and let it through.
		case strings.HasSuffix(a, ".py"), strings.HasPrefix(a, "tests/"):
			bad = append(bad, a)

		case strings.HasPrefix(a, "-"):
			name, hasEq := a, false
			if i := strings.IndexByte(a, '='); i >= 0 {
				name, hasEq = a[:i], true
			}
			if pytestOnlyFlags[name] {
				bad = append(bad, a)
			}
			takesValue = !hasEq

		case !prevTakesValue:
			// A bare positional not consumed as the preceding flag's value -
			// go test has no positional arguments at all, so this can only be
			// a leftover pytest test path that rules above didn't already
			// catch (e.g. a bare directory name).
			bad = append(bad, a)
		}
		prevTakesValue = takesValue
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf(`oak-dev test runs go test now, not pytest: %s %s left over from the old suite.

tests/test_01_health.py       -> -run TestSmokeHealth
tests/test_02_registration.py -> -run TestSmokeRegistration
tests/test_03_deployment.py   -> -run TestDeploymentLifecycle
tests/test_04_network.py      -> -run TestOverlayNetwork
tests/test_05_failures.py     -> -run TestFailureReporting
-k scale                      -> -run 'TestDeploymentLifecycle/scale'
-m "not deployment"           -> --smoke
oak-dev test -- -list .       lists every test name`,
		strings.Join(bad, ", "), pluralize(len(bad), "looks", "look"))
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// IsLocked reports whether a test run is currently in progress.
func IsLocked(cfg *config.Config) bool {
	_, err := os.Stat(LockPath(cfg))
	return err == nil
}

// Lock creates the test-in-progress marker; Unlock removes it.
func Lock(cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(LockPath(cfg)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(LockPath(cfg), []byte("1"), 0o644)
}

func Unlock(cfg *config.Config) error {
	err := os.Remove(LockPath(cfg))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
