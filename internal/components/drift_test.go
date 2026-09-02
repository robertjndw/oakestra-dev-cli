package components

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDebugPortsMatchComposeOverrides checks the one thing
// `oak-dev vscode install` (internal/vscode) assumes: that Target.DebugPort
// is actually the host port compose publishes once DebugOverride is applied.
// The old hand-maintained .vscode/launch.json could silently drift from
// either side; this catches that before a generated launch.json points VS
// Code at the wrong port.
func TestDebugPortsMatchComposeOverrides(t *testing.T) {
	composeDir, err := filepath.Abs(filepath.Join("..", "..", "compose"))
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range All() {
		for _, tg := range c.Targets {
			if tg.DebugOverride == "" {
				continue
			}
			t.Run(c.Name+"/"+tg.Stack, func(t *testing.T) {
				path := filepath.Join(composeDir, tg.DebugOverride)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading %s: %v", path, err)
				}

				ports := hostPorts(t, data)
				if len(ports) != 1 {
					t.Fatalf("%s publishes %d host ports (%v), expected exactly one debug port",
						tg.DebugOverride, len(ports), ports)
				}
				if ports[0] != tg.DebugPort {
					t.Errorf("%s publishes host port %d, but the registry's DebugPort for %s/%s is %d",
						tg.DebugOverride, ports[0], c.Name, tg.Stack, tg.DebugPort)
				}
			})
		}
	}
}

// hostPorts extracts the host side of every "ports:" list anywhere in a
// compose fragment ("host:container" entries), regardless of which service
// it's nested under - override-debug-cluster_service_manager.yml publishes
// its port on cluster_manager, not on the component it debugs, since that
// container has no network namespace of its own.
func hostPorts(t *testing.T, data []byte) []int {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing yaml: %v", err)
	}

	var out []int
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, val := n.Content[i], n.Content[i+1]
				if key.Value != "ports" || val.Kind != yaml.SequenceNode {
					continue
				}
				for _, item := range val.Content {
					host, _, _ := strings.Cut(item.Value, ":")
					p, err := strconv.Atoi(host)
					if err != nil {
						t.Fatalf("unparseable port mapping %q", item.Value)
					}
					out = append(out, p)
				}
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(&doc)
	return out
}
