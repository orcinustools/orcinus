// Command orcinus is a single, multicall binary — Compose-simple, Cluster-strong:
// a lightweight cluster runtime that runs docker-compose files and Kubernetes
// manifests natively, no translation.
package main

import (
	"os"

	"github.com/orcinustools/orcinus/pkg/cli"
)

func main() {
	root := cli.NewRootCmd()
	// Cobra prints the error (and, for usage errors, the command help) itself;
	// here we only need to set the non-zero exit code.
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
