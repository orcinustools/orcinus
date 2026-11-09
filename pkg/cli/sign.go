package cli

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orcinustools/orcinus/pkg/cluster"
	"github.com/orcinustools/orcinus/pkg/sign"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newSignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign images and allow only signed images to run (docs/SIGNING.md)",
		Long: "Sign container images with your key, and make the cluster refuse images\n" +
			"that are not signed by a key it trusts.\n\n" +
			"  1. orcinus sign keygen                      create a key pair\n" +
			"  2. orcinus sign image <image>               sign an image in its registry\n" +
			"  3. orcinus sign trust add <name> [key.pub]  trust a public key in the cluster\n" +
			"  4. orcinus sign enforce --mode audit        report unsigned images\n" +
			"     orcinus sign status                      see what is unsigned\n" +
			"  5. orcinus sign enforce --mode enforce      refuse unsigned images\n\n" +
			"Signatures use the cosign key-based format, stored next to the image in its\n" +
			"registry; the cluster checks them with Kyverno. They are not uploaded to a\n" +
			"public transparency log.",
	}
	cmd.AddCommand(newSignKeygenCmd(), newSignImageCmd(), newSignVerifyCmd(),
		newSignTrustCmd(), newSignEnforceCmd(), newSignExemptCmd(), newSignStatusCmd())
	return cmd
}

func keyDir() string { return filepath.Join(cluster.Dir(), "keys") }

