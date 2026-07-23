package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/build"
)

// newImageCmd is the `orcinus image` command group: build, inspect, push, ls.
// It works on the OCI/Docker-compatible artifacts produced by `orcinus build`
// (an OCI layout directory or an image tar) — all without a Docker daemon.
func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build and manage OCI/Docker-compatible images (no Docker daemon)",
		Long: "Build container images and work with the artifacts they produce — an OCI\n" +
			"image layout directory or an image tar — without a Docker runtime.\n\n" +
			"  orcinus image build ./app -t myapp:v1 -o ./out\n" +
			"  orcinus image inspect ./out\n" +
			"  orcinus image push ./out registry.example.com/myapp:v1\n" +
			"  orcinus image ls ./out",
	}
	cmd.AddCommand(
		newBuildCmd(),
		newImageInspectCmd(),
		newImagePushCmd(),
		newImageLsCmd(),
	)
	return cmd
}

func newImageInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <oci-dir|tar>",
		Short: "Show config, manifest digest and layers of a built image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			infos, err := build.Inspect(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for i, in := range infos {
				if i > 0 {
					fmt.Fprintln(out)
				}
				ref := in.Ref
				if ref == "" {
					ref = "<untagged>"
				}
				fmt.Fprintf(out, "Ref:          %s\n", ref)
				fmt.Fprintf(out, "Digest:       %s\n", in.Digest)
				fmt.Fprintf(out, "Platform:     %s/%s\n", in.OS, in.Architecture)
				fmt.Fprintf(out, "Size:         %s\n", humanSize(in.Size))
				fmt.Fprintf(out, "Layers:       %d\n", in.Layers)
				if in.WorkingDir != "" {
					fmt.Fprintf(out, "WorkingDir:   %s\n", in.WorkingDir)
				}
				if in.User != "" {
					fmt.Fprintf(out, "User:         %s\n", in.User)
				}
				if len(in.Entrypoint) > 0 {
					fmt.Fprintf(out, "Entrypoint:   %s\n", strings.Join(in.Entrypoint, " "))
				}
				if len(in.Cmd) > 0 {
					fmt.Fprintf(out, "Cmd:          %s\n", strings.Join(in.Cmd, " "))
				}
				if len(in.ExposedPorts) > 0 {
					sort.Strings(in.ExposedPorts)
					fmt.Fprintf(out, "ExposedPorts: %s\n", strings.Join(in.ExposedPorts, ", "))
				}
				if len(in.Env) > 0 {
					fmt.Fprintf(out, "Env:\n")
					for _, e := range in.Env {
						fmt.Fprintf(out, "  %s\n", e)
					}
				}
				if len(in.Labels) > 0 {
					keys := make([]string, 0, len(in.Labels))
					for k := range in.Labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					fmt.Fprintf(out, "Labels:\n")
					for _, k := range keys {
						fmt.Fprintf(out, "  %s=%s\n", k, in.Labels[k])
					}
				}
			}
			return nil
		},
	}
}

func newImagePushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <oci-dir|tar> <reference>",
		Short: "Push a built image artifact to a registry (Docker credentials)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, ref := args[0], args[1]
			if err := build.PushArtifact(cmd.Context(), path, ref); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ pushed %s → %s\n", path, ref)
			return nil
		},
	}
}

func newImageLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <oci-dir|tar>",
		Short: "List the images in a built artifact (ref, digest, size)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			infos, err := build.Inspect(args[0])
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "REFERENCE\tPLATFORM\tDIGEST\tSIZE")
			for _, in := range infos {
				ref := in.Ref
				if ref == "" {
					ref = "<untagged>"
				}
				dig := in.Digest
				if _, hex, ok := strings.Cut(dig, ":"); ok && len(hex) > 12 {
					dig = "sha256:" + hex[:12]
				}
				fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\n", ref, in.OS, in.Architecture, dig, humanSize(in.Size))
			}
			return w.Flush()
		},
	}
}

// humanSize renders a byte count as a compact human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
