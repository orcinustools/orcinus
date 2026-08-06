package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// secretKeyRe is what Kubernetes accepts as a Secret data key. A file whose
// name does not fit has to be given an explicit key.
var secretKeyRe = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// secretData builds the Secret payload from --from-literal and --from-file.
// Files are read as raw bytes, so binary content and trailing newlines survive
// intact — neither does when a value is routed through a shell argument.
func secretData(literals, files []string) (map[string][]byte, error) {
	if len(literals) == 0 && len(files) == 0 {
		return nil, fmt.Errorf("provide at least one --from-literal KEY=VALUE or --from-file PATH")
	}
	data := map[string][]byte{}
	if err := addLiterals(data, literals); err != nil {
		return nil, err
	}
	if err := addFiles(data, files); err != nil {
		return nil, err
	}
	return data, nil
}

// addLiterals folds KEY=VALUE pairs into data.
func addLiterals(data map[string][]byte, literals []string) error {
	for _, kv := range literals {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			return fmt.Errorf("invalid --from-literal %q (want KEY=VALUE)", kv)
		}
		key := kv[:i]
		if !secretKeyRe.MatchString(key) {
			return fmt.Errorf("invalid key %q in --from-literal: use letters, digits, '-', '_' or '.'", key)
		}
		data[key] = []byte(kv[i+1:])
	}
	return nil
}

// addFiles folds --from-file entries into data. An entry is a path, whose base
// name becomes the key; KEY=PATH to name the key; or a directory, where every
// regular file inside becomes a key of its own.
func addFiles(data map[string][]byte, files []string) error {
	for _, entry := range files {
		key, path := "", entry
		// Split on the first '=', but only when the left side looks like a key
		// rather than part of a Windows drive or an odd filename.
		if i := strings.IndexByte(entry, '='); i > 0 && secretKeyRe.MatchString(entry[:i]) {
			key, path = entry[:i], entry[i+1:]
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("--from-file %q: %w", path, err)
		}
		if !info.IsDir() {
			if key == "" {
				key = filepath.Base(path)
			}
			if err := addFile(data, key, path); err != nil {
				return err
			}
			continue
		}
		if key != "" {
			return fmt.Errorf("--from-file %q: a key cannot be given for a directory; each file in it becomes its own key", entry)
		}
		if err := addDir(data, path); err != nil {
			return err
		}
	}
	return nil
}

// addDir folds every regular file directly inside dir into data. Nested
// directories are skipped rather than flattened, so keys stay predictable.
func addDir(data map[string][]byte, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("--from-file %q: %w", dir, err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := addFile(data, e.Name(), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
		found++
	}
	if found == 0 {
		return fmt.Errorf("--from-file %q: directory has no files", dir)
	}
	return nil
}

// addFile reads one file into data under key, refusing to overwrite a key that
// another flag already set — silently keeping one of two values would be worse
// than saying which flag to fix.
func addFile(data map[string][]byte, key, path string) error {
	if !secretKeyRe.MatchString(key) {
		return fmt.Errorf("--from-file %q: %q is not a usable key; pass KEY=%s to name it", path, key, path)
	}
	if _, taken := data[key]; taken {
		return fmt.Errorf("--from-file %q: key %q was already set by another flag", path, key)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("--from-file %q: %w", path, err)
	}
	data[key] = content
	return nil
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
	var literals, files []string
	cmd := &cobra.Command{
		Use:   "create <name> [--from-literal KEY=VALUE] [--from-file PATH]",
		Short: "Create an opaque Secret, replacing it if it exists",
		Long: `Create an opaque Secret from --from-literal KEY=VALUE pairs and/or files.

  --from-file ./api.key          key is the file name ("api.key")
  --from-file apikey=./api.key   key is given explicitly
  --from-file ./conf.d           every file in the directory becomes a key

Files are read as raw bytes, so binary content and trailing newlines are kept
exactly — passing a file through --from-literal "$(cat f)" loses both.

An existing Secret of the same name is REPLACED: keys not passed here are
dropped. To change one key and keep the rest, use ` + "`orcinus secret set`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := secretData(literals, files)
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
	cmd.Flags().StringArrayVar(&files, "from-file", nil, "PATH, KEY=PATH, or a directory (repeatable); read as raw bytes")
	return cmd
}

func newSecretSetCmd() *cobra.Command {
	var kubeconfig, namespace string
	var literals, files []string
	cmd := &cobra.Command{
		Use:   "set <name> [--from-literal KEY=VALUE] [--from-file PATH]",
		Short: "Set keys on a Secret, keeping the ones not named",
		Long: `Set individual keys on a Secret without touching the rest.

Takes the same --from-literal and --from-file inputs as ` + "`orcinus secret create`" + `,
including reading a file as raw bytes (` + "`--from-file apikey=./api.key`" + `).

Unlike ` + "`orcinus secret create`" + `, keys that are not named here are kept.
The Secret is created if it does not exist yet.

Pods do not pick up a changed Secret on their own — env vars are injected when
the container starts. Restart the service afterwards (` + "`orcinus restart <service>`" + `).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := secretData(literals, files)
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
	cmd.Flags().StringArrayVar(&files, "from-file", nil, "PATH, KEY=PATH, or a directory (repeatable); read as raw bytes")
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
