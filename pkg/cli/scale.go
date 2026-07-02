package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"
)

func newScaleCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "scale <service> <replicas>",
		Short: "Set the replica count of a service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 {
				return fmt.Errorf("replicas must be a non-negative integer, got %q", args[1])
			}
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			kind, err := applier.Scale(cmd.Context(), namespace, args[0], int32(n))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "scaled %s/%s to %d replicas\n", kind, args[0], n)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}

func newAutoscaleCmd() *cobra.Command {
	var kubeconfig, namespace string
	var min, max, cpu, memory int
	cmd := &cobra.Command{
		Use:   "autoscale <service>",
		Short: "Create/update a HorizontalPodAutoscaler for a service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if max < 1 {
				return fmt.Errorf("--max must be >= 1")
			}
			cfg, err := deploy.LoadRESTConfig(kubeconfig)
			if err != nil {
				return err
			}
			applier, err := deploy.NewApplier(cfg)
			if err != nil {
				return err
			}
			if err := applier.Autoscale(cmd.Context(), namespace, args[0], deploy.AutoscaleSpec{
				Min: int32(min), Max: int32(max), CPU: int32(cpu), Memory: int32(memory),
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "autoscaling %s: %d-%d replicas\n", args[0], min, max)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)")
	f.StringVarP(&namespace, "namespace", "n", "default", "namespace")
	f.IntVar(&min, "min", 1, "minimum replicas")
	f.IntVar(&max, "max", 0, "maximum replicas (required)")
	f.IntVar(&cpu, "cpu", 0, "target average CPU utilization %% (default 80 if no metric set)")
	f.IntVar(&memory, "memory", 0, "target average memory utilization %%")
	return cmd
}