// keyPath turns a key name into its file in ~/.orcinus/keys; a path is kept.
func keyPath(key, ext string) string {
	if strings.ContainsAny(key, `/\`) || strings.HasSuffix(key, ext) {
		return key
	}
	return filepath.Join(keyDir(), key+ext)
}

// password reads ORCINUS_SIGNING_PASSWORD, or asks on the terminal.
func password(prompt string, confirm bool) ([]byte, error) {
	if p, ok := os.LookupEnv("ORCINUS_SIGNING_PASSWORD"); ok {
		return []byte(p), nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, nil // no terminal: an unencrypted-password key (CI sets the env var)
	}
	fmt.Fprint(os.Stderr, prompt)
	p, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil || !confirm {
		return p, err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	again, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if string(p) != string(again) {
		return nil, errors.New("passwords do not match")
	}
	return p, nil
}

// loadSigningKey reads the private key: ORCINUS_SIGNING_KEY (PEM, for CI) when
// no --key is given, else the named key or file.
func loadSigningKey(key string) (*ecdsa.PrivateKey, error) {
	var data []byte
	if env := os.Getenv("ORCINUS_SIGNING_KEY"); key == "" && env != "" {
		data = []byte(env)
	} else {
		if key == "" {
			key = "default"
		}
		var err error
		if data, err = os.ReadFile(keyPath(key, ".key")); err != nil {
			return nil, fmt.Errorf("read signing key: %w (create one with `orcinus sign keygen`)", err)
		}
	}
	pw, err := password("Password for the signing key: ", false)
	if err != nil {
		return nil, err
	}
	return sign.ParsePrivateKey(data, pw)
}

func newSignKeygenCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen [name]",
		Short: "Create a signing key pair in ~/.orcinus/keys (name: default)",
		Long: "Create an ECDSA P-256 key pair: <name>.key (private, password-encrypted,\n" +
			"mode 0600) and <name>.pub (public — give it to the cluster with\n" +
			"`orcinus sign trust add`). The password is asked for, or taken from\n" +
			"ORCINUS_SIGNING_PASSWORD. Keep the private key out of git; in CI pass it in\n" +
			"ORCINUS_SIGNING_KEY instead.",
		Example: "  orcinus sign keygen\n  orcinus sign keygen ci",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "default"
			if len(args) == 1 {
				name = args[0]
			}
			priv, pub := keyPath(name, ".key"), keyPath(name, ".pub")
			if _, err := os.Stat(priv); err == nil && !force {
				return fmt.Errorf("%s exists; pass --force to replace it (images it signed stay signed by the old key)", priv)
			}
			pw, err := password("Password for the new key (empty for none): ", true)
			if err != nil {
				return err
			}
			privPEM, pubPEM, err := sign.GenerateKey(pw)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(priv, privPEM, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(pub, pubPEM, 0o644); err != nil {
				return err
			}
			k, _ := sign.ParsePublicKey(pubPEM)
			fmt.Fprintf(cmd.OutOrStdout(), "private key: %s\npublic key:  %s (%s)\n", priv, pub, sign.Fingerprint(k))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing key")
	return cmd
}

func newSignImageCmd() *cobra.Command {
	var key string
	var o sign.Options
	cmd := &cobra.Command{
		Use:   "image <image>...",
		Short: "Sign images in their registry",
		Long: "Resolve each image to its digest, sign that digest and push the signature\n" +
			"next to the image (tag sha256-<digest>.sig). The signature covers that exact\n" +
			"content: retagging the name to another image does not carry it over.\n" +
			"Registry credentials come from `docker login`.",
		Example: "  orcinus sign image registry.example.com/app:1.4\n" +
			"  ORCINUS_SIGNING_KEY=\"$(cat ci.key)\" ORCINUS_SIGNING_PASSWORD=… orcinus sign image $IMAGE",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := loadSigningKey(key)
			if err != nil {
				return err
			}
			for _, ref := range args {
				d, err := sign.SignImage(cmd.Context(), ref, k, o)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "signed %s\n", d)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "signing key name or file (default: $ORCINUS_SIGNING_KEY, else \"default\")")
	cmd.Flags().BoolVar(&o.Insecure, "insecure-registry", false, "the registry speaks plain HTTP")
	return cmd
}

func newSignVerifyCmd() *cobra.Command {
	var keys []string
	var kubeconfig string
	var o sign.Options
	cmd := &cobra.Command{
		Use:   "verify <image>...",
		Short: "Check that images are signed by a trusted key",
		Long: "Check each image's signature against the keys given with --key (names or\n" +
			".pub files), or, without --key, the keys the cluster trusts.",
		Example: "  orcinus sign verify registry.example.com/app:1.4\n  orcinus sign verify --key default registry.example.com/app:1.4",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trusted, err := verifyKeys(cmd.Context(), keys, kubeconfig)
			if err != nil {
				return err
			}
			bad := 0
			for _, ref := range args {
				d, signer, err := sign.Verify(cmd.Context(), ref, trusted, o)
				if err != nil {
					bad++
					fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", ref, err)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "✓ %s signed by %s\n", d, signer)
			}
			if bad > 0 {
				return fmt.Errorf("%d image(s) not signed by a trusted key", bad)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&keys, "key", nil, "public key name or .pub file (repeatable; default: the cluster's trusted keys)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.Flags().BoolVar(&o.Insecure, "insecure-registry", false, "the registry speaks plain HTTP")
	return cmd
}

func verifyKeys(ctx context.Context, names []string, kubeconfig string) (map[string]*ecdsa.PublicKey, error) {
	if len(names) == 0 {
		cfg, err := sign.LoadConfig(ctx, kubeconfig)
		if err != nil {
			return nil, err
		}
		if cfg == nil || len(cfg.Keys) == 0 {
			return nil, errors.New("no --key given and the cluster trusts no keys yet (orcinus sign trust add)")
		}
		return cfg.PublicKeys()
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, n := range names {
		data, err := os.ReadFile(keyPath(n, ".pub"))
		if err != nil {
			return nil, err
		}
		k, err := sign.ParsePublicKey(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out[strings.TrimSuffix(filepath.Base(n), ".pub")] = k
	}
	return out, nil
}

func newSignTrustCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the public keys the cluster accepts signatures from",
	}
	add := &cobra.Command{
		Use:     "add <name> [key.pub]",
		Short:   "Trust a public key (default file: ~/.orcinus/keys/<name>.pub)",
		Example: "  orcinus sign trust add default\n  orcinus sign trust add ci ./ci.pub",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := keyPath(args[0], ".pub")
			if len(args) == 2 {
				file = args[1]
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			k, err := sign.ParsePublicKey(data)
			if err != nil {
				return err
			}
			return updateConfig(cmd, kubeconfig, func(c *sign.Config) error {
				c.Keys[args[0]] = string(data)
				fmt.Fprintf(cmd.OutOrStdout(), "trusted %s (%s)\n", args[0], sign.Fingerprint(k))
				return nil
			})
		},
	}
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Stop trusting a key (pods it signed keep running; new ones are refused)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateConfig(cmd, kubeconfig, func(c *sign.Config) error {
				if _, ok := c.Keys[args[0]]; !ok {
					return fmt.Errorf("no trusted key %q", args[0])
				}
				if len(c.Keys) == 1 && c.Mode != "off" {
					// No keys would mean no policy: unsigned images would run again.
					return fmt.Errorf("%q is the last trusted key and signing is %s; trust another key first, or turn signing off (orcinus sign enforce --mode off)", args[0], c.Mode)
				}
				delete(c.Keys, args[0])
				return nil
			})
		},
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the trusted keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := sign.LoadConfig(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			if c == nil || len(c.Keys) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no trusted keys")
				return nil
			}
			keys, err := c.PublicKeys()
			if err != nil {
				return err
			}
			for _, n := range sortedKeys(keys) {
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %s\n", n, sign.Fingerprint(keys[n]))
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	cmd.AddCommand(add, rm, ls)
	return cmd
}

func newSignEnforceCmd() *cobra.Command {
	var kubeconfig, mode string
	var pullSecrets []string
	var insecure bool
	cmd := &cobra.Command{
		Use:   "enforce",
		Short: "Turn signature checking on (audit|enforce) or off",
		Long: "Install the admission policy that checks every new pod's images against the\n" +
			"trusted keys (Kyverno is installed the first time).\n\n" +
			"  audit    pods run; unsigned images are reported (orcinus sign status)\n" +
			"  enforce  pods with an image not signed by a trusted key are refused\n" +
			"  off      remove the policy (Kyverno and the settings stay)\n\n" +
			"Every image is pinned to the digest that was verified, so a tag moved\n" +
			"later cannot swap the content. System and plugin namespaces are exempt\n" +
			"(see `orcinus sign exempt`). Start with audit, sign what `status` lists,\n" +
			"then enforce.",
		Example: "  orcinus sign enforce --mode audit\n  orcinus sign enforce --mode enforce\n" +
			"  orcinus sign enforce --mode enforce --pull-secret regcred   # signatures in a private registry",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "audit" && mode != "enforce" && mode != "off" {
				return fmt.Errorf("--mode must be audit, enforce or off")
			}
			return updateConfig(cmd, kubeconfig, func(c *sign.Config) error {
				if mode != "off" && len(c.Keys) == 0 {
					return errors.New("trust a key first: orcinus sign trust add <name>")
				}
				c.Mode = mode
				if cmd.Flags().Changed("pull-secret") {
					c.PullSecrets = pullSecrets
				}
				if cmd.Flags().Changed("insecure-registry") {
					c.InsecureRegistry = insecure
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "audit", "audit, enforce or off")
	cmd.Flags().BoolVar(&insecure, "insecure-registry", false, "allow reading signatures from plain-HTTP registries (local labs)")
	cmd.Flags().StringSliceVar(&pullSecrets, "pull-secret", nil, "docker-registry secret(s) in the kyverno namespace for reading signatures from private registries")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func newSignExemptCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "exempt",
		Short: "Manage namespaces and images that run without a signature",
		Long: "Namespaces kube-system, kube-public, kube-node-lease and kyverno, and every\n" +
			"namespace a plugin installed into, are always exempt. Add more, or image\n" +
			"prefixes (a trailing * is optional): an argument with a '/' or ':' is an\n" +
			"image, anything else a namespace.",
		Example: "  orcinus sign exempt add sandbox\n  orcinus sign exempt add docker.io/library/busybox\n  orcinus sign exempt rm sandbox",
	}
	edit := func(add bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			return updateConfig(cmd, kubeconfig, func(c *sign.Config) error {
				for _, a := range args {
					list := &c.ExemptNamespaces
					if strings.ContainsAny(a, "/:") {
						list, a = &c.ExemptImages, strings.TrimSuffix(a, "*")
					}
					*list = setEdit(*list, a, add)
				}
				return nil
			})
		}
	}
	cmd.AddCommand(
		&cobra.Command{Use: "add <namespace|image>...", Short: "Exempt namespaces or image prefixes", Args: cobra.MinimumNArgs(1), RunE: edit(true)},
		&cobra.Command{Use: "rm <namespace|image>...", Short: "Remove exemptions", Args: cobra.MinimumNArgs(1), RunE: edit(false)},
	)
	cmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func setEdit(list []string, v string, add bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	if add {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func newSignStatusCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the signing mode, trusted keys, exemptions and unsigned running images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := sign.LoadConfig(cmd.Context(), kubeconfig)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if c == nil {
				fmt.Fprintln(out, "image signing: not set up (orcinus sign --help)")
				return nil
			}
			keys, err := c.PublicKeys()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "mode:      %s\n", c.Mode)
			if c.Mode != "off" {
				if ok, why := sign.Active(cmd.Context(), kubeconfig); ok {
					fmt.Fprintln(out, "admission: active")
				} else {
					fmt.Fprintf(out, "admission: INACTIVE: %s\n", why)
				}
			}
			fmt.Fprintf(out, "keys:      %s\n", orNone(strings.Join(sortedKeys(keys), ", ")))
			fmt.Fprintf(out, "exempt:    namespaces: %s\n", strings.Join(append(sign.SystemNamespaces, c.ExemptNamespaces...), ", ")+" + plugin namespaces")
			fmt.Fprintf(out, "           images: %s\n", orNone(strings.Join(c.ExemptImages, ", ")))
			unsigned, err := sign.Unsigned(cmd.Context(), kubeconfig, c)
			if err != nil {
				return err
			}
			if len(unsigned) == 0 {
				fmt.Fprintln(out, "running:   every checked image is signed")
				return nil
			}
			fmt.Fprintf(out, "unsigned:  %d image(s) in running pods\n", len(unsigned))
			for _, u := range unsigned {
				fmt.Fprintf(out, "  %-40s %s (%s)\n", u.Image, u.Where, u.Reason)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig")
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// updateConfig loads the cluster's signing settings, edits them and applies
// them (settings + admission policy).
func updateConfig(cmd *cobra.Command, kubeconfig string, edit func(*sign.Config) error) error {
	c, err := sign.LoadConfig(cmd.Context(), kubeconfig)
	if err != nil {
		return err
	}
	if c == nil {
		c = &sign.Config{Mode: "off"}
	}
	if c.Keys == nil {
		c.Keys = map[string]string{}
	}
	if err := edit(c); err != nil {
		return err
	}
	if err := sign.Apply(cmd.Context(), kubeconfig, c); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "image signing: %s, %d trusted key(s)\n", c.Mode, len(c.Keys))
	return nil
}
