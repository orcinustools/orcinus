// Package cli wires the orcinus command tree (CLI.md §2).
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/biznetgio/orcinus/pkg/version"
)

// NewRootCmd builds the top-level orcinus command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "orcinus",
		Short:         "Lightweight Kubernetes (k3s) that natively understands docker-compose",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newDeployCmd(),
		newVersionCmd(),
		// Cluster-runtime & lifecycle commands (implemented in later milestones).
		newStubCmd("init", "Make this node a control plane (M3)"),
		newStubCmd("join", "Join a cluster as a node (M3)"),
		newStubCmd("rm", "Remove an orcinus-managed project (M2)"),
		newStubCmd("ls", "List orcinus-managed projects (M2)"),
		newStubCmd("ps", "List a project's pods/tasks (M2)"),
		newStubCmd("logs", "Show service logs (M2)"),
		newStubCmd("kubectl", "Passthrough to the built-in kubectl (M3)"),
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

// newStubCmd is a placeholder for commands landing in M2/M3 so `--help` and the
// command tree are complete today.
func newStubCmd(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%q is not implemented yet (see ARCHITECTURE.md §10 roadmap)", use)
		},
	}
}
