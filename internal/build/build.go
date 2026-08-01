// Package build cross-compiles Go components on the host (CGO_ENABLED=0
// GOOS=linux) and drops the binary into build/linux_<arch>/, the directory
// bind-mounted into the relevant container by compose/override-live-*.yml.
// Mounting the directory rather than a single file matters: go build writes
// a new inode each time, and a file bind-mount would keep resolving the old
// one, silently testing stale code.
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
)

// Dir returns build/linux_<arch>, creating it if needed.
func Dir(cfg *config.Config) (string, error) {
	dir := filepath.Join(cfg.RepoRoot, "build", "linux_"+cfg.GOARCH)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Options controls a single build invocation.
type Options struct {
	Debug bool // add -gcflags="all=-N -l" for Delve
}

// Build cross-compiles c and returns the binary path(s) it produced. Most
// components produce one binary; nodeengine produces two (NodeEngine CLI +
// nodeengined daemon).
func Build(cfg *config.Config, c components.Component, opts Options) ([]string, error) {
	if c.Kind != components.KindGo {
		return nil, fmt.Errorf("%s is not a Go component", c.Name)
	}
	dir, err := Dir(cfg)
	if err != nil {
		return nil, err
	}
	srcDir := cfg.SourceDir(c)

	if len(c.ExtraBinNames) > 0 {
		return buildNodeEngine(cfg.GOARCH, srcDir, dir, opts)
	}

	var ldflags []string
	if c.VersionVar != "" {
		ldflags = []string{"-X", c.VersionVar + "=dev"}
	}
	out := filepath.Join(dir, c.BinName)
	if err := goBuild(cfg.GOARCH, srcDir, out, c.GoMain, opts, ldflags); err != nil {
		return nil, err
	}
	return []string{out}, nil
}

func buildNodeEngine(arch, srcDir, outDir string, opts Options) ([]string, error) {
	ldflags := []string{"-X", "go_node_engine/cmd.Version=dev"}
	cli := filepath.Join(outDir, "NodeEngine")
	daemon := filepath.Join(outDir, "nodeengined")
	if err := goBuild(arch, srcDir, cli, ".", opts, ldflags); err != nil {
		return nil, fmt.Errorf("building NodeEngine CLI: %w", err)
	}
	if err := goBuild(arch, srcDir, daemon, "./internal/daemon/nodeengined.go", opts, ldflags); err != nil {
		return nil, fmt.Errorf("building nodeengined daemon: %w", err)
	}
	return []string{cli, daemon}, nil
}

func goBuild(arch, dir, out, pkg string, opts Options, ldflags []string) error {
	args := []string{"build", "-o", out}
	if opts.Debug {
		args = append(args, "-gcflags=all=-N -l")
	}
	if len(ldflags) > 0 {
		args = append(args, "-ldflags="+joinLdflags(ldflags))
	}
	args = append(args, pkg)

	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func joinLdflags(parts []string) string {
	// parts come in pairs like ["-X", "pkg.Var=val"]; join into one flag value.
	var pieces []string
	for i := 0; i < len(parts); i += 2 {
		pieces = append(pieces, parts[i]+" '"+parts[i+1]+"'")
	}
	return strings.Join(pieces, " ")
}

// EnsureDelve cross-compiles Delve into build/linux_<arch>/dlv the first
// time it's needed - `oak-dev debug` mounts it alongside the target binary
// so it can run headless *inside* the container's OS/arch, not the host's.
func EnsureDelve(cfg *config.Config) error {
	dir, err := Dir(cfg)
	if err != nil {
		return err
	}
	out := filepath.Join(dir, "dlv")
	if _, err := os.Stat(out); err == nil {
		return nil
	}
	fmt.Println("oak-dev: installing Delve for linux/" + cfg.GOARCH + " (first debug session only)...")

	// `go install` refuses to cross-compile while GOBIN is set, so GOBIN can't
	// simply point at the output directory - and developers commonly have it
	// set. Clear it instead and let the install land in its default place;
	// when cross-compiling that is $GOPATH/bin/$GOOS_$GOARCH/, which we copy
	// from. GOPATH itself is left alone so this shares the normal module
	// cache rather than downloading a second copy of everything.
	gopath, err := goEnv("GOPATH")
	if err != nil {
		return err
	}
	cmd := exec.Command("go", "install", "github.com/go-delve/delve/cmd/dlv@latest")
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0", "GOOS=linux", "GOARCH="+cfg.GOARCH, "GOBIN=")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	built := filepath.Join(gopath, "bin", "linux_"+cfg.GOARCH, "dlv")
	if _, err := os.Stat(built); err != nil {
		// Go drops the $GOOS_$GOARCH suffix when the target matches the host,
		// which is the case on a Linux host of the same architecture.
		built = filepath.Join(gopath, "bin", "dlv")
	}
	return copyExecutable(built, out)
}

func goEnv(key string) (string, error) {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("reading go env %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("locating the cross-compiled dlv: %w", err)
	}
	return os.WriteFile(dst, data, 0o755)
}

// UnitTest runs `go test ./...` for c on the host - no containers, no
// cross-compilation, just the fastest rung on the loop ladder.
func UnitTest(cfg *config.Config, c components.Component, extraArgs ...string) error {
	if c.Kind != components.KindGo {
		return fmt.Errorf("%s is not a Go component", c.Name)
	}
	srcDir := cfg.SourceDir(c)
	args := append([]string{"test", "./..."}, extraArgs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
