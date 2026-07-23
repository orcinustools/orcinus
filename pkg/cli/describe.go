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
	cmd.AddCommand(
		newDescribePodCmd(),
		newDescribeServiceCmd(),
		newDescribeProjectCmd(),
		newDescribeNodeCmd(),
	)
	return cmd
}

// exactName builds an Args validator that requires exactly one <noun> argument
// and explains what is missing, e.g. `orcinus describe service <name>`.
func exactName(noun, usage string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return fmt.Errorf("missing %s name: specify which %s to describe, e.g. `%s`", noun, noun, usage)
		case len(args) > 1:
			return fmt.Errorf("too many arguments: describe one %s at a time, got %d (%v)", noun, len(args), args)
		}
		return nil
	}
}

func newDescribePodCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:     "pod <name>",
		Aliases: []string{"pods", "po"},
		Short:   "Show detailed information about a pod (kubectl-style, with events)",
		Args:    exactName("pod", "orcinus describe pod <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			applier, err := newApplier(kubeconfig)
			if err != nil {
				return err
			}
			return applier.DescribePod(cmd.Context(), args[0], namespace, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", kubeconfigFlagHelp)
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}

func newDescribeServiceCmd() *cobra.Command {
	var kubeconfig, namespace, project string
	cmd := &cobra.Command{
		Use:     "service <name>",
		Aliases: []string{"services", "svc"},
		Short:   "Show detailed information about a service's workload (Deployment/StatefulSet), with events",
		Args:    exactName("service", "orcinus describe service <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			applier, err := newApplier(kubeconfig)
			if err != nil {
				return err
			}
			return applier.DescribeService(cmd.Context(), args[0], project, namespace, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", kubeconfigFlagHelp)
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringVar(&project, "project", "", "further scope to a project")
	return cmd
}

func newDescribeProjectCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:     "project <name>",
		Aliases: []string{"projects", "proj", "app"},
		Short:   "Show an aggregate summary of a project (its workloads and pods)",
		Args:    exactName("project", "orcinus describe project <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			applier, err := newApplier(kubeconfig)
			if err != nil {
				return err
			}
			return applier.DescribeProject(cmd.Context(), args[0], namespace, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", kubeconfigFlagHelp)
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace (default: all namespaces)")
	return cmd
}

func newDescribeNodeCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:     "node <name>",
		Aliases: []string{"nodes", "no"},
		Short:   "Show detailed information about a cluster node (kubectl-style, with events)",
		Args:    exactName("node", "orcinus describe node <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			applier, err := newApplier(kubeconfig)
			if err != nil {
				return err
			}
			return applier.DescribeNode(cmd.Context(), args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", kubeconfigFlagHelp)
	return cmd
}

const kubeconfigFlagHelp = "path to kubeconfig (default: ~/.orcinus/kubeconfig, $KUBECONFIG, or ~/.kube/config)"

func newApplier(kubeconfig string) (*deploy.Applier, error) {
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return deploy.NewApplier(cfg)
}
