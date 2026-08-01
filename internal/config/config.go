// Package config resolves oak-dev's settings from oak-dev.yaml, .env, and
// the process environment, in that priority order (env wins - "Keep .env
// working as a manual override so nothing breaks mid-migration").
package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"oak-dev/internal/components"
)

type ClusterCfg struct {
	Name     string `yaml:"name"`
	Location string `yaml:"location"`
}

type ProfilesCfg struct {
	Dashboard     bool `yaml:"dashboard"`
	Observability bool `yaml:"observability"`
	Addons        bool `yaml:"addons"`
}

type VersionsCfg struct {
	NetManager string `yaml:"netmanager"` // "auto" or an explicit tag
	LibBranch  string `yaml:"lib_branch"`
}

// fileConfig is the raw shape of oak-dev.yaml.
type fileConfig struct {
	OakestraRepo string      `yaml:"oakestra_repo"`
	NetRepo      string      `yaml:"oakestra_net_repo"`
	LibsRepo     string      `yaml:"libs_repo"`
	Cluster      ClusterCfg  `yaml:"cluster"`
	Workers      int         `yaml:"workers"`
	Live         []string    `yaml:"live"`
	Stack        string      `yaml:"stack"`
	Profiles     ProfilesCfg `yaml:"profiles"`
	Versions     VersionsCfg `yaml:"versions"`
}

// Config is the fully resolved, ready-to-use configuration.
type Config struct {
	RepoRoot string // absolute path to this repo (oakestra-macos-testing checkout)

	OakestraRepo      string // absolute
	OakestraNetRepo   string // absolute
	LibsRepo          string // absolute, or "" if not set
	ClusterName       string
	ClusterAddr       string // container name the root reaches this cluster back on
	ClusterLoc        string
	Workers           int
	Live              map[string]bool
	Stack             string // full | root | cluster | worker
	StackSource       string // which precedence layer set Stack, for `scope:` output
	Profiles          ProfilesCfg
	LibBranch         string
	NetManagerVersion string
	SystemManagerURL  string
	GOARCH            string
}

const yamlFileName = "oak-dev.yaml"
const genDir = ".generated"
const stackStateFile = "stack"

// StackStatePath returns where SetLastStack persists the sticky stack scope.
func StackStatePath(repoRoot string) string {
	return filepath.Join(repoRoot, genDir, stackStateFile)
}

