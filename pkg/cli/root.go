// Package cli wires the orcinus command tree (docs/USAGE.md §5).
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/version"
)

// NewRootCmd builds the top-level orcinus command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "orcinus",
		Short:         "Lightweight Kubernetes that natively understands docker-compose",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newClusterCmd(),
		newPluginCmd(),
		newDeployCmd(),
		newRmCmd(),
		newLsCmd(),
		newPsCmd(),
		newLogsCmd(),
		newVersionCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "orcinus %s (commit %s)\n", version.Version, version.GitCommit)
			fmt.Fprintf(cmd.OutOrStdout(), "kompose (forked): %s\n", version.KomposeRef)
			return nil
		},
	}
}
