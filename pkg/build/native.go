package build

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

// buildNative assembles the image entirely in-process, with no container
// runtime: it pulls the base for the target platform, then replays every
// non-executing Dockerfile instruction as layer/config mutations.
func buildNative(ctx context.Context, opts Options, p *parsedDockerfile) (v1.Image, error) {
	stage, err := p.finalStage(opts.Target)
	if err != nil {
		return nil, err
	}

	osName, arch, variant := splitPlatform(opts.Platform)

	// Build-arg scope: meta ARGs (before FROM) with defaults, overridden by
	// --build-arg. Used to expand the FROM line and later ARG references.
	scope := newEnv()
	for _, a := range p.metaArgs {
		for _, kv := range a.Args {
			if kv.Value != nil {
				scope.set(kv.Key, *kv.Value)
			}
		}
	}
	for k, v := range opts.BuildArgs {
		scope.set(k, v)
	}
	lex := shell.NewLex(p.escape)

	baseName, _, err := lex.ProcessWord(stage.BaseName, scope)
	if err != nil {
		return nil, fmt.Errorf("expand FROM %q: %w", stage.BaseName, err)
	}

	var img v1.Image
	if strings.EqualFold(baseName, "scratch") {
		img = empty.Image
		fmt.Fprintf(opts.stdout(), "base: scratch (%s/%s)\n", osName, arch)
	} else {
		ref, err := name.ParseReference(baseName)
		if err != nil {
			return nil, fmt.Errorf("parse base %q: %w", baseName, err)
		}
		plat := v1.Platform{OS: osName, Architecture: arch, Variant: variant}
		fmt.Fprintf(opts.stdout(), "pulling base %s (%s)…\n", baseName, plat.String())
		img, err = remote.Image(ref,
			remote.WithContext(ctx),
			remote.WithPlatform(plat),
			remote.WithAuthFromKeychain(authn.DefaultKeychain))
		if err != nil {
			return nil, fmt.Errorf("pull base %s: %w", baseName, err)
		}
	}

	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture, cf.Variant = osName, arch, variant

	// Seed the env scope with the base image's ENV so later expansions see it.
	env := newEnv()
	for _, e := range cf.Config.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			env.set(k, v)
		}
	}
	// combined = build args layered under ENV (ENV wins at runtime references).
	expandEnv := func(word string) (string, error) {
		s, _, err := lex.ProcessWord(word, mergedEnv{args: scope, env: env})
		return s, err
	}

	workdir := cf.Config.WorkingDir
	if workdir == "" {
		workdir = "/"
	}

	for _, cmd := range stage.Commands {
		switch c := cmd.(type) {
		case *instructions.CopyCommand:
			layer, err := fileLayer(opts.ContextDir, workdir, c.SourcePaths, c.DestPath, c.Chmod, expandEnv)
			if err != nil {
				return nil, fmt.Errorf("COPY: %w", err)
			}
			if img, err = appendLayer(img, layer, opts, "COPY "+strings.Join(c.SourcePaths, " ")+" "+c.DestPath); err != nil {
				return nil, err
			}
		case *instructions.AddCommand:
			// ADD from a URL or with auto-extract is out of native scope; those
			// are classified to buildah earlier. Here ADD behaves like COPY.
			layer, err := fileLayer(opts.ContextDir, workdir, c.SourcePaths, c.DestPath, c.Chmod, expandEnv)
			if err != nil {
				return nil, fmt.Errorf("ADD: %w", err)
			}
			if img, err = appendLayer(img, layer, opts, "ADD "+strings.Join(c.SourcePaths, " ")+" "+c.DestPath); err != nil {
				return nil, err
			}
		case *instructions.EnvCommand:
			for _, kv := range c.Env {
				val, err := expandEnv(kv.Value)
				if err != nil {
					return nil, err
				}
				env.set(kv.Key, val)
				cf.Config.Env = upsertEnv(cf.Config.Env, kv.Key, val)
			}
		case *instructions.ArgCommand:
			for _, kv := range c.Args {
				if kv.Value != nil && !scope.has(kv.Key) {
					v, _ := expandEnv(*kv.Value)
					scope.set(kv.Key, v)
				}
			}
		case *instructions.WorkdirCommand:
			wd, err := expandEnv(c.Path)
			if err != nil {
				return nil, err
			}
			if path.IsAbs(wd) {
				workdir = path.Clean(wd)
			} else {
				workdir = path.Join(workdir, wd)
			}
			cf.Config.WorkingDir = workdir
		case *instructions.CmdCommand:
			cf.Config.Cmd = resolveCmdline(c.ShellDependantCmdLine, cf.Config.Shell)
		case *instructions.EntrypointCommand:
			cf.Config.Entrypoint = resolveCmdline(c.ShellDependantCmdLine, cf.Config.Shell)
		case *instructions.ExposeCommand:
			if cf.Config.ExposedPorts == nil {
				cf.Config.ExposedPorts = map[string]struct{}{}
			}
			for _, port := range c.Ports {
				p, err := expandEnv(port)
				if err != nil {
					return nil, err
				}
				cf.Config.ExposedPorts[normalizePort(p)] = struct{}{}
			}
		case *instructions.LabelCommand:
			if cf.Config.Labels == nil {
				cf.Config.Labels = map[string]string{}
			}
			for _, kv := range c.Labels {
				v, _ := expandEnv(kv.Value)
				cf.Config.Labels[kv.Key] = v
			}
		case *instructions.UserCommand:
			u, err := expandEnv(c.User)
			if err != nil {
				return nil, err
			}
			cf.Config.User = u
		case *instructions.VolumeCommand:
			if cf.Config.Volumes == nil {
				cf.Config.Volumes = map[string]struct{}{}
			}
			for _, vol := range c.Volumes {
				v, _ := expandEnv(vol)
				cf.Config.Volumes[v] = struct{}{}
			}
		case *instructions.StopSignalCommand:
			cf.Config.StopSignal = c.Signal
		case *instructions.ShellCommand:
			cf.Config.Shell = c.Shell
		case *instructions.HealthCheckCommand:
			// v1.Config has no Healthcheck field; skip with a note.
			fmt.Fprintln(opts.stderr(), "warning: HEALTHCHECK is not represented in the OCI config; skipped")
		default:
			// RUN / multi-stage were classified to buildah already.
		}
	}

	// Apply extra labels from --label / compose build.labels last so they win.
	if len(opts.Labels) > 0 {
		if cf.Config.Labels == nil {
			cf.Config.Labels = map[string]string{}
		}
		for k, v := range opts.Labels {
			cf.Config.Labels[k] = v
		}
	}

	// cf was copied from the base config before layers were appended, so its
	// RootFS/History still describe the base only. Adopt the layered image's
	// RootFS and History (updated by mutate.Append) so the config's diff_ids
	// match the actual layers — otherwise the image is not runnable.
	layered, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cf.RootFS = layered.RootFS
	cf.History = layered.History
	return mutate.ConfigFile(img, cf)
}

