// Package testsuite wraps the pytest E2E suite: creating the venv (what
// `make venv`/`make test` used to do) and running it, either in full or in
// smoke mode (health + registration only, no deployment tests).
package testsuite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oak-dev/internal/config"
)

// LockPath is a marker file written for the duration of a test run so
// `oak-dev dev`'s watchers know to skip the worker (see CLAUDE.md: recreating
// or restarting the worker mid-run strands scheduled instances on a stale
// node ID).
func LockPath(cfg *config.Config) string {
	return filepath.Join(cfg.RepoRoot, ".generated", "test.lock")
}

// EnsureVenv creates .venv and installs tests/requirements.txt if the venv
// doesn't exist yet, mirroring the old `make venv` target.
func EnsureVenv(cfg *config.Config) error {
	venv := filepath.Join(cfg.RepoRoot, ".venv")
	marker := filepath.Join(venv, "bin", "activate")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := run(cfg.RepoRoot, "python3", "-m", "venv", venv); err != nil {
		return err
	}
	pip := filepath.Join(venv, "bin", "pip")
	return run(cfg.RepoRoot, pip, "install", "--quiet", "-r", "tests/requirements.txt")
}

// Run executes the suite. smoke=true runs only health + registration tests.
// extra is passed straight through to pytest; if it names any test paths of
// its own, they replace the default `tests/` rather than adding to it, so
// `oak-dev test -- tests/test_03_deployment.py` runs just that file.
func Run(cfg *config.Config, smoke bool, extra ...string) error {
	if err := EnsureVenv(cfg); err != nil {
		return err
	}
	pytest := filepath.Join(cfg.RepoRoot, ".venv", "bin", "pytest")

	var args []string
	if !namesPaths(extra) {
		args = append(args, "tests/")
	}
	args = append(args, "-v")
	if smoke {
		args = append(args, "-m", "not deployment")
	}
	args = append(args, extra...)
	return run(cfg.RepoRoot, pytest, args...)
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

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
