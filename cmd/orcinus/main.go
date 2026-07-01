// Command orcinus is a single, multicall binary: a lightweight k3s-based
// Kubernetes distribution that natively understands docker-compose.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/biznetgio/orcinus/pkg/cli"
)

func main() {
	args := os.Args[1:]

	// Multicall: if invoked under a known component name (e.g. symlinked as
	// `kubectl`), dispatch to that subcommand (ARCHITECTURE.md §2.2).
	switch filepath.Base(os.Args[0]) {
	case "kubectl":
		args = append([]string{"kubectl"}, args...)
	}

	root := cli.NewRootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
