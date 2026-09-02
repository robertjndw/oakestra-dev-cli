// Package oakapi is the stack-free plumbing behind the Oakestra E2E suite:
// an HTTP client for the root API, SLA/job document types, a generic
// poll-until-ready helper, and the settings that tie them to a running
// stack. It has no Docker or filesystem dependency beyond .env, so it
// compiles and tests in `go test ./... -race` with no stack running - the
// e2e package (build-tagged, not part of this package) is what actually
// needs one.
package oakapi

import (
	"os"
	"time"

	"oak-dev/internal/config"
)

// Settings mirrors config.E2ECfg. It is redeclared here rather than reused
// directly so that internal/config - a package the E2E binary otherwise has
// no reason to import beyond LoadSettings - stays an implementation detail
// callers in e2e/ don't need to know about.
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
// oakestra-dev-cli checkout (config.FindRoot, walking up from the process
// cwd for compose/worker.yml) and reading its .env through config.LoadE2E.
//
// If FindRoot fails, this falls back to process-env-plus-defaults instead of
// returning an error: `go test -c` produces a standalone binary that can be
// copied and run from anywhere, and a suite that only needs OAK_ROOT_API et
// al set as plain environment variables should still work from such a
// binary - it's only the convenience of reading .env that's lost.
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
