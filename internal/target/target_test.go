package target

import (
	"slices"
	"strings"
	"testing"

	"oak-dev/internal/components"
	"oak-dev/internal/compose"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
	"oak-dev/internal/topology"
)

// clusterServices is what `docker compose config --services` reports for the
// cluster stack, including the infrastructure containers that have no entry in
// the component registry.
const clusterServices = "cluster_manager\ncluster_redis\ncluster_resource_abstractor\ncluster_scheduler\ncluster_service_manager\nmongo_cluster\nmongo_clusternet\nmqtt\n"

const rootServices = "jwt_generator\nmongo_root\nmongo_rootnet\nroot_redis\nroot_resource_abstractor\nroot_scheduler\nroot_service_manager\nsystem_manager\n"

const workerServices = "worker\n"

func testResolver(t *testing.T) (*Resolver, *proc.Recorder) {
	t.Helper()
	cfg := &config.Config{
		RepoRoot:        "/repo",
		OakestraRepo:    "/oakestra",
		OakestraNetRepo: "/oakestra-net",
		Stack:           components.ScopeFull,
		Live:            map[string]bool{},
	}
	topo, err := topology.Chains(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := proc.NewRecorder()
	rec.OnContains("root_orchestrator", []byte(rootServices), nil)
	rec.OnContains("cluster_orchestrator", []byte(clusterServices), nil)
	rec.OnContains("worker.yml", []byte(workerServices), nil)
	return NewResolver(cfg, compose.New(cfg, rec, topo)), rec
}

func labels(ts []Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Label
	}
	return out
}

// Stack names are checked before components because "cluster" is an
// unambiguous stack but an ambiguous component prefix.
func TestResolveStackBeatsAmbiguousComponentPrefix(t *testing.T) {
	r, _ := testResolver(t)

	got, err := r.Resolve("cluster")
	if err != nil {
		t.Fatalf("Resolve(cluster): %v", err)
	}
	if len(got) != 8 {
		t.Errorf("Resolve(cluster) = %v, want all 8 cluster services", labels(got))
	}
	for _, tt := range got {
		if tt.Stack != components.StackCluster {
			t.Errorf("%s has stack %q, want cluster", tt.Label, tt.Stack)
		}
	}
}

// A component resolves to its containers in every stack in scope, so `logs
// sched` follows both schedulers.
func TestResolveComponentSpansStacks(t *testing.T) {
	r, _ := testResolver(t)

	got, err := r.Resolve("sched")
	if err != nil {
		t.Fatalf("Resolve(sched): %v", err)
	}
	want := []string{"root_scheduler", "cluster_scheduler"}
	if !slices.Equal(labels(got), want) {
		t.Errorf("Resolve(sched) = %v, want %v", labels(got), want)
	}
}

// The endpoints exist so `mongo` and `mqtt-tap` could disappear as commands
// without losing what they did. mqtt is followed by subscribing to the broker,
// not by reading a container's stdout.
func TestResolveEndpoints(t *testing.T) {
	tests := []struct {
		spec      string
		container string
		exec      []string
		follow    []string
	}{
		{"mongo-root", "mongo_root", []string{"mongosh", "--port", "10007"}, nil},
		{"mongo_root", "mongo_root", []string{"mongosh", "--port", "10007"}, nil},
		{"mongo-cluster", "mongo_cluster", []string{"mongosh", "--port", "10107"}, nil},
		{"mqtt", "mqtt", nil, []string{"mosquitto_sub", "-p", "10003", "-t", "#", "-v"}},
	}

	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			r, _ := testResolver(t)
			got, err := r.Resolve(tt.spec)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tt.spec, err)
			}
			if len(got) != 1 {
				t.Fatalf("Resolve(%s) = %v, want one target", tt.spec, labels(got))
			}
			if got[0].Container != tt.container {
				t.Errorf("container = %q, want %q", got[0].Container, tt.container)
			}
			if !slices.Equal(got[0].Exec, tt.exec) {
				t.Errorf("Exec = %v, want %v", got[0].Exec, tt.exec)
			}
			if !slices.Equal(got[0].Follow, tt.follow) {
				t.Errorf("Follow = %v, want %v", got[0].Follow, tt.follow)
			}
		})
	}
}

