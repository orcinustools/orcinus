// Package build assembles OCI/Docker-compatible container images without a
// Docker daemon. It reads the same inputs as `docker build` and
// `docker compose build` — a build context, a Dockerfile (or the compose
// `build:` block) — and produces an image that any Docker/OCI registry or
// runtime accepts.
//
// Two backends cooperate behind one API:
//
//   - native (ggcr): pure-Go, CGO-free, cross-platform. It parses the
//     Dockerfile and applies every non-RUN instruction (FROM, COPY, ADD, ENV,
//     WORKDIR, CMD, ENTRYPOINT, EXPOSE, LABEL, USER, VOLUME, ARG, …) by
//     mutating image layers in-process. No runtime of any kind is required, so
//     it can build a linux/amd64 image from macOS. It cannot execute RUN.
//
//   - buildah (linux only): imported as a library and used when the Dockerfile
//     needs RUN (or multi-stage / COPY --from). It runs the full build the same
//     way `docker build` does, daemonless, using an OCI runtime (crun/runc)
//     under the hood. Compiled only under the opt-in `orcinus_buildah` build
//     tag on Linux (see docs/BUILD.md); every other build gets a stub that
//     returns a clear, actionable error.
//
// Both backends converge on a single go-containerregistry v1.Image, which the
// output layer writes to an OCI layout directory, a docker/OCI tar archive, or
// (later) a registry — so the destination code is shared and Docker-compatible.
package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	composecli "github.com/compose-spec/compose-go/v2/cli"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Output describes where a built image is written. At least one destination
// should be set; the caller may set several to fan out to all of them.
type Output struct {
	OCIDir    string   // write an OCI image layout to this directory
	TarPath   string   // write a single image tar archive to this path
	TarFormat string   // "docker" (default, `docker load`-able) or "oci"
	Push      []string // registry references to push to (reserved; see WriteOutputs)
}

// Options configures a single image build. It mirrors the knobs of
// `docker build` so the CLI can map flags 1:1.
type Options struct {
	ContextDir       string            // build context (defaults to ".")
	Dockerfile       string            // Dockerfile path; relative paths resolve against ContextDir
	DockerfileInline string            // inline Dockerfile contents (wins over Dockerfile)
	Tags             []string          // image references, e.g. "myapp:latest"
	BuildArgs        map[string]string // --build-arg values
	Labels           map[string]string // --label values (merged onto the image config)
	Target           string            // target stage for multi-stage builds
	Platform         string            // e.g. "linux/amd64" (defaults to linux/<host arch>)
	Pull             bool              // always re-pull the base image
	ForceBuildah     bool              // force the buildah backend even without RUN
	Isolation        string            // buildah RUN isolation: ""(auto)|oci|chroot|rootless
	Runtime          string            // OCI runtime for RUN (e.g. crun, runc); "" = buildah default
	Output           Output            // where to write the result

	Stdout io.Writer // build progress (defaults to os.Stdout)
	Stderr io.Writer // build errors/logs (defaults to os.Stderr)
}

func (o *Options) stdout() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return os.Stdout
}

func (o *Options) stderr() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

// primaryRef returns the reference used to name the image inside a tar archive
// and as the default push target. Falls back to a deterministic placeholder so
// output never fails purely for lack of a tag.
func (o *Options) primaryRef() string {
	if len(o.Tags) > 0 && o.Tags[0] != "" {
		return o.Tags[0]
	}
	return "orcinus-build:latest"
}

// dockerfileContents returns the Dockerfile bytes (inline wins) and the
// directory used to resolve relative COPY/ADD sources (the build context).
func (o *Options) dockerfileContents() ([]byte, error) {
	if strings.TrimSpace(o.DockerfileInline) != "" {
		return []byte(o.DockerfileInline), nil
	}
	ctxDir := o.ContextDir
	if ctxDir == "" {
		ctxDir = "."
	}
	df := o.Dockerfile
	if df == "" {
		df = "Dockerfile"
	}
	if !filepath.IsAbs(df) {
		df = filepath.Join(ctxDir, df)
	}
	data, err := os.ReadFile(df)
	if err != nil {
		return nil, fmt.Errorf("read Dockerfile %s: %w", df, err)
	}
	return data, nil
}

