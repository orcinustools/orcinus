package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/cluster"
)

func newJoinCmd() *cobra.Command {
	o := cluster.JoinOptions{}
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a node to an existing cluster",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cluster.Join(o); err != nil {
				return err
			}
			name := o.Name
			if name == "" {
				name = "agent"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "node %q is joining the cluster\n", name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.ServerURL, "server", "", "cluster server URL (default: from saved state)")
	f.StringVar(&o.Token, "token", "", "join token (default: from saved state)")
	f.StringVar(&o.Name, "name", "", "agent node/container name")
	f.StringVar(&o.Image, "image", "", "cluster runtime image (default: from saved state)")
	return cmd
}
