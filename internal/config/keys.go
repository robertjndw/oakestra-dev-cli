package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"oak-dev/internal/components"
)

// settableKey describes one oak-dev.yaml scalar that `oak-dev config`
// exposes for direct reading and writing.
type settableKey struct {
	path []string // dotted path into oak-dev.yaml, e.g. []string{"cluster", "name"}
	// envVar is the .env/process-env variable that overrides this key, per
	// the priority order documented on Load. Empty if none exists.
	envVar string
	kind   string // "string" | "workers" | "bool" | "stack"
	// get reads the resolved value (the same one Load produced) off a
	// Config, for `oak-dev config get`.
	get func(*Config) string
}

// configKeys is every oak-dev.yaml key `oak-dev config get`/`set` accepts.
// live: is deliberately not here - `reload`/`debug` already manage it, and a
// bare list of component names doesn't fit a single scalar value. Anything
// else is still editable by hand in oak-dev.yaml.
var configKeys = map[string]settableKey{
	"oakestra_repo":          {path: []string{"oakestra_repo"}, envVar: "OAKESTRA_REPO", kind: "string", get: func(c *Config) string { return c.OakestraRepo }},
	"libs_repo":              {path: []string{"libs_repo"}, envVar: "OAKESTRA_LIBS_REPO", kind: "string", get: func(c *Config) string { return c.LibsRepo }},
	"cluster.name":           {path: []string{"cluster", "name"}, envVar: "CLUSTER_NAME", kind: "string", get: func(c *Config) string { return c.ClusterName }},
	"cluster.location":       {path: []string{"cluster", "location"}, envVar: "CLUSTER_LOCATION", kind: "string", get: func(c *Config) string { return c.ClusterLoc }},
	"workers":                {path: []string{"workers"}, kind: "workers", get: func(c *Config) string { return strconv.Itoa(c.Workers) }},
	"stack":                  {path: []string{"stack"}, envVar: "OAK_DEV_STACK", kind: "stack", get: func(c *Config) string { return c.Stack }},
	"profiles.dashboard":     {path: []string{"profiles", "dashboard"}, kind: "bool", get: func(c *Config) string { return strconv.FormatBool(c.Profiles.Dashboard) }},
	"profiles.observability": {path: []string{"profiles", "observability"}, kind: "bool", get: func(c *Config) string { return strconv.FormatBool(c.Profiles.Observability) }},
	"profiles.addons":        {path: []string{"profiles", "addons"}, kind: "bool", get: func(c *Config) string { return strconv.FormatBool(c.Profiles.Addons) }},
	"versions.netmanager":    {path: []string{"versions", "netmanager"}, envVar: "NETMANAGER_VERSION", kind: "string", get: func(c *Config) string { return c.NetManagerVersion }},
	"versions.lib_branch":    {path: []string{"versions", "lib_branch"}, envVar: "LIB_BRANCH", kind: "string", get: func(c *Config) string { return c.LibBranch }},
	"e2e.root_api":           {path: []string{"e2e", "root_api"}, envVar: "OAK_ROOT_API", kind: "string", get: func(c *Config) string { return c.E2E.RootAPI }},
	"e2e.cluster_api":        {path: []string{"e2e", "cluster_api"}, envVar: "OAK_CLUSTER_API", kind: "string", get: func(c *Config) string { return c.E2E.ClusterAPI }},
	"e2e.root_ra":            {path: []string{"e2e", "root_ra"}, envVar: "OAK_ROOT_RA", kind: "string", get: func(c *Config) string { return c.E2E.RootRA }},
	"e2e.username":           {path: []string{"e2e", "username"}, envVar: "OAK_USERNAME", kind: "string", get: func(c *Config) string { return c.E2E.Username }},
	"e2e.password":           {path: []string{"e2e", "password"}, envVar: "OAK_PASSWORD", kind: "string", get: func(c *Config) string { return c.E2E.Password }},
	"e2e.ready_timeout":      {path: []string{"e2e", "ready_timeout"}, envVar: "OAK_READY_TIMEOUT", kind: "seconds", get: func(c *Config) string { return strconv.Itoa(int(c.E2E.ReadyTimeout.Seconds())) }},
	"e2e.deploy_timeout":     {path: []string{"e2e", "deploy_timeout"}, envVar: "OAK_DEPLOY_TIMEOUT", kind: "seconds", get: func(c *Config) string { return strconv.Itoa(int(c.E2E.DeployTimeout.Seconds())) }},
}

