//go:build orcinus_buildah && linux

// This file is compiled only with `-tags orcinus_buildah` on Linux. It imports
// buildah as a library (go.podman.io/buildah) — no Docker daemon — and runs the
// full Dockerfile, including RUN and multi-stage builds, using an OCI runtime
// (crun/runc) under the hood.
//
// Enabling it (once) on a Linux build host:
//
//	go get go.podman.io/buildah@latest
//	go mod tidy -tags orcinus_buildah
//	go build -tags orcinus_buildah ./cmd/orcinus
//
// Runtime requirements: an OCI runtime (crun or runc) on PATH, and for rootless
// use, subuid/subgid entries plus newuidmap/newgidmap. See docs/BUILD.md.
package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.podman.io/buildah/define"
	"go.podman.io/buildah/imagebuildah"
	cp "go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/oci/layout"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/storage"
	"go.podman.io/image/v5/types"
	cstorage "go.podman.io/storage"
	"go.podman.io/storage/pkg/unshare"

	"go.podman.io/storage/pkg/reexec"

	ggcrlayout "github.com/google/go-containerregistry/pkg/v1/layout"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// InitReexec initializes the containers/storage reexec machinery. It MUST be
// called at the very start of main() before any other work: buildah's rootless
// user-namespace setup re-executes this binary, and storage panics if reexec
// was never initialized. It returns true when the current process is such a
// re-execution, in which case main should return immediately (the registered
// handler has already run).
func InitReexec() bool { return reexec.Init() }

// buildWithBuildah runs the Dockerfile through buildah into containers-storage,
// exports the result to a temporary OCI layout, and reads it back as a
// go-containerregistry v1.Image so the shared output layer can write the OCI
// dir / docker tar / push exactly like the native backend.
func buildWithBuildah(ctx context.Context, opts Options) (v1.Image, error) {
	// Join a user namespace when running rootless (no-op as root).
	unshare.MaybeReexecUsingUserNamespace(false)

	storeOpts, err := cstorage.DefaultStoreOptions()
	if err != nil {
		return nil, fmt.Errorf("buildah: store options: %w", err)
	}
	store, err := cstorage.GetStore(storeOpts)
	if err != nil {
		return nil, fmt.Errorf("buildah: get store: %w", err)
	}
	defer func() { _, _ = store.Shutdown(false) }()

	// Materialize the Dockerfile on disk (buildah reads it by path).
	dfPath, cleanup, err := materializeDockerfile(opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	args := map[string]string{}
	for k, v := range opts.BuildArgs {
		args[k] = v
	}
	labels := make([]string, 0, len(opts.Labels))
	for k, v := range opts.Labels {
		labels = append(labels, fmt.Sprintf("%s=%s", k, v))
	}
	var platforms []struct{ OS, Arch, Variant string }
	if opts.Platform != "" {
		osName, arch, variant := splitPlatform(opts.Platform)
		platforms = append(platforms, struct{ OS, Arch, Variant string }{osName, arch, variant})
	}

	// Provide a self-contained containers config so the build works without
	// /etc/containers/* set up: default unqualified images to docker.io and
	// accept images without signature verification (docker's default behavior).
	sysCtx, cleanupCfg, err := defaultSystemContext()
	if err != nil {
		return nil, err
	}
	defer cleanupCfg()

	buildOpts := define.BuildOptions{
		ContextDirectory: opts.ContextDir,
		Args:             args,
		Labels:           labels,
		Target:           opts.Target,
		Output:           opts.primaryRef(),
		AdditionalTags:   restTags(opts.Tags),
		OutputFormat:     define.OCIv1ImageManifest,
		Out:              opts.stdout(),
		Err:              opts.stderr(),
		ReportWriter:     opts.stdout(),
		PullPolicy:       pullPolicy(opts.Pull),
		Isolation:        isolationFor(opts.Isolation),
		Runtime:          opts.Runtime,
		SystemContext:    sysCtx,
	}
	for _, p := range platforms {
		buildOpts.Platforms = append(buildOpts.Platforms, struct {
			OS, Arch, Variant string
		}{p.OS, p.Arch, p.Variant})
	}

	imageID, _, err := imagebuildah.BuildDockerfiles(ctx, store, buildOpts, dfPath)
	if err != nil {
		return nil, fmt.Errorf("buildah build: %w", err)
	}

	// Export containers-storage:<imageID> to a temp OCI layout, then hand it to
	// the shared ggcr output path.
	ociDir, err := os.MkdirTemp("", "orcinus-buildah-oci-*")
	if err != nil {
		return nil, err
	}
	// NOTE: the caller writes final outputs; this temp dir is only the bridge.
	if err := exportStorageToOCI(ctx, store, imageID, ociDir); err != nil {
		os.RemoveAll(ociDir)
		return nil, err
	}
	img, err := readOCIImage(ociDir)
	if err != nil {
		os.RemoveAll(ociDir)
		return nil, err
	}
	return img, nil
}

// materializeDockerfile returns a filesystem path to the Dockerfile buildah
// should read, writing inline content to a temp file when needed.
func materializeDockerfile(opts Options) (path string, cleanup func(), err error) {
	cleanup = func() {}
	if opts.DockerfileInline != "" {
		f, err := os.CreateTemp(opts.ContextDir, ".orcinus-dockerfile-*")
		if err != nil {
			return "", cleanup, err
		}
		if _, err := f.WriteString(opts.DockerfileInline); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", cleanup, err
		}
		f.Close()
		return f.Name(), func() { os.Remove(f.Name()) }, nil
	}
	df := opts.Dockerfile
	if df == "" {
		df = "Dockerfile"
	}
	if !filepath.IsAbs(df) {
		df = filepath.Join(opts.ContextDir, df)
	}
	return df, cleanup, nil
}

