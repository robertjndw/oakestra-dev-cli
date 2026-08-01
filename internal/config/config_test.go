package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example file is heavily commented, and auto-promotion rewrites it on a
// plain `oak-dev reload`/`debug`. A marshal round-trip would drop every
// comment, so this is the test that keeps AddLive on the yaml.Node path.
const commentedYAML = `# oak-dev's topology file.
oakestra_repo: ../oakestra

cluster:
  name: test-cluster       # inline comment
  location: "52.5200,13.4050,100"

# Tilt-style: run these from your working tree instead of the baked image.
live: [system_manager, cluster_manager]

# Named partial stacks.
stack: full
`

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, yamlFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadFrom(t *testing.T, dir string) *Config {
	t.Helper()
	// Load reads .env and the process env too; neither exists in a temp dir.
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestAddLivePreservesComments(t *testing.T) {
	dir := writeCfg(t, commentedYAML)
	cfg := loadFrom(t, dir)

	changed, err := cfg.AddLive("scheduler")
	if err != nil {
		t.Fatalf("AddLive: %v", err)
	}
	if !changed {
		t.Fatal("AddLive reported no change for a component that was not live")
	}

	out, err := os.ReadFile(filepath.Join(dir, yamlFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{
		"# oak-dev's topology file.",
		"# inline comment",
		"# Tilt-style: run these from your working tree instead of the baked image.",
		"# Named partial stacks.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comment %q was stripped by AddLive.\n--- got ---\n%s", want, got)
		}
	}

	if !strings.Contains(got, "scheduler") {
		t.Errorf("scheduler was not added.\n--- got ---\n%s", got)
	}

	// And the rewritten file must still load, with the new component live.
	reloaded := loadFrom(t, dir)
	for _, name := range []string{"system_manager", "cluster_manager", "scheduler"} {
		if !reloaded.IsLive(name) {
			t.Errorf("after AddLive + reload, %s is not live (live: %v)", name, reloaded.LiveNames())
		}
	}
}

func TestAddLiveIsIdempotentAndResolvesAliases(t *testing.T) {
	dir := writeCfg(t, commentedYAML)
	cfg := loadFrom(t, dir)

	// "sm" is system_manager, which is already live.
	changed, err := cfg.AddLive("sm")
	if err != nil {
		t.Fatalf("AddLive: %v", err)
	}
	if changed {
		t.Error("AddLive(\"sm\") reported a change, but system_manager was already live")
	}

	// An alias for something not yet live must add the canonical name.
	if _, err := cfg.AddLive("ne"); err != nil {
		t.Fatalf("AddLive: %v", err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, yamlFileName))
	if !strings.Contains(string(out), "nodeengine") {
		t.Errorf("alias was not canonicalised to nodeengine:\n%s", out)
	}
}

func TestAddLiveOnMissingKeyAndMissingFile(t *testing.T) {
	t.Run("key absent", func(t *testing.T) {
		dir := writeCfg(t, "oakestra_repo: ../oakestra\nstack: full\n")
		cfg := loadFrom(t, dir)
		if _, err := cfg.AddLive("scheduler"); err != nil {
			t.Fatalf("AddLive: %v", err)
		}
		if !loadFrom(t, dir).IsLive("scheduler") {
			t.Error("scheduler not live after adding to a file with no live: key")
		}
	})

	t.Run("empty value", func(t *testing.T) {
		dir := writeCfg(t, "live:\nstack: full\n")
		cfg := loadFrom(t, dir)
		if _, err := cfg.AddLive("scheduler"); err != nil {
			t.Fatalf("AddLive: %v", err)
		}
		if !loadFrom(t, dir).IsLive("scheduler") {
			t.Error("scheduler not live after adding to an empty live: key")
		}
	})

	t.Run("file absent", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadFrom(t, dir)
		if _, err := cfg.AddLive("scheduler"); err != nil {
			t.Fatalf("AddLive: %v", err)
		}
		if !loadFrom(t, dir).IsLive("scheduler") {
			t.Error("scheduler not live after AddLive created oak-dev.yaml")
		}
	})
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Run("unknown live entry", func(t *testing.T) {
		// Previously silently ignored, so the component just never went live.
		dir := writeCfg(t, "live: [node_engine]\n")
		if _, err := Load(dir); err == nil {
			t.Error("expected an error for an unknown live: entry, got nil")
		}
	})

	t.Run("unknown stack scope", func(t *testing.T) {
		// Previously made StackEnabled false for everything, so `up` started nothing.
		dir := writeCfg(t, "stack: rooot\n")
		if _, err := Load(dir); err == nil {
			t.Error("expected an error for an invalid stack: value, got nil")
		}
	})

	t.Run("alias in live is accepted", func(t *testing.T) {
		dir := writeCfg(t, "live: [cm, sched]\n")
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.IsLive("cluster_manager") || !cfg.IsLive("scheduler") {
			t.Errorf("aliases not canonicalised on load: %v", cfg.LiveNames())
		}
	})
}

func TestStackSource(t *testing.T) {
	dir := writeCfg(t, "stack: cluster\n")
	if got := loadFrom(t, dir).StackSource; got != yamlFileName {
		t.Errorf("StackSource = %q, want %q", got, yamlFileName)
	}

	if err := SetLastStack(dir, "worker"); err != nil {
		t.Fatal(err)
	}
	cfg := loadFrom(t, dir)
	if cfg.Stack != "worker" {
		t.Errorf("Stack = %q, want worker (sticky file should win over yaml)", cfg.Stack)
	}
	if !strings.Contains(cfg.StackSource, "sticky") {
		t.Errorf("StackSource = %q, want it to mention the sticky file", cfg.StackSource)
	}

	t.Setenv("OAK_DEV_STACK", "root")
	cfg = loadFrom(t, dir)
	if cfg.Stack != "root" || cfg.StackSource != "OAK_DEV_STACK" {
		t.Errorf("env should outrank the sticky file: got %q from %q", cfg.Stack, cfg.StackSource)
	}
}
