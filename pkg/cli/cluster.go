package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
)

// newClusterCmd groups the cluster-lifecycle commands under `orcinus cluster`.
// Running `orcinus cluster` (or `--help`) lists its subcommands.
func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage the orcinus cluster (init, join, status, down)",
		Long:  "Manage the orcinus cluster lifecycle.\n\nRun a subcommand, e.g. `orcinus cluster init`.",
	}
	cmd.AddCommand(
		newInitCmd(),
		newJoinCmd(),
		newStatusCmd(),
		newDownCmd(),
		newNetfixCmd(),
	)
	return cmd
}

// newNetfixCmd is the hidden helper behind the cross-host VXLAN fix: init/join
// copy the orcinus binary into the node container and run this command there
// (the k3s image ships no ethtool).
func newNetfixCmd() *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:    "netfix [iface]",
		Hidden: true,
		Short:  "Disable VXLAN TX checksum offload on an interface (runs inside a node container)",
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			iface := "flannel.1"
			if len(args) > 0 {
				iface = args[0]
			}
			return cluster.DisableVXLANChecksumOffload(iface, wait)
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 5*time.Minute, "how long to wait for the interface to appear")
	return cmd
}
