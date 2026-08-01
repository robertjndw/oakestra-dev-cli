// Package components is the single source of truth mapping each Oakestra
// service to its language, source location, container, build command and
// debug port. Every dispatching subcommand (dev, reload, debug, test, status)
// reads from this registry instead of re-encoding the mapping.
package components

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type Kind string

const (
	KindPython Kind = "python"
	KindGo     Kind = "go"
)

// Stack names, matching the three compose projects.
const (
	StackRoot    = "root"
	StackCluster = "cluster"
	StackWorker  = "worker"
)

// ScopeFull is the stack scope meaning "every stack". It is a legal value of
// --stack and of oak-dev.yaml's stack:, but it is not itself a stack.
const ScopeFull = "full"

// Stacks returns the three real stack names, in start order.
func Stacks() []string { return []string{StackRoot, StackCluster, StackWorker} }

// Scopes returns every legal --stack / stack: value.
func Scopes() []string { return []string{ScopeFull, StackRoot, StackCluster, StackWorker} }

// ValidScope reports whether s is a legal stack scope. An unknown scope used
// to silently disable every stack, so `up` would quietly start nothing.
func ValidScope(s string) bool {
	return slices.Contains(Scopes(), s)
}

// Target is one running instance of a component: a single container in a
// single compose stack. Most components have exactly one; scheduler has two
// (root_scheduler, cluster_scheduler) sharing one cross-compiled binary.
type Target struct {
	Stack string // root | cluster | worker
	// Container is the service/container name in that stack's compose file.
	Container string
	// Module is the gunicorn "module:app" import target. Empty for Go targets.
	Module string
	// Port is the port gunicorn binds inside the container (Python only).
	Port int
	// LiveOverride is the checked-in compose fragment (relative to compose/)
	// applied when this target is in the active `live:` set. Empty means no
	// live-mount exists for this target (nothing to bind-mount over).
	LiveOverride string
	// DebugOverride is the checked-in compose fragment (relative to compose/)
	// layering a debugger on top of the live mount. Held here rather than
	// derived by string-formatting the container name at the call site, so a
	// missing fragment is caught by the registry instead of surfacing as a
	// raw `docker compose` error.
	DebugOverride string
	// DebugPort is the host-published port for `oak-dev debug`.
	DebugPort int
}

// Component is one editable piece of Oakestra source.
type Component struct {
	Name string
	// Aliases are short forms accepted anywhere Name is. Resolve also accepts
	// any unambiguous prefix of a name or alias, so these exist to give the
	// long snake_case names a two- or three-letter spelling worth typing.
	Aliases []string
	Kind    Kind
	// SourcePath is relative to $OAKESTRA_REPO.
	SourcePath string
	// GoMain is the main package/file to build, relative to SourcePath.
	// Only set for Kind == KindGo with a single build unit (scheduler).
	GoMain string
	// BinName is the output binary name under build/linux_<arch>/.
	BinName string
	Targets []Target
}

// NodeEngine needs two binaries (the CLI used only for config, and the
// daemon), built specially - see internal/goexec.BuildNodeEngine.
const NodeEngineName = "nodeengine"

var registry = []Component{
	{
		Name:       "system_manager",
		Aliases:    []string{"sm"},
		Kind:       KindPython,
		SourcePath: "root_orchestrator/system-manager-python",
		Targets: []Target{{
			Stack: StackRoot, Container: "system_manager",
			Module: "system_manager:app", Port: 10000,
			LiveOverride:  "override-live-system_manager.yml",
			DebugOverride: "override-debug-system_manager.yml", DebugPort: 5678,
		}},
	},
	{
		Name:       "jwt_generator",
		Aliases:    []string{"jwt"},
		Kind:       KindPython,
		SourcePath: "root_orchestrator/jwt-generator",
		Targets: []Target{{
			Stack: StackRoot, Container: "jwt_generator",
			Module: "jwt_generator:app", Port: 10011,
			LiveOverride:  "override-live-jwt_generator.yml",
			DebugOverride: "override-debug-jwt_generator.yml", DebugPort: 5679,
		}},
	},
	{
		Name:       "root_resource_abstractor",
		Aliases:    []string{"rra"},
		Kind:       KindPython,
		SourcePath: "resource-abstractor",
		Targets: []Target{{
			Stack: StackRoot, Container: "root_resource_abstractor",
			Module: "resource_abstractor:app", Port: 11011,
			LiveOverride:  "override-live-root_resource_abstractor.yml",
			DebugOverride: "override-debug-root_resource_abstractor.yml", DebugPort: 5680,
		}},
	},
	{
		Name:       "cluster_manager",
		Aliases:    []string{"cm"},
		Kind:       KindPython,
		SourcePath: "cluster_orchestrator/cluster-manager",
		Targets: []Target{{
			Stack: StackCluster, Container: "cluster_manager",
			Module: "cluster_manager:app", Port: 10100,
			LiveOverride:  "override-live-cluster_manager.yml",
			DebugOverride: "override-debug-cluster_manager.yml", DebugPort: 5681,
		}},
	},
	{
		Name:       "cluster_resource_abstractor",
		Aliases:    []string{"cra"},
		Kind:       KindPython,
		SourcePath: "resource-abstractor",
		Targets: []Target{{
			Stack: StackCluster, Container: "cluster_resource_abstractor",
			Module: "resource_abstractor:app", Port: 11012,
			LiveOverride:  "override-live-cluster_resource_abstractor.yml",
			DebugOverride: "override-debug-cluster_resource_abstractor.yml", DebugPort: 5682,
		}},
	},
	{
		Name:       "scheduler",
		Aliases:    []string{"sched"},
		Kind:       KindGo,
		SourcePath: "scheduler",
		GoMain:     "./cmd",
		BinName:    "scheduler",
		Targets: []Target{
			{Stack: StackRoot, Container: "root_scheduler",
				LiveOverride:  "override-live-root_scheduler.yml",
				DebugOverride: "override-debug-root_scheduler.yml", DebugPort: 2345},
			{Stack: StackCluster, Container: "cluster_scheduler",
				LiveOverride:  "override-live-cluster_scheduler.yml",
				DebugOverride: "override-debug-cluster_scheduler.yml", DebugPort: 2346},
		},
	},
	{
		Name: NodeEngineName,
		// Deliberately not aliased to "worker": that is already a stack name
		// and a container name, and the three-way collision is one of the
		// things that made the old surface hard to reason about.
		Aliases:    []string{"ne"},
		Kind:       KindGo,
		SourcePath: "go_node_engine",
		BinName:    "nodeengined", // the daemon is the reload/debug target; NodeEngine (CLI) is built alongside it
		Targets: []Target{
			{Stack: StackWorker, Container: "worker",
				LiveOverride:  "override-live-worker.yml",
				DebugOverride: "override-debug-worker.yml", DebugPort: 2347},
		},
	},
}