// SetLastStack persists the stack scope `up`/`down` actually acted on, so
// later commands (go, debug, status, ...) default to the same scope instead
// of oak-dev.yaml's static stack: - see the priority comment in Load.
func SetLastStack(repoRoot, stack string) error {
	if err := os.MkdirAll(filepath.Join(repoRoot, genDir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(StackStatePath(repoRoot), []byte(stack), 0o644)
}

// Load resolves configuration relative to repoRoot (the oakestra-macos-testing
// checkout - normally the process cwd).
func Load(repoRoot string) (*Config, error) {
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}

	fc := fileConfig{
		OakestraRepo: "../oakestra",
		NetRepo:      "../oakestra-net",
		Cluster:      ClusterCfg{Name: "test-cluster", Location: "52.5200,13.4050,100"},
		Workers:      1,
		Stack:        "full",
		Versions:     VersionsCfg{NetManager: "auto", LibBranch: "develop"},
	}

	if data, err := os.ReadFile(filepath.Join(repoRoot, yamlFileName)); err == nil {
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", yamlFileName, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	env := loadDotEnv(filepath.Join(repoRoot, ".env"))
	getenv := func(key, fallback string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		if v, ok := env[key]; ok && v != "" {
			return v
		}
		return fallback
	}

	oakestraRepo := getenv("OAKESTRA_REPO", fc.OakestraRepo)
	if !filepath.IsAbs(oakestraRepo) {
		oakestraRepo = filepath.Join(repoRoot, oakestraRepo)
	}
	oakestraRepo, err = filepath.Abs(oakestraRepo)
	if err != nil {
		return nil, err
	}

	oakestraNetRepo := getenv("OAKESTRA_NET_REPO", fc.NetRepo)
	if !filepath.IsAbs(oakestraNetRepo) {
		oakestraNetRepo = filepath.Join(repoRoot, oakestraNetRepo)
	}
	oakestraNetRepo, err = filepath.Abs(oakestraNetRepo)
	if err != nil {
		return nil, err
	}

	libsRepo := getenv("OAKESTRA_LIBS_REPO", fc.LibsRepo)
	if libsRepo != "" && !filepath.IsAbs(libsRepo) {
		libsRepo = filepath.Join(repoRoot, libsRepo)
	}

	netmanagerVersion := getenv("NETMANAGER_VERSION", "")
	if netmanagerVersion == "" {
		if fc.Versions.NetManager != "" && fc.Versions.NetManager != "auto" {
			netmanagerVersion = fc.Versions.NetManager
		} else {
			versionBytes, _ := os.ReadFile(filepath.Join(oakestraRepo, "version.txt"))
			v := strings.TrimSpace(string(versionBytes))
			if v == "" {
				v = "v0.4.411"
			}
			netmanagerVersion = "alpha-" + v
		}
	}

	// Priority: OAK_DEV_STACK env > the stack `up`/`down` last actually brought
	// up (.generated/stack, written by --stack) > oak-dev.yaml's stack: > full.
	// Without this, `oak-dev up --stack worker` followed by a plain `oak-dev
	// reload scheduler` would fall back to oak-dev.yaml's default and try to
	// touch a root stack that was never started.
	//
	// stackSource records which layer won so commands can print it. The scope
	// being invisible was itself a usability problem: the same command meant
	// different things depending on an unprinted file written by an earlier run.
	stack, stackSource := fc.Stack, yamlFileName
	if stack == "" {
		stackSource = "default"
	}
	if last, err := os.ReadFile(StackStatePath(repoRoot)); err == nil {
		if s := strings.TrimSpace(string(last)); s != "" {
			stack, stackSource = s, "sticky, from an earlier up"
		}
	}
	if s := getenv("OAK_DEV_STACK", ""); s != "" {
		stack, stackSource = s, "OAK_DEV_STACK"
	}
	if stack == "" {
		stack, stackSource = components.ScopeFull, "default"
	}
	if !components.ValidScope(stack) {
		return nil, fmt.Errorf("invalid stack scope %q (%s), valid values: %s",
			stack, stackSource, strings.Join(components.Scopes(), ", "))
	}

	// Resolve live: entries through the component registry so a typo is a hard
	// error instead of silently doing nothing, and so aliases work in the file
	// too. Keys are stored canonically; IsLive is an exact lookup.
	live := map[string]bool{}
	for _, name := range fc.Live {
		c, err := components.Resolve(name)
		if err != nil {
			return nil, fmt.Errorf("%s: live: %w", yamlFileName, err)
		}
		live[c.Name] = true
	}

	cfg := &Config{
		RepoRoot:          repoRoot,
		OakestraRepo:      oakestraRepo,
		OakestraNetRepo:   oakestraNetRepo,
		LibsRepo:          libsRepo,
		ClusterName:       getenv("CLUSTER_NAME", fc.Cluster.Name),
		ClusterAddr:       getenv("CLUSTER_ADDRESS", "cluster_manager"),
		ClusterLoc:        getenv("CLUSTER_LOCATION", fc.Cluster.Location),
		Workers:           fc.Workers,
		Live:              live,
		Stack:             stack,
		StackSource:       stackSource,
		Profiles:          fc.Profiles,
		LibBranch:         getenv("LIB_BRANCH", fc.Versions.LibBranch),
		NetManagerVersion: netmanagerVersion,
		SystemManagerURL:  getenv("SYSTEM_MANAGER_URL", "system_manager"),
		GOARCH:            goarch(),
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	return cfg, nil
}

// IsLive reports whether the named component should run from mounted source.
// name must be canonical - resolve through components.Resolve first.
func (c *Config) IsLive(name string) bool {
	return c.Live[name]
}

// RepoPath returns the absolute checkout path for the given component repo.
func (c *Config) RepoPath(r components.Repo) string {
	if r == components.RepoOakestraNet {
		return c.OakestraNetRepo
	}
	return c.OakestraRepo
}

// SourceDir returns the absolute path to a component's source tree - its
// SourcePath resolved against the checkout named by its Repo. This is the one
// place that join happens, so build/watch/shortSrc never have to know there's
// more than one possible repo.
func (c *Config) SourceDir(comp components.Component) string {
	return filepath.Join(c.RepoPath(comp.Repo), comp.SourcePath)
}

// LiveNames returns the live set in a stable order. cfg.Live is a map, so
// ranging it directly produces output that reorders between runs.
func (c *Config) LiveNames() []string {
	out := make([]string, 0, len(c.Live))
	for name := range c.Live {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SetStack overrides the resolved scope, recording where the override came
// from so `scope:` output stays honest.
func (c *Config) SetStack(stack, source string) error {
	if !components.ValidScope(stack) {
		return fmt.Errorf("invalid stack scope %q, valid values: %s",
			stack, strings.Join(components.Scopes(), ", "))
	}
	c.Stack, c.StackSource = stack, source
	return nil
}

// ScopeLine is the one-line scope banner commands print before doing work.
func (c *Config) ScopeLine() string {
	return fmt.Sprintf("scope: %s (%s)", c.Stack, c.StackSource)
}

// AddLive adds a component to oak-dev.yaml's live: list and to the in-memory
// config.
//
// Reports whether the file actually changed.
func (c *Config) AddLive(name string) (bool, error) {
	comp, err := components.Resolve(name)
	if err != nil {
		return false, err
	}
	if c.Live[comp.Name] {
		return false, nil
	}

	err = editYAMLFile(c.RepoRoot, []byte("# Created by oak-dev.\nlive: []\n"), func(doc *yaml.Node) error {
		return appendToSequence(doc, "live", comp.Name)
	})
	if err != nil {
		return false, err
	}

	c.Live[comp.Name] = true
	return true, nil
}

// editYAMLFile reads oak-dev.yaml (or placeholder if it doesn't exist yet),
// applies mutate to the parsed document, and writes the result back. Edits go
// through the yaml.Node API rather than a marshal round-trip, because
// oak-dev.yaml is a hand-written, heavily commented file and a round-trip
// would silently strip every comment in it. Shared by AddLive and Set, the
// only two things that write oak-dev.yaml programmatically.
func editYAMLFile(repoRoot string, placeholder []byte, mutate func(*yaml.Node) error) error {
	path := filepath.Join(repoRoot, yamlFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		data = placeholder
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", yamlFileName, err)
	}
	if err := mutate(&doc); err != nil {
		return fmt.Errorf("updating %s: %w", yamlFileName, err)
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// ensureTopLevelMap normalizes doc into a document whose root is a mapping -
// an empty or comment-only file unmarshals to a zero node - and returns that
// root. Shared by appendToSequence and setScalarPath, the two yaml.Node
// mutators editYAMLFile's callers use.
func ensureTopLevelMap(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a top-level mapping, got %v", root.Kind)
	}
	return root, nil
}

// appendToSequence adds value to the sequence stored under key in the
// document's top-level mapping, creating the key or the sequence if needed.
func appendToSequence(doc *yaml.Node, key, value string) error {
	root, err := ensureTopLevelMap(doc)
	if err != nil {
		return err
	}

	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}

	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		val := root.Content[i+1]
		switch val.Kind {
		case yaml.SequenceNode:
			val.Content = append(val.Content, scalar)
		default:
			// `live:` with no value parses as a null scalar.
			*val = yaml.Node{
				Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle,
				Content: []*yaml.Node{scalar},
			}
		}
		return nil
	}

	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle,
			Content: []*yaml.Node{scalar}},
	)
	return nil
}

// StackEnabled reports whether the given compose stack (root|cluster|worker)
// is part of the currently selected `stack:` scope.
func (c *Config) StackEnabled(stack string) bool {
	switch c.Stack {
	case "", "full":
		return true
	case "worker":
		// worker stack needs cluster (registration target) but not root
		return stack == "cluster" || stack == "worker"
	default:
		return c.Stack == stack
	}
}

// NormalizeArch maps `uname -m`/`docker info` spellings to Go's GOARCH
// values ("x86_64" -> "amd64", "aarch64" -> "arm64") - the mismatch
// CLAUDE.md's Gotchas section warns must be reconciled before comparing.
func NormalizeArch(arch string) string {
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

func goarch() string {
	out, err := exec.Command("uname", "-m").Output()
	arch := strings.TrimSpace(string(out))
	if err != nil {
		arch = "arm64"
	}
	return NormalizeArch(arch)
}

func loadDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		out[key] = val
	}
	_ = scanner.Err() // best-effort: a truncated .env just yields fewer keys
	return out
}
