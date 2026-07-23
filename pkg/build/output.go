package build

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// WriteOutputs writes the built image to every configured destination. The
// same v1.Image is used for all of them, so an OCI layout dir, a docker-load
// tar, and a registry push all describe the identical, Docker-compatible image.
func WriteOutputs(ctx context.Context, img v1.Image, opts Options) error {
	out := opts.Output
	wrote := false

	if out.OCIDir != "" {
		if err := writeOCILayout(img, out.OCIDir, opts.Tags); err != nil {
			return fmt.Errorf("write OCI layout %s: %w", out.OCIDir, err)
		}
		fmt.Fprintf(opts.stdout(), "✓ OCI layout → %s\n", out.OCIDir)
		wrote = true
	}

	if out.TarPath != "" {
		if err := writeTar(img, out.TarPath, out.TarFormat, opts.Tags); err != nil {
			return fmt.Errorf("write tar %s: %w", out.TarPath, err)
		}
		fmt.Fprintf(opts.stdout(), "✓ image archive → %s (docker load -i %s)\n", out.TarPath, out.TarPath)
		wrote = true
	}

	for _, ref := range out.Push {
		if err := pushImage(ctx, img, ref); err != nil {
			return fmt.Errorf("push %s: %w", ref, err)
		}
		fmt.Fprintf(opts.stdout(), "✓ pushed → %s\n", ref)
		wrote = true
	}

	if !wrote {
		return fmt.Errorf("no output configured (use --output, --tar, or push)")
	}
	return nil
}

// writeOCILayout writes an OCI image layout (index.json + blobs/) with the
// image tagged by every ref's tag under org.opencontainers.image.ref.name.
func writeOCILayout(img v1.Image, dir string, tags []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		return err
	}
	// Annotate each tag so tools can find the image by name in the layout.
	for _, t := range tags {
		ref, err := name.NewTag(t, name.WeakValidation)
		if err != nil {
			// Not a tagged ref (e.g. digest-only); append without annotation.
			if err := p.AppendImage(img); err != nil {
				return err
			}
			continue
		}
		if err := p.AppendImage(img, layout.WithAnnotations(map[string]string{
			"org.opencontainers.image.ref.name": ref.TagStr(),
			"io.containerd.image.name":          ref.Name(),
		})); err != nil {
			return err
		}
	}
	if len(tags) == 0 {
		return p.AppendImage(img)
	}
	return nil
}

// writeTar writes a single-file image archive. "docker" (default) produces a
// `docker load`-compatible tarball; "oci" produces an OCI-layout tar.
func writeTar(img v1.Image, tarPath, format string, tags []string) error {
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o755); err != nil {
		return err
	}
	switch format {
	case "", "docker":
		ref, err := name.NewTag(firstTag(tags), name.WeakValidation)
		if err != nil {
			return err
		}
		return tarball.WriteToFile(tarPath, ref, img)
	case "oci":
		// OCI-layout tar: write a layout to a temp dir, then tar it.
		tmp, err := os.MkdirTemp("", "orcinus-oci-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := writeOCILayout(img, tmp, tags); err != nil {
			return err
		}
		return tarDir(tmp, tarPath)
	default:
		return fmt.Errorf("unknown tar format %q (want \"docker\" or \"oci\")", format)
	}
}

// pushImage pushes to a registry using the local docker credential chain, so
// `orcinus build --push` authenticates exactly like `docker push`.
func pushImage(ctx context.Context, img v1.Image, ref string) error {
	r, err := name.ParseReference(ref)
	if err != nil {
		return err
	}
	return remote.Write(r, img,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

func firstTag(tags []string) string {
	if len(tags) > 0 && tags[0] != "" {
		return tags[0]
	}
	return "orcinus-build:latest"
}

// tarDir writes the contents of dir into a tar archive at tarPath (used for the
// OCI-layout tar format).
func tarDir(dir, tarPath string) error {
	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	return filepath.WalkDir(dir, func(pth string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, pth)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		src, err := os.Open(pth)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}