// All returns every known component.
func All() []Component {
	return registry
}

// normalize makes lookup forgiving about the two things that differ purely by
// habit: case, and `-` versus `_` (compose service names and Go package dirs
// disagree, so users type both).
func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

// Resolve looks up a component by exact name, exact alias, or any prefix that
// matches exactly one component. The two failure modes are reported
// differently on purpose: an unknown name lists everything, while an ambiguous
// prefix lists only the candidates it actually matched.
func Resolve(name string) (Component, error) {
	q := normalize(name)
	if q == "" {
		return Component{}, fmt.Errorf("no component given, valid names: %s", Names())
	}

	for _, c := range registry {
		if normalize(c.Name) == q {
			return c, nil
		}
		for _, a := range c.Aliases {
			if normalize(a) == q {
				return c, nil
			}
		}
	}

	var matched []Component
	for _, c := range registry {
		hit := strings.HasPrefix(normalize(c.Name), q)
		for _, a := range c.Aliases {
			hit = hit || strings.HasPrefix(normalize(a), q)
		}
		if hit {
			matched = append(matched, c)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return Component{}, fmt.Errorf("unknown component %q, valid names: %s", name, Names())
	default:
		names := make([]string, len(matched))
		for i, c := range matched {
			names[i] = c.Name
		}
		return Component{}, fmt.Errorf("%q is ambiguous: %s", name, strings.Join(names, ", "))
	}
}

// Names returns every valid component name, for usage/error messages.
func Names() string {
	names := make([]string, len(registry))
	for i, c := range registry {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// Complete returns cobra shell-completion candidates for prefix, in
// "name\tdescription" form. Completing to the canonical name while showing the
// alias in the description teaches the short forms without making them the
// only thing TAB can produce.
func Complete(prefix string) []string {
	q := normalize(prefix)
	var out []string
	for _, c := range registry {
		if !strings.HasPrefix(normalize(c.Name), q) {
			continue
		}
		desc := string(c.Kind)
		if len(c.Aliases) > 0 {
			desc = strings.Join(c.Aliases, ", ") + " (" + desc + ")"
		}
		out = append(out, c.Name+"\t"+desc)
	}
	sort.Strings(out)
	return out
}

// GoComponents returns only Go components (for `test <component>`, `reload`,
// `debug`).
func GoComponents() []Component {
	var out []Component
	for _, c := range registry {
		if c.Kind == KindGo {
			out = append(out, c)
		}
	}
	return out
}

// GoNames returns the Go component names, for the "not a Go component" errors.
func GoNames() string {
	var names []string
	for _, c := range GoComponents() {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

// ByContainer finds the component owning a compose service name. Used by the
// target resolver so `logs cluster_scheduler` and `logs sched` reach the same
// place.
func ByContainer(container string) (Component, Target, bool) {
	for _, c := range registry {
		for _, t := range c.Targets {
			if t.Container == container {
				return c, t, true
			}
		}
	}
	return Component{}, Target{}, false
}

// InStack returns the targets of c that belong to the given stack.
func (c Component) InStack(stack string) []Target {
	var out []Target
	for _, t := range c.Targets {
		if t.Stack == stack {
			out = append(out, t)
		}
	}
	return out
}
