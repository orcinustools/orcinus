// Command orcinus is a single, multicall binary: a lightweight k3s-based
// Kubernetes distribution that natively understands docker-compose.
package main

import (
	"fmt"
	"os"

	"github.com/biznetgio/orcinus/pkg/cli"
)

func main() {
	root := cli.NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
