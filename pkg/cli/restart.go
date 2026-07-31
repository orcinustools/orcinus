package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newRestartCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "restart <service>",
		Short: "Restart a service's pods (rolling, no spec change)",
		Long: `Roll every pod of a service, like ` + "`kubectl rollout restart`" + `.

The workload's spec is untouched — only its pod template is re-stamped, so the
controller replaces the pods through its normal rolling update. Use it to pick
up a changed Secret/ConfigMap, re-pull a moving image tag, or clear a wedged
process. Volumes are never affected.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			kind, err := applier.Restart(cmd.Context(), namespace, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restarted %s/%s\n", kind, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}
