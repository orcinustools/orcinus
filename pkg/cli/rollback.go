package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newRollbackCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "rollback <service>",
		Short: "Roll a service back to its previous revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			kind, err := applier.Rollback(cmd.Context(), namespace, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rolled back %s/%s to the previous revision\n", kind, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}
