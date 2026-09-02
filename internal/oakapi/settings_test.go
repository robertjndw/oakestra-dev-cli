package oakapi

import (
	"os"
	"path/filepath"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatal(err)
		}
	})
}

// LoadSettings must find the checkout root by walking up for
// compose/worker.yml and read its .env from there, even when the process
// cwd is a subdirectory - the shape `go test` actually runs in, since a
// package's test binary runs with its source directory as cwd.
func TestLoadSettingsFindsRootAndReadsDotEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose", "worker.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("OAK_ROOT_API=http://from-dotenv:10000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(root, "internal", "oakapi")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, nested)

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if settings.RootAPI != "http://from-dotenv:10000" {
		t.Errorf("RootAPI = %q, want the .env value found by walking up to %s", settings.RootAPI, root)
	}
}

// A test binary built with `go test -c` and copied elsewhere has no
// compose/worker.yml above it anywhere - LoadSettings must degrade to
// process-env-plus-defaults rather than erroring out.
func TestLoadSettingsFallsBackWhenRootNotFound(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("OAK_ROOT_API", "http://from-env:10000")

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v, want a graceful fallback instead of an error", err)
	}
	if settings.RootAPI != "http://from-env:10000" {
		t.Errorf("RootAPI = %q, want the process-env value", settings.RootAPI)
	}
	if settings.ClusterAPI != "http://localhost:10100" {
		t.Errorf("ClusterAPI = %q, want the built-in default", settings.ClusterAPI)
	}
}
