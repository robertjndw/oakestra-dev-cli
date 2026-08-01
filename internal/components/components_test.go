package components

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		in   string
		want string // canonical name, or "" if an error is expected
		errs string // substring the error must contain
	}{
		// exact names
		{in: "system_manager", want: "system_manager"},
		{in: "nodeengine", want: "nodeengine"},
		{in: "cluster_resource_abstractor", want: "cluster_resource_abstractor"},

		// aliases
		{in: "sm", want: "system_manager"},
		{in: "cm", want: "cluster_manager"},
		{in: "cra", want: "cluster_resource_abstractor"},
		{in: "rra", want: "root_resource_abstractor"},
		{in: "sched", want: "scheduler"},
		{in: "ne", want: "nodeengine"},
		{in: "jwt", want: "jwt_generator"},
		{in: "rsm", want: "root_service_manager"},
		{in: "csm", want: "cluster_service_manager"},
		{in: "nm", want: "netmanager"},

		// case and separator normalisation
		{in: "Cluster-Manager", want: "cluster_manager"},
		{in: "CLUSTER_MANAGER", want: "cluster_manager"},
		{in: "  cm  ", want: "cluster_manager"},

		// unambiguous prefixes
		{in: "cluster_res", want: "cluster_resource_abstractor"},
		{in: "cluster-res", want: "cluster_resource_abstractor"},
		{in: "node", want: "nodeengine"},
		{in: "jwt_gen", want: "jwt_generator"},
		{in: "root_res", want: "root_resource_abstractor"},
		{in: "root_serv", want: "root_service_manager"},

		// an exact alias must win over being a prefix of other things
		{in: "sm", want: "system_manager"},

		// ambiguous
		{in: "c", errs: "ambiguous"},
		{in: "cluster", errs: "ambiguous"},
		{in: "s", errs: "ambiguous"},
		{in: "root", errs: "ambiguous"},

		// unknown
		{in: "schedulerr", errs: "unknown component"},
		{in: "zzz", errs: "unknown component"},
		{in: "", errs: "no component given"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Resolve(tc.in)
			if tc.errs != "" {
				if err == nil {
					t.Fatalf("Resolve(%q) = %s, want error containing %q", tc.in, got.Name, tc.errs)
				}
				if !strings.Contains(err.Error(), tc.errs) {
					t.Fatalf("Resolve(%q) error = %q, want it to contain %q", tc.in, err, tc.errs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.in, err)
			}
			if got.Name != tc.want {
				t.Fatalf("Resolve(%q) = %s, want %s", tc.in, got.Name, tc.want)
			}
		})
	}
}

func TestAmbiguousErrorListsOnlyCandidates(t *testing.T) {
	_, err := Resolve("cluster")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	msg := err.Error()
	for _, want := range []string{"cluster_manager", "cluster_resource_abstractor"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ambiguity error %q should list %s", msg, want)
		}
	}
	// The point of distinguishing the two failure modes is that this does NOT
	// dump the whole registry at you.
	if strings.Contains(msg, "system_manager") {
		t.Errorf("ambiguity error should list only the candidates, got %q", msg)
	}
}

func TestRegistryInvariants(t *testing.T) {
	seen := map[string]string{} // name or alias -> owning component

	for _, c := range All() {
		for _, key := range append([]string{c.Name}, c.Aliases...) {
			if prev, dup := seen[normalize(key)]; dup {
				t.Errorf("%q is claimed by both %s and %s", key, prev, c.Name)
			}
			seen[normalize(key)] = c.Name
		}

		if c.Repo == "" {
			t.Errorf("%s has no Repo", c.Name)
		}
		if len(c.Targets) == 0 {
			t.Errorf("%s has no targets", c.Name)
		}
		for _, tg := range c.Targets {
			// Every dispatching command relies on these being present.
			if tg.LiveOverride == "" {
				t.Errorf("%s target %s has no LiveOverride", c.Name, tg.Container)
			}
			if tg.DebugOverride == "" {
				t.Errorf("%s target %s has no DebugOverride", c.Name, tg.Container)
			}
			if tg.DebugPort == 0 {
				t.Errorf("%s target %s has no DebugPort", c.Name, tg.Container)
			}
			if !ValidScope(tg.Stack) || tg.Stack == ScopeFull {
				t.Errorf("%s target %s has invalid stack %q", c.Name, tg.Container, tg.Stack)
			}
		}

		if c.Kind == KindGo && c.BinName == "" {
			t.Errorf("go component %s has no BinName", c.Name)
		}
	}

	// Debug ports must be unique: they are host-published, and .vscode/launch.json
	// hardcodes them.
	ports := map[int]string{}
	for _, c := range All() {
		for _, tg := range c.Targets {
			if prev, dup := ports[tg.DebugPort]; dup {
				t.Errorf("debug port %d is used by both %s and %s", tg.DebugPort, prev, tg.Container)
			}
			ports[tg.DebugPort] = tg.Container
		}
	}
}

func TestByContainer(t *testing.T) {
	// scheduler is the one component with two containers; both must map back.
	for container, want := range map[string]string{
		"root_scheduler":    "scheduler",
		"cluster_scheduler": "scheduler",
		"worker":            "nodeengine",
		"system_manager":    "system_manager",
	} {
		c, tg, ok := ByContainer(container)
		if !ok {
			t.Errorf("ByContainer(%q) not found", container)
			continue
		}
		if c.Name != want {
			t.Errorf("ByContainer(%q) = %s, want %s", container, c.Name, want)
		}
		if tg.Container != container {
			t.Errorf("ByContainer(%q) returned target for %s", container, tg.Container)
		}
	}

	if _, _, ok := ByContainer("mongo_root"); ok {
		t.Error("ByContainer should not claim infrastructure containers")
	}
}

func TestComplete(t *testing.T) {
	got := Complete("cluster")
	if len(got) != 3 {
		t.Fatalf("Complete(\"cluster\") returned %d candidates, want 3: %v", len(got), got)
	}
	// Completion must offer the canonical name, with the alias as the description.
	if !strings.HasPrefix(got[0], "cluster_manager\t") {
		t.Errorf("candidate %q should complete to the canonical name", got[0])
	}
	if !strings.Contains(got[0], "cm") {
		t.Errorf("candidate %q should mention the alias", got[0])
	}
	if len(Complete("")) != len(All()) {
		t.Errorf("Complete(\"\") should offer every component")
	}
}
