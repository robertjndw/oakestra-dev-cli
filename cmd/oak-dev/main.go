// Command oak-dev is the developer-workflow CLI for oakestra-dev-cli:
// bringing up partial stacks, live-mounting source instead of rebuilding
// images, cross-compiling Go services, attaching debuggers, and watching the
// control plane. It is a separate binary from `oak` (oakestra-cli, the
// user-facing CLI operators install) - see CLAUDE.md and README.md for why.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "oak-dev:", err)
		os.Exit(1)
	}
}
