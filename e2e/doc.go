// Package e2e is the Go successor to tests/: end-to-end tests that drive a
// live Oakestra stack (all three compose projects) through internal/oakapi.
// This file has no build tag, so `go build ./...` always finds the package.
// The actual tests do carry `-tags e2e` (see guard_test.go and
// harness_test.go) and only compile and run when it's passed.
//
// # Running
//
//	go test -tags e2e -count=1 -timeout 45m ./e2e/... -v
//
// oak-dev test builds this command line for you and derives -timeout from
// the configured ready/deploy timeouts instead of trusting the 10-minute
// default. The deployment and network tests alone can poll for most of an
// hour in the worst case, and a blown -timeout prints a goroutine dump
// instead of a readable test failure.
//
// -count=1 matters here: go test caches a successful run keyed on package
// files and env vars, not on network I/O, so re-running against a stack
// that's since broken can replay a stale PASS in milliseconds. Always pass
// it, or trust that oak-dev test does.
//
// # Smoke mode
//
// TestSmokeHealth and TestSmokeRegistration are safe to run against a stack
// that just started: they check "is anything alive" and "did the worker
// register" without deploying anything. Smoke mode is `-run '^TestSmoke'` -
// the TestSmoke prefix on a function name is the whole contract, there's no
// separate list to keep in sync. A smoke-safe test just needs that name.
//
// # Ordering
//
// Files are numbered (01_health, 02_registration, ...), and Go compiles and
// runs a package's top-level tests in that sorted-filename order, so a plain
// `go test -tags e2e ./e2e/...` reproduces the old pytest file ordering. But
// that only decides which top-level test reports first. The ordering that
// actually matters - deploy before scale before undeploy, web before client -
// is enforced within a single top-level test as ordered subtests (see
// TestDeploymentLifecycle, TestOverlayNetwork), using
// `if !t.Run(name, fn) { return }`. Don't rely on cross-file ordering for
// anything that must not run after a prior step failed.
//
// # No t.Parallel
//
// Nothing in this package calls t.Parallel. Every test shares the same one
// worker node and cluster, so concurrent subtests would race deploys, scale
// operations, and node capacity against each other, and the failures that
// come out of that race have nothing to do with what's actually being
// tested. Even a test that doesn't share state with the others should stay
// serial, just to keep this package's execution model consistent.
package e2e
