package testsuite

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{RepoRoot: t.TempDir()}
}

// mkVenv creates the .venv directory itself, which is what the stamp is
// written into. Pass activate to also mark the interpreter as already created.
func mkVenv(t *testing.T, cfg *config.Config, activate bool) {
	t.Helper()
	venv := filepath.Join(cfg.RepoRoot, ".venv")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if activate {
		if err := os.WriteFile(filepath.Join(venv, "bin", "activate"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureVenvSkipsWhenStamped(t *testing.T) {
	cfg := testConfig(t)
	mkVenv(t, cfg, true)
	if err := os.WriteFile(VenvStamp(cfg.RepoRoot), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := proc.NewRecorder()
	if err := New(cfg, rec).EnsureVenv(); err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}
	if got := rec.Commands(); len(got) != 0 {
		t.Errorf("ran %v, want nothing - the stamp means the install already finished", got)
	}
}

func TestEnsureVenvCreatesAndInstalls(t *testing.T) {
	cfg := testConfig(t)
	mkVenv(t, cfg, false) // the directory exists but the interpreter does not

	rec := proc.NewRecorder()
	if err := New(cfg, rec).EnsureVenv(); err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}

	want := []string{
		"python3 -m venv .venv",
		".venv/bin/pip install --quiet -r tests/requirements.txt",
	}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if _, err := os.Stat(VenvStamp(cfg.RepoRoot)); err != nil {
		t.Error("stamp not written after a successful install")
	}
}

// An existing interpreter is reused; only the requirements are installed.
func TestEnsureVenvReusesExistingInterpreter(t *testing.T) {
	cfg := testConfig(t)
	mkVenv(t, cfg, true)

	rec := proc.NewRecorder()
	if err := New(cfg, rec).EnsureVenv(); err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}

	want := []string{".venv/bin/pip install --quiet -r tests/requirements.txt"}
	if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

// The stamp is the whole point: a pip run that dies after installing pytest
// but before the rest leaves that binary behind, so its presence proves
// nothing. Only a completed install may be recorded, or every later run skips
// the install and calls a broken venv finished.
func TestEnsureVenvDoesNotStampAFailedInstall(t *testing.T) {
	cfg := testConfig(t)
	mkVenv(t, cfg, true)

	boom := errors.New("boom")
	rec := proc.NewRecorder()
	rec.OnContains("pip install", nil, boom)

	if err := New(cfg, rec).EnsureVenv(); !errors.Is(err, boom) {
		t.Fatalf("EnsureVenv err = %v, want %v", err, boom)
	}
	if _, err := os.Stat(VenvStamp(cfg.RepoRoot)); err == nil {
		t.Error("stamp written despite a failed install - the next run would inherit a broken venv")
	}
	if VenvReady(cfg) {
		t.Error("VenvReady = true after a failed install")
	}
}

func TestRunPytestArgs(t *testing.T) {
	tests := []struct {
		name  string
		smoke bool
		extra []string
		want  string
	}{
		{
			name: "full suite",
			want: ".venv/bin/pytest tests/ -v",
		},
		{
			name:  "smoke skips the deployment tests",
			smoke: true,
			want:  ".venv/bin/pytest tests/ -v -m not deployment",
		},
		{
			// A named path replaces tests/ rather than adding to it, so
			// `oak-dev test -- tests/test_03_deployment.py` runs just that file.
			name:  "an explicit path replaces the default",
			extra: []string{"tests/test_03_deployment.py", "-k", "scale"},
			want:  ".venv/bin/pytest -v tests/test_03_deployment.py -k scale",
		},
		{
			// A bare flag is not a path, so the default target survives.
			name:  "a bare flag keeps the default target",
			extra: []string{"-x"},
			want:  ".venv/bin/pytest tests/ -v -x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			mkVenv(t, cfg, true)
			if err := os.WriteFile(VenvStamp(cfg.RepoRoot), nil, 0o644); err != nil {
				t.Fatal(err)
			}

			rec := proc.NewRecorder()
			if err := New(cfg, rec).Run(tt.smoke, tt.extra...); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("commands = %v, want [%q]", got, tt.want)
			}
			if got, want := rec.Specs[0].Dir, cfg.RepoRoot; got != want {
				t.Errorf("Dir = %q, want the repo root %q - pytest.ini and tests/ resolve against it", got, want)
			}
		})
	}
}

// The lock is what stops `oak-dev dev`'s watchers restarting the worker
// mid-suite, which would strand every scheduled instance on a stale node ID.
func TestLockLifecycle(t *testing.T) {
	cfg := testConfig(t)

	if IsLocked(cfg) {
		t.Error("IsLocked = true before any lock")
	}
	if err := Lock(cfg); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if !IsLocked(cfg) {
		t.Error("IsLocked = false after Lock")
	}
	if err := Unlock(cfg); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if IsLocked(cfg) {
		t.Error("IsLocked = true after Unlock")
	}
	// Unlocking twice is not an error: `test` defers it, and a run that never
	// locked must not fail on the way out.
	if err := Unlock(cfg); err != nil {
		t.Errorf("second Unlock: %v", err)
	}
}