// Raw compose service names are what make the infrastructure containers
// reachable without listing them anywhere.
func TestResolveRawServiceName(t *testing.T) {
	r, _ := testResolver(t)

	got, err := r.Resolve("mongo_rootnet")
	if err != nil {
		t.Fatalf("Resolve(mongo_rootnet): %v", err)
	}
	if len(got) != 1 || got[0].Stack != components.StackRoot {
		t.Errorf("Resolve(mongo_rootnet) = %+v, want one target in the root stack", got)
	}
}

func TestResolveUnknown(t *testing.T) {
	r, _ := testResolver(t)

	_, err := r.Resolve("not_a_thing")
	if err == nil {
		t.Fatal("Resolve(not_a_thing) = nil error, want one")
	}
	// The message has to teach the vocabulary, since there are four kinds of
	// valid target and no way to guess them.
	for _, want := range []string{"stacks:", "components:", "endpoints:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestResolveOutOfScope(t *testing.T) {
	r, _ := testResolver(t)
	r.cfg.Stack = components.StackRoot

	if _, err := r.Resolve("worker"); err == nil {
		t.Error("Resolve(worker) under --stack root = nil error, want one")
	}
	if _, err := r.Resolve("mongo-cluster"); err == nil {
		t.Error("Resolve(mongo-cluster) under --stack root = nil error, want one")
	}
}

// ResolveAll de-duplicates so `logs cluster cluster_manager` does not tail the
// same container twice.
func TestResolveAllDeduplicates(t *testing.T) {
	r, _ := testResolver(t)

	got, err := r.ResolveAll([]string{"cluster", "cluster_manager"})
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	seen := map[string]int{}
	for _, tt := range got {
		seen[tt.Container]++
	}
	if seen["cluster_manager"] != 1 {
		t.Errorf("cluster_manager appears %d times, want 1", seen["cluster_manager"])
	}
}

// The question costs ~200ms per stack and one command line asks it repeatedly.
func TestServicesAreCachedPerResolver(t *testing.T) {
	r, rec := testResolver(t)

	for range 3 {
		if _, err := r.Resolve("root"); err != nil {
			t.Fatalf("Resolve(root): %v", err)
		}
	}

	want := []string{
		"docker compose -f /oakestra/root_orchestrator/docker-compose.yml " +
			"-f /oakestra/root_orchestrator/override-no-addons.yml " +
			"-f /oakestra/root_orchestrator/override-no-observe.yml " +
			"-f /oakestra/root_orchestrator/override-no-dashboard.yml " +
			"-f /repo/compose/override-root-mongo.yml " +
			"-f /repo/compose/override-root-servicemanager.yml config --services",
	}
	if got := rec.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands = %v, want exactly one lookup:\n  %v", got, want)
	}
}

// A second Resolver must not see the first one's answers - that is why the
// cache moved off the package.
func TestServiceCacheIsNotShared(t *testing.T) {
	r1, _ := testResolver(t)
	if _, err := r1.Resolve("root"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	r2, rec2 := testResolver(t)
	if _, err := r2.Resolve("root"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := len(rec2.Commands()); got != 1 {
		t.Errorf("second resolver made %d lookups, want 1 - it must not inherit a cache", got)
	}
}

func TestCompleteOffersEveryKindOfTarget(t *testing.T) {
	r, _ := testResolver(t)

	got := r.Complete("")
	has := func(name string) bool {
		return slices.ContainsFunc(got, func(c string) bool {
			return strings.HasPrefix(c, name+"\t")
		})
	}
	for _, want := range []string{"root", "cluster", "worker", "mongo-root", "mqtt", "system_manager", "mongo_rootnet"} {
		if !has(want) {
			t.Errorf("Complete(\"\") missing %q", want)
		}
	}
}
