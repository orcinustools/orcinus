package cli

import (
	"fmt"
	"os"
	"sort"
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
	cmd.AddCommand(newSecretCreateCmd(), newSecretSetCmd(), newSecretGetCmd(), newSecretCreateTLSCmd(),
		newSecretCreateRegistryCmd(), newSecretLsCmd(), newSecretRmCmd())
	return cmd
}

// parseLiterals turns repeated --from-literal KEY=VALUE flags into Secret data.
func parseLiterals(literals []string) (map[string][]byte, error) {
	if len(literals) == 0 {
		return nil, fmt.Errorf("provide at least one --from-literal KEY=VALUE")
	}
	data := map[string][]byte{}
	for _, kv := range literals {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			return nil, fmt.Errorf("invalid --from-literal %q (want KEY=VALUE)", kv)
		}
		data[kv[:i]] = []byte(kv[i+1:])
	}
	return data, nil
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
		Short: "Create an opaque Secret, replacing it if it exists",
		Long: `Create an opaque Secret from --from-literal KEY=VALUE pairs.

An existing Secret of the same name is REPLACED: keys not passed here are
dropped. To change one key and keep the rest, use ` + "`orcinus secret set`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := parseLiterals(literals)
			if err != nil {
				return err
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			// Read first, so replacing an existing Secret can say what it drops
			// rather than losing keys silently.
			var dropped []string
			if prev, err := a.GetSecret(cmd.Context(), namespace, args[0]); err == nil {
				for _, k := range prev.KeyNames() {
					if _, kept := data[k]; !kept {
						dropped = append(dropped, k)
					}
				}
			}
			if err := a.ApplySecret(cmd.Context(), namespace, args[0], corev1.SecretTypeOpaque, data); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "secret %q created (%d key(s)) — load it into a service with `x-orcinus-env-from-secret: %s`\n",
				args[0], len(data), args[0])
			if len(dropped) > 0 {
				fmt.Fprintf(out, "warning: replaced the existing secret and dropped %d key(s): %s\n",
					len(dropped), strings.Join(dropped, ", "))
				fmt.Fprintf(out, "         use `orcinus secret set` to change keys without dropping the others\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringArrayVar(&literals, "from-literal", nil, "KEY=VALUE (repeatable)")
	return cmd
}

func newSecretSetCmd() *cobra.Command {
	var kubeconfig, namespace string
	var literals []string
	cmd := &cobra.Command{
		Use:   "set <name> --from-literal KEY=VALUE [...]",
		Short: "Set keys on a Secret, keeping the ones not named",
		Long: `Set individual keys on a Secret without touching the rest.

Unlike ` + "`orcinus secret create`" + `, keys that are not named here are kept.
The Secret is created if it does not exist yet.

Pods do not pick up a changed Secret on their own — env vars are injected when
the container starts. Restart the service afterwards (` + "`orcinus restart <service>`" + `).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := parseLiterals(literals)
			if err != nil {
				return err
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			existed := false
			if _, err := a.GetSecret(cmd.Context(), namespace, args[0]); err == nil {
				existed = true
			}
			if err := a.MergeSecret(cmd.Context(), namespace, args[0], corev1.SecretTypeOpaque, data); err != nil {
				return err
			}
			keys := make([]string, 0, len(data))
			for k := range data {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			out := cmd.OutOrStdout()
			if !existed {
				fmt.Fprintf(out, "secret %q created (%d key(s))\n", args[0], len(keys))
				return nil
			}
			after, err := a.GetSecret(cmd.Context(), namespace, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "secret %q updated: set %s (%d key(s) total)\n",
				args[0], strings.Join(keys, ", "), len(after.Data))
			fmt.Fprintf(out, "         running pods keep the old values until restarted (`orcinus restart <service>`)\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().StringArrayVar(&literals, "from-literal", nil, "KEY=VALUE (repeatable)")
	return cmd
}

func newSecretGetCmd() *cobra.Command {
	var kubeconfig, namespace string
	var showValues bool
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show a Secret's keys (values only with --show-values)",
		Long: `Show what a Secret holds.

Keys are listed by default and values are withheld, so this is safe to run in
a shared terminal. Pass --show-values to print them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			s, err := a.GetSecret(cmd.Context(), namespace, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Name:\t%s\n", s.Name)
			fmt.Fprintf(out, "Namespace:\t%s\n", s.Namespace)
			fmt.Fprintf(out, "Type:\t%s\n", s.Type)
			fmt.Fprintf(out, "Managed by orcinus:\t%t\n", s.ManagedBy)

			keys := s.KeyNames()
			if len(keys) == 0 {
				fmt.Fprintln(out, "\nNo keys.")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 2, 3, ' ', 0)
			fmt.Fprintln(w, "\nKEY\tVALUE")
			for _, k := range keys {
				if showValues {
					fmt.Fprintf(w, "%s\t%s\n", k, s.Data[k])
					continue
				}
				fmt.Fprintf(w, "%s\t<%d bytes, hidden>\n", k, len(s.Data[k]))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if !showValues {
				fmt.Fprintln(out, "\nValues hidden. Re-run with --show-values to print them.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().BoolVar(&showValues, "show-values", false, "print the values instead of hiding them")
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
	var insecure, skipLoginCheck bool
	cmd := &cobra.Command{
		Use:   "create-registry <name> --server <host> --username <user> --password <pass>",
		Short: "Create/update a private-registry pull secret (verifies login first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if server == "" || username == "" || password == "" {
				return fmt.Errorf("--server, --username and --password are required")
			}
			out := cmd.OutOrStdout()
			// Test the credentials against the registry BEFORE storing the secret.
			if !skipLoginCheck {
				fmt.Fprintf(out, "testing login to %s ...\n", server)
				if err := deploy.VerifyRegistryLogin(cmd.Context(), server, username, password, insecure); err != nil {
					return err
				}
				fmt.Fprintln(out, "login OK")
			}
			a, err := applierFor(kubeconfig)
			if err != nil {
				return err
			}
			if err := a.ApplyDockerRegistrySecret(cmd.Context(), namespace, args[0], server, username, password, email); err != nil {
				return err
			}
			fmt.Fprintf(out,
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
	cmd.Flags().BoolVar(&insecure, "insecure", false, "skip TLS verification when testing login (self-signed registries)")
	cmd.Flags().BoolVar(&skipLoginCheck, "skip-login-check", false, "create the secret without testing the login first")
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
				fmt.Fprintf(w, "%s\t%s\t%s\t%t\n", s.Name, s.Type, summarizeKeys(s.KeyNames), s.ManagedBy)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	return cmd
}

// summarizeKeys renders a Secret's key names for the ls table, keeping the row
// scannable when a Secret holds many of them. `orcinus secret get` shows all.
func summarizeKeys(keys []string) string {
	const max = 3
	switch {
	case len(keys) == 0:
		return "-"
	case len(keys) <= max:
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(keys[:max], ", "), len(keys)-max)
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
