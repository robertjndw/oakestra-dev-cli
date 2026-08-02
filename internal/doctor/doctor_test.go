package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"oak-dev/internal/config"
	"oak-dev/internal/testsuite"
)

func TestParseMajorMinor(t *testing.T) {
	cases := []struct {
		in        string
		wantMajor int
		wantMinor int
	}{
		{"v2.29.7", 2, 29},
		{"2.18.0", 2, 18},
		{"2.17.3", 2, 17},
		{"garbage", 0, 0},
	}
	for _, c := range cases {
		major, minor := parseMajorMinor(c.in)
		if major != c.wantMajor || minor != c.wantMinor {
			t.Errorf("parseMajorMinor(%q) = %d.%d, want %d.%d", c.in, major, minor, c.wantMajor, c.wantMinor)
		}
	}
}

func TestCheckVenv(t *testing.T) {
	cfg := &config.Config{RepoRoot: t.TempDir()}

	if ok, detail, _ := checkVenv(cfg); ok {
		t.Errorf("checkVenv with no .venv = ok (%s), want a failure", detail)
	}

	// The regression: `pip install -r` dying partway leaves the packages it
	// already installed - pytest among them - behind, and doctor used to call
	// that a healthy venv and print "all required checks passed".
	bin := filepath.Join(cfg.RepoRoot, ".venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"activate", "pytest"} {
		if err := os.WriteFile(filepath.Join(bin, f), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if ok, detail, _ := checkVenv(cfg); ok {
		t.Errorf("checkVenv with an unfinished install = ok (%s), want a failure", detail)
	}

	if err := os.WriteFile(testsuite.VenvStamp(cfg.RepoRoot), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, detail, _ := checkVenv(cfg); !ok {
		t.Errorf("checkVenv with a completed install = not ok (%s)", detail)
	}
}

func TestRunReturnsOneResultPerCheck(t *testing.T) {
	// A deliberately bogus checkout path: every check must still return a
	// Result rather than panic - that's the whole point of doctor being
	// safe to run before anything else is set up.
	cfg := &config.Config{
		RepoRoot:     t.TempDir(),
		OakestraRepo: "/nonexistent/oakestra",
	}
	results := Run(cfg)
	if len(results) != len(checks) {
		t.Fatalf("got %d results, want %d (one per check)", len(results), len(checks))
	}
	if results[0].OK {
		t.Errorf("checkOakestraRepo should fail against a nonexistent checkout")
	}
}
