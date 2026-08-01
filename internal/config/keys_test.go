package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSetPreservesComments(t *testing.T) {
	dir := writeCfg(t, commentedYAML)
	cfg := loadFrom(t, dir)

	if _, err := cfg.Set("cluster.name", "new-cluster"); err != nil {
		t.Fatalf("Set: %v", err)
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
			t.Errorf("comment %q was stripped by Set.\n--- got ---\n%s", want, got)
		}
	}
	if !strings.Contains(got, "new-cluster") {
		t.Errorf("new value not written.\n--- got ---\n%s", got)
	}

	reloaded := loadFrom(t, dir)
	if reloaded.ClusterName != "new-cluster" {
		t.Errorf("ClusterName = %q after reload, want new-cluster", reloaded.ClusterName)
	}
}

func TestConfigSetCreatesMissingNesting(t *testing.T) {
	dir := writeCfg(t, "oakestra_repo: ../oakestra\n")
	cfg := loadFrom(t, dir)

	if _, err := cfg.Set("profiles.dashboard", "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	reloaded := loadFrom(t, dir)
	if !reloaded.Profiles.Dashboard {
		t.Error("profiles.dashboard not set to true after reload")
	}
}

func TestConfigSetOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := loadFrom(t, dir)

	if _, err := cfg.Set("workers", "3"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if loadFrom(t, dir).Workers != 3 {
		t.Error("workers not persisted after Set created oak-dev.yaml")
	}
}

func TestConfigSetValidatesValues(t *testing.T) {
	dir := writeCfg(t, "oakestra_repo: ../oakestra\n")
	cfg := loadFrom(t, dir)

	cases := []struct {
		key, value string
	}{
		{"workers", "0"},
		{"workers", "nope"},
		{"profiles.dashboard", "maybe"},
		{"stack", "rooot"},
		{"no.such.key", "x"},
	}
	for _, c := range cases {
		if _, err := cfg.Set(c.key, c.value); err == nil {
			t.Errorf("Set(%q, %q) succeeded, want an error", c.key, c.value)
		}
	}
}

func TestConfigGetUnknownKey(t *testing.T) {
	dir := writeCfg(t, "oakestra_repo: ../oakestra\n")
	cfg := loadFrom(t, dir)

	if _, err := cfg.Get("no.such.key"); err == nil {
		t.Error("Get on an unknown key returned no error")
	}
}

func TestConfigSetReportsEnvOverride(t *testing.T) {
	dir := writeCfg(t, "cluster:\n  name: test-cluster\n")
	t.Setenv("CLUSTER_NAME", "env-cluster")
	cfg := loadFrom(t, dir)

	envOverride, err := cfg.Set("cluster.name", "file-cluster")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if envOverride != "CLUSTER_NAME" {
		t.Errorf("envOverride = %q, want CLUSTER_NAME", envOverride)
	}
}

func TestConfigSetNoEnvOverrideWhenUnset(t *testing.T) {
	dir := writeCfg(t, "cluster:\n  name: test-cluster\n")
	cfg := loadFrom(t, dir)

	envOverride, err := cfg.Set("cluster.name", "file-cluster")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if envOverride != "" {
		t.Errorf("envOverride = %q, want empty", envOverride)
	}
}
