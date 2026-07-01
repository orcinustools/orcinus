// Command orcinus is a single, multicall binary: a lightweight Kubernetes
// distribution that natively understands docker-compose.
package main

import (
	"fmt"
	"os"

	"github.com/orcinustools/orcinus/pkg/cli"
)

func main() {
	root := cli.NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
