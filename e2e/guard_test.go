//go:build !e2e

package e2e

import "testing"

// TestE2ESuiteNeedsItsBuildTag makes `go test ./e2e/...` (without -tags e2e)
// print a skip instead of `ok oak-dev/e2e [no test files]`. The latter looks
// like a passing suite if you're skimming CI output, but none of the real
// tests even compiled.
func TestE2ESuiteNeedsItsBuildTag(t *testing.T) {
	t.Skip("the Oakestra E2E suite needs a running stack: run `oak-dev test`, " +
		"or `go test -tags e2e -count=1 ./e2e/...`")
}
