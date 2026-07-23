package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orcinustools/orcinus/pkg/build"
)

type buildOpts struct {
	files      []string // compose files (docker compose build mode)
	tags       []string
	dockerfile string
	buildArgs  []string
	labels     []string
	target     string
	platform   string
	pull       bool
	engine     string // auto|native|buildah
	isolation  string // auto|oci|chroot|rootless
	runtime    string // crun|runc|...
	profiles   []string

	outputDir string // OCI image layout dir
	tarPath   string // image tar archive
	tarFormat string // docker|oci
	push      []string
}

// newBuildCmd builds the image-build command. It is the canonical
// `orcinus image build` and is also registered as the top-level `orcinus build`
// shortcut — a fresh instance per call, since a cobra command has one parent.
func newBuildCmd() *cobra.Command {
	o := &buildOpts{}
	cmd := &cobra.Command{
		Use:   "build [CONTEXT]",
		Short: "Build OCI/Docker-compatible images without a Docker daemon (docs/BUILD.md)",
		Long: "Build container images the way `docker build` and `docker compose build` do,\n" +
			"but with no Docker runtime.\n\n" +
			"Two modes:\n" +
			"  • CONTEXT given → build one image from that directory's Dockerfile\n" +
			"      orcinus build ./app -t myapp:latest -o ./out\n" +
			"  • no CONTEXT → read `build:` from orcinus.yml / compose files (like\n" +
			"    `docker compose build`) and build every service that declares one\n" +
			"      orcinus build -f orcinus.yml --tar images.tar\n\n" +
			"Engines: 'native' assembles the image in-process (no runtime at all) and\n" +
			"handles FROM/COPY/ADD/ENV/WORKDIR/CMD/ENTRYPOINT/EXPOSE/LABEL/USER. RUN and\n" +
			"multi-stage need the 'buildah' engine (Linux; see docs/BUILD.md). 'auto'\n" +
			"(default) picks native when possible and buildah otherwise.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, o, args)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&o.files, "file", "f", nil, "compose file for `docker compose build` mode (repeatable)")
	f.StringArrayVarP(&o.tags, "tag", "t", nil, "image name and optional tag (repeatable), e.g. myapp:latest")
	f.StringVar(&o.dockerfile, "dockerfile", "", "path to the Dockerfile (default: <context>/Dockerfile)")
	f.StringArrayVar(&o.buildArgs, "build-arg", nil, "set a build-time variable KEY=VALUE (repeatable)")
	f.StringArrayVar(&o.labels, "label", nil, "set image metadata KEY=VALUE (repeatable)")
	f.StringVar(&o.target, "target", "", "target build stage")
	f.StringVar(&o.platform, "platform", "linux/amd64", "target platform os/arch[/variant]")
	f.BoolVar(&o.pull, "pull", false, "always attempt to pull a newer base image")
	f.StringVar(&o.engine, "engine", "auto", "build engine: auto|native|buildah")
	f.StringVar(&o.isolation, "isolation", "", "buildah RUN isolation: auto|oci|chroot|rootless (chroot needs no OCI runtime/network backend)")
	f.StringVar(&o.runtime, "runtime", "", "OCI runtime for RUN steps: crun|runc (default: buildah's choice)")
	f.StringArrayVar(&o.profiles, "profile", nil, "compose profile to activate (compose mode, repeatable)")

	f.StringVarP(&o.outputDir, "output", "o", "", "write an OCI image layout to this directory")
	f.StringVar(&o.tarPath, "tar", "", "write an image archive to this path (docker load-able)")
	f.StringVar(&o.tarFormat, "tar-format", "docker", "tar archive format: docker|oci")
	f.StringArrayVar(&o.push, "push", nil, "push the built image to this registry reference (repeatable)")
	return cmd
}

func runBuild(cmd *cobra.Command, o *buildOpts, args []string) error {
	if o.outputDir == "" && o.tarPath == "" && len(o.push) == 0 {
		return fmt.Errorf("no output selected: pass -o <dir> (OCI layout), --tar <file>, and/or --push <ref>")
	}
	buildArgs, err := parseKV(o.buildArgs)
	if err != nil {
		return fmt.Errorf("--build-arg: %w", err)
	}
	labels, err := parseKV(o.labels)
	if err != nil {
		return fmt.Errorf("--label: %w", err)
	}

	base := build.Options{
		Tags:         o.tags,
		Dockerfile:   o.dockerfile,
		BuildArgs:    buildArgs,
		Labels:       labels,
		Target:       o.target,
		Platform:     o.platform,
		Pull:         o.pull,
		ForceBuildah: o.engine == "buildah",
		Isolation:    o.isolation,
		Runtime:      o.runtime,
		Stdout:       cmd.OutOrStdout(),
		Stderr:       cmd.ErrOrStderr(),
	}
	if o.engine != "auto" && o.engine != "native" && o.engine != "buildah" {
		return fmt.Errorf("--engine must be auto|native|buildah, got %q", o.engine)
	}

	// docker-build mode: a positional context (or an explicit --dockerfile with
	// no compose files) builds a single image.
	if len(args) > 0 || (len(o.files) == 0 && (o.dockerfile != "" || len(o.tags) > 0)) {
		ctxDir := "."
		if len(args) > 0 {
			ctxDir = args[0]
		}
		opts := base
		opts.ContextDir = ctxDir
		opts.Output = build.Output{
			OCIDir:    o.outputDir,
			TarPath:   o.tarPath,
			TarFormat: o.tarFormat,
			Push:      o.push,
		}
		return build.Build(cmd.Context(), opts)
	}

	// compose-build mode: discover build: targets from compose files.
	files := o.files
	if len(files) == 0 {
		found, err := discoverDefaultFile()
		if err != nil {
			return fmt.Errorf("no CONTEXT and no compose file: %w", err)
		}
		files = []string{found}
		fmt.Fprintf(cmd.ErrOrStderr(), "using %s\n", found)
	}
	baseDir := "."
	if files[0] != "-" {
		baseDir = filepath.Dir(files[0])
	}
	targets, err := build.DiscoverTargets(cmd.Context(), files, baseDir, o.profiles, args)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no service declares a build: section in %s", strings.Join(files, ", "))
	}

	multi := len(targets) > 1
	for _, t := range targets {
		fmt.Fprintf(cmd.ErrOrStderr(), "==> building service %q (%s)\n", t.Service, strings.Join(t.Tags, ", "))
		opts := t.ToOptions(base)
		opts.Output = composeOutput(o, t, multi)
		if err := build.Build(cmd.Context(), opts); err != nil {
			return fmt.Errorf("service %q: %w", t.Service, err)
		}
	}
	return nil
}

// composeOutput derives per-service output paths. With multiple services, a
// single --tar / -o would collide, so each service gets a name-qualified path.
func composeOutput(o *buildOpts, t build.Target, multi bool) build.Output {
	out := build.Output{TarFormat: o.tarFormat, Push: o.push}
	out.OCIDir = o.outputDir
	out.TarPath = o.tarPath
	if multi {
		if o.outputDir != "" {
			out.OCIDir = filepath.Join(o.outputDir, t.Service)
		}
		if o.tarPath != "" {
			ext := filepath.Ext(o.tarPath)
			out.TarPath = strings.TrimSuffix(o.tarPath, ext) + "-" + t.Service + ext
		}
	}
	return out
}

// parseKV turns []{"K=V"} into a map, tolerating "K" (empty value) for
// --build-arg pass-through from the environment.
func parseKV(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok {
			// KEY with no value: inherit from the process environment.
			if ev, present := os.LookupEnv(k); present {
				m[k] = ev
				continue
			}
			return nil, fmt.Errorf("invalid entry %q (want KEY=VALUE)", it)
		}
		m[k] = v
	}
	return m, nil
}
