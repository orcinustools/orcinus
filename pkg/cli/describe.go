package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

// newDescribeCmd wires `orcinus describe <resource> <name>`, mirroring
// `kubectl describe`. Only `pod` is supported today.
func newDescribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe",
		Short: "Show detailed information about a resource",
	}
	cmd.AddCommand(newDescribePodCmd())
	return cmd
}

func newDescribePodCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:     "pod <name>",
		Aliases: []string{"pods", "po"},
		Short:   "Show detailed information about a pod (kubectl-style, with events)",
		Args: func(_ *cobra.Command, args []string) error {
			switch {
			case len(args) == 0:
				return fmt.Errorf("missing pod name: specify which pod to describe, e.g. `orcinus describe pod <name>`")
			case len(args) > 1:
				return fmt.Errorf("too many arguments: describe one pod at a time, got %d (%v)", len(args), args)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			return applier.DescribePod(cmd.Context(), args[0], namespace, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}
