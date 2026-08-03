package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
)

// FixProto generates proto/*_pb2.py inside the host oakestra checkout, running
// grpc_tools in a throwaway container so no host Python setup is needed. This
// mirrors exactly what each service's Dockerfile does at build time.
//
// Note this writes into $OAKESTRA_REPO, a different repo from the one oak-dev
// lives in, which is why it announces the path.
func FixProto(cfg *config.Config) error {
	const script = "pip install --quiet --no-cache-dir grpcio-tools && " +
		"python -m grpc_tools.protoc -I. --python_out=. --grpc_python_out=. proto/clusterRegistration.proto"

	for _, c := range components.All() {
		if !c.NeedsProtoStubs {
			continue
		}
		dir := cfg.SourceDir(c)
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("%s does not exist - is OAKESTRA_REPO right?", dir)
		}
		fmt.Printf("    generating protobuf stubs in %s\n", dir)
		cmd := exec.Command("docker", "run", "--rm",
			"-v", dir+":/src", "-w", "/src",
			"python:3.10-slim-bookworm", "bash", "-c", script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("generating protos in %s: %w", dir, err)
		}
	}
	return nil
}

// FixOakConfig points the real `oak` CLI (oakestra-cli) at this local stack,
// so `oak application create -f fixtures/nginx.json` and `oak-dev status` work
// against it. oak-dev dogfoods that CLI rather than shipping its own API
// client.
//
// This writes the user's global `oak` configuration, not anything inside this
// repo, so it prints each setting it changes.
func FixOakConfig(cfg *config.Config) error {
	if _, err := exec.LookPath("oak"); err != nil {
		return fmt.Errorf("`oak` is not on PATH - install oakestra-cli first " +
			"(https://github.com/oakestra/oakestra-cli); it is only needed for `oak-dev status`")
	}

	settings := [][]string{
		{"config", "set", "system_manager_ip", "localhost"},
		{"config", "set", "cluster_manager_ip", "localhost"},
		{"config", "set", "cluster_name", cfg.ClusterName},
		{"config", "set", "cluster_location", cfg.ClusterLoc},
		{"config", "credentials", "Admin", "Admin"},
	}
	fmt.Println("    writing your global `oak` CLI configuration:")
	for _, args := range settings {
		fmt.Printf("      oak %s\n", joinArgs(args))
		cmd := exec.Command("oak", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("oak %s: %w", joinArgs(args), err)
		}
	}
	return nil
}

func joinArgs(args []string) string { return strings.Join(args, " ") }
