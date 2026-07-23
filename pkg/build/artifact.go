package build

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// NamedImage pairs a built image with the reference it was tagged as inside an
// artifact (OCI layout dir or docker tar).
type NamedImage struct {
	Ref   string // "org.opencontainers.image.ref.name" / RepoTag, may be ""
	Image v1.Image
}

// ImageInfo is the human-facing summary of one image in an artifact.
type ImageInfo struct {
	Ref          string
	Digest       string
	Size         int64 // total size of config + compressed layers
	OS           string
	Architecture string
	Layers       int
	Env          []string
	Entrypoint   []string
	Cmd          []string
	WorkingDir   string
	User         string
	ExposedPorts []string
	Labels       map[string]string
}

// LoadArtifact reads a built image artifact from disk. It accepts both an OCI
// image layout directory and a single image tar (docker-save or OCI-layout
// tar), returning every image it contains with its reference.
func LoadArtifact(path string) ([]NamedImage, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return loadOCILayout(path)
	}
	return loadTar(path)
}

func loadOCILayout(dir string) ([]NamedImage, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return nil, fmt.Errorf("not an OCI layout %q: %w", dir, err)
	}
	idx, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	var out []NamedImage
	for _, desc := range m.Manifests {
		img, err := idx.Image(desc.Digest)
		if err != nil {
			return nil, err
		}
		ref := desc.Annotations["org.opencontainers.image.ref.name"]
		out = append(out, NamedImage{Ref: ref, Image: img})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("OCI layout %q contains no images", dir)
	}
	return out, nil
}

func loadTar(path string) ([]NamedImage, error) {
	opener := func() (io.ReadCloser, error) { return os.Open(path) }
	mf, err := tarball.LoadManifest(opener)
	if err != nil {
		// Fall back to a single untagged image (e.g. OCI-format tar).
		img, ierr := tarball.ImageFromPath(path, nil)
		if ierr != nil {
			return nil, fmt.Errorf("read image tar %q: %w", path, err)
		}
		return []NamedImage{{Image: img}}, nil
	}
	var out []NamedImage
	for _, desc := range mf {
		ref := ""
		if len(desc.RepoTags) > 0 {
			ref = desc.RepoTags[0]
		}
		var tag *name.Tag
		if ref != "" {
			if t, err := name.NewTag(ref, name.WeakValidation); err == nil {
				tag = &t
			}
		}
		img, err := tarball.ImageFromPath(path, tag)
		if err != nil {
			return nil, err
		}
		out = append(out, NamedImage{Ref: ref, Image: img})
	}
	return out, nil
}

// Inspect summarizes every image in an artifact.
func Inspect(path string) ([]ImageInfo, error) {
	imgs, err := LoadArtifact(path)
	if err != nil {
		return nil, err
	}
	var infos []ImageInfo
	for _, ni := range imgs {
		info, err := imageInfo(ni)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func imageInfo(ni NamedImage) (ImageInfo, error) {
	dig, err := ni.Image.Digest()
	if err != nil {
		return ImageInfo{}, err
	}
	cf, err := ni.Image.ConfigFile()
	if err != nil {
		return ImageInfo{}, err
	}
	layers, err := ni.Image.Layers()
	if err != nil {
		return ImageInfo{}, err
	}
	size, _ := ni.Image.Size()
	for _, l := range layers {
		if s, err := l.Size(); err == nil {
			size += s
		}
	}
	var ports []string
	for p := range cf.Config.ExposedPorts {
		ports = append(ports, p)
	}
	return ImageInfo{
		Ref:          ni.Ref,
		Digest:       dig.String(),
		Size:         size,
		OS:           cf.OS,
		Architecture: cf.Architecture,
		Layers:       len(layers),
		Env:          cf.Config.Env,
		Entrypoint:   cf.Config.Entrypoint,
		Cmd:          cf.Config.Cmd,
		WorkingDir:   cf.Config.WorkingDir,
		User:         cf.Config.User,
		ExposedPorts: ports,
		Labels:       cf.Config.Labels,
	}, nil
}

// PushArtifact loads an artifact and pushes it to ref. When the artifact holds
// several images, the one whose ref matches (or the first) is pushed.
func PushArtifact(ctx context.Context, path, ref string) error {
	imgs, err := LoadArtifact(path)
	if err != nil {
		return err
	}
	img := imgs[0].Image
	for _, ni := range imgs {
		if ni.Ref == ref {
			img = ni.Image
			break
		}
	}
	// pushImage is defined in output.go (shared with the build path).
	return pushImage(ctx, img, ref)
}
