package doctor

import (
	"testing"

	"oak-dev/internal/config"
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
