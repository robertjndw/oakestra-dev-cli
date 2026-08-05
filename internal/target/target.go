// Package target resolves the strings users type at `oak-dev logs` and
// `oak-dev shell` into concrete containers.
//
// It exists so those two commands share one vocabulary. Previously the same
// idea was spelled five ways - `sh <container>`, `mongo root|cluster`,
// `mqtt-tap`, `make log s=<container>`, `make worker-logs` - each hardcoding
// its own container name, port and command. Here a target may be a stack, a
// component (name, alias or prefix), a raw container name, or one of the few
// named endpoints that want a specific client rather than a shell.
package target

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
)

// Resolver turns specs into Targets. It holds the compose client because
// answering "is this a raw container name" means asking compose what services
// a stack declares, and it holds the answer cache because that question costs
// ~200ms per stack and one command line can ask it several times.
//
// The cache lives here rather than in a package-level map so that it lasts
// exactly as long as the command that created the Resolver, and so two tests
// cannot see each other's answers.
type Resolver struct {
	cfg *config.Config
	cmp *compose.Client

	serviceCache map[string][]string
}

// NewResolver returns a Resolver for one command's worth of lookups.
func NewResolver(cfg *config.Config, cmp *compose.Client) *Resolver {
	return &Resolver{cfg: cfg, cmp: cmp, serviceCache: map[string][]string{}}
}

// Target is one addressable container.
type Target struct {
	// Stack is the compose project it belongs to, so callers know which -f
	// chain to use.
	Stack string
	// Container is the compose service name.
	Container string
	// Label is what to print for it (the container name, or the endpoint
	// alias the user asked for).
	Label string
	// Exec, when non-empty, is the command `shell` should run instead of
	// probing for bash/sh - mongosh for the mongo endpoints, for example.
	Exec []string
	// Follow, when non-empty, is the command `logs` should run instead of
	// `docker compose logs`. The MQTT control plane is only observable by
	// subscribing to the broker, not by reading a container's stdout.
	Follow []string
}

// endpoint is a named thing that lives in a container but wants a specific
// client. Keeping these in one table is what lets `mongo` and `mqtt-tap`
// disappear as commands without losing what they did.
type endpoint struct {
	name      string
	stack     string
	container string
	exec      []string
	follow    []string
	desc      string
}

var endpoints = []endpoint{
	{
		name: "mongo-root", stack: components.StackRoot, container: "mongo_root",
		exec: []string{"mongosh", "--port", "10007"},
		desc: "mongosh into the root mongo",
	},
	{
		name: "mongo-cluster", stack: components.StackCluster, container: "mongo_cluster",
		exec: []string{"mongosh", "--port", "10107"},
		desc: "mongosh into the cluster mongo",
	},
	{
		// The cluster<->worker control plane is MQTT, so this is the only way
		// to watch e.g. nodes/<id>/control/deploy live.
		name: "mqtt", stack: components.StackCluster, container: "mqtt",
		follow: []string{"mosquitto_sub", "-p", "10003", "-t", "#", "-v"},
		desc:   "tap the cluster<->worker MQTT control plane",
	},
}

func (e endpoint) target() Target {
	return Target{
		Stack: e.stack, Container: e.container, Label: e.name,
		Exec: e.exec, Follow: e.follow,
	}
}

