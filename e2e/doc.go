// Package e2e is the Go successor to tests/: end-to-end tests that drive a
// live Oakestra stack (all three compose projects) through internal/oakapi.
// It has no build tag itself - see guard_test.go and harness_test.go, which
// do - so `go build ./...` always finds Go files here, but the tests
// themselves only compile and run under `-tags e2e`.
//
// # Running
//
//	go test -tags e2e -count=1 -timeout 45m ./e2e/... -v
//
// oak-dev test builds this command line for you, deriving -timeout from the
// configured ready/deploy timeouts rather than trusting the 10-minute
// default - the deployment and network tests alone can poll for most of an
// hour in the worst case, and a blown -timeout prints a goroutine dump for
// every live goroutine instead of a readable test failure.
//
// -count=1 is not optional. go test caches a successful run keyed on package
// files and the env vars it reads - not on network I/O - so re-running
// against a stack that has since broken can replay a stale PASS in
// milliseconds. Always pass it, or trust that oak-dev test does.
//
// # Smoke mode
//
// TestSmokeHealth and TestSmokeRegistration are the two tests safe to run
// against a stack that only just started - they probe "is anything alive at
// all" and "did the worker register" without deploying anything. Smoke mode
// is exactly `-run '^TestSmoke'`; the TestSmoke prefix on a function name IS
// the smoke-mode contract; there is no separate list to keep in sync. A test
// that belongs in smoke mode must be named TestSmoke*, full stop.
//
// # Ordering
//
// Files are numbered (01_health, 02_registration, ...) and Go compiles a
// package's files, and runs its top-level tests, in that sorted-filename
// order - so a plain `go test -tags e2e ./e2e/...` reproduces the pytest
// suite's file ordering. But that is fail-fast ergonomics only: it decides
// which top-level test reports first, nothing more. The ordering that
// actually matters - deploy before scale before undeploy, web before client -
// is structural, encoded as ordered subtests within a single top-level test
// (see TestDeploymentLifecycle, TestOverlayNetwork) using
// `if !t.Run(name, fn) { return }`. Don't rely on cross-file ordering for
// anything that must not run after a prior step failed.
//
// # No t.Parallel
//
// Nothing in this package calls t.Parallel, anywhere, ever. Every test in
// this suite contends for the same one worker node and the same one cluster;
// concurrent subtests would race deploys, scale operations and node capacity
// against each other and produce failures that have nothing to do with the
// thing under test. If a future test genuinely doesn't share state with the
// rest, that's still not a reason to parallelize it here - keep this
// package's execution model uniform and easy to reason about.
package e2e
