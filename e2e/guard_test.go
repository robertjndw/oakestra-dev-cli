//go:build !e2e

package e2e

import "testing"

// TestE2ESuiteNeedsItsBuildTag exists so `go test ./e2e/...` (no -tags e2e)
// prints a skip instead of `ok oak-dev/e2e [no test files]` - which looks
// exactly like a passing suite to anyone skimming CI output or a terminal
// scrollback, when in fact none of the real tests ever compiled.
func TestE2ESuiteNeedsItsBuildTag(t *testing.T) {
	t.Skip("the Oakestra E2E suite needs a running stack: run `oak-dev test`, " +
		"or `go test -tags e2e -count=1 ./e2e/...`")
}
