// Package doctor implements oak-dev's preflight checks. Every check is a
// plain, independently testable Go function - each one documents a failure
// mode seen in one of the repos this project draws on (see the Source field)
// and prints the fix, not just the symptom.
package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"oak-dev/internal/config"
)

// Result is the outcome of one check.
type Result struct {
	Name     string
	Source   string // where this failure mode is documented/came from
	Optional bool   // true if this check only gates a specific subcommand
	OK       bool
	Detail   string
	Fix      string
}

// CheckFunc inspects the environment and returns a Result.
type CheckFunc func(cfg *config.Config) Result

type namedCheck struct {
	name     string
	source   string
	optional bool
	fn       func(cfg *config.Config) (ok bool, detail, fix string)
}

var checks = []namedCheck{
	{"OAKESTRA_REPO checkout", "Makefile", false, checkOakestraRepo},
	{"docker + compose", "oakestra-deploy .../orchestrator.yml", false, checkDockerCompose},
	{"kernel vs mongo pin", "CLAUDE.md, MongoDB SERVER-121912", false, checkKernelMongoPin},
	{"host arch vs docker arch", "worker/Dockerfile", false, checkArch},
	{"proto/*_pb2.py generated", "oakestra/.gitignore", false, checkProto},
	{"Go toolchain", "builds oak-dev itself and the cross-compiled binaries", false, checkGoToolchain},
	{"watchexec", "needed for `oak-dev dev`", true, checkWatchexec},
	{"oak CLI on PATH", "needed for `oak-dev status`", true, checkOakCLI},
	{"stale oakestra network/volumes", "`down --volumes` exists for this", false, checkStaleNetwork},
}

// Run executes every check and returns the results in order.
func Run(cfg *config.Config) []Result {
	out := make([]Result, 0, len(checks))
	for _, c := range checks {
		ok, detail, fix := c.fn(cfg)
		out = append(out, Result{
			Name: c.name, Source: c.source, Optional: c.optional,
			OK: ok, Detail: detail, Fix: fix,
		})
	}
	return out
}

// Fixer is one bootstrap action `doctor --fix` can perform.
//
// Fixers are deliberately separate from checks rather than hanging off a
// failed one: whether an action is possible ("is `oak` installed?") is not the
// same question as whether a check passed, and conflating them advertises
// automatic repairs that would immediately fail.
type Fixer struct {
	Name string
	// Needed reports whether there is anything to do. A fixer whose
	// prerequisite is missing reports false and explains why via Skip.
	Needed func(cfg *config.Config) bool
	// Skip explains why Needed said no, when that's worth saying out loud.
	Skip func(cfg *config.Config) string
	// Run performs the action, describing each step as it goes. Some of these
	// write outside this repo - generated protobuf stubs land in the oakestra
	// checkout, `oak` configuration in the user's home - so none of them
	// should be silent.
	Run func(cfg *config.Config) error
}

// Fixers returns the bootstrap actions, in the order they should run.
func Fixers() []Fixer {
	return []Fixer{
		{
			Name:   "generate proto/*_pb2.py",
			Needed: func(cfg *config.Config) bool { ok, _, _ := checkProto(cfg); return !ok },
			Run:    FixProto,
		},
		{
			Name: "point the `oak` CLI at this stack",
			// Idempotent, so it runs whenever `oak` exists rather than trying
			// to detect the current configuration.
			Needed: func(*config.Config) bool { return oakOnPath() },
			Skip: func(*config.Config) string {
				if oakOnPath() {
					return ""
				}
				return "`oak` is not installed - see https://github.com/oakestra/oakestra-cli"
			},
			Run: FixOakConfig,
		},
	}
}

func oakOnPath() bool {
	_, err := exec.LookPath("oak")
	return err == nil
}

func checkOakestraRepo(cfg *config.Config) (bool, string, string) {
	path := filepath.Join(cfg.OakestraRepo, "version.txt")
	if _, err := os.Stat(path); err != nil {
		return false, cfg.OakestraRepo + " has no version.txt", "set OAKESTRA_REPO in .env or oak-dev.yaml to a real oakestra checkout"
	}
	return true, cfg.OakestraRepo, ""
}

