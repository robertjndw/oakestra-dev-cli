// Package testsuite wraps the pytest E2E suite: creating the venv (what
// `make venv`/`make test` used to do) and running it, either in full or in
// smoke mode (health + registration only, no deployment tests).
package testsuite

import (
	"os"
	"path/filepath"
	"strings"

	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// Suite runs the pytest E2E suite. It holds the Runner so a test can prove
// that EnsureVenv skips the install when the stamp is present, and that the
// stamp is only written after pip has actually succeeded.
type Suite struct {
	cfg    *config.Config
	runner proc.Runner
}

// New returns a Suite that runs python through runner.
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

// VenvStamp is the file written once tests/requirements.txt is fully
// installed. Its presence is what "the venv is ready" means everywhere.
//
// Nothing python3 or pip creates is a safe substitute. `python3 -m venv`
// writes bin/activate before a single requirement is installed, and an
// interrupted or half-failed `pip install -r` leaves the packages it got
// through - pytest among them - on disk. Keying off either made every later
// run skip the install and call a broken venv finished.
func VenvStamp(repoRoot string) string {
	return filepath.Join(repoRoot, ".venv", ".oak-dev-requirements-installed")
}

// VenvReady reports whether .venv exists with its requirements installed.
// doctor's venv check reads the same stamp EnsureVenv writes, so a failed
// setup can't be reported as a healthy venv.
func VenvReady(cfg *config.Config) bool {
	_, err := os.Stat(VenvStamp(cfg.RepoRoot))
	return err == nil
}

// EnsureVenv creates .venv and installs tests/requirements.txt unless that has
// already completed, mirroring the old `make venv` target. A previous run that
// failed partway is retried rather than inherited.
func (s *Suite) EnsureVenv() error {
	cfg := s.cfg
	if VenvReady(cfg) {
		return nil
	}
	venv := filepath.Join(cfg.RepoRoot, ".venv")
	if _, err := os.Stat(filepath.Join(venv, "bin", "activate")); err != nil {
		if err := s.run("python3", "-m", "venv", venv); err != nil {
			return err
		}
	}
	pip := filepath.Join(venv, "bin", "pip")
	if err := s.run(pip, "install", "--quiet", "-r", "tests/requirements.txt"); err != nil {
		return err
	}
	return os.WriteFile(VenvStamp(cfg.RepoRoot), nil, 0o644)
}

// Run executes the suite. smoke=true runs only health + registration tests.
// extra is passed straight through to pytest; if it names any test paths of
// its own, they replace the default `tests/` rather than adding to it, so
// `oak-dev test -- tests/test_03_deployment.py` runs just that file.
func (s *Suite) Run(smoke bool, extra ...string) error {
	if err := s.EnsureVenv(); err != nil {
		return err
	}
	pytest := filepath.Join(s.cfg.RepoRoot, ".venv", "bin", "pytest")

	var args []string
	if !namesPaths(extra) {
		args = append(args, "tests/")
	}
	args = append(args, "-v")
	if smoke {
		args = append(args, "-m", "not deployment")
	}
	args = append(args, extra...)
	return s.run(pytest, args...)
}

// namesPaths reports whether extra contains a positional argument (anything
// not starting with "-"), which pytest would treat as a test path.
func namesPaths(extra []string) bool {
	for _, a := range extra {
		if a != "" && !strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// run executes from the repo root: pytest.ini, tests/ and tests/requirements.txt
// are all resolved relative to it.
func (s *Suite) run(name string, args ...string) error {
	return s.runner.Run(proc.Spec{Name: name, Args: args, Dir: s.cfg.RepoRoot})
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
