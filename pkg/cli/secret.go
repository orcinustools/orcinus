package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/deploy"

	corev1 "k8s.io/api/core/v1"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage Kubernetes Secrets (generic and TLS)",
	}
	cmd.AddCommand(newSecretCreateCmd(), newSecretCreateTLSCmd(), newSecretCreateRegistryCmd(), newSecretLsCmd(), newSecretRmCmd())
	return cmd
}

func applierFor(kubeconfig string) (*deploy.Applier, error) {
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return deploy.NewApplier(cfg)
}

func newSecretCreateCmd() *cobra.Command {
	var kubeconfig, namespace string
	var literals []string
	cmd := &cobra.Command{
		Use:   "create <name> --from-literal KEY=VALUE [...]",
		Short: "Create/update an opaque Secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(literals) == 0 {
				return fmt.Errorf("provide at least one --from-literal KEY=VALUE")
			}
			data := map[string][]byte{}
			for _, kv := range literals {
				i := strings.IndexByte(kv, '=')
				if i <= 0 {
					return fmt.Errorf("invalid --from-literal %q (want KEY=VALUE)", kv)
				}
				data[kv[:i]] = []byte(kv[i+1:])
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			if err := a.ApplySecret(cmd.Context(), namespace, args[0], corev1.SecretTypeOpaque, data); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret %q created (%d key(s))\n", args[0], len(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringArrayVar(&literals, "from-literal", nil, "KEY=VALUE (repeatable)")
	return cmd
}

func newSecretCreateTLSCmd() *cobra.Command {
	var kubeconfig, namespace, certFile, keyFile string
	cmd := &cobra.Command{
		Use:   "create-tls <name> --cert <file> --key <file>",
		Short: "Create/update a TLS Secret from a cert + key (custom/BYO cert)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if certFile == "" || keyFile == "" {
				return fmt.Errorf("--cert and --key are required")
			}
			cert, err := os.ReadFile(certFile)
			if err != nil {
				return fmt.Errorf("read cert: %w", err)
			}
			key, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read key: %w", err)
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			data := map[string][]byte{"tls.crt": cert, "tls.key": key}
			if err := a.ApplySecret(cmd.Context(), namespace, args[0], corev1.SecretTypeTLS, data); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "TLS secret %q created — use it with `x-orcinus-tls-secret: %s`\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringVar(&certFile, "cert", "", "path to the certificate (PEM)")
	cmd.Flags().StringVar(&keyFile, "key", "", "path to the private key (PEM)")
	return cmd
}

func newSecretCreateRegistryCmd() *cobra.Command {
	var kubeconfig, namespace, server, username, password, email string
	cmd := &cobra.Command{
		Use:   "create-registry <name> --server <host> --username <user> --password <pass>",
		Short: "Create/update a private-registry pull secret (docker login)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if server == "" || username == "" || password == "" {
				return fmt.Errorf("--server, --username and --password are required")
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			if err := a.ApplyDockerRegistrySecret(cmd.Context(), namespace, args[0], server, username, password, email); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"registry secret %q created — use it with `x-orcinus-image-pull-secret: %s`\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringVar(&server, "server", "", "registry host (e.g. registry.example.com, ghcr.io, docker.io)")
	cmd.Flags().StringVarP(&username, "username", "u", "", "registry username")
	cmd.Flags().StringVarP(&password, "password", "p", "", "registry password or token")
	cmd.Flags().StringVar(&email, "email", "", "registry email (optional)")
	return cmd
}

func newSecretLsCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List Secrets in a namespace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			secrets, err := a.ListSecrets(cmd.Context(), namespace)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tKEYS\tMANAGED-BY-ORCINUS")
			for _, s := range secrets {
				fmt.Fprintf(w, "%s\t%s\t%d\t%t\n", s.Name, s.Type, s.Keys, s.ManagedBy)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}

func newSecretRmCmd() *cobra.Command {
	var kubeconfig, namespace string
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a Secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			if err := a.DeleteSecret(cmd.Context(), namespace, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret %q deleted\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}