// Resolve turns one user-supplied spec into the targets it names, restricted
// to stacks in the current scope. Resolution order matters: stack names are
// checked before components because "cluster" is an unambiguous stack but an
// ambiguous component prefix.
func (r *Resolver) Resolve(spec string) ([]Target, error) {
	cfg := r.cfg
	q := strings.ToLower(strings.TrimSpace(spec))

	// 1. A stack name: every container in that project.
	if q == components.StackRoot || q == components.StackCluster || q == components.StackWorker {
		if !cfg.StackEnabled(q) {
			return nil, fmt.Errorf("stack %q is not in the current scope (%s)", q, cfg.Stack)
		}
		names, err := r.services(q)
		if err != nil {
			return nil, err
		}
		var out []Target
		for _, n := range names {
			out = append(out, Target{Stack: q, Container: n, Label: n})
		}
		return out, nil
	}

	// 2. A named endpoint. Accepts underscores as well as the canonical
	//    dashed spelling (endpoint names use dashes, e.g. "mongo-root", but
	//    every container/compose-service name in this repo uses underscores
	//    - typing the more familiar "mongo_root" should still hit the mongosh
	//    shortcut below rather than silently falling through to a plain
	//    shell via the raw-container-name branch further down).
	qDashed := strings.ReplaceAll(q, "_", "-")
	for _, e := range endpoints {
		if qDashed == e.name {
			if !cfg.StackEnabled(e.stack) {
				return nil, fmt.Errorf("%s lives in the %s stack, which is not in the current scope (%s)",
					e.name, e.stack, cfg.Stack)
			}
			return []Target{e.target()}, nil
		}
	}

	// 3. A component, by name, alias or unambiguous prefix. Its containers in
	//    scope - so `logs sched` follows both schedulers.
	if c, err := components.Resolve(q); err == nil {
		var out []Target
		for _, t := range c.Targets {
			if cfg.StackEnabled(t.Stack) {
				out = append(out, Target{Stack: t.Stack, Container: t.Container, Label: t.Container})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s has no containers in the current scope (%s)", c.Name, cfg.Stack)
		}
		return out, nil
	} else if strings.Contains(err.Error(), "ambiguous") {
		// Only surface ambiguity if it isn't resolvable as a container below.
		if stack, ok := r.findService(q); ok {
			return []Target{{Stack: stack, Container: q, Label: q}}, nil
		}
		return nil, err
	}

	// 4. A raw compose service name, in whichever enabled stack declares it.
	//    This is what makes infrastructure containers (mongo_rootnet,
	//    root_redis, cluster_service_manager) reachable without listing them.
	if stack, ok := r.findService(q); ok {
		return []Target{{Stack: stack, Container: q, Label: q}}, nil
	}

	return nil, fmt.Errorf("unknown target %q\n  stacks:     %s\n  components: %s\n  endpoints:  %s\n"+
		"  ...or any compose service name in the current scope",
		spec, strings.Join(components.Stacks(), ", "), components.Names(), endpointNames())
}

// ResolveAll resolves several specs, de-duplicating containers so `logs
// cluster cluster_manager` doesn't tail the same container twice.
func (r *Resolver) ResolveAll(specs []string) ([]Target, error) {
	var out []Target
	seen := map[string]bool{}
	for _, s := range specs {
		ts, err := r.Resolve(s)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			key := t.Stack + "/" + t.Container + "/" + strings.Join(t.Exec, " ") + strings.Join(t.Follow, " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// Complete offers completion candidates for a target argument.
func (r *Resolver) Complete(prefix string) []string {
	cfg := r.cfg
	var out []string
	add := func(v, desc string) {
		if strings.HasPrefix(v, strings.ToLower(prefix)) {
			out = append(out, v+"\t"+desc)
		}
	}
	for _, s := range components.Stacks() {
		if cfg.StackEnabled(s) {
			add(s, "the whole "+s+" stack")
		}
	}
	for _, e := range endpoints {
		if cfg.StackEnabled(e.stack) {
			add(e.name, e.desc)
		}
	}
	for _, c := range components.All() {
		for _, t := range c.Targets {
			if cfg.StackEnabled(t.Stack) {
				add(c.Name, strings.Join(c.Aliases, ", ")+" ("+string(c.Kind)+")")
				break
			}
		}
	}
	for _, s := range components.Stacks() {
		if !cfg.StackEnabled(s) {
			continue
		}
		names, err := r.services(s)
		if err != nil {
			continue
		}
		for _, n := range names {
			if _, _, isComponent := components.ByContainer(n); !isComponent {
				add(n, s+" stack")
			}
		}
	}
	sort.Strings(out)
	return out
}

func endpointNames() string {
	return strings.Join(EndpointNames(), ", ")
}

// EndpointNames returns the named endpoints' canonical spellings, in registry
// order - every one of them resolves to exactly one container, which is what
// lets callers like the vscode picker generator build a shell-safe target
// list without hardcoding this table a second time.
func EndpointNames() []string {
	names := make([]string, len(endpoints))
	for i, e := range endpoints {
		names[i] = e.name
	}
	return names
}

// services lists the compose services declared by a stack. Asking compose
// keeps this correct as upstream's files change, rather than maintaining a
// hand-written container allowlist that silently goes stale.
//
// Answers are cached on the Resolver: the question costs ~200ms per stack and
// one command line (`logs cluster mongo_root worker`) asks it repeatedly.
func (r *Resolver) services(stack string) ([]string, error) {
	if cached, ok := r.serviceCache[stack]; ok {
		return cached, nil
	}
	out, err := r.cmp.StackCapture(stack, "config", "--services")
	if err != nil {
		return nil, fmt.Errorf("listing %s services: %w: %s", stack, err, strings.TrimSpace(string(out)))
	}
	var names []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	r.serviceCache[stack] = names
	return names, nil
}

func (r *Resolver) findService(name string) (string, bool) {
	for _, stack := range components.Stacks() {
		if !r.cfg.StackEnabled(stack) {
			continue
		}
		names, err := r.services(stack)
		if err != nil {
			continue
		}
		if slices.Contains(names, name) {
			return stack, true
		}
	}
	return "", false
}
