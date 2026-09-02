package testsuite

import (
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{RepoRoot: t.TempDir()}
}

// testConfig's cfg.E2E is the zero value, so this also pins the floor:
// derivedTimeout must fall back to the 180s/300s defaults rather than
// deriving from 0, which would otherwise produce a useless -timeout 0s.
func TestRunGoTestArgs(t *testing.T) {
	const base = "go test -tags e2e ./e2e/... -v -count=1 -timeout 46m0s"

	tests := []struct {
		name  string
		smoke bool
		extra []string
		want  string
	}{
		{
			name: "full suite",
			want: base,
		},
		{
			name:  "smoke adds the TestSmoke run filter",
			smoke: true,
			want:  base + " -run ^TestSmoke",
		},
		{
			name:  "passthrough is appended last",
			extra: []string{"-x"},
			want:  base + " -x",
		},
		{
			// The test binary uses the standard flag package, where the last
			// occurrence of a repeated flag wins - so a user -run after
			// --smoke narrows further rather than being shadowed by it.
			name:  "a user -run after --smoke: both present, user's last",
			smoke: true,
			extra: []string{"-run", "TestSmokeHealth"},
			want:  base + " -run ^TestSmoke -run TestSmokeHealth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			rec := proc.NewRecorder()
			if err := New(cfg, rec).Run(tt.smoke, tt.extra...); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := rec.CommandsRel(cfg.RepoRoot); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("commands = %v, want [%q]", got, tt.want)
			}
		})
	}
}

// OAK_* variables reach the suite only through the process environment
// (internal/config.LoadE2E reads os.LookupEnv directly), so a non-nil Spec.Env
// here would silently break the process env > .env precedence those settings
// rely on.
func TestRunEnvIsInherited(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()
	if err := New(cfg, rec).Run(false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Specs[0].Env != nil {
		t.Errorf("Env = %v, want nil so the process environment is inherited unchanged", rec.Specs[0].Env)
	}
}

// pytest.ini and tests/ used to resolve relative to the repo root; go test's
// package pattern (./e2e/...) is resolved the same way, against Dir.
func TestRunDirIsRepoRoot(t *testing.T) {
	cfg := testConfig(t)
	rec := proc.NewRecorder()
	if err := New(cfg, rec).Run(false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := rec.Specs[0].Dir, cfg.RepoRoot; got != want {
		t.Errorf("Dir = %q, want the repo root %q", got, want)
	}
}

func TestRejectsPytestArgs(t *testing.T) {
	rejected := []struct {
		name  string
		extra []string
	}{
		{"a bare path", []string{"tests/test_03_deployment.py"}},
		{"-k", []string{"-k", "scale"}},
		{"-m", []string{"-m", "not deployment"}},
		{"--collect-only", []string{"--collect-only"}},
		// A pytest path is rejected even sitting where a boolean flag's
		// value would go - rule order matters here, see rejectPytestArgs.
		{"a pytest path after a boolean flag", []string{"-v", "tests/test_03_deployment.py"}},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			rec := proc.NewRecorder()
			if err := New(cfg, rec).Run(false, tt.extra...); err == nil {
				t.Fatalf("Run(%v) = nil error, want one", tt.extra)
			}
			if got := rec.Commands(); len(got) != 0 {
				t.Errorf("ran %v, want nothing - a rejected arg must never reach go test", got)
			}
		})
	}

	forwarded := []struct {
		name  string
		extra []string
	}{
		{"-x", []string{"-x"}},
		{"-count=2", []string{"-count=2"}},
		{"-run", []string{"-run", "Foo"}},
		// None of these are in any enumerated flag list - they must pass on
		// the generic "did the previous token start with - and have no ="
		// rule, not on knowing these specific flag names.
		{"-list .", []string{"-list", "."}},
		{"-run with a subtest path", []string{"-run", "TestDeploymentLifecycle/scale"}},
		{"-timeout 90m", []string{"-timeout", "90m"}},
		{"-failfast", []string{"-failfast"}},
		{"-bench .", []string{"-bench", "."}},
		{"-shuffle on", []string{"-shuffle", "on"}},
	}
	for _, tt := range forwarded {
		t.Run("forwards "+tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			rec := proc.NewRecorder()
			if err := New(cfg, rec).Run(false, tt.extra...); err != nil {
				t.Fatalf("Run(%v): %v", tt.extra, err)
			}
			if got := rec.Commands(); len(got) != 1 {
				t.Errorf("ran %v, want exactly one command", got)
			}
		})
	}
}

// The teaching error text tells the user what to type instead - an error
// message that recommends a command its own gate then rejects would be worse
// than no message at all. This reads the recommendations back out of the
// live error text (rather than a hand-copied list) so the two can't drift:
// editing the message without also fixing rejectPytestArgs fails this test.
func TestTeachingRecommendationsAreThemselvesAccepted(t *testing.T) {
	err := rejectPytestArgs([]string{"--collect-only"})
	if err == nil {
		t.Fatal("expected an error to read the teaching text out of")
	}

	found := 0
	for _, line := range strings.Split(err.Error(), "\n") {
		_, rhs, ok := strings.Cut(line, "-> ")
		if !ok {
			continue
		}
		if rhs == "--smoke" {
			continue // an oak-dev flag, not a go test one - rejectPytestArgs never sees it
		}
		found++

		fields := strings.Fields(rhs)
		for i, f := range fields {
			fields[i] = strings.Trim(f, `'"`)
		}
		if err := rejectPytestArgs(fields); err != nil {
			t.Errorf("teaching text recommends %q, but rejectPytestArgs rejects it: %v", rhs, err)
		}
	}
	if found == 0 {
		t.Fatal("found no \"-> ...\" recommendations to check - did the teaching text change shape?")
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