// ConfigKeys returns every key `oak-dev config` accepts, sorted.
func ConfigKeys() []string {
	out := make([]string, 0, len(configKeys))
	for k := range configKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unknownKeyError(key string) error {
	return fmt.Errorf("unknown config key %q, valid keys: %s", key, strings.Join(ConfigKeys(), ", "))
}

// Get returns the resolved value of key - the same value Load produced,
// so it reflects any .env/process-env override, not just what's on disk.
func (c *Config) Get(key string) (string, error) {
	sk, ok := configKeys[key]
	if !ok {
		return "", unknownKeyError(key)
	}
	return sk.get(c), nil
}

// Set writes value into oak-dev.yaml at key, preserving the file's comments
// and formatting - the same reasoning as AddLive: this file is hand-written
// and heavily commented, so a marshal round-trip would strip it bare.
//
// It reports envOverride, the name of a .env/process-env variable that
// currently outranks the file per Load's precedence - the write still
// succeeds, but has no effect until that override is unset.
func (c *Config) Set(key, value string) (envOverride string, err error) {
	sk, ok := configKeys[key]
	if !ok {
		return "", unknownKeyError(key)
	}

	tag, normalized, err := sk.encode(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}

	err = editYAMLFile(c.RepoRoot, []byte("# Created by oak-dev.\n"), func(doc *yaml.Node) error {
		return setScalarPath(doc, sk.path, tag, normalized)
	})
	if err != nil {
		return "", err
	}

	if sk.envVar != "" {
		if v, ok := os.LookupEnv(sk.envVar); ok && v != "" {
			envOverride = sk.envVar
		} else if v, ok := loadDotEnv(filepath.Join(c.RepoRoot, ".env"))[sk.envVar]; ok && v != "" {
			envOverride = sk.envVar
		}
	}
	return envOverride, nil
}

// StickyStack returns the stack scope persisted by SetLastStack, if any. It
// outranks oak-dev.yaml's stack: (see the priority comment in Load), so
// `oak-dev config set stack` needs it to tell whether the write it just made
// will actually take effect.
func StickyStack(repoRoot string) (string, bool) {
	data, err := os.ReadFile(StackStatePath(repoRoot))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	return s, s != ""
}

// encode validates value against the key's type and returns the YAML tag and
// normalized string to store.
func (sk settableKey) encode(value string) (tag, normalized string, err error) {
	switch sk.kind {
	case "workers":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", "", fmt.Errorf("%q is not a whole number", value)
		}
		if n < 1 {
			return "", "", fmt.Errorf("must be at least 1, got %d", n)
		}
		return "!!int", strconv.Itoa(n), nil
	case "bool":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", "", fmt.Errorf("%q is not true/false", value)
		}
		return "!!bool", strconv.FormatBool(b), nil
	case "seconds":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", "", fmt.Errorf("%q is not a whole number of seconds", value)
		}
		if n < 1 {
			return "", "", fmt.Errorf("must be at least 1, got %d", n)
		}
		return "!!int", strconv.Itoa(n), nil
	case "stack":
		if !components.ValidScope(value) {
			return "", "", fmt.Errorf("invalid stack scope %q, valid values: %s",
				value, strings.Join(components.Scopes(), ", "))
		}
		return "!!str", value, nil
	default:
		return "!!str", value, nil
	}
}

// setScalarPath sets the scalar at path within doc's top-level mapping,
// creating intermediate mappings as needed. Shares AddLive's rationale for
// editing through yaml.Node rather than a marshal round-trip.
func setScalarPath(doc *yaml.Node, path []string, tag, value string) error {
	node, err := ensureTopLevelMap(doc)
	if err != nil {
		return err
	}

	for depth, key := range path {
		last := depth == len(path)-1

		var val *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				val = node.Content[i+1]
				break
			}
		}

		if last {
			if val != nil {
				val.Kind, val.Tag, val.Value, val.Content, val.Style = yaml.ScalarNode, tag, value, nil, 0
				return nil
			}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
			return nil
		}

		if val == nil {
			val = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
		} else if val.Kind != yaml.MappingNode {
			val.Kind, val.Tag, val.Content = yaml.MappingNode, "!!map", nil
		}
		node = val
	}
	return nil
}
