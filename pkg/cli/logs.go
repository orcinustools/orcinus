package cli

import (
	"github.com/spf13/cobra"

	"github.com/biznetgio/orcinus/pkg/deploy"
)

func newLogsCmd() *cobra.Command {
	var kubeconfig, namespace, project string
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "Stream logs of an orcinus-managed service",
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
			return applier.StreamServiceLogs(cmd.Context(), args[0], project, namespace, follow, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringVar(&project, "project", "", "further scope to a project")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log stream")
	return cmd
}
