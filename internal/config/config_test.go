package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oak-dev/internal/components"
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

func TestRepoPathResolution(t *testing.T) {
	t.Run("defaults to a sibling checkout, resolved absolute", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadFrom(t, dir)

		wantOakestra, err := filepath.Abs(filepath.Join(dir, "..", "oakestra"))
		if err != nil {
			t.Fatal(err)
		}
		wantNet, err := filepath.Abs(filepath.Join(dir, "..", "oakestra-net"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OakestraRepo != wantOakestra {
			t.Errorf("OakestraRepo = %q, want %q", cfg.OakestraRepo, wantOakestra)
		}
		if cfg.OakestraNetRepo != wantNet {
			t.Errorf("OakestraNetRepo = %q, want %q", cfg.OakestraNetRepo, wantNet)
		}
		if !filepath.IsAbs(cfg.OakestraNetRepo) {
			t.Errorf("OakestraNetRepo = %q, want an absolute path", cfg.OakestraNetRepo)
		}
	})

	t.Run("oak-dev.yaml sets it, relative to repoRoot", func(t *testing.T) {
		dir := writeCfg(t, "oakestra_net_repo: ../my-oakestra-net\n")
		cfg := loadFrom(t, dir)

		want, err := filepath.Abs(filepath.Join(dir, "..", "my-oakestra-net"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OakestraNetRepo != want {
			t.Errorf("OakestraNetRepo = %q, want %q", cfg.OakestraNetRepo, want)
		}
	})

	t.Run("OAKESTRA_NET_REPO env outranks oak-dev.yaml", func(t *testing.T) {
		dir := writeCfg(t, "oakestra_net_repo: ../my-oakestra-net\n")
		t.Setenv("OAKESTRA_NET_REPO", "/tmp/somewhere-else")
		cfg := loadFrom(t, dir)

		if cfg.OakestraNetRepo != "/tmp/somewhere-else" {
			t.Errorf("OakestraNetRepo = %q, want the env override", cfg.OakestraNetRepo)
		}
	})

	t.Run("RepoPath and SourceDir route by Component.Repo", func(t *testing.T) {
		dir := t.TempDir()
		cfg := loadFrom(t, dir)

		if got := cfg.RepoPath(components.RepoOakestra); got != cfg.OakestraRepo {
			t.Errorf("RepoPath(RepoOakestra) = %q, want %q", got, cfg.OakestraRepo)
		}
		if got := cfg.RepoPath(components.RepoOakestraNet); got != cfg.OakestraNetRepo {
			t.Errorf("RepoPath(RepoOakestraNet) = %q, want %q", got, cfg.OakestraNetRepo)
		}

		nm, err := components.Resolve("netmanager")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(cfg.OakestraNetRepo, nm.SourcePath)
		if got := cfg.SourceDir(nm); got != want {
			t.Errorf("SourceDir(netmanager) = %q, want %q", got, want)
		}

		sched, err := components.Resolve("scheduler")
		if err != nil {
			t.Fatal(err)
		}
		want = filepath.Join(cfg.OakestraRepo, sched.SourcePath)
		if got := cfg.SourceDir(sched); got != want {
			t.Errorf("SourceDir(scheduler) = %q, want %q", got, want)
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

// TestLoadE2EPrecedence is the regression test for two bugs: .env never
// reached the E2E suite (config.loadDotEnv only filled a Go-side map that
// nothing read for the OAK_* vars), and `oak-dev config set e2e.*` was a
// silent no-op (it wrote oak-dev.yaml, but resolveE2E never read that value
// back). Exercises the full four-level chain - process env > .env >
// oak-dev.yaml > default - and checks Load and LoadE2E agree, since a
// standalone `go test -tags e2e ./e2e/...` must see the same settings as
// `oak-dev test`.
func TestLoadE2EPrecedence(t *testing.T) {
	dir := writeCfg(t, "e2e:\n  cluster_api: http://yaml:10100\n  ready_timeout: 55\n  deploy_timeout: 66\n")
	envFile := "OAK_ROOT_API=http://dotenv:10000\nOAK_READY_TIMEOUT=42\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFile), 0o644); err != nil {
		t.Fatal(err)
	}
	// The marker LoadE2E's caller (oakapi.LoadSettings) locates via FindRoot -
	// LoadE2E itself only needs .env and oak-dev.yaml, but keep the fixture
	// realistic.
	if err := os.MkdirAll(filepath.Join(dir, "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose", "worker.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// No process env set: a yaml value with nothing above it in the chain
	// must actually take effect - the regression case for the no-op bug.
	e2e := LoadE2E(dir)
	if e2e.ClusterAPI != "http://yaml:10100" {
		t.Errorf("ClusterAPI = %q, want the oak-dev.yaml value (not set in .env or env)", e2e.ClusterAPI)
	}
	if e2e.DeployTimeout != 66*time.Second {
		t.Errorf("DeployTimeout = %v, want 66s from oak-dev.yaml", e2e.DeployTimeout)
	}

	// .env must outrank oak-dev.yaml: ready_timeout is 55 in yaml, 42 in .env.
	if e2e.ReadyTimeout != 42*time.Second {
		t.Errorf("ReadyTimeout = %v, want 42s (.env should outrank oak-dev.yaml's 55)", e2e.ReadyTimeout)
	}
	// .env must outrank the built-in default too.
	if e2e.RootAPI != "http://dotenv:10000" {
		t.Errorf("RootAPI = %q, want the .env value (dotenv should outrank the default)", e2e.RootAPI)
	}
	// Set nowhere at all - must still fall back to the documented default.
	if e2e.RootRA != "http://localhost:11011" {
		t.Errorf("RootRA = %q, want the default (not set anywhere)", e2e.RootRA)
	}

	// Process env must outrank .env, same as every other setting in Load.
	t.Setenv("OAK_ROOT_API", "http://processenv:10000")
	e2e = LoadE2E(dir)
	if e2e.RootAPI != "http://processenv:10000" {
		t.Errorf("RootAPI = %q, want the process-env value to outrank .env", e2e.RootAPI)
	}

	// Load (cfg.E2E) must resolve identically to LoadE2E - the whole point
	// of sharing resolveE2E - so `oak-dev config`/`oak-dev test` and a
	// standalone `go test -tags e2e` never disagree about the same setting.
	cfg := loadFrom(t, dir)
	if cfg.E2E != e2e {
		t.Errorf("Load().E2E = %+v, want it to match LoadE2E() = %+v", cfg.E2E, e2e)
	}
}

// A yaml-only e2e.ready_timeout must resolve to the correct time.Duration,
// not just the correct int - a seconds-vs-nanoseconds slip here would make
// every wait either instant or absurdly long.
func TestLoadE2EYamlTimeoutResolvesToDuration(t *testing.T) {
	dir := writeCfg(t, "e2e:\n  ready_timeout: 7\n")
	e2e := LoadE2E(dir)
	if e2e.ReadyTimeout != 7*time.Second {
		t.Errorf("ReadyTimeout = %v, want 7s", e2e.ReadyTimeout)
	}
}

// A non-numeric timeout must degrade to the default rather than erroring -
// the E2E binary has no flag parsing to report a config error through, so a
// hard failure here would surface as an opaque panic instead of a fallback.
func TestLoadE2EInvalidTimeoutFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OAK_READY_TIMEOUT", "not-a-number")

	e2e := LoadE2E(dir)
	if e2e.ReadyTimeout != 180*time.Second {
		t.Errorf("ReadyTimeout = %v, want the 180s default for an invalid value", e2e.ReadyTimeout)
	}
}

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose", "worker.yml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "oakapi")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(nested)
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	// Resolve both sides through EvalSymlinks: on macOS t.TempDir() lives
	// under /var, a symlink to /private/var, and a naive string compare
	// would fail despite both paths naming the same directory.
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotResolved != wantRoot {
		t.Errorf("FindRoot(%q) = %q, want %q", nested, got, root)
	}

	// No marker anywhere up the tree - a moved/copied test binary must get a
	// clear error, not a silent wrong answer.
	if _, err := FindRoot(t.TempDir()); err == nil {
		t.Error("FindRoot with no compose/worker.yml anywhere up the tree = nil error, want one")
	}
}

func TestE2EConfigKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OAK_ROOT_API", "http://custom:10000")
	t.Setenv("OAK_READY_TIMEOUT", "99")
	cfg := loadFrom(t, dir)

	if got, err := cfg.Get("e2e.root_api"); err != nil || got != "http://custom:10000" {
		t.Errorf("Get(e2e.root_api) = (%q, %v), want the env override", got, err)
	}
	if got, err := cfg.Get("e2e.ready_timeout"); err != nil || got != "99" {
		t.Errorf("Get(e2e.ready_timeout) = (%q, %v), want %q", got, err, "99")
	}

	for _, key := range []string{
		"e2e.root_api", "e2e.cluster_api", "e2e.root_ra",
		"e2e.username", "e2e.password", "e2e.ready_timeout", "e2e.deploy_timeout",
	} {
		if _, err := cfg.Get(key); err != nil {
			t.Errorf("Get(%s): %v", key, err)
		}
	}
}

// TestE2EConfigSetRoundTrips is the direct reproduction of the reported
// no-op bug: `oak-dev config set e2e.root_api ...` wrote oak-dev.yaml
// successfully but a subsequent `get`/reload kept reporting the old value,
// because fileConfig had no E2E field for Load to read it back into.
func TestE2EConfigSetRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfg := loadFrom(t, dir)

	if _, err := cfg.Set("e2e.root_api", "http://localhost:19999"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := cfg.Set("e2e.ready_timeout", "45"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	reloaded := loadFrom(t, dir)
	if got, err := reloaded.Get("e2e.root_api"); err != nil || got != "http://localhost:19999" {
		t.Errorf("after Set+reload, Get(e2e.root_api) = (%q, %v), want the written value - Set must not be a silent no-op", got, err)
	}
	if reloaded.E2E.ReadyTimeout != 45*time.Second {
		t.Errorf("after Set+reload, E2E.ReadyTimeout = %v, want 45s", reloaded.E2E.ReadyTimeout)
	}
}

// The "seconds" kind backs e2e.ready_timeout/e2e.deploy_timeout - it must
// reject non-numeric and non-positive input with a message naming the
// invariant, the same contract "workers" already has.
func TestSecondsKindValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := loadFrom(t, dir)

	if _, err := cfg.Set("e2e.ready_timeout", "not-a-number"); err == nil {
		t.Error("Set(e2e.ready_timeout, \"not-a-number\") = nil error, want one")
	}
	if _, err := cfg.Set("e2e.ready_timeout", "0"); err == nil {
		t.Error("Set(e2e.ready_timeout, \"0\") = nil error, want one (must be at least 1)")
	}
	if _, err := cfg.Set("e2e.deploy_timeout", "300"); err != nil {
		t.Errorf("Set(e2e.deploy_timeout, \"300\"): %v", err)
	}
}