// exportStorageToOCI copies containers-storage:<imageID> into an OCI image
// layout directory using containers/image, with signature policy set to accept
// (local, already-built image).
func exportStorageToOCI(ctx context.Context, store cstorage.Store, imageID, ociDir string) error {
	srcRef, err := storage.Transport.ParseStoreReference(store, imageID)
	if err != nil {
		return fmt.Errorf("parse storage ref: %w", err)
	}
	dstRef, err := layout.ParseReference(ociDir + ":latest")
	if err != nil {
		return fmt.Errorf("parse oci ref: %w", err)
	}
	policy, err := signature.NewPolicyFromBytes([]byte(`{"default":[{"type":"insecureAcceptAnything"}]}`))
	if err != nil {
		return err
	}
	pc, err := signature.NewPolicyContext(policy)
	if err != nil {
		return err
	}
	defer pc.Destroy()
	_, err = cp.Image(ctx, pc, dstRef, srcRef, &cp.Options{
		ReportWriter: os.Stderr,
		SourceCtx:    &types.SystemContext{},
	})
	return err
}

func readOCIImage(dir string) (v1.Image, error) {
	p, err := ggcrlayout.FromPath(dir)
	if err != nil {
		return nil, err
	}
	idx, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) == 0 {
		return nil, fmt.Errorf("buildah export produced an empty OCI index")
	}
	return idx.Image(m.Manifests[0].Digest)
}

func restTags(tags []string) []string {
	if len(tags) <= 1 {
		return nil
	}
	return tags[1:]
}

func pullPolicy(pull bool) define.PullPolicy {
	if pull {
		return define.PullAlways
	}
	return define.PullIfMissing
}

// isolationFor maps the CLI isolation string to buildah's Isolation. Chroot
// needs no OCI runtime or network backend (uses the host's), so it is the most
// dependency-light option; oci/rootless use crun/runc + netavark/pasta.
func isolationFor(s string) define.Isolation {
	switch s {
	case "oci":
		return define.IsolationOCI
	case "chroot":
		return define.IsolationChroot
	case "rootless":
		return define.IsolationOCIRootless
	default:
		return define.IsolationDefault
	}
}

// defaultSystemContext writes a minimal, docker-like containers configuration
// to a temp dir and returns a SystemContext pointing at it: unqualified images
// resolve against docker.io (permissive short names) and images are accepted
// without signature verification. Returns a cleanup func for the temp dir.
func defaultSystemContext() (*types.SystemContext, func(), error) {
	dir, err := os.MkdirTemp("", "orcinus-containers-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	policyPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"default":[{"type":"insecureAcceptAnything"}]}`), 0o644); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	regPath := filepath.Join(dir, "registries.conf")
	regConf := "unqualified-search-registries = [\"docker.io\"]\nshort-name-mode = \"permissive\"\n"
	if err := os.WriteFile(regPath, []byte(regConf), 0o644); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return &types.SystemContext{
		SignaturePolicyPath:      policyPath,
		SystemRegistriesConfPath: regPath,
	}, cleanup, nil
}
