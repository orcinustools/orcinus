package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newRmCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "rm <project>",
		Short: "Remove all resources of an orcinus-managed project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			n, err := applier.RemoveProject(cmd.Context(), project, namespace)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %d resource(s) from project %q\n", n, project)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace to remove from")
	return cmd
}