func checkDockerCompose(cfg *config.Config) (bool, string, string) {
	if _, err := exec.LookPath("docker"); err != nil {
		return false, "docker not found", "install Docker Desktop or OrbStack"
	}
	out, err := exec.Command("docker", "compose", "version", "--short").Output()
	if err != nil {
		return false, "docker compose not available", "install the Docker Compose v2 plugin (>= 2.18)"
	}
	version := strings.TrimSpace(string(out))
	major, minor := parseMajorMinor(version)
	if major < 2 || (major == 2 && minor < 18) {
		return false, "docker compose " + version, "upgrade to Docker Compose >= 2.18"
	}
	return true, "docker compose " + version, ""
}

func parseMajorMinor(v string) (int, int) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0
	}
	maj, _ := strconv.Atoi(parts[0])
	min, _ := strconv.Atoi(parts[1])
	return maj, min
}

func checkKernelMongoPin(cfg *config.Config) (bool, string, string) {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return true, "could not read kernel version, skipping", ""
	}
	kernel := strings.TrimSpace(string(out))
	pinFiles := []string{
		filepath.Join(cfg.RepoRoot, "compose", "override-root-mongo.yml"),
		filepath.Join(cfg.RepoRoot, "compose", "override-cluster-mongo.yml"),
	}
	for _, f := range pinFiles {
		if _, err := os.Stat(f); err != nil {
			return false, "kernel " + kernel + " but " + f + " is missing", "restore compose/override-*-mongo.yml - mongo:8.0 refuses to start on kernel 6.19+"
		}
	}
	return true, "kernel " + kernel + ", mongo pinned to 8.2 via compose overrides", ""
}

func checkArch(cfg *config.Config) (bool, string, string) {
	hostOut, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return true, "could not read host arch, skipping", ""
	}
	host := strings.TrimSpace(string(hostOut))
	dockerOut, err := exec.Command("docker", "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		return true, "docker not reachable, skipping", ""
	}
	dockerArch := strings.TrimSpace(string(dockerOut))
	norm := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
	if norm[host] != "" && norm[dockerArch] != "" && norm[host] != norm[dockerArch] {
		return false, fmt.Sprintf("host %s vs docker engine %s", host, dockerArch),
			"OrbStack/Docker Desktop sometimes omits TARGETARCH - check your engine's platform settings"
	}
	return true, fmt.Sprintf("host %s, docker engine %s", host, dockerArch), ""
}

func checkProto(cfg *config.Config) (bool, string, string) {
	targets := []string{
		filepath.Join(cfg.OakestraRepo, "root_orchestrator", "system-manager-python", "proto", "clusterRegistration_pb2.py"),
		filepath.Join(cfg.OakestraRepo, "cluster_orchestrator", "cluster-manager", "proto", "clusterRegistration_pb2.py"),
	}
	var missing []string
	for _, t := range targets {
		if _, err := os.Stat(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("%d missing", len(missing)), "run `oak-dev doctor --fix`"
	}
	return true, "present", ""
}

func checkGoToolchain(cfg *config.Config) (bool, string, string) {
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return false, "go not found", "install Go - it builds oak-dev itself, plus the cross-compiled scheduler/NodeEngine binaries"
	}
	return true, strings.TrimSpace(string(out)), ""
}

func checkWatchexec(cfg *config.Config) (bool, string, string) {
	if _, err := exec.LookPath("watchexec"); err != nil {
		return false, "not found", "brew install watchexec (or see watchexec.github.io) - only needed for `oak-dev dev`"
	}
	return true, "found", ""
}

func checkOakCLI(cfg *config.Config) (bool, string, string) {
	if _, err := exec.LookPath("oak"); err != nil {
		return false, "not found", "install oakestra-cli, then `oak-dev doctor --fix` to point it at this stack - only needed for `oak-dev status`"
	}
	return true, "found", ""
}

func checkStaleNetwork(cfg *config.Config) (bool, string, string) {
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "label=com.docker.compose.project=oakestra-worker").Output()
	if err != nil {
		return true, "could not inspect volumes, skipping", ""
	}
	vols := strings.Fields(string(out))
	netOut, _ := exec.Command("docker", "network", "inspect", "oakestra", "--format", "{{len .Containers}}").Output()
	containers := strings.TrimSpace(string(netOut))
	if containers == "0" && len(vols) > 0 {
		return false, fmt.Sprintf("oakestra network is empty but %d worker volume(s) remain", len(vols)),
			"run `oak-dev down --volumes` or `docker volume prune` - stale containerd volumes accumulate across worker recreations"
	}
	return true, "clean", ""
}
