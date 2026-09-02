// Package build cross-compiles Go components on the host (CGO_ENABLED=0
// GOOS=linux) into build/linux_<arch>/, which compose/override-live-*.yml
// bind-mounts into the container. The mount targets the directory, not the
// binary: go build writes a new inode on every build, so a file mount would
// keep resolving the old one and silently run stale code.
package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
	"oak-dev/internal/proc"
)

// Builder cross-compiles components. It takes a Runner instead of shelling
// out directly, so tests can assert on the actual `go build` invocation:
// GOARCH, the Delve -gcflags, the ldflags stamping. All of that used to be
// untestable.
type Builder struct {
	cfg    *config.Config
	runner proc.Runner
}

// New returns a Builder that runs the toolchain through runner.
func New(cfg *config.Config, runner proc.Runner) *Builder {
	return &Builder{cfg: cfg, runner: runner}
}

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
func (b *Builder) Build(c components.Component, opts Options) ([]string, error) {
	if c.Kind != components.KindGo {
		return nil, fmt.Errorf("%s is not a Go component", c.Name)
	}
	dir, err := Dir(b.cfg)
	if err != nil {
		return nil, err
	}
	srcDir := b.cfg.SourceDir(c)

	if len(c.ExtraBinNames) > 0 {
		return b.buildNodeEngine(b.cfg.GOARCH, srcDir, dir, opts)
	}

	var ldflags []string
	if c.VersionVar != "" {
		ldflags = []string{"-X", c.VersionVar + "=dev"}
	}
	out := filepath.Join(dir, c.BinName)
	if err := b.goBuild(b.cfg.GOARCH, srcDir, out, c.GoMain, opts, ldflags); err != nil {
		return nil, err
	}
	return []string{out}, nil
}

func (b *Builder) buildNodeEngine(arch, srcDir, outDir string, opts Options) ([]string, error) {
	ldflags := []string{"-X", "go_node_engine/cmd.Version=dev"}
	cli := filepath.Join(outDir, "NodeEngine")
	daemon := filepath.Join(outDir, "nodeengined")
	if err := b.goBuild(arch, srcDir, cli, ".", opts, ldflags); err != nil {
		return nil, fmt.Errorf("building NodeEngine CLI: %w", err)
	}
	if err := b.goBuild(arch, srcDir, daemon, "./internal/daemon/nodeengined.go", opts, ldflags); err != nil {
		return nil, fmt.Errorf("building nodeengined daemon: %w", err)
	}
	return []string{cli, daemon}, nil
}

func (b *Builder) goBuild(arch, dir, out, pkg string, opts Options, ldflags []string) error {
	args := []string{"build", "-o", out}
	if opts.Debug {
		args = append(args, "-gcflags=all=-N -l")
	}
	if len(ldflags) > 0 {
		args = append(args, "-ldflags="+joinLdflags(ldflags))
	}
	args = append(args, pkg)

	return b.runner.Run(proc.Spec{
		Name: "go",
		Args: args,
		Dir:  dir,
		Env:  append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch),
	})
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
func (b *Builder) EnsureDelve() error {
	cfg := b.cfg
	dir, err := Dir(cfg)
	if err != nil {
		return err
	}
	out := filepath.Join(dir, "dlv")
	if _, err := os.Stat(out); err == nil {
		return nil
	}
	fmt.Println("oak-dev: installing Delve for linux/" + cfg.GOARCH + " (first debug session only)...")

	// `go install` won't cross-compile while GOBIN is set, and plenty of
	// developers have it set. Clear it and let the install go to its default
	// location instead; for a cross-compile that's $GOPATH/bin/$GOOS_$GOARCH/,
	// and we copy the binary from there below. GOPATH itself stays untouched
	// so this still uses the normal module cache instead of fetching
	// everything a second time.
	gopath, err := b.goEnv("GOPATH")
	if err != nil {
		return err
	}
	if err := b.runner.Run(proc.Spec{
		Name: "go",
		Args: []string{"install", "github.com/go-delve/delve/cmd/dlv@latest"},
		Env: append(os.Environ(),
			"CGO_ENABLED=0", "GOOS=linux", "GOARCH="+cfg.GOARCH, "GOBIN="),
	}); err != nil {
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

// goEnv captures stdout only: the result is used as a filesystem path, and a
// toolchain warning on stderr would otherwise end up baked into it.
func (b *Builder) goEnv(key string) (string, error) {
	out, err := b.runner.Capture(proc.Spec{Name: "go", Args: []string{"env", key}})
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
// cross-compilation. It's the fastest feedback loop we have.
func (b *Builder) UnitTest(c components.Component, extraArgs ...string) error {
	if c.Kind != components.KindGo {
		return fmt.Errorf("%s is not a Go component", c.Name)
	}
	return b.runner.Run(proc.Spec{
		Name: "go",
		Args: append([]string{"test", "./..."}, extraArgs...),
		Dir:  b.cfg.SourceDir(c),
	})
}
