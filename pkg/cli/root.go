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
		Use:   "orcinus",
		Short: "Compose-simple. Cluster-strong. Run docker-compose files and Kubernetes manifests natively.",
		// Usage is printed for *usage* errors (missing/extra args, unknown flags)
		// so the user sees the error and the command's help together. Once arg and
		// flag validation has passed, PersistentPreRunE silences usage so a later
		// runtime failure (no cluster, network, …) prints just the error, not the
		// whole help text. Cobra prints the error itself (SilenceErrors stays off).
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return nil
		},
	}

	root.AddCommand(
		newClusterCmd(),
		newNodeCmd(),
		newPluginCmd(),
		newDeployCmd(),
		newRmCmd(),
		newLsCmd(),
		newPsCmd(),
		newLogsCmd(),
		newDescribeCmd(),
		newScaleCmd(),
		newAutoscaleCmd(),
		newRollbackCmd(),
		newSecretCmd(),
		newKubectlCmd(),
		newRuntimeCmd(),
		newAPICmd(),
		newSkillsCmd(),
		newMCPCmd(),
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