// Build assembles one image per Options and writes it to the configured
// outputs. It selects the native backend when the Dockerfile is expressible
// without executing commands, and the buildah backend otherwise (or when
// ForceBuildah is set).
func Build(ctx context.Context, opts Options) error {
	if opts.ContextDir == "" {
		opts.ContextDir = "."
	}
	dfData, err := opts.dockerfileContents()
	if err != nil {
		return err
	}

	parsed, err := parseDockerfile(dfData, opts.BuildArgs)
	if err != nil {
		return fmt.Errorf("parse Dockerfile: %w", err)
	}

	var img v1.Image
	if opts.ForceBuildah || parsed.needsExecutor() {
		reason := parsed.executorReason()
		if opts.ForceBuildah {
			reason = "forced (--engine buildah)"
		}
		fmt.Fprintf(opts.stdout(), "building with buildah (%s)…\n", reason)
		img, err = buildWithBuildah(ctx, opts)
	} else {
		fmt.Fprintf(opts.stdout(), "building with native engine (no runtime)…\n")
		img, err = buildNative(ctx, opts, parsed)
	}
	if err != nil {
		return err
	}
	return WriteOutputs(ctx, img, opts)
}

// Target is a single image to build, discovered from a compose file's
// `build:` block. It is a compose-flavored view over Options.
type Target struct {
	Service    string // compose service name
	Context    string // absolute build context directory
	Dockerfile string // Dockerfile path (may be absolute)
	Inline     string // dockerfile_inline contents, if any
	Args       map[string]string
	Labels     map[string]string
	Target     string
	Platforms  []string
	Tags       []string // image + build.tags
}

// ToOptions projects a compose Target onto build Options, layering the shared
// output and platform settings on top.
func (t Target) ToOptions(base Options) Options {
	o := base
	o.ContextDir = t.Context
	o.Dockerfile = t.Dockerfile
	o.DockerfileInline = t.Inline
	o.Target = t.Target
	o.Tags = t.Tags
	if o.BuildArgs == nil {
		o.BuildArgs = map[string]string{}
	}
	for k, v := range t.Args {
		if _, ok := o.BuildArgs[k]; !ok {
			o.BuildArgs[k] = v
		}
	}
	if len(t.Labels) > 0 {
		if o.Labels == nil {
			o.Labels = map[string]string{}
		}
		for k, v := range t.Labels {
			o.Labels[k] = v
		}
	}
	if o.Platform == "" && len(t.Platforms) > 0 {
		o.Platform = t.Platforms[0]
	}
	return o
}

// DiscoverTargets loads compose files and returns one Target per service that
// declares a `build:` block, mirroring `docker compose build`. If services is
// non-empty, only those services are returned. baseDir is the working
// directory used to resolve relative contexts (defaults to the cwd).
func DiscoverTargets(ctx context.Context, files []string, baseDir string, profiles, services []string) ([]Target, error) {
	if baseDir == "" {
		baseDir = "."
	}
	popts, err := composecli.NewProjectOptions(
		files,
		composecli.WithWorkingDirectory(baseDir),
		composecli.WithProfiles(profiles),
		composecli.WithOsEnv,
		composecli.WithDotEnv,
	)
	if err != nil {
		return nil, err
	}
	project, err := composecli.ProjectFromOptions(ctx, popts)
	if err != nil {
		return nil, fmt.Errorf("load compose files: %w", err)
	}

	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}

	var targets []Target
	for name, svc := range project.Services {
		if svc.Build == nil {
			continue
		}
		if len(want) > 0 && !want[name] {
			continue
		}
		targets = append(targets, targetFromService(name, svc))
	}
	if len(want) > 0 {
		for _, s := range services {
			if !hasTarget(targets, s) {
				return nil, fmt.Errorf("service %q has no build: section (or does not exist)", s)
			}
		}
	}
	return targets, nil
}

func hasTarget(ts []Target, service string) bool {
	for _, t := range ts {
		if t.Service == service {
			return true
		}
	}
	return false
}

func targetFromService(name string, svc composetypes.ServiceConfig) Target {
	b := svc.Build
	t := Target{
		Service:    name,
		Context:    b.Context,
		Dockerfile: b.Dockerfile,
		Inline:     b.DockerfileInline,
		Target:     b.Target,
		Platforms:  []string(b.Platforms),
		Args:       map[string]string{},
		Labels:     map[string]string{},
	}
	// compose-go leaves the context relative when it cannot resolve it; make it
	// absolute so downstream file walks are stable.
	if t.Context != "" && !filepath.IsAbs(t.Context) {
		if abs, err := filepath.Abs(t.Context); err == nil {
			t.Context = abs
		}
	}
	for k, v := range b.Args {
		if v != nil {
			t.Args[k] = *v
		}
	}
	for k, v := range b.Labels {
		t.Labels[k] = v
	}
	// Tags: the service image is the canonical name; build.tags add more.
	if svc.Image != "" {
		t.Tags = append(t.Tags, svc.Image)
	}
	t.Tags = append(t.Tags, []string(b.Tags)...)
	if len(t.Tags) == 0 {
		t.Tags = []string{name + ":latest"}
	}
	return t
}