// appendLayer adds a layer and records a history entry so `docker history`
// shows the instruction, matching Docker's output.
func appendLayer(img v1.Image, layer v1.Layer, opts Options, createdBy string) (v1.Image, error) {
	if layer == nil {
		return img, nil
	}
	return mutate.Append(img, mutate.Addendum{
		Layer:   layer,
		History: v1.History{CreatedBy: createdBy, Comment: "orcinus build"},
	})
}

// fileLayer builds a tar layer that places the given context sources under dest
// inside the image. dest is resolved against workdir when relative. It mirrors
// Docker COPY semantics for the common cases: multiple sources into a
// directory, a single file into a named path, and directory trees.
func fileLayer(ctxDir, workdir string, sources []string, dest, chmod string, expand func(string) (string, error)) (v1.Layer, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("no sources")
	}
	destExpanded, err := expand(dest)
	if err != nil {
		return nil, err
	}
	// Resolve dest to an absolute, slash path inside the image.
	imgDest := destExpanded
	trailingSlash := strings.HasSuffix(imgDest, "/")
	if !path.IsAbs(imgDest) {
		imgDest = path.Join(workdir, imgDest)
	}
	imgDest = path.Clean(imgDest)

	var mode int64 = -1
	if chmod != "" {
		m, err := strconv.ParseInt(chmod, 8, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid --chmod %q: %w", chmod, err)
		}
		mode = m
	}

	// Expand source globs relative to the context.
	var srcPaths []string
	for _, s := range sources {
		se, err := expand(s)
		if err != nil {
			return nil, err
		}
		abs := filepath.Join(ctxDir, filepath.FromSlash(se))
		matches, err := filepath.Glob(abs)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("source %q not found in context", se)
		}
		srcPaths = append(srcPaths, matches...)
	}

	// Docker rule: if there are multiple sources, or dest ends in "/", dest is a
	// directory and each source keeps its base name inside it.
	destIsDir := trailingSlash || len(srcPaths) > 1
	if !destIsDir {
		if fi, err := os.Stat(srcPaths[0]); err == nil && fi.IsDir() {
			destIsDir = false // a single dir copies its *contents* into dest
		}
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	seenDirs := map[string]bool{}

	writeEntry := func(hostPath, targetPath string, info fs.FileInfo) error {
		targetPath = strings.TrimPrefix(path.Clean("/"+targetPath), "/")
		if err := ensureParents(tw, targetPath, seenDirs); err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = targetPath
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "", ""
		if info.IsDir() {
			hdr.Name += "/"
		}
		if mode >= 0 && !info.IsDir() {
			hdr.Mode = mode
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		f, err := os.Open(hostPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}

	for _, src := range srcPaths {
		fi, err := os.Lstat(src)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			// Copy the directory's contents. When dest is a dir and there are
			// multiple sources, nest under the source's base name.
			root := src
			base := ""
			if destIsDir {
				base = filepath.Base(src)
			}
			err := filepath.WalkDir(root, func(pth string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(root, pth)
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
				target := path.Join(imgDest, base, filepath.ToSlash(rel))
				return writeEntry(pth, target, info)
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		target := imgDest
		if destIsDir {
			target = path.Join(imgDest, filepath.Base(src))
		}
		if err := writeEntry(src, target, fi); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	})
}

// ensureParents writes directory headers for every missing ancestor of name so
// the layer tar is self-contained (Docker tolerates missing parents, but
// explicit dirs keep permissions predictable).
func ensureParents(tw *tar.Writer, name string, seen map[string]bool) error {
	dir := path.Dir(name)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = path.Join(cur, p)
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if err := tw.WriteHeader(&tar.Header{
			Name:     cur + "/",
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}); err != nil {
			return err
		}
	}
	return nil
}

// resolveCmdline converts a CMD/ENTRYPOINT instruction to its final argv,
// prepending the configured shell for the shell (non-exec) form.
func resolveCmdline(c instructions.ShellDependantCmdLine, shellCfg []string) []string {
	if !c.PrependShell {
		return append([]string(nil), c.CmdLine...)
	}
	sh := shellCfg
	if len(sh) == 0 {
		sh = []string{"/bin/sh", "-c"}
	}
	return append(append([]string(nil), sh...), strings.Join(c.CmdLine, " "))
}

// normalizePort turns "80" into "80/tcp" and leaves "80/udp" untouched.
func normalizePort(p string) string {
	if strings.Contains(p, "/") {
		return p
	}
	return p + "/tcp"
}

// upsertEnv sets KEY=VALUE in a docker-style env slice, replacing any existing
// entry for KEY and preserving order.
func upsertEnv(env []string, key, val string) []string {
	entry := key + "=" + val
	for i, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == key {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}

// splitPlatform parses "os/arch/variant"; empty parts default to the cluster
// target linux/amd64.
func splitPlatform(platform string) (os, arch, variant string) {
	os, arch, variant = "linux", "amd64", ""
	if platform == "" {
		return
	}
	parts := strings.Split(platform, "/")
	if len(parts) > 0 && parts[0] != "" {
		os = parts[0]
	}
	if len(parts) > 1 && parts[1] != "" {
		arch = parts[1]
	}
	if len(parts) > 2 {
		variant = parts[2]
	}
	return
}

// env is an ordered environment usable as a shell.EnvGetter.
type env struct {
	keys []string
	vals map[string]string
}

func newEnv() *env { return &env{vals: map[string]string{}} }

func (e *env) set(k, v string) {
	if _, ok := e.vals[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.vals[k] = v
}
func (e *env) has(k string) bool { _, ok := e.vals[k]; return ok }
func (e *env) Get(k string) (string, bool) {
	v, ok := e.vals[k]
	return v, ok
}
func (e *env) Keys() []string {
	out := append([]string(nil), e.keys...)
	sort.Strings(out)
	return out
}

// mergedEnv exposes build args and ENV as one EnvGetter, with ENV taking
// precedence over build args on key collisions.
type mergedEnv struct {
	args *env
	env  *env
}

func (m mergedEnv) Get(k string) (string, bool) {
	if v, ok := m.env.Get(k); ok {
		return v, true
	}
	return m.args.Get(k)
}
func (m mergedEnv) Keys() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range append(m.args.Keys(), m.env.Keys()...) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
