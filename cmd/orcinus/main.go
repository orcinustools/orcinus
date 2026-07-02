// Command orcinus is a single, multicall binary — Compose-simple, Cluster-strong:
// a lightweight cluster runtime that runs docker-compose files and Kubernetes
// manifests natively, no translation.
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
