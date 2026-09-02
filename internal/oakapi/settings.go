// Package oakapi is the stack-free plumbing behind the Oakestra E2E suite:
// an HTTP client for the root API, SLA/job document types, a generic
// poll-until-ready helper, and the settings that tie them to a running
// stack. It has no Docker or filesystem dependency beyond .env, so
// `go test ./... -race` can compile and run it with no stack running.
// Actually needing a live stack is the build-tagged e2e package's job, not
// this one's.
package oakapi

import (
	"os"
	"time"

	"oak-dev/internal/config"
)

// Settings mirrors config.E2ECfg. It's redeclared here instead of reused
// directly so callers in e2e/ don't need to know about internal/config,
// which this package otherwise only imports for LoadSettings.
type Settings struct {
	RootAPI       string
	ClusterAPI    string
	RootRA        string
	Username      string
	Password      string
	ReadyTimeout  time.Duration
	DeployTimeout time.Duration
}

// LoadSettings resolves the E2E suite's settings by locating the
// oakestra-dev-cli checkout (config.FindRoot walks up from the process cwd
// looking for compose/worker.yml) and reading its .env through
// config.LoadE2E.
//
// If FindRoot fails, we fall back to process-env-plus-defaults rather than
// erroring out. `go test -c` produces a standalone binary that can be
// copied and run from anywhere, so a suite that just needs OAK_ROOT_API et
// al set as plain env vars should keep working; it only loses the
// convenience of reading .env.
func LoadSettings() (Settings, error) {
	wd, err := os.Getwd()
	if err != nil {
		return Settings{}, err
	}

	root, err := config.FindRoot(wd)
	if err != nil {
		root = wd
	}

	e2e := config.LoadE2E(root)
	return Settings{
		RootAPI:       e2e.RootAPI,
		ClusterAPI:    e2e.ClusterAPI,
		RootRA:        e2e.RootRA,
		Username:      e2e.Username,
		Password:      e2e.Password,
		ReadyTimeout:  e2e.ReadyTimeout,
		DeployTimeout: e2e.DeployTimeout,
	}, nil
}
